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
	"haoma-frontend/internal/msg"
)

var callOfferTimeout = 30 * time.Second

var callTimeoutShipBudget = 5 * time.Second

func (sd *sessionDispatcher) handleStartCall(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle start_call")
	var req ipc.StartCallRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	if req.ChatID == "" {
		sendError(sess, f.ID, "bad_request", "chat_id empty")
		return
	}
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
		sendError(sess, f.ID, "unsupported", "v0 calls are 1:1 only — group calls land with ADR-019")
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

	modalities := []string{msg.ModalityAudio}
	outboundKey, tokens, err := mintCallSecrets(modalities)
	if err != nil {
		sendError(sess, f.ID, "build_failed", err.Error())
		return
	}

	for _, modality := range modalities {
		if _, serr := spawnSenderLeg(ctx, sd.d, callID, modality, tokens[modality], outboundKey); serr != nil {
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
	if err := sd.d.calls.PutState(state); err != nil {

		teardownCall(sd.d, calls.State{CallID: callID, LocalTokens: tokens})
		sendError(sess, f.ID, "persist_failed", err.Error())
		return
	}

	broadcastCallStateChanged(sd.d, state)

	if err := shipCallEnvelope(ctx, sd.d, peerID, callID, msg.KindCallOffer, modalities, "", tokens, outboundKey); err != nil {
		slog.Warn("call_offer ship failed",
			slog.String("call_id", callID),
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		failed, terr := sd.d.calls.Transition(callID, calls.StatusFailed, calls.FailReasonSendFail+": "+err.Error(), time.Now().Unix())
		if terr == nil {
			teardownCall(sd.d, failed)
			broadcastCallStateChanged(sd.d, failed)
		}
		sendError(sess, f.ID, "backend_send", err.Error())
		return
	}

	scheduleCallOfferTimeout(sd.d, callID, peerID)

	slog.Info("call started",
		slog.String("call_id", callID),
		slog.String("peer_id", peerID),
		slog.String("chat_id", string(chatID)),
		slog.Any("modalities", modalities),
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
		spawnFailed := ""
		for _, modality := range state.Modalities {
			if _, serr := spawnSenderLeg(ctx, sd.d, state.CallID, modality, tokens[modality], key); serr != nil {
				spawnFailed = "sender:" + serr.Error()
				break
			}
			peerToken := state.RemoteTokens[modality]
			if _, serr := spawnReceiverLeg(ctx, sd.d, state.CallID, state.PeerID, modality, peerToken, state.RemoteOutboundKey); serr != nil {
				spawnFailed = "receiver:" + serr.Error()
				break
			}
		}
		if spawnFailed != "" {
			teardownCall(sd.d, calls.State{CallID: state.CallID, LocalTokens: tokens, RemoteTokens: state.RemoteTokens})
			failed, terr := sd.d.calls.Transition(state.CallID, calls.StatusFailed, calls.FailReasonStreamerSpawn+": "+spawnFailed, time.Now().Unix())
			if terr == nil {
				broadcastCallStateChanged(sd.d, failed)
			}
			sendError(sess, f.ID, "streamer_spawn", spawnFailed)
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

	if err := shipCallEnvelope(ctx, sd.d, state.PeerID, state.CallID, wireKind, state.Modalities, reason, ownTokens, ownKey); err != nil {

		slog.Warn("call response ship failed",
			slog.String("call_id", state.CallID),
			slog.String("peer_id", state.PeerID),
			slog.String("kind", string(wireKind)),
			slog.Any("err", err),
		)
		if req.Action == ipc.CallActionAccept {
			teardownCall(sd.d, updated)
			failed, terr := sd.d.calls.Transition(state.CallID, calls.StatusFailed, calls.FailReasonSendFail+": "+err.Error(), time.Now().Unix())
			if terr == nil {
				broadcastCallStateChanged(sd.d, failed)
			}
		}
		sendError(sess, f.ID, "backend_send", err.Error())
		return
	}

	if updated.IsTerminal() {
		teardownCall(sd.d, updated)
	}

	slog.Info("call response shipped",
		slog.String("call_id", state.CallID),
		slog.String("peer_id", state.PeerID),
		slog.String("kind", string(wireKind)),
		slog.String("status", string(updated.Status)),
		slog.String("reason", reason),
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

func shipCallEnvelope(ctx context.Context, d *daemon, peerID, callID string, kind msg.Kind, modalities []string, reason string, tokens map[string]string, outboundKey []byte) error {
	seq, err := d.peerSeq.NextSendSeq(peerID)
	if err != nil {
		return fmt.Errorf("seq: %w", err)
	}
	msgID, err := msg.NewID()
	if err != nil {
		return fmt.Errorf("msg id: %w", err)
	}
	now := time.Now().Unix()
	var wrapper *msg.Wrapper
	switch kind {
	case msg.KindCallOffer:
		wrapper, err = msg.BuildCallOffer(seq, now, msgID, callID, modalities, tokens, outboundKey, 0)
	case msg.KindCallAccept:
		wrapper, err = msg.BuildCallAccept(seq, now, msgID, callID, modalities, tokens, outboundKey, 0)
	case msg.KindCallReject:
		wrapper, err = msg.BuildCallReject(seq, now, msgID, callID, reason, 0)
	case msg.KindCallEnd:
		wrapper, err = msg.BuildCallEnd(seq, now, msgID, callID, 0)
	default:
		return fmt.Errorf("unsupported call kind %q", kind)
	}
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
	if _, err := d.backendClient.Send(ctx, backendapi.SendRequest{
		PeerID:         peerID,
		Payload:        blob,
		PresenceSource: backendapi.PresenceSourceHaoma,
	}); err != nil {
		return fmt.Errorf("backend send: %w", err)
	}
	return nil
}

func (sd *sessionDispatcher) handleCallControl(_ context.Context, sess *ipc.Session, f ipc.Frame) {
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
	switch req.Action {
	case ipc.CallControlMute, ipc.CallControlUnmute:

	default:
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("action %q must be mute|unmute", req.Action))
		return
	}
	if sd.d.streamers == nil {
		sendError(sess, f.ID, "not_ready", "streamers manager not configured")
		return
	}
	mic := sd.d.streamers.Mic(req.CallID)
	if mic == nil {
		sendError(sess, f.ID, "unknown_call", fmt.Sprintf("no mic streamer for call %s", req.CallID))
		return
	}
	if err := mic.SendCommand(map[string]any{"cmd": req.Action}); err != nil {
		sendError(sess, f.ID, "internal", err.Error())
		return
	}
	slog.Info("call control dispatched",
		slog.String("call_id", req.CallID),
		slog.String("action", req.Action),
	)
	resp, err := ipc.NewFrame(ipc.FrameCallControlled, f.ID, ipc.CallControlledResponse(req))
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send call_controlled reply failed", slog.Any("err", err))
	}
}

func scheduleCallOfferTimeout(d *daemon, callID, peerID string) {
	time.AfterFunc(callOfferTimeout, func() {
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
		updated, err := d.calls.Transition(callID, calls.StatusFailed, calls.FailReasonNoAnswer, time.Now().Unix())
		if err != nil {
			if errors.Is(err, calls.ErrIllegalTransition) {

				slog.Debug("call timeout: lost transition race; no-op",
					slog.String("call_id", callID),
				)
				return
			}
			slog.Warn("call timeout: transition failed",
				slog.String("call_id", callID),
				slog.Any("err", err),
			)
			return
		}
		broadcastCallStateChanged(d, updated)
		slog.Info("call auto-hangup (no answer)",
			slog.String("call_id", callID),
			slog.String("peer_id", peerID),
			slog.Duration("after", callOfferTimeout),
		)

		teardownCall(d, updated)

		ctx, cancel := context.WithTimeout(context.Background(), callTimeoutShipBudget)
		defer cancel()
		if err := shipCallEnvelope(ctx, d, peerID, callID, msg.KindCallEnd, nil, "", nil, nil); err != nil {
			slog.Warn("call timeout: KindCallEnd ship failed",
				slog.String("call_id", callID),
				slog.String("peer_id", peerID),
				slog.Any("err", err),
			)
		}
	})
}
