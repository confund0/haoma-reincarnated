package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"haoma/internal/store"
)

const unlinkReceiptWindow = 12 * time.Hour

const unlinkSweepInterval = 5 * time.Minute

func (d *daemon) beginUnlink(peerID string) (store.UnlinkPending, error) {
	if peerID == "" {
		return store.UnlinkPending{}, errors.New("unlink: no peer named")
	}
	if existing, err := d.store.GetUnlinkPending(); err == nil &&
		existing.InFlight() && existing.PeerID == peerID {
		return existing, nil
	}
	now := time.Now()
	rec := store.UnlinkPending{
		PeerID:    peerID,
		Deadline:  now.Add(unlinkReceiptWindow).Unix(),
		StartedAt: now.Unix(),
	}
	if err := d.store.PutUnlinkPending(rec); err != nil {
		return store.UnlinkPending{}, err
	}
	slog.Info("unlink: rail narrowed, only a destroy receipt is accepted from that device",
		slog.String("peer_id", peerID),
		slog.Int64("deadline", rec.Deadline),
	)
	return rec, nil
}

func (d *daemon) armSelfErase(peerID, proof string) {
	if proof == "" {
		return
	}
	if err := d.store.ArmUnlinkSelfErase(peerID, proof); err != nil {
		slog.Warn("unlink: could not arm the answer; that device will not learn it is gone",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return
	}
	slog.Info("unlink: the rail address now answers that device with a destroy proof",
		slog.String("peer_id", peerID),
	)
}

func (d *daemon) unlinkAcceptsKind(peerID, kind string) bool {
	rec, err := d.store.GetUnlinkPending()
	if err != nil {

		slog.Warn("unlink: could not read the narrowing; refusing traffic from the removed device",
			slog.Any("err", err),
		)
		return false
	}
	if !rec.InFlight() || rec.PeerID != peerID {
		return true
	}
	return kind == "destroy_receipt"
}

func (d *daemon) finishUnlink(reason string) {
	rec, err := d.store.GetUnlinkPending()
	if err != nil {
		slog.Warn("unlink: could not read the narrowing", slog.Any("err", err))
		return
	}
	if !rec.InFlight() {
		return
	}

	if err := d.registry.Retire(rec.PeerID); err != nil {
		slog.Warn("unlink: could not retire the removed device; the rail stays narrowed",
			slog.String("peer_id", rec.PeerID),
			slog.Any("err", err),
		)
		return
	}
	d.routes.forget(rec.PeerID)
	if err := d.store.ClearUnlinkPending(); err != nil {
		slog.Warn("unlink: could not clear the narrowing", slog.Any("err", err))
		return
	}
	slog.Info("unlink: finished, the rail address is gone",
		slog.String("peer_id", rec.PeerID),
		slog.String("reason", reason),

		slog.Bool("was_armed", rec.Armed()),
	)
}

func (d *daemon) sweepUnlinkDeadline(ctx context.Context) {
	check := func() {
		rec, err := d.store.GetUnlinkPending()
		if err != nil || !rec.InFlight() {
			return
		}
		if time.Now().Unix() < rec.Deadline {
			return
		}
		d.finishUnlink("no receipt arrived before the deadline")
	}

	check()
	t := time.NewTicker(unlinkSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			check()
		}
	}
}

func (d *daemon) unlinkGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec, refused := d.unlinkRefusesRequest(r)
		if !refused {
			next.ServeHTTP(w, r)
			return
		}
		if rec.Armed() {

			w.Header().Set(destroyProofHeader, rec.Proof)
			w.WriteHeader(http.StatusTeapot)
			slog.Info("unlink: told that device it is gone",
				slog.String("peer_id", rec.PeerID),
				slog.String("path", logSafePath(r.URL.Path)),
			)
			return
		}

		http.NotFound(w, r)
	})
}

const destroyProofHeader = "X-Haoma-Destroy-Proof"

func (d *daemon) unlinkRefusesRequest(r *http.Request) (store.UnlinkPending, bool) {
	rec, err := d.store.GetUnlinkPending()
	if err != nil {

		slog.Warn("unlink: could not read the narrowing; refusing this request",
			slog.Any("err", err),
		)
		return store.UnlinkPending{}, true
	}
	if !rec.InFlight() {
		return rec, false
	}
	if d.unlinkOwnsInboundHost(rec.PeerID, r.Host) {
		slog.Info("unlink: dropped a request from the device being removed",
			slog.String("peer_id", rec.PeerID),
			slog.String("path", logSafePath(r.URL.Path)),
		)
		return rec, true
	}
	if d.peerIDForFilePath(r.URL.Path) == rec.PeerID {
		slog.Info("unlink: dropped a file fetch minted for the device being removed",
			slog.String("peer_id", rec.PeerID),
		)
		return rec, true
	}
	return rec, false
}

func (d *daemon) unlinkOwnsInboundHost(peerID, host string) bool {
	host = onionServiceID(host)
	if host == "" || peerID == "" || d.registry == nil {
		return false
	}
	peer, err := d.registry.Get(peerID)
	if err != nil || peer == nil {

		slog.Warn("unlink: could not read the device being removed",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return false
	}
	if peer.MyOnionAddr == host {
		return true
	}
	if prev := peer.PrevMyOnion; prev != nil && prev.Address == host {
		return prev.ExpiresAt > time.Now().Unix()
	}
	return false
}

func onionServiceID(host string) string {
	if host == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimSuffix(strings.ToLower(host), ".onion")
}

func (d *daemon) peerIDForOnionHost(host string) string {
	host = onionServiceID(host)
	if host == "" || d.registry == nil {
		return ""
	}
	peer, err := d.registry.ByAddress(host)
	if err != nil || peer == nil {
		return ""
	}
	return peer.ID
}

func logSafePath(path string) string {
	for _, prefix := range []string{"/files/", "/audio/", "/video/", "/screen/"} {
		if strings.HasPrefix(path, prefix) {
			return prefix + "<token>"
		}
	}
	return path
}

func (d *daemon) peerIDForFilePath(path string) string {
	if d.files == nil {
		return ""
	}
	token, ok := strings.CutPrefix(path, "/files/")
	if !ok || token == "" || strings.Contains(token, "/") {
		return ""
	}
	row, err := d.files.LookupToken(token)
	if err != nil {
		return ""
	}
	return row.RecipientPeerID
}

func (d *daemon) unlinkRemovedPeerID() (peerID string, readable bool) {
	rec, err := d.store.GetUnlinkPending()
	if err != nil {
		slog.Warn("unlink: could not read the narrowing; refusing to reach any peer",
			slog.Any("err", err),
		)
		return "", false
	}
	return rec.PeerID, true
}

func (d *daemon) unlinkRefusesPeer(peerID string) bool {
	removed, readable := d.unlinkRemovedPeerID()
	if !readable {
		return true
	}
	return removed != "" && removed == peerID
}

func (d *daemon) unlinkRefusesDest(dest string) bool {
	removed, readable := d.unlinkRemovedPeerID()
	if !readable {
		return true
	}
	if removed == "" {
		return false
	}
	onion := onionFromDest(dest)
	if onion == "" {
		return false
	}
	return d.peerIDForOnionHost(onion) == removed
}

var errUnlinkPeerRemoved = errors.New("that device is being removed")

var errUnlinkNarrowed = errors.New("envelope refused")
