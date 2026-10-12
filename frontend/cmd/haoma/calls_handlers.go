package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/calls"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/mirror"
	"haoma-frontend/internal/msg"
)

var callOfferTimeout = 30 * time.Second

var callEndShipTimeout = 5 * time.Second

func resolveStartCallModalities(req []string) ([]string, error) {
	if len(req) == 0 {
		return []string{msg.ModalityAudio}, nil
	}
	seen := make(map[string]struct{}, len(req))
	out := make([]string, 0, len(req))
	for _, m := range req {
		switch m {
		case msg.ModalityAudio, msg.ModalityVideo:
			if _, dup := seen[m]; dup {
				continue
			}
			seen[m] = struct{}{}
			out = append(out, m)
		default:
			return nil, fmt.Errorf("unsupported modality %q (allowed: audio, video)", m)
		}
	}

	if len(out) == 2 {
		out[0], out[1] = msg.ModalityAudio, msg.ModalityVideo
	}
	return out, nil
}

func (sd *sessionDispatcher) handleStartCall(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	var req ipc.StartCallRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	if req.ChatID == "" {
		sendError(sess, f.ID, "bad_request", "chat_id empty")
		return
	}
	slog.Debug("start_call request",
		slog.String("chat_id", req.ChatID),
		slog.Any("modalities_req", req.Modalities),
	)
	if sd.d.calls == nil || sd.d.cipher == nil || sd.d.peerSeq == nil || sd.d.backendClient == nil || sd.d.chats == nil {
		sendError(sess, f.ID, "not_ready", "frontend wiring incomplete (calls / cipher / peer-seq / chats / backend client missing)")
		return
	}
	if sd.d.streamers == nil {
		sendError(sess, f.ID, "not_ready", "streamer binaries not discovered (set --streamer-dir or $HAOMA_STREAMER_DIR)")
		return
	}

	chatID := chat.ChatID(req.ChatID)
	c, err := sd.d.chats.Get(chatID)
	if err != nil {
		sendError(sess, f.ID, "unknown_chat", fmt.Sprintf("chat %s: %v", chatID, err))
		return
	}
	dc, ok := c.(*chat.DirectChat)
	if !ok {
		sendError(sess, f.ID, "unsupported", "calls are 1:1 only; group calls are not supported yet")
		return
	}
	peerID := dc.PeerID
	if peerID == "" {
		sendError(sess, f.ID, "internal", "DirectChat missing peer_id")
		return
	}

	callID, err := calls.NewCallID()
	if err != nil {
		sendError(sess, f.ID, "build_failed", err.Error())
		return
	}

	modalities, mErr := resolveStartCallModalities(req.Modalities)
	if mErr != nil {
		sendError(sess, f.ID, "bad_request", mErr.Error())
		return
	}
	outboundKey, tokens, err := mintCallSecrets(modalities)
	if err != nil {
		sendError(sess, f.ID, "build_failed", err.Error())
		return
	}

	for _, modality := range modalities {
		if _, serr := spawnCallSender(ctx, sd.d, callID, modality, tokens[modality], outboundKey); serr != nil {
			slog.Warn("call sender-leg spawn failed",
				slog.String("call_id", callID),
				slog.String("modality", modality),
				slog.Any("err", serr),
			)
			sendError(sess, f.ID, "streamer_spawn", serr.Error())
			return
		}
	}

	state := calls.State{
		CallID:      callID,
		ChatID:      chatID,
		PeerID:      peerID,
		Direction:   calls.DirOut,
		Status:      calls.StatusOffered,
		Modalities:  modalities,
		StartedAt:   time.Now().Unix(),
		LocalTokens: tokens,
	}

	offerSenderNick := sd.d.selfNick()
	if dc.NickOverride != "" {
		offerSenderNick = dc.NickOverride
	}

	state, code, err := startCallOffer(ctx, sd.d, dc, state, outboundKey, offerSenderNick)
	if err != nil {
		slog.Warn("call_offer ship failed",
			slog.String("call_id", callID),
			slog.String("chat_id", string(chatID)),
			slog.Any("err", err),
		)
		sendError(sess, f.ID, code, clientDetail(code, err.Error()))
		return
	}

	slog.Info("call started",
		slog.String("call_id", callID),
		slog.String("peer_id", peerID),
		slog.String("chat_id", string(chatID)),
		slog.Any("modalities", modalities),
		slog.Int("targets", len(state.Targets)),
	)

	resp, err := ipc.NewFrame(ipc.FrameCallStarted, f.ID, ipc.CallStartedResponse{
		CallID: callID,
		Call:   callStateToEntry(state),
	})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send call_started reply failed", slog.Any("err", err))
	}
}

