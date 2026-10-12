package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/logid"
	"haoma-frontend/internal/mirror"
	"haoma-frontend/internal/msg"
)

func chatNickOverrideOf(c chat.Chat) string {
	switch v := c.(type) {
	case *chat.DirectChat:
		return v.NickOverride
	case *chat.GroupChat:
		return v.NickOverride
	default:
		return ""
	}
}

func validateChatNickOverride(s string) (string, error) {
	clean := strings.TrimSpace(s)
	if clean == "" {
		return "", nil
	}
	if len(clean) > selfNickMaxLen {
		return "", fmt.Errorf("nick override too long (max %d chars)", selfNickMaxLen)
	}
	for _, r := range clean {
		if r < 0x20 || r == 0x7f {
			return "", errors.New("nick override must not contain control characters")
		}
	}
	return clean, nil
}

func (sd *sessionDispatcher) handleGetChatSettings(_ context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle get_chat_settings")
	var req ipc.GetChatSettingsRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	if req.ChatID == "" {
		sendError(sess, f.ID, "bad_request", "chat_id required")
		return
	}
	if sd.d.chats == nil {
		sendError(sess, f.ID, "not_ready", "chat store not wired")
		return
	}
	c, err := sd.d.chats.Get(chat.ChatID(req.ChatID))
	if err != nil {
		if errors.Is(err, chat.ErrNotFound) {
			sendError(sess, f.ID, "not_found", fmt.Sprintf("no chat %q", req.ChatID))
			return
		}
		sendError(sess, f.ID, "internal", err.Error())
		return
	}
	resp, err := ipc.NewFrame(ipc.FrameChatSettings, f.ID, ipc.ChatSettingsPayload{
		ChatID:              req.ChatID,
		RetentionTTL:        c.Retention(),
		DisableReadReceipts: c.ReadReceiptsDisabled(),
		NotificationsMuted:  c.IsNotificationsMuted(),
		NickOverride:        chatNickOverrideOf(c),
	})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send chat_settings frame failed", slog.Any("err", err))
	}
}

func (sd *sessionDispatcher) handleSetChatSettings(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle set_chat_settings")
	var req ipc.SetChatSettingsRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	if req.ChatID == "" {
		sendError(sess, f.ID, "bad_request", "chat_id required")
		return
	}
	if sd.d.chats == nil || sd.d.events == nil {
		sendError(sess, f.ID, "not_ready", "chat store or events log not wired")
		return
	}

	cleanOverride, err := validateChatNickOverride(req.NickOverride)
	if err != nil {
		sendError(sess, f.ID, "bad_request", err.Error())
		return
	}

	chatID := chat.ChatID(req.ChatID)
	c, err := sd.d.chats.Get(chatID)
	if err != nil {
		if errors.Is(err, chat.ErrNotFound) {
			sendError(sess, f.ID, "not_found", fmt.Sprintf("no chat %q", req.ChatID))
			return
		}
		sendError(sess, f.ID, "internal", err.Error())
		return
	}
	oldTTL := c.Retention()
	oldDisableReceipts := c.ReadReceiptsDisabled()

	now := time.Now().Unix()
	if err := sd.d.chats.SetRetentionAndTimerTs(chatID, req.RetentionTTL, now); err != nil {
		sendError(sess, f.ID, "internal", err.Error())
		return
	}
	if err := sd.d.chats.SetDisableReadReceipts(chatID, req.DisableReadReceipts); err != nil {
		sendError(sess, f.ID, "internal", err.Error())
		return
	}
	if err := sd.d.chats.SetNotificationsMuted(chatID, req.NotificationsMuted); err != nil {
		sendError(sess, f.ID, "internal", err.Error())
		return
	}
	if err := sd.d.chats.SetNickOverride(chatID, cleanOverride); err != nil {
		sendError(sess, f.ID, "internal", err.Error())
		return
	}

	if oldTTL != req.RetentionTTL {
		body, merr := json.Marshal(events.TimerChangeBody{
			From:      oldTTL,
			To:        req.RetentionTTL,
			ChangedBy: "",
		})
		if merr != nil {
			slog.Warn("marshal timer_change body failed", slog.Any("err", merr))
		} else if _, err := sd.d.events.AppendLocal(events.LocalParams{
			ChatID:        chatID,
			Kind:          events.KindTimerChange,
			Direction:     events.DirOut,
			DisplayTs:     now,
			ExpireSeconds: req.RetentionTTL,
			Body:          body,
		}); err != nil {
			slog.Warn("persist timer_change breadcrumb failed", slog.Any("err", err))
		} else {
			bumpChatActivity(ctx, sd.d, chatID, now)
		}
	}

	if !oldDisableReceipts && req.DisableReadReceipts {
		if n, err := sd.d.events.SuppressPendingReadReceipts(chatID); err != nil {
			slog.Warn("suppress pending read receipts failed",
				slog.String("chat_id", req.ChatID),
				slog.Any("err", err),
			)
		} else if n > 0 {
			slog.Info("read receipts disabled — pending suppressed",
				slog.String("chat_id", req.ChatID),
				slog.Int("suppressed", n),
			)
		}
	}

	pushChatSettings(ctx, sd.d, chatID)

	slog.Info("chat settings updated",
		slog.String("chat_id", req.ChatID),
		slog.Int("retention_ttl_sec", int(req.RetentionTTL)),
		slog.Bool("disable_read_receipts", req.DisableReadReceipts),
		slog.Bool("nick_override_set", cleanOverride != ""),
	)
	slog.Debug("chat settings nick override applied",
		slog.String("chat_id", req.ChatID),
	)

	payload := ipc.ChatSettingsPayload(req)
	payload.NickOverride = cleanOverride
	if sd.d.ipcSrv != nil {
		push(sd.d.ipcSrv, ipc.FrameChatSettings, "", payload)
	}

	resp, err := ipc.NewFrame(ipc.FrameChatSettings, f.ID, payload)
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send chat_settings (set) frame failed", slog.Any("err", err))
	}
}

