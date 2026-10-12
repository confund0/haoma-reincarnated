package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"go.mau.fi/libsignal/protocol"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/files"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/mirror"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/pair"
	"haoma-frontend/internal/peerstate"
	"haoma-frontend/internal/session"
	"haoma-frontend/internal/signal"
	"haoma-frontend/internal/syncrail"
)

func peerEntryFor(d *daemon, p backendapi.Peer) (ipc.PeerEntry, error) {
	entry := ipc.PeerEntry{
		ID:            p.ID,
		ChatID:        resolveDirectChatID(d, p.ID),
		LastActiveAt:  p.LastActiveAt,
		LastPassiveAt: p.LastPassiveAt,
		RetiredAt:     p.RetiredAt,
		Unreachable:   p.Unreachable,
	}
	var meta peerstate.MetaRecord
	if d != nil && d.peerMeta != nil {
		if rec, err := d.peerMeta.Get(p.ID); err == nil {
			meta = rec
		} else {
			slog.Warn("peerMeta.Get failed",
				slog.String("peer_id", p.ID),
				slog.Any("err", err),
			)
		}
	}
	entry.Nick = meta.Nick
	entry.Alias = meta.Alias
	entry.Label = peerstate.Resolve(meta, p.ID)
	sibling, err := d.readIsSibling(p.ID)
	if err != nil {
		slog.Error("peer row not built: the linked-device set could not be read",
			slog.String("peer_id", p.ID),
			slog.Any("err", err),
		)
		return ipc.PeerEntry{}, fmt.Errorf("read linked-device set: %w", err)
	}
	entry.Sibling = sibling

	entry.Fingerprint = meta.Fingerprint
	entry.Verified = meta.Verified
	entry.Blocked = meta.Blocked
	applyPresenceSnapshot(d, p.ID, &entry)
	return entry, nil
}

func refreshedPeerEntry(ctx context.Context, d *daemon, peerID string) (ipc.PeerEntry, error) {
	p := backendapi.Peer{ID: peerID}
	if d != nil && d.backendClient != nil {
		if got, err := d.backendClient.Peer(ctx, peerID); err == nil && got.ID != "" {
			p = got
		} else {
			slog.Debug("refreshed peer row: backend fetch failed; using stub",
				slog.String("peer_id", peerID),
				slog.Any("err", err),
			)
		}
	}
	return peerEntryFor(d, p)
}

