package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"go.mau.fi/libsignal/protocol"

	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/pair"
	"haoma-frontend/internal/store"
)

func (sd *sessionDispatcher) handleGetPeerFingerprint(sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle get_peer_fingerprint")
	var req ipc.GetPeerFingerprintRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	if req.PeerID == "" {
		sendError(sess, f.ID, "bad_request", "peer_id required")
		return
	}
	if sd.d.stores == nil {
		sendError(sess, f.ID, "not_ready", "signal stores not wired")
		return
	}

	addr := protocol.NewSignalAddress(req.PeerID, pair.DeviceID)
	fingerprint := ""
	remoteKey, err := sd.d.stores.GetRemoteIdentity(addr)
	switch {
	case err == nil:
		fingerprint = remoteKey.Fingerprint()
	case errors.Is(err, store.ErrNotFound):

	default:
		sendError(sess, f.ID, "internal", err.Error())
		return
	}

	resp, err := ipc.NewFrame(ipc.FramePeerFingerprint, f.ID, ipc.PeerFingerprintPayload{
		PeerID:      req.PeerID,
		Fingerprint: fingerprint,
	})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send peer_fingerprint frame failed", slog.Any("err", err))
	}
}
