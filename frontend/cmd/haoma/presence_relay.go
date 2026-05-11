package main

import (
	"context"
	"log/slog"

	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/presence"
)

func pushPresenceChanges(ctx context.Context, cache *presence.Cache, ipcSrv *ipc.Server) {
	if cache == nil {
		return
	}
	ch, cancel := cache.Subscribe(64)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			slog.Debug("presence change pushed",
				slog.String("peer_id", ev.PeerID),
				slog.String("effective", ev.Snapshot.Effective),
				slog.Bool("accepting", ev.Snapshot.Accepting),
				slog.String("chatty", ev.Snapshot.Chatty),
			)
			push(ipcSrv, ipc.FramePresenceChanged, "", ipc.PresenceChangedPayload{
				PeerID:    ev.PeerID,
				Accepting: ev.Snapshot.Accepting,
				Chatty:    ev.Snapshot.Chatty,
				Effective: ev.Snapshot.Effective,
			})
		}
	}
}