func seedPairNick(d *daemon, peerID, nick string) {
	if d == nil || d.peerMeta == nil || peerID == "" || nick == "" {
		return
	}
	changed, err := d.peerMeta.SetNick(peerID, nick, 0)
	if err != nil {
		slog.Warn("peerMeta.SetNick (pair seed) failed",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return
	}
	slog.Debug("peer-meta nick seeded (pair handoff)",
		slog.String("peer_id", peerID),
		slog.Bool("changed", changed),
	)
	if changed {

		noteContactChangedForPeer(d, peerID)
	}
}

func seedPeerFingerprint(d *daemon, peerID string) {
	if d == nil || d.peerMeta == nil || d.stores == nil || peerID == "" {
		return
	}
	addr := protocol.NewSignalAddress(peerID, pair.DeviceID)
	remoteKey, err := d.stores.GetRemoteIdentity(addr)
	if err != nil {
		slog.Warn("peer-meta fingerprint seed skipped: remote identity unavailable",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return
	}
	fp := remoteKey.Fingerprint()
	changed, prev, err := d.peerMeta.SetFingerprint(peerID, fp)
	if err != nil {
		slog.Warn("peerMeta.SetFingerprint (pair seed) failed",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return
	}
	if prev != "" && prev != fp {
		slog.Warn("peer fingerprint changed on re-pair (possible identity rotation/mismatch)",
			slog.String("peer_id", peerID),
			slog.String("prev_fingerprint", prev),
			slog.String("new_fingerprint", fp),
		)
	}
	slog.Debug("peer-meta fingerprint seeded (pair handoff)",
		slog.String("peer_id", peerID),
		slog.String("fingerprint", fp),
		slog.Bool("changed", changed),
	)
}

func pushPeerPaired(ctx context.Context, d *daemon, peerID, source string) {
	if d == nil || peerID == "" {
		return
	}

	dropRetiredRoutes(ctx, d, peerID)

	d.sessionsChanged.fire()
	if d.ipcSrv == nil {
		return
	}
	push(d.ipcSrv, ipc.FramePeerPaired, "", ipc.PeerPairedPush{
		PeerID: peerID,
		Source: source,
	})
	pushPeerMetaUpdated(ctx, d, peerID)
	pushInheritedContactsChanged(d, nil, "paired_"+source)

	notePairingCompleted(d, peerID, source)
}

func pushPeerMetaUpdated(ctx context.Context, d *daemon, peerID string) {
	if d == nil || d.ipcSrv == nil || peerID == "" {
		return
	}
	pushPeerRowUpdated(ctx, d, peerID)

	if d.chats == nil {
		return
	}
	dc, err := d.chats.GetByDirectPeer(peerID)
	if err != nil {
		return
	}
	pushChatUpdated(d, dc)
}

func pushPeerRowUpdated(ctx context.Context, d *daemon, peerID string) {
	if d == nil || d.ipcSrv == nil || peerID == "" {
		return
	}
	if skipRowWhileLinking(d, peerID) {
		return
	}
	var pp backendapi.Peer
	if d.backendClient != nil {
		if got, err := d.backendClient.Peer(ctx, peerID); err == nil {
			pp = got
		} else if backendNotFound(err) {

			slog.Debug("peer.updated not pushed — haomad has no such pairing",
				slog.String("peer_id", peerID),
			)
			return
		} else {
			slog.Debug("pushPeerMetaUpdated: backend fetch failed; using stub",
				slog.String("peer_id", peerID),
				slog.Any("err", err),
			)
			pp = backendapi.Peer{ID: peerID}
		}
	} else {
		pp = backendapi.Peer{ID: peerID}
	}

	entry, err := peerEntryFor(d, pp)
	if err != nil {
		slog.Warn("peer.updated not pushed — the row could not be built",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return
	}
	push(d.ipcSrv, ipc.FramePeerUpdated, "", ipc.PeerUpdatedPayload{Peer: entry})
	slog.Debug("peer.updated pushed",
		slog.String("peer_id", peerID),
		slog.Bool("unreachable", entry.Unreachable),
	)
}

func (sd *sessionDispatcher) effectiveNick(reqNick string) string {
	if reqNick != "" {
		return reqNick
	}
	return sd.d.selfNick()
}

const pasteInPendingHandle = "paste_in_pending"

type sessionDispatcher struct {
	d *daemon
}

func newSessionDispatcher(d *daemon) *sessionDispatcher {
	return &sessionDispatcher{d: d}
}

func (sd *sessionDispatcher) run(ctx context.Context, sess *ipc.Session) {

	sd.pushInitialBackendStatus(ctx, sess)

	sd.pushInitialPresence(sess)

	pings := time.NewTicker(ipc.PingInterval)
	defer pings.Stop()

	reads := make(chan ipc.Frame, 4)
	readErrs := make(chan error, 1)
	go func() {
		for {
			f, err := sess.Recv()
			if err != nil {
				readErrs <- err
				return
			}
			select {
			case reads <- f:
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-readErrs:
			return
		case f := <-reads:

			fctx, place := takeSendPlace(ctx, sd.d, f)
			go func() {
				if place != nil {
					defer place.release()
				}
				sd.dispatch(fctx, sess, f)
			}()
		case <-pings.C:
			ping, _ := ipc.NewFrame(ipc.FramePing, "", nil)
			if err := sess.Send(ping); err != nil {
				return
			}
		}
	}
}

func (sd *sessionDispatcher) dispatch(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	switch f.Type {
	case ipc.FramePing:
		pong, _ := ipc.NewFrame(ipc.FramePong, f.ID, nil)
		_ = sess.Send(pong)
	case ipc.FramePong:

	case ipc.FrameInviteCreate:
		sd.handleInviteCreate(ctx, sess, f)
	case ipc.FrameInviteAccept:
		sd.handleInviteAccept(ctx, sess, f)
	case ipc.FrameSendText:
		sd.handleSendText(ctx, sess, f)
	case ipc.FrameSendFile:
		sd.handleSendFile(ctx, sess, f)
	case ipc.FrameSendEdit:
		sd.handleSendEdit(ctx, sess, f)
	case ipc.FrameSendDelete:
		sd.handleSendDelete(ctx, sess, f)
	case ipc.FrameSendReaction:
		sd.handleSendReaction(ctx, sess, f)
	case ipc.FrameTorInfo:
		sd.handleTorInfo(ctx, sess, f)
	case ipc.FrameSystemInfo:
		sd.handleSystemInfo(ctx, sess, f)
	case ipc.FrameListPeers:
		sd.handleListPeers(ctx, sess, f)
	case ipc.FrameListInheritedContacts:
		sd.handleListInheritedContacts(ctx, sess, f)
	case ipc.FrameListTimeline:
		sd.handleListTimeline(ctx, sess, f)
	case ipc.FrameListFiles:
		sd.handleListFiles(ctx, sess, f)
	case ipc.FrameSaveFile:
		sd.handleSaveFile(ctx, sess, f)
	case ipc.FrameOpenFile:
		sd.handleOpenFile(ctx, sess, f)
	case ipc.FrameImageStream:
		sd.handleImageStream(ctx, sess, f)
	case ipc.FrameWipeOpenTransient:
		sd.handleWipeOpenTransient(ctx, sess, f)
	case ipc.FrameSetAlias:
		sd.handleSetAlias(ctx, sess, f)
	case ipc.FrameSetPeerVerified:
		sd.handleSetPeerVerified(ctx, sess, f)
	case ipc.FramePeerAction:
		sd.handlePeerAction(ctx, sess, f)
	case ipc.FrameListChats:
		sd.handleListChats(ctx, sess, f)
	case ipc.FrameChatAction:
		sd.handleChatAction(ctx, sess, f)
	case ipc.FrameEnsureChat:
		sd.handleEnsureChat(ctx, sess, f)
	case ipc.FrameInspectEvent:
		sd.handleInspectEvent(ctx, sess, f)
	case ipc.FrameChatSearch:
		sd.handleChatSearch(ctx, sess, f)
	case ipc.FrameGetPeerFingerprint:
		sd.handleGetPeerFingerprint(sess, f)
	case ipc.FrameGetChatSettings:
		sd.handleGetChatSettings(ctx, sess, f)
	case ipc.FrameSetChatSettings:
		sd.handleSetChatSettings(ctx, sess, f)
	case ipc.FrameMarkRead:
		sd.handleMarkRead(ctx, sess, f)
	case ipc.FrameClientFocus:
		sd.handleClientFocus(ctx, sess, f)
	case ipc.FrameSetPresenceOverride:
		sd.handleSetPresenceOverride(ctx, sess, f)
	case ipc.FrameSetNick:
		sd.handleSetNick(ctx, sess, f)
	case ipc.FrameSetChatFontScale:
		sd.handleSetChatFontScale(ctx, sess, f)
	case ipc.FrameSetChatDefaults:
		sd.handleSetChatDefaults(ctx, sess, f)
	case ipc.FramePushPresence:
		sd.handlePushPresence(ctx, sess, f)
	case ipc.FrameInviteDHT:
		sd.handleInviteDHT(ctx, sess, f)
	case ipc.FrameAcceptDHT:
		sd.handleAcceptDHT(ctx, sess, f)
	case ipc.FrameCancelDHT:
		sd.handleCancelDHT(ctx, sess, f)
	case ipc.FramePairOnionInvite:
		sd.handlePairOnionInvite(ctx, sess, f)
	case ipc.FramePairOnionAccept:
		sd.handlePairOnionAccept(ctx, sess, f)
	case ipc.FramePairOnionCancel:
		sd.handlePairOnionCancel(ctx, sess, f)
	case ipc.FrameDeviceLinkOffer:
		sd.handleDeviceLinkOffer(ctx, sess, f)
	case ipc.FrameDeviceLinkAccept:
		sd.handleDeviceLinkAccept(ctx, sess, f)
	case ipc.FrameDeviceLinkCancel:
		sd.handleDeviceLinkCancel(ctx, sess, f)
	case ipc.FrameDeviceLinksList:
		sd.handleDeviceLinksList(ctx, sess, f)
	case ipc.FrameDeviceSync:
		sd.handleDeviceSync(ctx, sess, f)
	case ipc.FrameGetDestroyPIN:
		sd.handleGetDestroyPIN(ctx, sess, f)
	case ipc.FrameSetDestroyPIN:
		sd.handleSetDestroyPIN(ctx, sess, f)
	case ipc.FrameUnlinkDevice:
		sd.handleUnlinkDevice(ctx, sess, f)
	case ipc.FrameStopDestroyWaiting:
		sd.handleStopDestroyWaiting(ctx, sess, f)
	case ipc.FrameWipeDevice:
		sd.handleWipeDevice(ctx, sess, f)

	case ipc.FrameVerifyDestroyPIN:
		sd.handleVerifyDestroyPIN(ctx, sess, f)
	case ipc.FrameInvitationOffer:
		sd.handleInvitationOffer(ctx, sess, f)
	case ipc.FrameInvitationAccept:
		sd.handleInvitationAccept(ctx, sess, f)
	case ipc.FrameInvitationsList:
		sd.handleInvitationsList(ctx, sess, f)
	case ipc.FrameSubscribe:
		sd.handleSubscribe(sess, f)
	case ipc.FrameSetTorPassword:
		sd.handleSetTorPassword(ctx, sess, f)
	case ipc.FrameGetSettings:
		sd.handleGetSettings(sess, f)
	case ipc.FrameSyncSettings:
		sd.handleSyncSettings(sess, f)
	case ipc.FrameClientLockState:
		sd.handleClientLockState(sess, f)
	case ipc.FrameStartCall:
		sd.handleStartCall(ctx, sess, f)
	case ipc.FrameRespondCall:
		sd.handleRespondCall(ctx, sess, f)
	case ipc.FrameCallControl:
		sd.handleCallControl(ctx, sess, f)
	case ipc.FrameNewCircuitForPeer:
		sd.handleNewCircuitForPeer(ctx, sess, f)
	case ipc.FramePeerSelfProbe:
		sd.handlePeerSelfProbe(ctx, sess, f)
	case ipc.FrameExternalProbeBurst:
		sd.handleExternalProbeBurst(ctx, sess, f)
	case ipc.FrameNetworkChanged:
		sd.handleNetworkChanged(ctx, sess, f)
	case ipc.FrameRotateBegin:
		sd.handleRotateBegin(ctx, sess, f)
	case ipc.FrameRotateUserAccept:
		sd.handleRotateUserAccept(ctx, sess, f)
	case ipc.FrameRotateUserDecline:
		sd.handleRotateUserDecline(ctx, sess, f)
	case ipc.FrameRetryFiles:
		sd.handleRetryFiles(ctx, sess, f)
	default:
		sendError(sess, f.ID, "unsupported_frame", fmt.Sprintf("frame type %q is not handled in this protocol version", f.Type))
	}
}

func (sd *sessionDispatcher) handleSubscribe(sess *ipc.Session, f ipc.Frame) {
	var req ipc.SubscribeRequest
	if len(f.Payload) > 0 {
		if err := json.Unmarshal(f.Payload, &req); err != nil {
			sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
			return
		}
	}
	installed := sess.SetPushFilter(req.Topics)
	slog.Debug("subscribe filter installed",
		slog.String("client", sess.ClientName),
		slog.Any("topics", installed),
	)
	resp, err := ipc.NewFrame(ipc.FrameSubscribed, f.ID, ipc.SubscribedResponse{Topics: installed})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send subscribed reply failed", slog.Any("err", err))
	}
}

func (sd *sessionDispatcher) handleInviteCreate(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle invite_create")
	var req ipc.InviteCreateRequest
	if len(f.Payload) > 0 {
		if err := json.Unmarshal(f.Payload, &req); err != nil {
			sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
			return
		}
	}
	if sd.d.signalState == nil || sd.d.backendClient == nil {
		sendError(sess, f.ID, "not_ready", "frontend backend wiring incomplete (libsignal or backend client missing)")
		return
	}

	minted, err := sd.d.backendClient.MintOnion(ctx)
	if err != nil {
		sendError(sess, f.ID, "backend_unavailable", fmt.Sprintf("mint persistent onion: %v", err))
		return
	}
	if minted.Address == "" || minted.PrivateKey == "" {
		sendError(sess, f.ID, "not_ready", "haomad returned empty mint")
		return
	}

	inv, mine, err := pair.Build(sd.d.signalState, sd.d.stores, []string{minted.Address}, sd.effectiveNick(req.Nick), nil)
	if err != nil {
		sendError(sess, f.ID, "build_failed", err.Error())
		return
	}

	if err := pair.SaveMyKeys(sd.d.store, pasteInPendingHandle, mine, minted); err != nil {
		slog.Warn("stash invite mykeys failed",
			slog.String("peer_id", mine.PeerID),
			slog.Any("err", err),
		)
	}
	slog.Info("invite created",
		slog.String("peer_id", inv.PeerID),
	)

	raw, err := inv.Marshal()
	if err != nil {
		sendError(sess, f.ID, "encode_failed", err.Error())
		return
	}

	resp, err := ipc.NewFrame(ipc.FrameInviteCreated, f.ID, ipc.InviteCreatedResponse{
		InviteJSON: string(raw),
	})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send invite_created frame failed", slog.Any("err", err))
	}
}

func (sd *sessionDispatcher) handleInviteAccept(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle invite_accept")
	var req ipc.InviteAcceptRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	if req.InviteJSON == "" {
		sendError(sess, f.ID, "bad_request", "invite_json empty")
		return
	}
	if sd.d.stores == nil || sd.d.backendClient == nil {
		sendError(sess, f.ID, "not_ready", "frontend backend wiring incomplete (stores or backend client missing)")
		return
	}

	inv, err := pair.Parse([]byte(req.InviteJSON))
	if err != nil {
		sendError(sess, f.ID, "bad_invite", err.Error())
		return
	}
	mine, minted, err := pair.LoadMyKeys(sd.d.store, pasteInPendingHandle)
	if errors.Is(err, pair.ErrMyKeysNotFound) {
		sendError(sess, f.ID, "no_pending_invite", "run /invite-paste first; paste-in pairing needs MyKeys minted on this daemon")
		return
	}
	if err != nil {
		sendError(sess, f.ID, "mykeys_load", err.Error())
		return
	}
	if err := pair.Import(ctx, sd.d.stores, sd.d.backendClient, inv, mine, minted); err != nil {
		sendError(sess, f.ID, "import_failed", err.Error())
		return
	}
	seedPairNick(sd.d, inv.PeerID, inv.Frontend.Nick)
	seedPeerFingerprint(sd.d, inv.PeerID)
	if err := pair.DeleteMyKeys(sd.d.store, pasteInPendingHandle); err != nil {
		slog.Warn("delete pending mykeys failed", slog.Any("err", err))
	}

	if sd.d.chats != nil {
		if _, _, err := sd.d.createPairedChat(inv.PeerID, true); err != nil {
			slog.Warn("create direct chat after invite accept failed",
				slog.String("peer_id", inv.PeerID),
				slog.Any("err", err),
			)
		}
	}

	addr := protocol.NewSignalAddress(inv.PeerID, pair.DeviceID)
	remoteKey, err := sd.d.stores.GetRemoteIdentity(addr)
	fingerprint := ""
	if err == nil {
		fingerprint = remoteKey.Fingerprint()
	}

	resp, err := ipc.NewFrame(ipc.FrameInviteAccepted, f.ID, ipc.InviteAcceptedResponse{
		PeerID:              inv.PeerID,
		Nick:                inv.Frontend.Nick,
		IdentityFingerprint: fingerprint,
	})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	slog.Info("peer paired",
		slog.String("peer_id", inv.PeerID),
		slog.String("fingerprint", fingerprint),
	)
	if err := sess.Send(resp); err != nil {
		slog.Warn("send invite_accepted frame failed", slog.Any("err", err))
	}

	pushPeerPaired(ctx, sd.d, inv.PeerID, "paste-file")
}

func mirrorDispatchErrorCode(err error) string {
	var derr *mirror.DispatchError
	if errors.As(err, &derr) && derr.Stage == mirror.StagePrepare {
		return "encrypt_failed"
	}
	return "backend_send"
}

func (sd *sessionDispatcher) handleSendText(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle send_text")
	var req ipc.SendTextRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	if req.PeerID == "" {
		sendError(sess, f.ID, "bad_request", "peer_id empty")
		return
	}
	if req.Text == "" {
		sendError(sess, f.ID, "bad_request", "text empty")
		return
	}
	if sd.d.cipher == nil || sd.d.peerSeq == nil || sd.d.backendClient == nil || sd.d.chats == nil {
		sendError(sess, f.ID, "not_ready", "frontend wiring incomplete (cipher / peer-seq / chats / backend client missing)")
		return
	}

	if refuseLinkedDevice(sd.d, sess, f, req.PeerID) {
		return
	}

	if _, err := resolveChatForPeer(ctx, sd.d, req.PeerID); err != nil {
		sendError(sess, f.ID, "unknown_peer", fmt.Sprintf("resolve chat for peer %s: %v", req.PeerID, err))
		return
	}
	dc, err := sd.d.chats.GetByDirectPeer(req.PeerID)
	if err != nil {
		sendError(sess, f.ID, "internal", fmt.Sprintf("post-resolve chat lookup for peer %s: %v", req.PeerID, err))
		return
	}

	var replyToWire *msg.ReplyTo
	var replyToEvent *events.ReplyToSnapshot
	if req.ReplyToMsgID != "" {
		if sd.d.events == nil {
			sendError(sess, f.ID, "not_ready", "events log unavailable for reply lookup")
			return
		}
		target, lookupErr := sd.d.events.GetByMsgID(req.ReplyToMsgID)
		if lookupErr != nil {
			if errors.Is(lookupErr, events.ErrEventNotFound) {
				sendError(sess, f.ID, "unknown_target", "reply target message not found")
				return
			}
			sendError(sess, f.ID, "lookup_failed", lookupErr.Error())
			return
		}
		if target.ChatID != dc.ID {
			sendError(sess, f.ID, "wrong_chat", "reply target belongs to a different chat")
			return
		}
		if target.Kind != events.KindText {
			sendError(sess, f.ID, "unsupported_kind", "can only reply to text messages")
			return
		}
		if target.DeletedAt != 0 {
			sendError(sess, f.ID, "reply_target_deleted", "reply target has been deleted")
			return
		}
		var tb events.TextBody
		if perr := json.Unmarshal(target.Body, &tb); perr != nil {
			sendError(sess, f.ID, "lookup_failed", fmt.Sprintf("decode reply target body: %v", perr))
			return
		}
		replyToWire = &msg.ReplyTo{MsgID: req.ReplyToMsgID, Text: tb.Text}
		replyToEvent = &events.ReplyToSnapshot{MsgID: req.ReplyToMsgID, Text: tb.Text}
	}

	dc, release, err := awaitSendSession(ctx, sd.d, req.PeerID, dc.ID)
	if err != nil {
		sendError(sess, f.ID, sendWaitErrorCode(err), err.Error())
		return
	}
	defer release()

	msgID, err := msg.NewID()
	if err != nil {
		sendError(sess, f.ID, "build_failed", err.Error())
		return
	}

	expireSeconds := dc.RetentionTTL
	presenceState := sd.d.effectivePresenceState()
	senderNick := sd.d.selfNick()
	if dc.NickOverride != "" {
		senderNick = dc.NickOverride
	}
	sendTs := time.Now().Unix()

	deviceCount := sd.d.deviceCount()

	targets := dc.FanoutPeerIDs()
	if len(targets) == 0 {
		sendError(sess, f.ID, "unknown_peer", fmt.Sprintf("chat for peer %s has no routing targets", req.PeerID))
		return
	}

	prepare := func(ctx context.Context, deviceID string) (backendapi.SendRequest, []byte, error) {
		seq, seqErr := sd.d.peerSeq.NextSendSeq(deviceID)
		if seqErr != nil {
			return backendapi.SendRequest{}, nil, fmt.Errorf("seq: %w", seqErr)
		}
		wrapper, bErr := msg.BuildText(seq, sendTs, msgID, req.Text, expireSeconds, presenceState, senderNick, replyToWire, deviceCount)
		if bErr != nil {
			return backendapi.SendRequest{}, nil, fmt.Errorf("build: %w", bErr)
		}
		plaintext, mErr := msg.Marshal(wrapper)
		if mErr != nil {
			return backendapi.SendRequest{}, nil, fmt.Errorf("marshal: %w", mErr)
		}
		blob, encErr := sd.d.cipher.Encrypt(ctx, deviceID, plaintext)
		if encErr != nil {

			if errors.Is(encErr, session.ErrNoSession) {

				slog.Debug("fan-out skipped a device with no session yet",
					slog.String("peer_id", deviceID),
					slog.String("msg_id", msgID),
				)
			} else {
				slog.Warn("fan-out encrypt to a device failed",
					slog.String("peer_id", deviceID),
					slog.String("msg_id", msgID),
					slog.Any("err", encErr),
				)
			}
			return backendapi.SendRequest{}, nil, encErr
		}
		slog.Debug("fan-out prepared a device",
			slog.String("peer_id", deviceID),
			slog.String("msg_id", msgID),
			slog.Int("payload_bytes", len(blob)),

			slog.Int("device_count", deviceCount),
			slog.Uint64("sender_seq", seq),
		)
		return backendapi.SendRequest{
			PeerID:         deviceID,
			Payload:        blob,
			PresenceSource: backendapi.PresenceSourceHaoma,
		}, plaintext, nil
	}

	result, dErr := mirror.Dispatch(ctx, sd.d.backendClient, mirror.NewRail(sd.d.railKey, sd.d.siblings, sd.d.peerMeta),
		mirror.FanoutTargets(targets), prepare, mirror.NewOp(msg.KindText, string(dc.ID)))
	if dErr != nil {

		sendError(sess, f.ID, mirrorDispatchErrorCode(dErr), dErr.Error())
		return
	}
	envIDs := result.EnvelopeIDs
	primarySeq := result.Primary.Seq
	primaryEnvID := result.Primary.EnvelopeID

	if sd.d.events != nil {
		body, marshalErr := json.Marshal(events.TextBody{Text: req.Text, ReplyTo: replyToEvent})
		if marshalErr != nil {
			slog.Warn("send text: marshal body for timeline failed", slog.Any("err", marshalErr))
		} else if _, persistErr := sd.d.events.AppendOutbound(events.OutboundParams{
			ChatID:        dc.ID,
			Kind:          events.KindText,
			SenderSeq:     primarySeq,
			EnvelopeIDs:   envIDs,
			MsgID:         msgID,
			ExpireSeconds: expireSeconds,
			Body:          body,
		}); persistErr != nil {
			slog.Error("persist outbound timeline event failed",
				slog.String("envelope_id", primaryEnvID),
				slog.Any("err", persistErr),
			)
		} else {

			bumpChatActivity(ctx, sd.d, dc.ID, time.Now().Unix())
		}
	}
	slog.Debug("text sent",
		slog.String("peer_id", req.PeerID),
		slog.String("envelope_id", primaryEnvID),
		slog.Int("fanout_targets", len(targets)),
		slog.Int("fanout_delivered", len(envIDs)),
		slog.String("msg_id", msgID),
		slog.Uint64("sender_seq", primarySeq),
		slog.String("presence_state", presenceState),
	)

	resp, err := ipc.NewFrame(ipc.FrameTextSent, f.ID, ipc.SendTextResponse{
		EnvelopeID: primaryEnvID,
		MsgID:      msgID,
		SenderSeq:  primarySeq,
	})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send text_sent frame failed", slog.Any("err", err))
	}
}

const editWindow = events.MutationWindow

func (sd *sessionDispatcher) handleSendEdit(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle send_edit")
	var req ipc.SendEditRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	if req.PeerID == "" {
		sendError(sess, f.ID, "bad_request", "peer_id empty")
		return
	}
	if req.TargetMsgID == "" {
		sendError(sess, f.ID, "bad_request", "target_msg_id empty")
		return
	}
	if req.Text == "" {
		sendError(sess, f.ID, "bad_request", "text empty")
		return
	}
	if sd.d.cipher == nil || sd.d.peerSeq == nil || sd.d.backendClient == nil || sd.d.chats == nil || sd.d.events == nil {
		sendError(sess, f.ID, "not_ready", "frontend wiring incomplete (cipher / peer-seq / chats / events / backend client missing)")
		return
	}

	dc, err := sd.d.chats.GetByDirectPeer(req.PeerID)
	if err != nil {
		sendError(sess, f.ID, "unknown_peer", fmt.Sprintf("no direct chat for peer %s: %v", req.PeerID, err))
		return
	}

	target, err := sd.d.events.GetByMsgID(req.TargetMsgID)
	if err != nil {
		if errors.Is(err, events.ErrEventNotFound) {
			sendError(sess, f.ID, "unknown_target", "target message not found (expired or never existed)")
			return
		}
		sendError(sess, f.ID, "lookup_failed", err.Error())
		return
	}
	if target.ChatID != dc.ID {
		sendError(sess, f.ID, "wrong_chat", "target message belongs to a different chat")
		return
	}
	if target.Direction != events.DirOut {
		sendError(sess, f.ID, "not_author", "can only edit your own messages")
		return
	}
	if target.Kind != events.KindText {
		sendError(sess, f.ID, "unsupported_kind", "only text messages are editable")
		return
	}
	now := time.Now().Unix()
	if now-target.DisplayTs > int64(editWindow) {
		sendError(sess, f.ID, "edit_window_expired", "edit window (24h) has passed")
		return
	}

	dc, release, err := awaitSendSession(ctx, sd.d, req.PeerID, dc.ID)
	if err != nil {
		sendError(sess, f.ID, sendWaitErrorCode(err), err.Error())
		return
	}
	defer release()
	now = time.Now().Unix()

	targets := dc.FanoutPeerIDs()
	if len(targets) == 0 {
		sendError(sess, f.ID, "unknown_peer", fmt.Sprintf("chat for peer %s has no routing targets", req.PeerID))
		return
	}

	editMsgID, err := msg.NewID()
	if err != nil {
		sendError(sess, f.ID, "build_failed", err.Error())
		return
	}

	expireSeconds := dc.RetentionTTL

	prepare := func(ctx context.Context, deviceID string) (backendapi.SendRequest, []byte, error) {
		seq, seqErr := sd.d.peerSeq.NextSendSeq(deviceID)
		if seqErr != nil {
			return backendapi.SendRequest{}, nil, fmt.Errorf("seq: %w", seqErr)
		}
		wrapper, bErr := msg.BuildEdit(seq, now, editMsgID, req.TargetMsgID, req.Text, expireSeconds)
		if bErr != nil {
			return backendapi.SendRequest{}, nil, fmt.Errorf("build: %w", bErr)
		}
		plaintext, mErr := msg.Marshal(wrapper)
		if mErr != nil {
			return backendapi.SendRequest{}, nil, fmt.Errorf("marshal: %w", mErr)
		}
		blob, encErr := sd.d.cipher.Encrypt(ctx, deviceID, plaintext)
		if encErr != nil {
			if errors.Is(encErr, session.ErrNoSession) {
				slog.Debug("fan-out skipped a device with no session yet",
					slog.String("peer_id", deviceID),
					slog.String("edit_msg_id", editMsgID),
				)
			} else {
				slog.Warn("fan-out encrypt to a device failed",
					slog.String("peer_id", deviceID),
					slog.String("edit_msg_id", editMsgID),
					slog.Any("err", encErr),
				)
			}
			return backendapi.SendRequest{}, nil, encErr
		}
		slog.Debug("fan-out prepared a device",
			slog.String("peer_id", deviceID),
			slog.String("edit_msg_id", editMsgID),
			slog.Int("payload_bytes", len(blob)),
		)
		return backendapi.SendRequest{PeerID: deviceID, Payload: blob}, plaintext, nil
	}

	result, dErr := mirror.Dispatch(ctx, sd.d.backendClient, mirror.NewRail(sd.d.railKey, sd.d.siblings, sd.d.peerMeta),
		mirror.FanoutTargets(targets), prepare, mirror.NewOp(msg.KindEdit, string(dc.ID)))
	if dErr != nil {
		sendError(sess, f.ID, mirrorDispatchErrorCode(dErr), dErr.Error())
		return
	}

	if _, err := sd.d.events.ApplyEdit(req.TargetMsgID, req.Text, now, ""); err != nil {
		slog.Error("apply edit to local row failed",
			slog.String("target_msg_id", req.TargetMsgID),
			slog.String("envelope_id", result.Primary.EnvelopeID),
			slog.Any("err", err),
		)
	}
	slog.Debug("edit sent",
		slog.String("peer_id", req.PeerID),
		slog.String("target_msg_id", req.TargetMsgID),
		slog.String("envelope_id", result.Primary.EnvelopeID),
		slog.String("edit_msg_id", editMsgID),
		slog.Uint64("sender_seq", result.Primary.Seq),
		slog.Int("fanout_targets", len(targets)),
		slog.Int("fanout_delivered", len(result.EnvelopeIDs)),
	)

	resp, err := ipc.NewFrame(ipc.FrameEditSent, f.ID, ipc.SendEditResponse{
		EnvelopeID:  result.Primary.EnvelopeID,
		MsgID:       editMsgID,
		SenderSeq:   result.Primary.Seq,
		TargetMsgID: req.TargetMsgID,
	})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send edit_sent frame failed", slog.Any("err", err))
	}
}

func (sd *sessionDispatcher) handleSendDelete(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle send_delete")
	var req ipc.SendDeleteRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	if req.PeerID == "" {
		sendError(sess, f.ID, "bad_request", "peer_id empty")
		return
	}
	if req.TargetMsgID == "" {
		sendError(sess, f.ID, "bad_request", "target_msg_id empty")
		return
	}
	if sd.d.cipher == nil || sd.d.peerSeq == nil || sd.d.backendClient == nil || sd.d.chats == nil || sd.d.events == nil {
		sendError(sess, f.ID, "not_ready", "frontend wiring incomplete (cipher / peer-seq / chats / events / backend client missing)")
		return
	}

	dc, err := sd.d.chats.GetByDirectPeer(req.PeerID)
	if err != nil {
		sendError(sess, f.ID, "unknown_peer", fmt.Sprintf("no direct chat for peer %s: %v", req.PeerID, err))
		return
	}
	target, err := sd.d.events.GetByMsgID(req.TargetMsgID)
	if err != nil {
		if errors.Is(err, events.ErrEventNotFound) {
			sendError(sess, f.ID, "unknown_target", "target message not found (expired or never existed)")
			return
		}
		sendError(sess, f.ID, "lookup_failed", err.Error())
		return
	}
	if target.ChatID != dc.ID {
		sendError(sess, f.ID, "wrong_chat", "target message belongs to a different chat")
		return
	}
	now := time.Now().Unix()

	if !target.Deletable(now) {
		sendError(sess, f.ID, "not_deletable",
			"row is not deletable (must be your own text/file message, not already deleted, within 24h)")
		return
	}

	dc, release, err := awaitSendSession(ctx, sd.d, req.PeerID, dc.ID)
	if err != nil {
		sendError(sess, f.ID, sendWaitErrorCode(err), err.Error())
		return
	}
	defer release()
	now = time.Now().Unix()

	targets := dc.FanoutPeerIDs()
	if len(targets) == 0 {
		sendError(sess, f.ID, "unknown_peer", fmt.Sprintf("chat for peer %s has no routing targets", req.PeerID))
		return
	}

	deleteMsgID, err := msg.NewID()
	if err != nil {
		sendError(sess, f.ID, "build_failed", err.Error())
		return
	}

	expireSeconds := dc.RetentionTTL

	prepare := func(ctx context.Context, deviceID string) (backendapi.SendRequest, []byte, error) {
		seq, seqErr := sd.d.peerSeq.NextSendSeq(deviceID)
		if seqErr != nil {
			return backendapi.SendRequest{}, nil, fmt.Errorf("seq: %w", seqErr)
		}
		wrapper, bErr := msg.BuildDelete(seq, now, deleteMsgID, req.TargetMsgID, expireSeconds)
		if bErr != nil {
			return backendapi.SendRequest{}, nil, fmt.Errorf("build: %w", bErr)
		}
		plaintext, mErr := msg.Marshal(wrapper)
		if mErr != nil {
			return backendapi.SendRequest{}, nil, fmt.Errorf("marshal: %w", mErr)
		}
		blob, encErr := sd.d.cipher.Encrypt(ctx, deviceID, plaintext)
		if encErr != nil {
			if errors.Is(encErr, session.ErrNoSession) {
				slog.Debug("fan-out skipped a device with no session yet",
					slog.String("peer_id", deviceID),
					slog.String("delete_msg_id", deleteMsgID),
				)
			} else {
				slog.Warn("fan-out encrypt to a device failed",
					slog.String("peer_id", deviceID),
					slog.String("delete_msg_id", deleteMsgID),
					slog.Any("err", encErr),
				)
			}
			return backendapi.SendRequest{}, nil, encErr
		}
		slog.Debug("fan-out prepared a device",
			slog.String("peer_id", deviceID),
			slog.String("delete_msg_id", deleteMsgID),
			slog.Int("payload_bytes", len(blob)),
		)
		return backendapi.SendRequest{PeerID: deviceID, Payload: blob}, plaintext, nil
	}

	result, dErr := mirror.Dispatch(ctx, sd.d.backendClient, mirror.NewRail(sd.d.railKey, sd.d.siblings, sd.d.peerMeta),
		mirror.FanoutTargets(targets), prepare, mirror.NewOp(msg.KindDelete, string(dc.ID)))
	if dErr != nil {
		sendError(sess, f.ID, mirrorDispatchErrorCode(dErr), dErr.Error())
		return
	}

	if _, err := sd.d.events.ApplyDelete(req.TargetMsgID, now, ""); err != nil {
		slog.Error("apply delete to local row failed",
			slog.String("target_msg_id", req.TargetMsgID),
			slog.String("envelope_id", result.Primary.EnvelopeID),
			slog.Any("err", err),
		)
	}
	slog.Debug("delete sent",
		slog.String("peer_id", req.PeerID),
		slog.String("target_msg_id", req.TargetMsgID),
		slog.String("envelope_id", result.Primary.EnvelopeID),
		slog.String("delete_msg_id", deleteMsgID),
		slog.Uint64("sender_seq", result.Primary.Seq),
		slog.Int("fanout_targets", len(targets)),
		slog.Int("fanout_delivered", len(result.EnvelopeIDs)),
	)

	resp, err := ipc.NewFrame(ipc.FrameDeleteSent, f.ID, ipc.SendDeleteResponse{
		EnvelopeID:  result.Primary.EnvelopeID,
		MsgID:       deleteMsgID,
		SenderSeq:   result.Primary.Seq,
		TargetMsgID: req.TargetMsgID,
	})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send delete_sent frame failed", slog.Any("err", err))
	}
}

