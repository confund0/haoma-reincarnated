package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/mirror"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/pair"
	"haoma-frontend/internal/session"
	"haoma-frontend/internal/signal"
	"haoma-frontend/internal/syncrail"
)

const deviceLinkBundleTimeout = 3 * time.Minute

const deviceLinkRegisterTimeout = 30 * time.Second

const (
	reasonNotReady        = ipc.ReasonNotReady
	reasonBadRequest      = ipc.ReasonBadRequest
	reasonAlreadyLinked   = "already_linked"
	reasonDeviceNotEmpty  = "device_not_empty"
	reasonPeerCheckFailed = "peer_check_failed"
	reasonBackendDown     = ipc.ReasonBackendDown
	reasonBuildFailed     = ipc.ReasonBuildFailed
	reasonWaitFailed      = "wait_failed"
	reasonBadPayload      = ipc.ReasonBadPayload
	reasonMyKeysMissing   = "mykeys_missing"
	reasonRegisterFailed  = "register_failed"
	reasonBundleSendFail  = "bundle_send_failed"
	reasonBundleTimeout   = "bundle_timeout"
	reasonBundleBadShape  = "bundle_bad_shape"
	reasonBeforeLink      = "arrived_before_link"
	reasonAdoptFailed     = "adopt_failed"
	reasonLinkRecordFail  = "link_record_failed"
	reasonNoPendingLink   = "no_pending_link"
	reasonEncodeFrame     = ipc.ReasonEncodeFrame
)

const (
	probeUnreachable = "unreachable"
	probeGaveUp      = "probe-timeout"
)

type deviceLinkWaitKey struct {
	d      *daemon
	peerID string
}

var deviceLinkWaiters = struct {
	mu sync.Mutex
	m  map[deviceLinkWaitKey]chan []byte
}{m: make(map[deviceLinkWaitKey]chan []byte)}

func registerDeviceLinkWaiter(d *daemon, peerID string) (<-chan []byte, func()) {
	ch := make(chan []byte, 1)
	k := deviceLinkWaitKey{d: d, peerID: peerID}
	deviceLinkWaiters.mu.Lock()
	deviceLinkWaiters.m[k] = ch
	deviceLinkWaiters.mu.Unlock()
	slog.Debug("devicelink: bundle waiter registered", slog.String("peer_id", peerID))
	return ch, func() {
		deviceLinkWaiters.mu.Lock()
		delete(deviceLinkWaiters.m, k)
		deviceLinkWaiters.mu.Unlock()

		select {
		case leftover := <-ch:
			zeroBytes(leftover)
		default:
		}
		slog.Debug("devicelink: bundle waiter released", slog.String("peer_id", peerID))
	}
}

func deviceLinkPending(d *daemon, peerID string) bool {
	if d == nil || peerID == "" {
		return false
	}
	deviceLinkWaiters.mu.Lock()
	_, ok := deviceLinkWaiters.m[deviceLinkWaitKey{d: d, peerID: peerID}]
	deviceLinkWaiters.mu.Unlock()
	return ok
}

func offerDeviceLinkBundle(d *daemon, peerID string, plaintext []byte) bool {
	deviceLinkWaiters.mu.Lock()
	ch, ok := deviceLinkWaiters.m[deviceLinkWaitKey{d: d, peerID: peerID}]
	deviceLinkWaiters.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- plaintext:
		return true
	default:

		return false
	}
}

