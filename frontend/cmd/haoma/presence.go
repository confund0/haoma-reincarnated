package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/mirror"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/session"
)

func (sd *sessionDispatcher) handleSetNick(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle set_nick")
	var req ipc.SetNickRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	clean, err := sd.d.setSelfNick(req.Nick)
	if err != nil {
		sendError(sess, f.ID, "bad_nick", err.Error())
		return
	}
	replySet(sess, f.ID, ipc.FrameNick, ipc.NickPayload{
		Nick:      clean,
		IsDefault: sd.d.selfNickIsDefault(),
	})
	sd.d.pushSelfNick()

	pushSelfNickToSiblings(ctx, sd.d)
	slog.Info("self-nick updated")
}

func (sd *sessionDispatcher) handleSetChatFontScale(_ context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle set_chat_font_scale")
	var req ipc.SetChatFontScaleRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	clean, err := sd.d.setChatFontScale(req.Scale)
	if err != nil {
		sendError(sess, f.ID, "persist_failed", err.Error())
		return
	}
	replySet(sess, f.ID, ipc.FrameChatFontScale, ipc.ChatFontScalePayload{Scale: clean})
	push(sd.d.ipcSrv, ipc.FrameChatFontScale, "", ipc.ChatFontScalePayload{
		Scale: clean,
	})
	slog.Debug("chat font-scale updated", slog.Float64("scale", clean))
}

func (sd *sessionDispatcher) handleSetChatDefaults(_ context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle set_chat_defaults")
	var req ipc.SetChatDefaultsRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	if err := sd.d.setChatDefaults(req.RetentionSec, req.SendReceipts); err != nil {
		sendError(sess, f.ID, "persist_failed", err.Error())
		return
	}
	replySet(sess, f.ID, ipc.FrameChatDefaultsChanged, ipc.ChatDefaultsPayload(req))
	push(sd.d.ipcSrv, ipc.FrameChatDefaultsChanged, "", ipc.ChatDefaultsPayload(req))
	slog.Debug("chat defaults updated",
		slog.Uint64("retention_sec", req.RetentionSec),
		slog.Bool("send_receipts", req.SendReceipts),
	)
}

func replySet(sess *ipc.Session, id string, t ipc.FrameType, payload any) {
	if id == "" {
		slog.Debug("set: no correlation id; the broadcast is the only answer", slog.String("type", string(t)))
		return
	}
	resp, err := ipc.NewFrame(t, id, payload)
	if err != nil {
		sendError(sess, id, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("set: the reply to the client that asked was not sent",
			slog.String("type", string(t)), slog.String("corr_id", id), slog.Any("err", err))
		return
	}
	slog.Debug("set: answered the client that asked", slog.String("type", string(t)), slog.String("corr_id", id))
}

func (sd *sessionDispatcher) handleSetPresenceOverride(_ context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle set_presence_override")
	var req ipc.SetPresenceOverrideRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	switch req.State {
	case "", msg.PresenceAvailable, msg.PresenceAway, msg.PresenceBusy:
	default:
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("state must be empty|available|away|busy, got %q", req.State))
		return
	}
	if req.State == "" {
		sd.d.presenceOverride.Store(nil)
	} else {
		s := req.State
		sd.d.presenceOverride.Store(&s)
	}
	slog.Debug("presence override updated", slog.String("state", req.State))
}

func (sd *sessionDispatcher) handlePushPresence(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle push_presence")
	var req ipc.PushPresenceRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	if sd.d.cipher == nil || sd.d.peerSeq == nil || sd.d.backendClient == nil {
		sendError(sess, f.ID, "not_ready", "frontend wiring incomplete (cipher / peer-seq / backend client missing)")
		return
	}

	state := sd.d.effectivePresenceState()

	if req.Target != "" {
		if err := sd.d.shipPresenceToPersonBehind(ctx, req.Target, state); err != nil {
			slog.Warn("push_presence: the targeted send failed",
				slog.String("peer_id", req.Target),
				slog.String("state", state),
				slog.Any("err", err),
			)
		}
		return
	}

	resp, err := sd.d.backendClient.Peers(ctx)
	if err != nil {
		sendError(sess, f.ID, "backend_unreachable", err.Error())
		return
	}

	mine := make(map[string]struct{})
	for _, id := range sd.d.siblings() {
		mine[id] = struct{}{}
	}
	var attempted, enqueued, skippedOwn int
	for _, p := range resp.Peers {
		if p.RetiredAt != 0 {
			continue
		}
		if _, isMine := mine[p.ID]; isMine {
			skippedOwn++
			continue
		}
		attempted++
		if err := sd.d.shipPresenceToDevice(ctx, p.ID, state); err != nil {
			slog.Warn("push_presence: per-device ship failed",
				slog.String("peer_id", p.ID),
				slog.String("state", state),
				slog.Any("err", err),
			)
			continue
		}
		enqueued++
	}
	slog.Debug("push_presence: broadcast walked the peer list",
		slog.String("state", state),
		slog.Int("devices_attempted", attempted),
		slog.Int("devices_enqueued", enqueued),
		slog.Int("own_devices_skipped", skippedOwn),
	)
}

