package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"haoma-frontend/internal/ipc"
)

func (sd *sessionDispatcher) handleNewCircuitForPeer(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle new_circuit_for_peer")
	var req ipc.NewCircuitForPeerRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	if req.PeerID == "" {
		sendError(sess, f.ID, "bad_request", "peer_id empty")
		return
	}
	if sd.d.backendClient == nil {
		sendError(sess, f.ID, "not_ready", "backend client not wired")
		return
	}
	closed, err := sd.d.backendClient.NewCircuitForPeer(ctx, req.PeerID)
	if err != nil {
		sendError(sess, f.ID, "new_circuit_failed", err.Error())
		return
	}
	resp, err := ipc.NewFrame(ipc.FrameNewCircuitClosed, f.ID, ipc.NewCircuitClosedResponse{
		PeerID: req.PeerID,
		Closed: closed,
	})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send new_circuit_closed frame failed", slog.Any("err", err))
	}
}

func (sd *sessionDispatcher) handlePeerSelfProbe(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle peer_self_probe")
	var req ipc.PeerSelfProbeRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	if req.PeerID == "" {
		sendError(sess, f.ID, "bad_request", "peer_id empty")
		return
	}
	if sd.d.backendClient == nil {
		sendError(sess, f.ID, "not_ready", "backend client not wired")
		return
	}
	res, err := sd.d.backendClient.ProbePeerSelf(ctx, req.PeerID)
	if err != nil {
		sendError(sess, f.ID, "self_probe_failed", err.Error())
		return
	}
	resp, err := ipc.NewFrame(ipc.FramePeerSelfProbed, f.ID, ipc.PeerSelfReachPayload{
		PeerID: res.PeerID,
		Onion:  res.Onion,
		Ok:     res.Ok,
		At:     res.At,
	})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send peer_self_probed frame failed", slog.Any("err", err))
	}
}

func (sd *sessionDispatcher) handleExternalProbeBurst(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle external_probe_burst")
	if sd.d.backendClient == nil {
		sendError(sess, f.ID, "not_ready", "backend client not wired")
		return
	}
	if err := sd.d.backendClient.ExternalProbeBurst(ctx); err != nil {
		sendError(sess, f.ID, "external_probe_burst_failed", err.Error())
		return
	}
	resp, err := ipc.NewFrame(ipc.FrameExternalProbeAccepted, f.ID, ipc.ExternalProbeAcceptedResponse{})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send external_probe_accepted frame failed", slog.Any("err", err))
	}
}