func (sd *sessionDispatcher) handleRespondCall(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle respond_call")
	var req ipc.RespondCallRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	if req.CallID == "" {
		sendError(sess, f.ID, "bad_request", "call_id empty")
		return
	}
	if sd.d.calls == nil || sd.d.cipher == nil || sd.d.peerSeq == nil || sd.d.backendClient == nil {
		sendError(sess, f.ID, "not_ready", "frontend wiring incomplete (calls / cipher / peer-seq / backend client missing)")
		return
	}

	state, err := sd.d.calls.GetState(req.CallID)
	if err != nil {
		if errors.Is(err, calls.ErrCallNotFound) {
			sendError(sess, f.ID, "unknown_call", fmt.Sprintf("call %s: %v", req.CallID, err))
			return
		}
		sendError(sess, f.ID, "internal", err.Error())
		return
	}
	if state.IsTerminal() {
		sendError(sess, f.ID, "illegal_state", fmt.Sprintf("call %s is %s — no further actions", req.CallID, state.Status))
		return
	}

	var (
		nextStatus calls.Status
		wireKind   msg.Kind
		reason     string
		ownKey     []byte
		ownTokens  map[string]string
	)
	switch req.Action {
	case ipc.CallActionAccept:

		if state.Direction != calls.DirIn {
			sendError(sess, f.ID, "illegal_state", "accept is callee-only; caller cannot self-accept")
			return
		}
		if sd.d.streamers == nil {
			sendError(sess, f.ID, "not_ready", "streamer binaries not discovered (set --streamer-dir or $HAOMA_STREAMER_DIR)")
			return
		}
		if len(state.RemoteOutboundKey) == 0 || len(state.RemoteTokens) == 0 {
			sendError(sess, f.ID, "invalid_offer", "offer missing tokens / outbound_key — peer is on a pre-1f build")
			return
		}

		key, tokens, merr := mintCallSecrets(state.Modalities)
		if merr != nil {
			sendError(sess, f.ID, "build_failed", merr.Error())
			return
		}

		callsAnswering.Store(state.CallID, struct{}{})
		defer callsAnswering.Delete(state.CallID)
		spawnSide, spawnErr := "", error(nil)
		for _, modality := range state.Modalities {
			if _, serr := spawnCallSender(ctx, sd.d, state.CallID, modality, tokens[modality], key); serr != nil {
				spawnSide, spawnErr = "sender", serr
				break
			}
			peerToken := state.RemoteTokens[modality]
			if _, serr := spawnCallReceiver(ctx, sd.d, state.CallID, state.PeerID, modality, peerToken, state.RemoteOutboundKey); serr != nil {
				spawnSide, spawnErr = "receiver", serr
				break
			}
		}
		if spawnErr != nil {
			teardownCall(sd.d, calls.State{CallID: state.CallID, LocalTokens: tokens, RemoteTokens: state.RemoteTokens})

			failed, terr := sd.d.calls.Transition(state.CallID, calls.StatusFailed, calls.FailReasonStreamerSpawn+": "+spawnSide, time.Now().Unix())
			if terr == nil {
				broadcastCallStateChanged(sd.d, failed)
			}
			detail := clientDetail("streamer_spawn", spawnSide+": "+spawnErr.Error())
			slog.Warn("call accept: streamer start failed",
				slog.String("call_id", state.CallID),
				slog.String("side", spawnSide),
				slog.String("err", detail),
			)
			sendError(sess, f.ID, "streamer_spawn", detail)
			return
		}
		rememberLocalTokens(sd.d, state.CallID, tokens)
		ownKey = key
		ownTokens = tokens
		nextStatus = calls.StatusAccepted
		wireKind = msg.KindCallAccept
	case ipc.CallActionReject:
		nextStatus = calls.StatusRejected
		wireKind = msg.KindCallReject
		reason = req.Reason
		if reason == "" {
			reason = msg.CallRejectUserDeclined
		}
	case ipc.CallActionEnd:
		nextStatus = calls.StatusEnded
		wireKind = msg.KindCallEnd

		reason = req.Reason
	default:
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("action %q must be accept|reject|end", req.Action))
		return
	}

	updated, err := sd.d.calls.Transition(req.CallID, nextStatus, reason, time.Now().Unix())
	if err != nil {
		if errors.Is(err, calls.ErrIllegalTransition) {

			if ownTokens != nil {
				teardownCall(sd.d, calls.State{CallID: state.CallID, LocalTokens: ownTokens, RemoteTokens: state.RemoteTokens})
			}
			sendError(sess, f.ID, "illegal_state", err.Error())
			return
		}
		if ownTokens != nil {
			teardownCall(sd.d, calls.State{CallID: state.CallID, LocalTokens: ownTokens, RemoteTokens: state.RemoteTokens})
		}
		sendError(sess, f.ID, "internal", err.Error())
		return
	}
	broadcastCallStateChanged(sd.d, updated)

	respondSenderNick := ""
	if wireKind == msg.KindCallAccept {
		respondSenderNick = callSenderNickForChat(sd.d, chat.ChatID(state.ChatID))
	}

	recipients := callRecipients(updated)
	wire := callWire{
		callID:      state.CallID,
		kind:        wireKind,
		modalities:  state.Modalities,
		reason:      reason,
		tokens:      ownTokens,
		outboundKey: ownKey,
		senderNick:  respondSenderNick,
	}
	if wireKind == msg.KindCallEnd {

		wire.reason = ""
	}
	sent, err := shipCallToDevices(ctx, sd.d, recipients, wire)
	if len(recipients) > 0 && sent == 0 {

		slog.Warn("call response ship failed",
			slog.String("call_id", state.CallID),
			slog.String("peer_id", state.PeerID),
			slog.String("kind", string(wireKind)),
			slog.Int("devices", len(recipients)),
			slog.Any("err", err),
		)
		if req.Action == ipc.CallActionAccept {
			teardownCall(sd.d, updated)
			failed, terr := sd.d.calls.Transition(state.CallID, calls.StatusFailed, calls.FailReasonSendFail+": "+clientDetail(calls.FailReasonSendFail, err.Error()), time.Now().Unix())
			if terr == nil {
				broadcastCallStateChanged(sd.d, failed)
			}
		}
		sendError(sess, f.ID, "backend_send", clientDetail("backend_send", err.Error()))
		return
	}

	if updated.IsTerminal() {
		callTeardown(sd.d, updated)
	}

	slog.Info("call response shipped",
		slog.String("call_id", state.CallID),
		slog.String("peer_id", state.PeerID),
		slog.String("kind", string(wireKind)),
		slog.String("status", string(updated.Status)),
		slog.String("reason", reason),
		slog.Int("devices", len(recipients)),
		slog.Int("sent", sent),
	)

	resp, err := ipc.NewFrame(ipc.FrameCallResponded, f.ID, ipc.CallRespondedResponse{
		CallID: state.CallID,
		Status: string(updated.Status),
	})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send call_responded reply failed", slog.Any("err", err))
	}
}

