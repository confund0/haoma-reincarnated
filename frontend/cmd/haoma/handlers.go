package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.mau.fi/libsignal/protocol"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/files"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/pair"
	"haoma-frontend/internal/peerstate"
	"haoma-frontend/internal/signal"
)

func peerEntryFor(d *daemon, p backendapi.Peer) ipc.PeerEntry {
	entry := ipc.PeerEntry{
		ID:            p.ID,
		ChatID:        resolveDirectChatID(d, p.ID),
		LastActiveAt:  p.LastActiveAt,
		LastPassiveAt: p.LastPassiveAt,
		RetiredAt:     p.RetiredAt,
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
	applyPresenceSnapshot(d, p.ID, &entry)
	return entry
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
		slog.String("nick", nick),
		slog.Bool("changed", changed),
	)
}

func pushPeerMetaUpdated(ctx context.Context, d *daemon, peerID string) {
	if d == nil || d.ipcSrv == nil || peerID == "" {
		return
	}
	var pp backendapi.Peer
	if d.backendClient != nil {
		if got, err := d.backendClient.Peer(ctx, peerID); err == nil {
			pp = got
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
	entry := peerEntryFor(d, pp)
	push(d.ipcSrv, ipc.FramePeerUpdated, "", ipc.PeerUpdatedPayload{Peer: entry})
	slog.Debug("peer.updated pushed",
		slog.String("peer_id", peerID),
		slog.String("label", entry.Label),
		slog.String("nick", entry.Nick),
		slog.String("alias", entry.Alias),
	)
	if d.chats == nil {
		return
	}
	dc, err := d.chats.GetByDirectPeer(peerID)
	if err != nil {
		return
	}
	chatEntry := chatToEntry(dc)
	applyChatLabel(d, &chatEntry)
	applyChatPresenceSnapshot(d, &chatEntry)
	push(d.ipcSrv, ipc.FrameChatUpdated, "", ipc.ChatUpdatedPayload{Chat: chatEntry})
	slog.Debug("chat.updated pushed",
		slog.String("chat_id", chatEntry.ChatID),
		slog.String("peer_id", peerID),
		slog.String("label", chatEntry.Label),
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
			sd.dispatch(ctx, sess, f)
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
	case ipc.FrameListPeers:
		sd.handleListPeers(ctx, sess, f)
	case ipc.FrameListTimeline:
		sd.handleListTimeline(ctx, sess, f)
	case ipc.FrameListFiles:
		sd.handleListFiles(ctx, sess, f)
	case ipc.FrameSaveFile:
		sd.handleSaveFile(ctx, sess, f)
	case ipc.FrameOpenFile:
		sd.handleOpenFile(ctx, sess, f)
	case ipc.FrameSetAlias:
		sd.handleSetAlias(ctx, sess, f)
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
		slog.String("nick", req.Nick),
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
	if err := pair.DeleteMyKeys(sd.d.store, pasteInPendingHandle); err != nil {
		slog.Warn("delete pending mykeys failed", slog.Any("err", err))
	}

	if sd.d.chats != nil {
		if _, _, err := sd.d.createDirectWithDefaults(inv.PeerID); err != nil {
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
		slog.String("nick", inv.Frontend.Nick),
		slog.String("fingerprint", fingerprint),
	)
	if err := sess.Send(resp); err != nil {
		slog.Warn("send invite_accepted frame failed", slog.Any("err", err))
	}
	push(sd.d.ipcSrv, ipc.FramePeerPaired, "", ipc.PeerPairedPush{
		PeerID: inv.PeerID,
		Source: "paste-file",
	})

	pushPeerMetaUpdated(ctx, sd.d, inv.PeerID)
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

	if _, err := resolveChatForPeer(ctx, sd.d, req.PeerID); err != nil {
		sendError(sess, f.ID, "unknown_peer", fmt.Sprintf("resolve chat for peer %s: %v", req.PeerID, err))
		return
	}
	dc, err := sd.d.chats.GetByDirectPeer(req.PeerID)
	if err != nil {
		sendError(sess, f.ID, "internal", fmt.Sprintf("post-resolve chat lookup for peer %s: %v", req.PeerID, err))
		return
	}

	seq, err := sd.d.peerSeq.NextSendSeq(req.PeerID)
	if err != nil {
		sendError(sess, f.ID, "seq_failed", err.Error())
		return
	}

	msgID, err := msg.NewID()
	if err != nil {
		sendError(sess, f.ID, "build_failed", err.Error())
		return
	}

	expireSeconds := dc.RetentionTTL

	presenceState := sd.d.effectivePresenceState()

	senderNick := sd.d.selfNick()
	wrapper, err := msg.BuildText(seq, time.Now().Unix(), msgID, req.Text, expireSeconds, presenceState, senderNick)
	if err != nil {
		sendError(sess, f.ID, "build_failed", err.Error())
		return
	}
	plaintext, err := msg.Marshal(wrapper)
	if err != nil {
		sendError(sess, f.ID, "build_failed", err.Error())
		return
	}

	blob, err := sd.d.cipher.Encrypt(ctx, req.PeerID, plaintext)
	if err != nil {
		sendError(sess, f.ID, "encrypt_failed", err.Error())
		return
	}

	sendResp, err := sd.d.backendClient.Send(ctx, backendapi.SendRequest{
		PeerID:         req.PeerID,
		Payload:        blob,
		PresenceSource: backendapi.PresenceSourceHaoma,
	})
	if err != nil {
		sendError(sess, f.ID, "backend_send", err.Error())
		return
	}

	if sd.d.events != nil {
		body, marshalErr := json.Marshal(events.TextBody{Text: req.Text})
		if marshalErr != nil {
			slog.Warn("send text: marshal body for timeline failed", slog.Any("err", marshalErr))
		} else if _, persistErr := sd.d.events.AppendOutbound(events.OutboundParams{
			ChatID:        dc.ID,
			Kind:          events.KindText,
			SenderSeq:     seq,
			EnvelopeID:    sendResp.EnvelopeID,
			MsgID:         msgID,
			ExpireSeconds: expireSeconds,
			Body:          body,
		}); persistErr != nil {
			slog.Error("persist outbound timeline event failed",
				slog.String("envelope_id", sendResp.EnvelopeID),
				slog.Any("err", persistErr),
			)
		} else {

			bumpChatActivity(ctx, sd.d, dc.ID, time.Now().Unix())
		}
	}
	slog.Debug("text sent",
		slog.String("peer_id", req.PeerID),
		slog.String("envelope_id", sendResp.EnvelopeID),
		slog.String("msg_id", msgID),
		slog.Uint64("sender_seq", seq),
		slog.Int("payload_bytes", len(blob)),
		slog.String("presence_state", presenceState),
		slog.String("sender_nick", senderNick),
	)

	resp, err := ipc.NewFrame(ipc.FrameTextSent, f.ID, ipc.SendTextResponse{
		EnvelopeID: sendResp.EnvelopeID,
		MsgID:      msgID,
		SenderSeq:  seq,
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

	seq, err := sd.d.peerSeq.NextSendSeq(req.PeerID)
	if err != nil {
		sendError(sess, f.ID, "seq_failed", err.Error())
		return
	}

	editMsgID, err := msg.NewID()
	if err != nil {
		sendError(sess, f.ID, "build_failed", err.Error())
		return
	}

	expireSeconds := dc.RetentionTTL
	wrapper, err := msg.BuildEdit(seq, now, editMsgID, req.TargetMsgID, req.Text, expireSeconds)
	if err != nil {
		sendError(sess, f.ID, "build_failed", err.Error())
		return
	}
	plaintext, err := msg.Marshal(wrapper)
	if err != nil {
		sendError(sess, f.ID, "build_failed", err.Error())
		return
	}

	blob, err := sd.d.cipher.Encrypt(ctx, req.PeerID, plaintext)
	if err != nil {
		sendError(sess, f.ID, "encrypt_failed", err.Error())
		return
	}

	sendResp, err := sd.d.backendClient.Send(ctx, backendapi.SendRequest{
		PeerID:  req.PeerID,
		Payload: blob,
	})
	if err != nil {
		sendError(sess, f.ID, "backend_send", err.Error())
		return
	}

	if _, err := sd.d.events.ApplyEdit(req.TargetMsgID, req.Text, now, ""); err != nil {
		slog.Error("apply edit to local row failed",
			slog.String("target_msg_id", req.TargetMsgID),
			slog.String("envelope_id", sendResp.EnvelopeID),
			slog.Any("err", err),
		)
	}
	slog.Debug("edit sent",
		slog.String("peer_id", req.PeerID),
		slog.String("target_msg_id", req.TargetMsgID),
		slog.String("envelope_id", sendResp.EnvelopeID),
		slog.String("edit_msg_id", editMsgID),
		slog.Uint64("sender_seq", seq),
	)

	resp, err := ipc.NewFrame(ipc.FrameEditSent, f.ID, ipc.SendEditResponse{
		EnvelopeID:  sendResp.EnvelopeID,
		MsgID:       editMsgID,
		SenderSeq:   seq,
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

	seq, err := sd.d.peerSeq.NextSendSeq(req.PeerID)
	if err != nil {
		sendError(sess, f.ID, "seq_failed", err.Error())
		return
	}

	deleteMsgID, err := msg.NewID()
	if err != nil {
		sendError(sess, f.ID, "build_failed", err.Error())
		return
	}

	expireSeconds := dc.RetentionTTL
	wrapper, err := msg.BuildDelete(seq, now, deleteMsgID, req.TargetMsgID, expireSeconds)
	if err != nil {
		sendError(sess, f.ID, "build_failed", err.Error())
		return
	}
	plaintext, err := msg.Marshal(wrapper)
	if err != nil {
		sendError(sess, f.ID, "build_failed", err.Error())
		return
	}

	blob, err := sd.d.cipher.Encrypt(ctx, req.PeerID, plaintext)
	if err != nil {
		sendError(sess, f.ID, "encrypt_failed", err.Error())
		return
	}

	sendResp, err := sd.d.backendClient.Send(ctx, backendapi.SendRequest{
		PeerID:  req.PeerID,
		Payload: blob,
	})
	if err != nil {
		sendError(sess, f.ID, "backend_send", err.Error())
		return
	}

	if _, err := sd.d.events.ApplyDelete(req.TargetMsgID, now, ""); err != nil {
		slog.Error("apply delete to local row failed",
			slog.String("target_msg_id", req.TargetMsgID),
			slog.String("envelope_id", sendResp.EnvelopeID),
			slog.Any("err", err),
		)
	}
	slog.Debug("delete sent",
		slog.String("peer_id", req.PeerID),
		slog.String("target_msg_id", req.TargetMsgID),
		slog.String("envelope_id", sendResp.EnvelopeID),
		slog.String("delete_msg_id", deleteMsgID),
		slog.Uint64("sender_seq", seq),
	)

	resp, err := ipc.NewFrame(ipc.FrameDeleteSent, f.ID, ipc.SendDeleteResponse{
		EnvelopeID:  sendResp.EnvelopeID,
		MsgID:       deleteMsgID,
		SenderSeq:   seq,
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

	seq, err := sd.d.peerSeq.NextSendSeq(req.PeerID)
	if err != nil {
		sendError(sess, f.ID, "seq_failed", err.Error())
		return
	}

	reactionMsgID, err := msg.NewID()
	if err != nil {
		sendError(sess, f.ID, "build_failed", err.Error())
		return
	}

	now := time.Now().Unix()
	expireSeconds := dc.RetentionTTL
	wrapper, err := msg.BuildReaction(seq, now, reactionMsgID, req.TargetMsgID, req.Emoji, expireSeconds)
	if err != nil {
		sendError(sess, f.ID, "build_failed", err.Error())
		return
	}
	plaintext, err := msg.Marshal(wrapper)
	if err != nil {
		sendError(sess, f.ID, "build_failed", err.Error())
		return
	}

	blob, err := sd.d.cipher.Encrypt(ctx, req.PeerID, plaintext)
	if err != nil {
		sendError(sess, f.ID, "encrypt_failed", err.Error())
		return
	}

	sendResp, err := sd.d.backendClient.Send(ctx, backendapi.SendRequest{
		PeerID:  req.PeerID,
		Payload: blob,
	})
	if err != nil {
		sendError(sess, f.ID, "backend_send", err.Error())
		return
	}

	if _, err := sd.d.events.AppendReactionBreadcrumb(req.TargetMsgID, req.Emoji, "", now); err != nil {
		slog.Error("append local reaction breadcrumb failed",
			slog.String("target_msg_id", req.TargetMsgID),
			slog.String("envelope_id", sendResp.EnvelopeID),
			slog.Any("err", err),
		)
	}
	slog.Debug("reaction sent",
		slog.String("peer_id", req.PeerID),
		slog.String("target_msg_id", req.TargetMsgID),
		slog.String("envelope_id", sendResp.EnvelopeID),
		slog.String("reaction_msg_id", reactionMsgID),
		slog.String("emoji", req.Emoji),
		slog.Uint64("sender_seq", seq),
	)

	resp, err := ipc.NewFrame(ipc.FrameReactionSent, f.ID, ipc.SendReactionResponse{
		EnvelopeID:  sendResp.EnvelopeID,
		MsgID:       reactionMsgID,
		SenderSeq:   seq,
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
		entries[i] = peerEntryFor(sd.d, p)
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
	changed, err := sd.d.peerMeta.SetAlias(req.PeerID, req.Alias)
	if err != nil {
		sendError(sess, f.ID, "internal", fmt.Sprintf("set alias: %v", err))
		return
	}
	slog.Debug("peer-meta alias persisted",
		slog.String("peer_id", req.PeerID),
		slog.String("alias", req.Alias),
		slog.Bool("changed", changed),
	)

	var peerEntry ipc.PeerEntry
	if sd.d.backendClient != nil {
		if p, err := sd.d.backendClient.Peer(ctx, req.PeerID); err == nil {
			peerEntry = peerEntryFor(sd.d, p)
		}
	}
	if peerEntry.ID == "" {
		peerEntry = peerEntryFor(sd.d, backendapi.Peer{ID: req.PeerID})
	}
	if changed {
		pushPeerMetaUpdated(ctx, sd.d, req.PeerID)
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

		if chatID != "" {
			n, err := sd.d.events.DeleteByChat(chatID)
			if err != nil {
				sendError(sess, f.ID, "internal", fmt.Sprintf("clear events: %v", err))
				return
			}
			deleted = n
			if err := sd.d.chats.Delete(chatID); err != nil {
				sendError(sess, f.ID, "internal", fmt.Sprintf("drop chat: %v", err))
				return
			}
		}
		p, err := sd.d.backendClient.PeerAction(ctx, req.PeerID, string(ipc.PeerActionDelete))
		if err != nil {
			sendError(sess, f.ID, "backend_unavailable", fmt.Sprintf("POST /peers/%s/action: %v", req.PeerID, err))
			return
		}
		peer = p

	default:
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("unknown action %q", req.Action))
		return
	}

	peerEntry := peerEntryFor(sd.d, peer)
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
		pushPeerMetaUpdated(ctx, sd.d, req.PeerID)
		slog.Debug("peer.retired broadcast (via peer.updated)",
			slog.String("peer_id", req.PeerID),
		)
	case ipc.PeerActionDelete:
		push(sd.d.ipcSrv, ipc.FramePeerDeleted, "", ipc.PeerDeletedPayload{
			PeerID: req.PeerID,
		})
		slog.Debug("peer.deleted broadcast",
			slog.String("peer_id", req.PeerID),
		)
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
	for peerID, snap := range sd.d.presenceCache.All() {
		f, err := ipc.NewFrame(ipc.FramePresenceChanged, "", ipc.PresenceChangedPayload{
			PeerID:    peerID,
			Accepting: snap.Accepting,
			Chatty:    snap.Chatty,
			Effective: snap.Effective,
		})
		if err != nil {
			slog.Warn("initial presence build failed",
				slog.String("peer_id", peerID),
				slog.Any("err", err),
			)
			continue
		}
		if err := sess.Send(f); err != nil {
			slog.Debug("initial presence send failed",
				slog.String("peer_id", peerID),
				slog.Any("err", err),
			)
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
		slog.String("nick", inv.Frontend.Nick),
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
		sendError(sess, f.ID, "pair_fetch_failed", err.Error())
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
		slog.String("nick", inv.Frontend.Nick),
	)
	if err := pair.Import(ctx, sd.d.stores, sd.d.backendClient, inv, mine, minted); err != nil {
		sendError(sess, f.ID, "import_failed", err.Error())
		return
	}
	seedPairNick(sd.d, inv.PeerID, inv.Frontend.Nick)
	if sd.d.chats != nil {
		if _, _, err := sd.d.createDirectWithDefaults(inv.PeerID); err != nil {
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
		sendError(sess, f.ID, "pair_return_failed", err.Error())
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
		slog.String("nick", inv.Frontend.Nick),
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
	push(sd.d.ipcSrv, ipc.FramePeerPaired, "", ipc.PeerPairedPush{
		PeerID: inv.PeerID,
		Source: "dht-accept",
	})
	pushPeerMetaUpdated(ctx, sd.d, inv.PeerID)
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

	startResp, err := sd.d.backendClient.PairOnionInvite(ctx, raw, 0)
	if err != nil {
		sendError(sess, f.ID, "onion_invite_failed", err.Error())
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
		slog.String("nick", req.Nick),
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
		slog.String("alias", alias),
	)
	slog.Debug("pair: onion-wait calling backend long-poll", slog.String("handle_id", handleID))
	waitResp, err := sd.d.backendClient.PairOnionWait(ctx, handleID)
	if err != nil {
		slog.Warn("pair: onion-wait backend call failed",
			slog.String("handle_id", handleID),
			slog.Any("err", err),
		)
		sd.pushOnionFailed(sess, handleID, "wait_failed", err.Error())
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
		slog.String("joiner_nick", inv.Frontend.Nick),
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

	if sd.d.chats != nil {
		if _, _, err := sd.d.createDirectWithDefaults(inv.PeerID); err != nil {
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
			if _, err := sd.d.peerMeta.SetAlias(inv.PeerID, alias); err != nil {
				slog.Debug("pair: onion-wait apply alias failed",
					slog.String("peer_id", inv.PeerID),
					slog.Any("err", err),
				)
			} else {
				slog.Debug("pair: onion-wait alias applied", slog.String("peer_id", inv.PeerID), slog.String("alias", alias))
			}
		}
	}

	addr := protocol.NewSignalAddress(inv.PeerID, pair.DeviceID)
	fingerprint := ""
	if remoteKey, err := sd.d.stores.GetRemoteIdentity(addr); err == nil {
		fingerprint = remoteKey.Fingerprint()
	}

	slog.Info("pair: onion invite completed",
		slog.String("handle_id", handleID),
		slog.String("peer_id", inv.PeerID),
		slog.String("joiner_nick", inv.Frontend.Nick),
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
	push(sd.d.ipcSrv, ipc.FramePeerPaired, "", ipc.PeerPairedPush{
		PeerID: inv.PeerID,
		Source: "tor-invite",
	})
	pushPeerMetaUpdated(ctx, sd.d, inv.PeerID)
}

func (sd *sessionDispatcher) pushOnionFailed(sess *ipc.Session, handleID, reason, detail string) {
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
		sendError(sess, f.ID, "build_failed", err.Error())
		return
	}
	joinerBytes, err := joinerInv.Marshal()
	if err != nil {
		sendError(sess, f.ID, "encode_failed", err.Error())
		return
	}

	slog.Debug("pair_onion_accept: dialing handshake")
	acceptResp, err := sd.d.backendClient.PairOnionAccept(ctx, req.Words, joinerBytes)
	if err != nil {
		sendError(sess, f.ID, "onion_accept_failed", err.Error())
		return
	}

	inviterInv, err := pair.Parse(acceptResp.InviterPayload)
	if err != nil {
		sendError(sess, f.ID, "bad_invite", err.Error())
		return
	}
	if err := pair.Import(ctx, sd.d.stores, sd.d.backendClient, inviterInv, mine, minted); err != nil {
		sendError(sess, f.ID, "import_failed", err.Error())
		return
	}
	seedPairNick(sd.d, inviterInv.PeerID, inviterInv.Frontend.Nick)
	if sd.d.chats != nil {
		if _, _, err := sd.d.createDirectWithDefaults(inviterInv.PeerID); err != nil {
			slog.Warn("create direct chat after onion accept failed",
				slog.String("peer_id", inviterInv.PeerID),
				slog.Any("err", err),
			)
		}
	}
	if req.Alias != "" && sd.d.peerMeta != nil {
		if _, err := sd.d.peerMeta.SetAlias(inviterInv.PeerID, req.Alias); err != nil {
			slog.Debug("apply alias after onion accept failed",
				slog.String("peer_id", inviterInv.PeerID),
				slog.Any("err", err),
			)
		}
	}

	addr := protocol.NewSignalAddress(inviterInv.PeerID, pair.DeviceID)
	fingerprint := ""
	if remoteKey, err := sd.d.stores.GetRemoteIdentity(addr); err == nil {
		fingerprint = remoteKey.Fingerprint()
	}

	slog.Info("pair: onion accepted",
		slog.String("peer_id", inviterInv.PeerID),
		slog.String("inviter_nick", inviterInv.Frontend.Nick),
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
	push(sd.d.ipcSrv, ipc.FramePeerPaired, "", ipc.PeerPairedPush{
		PeerID: inviterInv.PeerID,
		Source: "tor-accept",
	})
	pushPeerMetaUpdated(ctx, sd.d, inviterInv.PeerID)
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