func (sd *sessionDispatcher) handleMarkRead(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle mark_read")
	var req ipc.MarkReadRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	if req.ChatID == "" {
		sendError(sess, f.ID, "bad_request", "chat_id required")
		return
	}
	if sd.d.events == nil {
		sendError(sess, f.ID, "not_ready", "events log not wired")
		return
	}
	n, pending, err := sd.d.events.MarkRead(chat.ChatID(req.ChatID))
	if err != nil {
		sendError(sess, f.ID, "internal", err.Error())
		return
	}

	clearChatUnread(ctx, sd.d, chat.ChatID(req.ChatID))
	if len(pending) > 0 {
		sd.d.shipReadReceipt(chat.ChatID(req.ChatID), pending)
	}
	resp, err := ipc.NewFrame(ipc.FrameMarkedRead, f.ID, ipc.MarkedReadResponse{
		ChatID:      req.ChatID,
		MarkedCount: n,
	})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send marked_read frame failed", slog.Any("err", err))
	}
}

func (sd *sessionDispatcher) handleClientFocus(ctx context.Context, _ *ipc.Session, f ipc.Frame) {
	slog.Debug("handle client_focus")
	var req ipc.ClientFocusRequest
	if len(f.Payload) > 0 {
		if err := json.Unmarshal(f.Payload, &req); err != nil {
			slog.Warn("client_focus: decode failed", slog.Any("err", err))
			return
		}
	}
	snapshot := req
	sd.d.clientFocus.Store(&snapshot)

	if req.ChatID != "" && req.ScrollPosition == 0 {
		clearChatUnread(ctx, sd.d, chat.ChatID(req.ChatID))

		if sd.d.notifier != nil {
			sd.d.notifier.Dismiss(ctx, req.ChatID)
		}
	}
	if req.ChatID == "" || sd.d.chats == nil {
		return
	}
	c, err := sd.d.chats.Get(chat.ChatID(req.ChatID))
	if err != nil {
		slog.Debug("client_focus: chat lookup for presence push failed",
			slog.String("chat_id", req.ChatID),
			slog.Any("err", err),
		)
		return
	}
	dc, ok := c.(*chat.DirectChat)
	if !ok {
		return
	}
	routes := dc.FanoutPeerIDs()
	if len(routes) == 0 {

		slog.Debug("client_focus: presence push skipped, no pairing with this contact yet",
			slog.String("chat_id", req.ChatID),
		)
		return
	}
	state := sd.d.effectivePresenceState()

	pushCtx, cancel := context.WithTimeout(context.Background(), focusPresencePushTimeout)
	go func(chatID string, routes []string, state string) {
		defer cancel()
		if err := sd.d.shipPresenceToPerson(pushCtx, routes, chatID, state); err != nil {
			slog.Debug("client_focus: presence push failed (Tor warm-up may still be running)",
				slog.String("chat_id", chatID),
				slog.Any("err", err),
			)
		}
	}(string(dc.ID), routes, state)
	slog.Debug("client_focus: presence push fired",
		slog.String("chat_id", string(dc.ID)),
		slog.Int("routing_devices", len(routes)),
		slog.String("state", state),
	)
}

