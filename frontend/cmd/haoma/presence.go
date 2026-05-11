package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/msg"
)

func (sd *sessionDispatcher) handleSetNick(_ context.Context, sess *ipc.Session, f ipc.Frame) {
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
	push(sd.d.ipcSrv, ipc.FrameNick, "", ipc.NickPayload{
		Nick:      clean,
		IsDefault: clean == defaultSelfNick,
	})
	slog.Info("self-nick updated", slog.String("nick", clean))
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

	var targets []string
	if req.Target != "" {
		targets = []string{req.Target}
	} else {
		resp, err := sd.d.backendClient.Peers(ctx)
		if err != nil {
			sendError(sess, f.ID, "backend_unreachable", err.Error())
			return
		}
		targets = make([]string, 0, len(resp.Peers))
		for _, p := range resp.Peers {
			if p.RetiredAt != 0 {
				continue
			}
			targets = append(targets, p.ID)
		}
	}

	for _, peerID := range targets {
		if err := sd.shipPresenceTo(ctx, peerID, state); err != nil {
			slog.Warn("push_presence: per-peer ship failed",
				slog.String("peer_id", peerID),
				slog.String("state", state),
				slog.Any("err", err),
			)
		}
	}
}

func (sd *sessionDispatcher) shipPresenceTo(ctx context.Context, peerID, state string) error {
	seq, err := sd.d.peerSeq.NextSendSeq(peerID)
	if err != nil {
		return fmt.Errorf("seq: %w", err)
	}
	msgID, err := msg.NewID()
	if err != nil {
		return fmt.Errorf("msg id: %w", err)
	}
	wrapper, err := msg.BuildPresence(seq, time.Now().Unix(), msgID, state, 0)
	if err != nil {
		return fmt.Errorf("build presence: %w", err)
	}
	plaintext, err := msg.Marshal(wrapper)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	blob, err := sd.d.cipher.Encrypt(ctx, peerID, plaintext)
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}
	_, err = sd.d.backendClient.Send(ctx, backendapi.SendRequest{
		PeerID:         peerID,
		Payload:        blob,
		Kind:           backendapi.WireKindPresence,
		PresenceSource: backendapi.PresenceSourceHaoma,
	})
	if err != nil {
		return fmt.Errorf("backend send: %w", err)
	}
	slog.Debug("presence shipped",
		slog.String("peer_id", peerID),
		slog.String("state", state),
		slog.String("msg_id", msgID),
	)
	return nil
}