func (sd *sessionDispatcher) handleSendReaction(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle send_reaction")
	var req ipc.SendReactionRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	if req.PeerID == "" {
		sendError(sess, f.ID, "bad_request", "peer_id empty")
		return
	}
	if req.TargetMsgID == "" {
		sendError(sess, f.ID, "bad_request", "target_msg_id empty")
		return
	}
	if sd.d.cipher == nil || sd.d.peerSeq == nil || sd.d.backendClient == nil || sd.d.chats == nil || sd.d.events == nil {
		sendError(sess, f.ID, "not_ready", "frontend wiring incomplete (cipher / peer-seq / chats / events / backend client missing)")
		return
	}

	dc, err := sd.d.chats.GetByDirectPeer(req.PeerID)
	if err != nil {
		sendError(sess, f.ID, "unknown_peer", fmt.Sprintf("no direct chat for peer %s: %v", req.PeerID, err))
		return
	}
	target, err := sd.d.events.GetByMsgID(req.TargetMsgID)
	if err != nil {
		if errors.Is(err, events.ErrEventNotFound) {
			sendError(sess, f.ID, "unknown_target", "target message not found (expired or never existed)")
			return
		}
		sendError(sess, f.ID, "lookup_failed", err.Error())
		return
	}
	if target.ChatID != dc.ID {
		sendError(sess, f.ID, "wrong_chat", "target message belongs to a different chat")
		return
	}
	if target.DeletedAt > 0 {
		sendError(sess, f.ID, "unsupported_kind", "can't react to a deleted message")
		return
	}
	if target.Kind != events.KindText && target.Kind != events.KindFile {
		sendError(sess, f.ID, "unsupported_kind", "only text and file messages are reactable")
		return
	}

	dc, release, err := awaitSendSession(ctx, sd.d, req.PeerID, dc.ID)
	if err != nil {
		sendError(sess, f.ID, sendWaitErrorCode(err), err.Error())
		return
	}
	defer release()

	targets := dc.FanoutPeerIDs()
	if len(targets) == 0 {
		sendError(sess, f.ID, "unknown_peer", fmt.Sprintf("chat for peer %s has no routing targets", req.PeerID))
		return
	}

	reactionMsgID, err := msg.NewID()
	if err != nil {
		sendError(sess, f.ID, "build_failed", err.Error())
		return
	}

	now := time.Now().Unix()
	expireSeconds := dc.RetentionTTL

	prepare := func(ctx context.Context, deviceID string) (backendapi.SendRequest, []byte, error) {
		seq, seqErr := sd.d.peerSeq.NextSendSeq(deviceID)
		if seqErr != nil {
			return backendapi.SendRequest{}, nil, fmt.Errorf("seq: %w", seqErr)
		}
		wrapper, bErr := msg.BuildReaction(seq, now, reactionMsgID, req.TargetMsgID, req.Emoji, expireSeconds)
		if bErr != nil {
			return backendapi.SendRequest{}, nil, fmt.Errorf("build: %w", bErr)
		}
		plaintext, mErr := msg.Marshal(wrapper)
		if mErr != nil {
			return backendapi.SendRequest{}, nil, fmt.Errorf("marshal: %w", mErr)
		}
		blob, encErr := sd.d.cipher.Encrypt(ctx, deviceID, plaintext)
		if encErr != nil {
			if errors.Is(encErr, session.ErrNoSession) {
				slog.Debug("fan-out skipped a device with no session yet",
					slog.String("peer_id", deviceID),
					slog.String("reaction_msg_id", reactionMsgID),
				)
			} else {
				slog.Warn("fan-out encrypt to a device failed",
					slog.String("peer_id", deviceID),
					slog.String("reaction_msg_id", reactionMsgID),
					slog.Any("err", encErr),
				)
			}
			return backendapi.SendRequest{}, nil, encErr
		}
		slog.Debug("fan-out prepared a device",
			slog.String("peer_id", deviceID),
			slog.String("reaction_msg_id", reactionMsgID),
			slog.Int("payload_bytes", len(blob)),
		)
		return backendapi.SendRequest{PeerID: deviceID, Payload: blob}, plaintext, nil
	}

	result, dErr := mirror.Dispatch(ctx, sd.d.backendClient, mirror.NewRail(sd.d.railKey, sd.d.siblings, sd.d.peerMeta),
		mirror.FanoutTargets(targets), prepare, mirror.NewOp(msg.KindReaction, string(dc.ID)))
	if dErr != nil {
		sendError(sess, f.ID, mirrorDispatchErrorCode(dErr), dErr.Error())
		return
	}

	if _, err := sd.d.events.AppendReactionBreadcrumb(req.TargetMsgID, req.Emoji, "", reactionMsgID, now, dc.RetentionTTL); err != nil {
		slog.Error("append local reaction breadcrumb failed",
			slog.String("target_msg_id", req.TargetMsgID),
			slog.String("envelope_id", result.Primary.EnvelopeID),
			slog.Any("err", err),
		)
	}
	slog.Debug("reaction sent",
		slog.String("peer_id", req.PeerID),
		slog.String("target_msg_id", req.TargetMsgID),
		slog.String("envelope_id", result.Primary.EnvelopeID),
		slog.String("reaction_msg_id", reactionMsgID),
		slog.String("emoji", req.Emoji),
		slog.Uint64("sender_seq", result.Primary.Seq),
		slog.Int("fanout_targets", len(targets)),
		slog.Int("fanout_delivered", len(result.EnvelopeIDs)),
	)

	resp, err := ipc.NewFrame(ipc.FrameReactionSent, f.ID, ipc.SendReactionResponse{
		EnvelopeID:  result.Primary.EnvelopeID,
		MsgID:       reactionMsgID,
		SenderSeq:   result.Primary.Seq,
		TargetMsgID: req.TargetMsgID,
	})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send reaction_sent frame failed", slog.Any("err", err))
	}
}