func (sd *sessionDispatcher) handleDeviceLinkOffer(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("devicelink: handle offer")
	d := sd.d
	if d.signalState == nil || d.stores == nil || d.backendClient == nil || d.linkedDevices == nil {
		slog.Debug("devicelink: offer refused", slog.String("reason", reasonNotReady))
		sendError(sess, f.ID, reasonNotReady, "frontend wiring incomplete")
		return
	}

	if reason, detail, ok := d.deviceLinkOffererPrecheck(); !ok {
		slog.Info("devicelink: offer refused", slog.String("reason", reason))
		slog.Debug("devicelink: offer refusal detail",
			slog.String("reason", reason),
			slog.String("detail", detail),
		)
		sendError(sess, f.ID, reason, detail)
		return
	}

	minted, err := d.backendClient.MintOnion(ctx)
	if err != nil {
		slog.Warn("devicelink: offer failed", slog.String("reason", reasonBackendDown), slog.Any("err", err))
		sendError(sess, f.ID, reasonBackendDown, fmt.Sprintf("mint persistent onion: %v", err))
		return
	}
	if minted.Address == "" || minted.PrivateKey == "" {
		slog.Warn("devicelink: offer failed", slog.String("reason", reasonNotReady))
		sendError(sess, f.ID, reasonNotReady, "haomad returned empty mint")
		return
	}

	inv, mine, err := pair.Build(d.signalState, d.stores, []string{minted.Address}, "", nil)
	if err != nil {
		slog.Warn("devicelink: offer failed", slog.String("reason", reasonBuildFailed), slog.Any("err", err))
		sendError(sess, f.ID, reasonBuildFailed, err.Error())
		return
	}
	raw, err := inv.Marshal()
	if err != nil {
		slog.Warn("devicelink: offer failed", slog.String("reason", reasonBuildFailed), slog.Any("err", err))
		sendError(sess, f.ID, reasonBuildFailed, err.Error())
		return
	}

	startResp, err := d.backendClient.PairOnionInvite(ctx, raw, 0, false)
	if err != nil {

		slog.Warn("devicelink: offer failed", slog.String("reason", reasonBackendDown))
		sendError(sess, f.ID, reasonBackendDown, "the daemon did not publish a rendezvous")
		return
	}
	if err := pair.SaveMyKeys(d.store, startResp.HandleID, mine, minted); err != nil {
		slog.Warn("devicelink: stash offer keys failed",
			slog.String("handle_id", startResp.HandleID),
			slog.Any("err", err),
		)
	}
	slog.Info("devicelink: offer started",
		slog.String("handle_id", startResp.HandleID),
		slog.String("peer_id", mine.PeerID),
	)

	resp, err := ipc.NewFrame(ipc.FrameDeviceLinkStarted, f.ID, ipc.DeviceLinkStartedResponse{
		HandleID:  startResp.HandleID,
		Words:     startResp.Words,
		ExpiresAt: startResp.ExpiresAt,
	})
	if err != nil {
		sendError(sess, f.ID, reasonEncodeFrame, err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("devicelink: send started frame failed", slog.Any("err", err))
		return
	}

	go sd.runDeviceLinkWait(ctx, sess, startResp.HandleID)
}

func (sd *sessionDispatcher) runDeviceLinkWait(ctx context.Context, sess *ipc.Session, handleID string) {
	d := sd.d
	slog.Debug("devicelink: offer wait entered", slog.String("handle_id", handleID))

	reachCtx, stopReachWatch := context.WithCancel(ctx)
	defer stopReachWatch()
	go sd.watchDeviceLinkReach(reachCtx, sess, handleID)

	waitResp, err := d.backendClient.PairOnionWait(ctx, handleID)
	if err != nil {
		sd.pushDeviceLinkFailed(sess, handleID, reasonWaitFailed, "the wait ended without anyone arriving")
		return
	}
	slog.Debug("devicelink: offer wait returned",
		slog.String("handle_id", handleID),
		slog.Int("joiner_payload_bytes", len(waitResp.JoinerPayload)),
	)

	inv, err := pair.Parse(waitResp.JoinerPayload)
	if err != nil {
		sd.pushDeviceLinkFailed(sess, handleID, reasonBadPayload, err.Error())
		return
	}
	mine, minted, err := pair.LoadMyKeys(d.store, handleID)
	if err != nil {
		sd.pushDeviceLinkFailed(sess, handleID, reasonMyKeysMissing, err.Error())
		return
	}

	regCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if err := pair.Import(regCtx, d.stores, d.backendClient, inv, mine, minted); err != nil {
		slog.Warn("devicelink: offer register failed",
			slog.String("handle_id", handleID),
			slog.String("peer_id", inv.PeerID),
			slog.String("reason", reasonRegisterFailed),
			slog.Any("err", err),
		)
		sd.pushDeviceLinkFailed(sess, handleID, reasonRegisterFailed, err.Error())
		return
	}
	if err := pair.DeleteMyKeys(d.store, handleID); err != nil {
		slog.Debug("devicelink: delete offer keys failed",
			slog.String("handle_id", handleID),
			slog.Any("err", err),
		)
	}
	slog.Debug("devicelink: offer registered joiner and established session",
		slog.String("handle_id", handleID),
		slog.String("peer_id", inv.PeerID),
	)

	if err := d.sendIdentityBundle(ctx, inv.PeerID); err != nil {
		slog.Warn("devicelink: offer bundle ship failed",
			slog.String("handle_id", handleID),
			slog.String("peer_id", inv.PeerID),
			slog.String("reason", reasonBundleSendFail),
			slog.Any("err", err),
		)
		sd.pushDeviceLinkFailed(sess, handleID, reasonBundleSendFail, err.Error())
		return
	}

	changed, err := d.linkedDevices.Link(inv.PeerID, time.Now().Unix())
	if err != nil {
		slog.Error("devicelink: offer link record failed",
			slog.String("handle_id", handleID),
			slog.String("peer_id", inv.PeerID),
			slog.String("reason", reasonLinkRecordFail),
			slog.Any("err", err),
		)
		sd.pushDeviceLinkFailed(sess, handleID, reasonLinkRecordFail, err.Error())
		return
	}
	slog.Info("devicelink: offer completed — device recorded as mine",
		slog.String("handle_id", handleID),
		slog.String("peer_id", inv.PeerID),
		slog.Bool("membership_changed", changed),
	)
	pushLinkedDeviceRow(ctx, d, inv.PeerID)

	var send func(ipc.Frame) error
	if sess != nil {
		send = sess.Send
	}
	announceDeviceLinkCompleted(d, send, handleID, inv.PeerID)
}

func announceDeviceLinkCompleted(d *daemon, send func(ipc.Frame) error, handleID, peerID string) {
	completed, err := ipc.NewFrame(ipc.FrameDeviceLinkCompleted, "", ipc.DeviceLinkCompletedPush{
		HandleID: handleID,
		PeerID:   peerID,
	})
	switch {
	case err != nil:
		slog.Warn("devicelink: encode completed push failed",
			slog.String("handle_id", handleID),
			slog.Any("err", err),
		)
	case send == nil:
		slog.Debug("devicelink: completed push not sent — no session",
			slog.String("handle_id", handleID),
		)
	default:
		if err := send(completed); err != nil {
			slog.Warn("devicelink: send completed push failed",
				slog.String("handle_id", handleID),
				slog.Any("err", err),
			)
		} else {
			slog.Debug("devicelink: completed push sent", slog.String("handle_id", handleID))
		}
	}

	pushInheritedContactsChanged(d, nil, "device_linked")
}

func (sd *sessionDispatcher) watchDeviceLinkReach(ctx context.Context, sess *ipc.Session, handleID string) {
	d := sd.d
	if d.backendClient == nil || handleID == "" {
		return
	}
	slog.Debug("devicelink: reachability watch started", slog.String("handle_id", handleID))

	_, err := awaitRendezvousReady(ctx, d, handleID, func(p backendapi.PairOnionProbe) {

		slog.Debug("devicelink: reachability attempt observed",
			slog.String("handle_id", handleID),
			slog.Int("attempt", p.Attempt),
			slog.Bool("ready", p.Ready),
			slog.String("reach", reachToken(p.Error)),
		)
		push := ipc.DeviceLinkProbePush{
			HandleID: handleID,
			Attempt:  p.Attempt,
			Ready:    p.Ready,
			Error:    reachToken(p.Error),
			At:       p.At,
		}
		if p.Ready {
			slog.Info("devicelink: rendezvous now reachable — the other device may join",
				slog.String("handle_id", handleID),
				slog.Int("attempt", p.Attempt),
				slog.Bool("verified", push.Error == ""),
			)
		}
		frame, err := ipc.NewFrame(ipc.FrameDeviceLinkProbe, "", push)
		if err != nil {
			slog.Warn("devicelink: encode reachability push failed", slog.Any("err", err))
			return
		}
		if sess != nil {
			if err := sess.Send(frame); err != nil {
				slog.Warn("devicelink: send reachability push failed",
					slog.String("handle_id", handleID),
					slog.Any("err", err),
				)
			}
		}
		if p.Ready {

			slog.Debug("devicelink: reachability watch satisfied — stopping",
				slog.String("handle_id", handleID),
			)
		}
	})
	slog.Debug("devicelink: reachability watch ended",
		slog.String("handle_id", handleID),
		slog.Any("err", err),
	)
}

func reachToken(raw string) string {
	switch raw {
	case "":
		return ""
	case probeGaveUp:
		return probeGaveUp
	default:
		return probeUnreachable
	}
}

func (d *daemon) sendIdentityBundle(ctx context.Context, peerID string) error {
	if d.cipher == nil || d.peerSeq == nil || d.backendClient == nil || d.stores == nil {
		return errors.New("devicelink: frontend wiring incomplete")
	}
	kp := d.stores.GetIdentityKeyPair()
	if kp == nil {
		return errors.New("devicelink: no identity keypair loaded")
	}
	seq, err := d.peerSeq.NextSendSeq(peerID)
	if err != nil {
		return fmt.Errorf("seq: %w", err)
	}
	msgID, err := msg.NewID()
	if err != nil {
		return fmt.Errorf("msg id: %w", err)
	}

	selfNick, selfNickAt, hasNick := d.selfNickToSend()
	if !hasNick {
		slog.Debug("devicelink: this device is still on the placeholder self-nick; the identity bundle has none",
			slog.String("peer_id", peerID),
		)
	} else {
		slog.Debug("devicelink: the identity bundle will hold this device's self-nick",
			slog.String("peer_id", peerID),
			slog.Int64("nick_at", selfNickAt),
		)
	}

	privArr := kp.PrivateKey().Serialize()
	priv := make([]byte, len(privArr))
	copy(priv, privArr[:])
	wrapper, err := msg.BuildDeviceLinkIdentity(seq, time.Now().Unix(), msgID, kp.PublicKey().Serialize(), priv, selfNick, selfNickAt)
	zeroBytes(priv)
	if err != nil {
		return fmt.Errorf("build identity bundle: %w", err)
	}

	defer zeroBytes(wrapper.Body)
	plaintext, err := msg.Marshal(wrapper)
	if err != nil {
		return fmt.Errorf("marshal identity bundle: %w", err)
	}
	defer zeroBytes(plaintext)

	blob, err := d.cipher.Encrypt(ctx, peerID, plaintext)
	if err != nil {
		return fmt.Errorf("encrypt identity bundle: %w", err)
	}

	res, err := mirror.Dispatch(ctx, d.backendClient, mirror.NewRail(d.railKey, d.siblings, d.peerMeta),
		mirror.SingleTarget(peerID),
		func(ctx context.Context, deviceID string) (backendapi.SendRequest, []byte, error) {

			return backendapi.SendRequest{PeerID: deviceID, Payload: blob}, nil, nil
		},
		mirror.NewOp(wrapper.Kind, ""))
	if err != nil {
		return fmt.Errorf("backend send: %w", err)
	}

	noteIdentityBundleQueued(d, peerID, res.Primary.EnvelopeID)
	slog.Debug("devicelink: identity bundle shipped",
		slog.String("peer_id", peerID),
		slog.String("msg_id", msgID),
		slog.String("envelope_id", res.Primary.EnvelopeID),
		slog.Uint64("seq", seq),
		slog.Bool("carries_self_nick", selfNick != ""),
	)
	return nil
}

func (sd *sessionDispatcher) pushDeviceLinkFailed(sess *ipc.Session, handleID, reason, detail string) {
	detail = clientDetail(reason, detail)
	slog.Info("devicelink: failed",
		slog.String("handle_id", handleID),
		slog.String("reason", reason),
	)
	slog.Debug("devicelink: failure detail",
		slog.String("handle_id", handleID),
		slog.String("reason", reason),
		slog.String("detail", detail),
	)
	frame, err := ipc.NewFrame(ipc.FrameDeviceLinkFailed, "", ipc.DeviceLinkFailedPush{
		HandleID: handleID,
		Reason:   reason,
		Detail:   detail,
	})
	if err != nil {
		slog.Warn("devicelink: encode failed push", slog.Any("err", err))
		return
	}
	if sess != nil {
		if err := sess.Send(frame); err != nil {
			slog.Warn("devicelink: send failed push", slog.Any("err", err))
		}
	}
}

func (sd *sessionDispatcher) handleDeviceLinkAccept(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("devicelink: handle accept")
	d := sd.d
	var req ipc.DeviceLinkAcceptRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, reasonBadRequest, fmt.Sprintf("decode payload: %v", err))
		return
	}
	if len(req.Words) == 0 {
		sendError(sess, f.ID, reasonBadRequest, "words required")
		return
	}
	if d.signalState == nil || d.stores == nil || d.backendClient == nil || d.linkedDevices == nil || d.chats == nil {
		slog.Debug("devicelink: accept refused", slog.String("reason", reasonNotReady))
		sendError(sess, f.ID, reasonNotReady, "frontend wiring incomplete")
		return
	}

	if reason, detail, ok := d.deviceLinkJoinerPrecheck(ctx); !ok {
		slog.Info("devicelink: accept refused", slog.String("reason", reason))
		slog.Debug("devicelink: accept refusal detail",
			slog.String("reason", reason),
			slog.String("detail", detail),
		)
		sendError(sess, f.ID, reason, detail)
		return
	}

	minted, err := d.backendClient.MintOnion(ctx)
	if err != nil {
		slog.Warn("devicelink: accept failed", slog.String("reason", reasonBackendDown), slog.Any("err", err))
		sendError(sess, f.ID, reasonBackendDown, fmt.Sprintf("mint persistent onion: %v", err))
		return
	}
	if minted.Address == "" || minted.PrivateKey == "" {
		sendError(sess, f.ID, reasonNotReady, "haomad returned empty mint")
		return
	}
	joinerInv, mine, err := pair.Build(d.signalState, d.stores, []string{minted.Address}, "", nil)
	if err != nil {
		slog.Warn("devicelink: accept failed", slog.String("reason", reasonBuildFailed), slog.Any("err", err))
		releaseJoinOnion(d, slog.Default(), minted.Address)
		sendError(sess, f.ID, reasonBuildFailed, err.Error())
		return
	}
	joinerBytes, err := joinerInv.Marshal()
	if err != nil {
		releaseJoinOnion(d, slog.Default(), minted.Address)
		sendError(sess, f.ID, reasonBuildFailed, err.Error())
		return
	}

	slog.Debug("devicelink: accept dialing rendezvous")
	acceptResp, err := d.backendClient.PairOnionAccept(ctx, req.Words, "", joinerBytes)
	if err != nil {

		reason := joinRefusal(err, reasonBackendDown)
		slog.Warn("devicelink: accept failed", slog.String("reason", reason))
		releaseJoinOnion(d, slog.Default(), minted.Address)
		sendError(sess, f.ID, reason, "the rendezvous did not answer")
		return
	}
	offerInv, err := pair.Parse(acceptResp.InviterPayload)
	if err != nil {
		slog.Warn("devicelink: accept failed", slog.String("reason", reasonBadPayload), slog.Any("err", err))
		releaseJoinOnion(d, slog.Default(), minted.Address)
		sendError(sess, f.ID, reasonBadPayload, err.Error())
		return
	}

	bundleCh, releaseWaiter := registerDeviceLinkWaiter(d, offerInv.PeerID)
	defer releaseWaiter()

	regCtx, cancel := context.WithTimeout(ctx, deviceLinkRegisterTimeout)

	err = pair.ImportResponder(regCtx, d.stores, d.backendClient, offerInv, mine, minted)
	cancel()
	if err != nil {
		slog.Warn("devicelink: accept register failed",
			slog.String("peer_id", offerInv.PeerID),
			slog.String("reason", reasonRegisterFailed),
			slog.Any("err", err),
		)
		sendError(sess, f.ID, reasonRegisterFailed, err.Error())
		return
	}
	slog.Debug("devicelink: accept registered offering device; awaiting identity bundle",
		slog.String("peer_id", offerInv.PeerID),
	)

	var plaintext []byte
	select {
	case plaintext = <-bundleCh:
	case <-ctx.Done():
		slog.Info("devicelink: accept aborted",
			slog.String("peer_id", offerInv.PeerID),
			slog.String("reason", reasonBundleTimeout),
		)
		sendError(sess, f.ID, reasonBundleTimeout, "session closed before the identity bundle arrived")
		return
	case <-time.After(deviceLinkBundleTimeout):
		slog.Info("devicelink: accept timed out",
			slog.String("peer_id", offerInv.PeerID),
			slog.String("reason", reasonBundleTimeout),
		)
		sendError(sess, f.ID, reasonBundleTimeout, "no identity bundle arrived before the deadline")
		return
	}

	fingerprint, reason, err := d.adoptIdentityBundle(plaintext)
	if err != nil {
		slog.Warn("devicelink: accept adoption failed",
			slog.String("peer_id", offerInv.PeerID),
			slog.String("reason", reason),
			slog.Any("err", err),
		)
		sendError(sess, f.ID, reason, err.Error())
		return
	}

	changed, err := d.linkedDevices.Link(offerInv.PeerID, time.Now().Unix())
	if err != nil {
		slog.Error("devicelink: accept link record failed",
			slog.String("peer_id", offerInv.PeerID),
			slog.String("reason", reasonLinkRecordFail),
			slog.Any("err", err),
		)
		sendError(sess, f.ID, reasonLinkRecordFail, err.Error())
		return
	}
	slog.Info("devicelink: accept completed — identity adopted, device recorded as mine",
		slog.String("peer_id", offerInv.PeerID),
		slog.String("fingerprint", fingerprint),
		slog.Bool("membership_changed", changed),
	)

	go askBackfill(context.Background(), sd.d, offerInv.PeerID, syncrail.SectionContacts, "", false)

	resp, err := ipc.NewFrame(ipc.FrameDeviceLinkAccepted, f.ID, ipc.DeviceLinkAcceptedResponse{
		PeerID:             offerInv.PeerID,
		AdoptedFingerprint: fingerprint,
	})
	if err != nil {
		sendError(sess, f.ID, reasonEncodeFrame, err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("devicelink: send accepted frame failed", slog.Any("err", err))
	}

	pushLinkedDeviceRow(ctx, sd.d, offerInv.PeerID)

	pushInheritedContactsChanged(sd.d, nil, "device_linked")
}

