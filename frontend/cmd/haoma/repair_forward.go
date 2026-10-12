package main

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"go.mau.fi/libsignal/protocol"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/devicetag"
	"haoma-frontend/internal/mirror"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/pair"
	"haoma-frontend/internal/syncrail"
)

func forwardInvitation(ctx context.Context, d *daemon, siblingPeerID string, body syncrail.InvitationBody) {
	logger := slog.With(
		slog.String("sibling_peer_id", siblingPeerID),
		slog.String("invitation_id", body.ID),
		slog.String("fingerprint", body.Fingerprint),
		slog.String("offering_device", body.OfferingDevice),
		slog.Int64("minted_at", body.MintedAt),
		slog.Int64("expires_at", body.ExpiresAt),
		slog.String("device", devicetag.Tag(body.Device)),
	)
	logger.Debug("invitation: a sibling asked me to pass one on")

	if _, running := d.passOnsRunning.LoadOrStore(body.ID, struct{}{}); running {
		logger.Debug("invitation: already being passed on — no state change (redelivery while it runs)",
			slog.String("outcome", "noop_running"),
		)
		return
	}
	defer d.passOnsRunning.Delete(body.ID)

	applied, err := railMirrorApplied(d.store, body.ID)
	if err != nil {
		logger.Warn("invitation: drop — applied-ledger read failed",
			slog.String("reason", "ledger_read_failed"),
			slog.Any("err", err),
		)
		return
	}
	if applied {
		logger.Debug("invitation: already passed on — no state change (idempotent replay)",
			slog.String("outcome", "noop_duplicate"),
		)
		return
	}

	if d.cipher == nil || d.peerSeq == nil || d.backendClient == nil {
		logger.Warn("invitation: drop — frontend wiring incomplete",
			slog.String("reason", reasonNotReady),
		)
		return
	}

	chatID, localPeerID, err := resolveChatByFingerprint(d, body.Fingerprint)
	if err != nil {
		logger.Debug("invitation: drop — no local conversation for this person",
			slog.String("reason", railResolveDropReason(err)),
			slog.Any("err", err),
		)
		return
	}
	if localPeerID == "" {

		peerIDs, perr := d.peerMeta.PeerIDsByFingerprint(body.Fingerprint)
		if perr != nil || len(peerIDs) == 0 {

			logger.Warn("invitation: drop — this device cannot reach that person either",
				slog.String("reason", "no_pairing_here"),
				slog.String("chat_id", string(chatID)),
			)
			return
		}
		logger.Debug("invitation: a pairing with the person is not in the conversation yet",
			slog.String("chat_id", string(chatID)),
			slog.Int("pairings", len(peerIDs)),
		)
	}
	logger = logger.With(slog.String("chat_id", string(chatID)))

	route, ok := awaitPassOnRoute(ctx, d, logger, chatID, localPeerID, body.Fingerprint, body.Device, body.ExpiresAt)
	if !ok {
		return
	}
	localPeerID = route
	logger = logger.With(slog.String("peer_id", localPeerID))

	if !d.isSibling(siblingPeerID) {
		logger.Info("invitation: drop — the device that asked for the pass-on is no longer linked",
			slog.String("reason", "not_sibling"),
		)
		return
	}
	if time.Now().Unix() >= body.ExpiresAt {
		logger.Warn("invitation: drop — the invitation lapsed before it was passed on",
			slog.String("reason", "lapsed"),
		)
		return
	}

	seq, err := d.peerSeq.NextSendSeq(localPeerID)
	if err != nil {
		logger.Warn("invitation: pass-on failed", slog.String("reason", "seq_failed"), slog.Any("err", err))
		return
	}

	wrapper, err := msg.BuildPairingInvitation(seq, time.Now().Unix(), body.ID, body.Secret, body.OfferingDevice, body.MintedAt, body.ExpiresAt, chatRetentionTTL(d, chatID))
	if err != nil {
		logger.Warn("invitation: pass-on failed", slog.String("reason", reasonBuildFailed), slog.Any("err", err))
		return
	}
	defer zeroBytes(wrapper.Body)
	plaintext, err := msg.Marshal(wrapper)
	if err != nil {
		logger.Warn("invitation: pass-on failed", slog.String("reason", reasonBuildFailed), slog.Any("err", err))
		return
	}
	defer zeroBytes(plaintext)

	blob, err := d.cipher.Encrypt(ctx, localPeerID, plaintext)
	if err != nil {
		logger.Warn("invitation: pass-on failed", slog.String("reason", "encrypt_failed"), slog.Any("err", err))
		return
	}

	dispatch := func() (mirror.Result, error) {
		return mirror.Dispatch(ctx, d.backendClient, mirror.NewRail(d.railKey, d.siblings, d.peerMeta),
			mirror.SingleTarget(localPeerID),
			func(ctx context.Context, deviceID string) (backendapi.SendRequest, []byte, error) {

				return backendapi.SendRequest{PeerID: deviceID, Payload: blob}, nil, nil
			},
			mirror.NewOp(wrapper.Kind, ""))
	}

	pauses := d.passOnRetryPauses
	if pauses == nil {
		pauses = passOnRetryPausesDefault
	}
	result, err := dispatch()
	for try := 0; err != nil; try++ {
		if try >= len(pauses) {

			logger.Warn("invitation: pass-on failed — haomad did not take the send; this device gives up",
				slog.String("reason", "backend_send"),
				slog.Int("tries", try+1),
				slog.Any("err", err),
			)
			return
		}
		logger.Info("invitation: haomad did not take the pass-on; it is tried again",
			slog.String("reason", "backend_send"),
			slog.Int("try", try+1),
			slog.Duration("pause", pauses[try]),
			slog.Any("err", err),
		)
		select {
		case <-time.After(pauses[try]):
		case <-ctx.Done():
			logger.Info("invitation: the pass-on stopped with this process before haomad took it",
				slog.String("reason", "stopped"),
			)
			return
		}
		result, err = dispatch()
	}
	slog.Info("invitation: passed on to the person it names",
		slog.String("invitation_id", body.ID),
		slog.String("fingerprint", body.Fingerprint),
		slog.String("envelope_id", result.Primary.EnvelopeID),
		slog.Int64("expires_at", body.ExpiresAt),
	)
	logger.Debug("invitation: pass-on sent — persisting nothing here",
		slog.String("outcome", "forwarded"),
		slog.String("envelope_id", result.Primary.EnvelopeID),
		slog.Uint64("seq", seq),
	)

	if err := markRailMirrorApplied(d.store, body.ID); err != nil {
		logger.Warn("invitation: applied-ledger write failed; a redelivery will pass this on a second time",
			slog.Any("err", err),
		)
	}
}

