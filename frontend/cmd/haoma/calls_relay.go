package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"haoma-frontend/internal/calls"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/ipc"
)

func ingestCallOffer(ctx context.Context, d *daemon, chatID chat.ChatID, peerID, callID string, modalities []string, peerOutboundKey []byte, peerTokens map[string]string) {
	if d.calls == nil {
		slog.Warn("call_offer dropped: calls manager not wired",
			slog.String("call_id", callID),
			slog.String("peer_id", peerID),
		)
		return
	}
	if existing, err := d.calls.GetState(callID); err == nil {
		slog.Debug("call_offer ignored: state already exists",
			slog.String("call_id", callID),
			slog.String("peer_id", peerID),
			slog.String("existing_status", string(existing.Status)),
		)
		return
	}
	state := calls.State{
		CallID:            callID,
		ChatID:            chatID,
		PeerID:            peerID,
		Direction:         calls.DirIn,
		Status:            calls.StatusRinging,
		Modalities:        modalities,
		StartedAt:         time.Now().Unix(),
		RemoteOutboundKey: append([]byte(nil), peerOutboundKey...),
		RemoteTokens:      peerTokens,
	}
	if err := d.calls.PutState(state); err != nil {
		slog.Error("call_offer persist failed",
			slog.String("call_id", callID),
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return
	}
	slog.Info("call_offer ringing",
		slog.String("call_id", callID),
		slog.String("peer_id", peerID),
		slog.String("chat_id", string(chatID)),
		slog.Any("modalities", modalities),
		slog.Bool("has_remote_key", len(peerOutboundKey) > 0),
		slog.Int("remote_token_count", len(peerTokens)),
	)
	broadcastCallStateChanged(d, state)
	_ = ctx
}

func applyCallTransition(d *daemon, callID string, next calls.Status, reason, fromKind string) {
	if d.calls == nil {
		slog.Warn("call transition dropped: calls manager not wired",
			slog.String("call_id", callID),
			slog.String("kind", fromKind),
		)
		return
	}
	state, err := d.calls.Transition(callID, next, reason, time.Now().Unix())
	if err != nil {
		switch {
		case errors.Is(err, calls.ErrCallNotFound):
			slog.Info("inbound call op: state missing (already cleaned or never seen)",
				slog.String("call_id", callID),
				slog.String("kind", fromKind),
			)
		case errors.Is(err, calls.ErrIllegalTransition):
			slog.Warn("inbound call op: illegal transition; dropping",
				slog.String("call_id", callID),
				slog.String("kind", fromKind),
				slog.String("requested", string(next)),
				slog.Any("err", err),
			)
		default:
			slog.Error("inbound call transition failed",
				slog.String("call_id", callID),
				slog.String("kind", fromKind),
				slog.Any("err", err),
			)
		}
		return
	}
	slog.Info("call state changed (inbound)",
		slog.String("call_id", callID),
		slog.String("status", string(state.Status)),
		slog.String("reason", reason),
	)
	broadcastCallStateChanged(d, state)
	if state.IsTerminal() {
		teardownCall(d, state)
	}
}

func applyCallAccept(ctx context.Context, d *daemon, callID string, peerOutboundKey []byte, peerTokens map[string]string) {
	if d.calls == nil {
		slog.Warn("call_accept dropped: calls manager not wired",
			slog.String("call_id", callID),
		)
		return
	}
	state, err := d.calls.GetState(callID)
	if err != nil {
		if errors.Is(err, calls.ErrCallNotFound) {
			slog.Info("call_accept: state missing", slog.String("call_id", callID))
			return
		}
		slog.Error("call_accept: state lookup failed", slog.String("call_id", callID), slog.Any("err", err))
		return
	}
	if state.Direction != calls.DirOut {
		slog.Warn("call_accept arrived on non-outbound row; ignoring",
			slog.String("call_id", callID),
			slog.String("direction", string(state.Direction)),
		)
		return
	}
	if state.IsTerminal() {
		slog.Debug("call_accept on terminal row; ignoring",
			slog.String("call_id", callID),
			slog.String("status", string(state.Status)),
		)
		return
	}
	if d.streamers == nil {
		failed, terr := d.calls.Transition(callID, calls.StatusFailed, calls.FailReasonStreamerSpawn+": no streamers manager", time.Now().Unix())
		if terr == nil {
			teardownCall(d, failed)
			broadcastCallStateChanged(d, failed)
		}
		return
	}
	if len(peerOutboundKey) == 0 || len(peerTokens) == 0 {
		failed, terr := d.calls.Transition(callID, calls.StatusFailed, calls.FailReasonInvalidOffer+": peer accept missing tokens / outbound_key (pre-1f peer)", time.Now().Unix())
		if terr == nil {
			teardownCall(d, failed)
			broadcastCallStateChanged(d, failed)
		}
		return
	}
	if err := d.calls.SetRemoteMaterial(callID, peerOutboundKey, peerTokens); err != nil {
		slog.Warn("call_accept: SetRemoteMaterial failed",
			slog.String("call_id", callID),
			slog.Any("err", err),
		)
	}

	state, err = d.calls.GetState(callID)
	if err != nil {
		slog.Error("call_accept: state re-read failed", slog.Any("err", err))
		return
	}

	for _, modality := range state.Modalities {
		peerToken := peerTokens[modality]
		if _, serr := spawnReceiverLeg(ctx, d, callID, state.PeerID, modality, peerToken, peerOutboundKey); serr != nil {
			slog.Warn("call_accept: receiver-leg spawn failed",
				slog.String("call_id", callID),
				slog.String("modality", modality),
				slog.Any("err", serr),
			)
			failed, terr := d.calls.Transition(callID, calls.StatusFailed, calls.FailReasonStreamerSpawn+": "+serr.Error(), time.Now().Unix())
			if terr == nil {
				teardownCall(d, failed)
				broadcastCallStateChanged(d, failed)
			}
			return
		}
	}

	updated, err := d.calls.Transition(callID, calls.StatusAccepted, "", time.Now().Unix())
	if err != nil {
		slog.Warn("call_accept: post-spawn transition failed; tearing down",
			slog.String("call_id", callID),
			slog.Any("err", err),
		)
		teardownCall(d, state)
		return
	}
	broadcastCallStateChanged(d, updated)
	slog.Info("call_accept: accepted, audio flowing",
		slog.String("call_id", callID),
		slog.String("peer_id", state.PeerID),
	)
}

func callStateToEntry(s calls.State) ipc.CallEntry {
	return ipc.CallEntry{
		CallID:     s.CallID,
		ChatID:     string(s.ChatID),
		PeerID:     s.PeerID,
		Direction:  string(s.Direction),
		Status:     string(s.Status),
		Modalities: s.Modalities,
		StartedAt:  s.StartedAt,
		UpdatedAt:  s.UpdatedAt,
		EndedAt:    s.EndedAt,
		FailReason: s.FailReason,
	}
}

func broadcastCallStateChanged(d *daemon, state calls.State) {
	if d == nil || d.ipcSrv == nil {
		return
	}
	push(d.ipcSrv, ipc.FrameCallStateChanged, "", ipc.CallStateChangedPayload{
		Call: callStateToEntry(state),
	})
}