func pushLinkedDeviceRow(ctx context.Context, d *daemon, peerID string) {
	if d == nil || d.ipcSrv == nil || peerID == "" {
		return
	}
	entry, err := refreshedPeerEntry(ctx, d, peerID)
	if err != nil {
		slog.Warn("devicelink: the linked device's row was not pushed — the row could not be built",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return
	}
	push(d.ipcSrv, ipc.FramePeerUpdated, "", ipc.PeerUpdatedPayload{Peer: entry})
	slog.Debug("devicelink: the linked device's row pushed",
		slog.String("peer_id", peerID),
		slog.Bool("sibling", entry.Sibling),
	)
}

func (d *daemon) deviceLinkOffererPrecheck() (reason, detail string, ok bool) {
	members, err := d.linkedDevices.Members()
	if err != nil {
		return reasonPeerCheckFailed, fmt.Sprintf("read linked-device set: %v", err), false
	}
	if len(members) > 0 {
		return reasonAlreadyLinked,
			fmt.Sprintf("this device already has %d linked device(s)", len(members)), false
	}
	return "", "", true
}

func (d *daemon) deviceLinkJoinerPrecheck(ctx context.Context) (reason, detail string, ok bool) {
	if members, err := d.linkedDevices.Members(); err != nil {
		return reasonPeerCheckFailed, fmt.Sprintf("read linked-device set: %v", err), false
	} else if len(members) > 0 {
		return reasonAlreadyLinked, fmt.Sprintf("this device already has %d linked device(s)", len(members)), false
	}

	chats, err := d.chats.List()
	if err != nil {
		return reasonPeerCheckFailed, fmt.Sprintf("read chats: %v", err), false
	}
	if len(chats) > 0 {
		return reasonDeviceNotEmpty, fmt.Sprintf("this device holds %d chat(s); a joining device must be empty", len(chats)), false
	}

	peers, err := d.backendClient.Peers(ctx)
	if err != nil {

		return reasonPeerCheckFailed, fmt.Sprintf("read peer registry: %v", err), false
	}
	live := 0
	for _, p := range peers.Peers {
		if p.RetiredAt == 0 {
			live++
		}
	}
	if live > 0 {
		return reasonDeviceNotEmpty, fmt.Sprintf("this device holds %d contact(s); a joining device must be empty", live), false
	}

	slog.Debug("devicelink: joiner precheck passed (device is empty)")
	return "", "", true
}

func (d *daemon) adoptIdentityBundle(plaintext []byte) (fingerprint, reason string, err error) {
	defer zeroBytes(plaintext)

	wrapper, err := msg.Unmarshal(plaintext)
	if err != nil {
		return "", reasonBundleBadShape, fmt.Errorf("decode identity bundle wrapper: %w", err)
	}
	defer zeroBytes(wrapper.Body)
	body, err := wrapper.DeviceLinkIdentity()
	if err != nil {
		return "", reasonBundleBadShape, fmt.Errorf("decode identity bundle: %w", err)
	}
	defer body.Zero()
	slog.Debug("devicelink: identity bundle decoded on the joining device",
		slog.Bool("carries_self_nick", body.SelfNick != ""),
	)

	if err := signal.AdoptIdentity(d.store, d.signalState, body.Public, body.Private); err != nil {
		return "", reasonAdoptFailed, err
	}
	fp := d.signalState.IdentityKeyPair.PublicKey().Fingerprint()
	slog.Debug("devicelink: identity adopted on the joining device",
		slog.String("fingerprint", fp),
	)

	d.adoptSelfNickFromBundle(body.SelfNick, body.SelfNickAt)
	return fp, "", nil
}

func processDeviceLinkInbound(ctx context.Context, d *daemon, entry backendapi.InboxEntry) {
	slog.Debug("devicelink: inbound envelope observed while a link is pending",
		slog.String("peer_id", entry.PeerID),
		slog.String("envelope_id", entry.Envelope.ID),
	)
	plain, err := d.cipher.Decrypt(ctx, entry.PeerID, entry.Envelope.Payload)
	if errors.Is(err, session.ErrUnknownType) {

		slog.Info("devicelink: an envelope arrived before the link finished and was dropped; the sync will send it again",
			slog.String("peer_id", entry.PeerID),
			slog.String("envelope_id", entry.Envelope.ID),
			slog.String("reason", reasonBeforeLink),
			slog.Int("payload_bytes", len(entry.Envelope.Payload)),
		)
		return
	}
	if err != nil {
		slog.Warn("devicelink: inbound decrypt failed",
			slog.String("peer_id", entry.PeerID),
			slog.String("envelope_id", entry.Envelope.ID),
			slog.String("reason", reasonBundleBadShape),
			slog.Any("err", err),
		)
		return
	}
	wrapper, err := msg.Unmarshal(plain)
	if err != nil {
		zeroBytes(plain)
		slog.Warn("devicelink: inbound wrapper parse failed",
			slog.String("peer_id", entry.PeerID),
			slog.String("reason", reasonBundleBadShape),
			slog.Any("err", err),
		)
		return
	}
	if wrapper.Kind != msg.KindDeviceLinkIdentity {
		zeroBytes(plain)

		slog.Debug("devicelink: inbound non-bundle kind dropped during link",
			slog.String("peer_id", entry.PeerID),
			slog.String("kind", string(wrapper.Kind)),
		)
		return
	}
	if !offerDeviceLinkBundle(d, entry.PeerID, plain) {
		zeroBytes(plain)
		slog.Warn("devicelink: identity bundle dropped",
			slog.String("peer_id", entry.PeerID),
			slog.String("reason", reasonNoPendingLink),
		)
		return
	}
	slog.Debug("devicelink: identity bundle handed to the waiting accept — state change pending adoption",
		slog.String("peer_id", entry.PeerID),
	)
}

func (sd *sessionDispatcher) handleDeviceLinksList(_ context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("devicelink: handle list")
	d := sd.d
	out := ipc.DeviceLinksListedResponse{Devices: []ipc.DeviceLinkEntry{}}
	if d.linkedDevices != nil {
		set, err := d.linkedDevices.Get()
		if err != nil {
			slog.Error("devicelink: list read failed", slog.Any("err", err))
			sendError(sess, f.ID, reasonNotReady, err.Error())
			return
		}
		for _, peerID := range set.Members() {
			out.Devices = append(out.Devices, ipc.DeviceLinkEntry{
				PeerID:   peerID,
				LinkedAt: set.Added[peerID],
			})
		}
	}
	slog.Debug("devicelink: list answered", slog.Int("device_count", len(out.Devices)))
	resp, err := ipc.NewFrame(ipc.FrameDeviceLinksListed, f.ID, out)
	if err != nil {
		sendError(sess, f.ID, reasonEncodeFrame, err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("devicelink: send listed frame failed", slog.Any("err", err))
	}
}

func (sd *sessionDispatcher) handleDeviceLinkCancel(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("devicelink: handle cancel")
	var req ipc.DeviceLinkCancelRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, reasonBadRequest, fmt.Sprintf("decode payload: %v", err))
		return
	}
	if req.HandleID == "" {
		sendError(sess, f.ID, reasonBadRequest, "handle_id required")
		return
	}
	if sd.d.backendClient == nil {
		sendError(sess, f.ID, reasonNotReady, "frontend wiring incomplete")
		return
	}
	if err := sd.d.backendClient.PairOnionCancel(ctx, req.HandleID); err != nil {
		slog.Warn("devicelink: cancel failed",
			slog.String("handle_id", req.HandleID),
			slog.String("reason", reasonBackendDown),
			slog.Any("err", err),
		)
		sendError(sess, f.ID, reasonBackendDown, err.Error())
		return
	}
	slog.Info("devicelink: offer cancelled", slog.String("handle_id", req.HandleID))
	resp, err := ipc.NewFrame(ipc.FrameDeviceLinkCancelled, f.ID, nil)
	if err != nil {
		sendError(sess, f.ID, reasonEncodeFrame, err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("devicelink: send cancelled frame failed", slog.Any("err", err))
	}
}
