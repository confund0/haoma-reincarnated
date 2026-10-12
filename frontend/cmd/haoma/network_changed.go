package main

import (
	"context"
	"log/slog"

	"haoma-frontend/internal/ipc"
)

func (sd *sessionDispatcher) handleNetworkChanged(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	if sd.d.backendClient == nil {
		sendError(sess, f.ID, "not_ready", "backend client not wired")
		return
	}
	res, err := sd.d.backendClient.NetworkChanged(ctx)
	if err != nil {
		slog.Warn("network change: haomad refused the report", slog.Any("err", err))
		sendError(sess, f.ID, "network_changed_failed", err.Error())
		return
	}
	slog.Info("network change: reported to haomad",
		slog.Bool("dropped", res.Dropped),
		slog.Int("rows", res.Rows),
		slog.Int("fetches", res.Fetches),
		slog.Bool("reset_failed", res.ResetFailed),
	)
	resp, err := ipc.NewFrame(ipc.FrameNetworkChangeHandled, f.ID, ipc.NetworkChangeHandledResponse{
		Dropped:      res.Dropped,
		Rows:         res.Rows,
		Destinations: res.Destinations,
		Fetches:      res.Fetches,
	})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send network_change_handled frame failed", slog.Any("err", err))
	}
}