func (sd *sessionDispatcher) handleTorInfo(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle tor_info")
	if sd.d.backendClient == nil {
		sendError(sess, f.ID, "not_ready", "backend client not wired")
		return
	}
	info, err := sd.d.backendClient.TorInfo(ctx)
	if err != nil {
		sendError(sess, f.ID, "backend_unavailable", fmt.Sprintf("GET /tor: %v", err))
		return
	}
	slots := make([]ipc.TorSlot, len(info.Slots))
	for i, s := range info.Slots {
		slots[i] = ipc.TorSlot{Slot: s.Slot, ServiceID: s.ServiceID, URL: s.URL}
	}
	payload := ipc.TorInfoResponsePayload{
		Slots: slots,
		Health: ipc.TorHealth{
			Bootstrap:   info.Health.Bootstrap,
			Ready:       info.Health.Ready,
			Unreachable: info.Health.Unreachable,
		},
	}
	resp, err := ipc.NewFrame(ipc.FrameTorInfoResponse, f.ID, payload)
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send tor_info_response frame failed", slog.Any("err", err))
	}
}

func (sd *sessionDispatcher) handleSystemInfo(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle system_info")
	payload := ipc.SystemInfoResponsePayload{
		Haoma: ipc.SystemInfoComponent{
			Version:   version,
			StartedAt: sd.d.startedAt.Format(time.RFC3339),
		},
	}
	if sd.d.backendClient != nil {
		info, err := sd.d.backendClient.SystemInfo(ctx)
		if err == nil {
			payload.Haomad = ipc.SystemInfoComponent{
				Version:   info.Version,
				StartedAt: info.StartedAt,
			}
			payload.Tor = ipc.SystemInfoComponent{
				Version:   info.Tor.Version,
				StartedAt: info.Tor.StartedAt,
			}
		} else {
			slog.Warn("system_info: backend fetch failed", slog.Any("err", err))
		}
	}
	resp, err := ipc.NewFrame(ipc.FrameSystemInfoResponse, f.ID, payload)
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send system_info_response frame failed", slog.Any("err", err))
	}
}

func (sd *sessionDispatcher) handleSetTorPassword(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle set_tor_password")
	if sd.d.backendClient == nil {
		sendError(sess, f.ID, "not_ready", "backend client not wired")
		return
	}
	var req ipc.SetTorPasswordRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_frame", "decode set_tor_password: "+err.Error())
		return
	}
	if err := sd.d.backendClient.SetTorPassword(ctx, req.Password); err != nil {
		sendError(sess, f.ID, "backend_unavailable", fmt.Sprintf("POST /tor-password: %v", err))
		return
	}
	resp, err := ipc.NewFrame(ipc.FrameTorPasswordAccepted, f.ID, struct{}{})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send tor_password_accepted frame failed", slog.Any("err", err))
	}
}

func (sd *sessionDispatcher) handleListPeers(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle list_peers")
	if sd.d.backendClient == nil {
		sendError(sess, f.ID, "not_ready", "backend client not wired")
		return
	}
	pr, err := sd.d.backendClient.Peers(ctx)
	if err != nil {
		sendError(sess, f.ID, "backend_unavailable", fmt.Sprintf("GET /peers: %v", err))
		return
	}
	entries := make([]ipc.PeerEntry, len(pr.Peers))
	for i, p := range pr.Peers {

		entry, err := peerEntryFor(sd.d, p)
		if err != nil {
			sendError(sess, f.ID, reasonStoreReadFailed, "the contacts could not be read")
			return
		}
		entries[i] = entry
	}
	resp, err := ipc.NewFrame(ipc.FramePeersListed, f.ID, ipc.PeersListedResponse{Peers: entries})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send peers_listed frame failed", slog.Any("err", err))
	}
}

func applyPresenceSnapshot(d *daemon, peerID string, entry *ipc.PeerEntry) {
	if d == nil || d.presenceCache == nil || peerID == "" {
		return
	}
	snap := d.presenceCache.Snapshot(peerID)
	entry.Accepting = snap.Accepting
	entry.Sending = snap.Sending
	entry.Chatty = snap.Chatty
	entry.Effective = snap.Effective
}

