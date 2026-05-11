package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/rotation"
)

func (sd *sessionDispatcher) handleRotateBegin(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle rotate_begin")
	var req ipc.RotateBeginRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	if req.PeerID == "" {
		sendError(sess, f.ID, "bad_request", "peer_id empty")
		return
	}
	if sd.d.rotation == nil {
		sendError(sess, f.ID, "not_ready", "rotation manager not wired")
		return
	}

	if sd.d.backendClient != nil {
		if peer, perr := sd.d.backendClient.Peer(ctx, req.PeerID); perr == nil {
			now := time.Now().Unix()
			if peer.PrevMyOnionExpiresAt > now {
				retryIn := peer.PrevMyOnionExpiresAt - now
				sendError(sess, f.ID, "rotation_cooldown",
					fmt.Sprintf("previous rotation's grace window still active; retry in %ds", retryIn))
				return
			}
		}
	}
	rotID, err := sd.d.rotation.Begin(ctx, req.PeerID)
	if err != nil {
		switch {
		case errors.Is(err, rotation.ErrInflight):
			sendError(sess, f.ID, "rotation_inflight", err.Error())
		case errors.Is(err, rotation.ErrNotConfigured):
			sendError(sess, f.ID, "not_ready", err.Error())
		default:
			sendError(sess, f.ID, "rotation_begin", err.Error())
		}
		return
	}
	snap, ok := sd.d.rotation.Get(rotID)
	if !ok {
		sendError(sess, f.ID, "rotation_begin", "rotation vanished post-Begin")
		return
	}
	resp, err := ipc.NewFrame(ipc.FrameRotateBegun, f.ID, ipc.RotateBegunResponse{
		RotationID: rotID,
		PeerID:     req.PeerID,
		DeadlineAt: snap.DeadlineAt,
	})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send rotate_begun frame failed", slog.Any("err", err))
	}
}

func (sd *sessionDispatcher) handleRotateUserAccept(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle rotate_user_accept")
	var req ipc.RotateUserAcceptRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	if req.RotationID == "" {
		sendError(sess, f.ID, "bad_request", "rotation_id empty")
		return
	}
	if sd.d.rotation == nil {
		sendError(sess, f.ID, "not_ready", "rotation manager not wired")
		return
	}
	if err := sd.d.rotation.UserAccept(ctx, req.RotationID); err != nil {
		switch {
		case errors.Is(err, rotation.ErrNotFound):
			sendError(sess, f.ID, "rotation_not_found", err.Error())
		case errors.Is(err, rotation.ErrBadState):
			sendError(sess, f.ID, "rotation_bad_state", err.Error())
		default:
			sendError(sess, f.ID, "rotation_user_accept", err.Error())
		}
	}
}

func (sd *sessionDispatcher) handleRotateUserDecline(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle rotate_user_decline")
	var req ipc.RotateUserDeclineRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	if req.RotationID == "" {
		sendError(sess, f.ID, "bad_request", "rotation_id empty")
		return
	}
	if sd.d.rotation == nil {
		sendError(sess, f.ID, "not_ready", "rotation manager not wired")
		return
	}
	if err := sd.d.rotation.UserDecline(ctx, req.RotationID, req.Reason); err != nil {
		if errors.Is(err, rotation.ErrNotFound) {
			sendError(sess, f.ID, "rotation_not_found", err.Error())
			return
		}
		sendError(sess, f.ID, "rotation_user_decline", err.Error())
	}
}
