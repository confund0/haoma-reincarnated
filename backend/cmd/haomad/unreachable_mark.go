package main

import (
	"log/slog"
	"time"

	"haoma/internal/eventbus"
	"haoma/internal/outbox"
	"haoma/internal/peers"
)

const markMinAttempts = 8

type peerUnreachableObservation struct {
	PeerID      string `json:"peer_id"`
	Unreachable bool   `json:"unreachable"`
}

func (d *daemon) peerForDest(dest string) *peers.Peer {
	if d == nil || d.registry == nil {
		return nil
	}
	onion := onionFromDest(dest)
	if onion == "" {
		return nil
	}
	p, err := d.registry.ByAddress(onion)
	if err != nil {
		return nil
	}
	return p
}

func (d *daemon) noteUndeliveredDest(dest string, queuedAt time.Time, countedAttempts int) {
	p := d.peerForDest(dest)
	if p == nil {
		slog.Debug("unreachable mark: no peer holds the destination of an undelivered envelope")
		return
	}
	if countedAttempts < markMinAttempts {
		slog.Debug("unreachable mark: give-up with too few attempts marks nothing",
			slog.String("peer_id", p.ID),
			slog.Int("counted_attempts", countedAttempts),
			slog.Int("needed", markMinAttempts),
		)
		return
	}
	if err := d.registry.NoteUndelivered(p.ID, queuedAt); err != nil {
		slog.Warn("unreachable mark: undelivered envelope not recorded",
			slog.String("peer_id", p.ID),
			slog.Any("err", err),
		)
		return
	}
	d.recheckUnreachable(p.ID, false)
}

func (d *daemon) noteDeliveredDest(dest string) {
	p := d.peerForDest(dest)
	if p == nil {
		return
	}
	if err := d.registry.NoteDelivered(p.ID, time.Now()); err != nil {
		slog.Warn("unreachable mark: delivered send not recorded",
			slog.String("peer_id", p.ID),
			slog.Any("err", err),
		)
		return
	}
	d.recheckUnreachable(p.ID, true)
}

func (d *daemon) noteInboundForMark(peerID string) {
	d.recheckUnreachable(peerID, true)
}

func (d *daemon) recheckUnreachable(peerID string, clearOnly bool) {
	if d == nil || d.registry == nil || peerID == "" {
		return
	}
	d.unreachableMu.Lock()
	defer d.unreachableMu.Unlock()
	if d.unreachableSent == nil {
		d.unreachableSent = make(map[string]bool)
	}
	prev, known := d.unreachableSent[peerID]
	if clearOnly && known && !prev {
		return
	}
	p, err := d.registry.Get(peerID)
	if err != nil {
		delete(d.unreachableSent, peerID)
		return
	}
	now := p.Unreachable()
	if known && now == prev {
		return
	}
	if !known && !now && p.UndeliveredQueuedAt == 0 {
		d.unreachableSent[peerID] = false
		slog.Debug("unreachable mark: a peer never marked is recorded, not published",
			slog.String("peer_id", peerID),
		)
		return
	}
	delivered := 0
	if d.bus != nil {
		delivered, _ = d.bus.Publish(eventbus.TopicPeerUnreachableChanged, peerUnreachableObservation{
			PeerID:      peerID,
			Unreachable: now,
		})
	}
	slog.Info("unreachable mark published",
		slog.String("peer_id", peerID),
		slog.Bool("unreachable", now),
		slog.Bool("was_known", known),
		slog.Int("delivered", delivered),
	)
	if delivered > 0 {
		d.unreachableSent[peerID] = now
	} else {
		delete(d.unreachableSent, peerID)
	}
}

func seedUnreachableSent(d *daemon) {
	if d == nil || d.registry == nil {
		return
	}
	list, err := d.registry.List()
	if err != nil {
		slog.Warn("unreachable mark: peer list not read for the seed", slog.Any("err", err))
		return
	}
	marked := 0
	d.unreachableMu.Lock()
	defer d.unreachableMu.Unlock()
	d.unreachableSent = make(map[string]bool, len(list))
	for i := range list {
		u := list[i].Unreachable()
		d.unreachableSent[list[i].ID] = u
		if u {
			marked++
		}
	}
	slog.Info("unreachable mark: seeded",
		slog.Int("peers", len(list)),
		slog.Int("marked", marked),
	)
}

