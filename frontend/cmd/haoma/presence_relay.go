package main

import (
	"context"
	"log/slog"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/presence"
)

func noteSending(d *daemon, entry backendapi.InboxEntry) {
	if d == nil || d.presenceCache == nil || !arrivedInsideTTL(d.presenceCache, entry, time.Now()) {
		return
	}
	d.presenceCache.ObserveSending(entry.PeerID)
}

func noteHumanState(d *daemon, entry backendapi.InboxEntry, state string) {
	if d == nil || d.presenceCache == nil || state == "" || !arrivedInsideTTL(d.presenceCache, entry, time.Now()) {
		return
	}
	d.presenceCache.ObserveHuman(entry.PeerID, state)
}

func arrivedInsideTTL(cache *presence.Cache, entry backendapi.InboxEntry, now time.Time) bool {
	if entry.ArrivalAt <= 0 {
		return false
	}
	age := now.Sub(time.Unix(0, entry.ArrivalAt))
	if age > cache.Staleness() {
		slog.Debug("presence: envelope from a backlog; no presence observation",
			slog.String("peer_id", entry.PeerID),
			slog.String("envelope_id", entry.Envelope.ID),
			slog.Duration("age", age),
		)
		return false
	}
	return true
}

func pushPresenceChanges(ctx context.Context, d *daemon) {
	if d == nil {
		return
	}
	cache, ipcSrv := d.presenceCache, d.ipcSrv
	if cache == nil || ipcSrv == nil {
		return
	}
	wake, drain, cancel := cache.SubscribeDirty()
	defer cancel()
	pushed := map[string]presence.Snapshot{}
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-wake:
			if !ok {
				return
			}
			for _, peerID := range drain() {
				snap := cache.Snapshot(peerID)
				if prev, ok := pushed[peerID]; !ok || prev != snap {
					pushed[peerID] = snap
					slog.Debug("presence change pushed",
						slog.String("peer_id", peerID),
						slog.String("effective", snap.Effective),
						slog.Bool("accepting", snap.Accepting),
						slog.Bool("sending", snap.Sending),
						slog.String("chatty", snap.Chatty),
					)
					push(ipcSrv, ipc.FramePresenceChanged, "", presencePayload(peerID, snap))
				}
				d.pushChatPresenceOfPeer(peerID, "pairing observed")
			}
		}
	}
}