func (d *daemon) shipPresenceToPersonBehind(ctx context.Context, deviceID, state string) error {
	if d.chats == nil {
		slog.Debug("presence: no conversation store; the named device is told on its own",
			slog.String("peer_id", deviceID),
		)
		return d.shipPresenceToDevice(ctx, deviceID, state)
	}
	dc, err := d.chats.GetByDirectPeer(deviceID)
	if err != nil {
		slog.Debug("presence: the named device resolved to no conversation; it is told on its own",
			slog.String("peer_id", deviceID),
			slog.Any("err", err),
		)
		return d.shipPresenceToDevice(ctx, deviceID, state)
	}
	routes := dc.FanoutPeerIDs()
	if len(routes) == 0 {
		slog.Debug("presence: the conversation has no routing set; the named device is told on its own",
			slog.String("peer_id", deviceID),
			slog.String("chat_id", string(dc.ID)),
		)
		return d.shipPresenceToDevice(ctx, deviceID, state)
	}
	slog.Debug("presence: the named device resolved to a conversation",
		slog.String("peer_id", deviceID),
		slog.String("chat_id", string(dc.ID)),
		slog.Int("routing_devices", len(routes)),
	)
	return d.shipPresenceToPerson(ctx, routes, string(dc.ID), state)
}

func (d *daemon) shipPresenceToDevice(ctx context.Context, deviceID, state string) error {
	return d.dispatchPresence(ctx, mirror.SingleTarget(deviceID), "", state)
}

func (d *daemon) sendFirstPresence(ctx context.Context, peerID, source string) error {
	if d.cipher == nil || d.peerSeq == nil {
		return nil
	}
	if err := d.shipPresenceToDevice(ctx, peerID, d.effectivePresenceState()); err != nil {
		return err
	}
	slog.Info("pair: first presence sent so the joiner has a session",
		slog.String("peer_id", peerID),
		slog.String("source", source),
	)
	return nil
}

func (d *daemon) shipUnlinkNotice(ctx context.Context, deviceID, state string, deviceCount int, unlinkedAt int64) error {
	return d.dispatchPresenceWithCount(ctx, mirror.SingleTarget(deviceID), "", state, deviceCount, unlinkedAt)
}

func (d *daemon) shipPresenceToPerson(ctx context.Context, routingSet []string, chatID, state string) error {
	if len(routingSet) == 0 {
		return fmt.Errorf("presence: the conversation has no device to send to")
	}
	return d.dispatchPresence(ctx, mirror.FanoutTargets(routingSet), chatID, state)
}

func (d *daemon) dispatchPresence(ctx context.Context, targets mirror.Targets, chatID, state string) error {

	return d.dispatchPresenceWithCount(ctx, targets, chatID, state, d.deviceCount(), 0)
}