func (sd *sessionDispatcher) handleSetAlias(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle set_alias")
	var req ipc.SetAliasRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	if req.PeerID == "" {
		sendError(sess, f.ID, "bad_request", "peer_id required")
		return
	}
	if sd.d.peerMeta == nil {
		sendError(sess, f.ID, "not_ready", "peer-meta store not wired")
		return
	}
	changed, err := sd.d.peerMeta.SetAlias(req.PeerID, req.Alias, time.Now().Unix())
	if err != nil {
		sendError(sess, f.ID, "internal", fmt.Sprintf("set alias: %v", err))
		return
	}
	slog.Debug("peer-meta alias persisted",
		slog.String("peer_id", req.PeerID),
		slog.Bool("changed", changed),
	)

	peerEntry, err := refreshedPeerEntry(ctx, sd.d, req.PeerID)
	if err != nil {
		sendError(sess, f.ID, reasonStoreReadFailed, "the alias was set and the contact row could not be read")
		return
	}
	if changed {
		personChangedLocally(ctx, sd.d, req.PeerID)
	}
	resp, err := ipc.NewFrame(ipc.FrameAliasUpdated, f.ID, ipc.AliasUpdatedResponse{
		Peer: peerEntry,
	})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send nickname_updated frame failed", slog.Any("err", err))
	}
}

func (sd *sessionDispatcher) handleSetPeerVerified(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle set_peer_verified")
	var req ipc.SetPeerVerifiedRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	if req.PeerID == "" {
		sendError(sess, f.ID, "bad_request", "peer_id required")
		return
	}
	if sd.d.peerMeta == nil {
		sendError(sess, f.ID, "not_ready", "peer-meta store not wired")
		return
	}
	changed, err := sd.d.peerMeta.SetVerified(req.PeerID, req.Verified, time.Now().Unix())
	if err != nil {
		sendError(sess, f.ID, "internal", fmt.Sprintf("set verified: %v", err))
		return
	}
	slog.Debug("peer-meta verified persisted",
		slog.String("peer_id", req.PeerID),
		slog.Bool("verified", req.Verified),
		slog.Bool("changed", changed),
	)
	peerEntry, err := refreshedPeerEntry(ctx, sd.d, req.PeerID)
	if err != nil {
		sendError(sess, f.ID, reasonStoreReadFailed, "the trust mark was set and the contact row could not be read")
		return
	}
	if changed {
		personChangedLocally(ctx, sd.d, req.PeerID)
	}
	resp, err := ipc.NewFrame(ipc.FramePeerVerified, f.ID, ipc.PeerVerifiedResponse{Peer: peerEntry})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send peer_verified frame failed", slog.Any("err", err))
	}
}

func (sd *sessionDispatcher) handlePeerAction(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle peer_action")
	var req ipc.PeerActionRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	if req.PeerID == "" {
		sendError(sess, f.ID, "bad_request", "peer_id required")
		return
	}
	if sd.d.backendClient == nil {
		sendError(sess, f.ID, "not_ready", "backend client not wired")
		return
	}
	if sd.d.events == nil {
		sendError(sess, f.ID, "not_ready", "events log not wired")
		return
	}
	if sd.d.chats == nil {
		sendError(sess, f.ID, "not_ready", "chat store not wired")
		return
	}

	dc, chatErr := sd.d.chats.GetByDirectPeer(req.PeerID)
	var chatID chat.ChatID
	if chatErr == nil {
		chatID = dc.ID
	}

	var (
		peer    backendapi.Peer
		deleted int

		keptChat *chat.DirectChat

		person        *personDeleteResult
		personDeleted syncrail.DeletedContact

		preDelete *ipc.PeerEntry
	)

	switch req.Action {
	case ipc.PeerActionRetire:
		p, err := sd.d.backendClient.PeerAction(ctx, req.PeerID, string(ipc.PeerActionRetire))
		if err != nil {
			sendError(sess, f.ID, "backend_unavailable", fmt.Sprintf("POST /peers/%s/action: %v", req.PeerID, err))
			return
		}
		peer = p

	case ipc.PeerActionDelete:

		before, bErr := peerEntryFor(sd.d, backendapi.Peer{ID: req.PeerID})
		if bErr != nil {
			sendError(sess, f.ID, reasonStoreReadFailed, "the contact row could not be read")
			return
		}
		preDelete = &before

		plan, pErr := planPairingDelete(ctx, sd.d, req.PeerID, dc)
		if pErr != nil {
			sendError(sess, f.ID, "backend_unavailable", fmt.Sprintf("plan the delete: %v", pErr))
			return
		}
		switch plan.kind {
		case deleteThePairing:

			if chatID != "" {
				routes := dc.FanoutPeerIDs()
				var liveRoute string
				for _, id := range plan.otherLive {
					if slices.Contains(routes, id) {
						liveRoute = id
						break
					}
				}
				if liveRoute != "" {
					if dc.PeerID == req.PeerID {
						if _, _, err := sd.d.dropRoutes(chatID, liveRoute, []string{req.PeerID}); err != nil {
							sendError(sess, f.ID, "internal", fmt.Sprintf("move the chat to another pairing: %v", err))
							return
						}
					}
					after, err := sd.d.dropPairing(chatID, req.PeerID)
					if err != nil {
						sendError(sess, f.ID, "internal", fmt.Sprintf("drop the pairing from its chat: %v", err))
						return
					}
					keptChat = after

					noteContactChangedForPeer(sd.d, liveRoute)
					chatID = ""
				}

			}
		case deleteLocal:

			if chatID != "" {
				keep, kErr := chatOutlivesPairing(ctx, sd.d, dc, req.PeerID)
				if kErr != nil {
					sendError(sess, f.ID, "backend_unavailable", fmt.Sprintf("read the peer list: %v", kErr))
					return
				}
				if keep {
					after, err := sd.d.dropPairing(chatID, req.PeerID)
					if err != nil {
						sendError(sess, f.ID, "internal", fmt.Sprintf("drop the pairing from its chat: %v", err))
						return
					}
					keptChat = after
					chatID = ""
				}
			}
		case deleteThePerson:
			fp := plan.fingerprint

			if mine, sErr := fingerprintNamesSibling(sd.d, fp); sErr != nil || mine {
				sendError(sess, f.ID, "bad_request", "that contact names one of my own devices, or the linked-device set could not be read")
				return
			}

			now := time.Now().Unix()
			at := now
			if cur, found, tErr := sd.d.peerMeta.Tombstone(fp); tErr != nil {
				sendError(sess, f.ID, reasonStoreReadFailed, fmt.Sprintf("read the tombstone: %v", tErr))
				return
			} else if found && cur.DeletedAt >= at {
				at = cur.DeletedAt + 1
			}
			tomb, _, mErr := sd.d.peerMeta.MarkDeleted(fp, at, now)
			if mErr != nil {
				sendError(sess, f.ID, "internal", fmt.Sprintf("write the tombstone: %v", mErr))
				return
			}
			res, dErr := deletePerson(ctx, sd.d, fp, tomb, "operator")
			if dErr != nil {

				pushPersonDeleted(ctx, sd.d, res)
				sendError(sess, f.ID, "backend_unavailable", fmt.Sprintf("delete the contact: %v", dErr))
				return
			}
			if err := sd.d.peerMeta.MarkDeleteDone(fp, tomb.DeletedAt); err != nil {
				slog.Warn("contact delete: could not record the delete as done", slog.Any("err", err))
			}
			p, ok := res.peerFor(req.PeerID)
			if !ok {
				p = backendapi.Peer{ID: req.PeerID}
			}
			peer = p
			deleted = res.events
			person = &res
			personDeleted = syncrail.DeletedContact{Fingerprint: fp, DeletedAt: tomb.DeletedAt}
		}
		if person != nil {
			break
		}

		if chatID != "" {
			n, cascade, err := sd.d.events.DeleteByChat(chatID)
			if err != nil {
				sendError(sess, f.ID, "internal", fmt.Sprintf("clear events: %v", err))
				return
			}
			deleted = n
			if cascade > 0 {
				slog.Info("peer-delete cascade", slog.String("chat_id", string(chatID)), slog.Int("cascade_keys", cascade))
			}
			if err := sd.d.chats.Delete(chatID); err != nil {
				sendError(sess, f.ID, "internal", fmt.Sprintf("drop chat: %v", err))
				return
			}
		}
		p, err := deletePairingInBackend(ctx, sd.d, req.PeerID)
		if err != nil {
			sendError(sess, f.ID, "backend_unavailable", fmt.Sprintf("POST /peers/%s/action: %v", req.PeerID, err))
			return
		}
		peer = p

		if sd.d.peerMeta != nil {
			if err := sd.d.peerMeta.ForgetPeer(req.PeerID); err != nil {
				slog.Warn("peer delete: the pairing's record could not be removed",
					slog.String("peer_id", req.PeerID),
					slog.Any("err", err),
				)
			}
		}

	default:
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("unknown action %q", req.Action))
		return
	}

	var peerEntry ipc.PeerEntry
	if preDelete != nil {
		peerEntry = *preDelete
		peerEntry.ChatID = resolveDirectChatID(sd.d, req.PeerID)
		peerEntry.LastActiveAt = peer.LastActiveAt
		peerEntry.LastPassiveAt = peer.LastPassiveAt
		peerEntry.RetiredAt = peer.RetiredAt
		peerEntry.Unreachable = peer.Unreachable
	} else {
		var err error
		peerEntry, err = peerEntryFor(sd.d, peer)
		if err != nil {
			sendError(sess, f.ID, reasonStoreReadFailed, "the action ran and the contact row could not be read")
			return
		}
	}
	resp, err := ipc.NewFrame(ipc.FramePeerActionApplied, f.ID, ipc.PeerActionAppliedResponse{
		Peer:         peerEntry,
		Action:       req.Action,
		DeletedCount: deleted,
	})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send peer_action_applied frame failed", slog.Any("err", err))
	}

	switch req.Action {
	case ipc.PeerActionRetire:
		afterPairingsRetired(ctx, sd.d, []string{req.PeerID}, "")
		slog.Debug("peer.retired broadcast (via peer.updated)",
			slog.String("peer_id", req.PeerID),
		)
	case ipc.PeerActionDelete:
		if person != nil {
			pushPersonDeleted(ctx, sd.d, *person)

			sendContactDelete(ctx, sd.d, personDeleted)
			return
		}
		push(sd.d.ipcSrv, ipc.FramePeerDeleted, "", ipc.PeerDeletedPayload{
			PeerID: req.PeerID,
		})
		slog.Debug("peer.deleted broadcast",
			slog.String("peer_id", req.PeerID),
		)
		if keptChat != nil {
			pushChatUpdated(sd.d, keptChat)
		}
		if chatID != "" {
			push(sd.d.ipcSrv, ipc.FrameChatDeleted, "", ipc.ChatDeletedPayload{
				ChatID:       string(chatID),
				DeletedCount: deleted,
			})
			slog.Debug("chat.deleted broadcast (peer-cascade)",
				slog.String("chat_id", string(chatID)),
				slog.Int("deleted_count", deleted),
			)
		}
	}
}

func (sd *sessionDispatcher) handleInspectEvent(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	_ = ctx
	slog.Debug("handle inspect_event")
	var req ipc.InspectEventRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	if req.MsgID == "" {
		sendError(sess, f.ID, "bad_request", "msg_id required")
		return
	}
	if sd.d.events == nil {
		sendError(sess, f.ID, "not_ready", "events log not wired")
		return
	}
	ev, err := sd.d.events.GetByMsgID(req.MsgID)
	if err != nil {
		if errors.Is(err, events.ErrEventNotFound) {
			sendError(sess, f.ID, "not_found", fmt.Sprintf("no event for msg_id %q", req.MsgID))
			return
		}
		sendError(sess, f.ID, "internal", err.Error())
		return
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	resp, err := ipc.NewFrame(ipc.FrameEventInspected, f.ID, ipc.EventInspectedResponse{Event: raw})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send event_inspected frame failed", slog.Any("err", err))
	}
}