const focusPresencePushTimeout = 2 * time.Minute

func (d *daemon) autoMarkOnArrival(ctx context.Context, chatID chat.ChatID) {
	if d.events == nil {
		return
	}
	focus := d.clientFocus.Load()
	if focus == nil {
		return
	}
	if focus.ChatID == "" || chat.ChatID(focus.ChatID) != chatID {
		return
	}
	if focus.ScrollPosition > 0 {
		return
	}

	clearChatUnread(ctx, d, chatID)
	_, pending, err := d.events.MarkRead(chatID)
	if err != nil {
		slog.Warn("auto-mark on arrival: MarkRead failed",
			slog.String("chat_id", string(chatID)),
			slog.Any("err", err),
		)
		return
	}
	if len(pending) > 0 {
		d.shipReadReceipt(chatID, pending)
	}
}

func (d *daemon) shipReadReceipt(chatID chat.ChatID, targets []events.ReceiptTarget) {
	if len(targets) == 0 {
		return
	}
	if d.chats == nil || d.cipher == nil || d.peerSeq == nil || d.backendClient == nil || d.events == nil {
		slog.Warn("ship read receipt: wiring incomplete; skipping",
			slog.String("chat_id", string(chatID)),
		)
		return
	}
	c, err := d.chats.Get(chatID)
	if err != nil {
		slog.Warn("ship read receipt: chat lookup failed",
			slog.String("chat_id", string(chatID)),
			slog.Any("err", err),
		)
		return
	}
	dc, ok := c.(*chat.DirectChat)
	if !ok {
		slog.Debug("ship read receipt: not a direct chat (group receipts deferred to v1)",
			slog.String("chat_id", string(chatID)),
		)
		return
	}

	if dc.DisableReadReceipts {
		if err := d.events.MarkReadReceiptSent(events.ReceiptMsgIDs(targets), events.ReadReceiptSuppressedSentinel); err != nil {
			slog.Warn("ship read receipt: stamp sentinel on disabled chat failed",
				slog.String("chat_id", string(chatID)),
				slog.Any("err", err),
			)
		}
		slog.Debug("read receipts disabled for this chat — suppressed (sentinel stamped)",
			slog.String("chat_id", string(chatID)),
			slog.Int("suppressed", len(targets)),
		)
		return
	}
	fallback := receiptFallbackPeer(dc)
	if fallback == "" {

		slog.Debug("read receipt skipped: no pairing with this contact yet",
			slog.String("chat_id", string(chatID)),
			slog.Int("pending", len(targets)),
		)
		return
	}

	presenceState := d.effectivePresenceState()
	groups := groupReceiptTargets(dc, fallback, targets)
	slog.Debug("read receipt: targets grouped by the device that sent them",
		slog.String("chat_id", string(chatID)),
		slog.Int("pending", len(targets)),
		slog.Int("groups", len(groups)),
		slog.Int("routing_devices", len(dc.FanoutPeerIDs())),
	)
	sendCtx, cancel := context.WithTimeout(context.Background(), readReceiptShipTimeout)
	defer cancel()

	for _, g := range groups {
		d.shipReadReceiptTo(sendCtx, chatID, dc, g.peerID, g.msgIDs, presenceState)
	}
}

const readReceiptShipTimeout = 2 * time.Minute

func receiptFallbackPeer(dc *chat.DirectChat) string {
	if dc.PeerID != "" {
		return dc.PeerID
	}
	if routes := dc.FanoutPeerIDs(); len(routes) > 0 {
		return routes[0]
	}
	return ""
}

type receiptGroup struct {
	peerID string
	msgIDs []string
}

func groupReceiptTargets(dc *chat.DirectChat, fallback string, targets []events.ReceiptTarget) []receiptGroup {
	var groups []receiptGroup
	index := map[string]int{}
	for _, t := range targets {
		peerID := resolveReceiptPeer(dc, fallback, t)
		i, held := index[peerID]
		if !held {
			i = len(groups)
			index[peerID] = i
			groups = append(groups, receiptGroup{peerID: peerID})
		}
		groups[i].msgIDs = append(groups[i].msgIDs, t.MsgID)
	}
	return groups
}