func wireUnreachableMarks(d *daemon) {
	if d == nil || d.worker == nil {
		return
	}
	d.worker.OnUndelivered = d.noteUndeliveredDest
}

func rowMarks(row *outbox.OutboxRow, now time.Time) bool {
	return row.GaveUp &&
		row.CountedAttempts() >= markMinAttempts &&
		now.UnixNano()-row.FirstAt < int64(outbox.SentRetention)
}

type destPeers struct {
	d    *daemon
	seen map[string]string
}

func (c *destPeers) id(dest string) string {
	if id, ok := c.seen[dest]; ok {
		return id
	}
	id := ""
	if p := c.d.peerForDest(dest); p != nil {
		id = p.ID
	}
	c.seen[dest] = id
	return id
}

func foldOutboxIntoMarks(d *daemon, st *outbox.Store) {
	if d == nil || d.registry == nil || st == nil {
		return
	}
	failed, err := st.ListByState(outbox.StateFailed, 0, 0)
	if err != nil {
		slog.Warn("unreachable mark: failed rows not read", slog.Any("err", err))
		return
	}
	now := time.Now()
	peersOf := &destPeers{d: d, seen: map[string]string{}}
	queued := map[string]int64{}
	noPeer, notCounted := 0, 0
	for _, row := range failed {
		if !rowMarks(row, now) {
			notCounted++
			continue
		}
		id := peersOf.id(row.Dest)
		if id == "" {
			noPeer++
			continue
		}
		if ts := time.Unix(0, row.FirstAt).Unix(); ts > queued[id] {
			queued[id] = ts
		}
	}

	var fresh []string
	for id, ts := range queued {
		p, err := d.registry.Get(id)
		if err != nil || ts <= p.UndeliveredQueuedAt {
			continue
		}
		fresh = append(fresh, id)
	}

	seeded := 0
	if len(fresh) > 0 {
		seeded = seedDeliveredFromOutbox(st, peersOf, fresh)
	}
	for _, id := range fresh {
		if err := d.registry.NoteUndelivered(id, time.Unix(queued[id], 0)); err != nil {
			slog.Warn("unreachable mark: undelivered envelope not recorded",
				slog.String("peer_id", id),
				slog.Any("err", err),
			)
		}
	}

	slog.Info("unreachable mark: outbox folded in",
		slog.Int("failed_rows", len(failed)),
		slog.Int("failed_rows_not_counted", notCounted),
		slog.Int("failed_rows_without_peer", noPeer),
		slog.Int("peers_with_failed_rows", len(queued)),
		slog.Int("peers_updated", len(fresh)),
		slog.Int("peers_delivered_seeded", seeded),
	)
}

func seedDeliveredFromOutbox(st *outbox.Store, peersOf *destPeers, peerIDs []string) int {
	want := make(map[string]bool, len(peerIDs))
	for _, id := range peerIDs {
		want[id] = true
	}
	sent, err := st.ListByState(outbox.StateSent, 0, 0)
	if err != nil {
		slog.Warn("unreachable mark: sent rows not read", slog.Any("err", err))
		return 0
	}
	newest := map[string]int64{}
	for _, row := range sent {
		id := peersOf.id(row.Dest)
		if id == "" || !want[id] {
			continue
		}
		if row.StateChangedAt > newest[id] {
			newest[id] = row.StateChangedAt
		}
	}
	n := 0
	for id, at := range newest {
		if err := peersOf.d.registry.NoteDelivered(id, time.Unix(0, at)); err != nil {
			slog.Warn("unreachable mark: delivered send not recorded",
				slog.String("peer_id", id),
				slog.Any("err", err),
			)
			continue
		}
		n++
	}
	return n
}