func (sd *sessionDispatcher) handleChatSearch(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	_ = ctx
	slog.Debug("handle chat_search")
	var req ipc.ChatSearchRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	if req.ChatID == "" {
		sendError(sess, f.ID, "bad_request", "chat_id required")
		return
	}
	if req.Query == "" {
		sendError(sess, f.ID, "bad_request", "query required")
		return
	}
	if sd.d.events == nil {
		sendError(sess, f.ID, "not_ready", "events log not wired")
		return
	}
	hits, truncated, err := sd.d.events.SearchInChat(chat.ChatID(req.ChatID), req.Query, 0)
	if err != nil {
		sendError(sess, f.ID, "internal", err.Error())
		return
	}
	matches := make([]ipc.ChatSearchMatch, len(hits))
	for i, h := range hits {
		matches[i] = ipc.ChatSearchMatch{
			MsgID:      h.MsgID,
			DisplayTs:  h.DisplayTs,
			BodyOffset: h.BodyOffset,
		}
	}
	resp, err := ipc.NewFrame(ipc.FrameChatSearched, f.ID, ipc.ChatSearchResponse{
		ChatID:    req.ChatID,
		Query:     req.Query,
		Matches:   matches,
		Truncated: truncated,
	})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send chat_searched frame failed", slog.Any("err", err))
	}
}

func (sd *sessionDispatcher) handleListTimeline(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle list_timeline")
	_ = ctx
	var req ipc.ListTimelineRequest
	if len(f.Payload) > 0 {
		if err := json.Unmarshal(f.Payload, &req); err != nil {
			sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
			return
		}
	}
	if req.PeerID == "" {
		sendError(sess, f.ID, "bad_request", "peer_id empty")
		return
	}
	if sd.d.events == nil || sd.d.chats == nil {
		sendError(sess, f.ID, "not_ready", "event log or chat store not wired")
		return
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 50
	}
	dc, err := sd.d.chats.GetByDirectPeer(req.PeerID)
	if err != nil {

		resp, encErr := ipc.NewFrame(ipc.FrameTimelinePage, f.ID, ipc.TimelinePageResponse{
			PeerID:  req.PeerID,
			Events:  nil,
			HasMore: false,
		})
		if encErr != nil {
			sendError(sess, f.ID, "encode_frame", encErr.Error())
			return
		}
		_ = sess.Send(resp)
		return
	}
	evs, err := sd.d.events.ListBefore(dc.ID, req.BeforeDisplayTs, limit)
	if err != nil {
		sendError(sess, f.ID, "storage_error", err.Error())
		return
	}
	rawEvs := make([]json.RawMessage, len(evs))
	for i, ev := range evs {
		b, merr := json.Marshal(ev)
		if merr != nil {
			sendError(sess, f.ID, "encode_failed", merr.Error())
			return
		}
		rawEvs[i] = b
	}
	resp, err := ipc.NewFrame(ipc.FrameTimelinePage, f.ID, ipc.TimelinePageResponse{
		PeerID:  req.PeerID,
		Events:  rawEvs,
		HasMore: len(evs) == limit,
	})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send timeline_page frame failed", slog.Any("err", err))
	}
}

func (sd *sessionDispatcher) handleListFiles(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle list_files")
	_ = ctx
	var req ipc.ListFilesRequest
	if len(f.Payload) > 0 {
		if err := json.Unmarshal(f.Payload, &req); err != nil {
			sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
			return
		}
	}
	if req.ChatID == "" {
		sendError(sess, f.ID, "bad_request", "chat_id empty")
		return
	}
	if sd.d.files == nil || sd.d.events == nil {
		sendError(sess, f.ID, "not_ready", "files manager or event log not wired")
		return
	}
	chatID := chat.ChatID(req.ChatID)
	metas, err := sd.d.files.ListByChat(chatID)
	if err != nil {
		sendError(sess, f.ID, "storage_error", err.Error())
		return
	}
	now := time.Now().Unix()
	out := make([]ipc.FileEntry, 0, len(metas))
	for _, m := range metas {
		ev, err := sd.d.events.GetByMsgID(m.MsgID)
		if errors.Is(err, events.ErrEventNotFound) {
			continue
		}
		if err != nil {
			slog.Warn("list_files: event lookup failed",
				slog.String("chat_id", req.ChatID),
				slog.String("msg_id", m.MsgID),
				slog.Any("err", err))
			continue
		}
		if ev.DeletedAt > 0 {
			continue
		}
		out = append(out, ipc.FileEntry{
			MsgID:         m.MsgID,
			Direction:     string(m.Direction),
			OriginalName:  m.OriginalName,
			Mime:          m.Mime,
			Size:          m.Size,
			DisplayTs:     ev.DisplayTs,
			State:         string(m.State),
			DeliveryState: ev.DeliveryState,
			Deletable:     ev.Deletable(now),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].DisplayTs > out[j].DisplayTs
	})

	resp, err := ipc.NewFrame(ipc.FrameFilesList, f.ID, ipc.FilesListResponse{
		ChatID: req.ChatID,
		Files:  out,
	})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send files_list frame failed", slog.Any("err", err))
	}
}

func (sd *sessionDispatcher) handleSaveFile(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle save_file")
	_ = ctx
	var req ipc.SaveFileRequest
	if len(f.Payload) > 0 {
		if err := json.Unmarshal(f.Payload, &req); err != nil {
			sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
			return
		}
	}
	if req.ChatID == "" {
		sendError(sess, f.ID, "bad_request", "chat_id empty")
		return
	}
	if req.MsgID == "" {
		sendError(sess, f.ID, "bad_request", "msg_id empty")
		return
	}
	if req.DestDir == "" {
		sendError(sess, f.ID, "bad_request", "dest_dir empty")
		return
	}
	if sd.d.files == nil {
		sendError(sess, f.ID, "not_ready", "files manager not wired")
		return
	}

	meta, err := sd.d.files.GetMeta(req.MsgID)
	if err != nil {
		if errors.Is(err, files.ErrMetaNotFound) {
			sendError(sess, f.ID, "unknown_msg", "no file metadata for msg_id")
			return
		}
		sendError(sess, f.ID, "storage_error", err.Error())
		return
	}
	if string(meta.ChatID) != req.ChatID {
		sendError(sess, f.ID, "wrong_chat", "msg_id belongs to a different chat")
		return
	}
	if meta.State != files.StateReady {
		sendError(sess, f.ID, "not_ready",
			fmt.Sprintf("file state %q — only ready files can be saved", meta.State))
		return
	}

	info, err := os.Stat(req.DestDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			sendError(sess, f.ID, "dest_missing", "dest_dir does not exist")
			return
		}
		sendError(sess, f.ID, "dest_stat_failed", err.Error())
		return
	}
	if !info.IsDir() {
		sendError(sess, f.ID, "dest_not_dir", "dest_dir is not a directory")
		return
	}

	plaintext, err := sd.d.files.UnsealAtRest(chat.ChatID(req.ChatID), req.MsgID)
	if err != nil {
		sendError(sess, f.ID, "unseal_failed", err.Error())
		return
	}

	name, err := generateSavedFilename(meta.OriginalName)
	if err != nil {
		sendError(sess, f.ID, "name_failed", err.Error())
		return
	}
	fullPath := filepath.Join(req.DestDir, name)

	out, err := os.OpenFile(fullPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {

		sendError(sess, f.ID, "write_failed", err.Error())
		return
	}
	if _, werr := out.Write(plaintext); werr != nil {
		_ = out.Close()
		_ = os.Remove(fullPath)
		sendError(sess, f.ID, "write_failed", werr.Error())
		return
	}
	if cerr := out.Close(); cerr != nil {
		_ = os.Remove(fullPath)
		sendError(sess, f.ID, "write_failed", cerr.Error())
		return
	}

	resp, err := ipc.NewFrame(ipc.FrameFileSaved, f.ID, ipc.SaveFileResponse{FullPath: fullPath})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send file_saved frame failed", slog.Any("err", err))
	}
}

func (sd *sessionDispatcher) handleOpenFile(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle open_file")
	_ = ctx
	var req ipc.OpenFileRequest
	if len(f.Payload) > 0 {
		if err := json.Unmarshal(f.Payload, &req); err != nil {
			sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
			return
		}
	}
	if req.ChatID == "" {
		sendError(sess, f.ID, "bad_request", "chat_id empty")
		return
	}
	if req.MsgID == "" {
		sendError(sess, f.ID, "bad_request", "msg_id empty")
		return
	}
	if sd.d.files == nil {
		sendError(sess, f.ID, "not_ready", "files manager not wired")
		return
	}

	meta, err := sd.d.files.GetMeta(req.MsgID)
	if err != nil {
		if errors.Is(err, files.ErrMetaNotFound) {
			sendError(sess, f.ID, "unknown_msg", "no file metadata for msg_id")
			return
		}
		sendError(sess, f.ID, "storage_error", err.Error())
		return
	}
	if string(meta.ChatID) != req.ChatID {
		sendError(sess, f.ID, "wrong_chat", "msg_id belongs to a different chat")
		return
	}
	if meta.State != files.StateReady {
		sendError(sess, f.ID, "not_ready",
			fmt.Sprintf("file state %q — only ready files can be opened", meta.State))
		return
	}

	fullPath, err := sd.d.files.WriteOpenTransient(chat.ChatID(req.ChatID), req.MsgID)
	if err != nil {
		sendError(sess, f.ID, "unseal_failed", err.Error())
		return
	}

	sniffed, matches, err := sd.d.files.ReSniffMIME(chat.ChatID(req.ChatID), req.MsgID, meta.Mime)
	if err != nil {

		slog.Warn("open_file: ReSniffMIME failed",
			slog.String("chat_id", req.ChatID),
			slog.String("msg_id", req.MsgID),
			slog.Any("err", err))
		sniffed, matches = "", true
	}

	resp, err := ipc.NewFrame(ipc.FrameFileOpenReady, f.ID, ipc.OpenFileReadyResponse{
		FullPath:    fullPath,
		SniffedMIME: sniffed,
		MIMEMatches: matches,
	})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send file_open_ready frame failed", slog.Any("err", err))
	}
}

func (sd *sessionDispatcher) handleImageStream(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle image_stream")
	_ = ctx
	var req ipc.ImageStreamRequest
	if len(f.Payload) > 0 {
		if err := json.Unmarshal(f.Payload, &req); err != nil {
			sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
			return
		}
	}
	if req.ChatID == "" {
		sendError(sess, f.ID, "bad_request", "chat_id empty")
		return
	}
	if req.MsgID == "" {
		sendError(sess, f.ID, "bad_request", "msg_id empty")
		return
	}
	if sd.d.files == nil {
		sendError(sess, f.ID, "not_ready", "files manager not wired")
		return
	}

	meta, err := sd.d.files.GetMeta(req.MsgID)
	if err != nil {
		if errors.Is(err, files.ErrMetaNotFound) {
			sendError(sess, f.ID, "unknown_msg", "no file metadata for msg_id")
			return
		}
		sendError(sess, f.ID, "storage_error", err.Error())
		return
	}
	if string(meta.ChatID) != req.ChatID {
		sendError(sess, f.ID, "wrong_chat", "msg_id belongs to a different chat")
		return
	}
	if meta.State != files.StateReady {
		sendError(sess, f.ID, "not_ready",
			fmt.Sprintf("file state %q — only ready files can be streamed", meta.State))
		return
	}
	if !strings.HasPrefix(strings.ToLower(meta.Mime), "image/") {
		sendError(sess, f.ID, "not_an_image",
			"image_stream only ships image MIMEs; use open_file for other types")
		return
	}

	plaintext, err := sd.d.files.UnsealAtRest(chat.ChatID(req.ChatID), req.MsgID)
	if err != nil {
		sendError(sess, f.ID, "unseal_failed", err.Error())
		return
	}

	sniffed, matches, err := files.SniffPlaintextMIME(plaintext, meta.Mime)
	if err != nil {

		slog.Warn("image_stream: SniffPlaintextMIME failed",
			slog.String("chat_id", req.ChatID),
			slog.String("msg_id", req.MsgID),
			slog.Any("err", err))
		sniffed, matches = "", true
	}

	encoded := base64.StdEncoding.EncodeToString(plaintext)

	for i := range plaintext {
		plaintext[i] = 0
	}

	resp, err := ipc.NewFrame(ipc.FrameImageStreamReady, f.ID, ipc.ImageStreamReadyResponse{
		BytesB64:    encoded,
		SniffedMIME: sniffed,
		MIMEMatches: matches,
	})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send image_stream_ready frame failed", slog.Any("err", err))
	}
}

func (sd *sessionDispatcher) handleWipeOpenTransient(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle wipe_open_transient")
	_ = ctx
	var req ipc.WipeOpenTransientRequest
	if len(f.Payload) > 0 {
		if err := json.Unmarshal(f.Payload, &req); err != nil {
			sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
			return
		}
	}
	if sd.d.files == nil {
		sendError(sess, f.ID, "not_ready", "files manager not wired")
		return
	}
	if err := sd.d.files.WipeOpenTransientFor(req.MsgID); err != nil {
		sendError(sess, f.ID, "wipe_failed", err.Error())
		return
	}
	resp, err := ipc.NewFrame(ipc.FrameOpenTransientWiped, f.ID, ipc.OpenTransientWipedResponse{})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send open_transient_wiped frame failed", slog.Any("err", err))
	}
}