func resolveReceiptPeer(dc *chat.DirectChat, fallback string, t events.ReceiptTarget) string {
	if t.ArrivedFromPeerID == "" {
		slog.Debug("read receipt addressed to the chat's stored device",
			slog.String("msg_id", t.MsgID),
			slog.String("peer_id", fallback),
			slog.String("source", "row_records_no_sending_device"),
		)
		return fallback
	}
	for _, pid := range dc.FanoutPeerIDs() {
		if pid == t.ArrivedFromPeerID {
			slog.Debug("read receipt addressed to the device that sent the message",
				slog.String("msg_id", t.MsgID),
				slog.String("peer_id", t.ArrivedFromPeerID),
				slog.String("source", "recorded_at_ingest"),
			)
			return t.ArrivedFromPeerID
		}
	}
	slog.Debug("read receipt addressed to the chat's stored device",
		slog.String("msg_id", t.MsgID),
		slog.String("peer_id", fallback),
		slog.String("recorded_peer_id", t.ArrivedFromPeerID),
		slog.String("source", "recorded_device_is_not_in_this_chats_routing_set"),
	)
	return fallback
}

func (d *daemon) shipReadReceiptTo(ctx context.Context, chatID chat.ChatID, dc *chat.DirectChat, peerID string, targets []string, presenceState string) {
	seq, err := d.peerSeq.NextSendSeq(peerID)
	if err != nil {
		slog.Warn("ship read receipt: next seq failed",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return
	}
	receiptMsgID, err := msg.NewID()
	if err != nil {
		slog.Warn("ship read receipt: new id failed", slog.Any("err", err))
		return
	}
	now := time.Now().Unix()

	wrapper, err := msg.BuildRead(seq, now, receiptMsgID, targets, dc.RetentionTTL, presenceState)
	if err != nil {
		slog.Warn("ship read receipt: build wrapper failed", slog.Any("err", err))
		return
	}
	plaintext, err := msg.Marshal(wrapper)
	if err != nil {
		slog.Warn("ship read receipt: marshal failed", slog.Any("err", err))
		return
	}
	blob, err := d.cipher.Encrypt(ctx, peerID, plaintext)
	if err != nil {
		slog.Warn("ship read receipt: encrypt failed",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return
	}
	result, err := mirror.Dispatch(ctx, d.backendClient, mirror.NewRail(d.railKey, d.siblings, d.peerMeta),
		mirror.SingleTarget(peerID),
		func(ctx context.Context, deviceID string) (backendapi.SendRequest, []byte, error) {
			return backendapi.SendRequest{
				PeerID:         deviceID,
				Payload:        blob,
				PresenceSource: backendapi.PresenceSourceHaoma,
			}, plaintext, nil
		},
		mirror.NewOp(wrapper.Kind, string(dc.ID)))
	if err != nil {
		slog.Warn("ship read receipt: backend send failed (will retry on next mark_read)",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return
	}
	if err := d.events.MarkReadReceiptSent(targets, now); err != nil {
		slog.Warn("ship read receipt: stamp ReadReceiptSentAt failed (receipt shipped, local bookkeeping stale)",
			slog.String("envelope_id", result.Primary.EnvelopeID),
			slog.Any("err", err),
		)
	}

	if err := d.events.RecordReceiptEnvelope(result.Primary.EnvelopeID, chatID, targets, now); err != nil {
		slog.Warn("ship read receipt: record envelope mapping failed (a give-up on this envelope will go unnoticed)",
			slog.String("envelope_id", result.Primary.EnvelopeID),
			slog.Any("err", err),
		)
	}

	shortAcked := make([]string, 0, len(targets))
	for _, id := range targets {
		shortAcked = append(shortAcked, logid.Short(id))
	}
	slog.Debug("read receipt shipped",
		slog.String("chat_id", string(chatID)),
		slog.String("peer_id", peerID),
		slog.String("envelope_id", result.Primary.EnvelopeID),
		slog.String("receipt_msg_id", receiptMsgID),
		slog.Uint64("sender_seq", seq),
		slog.Int("targets", len(targets)),
		slog.String("acked_msg_ids", strings.Join(shortAcked, ",")),
	)

	for _, id := range targets {
		slog.Debug("read receipt acknowledges this message",
			slog.String("msg_id", id),
			slog.String("chat_id", string(chatID)),
			slog.String("peer_id", peerID),
			slog.String("receipt_msg_id", receiptMsgID),
			slog.String("envelope_id", result.Primary.EnvelopeID),
		)
	}
	push(d.ipcSrv, ipc.FrameReadSent, "", ipc.ReadSentPayload{
		ChatID:     string(chatID),
		EnvelopeID: result.Primary.EnvelopeID,
		MsgID:      receiptMsgID,
		SenderSeq:  seq,
		Targets:    targets,
	})
}