func awaitPassOnRoute(ctx context.Context, d *daemon, logger *slog.Logger, chatID chat.ChatID, named, fingerprint string, device uint32, expiresAt int64) (route string, ok bool) {
	deadline := time.NewTimer(time.Until(time.Unix(expiresAt, 0)))
	defer deadline.Stop()
	waited := false
	for {

		wake := d.sessionsChanged.wait()
		route, wait, devices := passOnRoute(ctx, d, chatID, named, fingerprint, device)
		if route != "" {
			if waited {
				logger.Info("invitation: a session with the person appeared; the invitation is passed on now",
					slog.String("peer_id", route),
				)
			}
			return route, true
		}
		if !wait {
			logger.Warn("invitation: drop — no pairing here reaches the device of the person the invitation names",
				slog.String("reason", "no_route_to_device"),
				slog.Int("devices", devices),
			)
			return "", false
		}
		if !waited {
			logger.Info("invitation: no pairing with the person holds a session yet; the pass-on waits for one",
				slog.String("reason", "no_session_yet"),
				slog.String("peer_id", named),
				slog.Int64("expires_at", expiresAt),
			)
			waited = true
		}
		select {
		case <-wake:
		case <-deadline.C:
			logger.Warn("invitation: pass-on failed — no pairing with the person held a session before the invitation lapsed",
				slog.String("reason", "no_session"),
				slog.String("peer_id", named),
			)
			return "", false
		case <-ctx.Done():
			logger.Info("invitation: the pass-on stopped with this process before a session appeared",
				slog.String("reason", "stopped"),
				slog.String("peer_id", named),
			)
			return "", false
		}
	}
}

func passOnRoute(ctx context.Context, d *daemon, chatID chat.ChatID, named, fingerprint string, device uint32) (route string, wait bool, devices int) {
	if d.chats == nil || d.peerMeta == nil {
		return "", true, 0
	}
	c, err := d.chats.Get(chatID)
	if err != nil {
		return "", true, 0
	}
	dc, isDirect := c.(*chat.DirectChat)
	if !isDirect {
		return "", true, 0
	}
	routes := dc.FanoutPeerIDs()
	ordered := make([]string, 0, len(routes))
	if named != "" && slices.Contains(routes, named) {
		ordered = append(ordered, named)
	}
	for _, peerID := range routes {
		if peerID != named {
			ordered = append(ordered, peerID)
		}
	}
	var cands []passOnCandidate
	for _, peerID := range ordered {
		if peerID == "" {
			continue
		}
		if fp, err := d.pairingFingerprint(peerID); err != nil || fp != fingerprint {
			continue
		}
		if d.stores == nil {
			cands = append(cands, passOnCandidate{peerID: peerID, session: true})
			continue
		}
		has, err := d.stores.ContainsSession(ctx, protocol.NewSignalAddress(peerID, pair.DeviceID))
		if err != nil || !has {
			cands = append(cands, passOnCandidate{peerID: peerID})
			continue
		}
		dev, ok, err := d.pairingDevice(ctx, peerID)
		if err != nil {
			cands = append(cands, passOnCandidate{peerID: peerID})
			continue
		}
		if !ok {
			dev = 0
		}
		cands = append(cands, passOnCandidate{peerID: peerID, session: true, device: dev})
	}
	return pickPassOnRoute(cands, device)
}

type passOnCandidate struct {
	peerID  string
	session bool

	device uint32
}

func pickPassOnRoute(cands []passOnCandidate, device uint32) (route string, wait bool, devices int) {
	seen := map[uint32]bool{}
	pending := false
	for _, c := range cands {
		if !c.session {
			pending = true
			continue
		}
		if !seen[c.device] {
			seen[c.device] = true
			devices++
		}
		if route == "" && (device == 0 || c.device == device) {
			route = c.peerID
		}
	}
	if route != "" {
		return route, false, devices
	}

	return "", len(cands) == 0 || pending, devices
}

var passOnRetryPausesDefault = []time.Duration{5 * time.Second, 30 * time.Second}