func generateSavedFilename(originalName string) (string, error) {
	const maxExtLen = 16

	ext := filepath.Ext(originalName)
	var sanitized strings.Builder
	for _, r := range ext {
		switch {
		case r >= 'a' && r <= 'z':
			sanitized.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			sanitized.WriteRune(r)
		case r >= '0' && r <= '9':
			sanitized.WriteRune(r)
		case r == '.':
			sanitized.WriteRune(r)
		}
		if sanitized.Len() >= maxExtLen {
			break
		}
	}
	cleanExt := sanitized.String()

	if cleanExt == "." {
		cleanExt = ""
	}

	var randBytes [8]byte
	if _, err := rand.Read(randBytes[:]); err != nil {
		return "", fmt.Errorf("save-as: rand: %w", err)
	}
	return "haoma-" + hex.EncodeToString(randBytes[:]) + cleanExt, nil
}

func (sd *sessionDispatcher) pushInitialBackendStatus(_ context.Context, sess *ipc.Session) {
	p := sd.d.latestStatus.Load()
	if p == nil {
		return
	}
	f, err := ipc.NewFrame(ipc.FrameBackendStatus, "", *p)
	if err != nil {
		slog.Warn("initial backend_status build failed", slog.Any("err", err))
		return
	}
	if err := sess.Send(f); err != nil {
		slog.Debug("initial backend_status send failed", slog.Any("err", err))
	}
}

func (sd *sessionDispatcher) pushInitialPresence(sess *ipc.Session) {
	if sd.d.presenceCache == nil {
		return
	}
	send := func(t ipc.FrameType, payload any, id slog.Attr) bool {
		f, err := ipc.NewFrame(t, "", payload)
		if err != nil {
			slog.Warn("initial presence build failed",
				slog.String("type", string(t)),
				id,
				slog.Any("err", err),
			)
			return true
		}
		if err := sess.Send(f); err != nil {
			slog.Debug("initial presence send failed",
				slog.String("type", string(t)),
				id,
				slog.Any("err", err),
			)
			return false
		}
		return true
	}
	rows := sd.d.presenceCache.All()
	for peerID, snap := range rows {
		if !send(ipc.FramePresenceChanged, presencePayload(peerID, snap), slog.String("peer_id", peerID)) {
			return
		}
	}
	sent := map[chat.ChatID]bool{}
	for peerID := range rows {
		dc := sd.d.chatOfPeer(peerID)
		if dc == nil || sent[dc.ID] {
			continue
		}
		sent[dc.ID] = true
		snap, ok := sd.d.chatPresence(dc)
		if !ok {
			continue
		}
		if !send(ipc.FrameChatPresenceChanged, chatPresencePayload(dc.ID, snap), slog.String("chat_id", string(dc.ID))) {
			return
		}
	}
}

func (sd *sessionDispatcher) handleInviteDHT(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle invite_dht")
	if sd.d.signalState == nil || sd.d.backendClient == nil {
		sendError(sess, f.ID, "not_ready", "frontend wiring incomplete")
		return
	}

	minted, err := sd.d.backendClient.MintOnion(ctx)
	if err != nil {
		sendError(sess, f.ID, "backend_unavailable", fmt.Sprintf("mint persistent onion: %v", err))
		return
	}
	if minted.Address == "" || minted.PrivateKey == "" {
		sendError(sess, f.ID, "not_ready", "haomad returned empty mint")
		return
	}

	inv, mine, err := pair.Build(sd.d.signalState, sd.d.stores, []string{minted.Address}, sd.effectiveNick(""), nil)
	if err != nil {
		sendError(sess, f.ID, "build_failed", err.Error())
		return
	}

	raw, err := inv.Marshal()
	if err != nil {
		sendError(sess, f.ID, "encode_failed", err.Error())
		return
	}
	pubResp, err := sd.d.backendClient.DHTInvite(ctx, raw, inv.Secret)
	if err != nil {
		sendError(sess, f.ID, "dht_publish_failed", err.Error())
		return
	}

	if err := pair.SaveMyKeys(sd.d.store, pubResp.GUID, mine, minted); err != nil {
		slog.Warn("stash invite-dht mykeys failed",
			slog.String("guid", pubResp.GUID),
			slog.String("peer_id", mine.PeerID),
			slog.Any("err", err),
		)
	}
	slog.Info("pair: published via DHT",
		slog.String("guid", pubResp.GUID),
		slog.String("peer_id", inv.PeerID),
	)
	resp, err := ipc.NewFrame(ipc.FrameInvitedDHT, f.ID, ipc.InvitedDHTResponse{
		GUID:            pubResp.GUID,
		IDWords:         pubResp.IDWords,
		PassphraseWords: pubResp.PassphraseWords,
		ExpiresAt:       pubResp.ExpiresAt,
	})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send invited_dht frame failed", slog.Any("err", err))
	}
}

func (sd *sessionDispatcher) handleAcceptDHT(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle accept_dht")
	var req ipc.AcceptDHTRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	if len(req.IDWords) == 0 || len(req.PassphraseWords) == 0 {
		sendError(sess, f.ID, "bad_request", "id_words and passphrase_words required")
		return
	}
	if sd.d.signalState == nil || sd.d.stores == nil || sd.d.backendClient == nil {
		sendError(sess, f.ID, "not_ready", "frontend wiring incomplete")
		return
	}

	slog.Debug("accept_dht: step 1 — DHT fetch", slog.Int("id_words", len(req.IDWords)))
	bootstrap, err := sd.d.backendClient.DHTFetchBootstrap(ctx, req.IDWords, req.PassphraseWords)
	if err != nil {
		sendError(sess, f.ID, "dht_fetch_failed", err.Error())
		return
	}
	slog.Debug("accept_dht: step 1 ok — got bootstrap",
		slog.String("onion", bootstrap.OnionURL),
		slog.String("guid", bootstrap.GUID),
	)

	slog.Debug("accept_dht: step 2 — tor GET /pair", slog.String("onion", bootstrap.OnionURL))
	inviteBytes, err := sd.d.backendClient.DHTProxyFetch(ctx, bootstrap.OnionURL, bootstrap.GUID)
	if err != nil {

		slog.Warn("accept_dht: step 2 failed", slog.String("reason", "pair_fetch_failed"))
		sendError(sess, f.ID, "pair_fetch_failed", "the rendezvous did not answer")
		return
	}
	slog.Debug("accept_dht: step 2 ok — got invite", slog.Int("bytes", len(inviteBytes)))

	inv, err := pair.Parse(inviteBytes)
	if err != nil {
		sendError(sess, f.ID, "bad_invite", err.Error())
		return
	}
	minted, err := sd.d.backendClient.MintOnion(ctx)
	if err != nil {
		sendError(sess, f.ID, "backend_unavailable", fmt.Sprintf("mint persistent onion: %v", err))
		return
	}
	if minted.Address == "" || minted.PrivateKey == "" {
		sendError(sess, f.ID, "not_ready", "haomad returned empty mint")
		return
	}
	nickname := sd.effectiveNick("")
	returnInv, mine, err := pair.Build(sd.d.signalState, sd.d.stores, []string{minted.Address}, nickname, nil)
	if err != nil {
		sendError(sess, f.ID, "build_return_failed", err.Error())
		return
	}
	slog.Debug("accept_dht: step 3 — importing",
		slog.String("peer_id", inv.PeerID),
	)
	if err := pair.Import(ctx, sd.d.stores, sd.d.backendClient, inv, mine, minted); err != nil {
		sendError(sess, f.ID, "import_failed", err.Error())
		return
	}
	seedPairNick(sd.d, inv.PeerID, inv.Frontend.Nick)
	seedPeerFingerprint(sd.d, inv.PeerID)
	if sd.d.chats != nil {
		if _, _, err := sd.d.createPairedChat(inv.PeerID, true); err != nil {
			slog.Warn("create direct chat after accept_dht failed",
				slog.String("peer_id", inv.PeerID),
				slog.Any("err", err),
			)
		}
	}
	slog.Debug("accept_dht: step 3 ok — libsignal import done")
	returnBytes, err := returnInv.Marshal()
	if err != nil {
		sendError(sess, f.ID, "encode_return_failed", err.Error())
		return
	}
	slog.Debug("accept_dht: step 4 ok — return invite built",
		slog.String("our_peer_id", returnInv.PeerID),
		slog.Int("bytes", len(returnBytes)),
	)

	slog.Debug("accept_dht: step 5 — tor POST /pair/return")
	if err := sd.d.backendClient.DHTProxyReturn(ctx, bootstrap.OnionURL, bootstrap.GUID, inv.Secret, returnBytes); err != nil {

		slog.Warn("accept_dht: step 5 failed", slog.String("reason", "pair_return_failed"))
		sendError(sess, f.ID, "pair_return_failed", "the rendezvous did not accept the reply")
		return
	}
	slog.Debug("accept_dht: step 5 ok — return invite posted to Bob")

	addr := protocol.NewSignalAddress(inv.PeerID, pair.DeviceID)
	remoteKey, err := sd.d.stores.GetRemoteIdentity(addr)
	fingerprint := ""
	if err == nil {
		fingerprint = remoteKey.Fingerprint()
	}

	slog.Info("pair: accepted via DHT",
		slog.String("peer_id", inv.PeerID),
	)
	resp, err := ipc.NewFrame(ipc.FrameAcceptedDHT, f.ID, ipc.AcceptedDHTResponse{
		PeerID:              inv.PeerID,
		Nick:                inv.Frontend.Nick,
		IdentityFingerprint: fingerprint,
	})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send accepted_dht frame failed", slog.Any("err", err))
	}
	pushPeerPaired(ctx, sd.d, inv.PeerID, "dht-accept")
}

func (sd *sessionDispatcher) handleCancelDHT(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle cancel_dht")
	var req ipc.CancelDHTRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	if req.GUID == "" {
		sendError(sess, f.ID, "bad_request", "guid required")
		return
	}
	if sd.d.backendClient == nil {
		sendError(sess, f.ID, "not_ready", "backend not wired")
		return
	}
	if err := sd.d.backendClient.DHTCancel(ctx, req.GUID); err != nil {
		sendError(sess, f.ID, "revoke_failed", err.Error())
		return
	}
	resp, err := ipc.NewFrame(ipc.FrameCancelledDHT, f.ID, nil)
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send cancelled_dht frame failed", slog.Any("err", err))
	}
}

func (sd *sessionDispatcher) handlePairOnionInvite(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle pair_onion_invite")
	var req ipc.PairOnionInviteRequest
	if len(f.Payload) > 0 {
		if err := json.Unmarshal(f.Payload, &req); err != nil {
			sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
			return
		}
	}
	if sd.d.signalState == nil || sd.d.backendClient == nil || sd.d.stores == nil {
		sendError(sess, f.ID, "not_ready", "frontend wiring incomplete")
		return
	}

	minted, err := sd.d.backendClient.MintOnion(ctx)
	if err != nil {
		sendError(sess, f.ID, "backend_unavailable", fmt.Sprintf("mint persistent onion: %v", err))
		return
	}
	if minted.Address == "" || minted.PrivateKey == "" {
		sendError(sess, f.ID, "not_ready", "haomad returned empty mint")
		return
	}

	inv, mine, err := pair.Build(sd.d.signalState, sd.d.stores, []string{minted.Address}, sd.effectiveNick(req.Nick), nil)
	if err != nil {
		sendError(sess, f.ID, "build_failed", err.Error())
		return
	}
	raw, err := inv.Marshal()
	if err != nil {
		sendError(sess, f.ID, "encode_failed", err.Error())
		return
	}

	startResp, err := sd.d.backendClient.PairOnionInvite(ctx, raw, 0, false)
	if err != nil {

		slog.Warn("pair: onion invite failed", slog.String("reason", "onion_invite_failed"))
		sendError(sess, f.ID, "onion_invite_failed", "the daemon did not publish a rendezvous")
		return
	}

	if err := pair.SaveMyKeys(sd.d.store, startResp.HandleID, mine, minted); err != nil {
		slog.Warn("stash onion-invite mykeys failed",
			slog.String("handle_id", startResp.HandleID),
			slog.String("peer_id", mine.PeerID),
			slog.Any("err", err),
		)
	}
	slog.Info("pair: onion invite started",
		slog.String("handle_id", startResp.HandleID),
		slog.String("peer_id", inv.PeerID),
	)

	resp, err := ipc.NewFrame(ipc.FramePairOnionStarted, f.ID, ipc.PairOnionStartedResponse{
		HandleID:  startResp.HandleID,
		Words:     startResp.Words,
		ExpiresAt: startResp.ExpiresAt,
	})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send pair_onion_started failed", slog.Any("err", err))
		return
	}

	alias := req.Alias
	handleID := startResp.HandleID
	go sd.runOnionInviteWait(ctx, sess, handleID, alias)
}

