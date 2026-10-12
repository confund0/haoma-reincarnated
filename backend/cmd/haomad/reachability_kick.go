package main

import (
	"log/slog"
	"time"

	"haoma/internal/peers"
)

const (
	kickSignalInbound   = "inbound envelope"
	kickSignalRepublish = "own onion republished"
	kickSignalSendOK    = "send accepted"
)

const peerKickCooldown = 30 * time.Second

func (d *daemon) allowPeerKick(peerID string, now time.Time) bool {
	d.kickMu.Lock()
	defer d.kickMu.Unlock()
	if d.kickLastAt == nil {
		d.kickLastAt = make(map[string]time.Time)
	}
	if last, ok := d.kickLastAt[peerID]; ok && now.Sub(last) < peerKickCooldown {
		return false
	}
	d.kickLastAt[peerID] = now
	return true
}

func (d *daemon) forgetPeerKick(peerIDs []string, stamped time.Time) {
	if len(peerIDs) == 0 {
		return
	}
	d.kickMu.Lock()
	defer d.kickMu.Unlock()
	for _, id := range peerIDs {

		if last, ok := d.kickLastAt[id]; ok && last.Equal(stamped) {
			delete(d.kickLastAt, id)
		}
	}
}

func peerDests(p *peers.Peer) []string {
	if p == nil {
		return nil
	}
	dests := make([]string, 0, len(p.KnownAddresses))
	for _, addr := range p.KnownAddresses {
		if addr == "" {
			continue
		}
		dests = append(dests, "http://"+addr+".onion")
	}
	return dests
}

func (d *daemon) kickPeers(peerIDs []string, signal string) {
	if d.worker == nil || d.registry == nil || len(peerIDs) == 0 {
		return
	}
	now := time.Now()
	var (
		dests  []string
		kicked []string
	)
	for _, id := range peerIDs {
		p, err := d.registry.Get(id)
		if err != nil {
			slog.Debug("outbox kick: peer not in the registry",
				slog.String("peer_id", id),
				slog.String("signal", signal),
			)
			continue
		}

		pd := peerDests(p)
		if len(pd) == 0 {
			slog.Debug("outbox kick: peer holds no address",
				slog.String("peer_id", id),
				slog.String("signal", signal),
			)
			continue
		}
		if !d.allowPeerKick(id, now) {
			slog.Debug("outbox kick held by the cooldown",
				slog.String("peer_id", id),
				slog.String("signal", signal),
			)
			continue
		}

		slog.Debug("peer reachable again",
			slog.String("peer_id", id),
			slog.String("signal", signal),
			slog.Int("dest_count", len(pd)),
		)
		dests = append(dests, pd...)
		kicked = append(kicked, id)
	}
	if len(dests) == 0 {
		return
	}
	n, err := d.worker.KickByDests(dests)
	if err != nil {

		d.forgetPeerKick(kicked, now)
		slog.Warn("outbox kick failed",
			slog.String("signal", signal),
			slog.Int("peer_count", len(kicked)),
			slog.Any("err", err),
		)
		return
	}

	slog.Debug("outbox next attempt reset",
		slog.String("signal", signal),
		slog.Int("peer_count", len(kicked)),
		slog.Int("dest_count", len(dests)),
		slog.Int("rows_moved", n),
	)
}

func (d *daemon) kickPeerByDest(dest, signal string) {
	if d.registry == nil {
		return
	}
	onion := onionFromDest(dest)
	if onion == "" {
		return
	}
	p, err := d.registry.ByAddress(onion)
	if err != nil {
		slog.Debug("outbox kick: no peer holds this destination",
			slog.String("signal", signal),
		)
		return
	}
	d.kickPeers([]string{p.ID}, signal)
}

func wireReachabilityKicks(d *daemon) {
	if d == nil || d.worker == nil {
		return
	}
	d.worker.OnSendSuccess = func(dest string) {
		d.noteDeliveredDest(dest)
		d.kickPeerByDest(dest, kickSignalSendOK)
	}
}