func callSenderNickForChat(d *daemon, chatID chat.ChatID) string {
	global := d.selfNick()
	if d.chats == nil || chatID == "" {
		return global
	}
	c, err := d.chats.Get(chatID)
	if err != nil {
		return global
	}
	override := chatNickOverrideOf(c)
	if override == "" {
		return global
	}
	return override
}

func (sd *sessionDispatcher) handleCallControl(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle call_control")
	var req ipc.CallControlRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	if req.CallID == "" {
		sendError(sess, f.ID, "bad_request", "call_id empty")
		return
	}
	var side, streamerCmd string
	switch req.Action {
	case ipc.CallControlMute:
		side, streamerCmd = "mic", "mute"
	case ipc.CallControlUnmute:
		side, streamerCmd = "mic", "unmute"
	case ipc.CallControlVideoMute:
		side, streamerCmd = "cam", "mute"
	case ipc.CallControlVideoUnmute:
		side, streamerCmd = "cam", "unmute"
	default:
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("action %q must be mute|unmute|video_mute|video_unmute", req.Action))
		return
	}
	if sd.d.streamers == nil {
		sendError(sess, f.ID, "not_ready", "streamers manager not configured")
		return
	}

	state, stateErr := sd.d.calls.GetState(req.CallID)
	if stateErr != nil {
		if errors.Is(stateErr, calls.ErrCallNotFound) {
			sendError(sess, f.ID, "unknown_call", fmt.Sprintf("no call %s", req.CallID))
			return
		}
		sendError(sess, f.ID, "internal", stateErr.Error())
		return
	}
	if side == "cam" {
		hasVideo := false
		for _, m := range state.Modalities {
			if m == msg.ModalityVideo {
				hasVideo = true
				break
			}
		}
		if !hasVideo {
			sendError(sess, f.ID, "video_not_enabled", fmt.Sprintf("call %s has no video modality", req.CallID))
			return
		}
	}
	var stream interface {
		SendCommand(map[string]any) error
	}
	switch side {
	case "mic":
		if s := sd.d.streamers.Mic(req.CallID); s != nil {
			stream = s
		}
	case "cam":
		if s := sd.d.streamers.Cam(req.CallID); s != nil {
			stream = s
		}
	}
	if stream == nil {
		sendError(sess, f.ID, "internal", fmt.Sprintf("call %s exists but %s streamer is gone", req.CallID, side))
		return
	}
	if err := stream.SendCommand(map[string]any{"cmd": streamerCmd}); err != nil {
		sendError(sess, f.ID, "internal", err.Error())
		return
	}
	slog.Info("call control dispatched",
		slog.String("call_id", req.CallID),
		slog.String("side", side),
		slog.String("action", req.Action),
	)

	if side == "cam" {

		mediaPeer := state.MediaPeer()
		if mediaPeer == "" {
			slog.Warn("call control: no peer id for peer-signal ship",
				slog.String("call_id", req.CallID),
				slog.String("action", req.Action),
			)
		} else {
			if err := shipCallControl(ctx, sd.d, mediaPeer, req.CallID, req.Action); err != nil {
				slog.Warn("call control: peer-signal ship failed (receiver falls back to heuristic)",
					slog.String("call_id", req.CallID),
					slog.String("peer_id", mediaPeer),
					slog.String("action", req.Action),
					slog.Any("err", err),
				)
			} else {
				slog.Debug("call control: peer-signal shipped",
					slog.String("call_id", req.CallID),
					slog.String("peer_id", mediaPeer),
					slog.String("action", req.Action),
				)
			}
		}
	}

	resp, err := ipc.NewFrame(ipc.FrameCallControlled, f.ID, ipc.CallControlledResponse(req))
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send call_controlled reply failed", slog.Any("err", err))
	}
}