func (d *daemon) dispatchPresenceWithCount(ctx context.Context, targets mirror.Targets, chatID, state string, deviceCount int, unlinkedAt int64) error {
	msgID, err := msg.NewID()
	if err != nil {
		return fmt.Errorf("msg id: %w", err)
	}
	sendTs := time.Now().Unix()

	prepare := func(ctx context.Context, deviceID string) (backendapi.SendRequest, []byte, error) {
		seq, seqErr := d.peerSeq.NextSendSeq(deviceID)
		if seqErr != nil {
			return backendapi.SendRequest{}, nil, fmt.Errorf("seq: %w", seqErr)
		}
		wrapper, bErr := msg.BuildPresence(seq, sendTs, msgID, state, 0, deviceCount, unlinkedAt)
		if bErr != nil {
			return backendapi.SendRequest{}, nil, fmt.Errorf("build presence: %w", bErr)
		}
		plaintext, mErr := msg.Marshal(wrapper)
		if mErr != nil {
			return backendapi.SendRequest{}, nil, fmt.Errorf("marshal: %w", mErr)
		}
		blob, encErr := d.cipher.Encrypt(ctx, deviceID, plaintext)
		if encErr != nil {

			if errors.Is(encErr, session.ErrNoSession) {

				slog.Debug("presence: fan-out skipped a device with no session yet",
					slog.String("peer_id", deviceID),
					slog.String("msg_id", msgID),
				)
			} else {
				slog.Warn("presence: fan-out encrypt to a device failed",
					slog.String("peer_id", deviceID),
					slog.String("msg_id", msgID),
					slog.Any("err", encErr),
				)
			}
			return backendapi.SendRequest{}, nil, fmt.Errorf("encrypt: %w", encErr)
		}
		slog.Debug("presence: addressing a device",
			slog.String("peer_id", deviceID),
			slog.String("chat_id", chatID),
			slog.String("msg_id", msgID),
			slog.String("state", state),

			slog.Int("device_count", deviceCount),
			slog.Int64("unlinked_at", unlinkedAt),
			slog.Uint64("sender_seq", seq),
		)
		return backendapi.SendRequest{
			PeerID:         deviceID,
			Payload:        blob,
			Kind:           backendapi.WireKindPresence,
			PresenceSource: backendapi.PresenceSourceHaoma,
		}, plaintext, nil
	}

	result, err := mirror.Dispatch(ctx, d.backendClient, mirror.NewRail(d.railKey, d.siblings, d.peerMeta),
		targets, prepare, mirror.NewOp(msg.KindPresence, chatID))
	if err != nil {
		return fmt.Errorf("presence: %w", classifyDispatchFailure(err))
	}

	var noSession, outOfTime, failed int
	var outOfTimeErr error
	for _, s := range result.Skipped {
		switch {
		case errors.Is(s.Err, context.Canceled), errors.Is(s.Err, context.DeadlineExceeded):
			outOfTime++
			if outOfTimeErr == nil {
				outOfTimeErr = s.Err
			}
		case errors.Is(s.Err, session.ErrNoSession):
			noSession++
		default:
			failed++
		}
	}
	slog.Debug("presence shipped",
		slog.String("chat_id", chatID),
		slog.String("peer_id", result.Primary.DeviceID),
		slog.String("state", state),
		slog.String("msg_id", msgID),
		slog.Int("devices_enqueued", len(result.EnvelopeIDs)),
		slog.Int("devices_without_session", noSession),
		slog.Int("devices_out_of_time", outOfTime),
		slog.Int("devices_failed", failed),
	)
	if outOfTime > 0 {
		slog.Warn("presence: the push ran out of time; devices were left unsent",
			slog.String("chat_id", chatID),
			slog.String("msg_id", msgID),
			slog.Int("devices_enqueued", len(result.EnvelopeIDs)),
			slog.Int("devices_out_of_time", outOfTime),
		)
		return fmt.Errorf("presence: the push ran out of time; %d device(s) were never sent to: %w", outOfTime, outOfTimeErr)
	}

	if ctxErr := ctx.Err(); ctxErr != nil && failed > 0 {
		slog.Warn("presence: the push had run out of time by the end of the walk; devices were left unsent",
			slog.String("chat_id", chatID),
			slog.String("msg_id", msgID),
			slog.Int("devices_enqueued", len(result.EnvelopeIDs)),
			slog.Int("devices_failed", failed),
		)
		return fmt.Errorf("presence: %d device(s) were not sent to and the push had run out of time by the end of the walk: %w", failed, ctxErr)
	}
	return nil
}

func classifyDispatchFailure(err error) error {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("the push ran out of time before any device was sent to: %w", err)
	case isPrepareFailure(err):
		return fmt.Errorf("nothing reached haomad; no device could be prepared: %w", err)
	default:
		return fmt.Errorf("backend send: %w", err)
	}
}

func isPrepareFailure(err error) bool {
	var dErr *mirror.DispatchError
	return errors.As(err, &dErr) && dErr.Stage == mirror.StagePrepare
}