func (sd *sessionDispatcher) runOnionInviteWait(ctx context.Context, sess *ipc.Session, handleID, alias string) {
	slog.Debug("pair: onion-wait goroutine entered",
		slog.String("handle_id", handleID),
	)
	slog.Debug("pair: onion-wait calling backend long-poll", slog.String("handle_id", handleID))
	waitResp, err := sd.d.backendClient.PairOnionWait(ctx, handleID)
	if err != nil {
		slog.Warn("pair: onion-wait backend call failed",
			slog.String("handle_id", handleID),
		)
		sd.pushOnionFailed(sess, handleID, "wait_failed", "the wait ended without anyone arriving")
		return
	}
	slog.Debug("pair: onion-wait backend returned",
		slog.String("handle_id", handleID),
		slog.Int("joiner_payload_bytes", len(waitResp.JoinerPayload)),
	)

	inv, err := pair.Parse(waitResp.JoinerPayload)
	if err != nil {
		slog.Warn("pair: onion-wait parse joiner payload failed",
			slog.String("handle_id", handleID),
			slog.Any("err", err),
		)
		sd.pushOnionFailed(sess, handleID, "bad_payload", err.Error())
		return
	}
	slog.Debug("pair: onion-wait parsed joiner invite",
		slog.String("handle_id", handleID),
		slog.String("peer_id", inv.PeerID),
		slog.Int("joiner_addresses", len(inv.Addresses)),
	)

	importCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	mine, minted, err := pair.LoadMyKeys(sd.d.store, handleID)
	if err != nil {
		slog.Warn("pair: onion-wait load mykeys failed",
			slog.String("handle_id", handleID),
			slog.Any("err", err),
		)
		sd.pushOnionFailed(sess, handleID, "mykeys_missing", err.Error())
		return
	}
	slog.Debug("pair: onion-wait running pair.Import", slog.String("handle_id", handleID), slog.String("peer_id", inv.PeerID))
	if err := pair.Import(importCtx, sd.d.stores, sd.d.backendClient, inv, mine, minted); err != nil {
		slog.Warn("pair: onion-wait pair.Import failed",
			slog.String("handle_id", handleID),
			slog.String("peer_id", inv.PeerID),
			slog.Any("err", err),
		)
		sd.pushOnionFailed(sess, handleID, "import_failed", err.Error())
		return
	}
	if err := pair.DeleteMyKeys(sd.d.store, handleID); err != nil {
		slog.Warn("pair: onion-wait delete mykeys failed",
			slog.String("handle_id", handleID),
			slog.Any("err", err),
		)
	}
	slog.Debug("pair: onion-wait pair.Import ok", slog.String("handle_id", handleID), slog.String("peer_id", inv.PeerID))
	seedPairNick(sd.d, inv.PeerID, inv.Frontend.Nick)
	seedPeerFingerprint(sd.d, inv.PeerID)

	if sd.d.chats != nil {
		if _, _, err := sd.d.createPairedChat(inv.PeerID, true); err != nil {
			slog.Warn("pair: onion-wait create direct chat failed",
				slog.String("peer_id", inv.PeerID),
				slog.Any("err", err),
			)
		} else {
			slog.Debug("pair: onion-wait direct chat created", slog.String("peer_id", inv.PeerID))
		}
	}
	if alias != "" {

		if sd.d.peerMeta != nil {
			if _, err := sd.d.peerMeta.SetAlias(inv.PeerID, alias, time.Now().Unix()); err != nil {
				slog.Debug("pair: onion-wait apply alias failed",
					slog.String("peer_id", inv.PeerID),
					slog.Any("err", err),
				)
			} else {
				slog.Debug("pair: onion-wait alias applied", slog.String("peer_id", inv.PeerID))
				noteContactChangedForPeer(sd.d, inv.PeerID)
			}
		}
	}

	if err := sd.d.sendFirstPresence(ctx, inv.PeerID, "tor-invite"); err != nil {
		slog.Warn("pair: onion-wait initiator presence ship failed",
			slog.String("peer_id", inv.PeerID),
			slog.Any("err", err),
		)
	}

	addr := protocol.NewSignalAddress(inv.PeerID, pair.DeviceID)
	fingerprint := ""
	if remoteKey, err := sd.d.stores.GetRemoteIdentity(addr); err == nil {
		fingerprint = remoteKey.Fingerprint()
	}

	slog.Info("pair: onion invite completed",
		slog.String("handle_id", handleID),
		slog.String("peer_id", inv.PeerID),
		slog.String("fingerprint", fingerprint),
	)
	completedFrame, err := ipc.NewFrame(ipc.FramePairOnionCompleted, "", ipc.PairOnionCompletedPush{
		HandleID:            handleID,
		PeerID:              inv.PeerID,
		Nick:                inv.Frontend.Nick,
		IdentityFingerprint: fingerprint,
	})
	if err != nil {
		slog.Warn("pair: onion-wait encode completed push failed", slog.Any("err", err))
		return
	}
	if err := sess.Send(completedFrame); err != nil {
		slog.Warn("pair: onion-wait send completed push failed",
			slog.String("handle_id", handleID),
			slog.Any("err", err),
		)
		return
	}
	slog.Debug("pair: onion-wait completed push sent", slog.String("handle_id", handleID))
	pushPeerPaired(ctx, sd.d, inv.PeerID, "tor-invite")
}

func (sd *sessionDispatcher) pushOnionFailed(sess *ipc.Session, handleID, reason, detail string) {
	detail = clientDetail(reason, detail)
	slog.Info("pair: onion invite failed",
		slog.String("handle_id", handleID),
		slog.String("reason", reason),
		slog.String("detail", detail),
	)
	push, err := ipc.NewFrame(ipc.FramePairOnionFailed, "", ipc.PairOnionFailedPush{
		HandleID: handleID,
		Reason:   reason,
		Detail:   detail,
	})
	if err != nil {
		slog.Warn("encode pair_onion_failed push", slog.Any("err", err))
		return
	}
	if sess != nil {
		if err := sess.Send(push); err != nil {
			slog.Warn("send pair_onion_failed push", slog.Any("err", err))
		}
	}
}

func (sd *sessionDispatcher) handlePairOnionAccept(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle pair_onion_accept")
	var req ipc.PairOnionAcceptRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	if len(req.Words) == 0 {
		sendError(sess, f.ID, "bad_request", "words required")
		return
	}
	if sd.d.signalState == nil || sd.d.stores == nil || sd.d.backendClient == nil {
		sendError(sess, f.ID, "not_ready", "frontend wiring incomplete")
		return
	}

	minted, err := sd.d.backendClient.MintOnion(ctx)
	if err != nil {
		sendError(sess, f.ID, "backend_unavailable", fmt.Sprintf("mint persistent onion: %v", err))
		return
	}
	if minted.Address == "" || minted.PrivateKey == "" {
		sendError(sess, f.ID, "not_ready", "haomad returned empty mint")
		return
	}
	joinerInv, mine, err := pair.Build(sd.d.signalState, sd.d.stores, []string{minted.Address}, sd.effectiveNick(req.Nick), nil)
	if err != nil {
		releaseJoinOnion(sd.d, slog.Default(), minted.Address)
		sendError(sess, f.ID, "build_failed", err.Error())
		return
	}
	joinerBytes, err := joinerInv.Marshal()
	if err != nil {
		releaseJoinOnion(sd.d, slog.Default(), minted.Address)
		sendError(sess, f.ID, "encode_failed", err.Error())
		return
	}

	slog.Debug("pair_onion_accept: dialing handshake")
	acceptResp, err := sd.d.backendClient.PairOnionAccept(ctx, req.Words, "", joinerBytes)
	if err != nil {

		reason := joinRefusal(err, ipc.ReasonOnionAcceptFailed)
		slog.Warn("pair: onion accept failed", slog.String("reason", reason))
		releaseJoinOnion(sd.d, slog.Default(), minted.Address)
		sendError(sess, f.ID, reason, "the rendezvous did not answer")
		return
	}

	inviterInv, err := pair.Parse(acceptResp.InviterPayload)
	if err != nil {
		releaseJoinOnion(sd.d, slog.Default(), minted.Address)
		sendError(sess, f.ID, "bad_invite", err.Error())
		return
	}

	if err := pair.ImportResponder(ctx, sd.d.stores, sd.d.backendClient, inviterInv, mine, minted); err != nil {
		sendError(sess, f.ID, "import_failed", err.Error())
		return
	}
	seedPairNick(sd.d, inviterInv.PeerID, inviterInv.Frontend.Nick)
	seedPeerFingerprint(sd.d, inviterInv.PeerID)
	if sd.d.chats != nil {
		if _, _, err := sd.d.createPairedChat(inviterInv.PeerID, false); err != nil {
			slog.Warn("create direct chat after onion accept failed",
				slog.String("peer_id", inviterInv.PeerID),
				slog.Any("err", err),
			)
		}
	}
	if req.Alias != "" && sd.d.peerMeta != nil {
		if _, err := sd.d.peerMeta.SetAlias(inviterInv.PeerID, req.Alias, time.Now().Unix()); err != nil {
			slog.Debug("apply alias after onion accept failed",
				slog.String("peer_id", inviterInv.PeerID),
				slog.Any("err", err),
			)
		} else {
			noteContactChangedForPeer(sd.d, inviterInv.PeerID)
		}
	}

	addr := protocol.NewSignalAddress(inviterInv.PeerID, pair.DeviceID)
	fingerprint := ""
	if remoteKey, err := sd.d.stores.GetRemoteIdentity(addr); err == nil {
		fingerprint = remoteKey.Fingerprint()
	}

	slog.Info("pair: onion accepted",
		slog.String("peer_id", inviterInv.PeerID),
	)
	resp, err := ipc.NewFrame(ipc.FramePairOnionAccepted, f.ID, ipc.PairOnionAcceptedResponse{
		PeerID:              inviterInv.PeerID,
		Nick:                inviterInv.Frontend.Nick,
		IdentityFingerprint: fingerprint,
	})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send pair_onion_accepted failed", slog.Any("err", err))
	}
	pushPeerPaired(ctx, sd.d, inviterInv.PeerID, "tor-accept")
}

func (sd *sessionDispatcher) handlePairOnionCancel(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle pair_onion_cancel")
	var req ipc.PairOnionCancelRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	if req.HandleID == "" {
		sendError(sess, f.ID, "bad_request", "handle_id required")
		return
	}
	if sd.d.backendClient == nil {
		sendError(sess, f.ID, "not_ready", "frontend wiring incomplete")
		return
	}
	if err := sd.d.backendClient.PairOnionCancel(ctx, req.HandleID); err != nil {
		sendError(sess, f.ID, "onion_cancel_failed", err.Error())
		return
	}
	resp, err := ipc.NewFrame(ipc.FramePairOnionCancelled, f.ID, nil)
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send pair_onion_cancelled failed", slog.Any("err", err))
	}
}

func resolveDirectChatID(d *daemon, peerID string) string {
	if d.chats == nil || peerID == "" {
		return ""
	}
	dc, err := d.chats.GetByDirectPeer(peerID)
	if err != nil {
		slog.Debug("resolveDirectChatID: no chat",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return ""
	}
	return string(dc.ID)
}

func sendError(sess *ipc.Session, id, code, message string) {
	message = clientDetail(code, message)
	slog.Debug("ipc send error",
		slog.String("frame_id", id),
		slog.String("code", code),
		slog.String("message", message),
	)
	ep, _ := ipc.NewFrame(ipc.FrameError, id, ipc.ErrorPayload{Code: code, Message: message})
	_ = sess.Send(ep)
}

var (
	_ = backendapi.HealthResponse{}
	_ = signal.State{}
)