func shipCallControl(ctx context.Context, d *daemon, peerID, callID, action string) error {
	seq, err := d.peerSeq.NextSendSeq(peerID)
	if err != nil {
		return fmt.Errorf("seq: %w", err)
	}
	msgID, err := msg.NewID()
	if err != nil {
		return fmt.Errorf("msg id: %w", err)
	}
	wrapper, err := msg.BuildCallControl(seq, time.Now().Unix(), msgID, callID, action, 0)
	if err != nil {
		return fmt.Errorf("build wrapper: %w", err)
	}
	plaintext, err := msg.Marshal(wrapper)
	if err != nil {
		return fmt.Errorf("marshal wrapper: %w", err)
	}
	blob, err := d.cipher.Encrypt(ctx, peerID, plaintext)
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}
	if _, err := mirror.Dispatch(ctx, d.backendClient, mirror.NewRail(d.railKey, d.siblings, d.peerMeta),
		mirror.SingleTarget(peerID),
		func(ctx context.Context, deviceID string) (backendapi.SendRequest, []byte, error) {
			return backendapi.SendRequest{
				PeerID:         deviceID,
				Payload:        blob,
				PresenceSource: backendapi.PresenceSourceHaoma,
			}, plaintext, nil
		},
		mirror.NewOp(wrapper.Kind, "")); err != nil {
		return fmt.Errorf("backend send: %w", err)
	}
	return nil
}

func scheduleCallOfferTimeout(d *daemon, callID string) {
	armCallOfferTimer(d, callID, callOfferTimeout, 0)
}

func armCallOfferTimer(d *daemon, callID string, after time.Duration, rearms int) {
	entry := &offerTimer{}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	callOfferTimers.Store(callID, entry)
	entry.t = time.AfterFunc(after, func() {

		callOfferTimers.CompareAndDelete(callID, entry)
		state, err := d.calls.GetState(callID)
		if err != nil {
			if !errors.Is(err, calls.ErrCallNotFound) {
				slog.Warn("call timeout: state lookup failed",
					slog.String("call_id", callID),
					slog.Any("err", err),
				)
			}
			return
		}
		if state.Status != calls.StatusOffered {
			slog.Debug("call timeout: state already moved on; no-op",
				slog.String("call_id", callID),
				slog.String("status", string(state.Status)),
			)
			return
		}
		if state.AnsweredBy != "" {

			slog.Debug("call timeout: a device answered; no-op",
				slog.String("call_id", callID),
			)
			return
		}

		updated, err := failCallUnanswered(d.calls, callID, calls.FailReasonNoAnswer, time.Now().Unix())
		if err != nil {
			if errors.Is(err, calls.ErrIllegalTransition) {

				slog.Debug("call timeout: lost transition race; no-op",
					slog.String("call_id", callID),
				)
				return
			}
			if rearms < callTimerRearms {
				slog.Warn("call timeout: transition failed; trying again",
					slog.String("call_id", callID),
					slog.Int("attempt", rearms+1),
					slog.Any("err", err),
				)
				armCallOfferTimer(d, callID, callTimerRearmDelay, rearms+1)
				return
			}
			slog.Error("call timeout: transition failed on every try; the row stays offered until the next start",
				slog.String("call_id", callID),
				slog.Any("err", err),
			)
			return
		}
		broadcastCallStateChanged(d, updated)
		slog.Info("call auto-hangup (no answer)",
			slog.String("call_id", callID),
			slog.Duration("after", callOfferTimeout),
		)

		ctx, cancel := context.WithTimeout(context.Background(), callEndShipTimeout)
		defer cancel()

		endCallOnDevices(ctx, d, callID, callRecipients(updated), "", "no answer")

		callTeardown(d, updated)
	})
}
