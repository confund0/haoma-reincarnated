package main

import (
	"context"
	"log/slog"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/ipc"
)

const unlinkNoticeTimeout = 2 * time.Minute

func (d *daemon) deviceCount() int {
	if d == nil || d.linkedDevices == nil {
		return 0
	}
	members, err := d.linkedDevices.Members()
	if err != nil {
		slog.Warn("device count: the linked-device set could not be read; saying nothing",
			slog.Any("err", err),
		)
		return 0
	}
	return len(members) + 1
}

func pushUnlinkNotice(ctx context.Context, d *daemon, removedPeerID string, unlinkedAt int64) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), unlinkNoticeTimeout)
	defer cancel()
	if d.backendClient == nil {
		slog.Debug("unlink notice: no backend client; the contacts are not told")
		return
	}
	if unlinkedAt <= 0 {
		slog.Warn("unlink notice: no unlink time; the contacts are not told",
			slog.String("removed_peer_id", removedPeerID),
		)
		return
	}
	count := d.deviceCount()
	if count != 1 {
		slog.Warn("unlink notice: the device count after the unlink is not 1, so the unlink did not take; the contacts are not told",
			slog.String("removed_peer_id", removedPeerID),
			slog.Int("device_count", count),
		)
		return
	}
	resp, err := d.backendClient.Peers(ctx)
	if err != nil {
		slog.Warn("unlink notice: the peer list could not be read; the contacts are not told",
			slog.String("removed_peer_id", removedPeerID),
			slog.Any("err", err),
		)
		return
	}
	targets := contactsToTell(resp.Peers, d.siblings(), removedPeerID)
	state := d.effectivePresenceState()
	var enqueued int
	for _, peerID := range targets {

		if sErr := d.shipUnlinkNotice(ctx, peerID, state, count, unlinkedAt); sErr != nil {
			slog.Warn("unlink notice: one contact device was not told",
				slog.String("peer_id", peerID),
				slog.Any("err", sErr),
			)
			continue
		}
		enqueued++
	}

	attrs := []any{
		slog.Int64("unlinked_at", unlinkedAt),
		slog.Int("device_count", count),
		slog.String("removed_peer_id", removedPeerID),
		slog.Int("devices_attempted", len(targets)),
		slog.Int("devices_enqueued", enqueued),
		slog.Int("devices_skipped", len(resp.Peers)-len(targets)),
	}
	switch {
	case len(targets) == 0:
		slog.Info("unlink notice: there is no contact device to tell", attrs...)
	case enqueued == 0:
		slog.Warn("unlink notice: no contact device was told", attrs...)
	case enqueued < len(targets):
		slog.Warn("unlink notice: handed to the outbox for some contact devices; the others are not told", attrs...)
	default:
		slog.Info("unlink notice: handed to the outbox for every contact device", attrs...)
	}
}

func contactsToTell(peers []backendapi.Peer, siblings []string, removedPeerID string) []string {
	mine := make(map[string]struct{}, len(siblings))
	for _, id := range siblings {
		mine[id] = struct{}{}
	}
	out := make([]string, 0, len(peers))
	for _, p := range peers {
		if p.RetiredAt != 0 || p.ID == removedPeerID {
			continue
		}
		if _, isMine := mine[p.ID]; isMine {
			continue
		}
		out = append(out, p.ID)
	}
	return out
}

func applyUnlinkFields(ctx context.Context, d *daemon, peerID, kind string, marker bool, count int, unlinkedAt, sealedAt int64) {
	if !marker && count <= 0 && unlinkedAt <= 0 {
		return
	}
	if d.peerMeta == nil || d.backendClient == nil {
		if count > 0 || unlinkedAt > 0 {
			slog.Warn("unlink notice: frontend wiring incomplete; nothing is retired",
				slog.String("peer_id", peerID),
			)
		}
		return
	}
	sendsNotice := marker
	if marker {
		recordUnlinkNoticeMarker(d, peerID, kind)
	}
	if unlinkedAt > 0 {
		applyUnlinkNotice(ctx, d, peerID, unlinkedAt, count)
		return
	}
	if count <= 0 {
		return
	}
	if !sendsNotice {
		held, err := d.peerMeta.SendsUnlinkNotice(peerID)
		if err != nil {
			slog.Warn("device count: whether this pairing sends the unlink notice could not be read; nothing is retired",
				slog.String("peer_id", peerID),
				slog.Any("err", err),
			)
			return
		}
		sendsNotice = held
	}
	if sendsNotice {
		slog.Debug("device count: read and dropped; from this pairing only an unlink notice retires",
			slog.String("peer_id", peerID),
			slog.Int("device_count", count),
			slog.String("kind", kind),
			slog.String("rule", ruleUnlinkNotice.name),
			slog.String("outcome", outcomeCountOnly),
		)
		return
	}
	applyCountFromOlderSender(ctx, d, peerID, count, sealedAt, kind)
}

func recordUnlinkNoticeMarker(d *daemon, peerID, kind string) {
	held, err := d.peerMeta.SendsUnlinkNotice(peerID)
	if err == nil && held {
		return
	}
	changed, err := d.peerMeta.MarkSendsUnlinkNotice(peerID, time.Now().Unix())
	if err != nil {
		slog.Warn("unlink notice: the marker on this pairing could not be recorded",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return
	}
	if changed {
		slog.Info("unlink notice: this pairing's build sends the unlink notice; a count alone no longer retires on it",
			slog.String("peer_id", peerID),
			slog.String("kind", kind),
		)
	}
}

func applyUnlinkNotice(ctx context.Context, d *daemon, peerID string, unlinkedAt int64, count int) {
	r := ruleUnlinkNotice
	slog.Debug("unlink notice: arrived",
		slog.String("peer_id", peerID),
		slog.Int64("unlinked_at", unlinkedAt),
		slog.Int("device_count", count),
		slog.String("rule", r.name),
	)
	if count != 1 {
		slog.Info("unlink notice: the sender does not say it is the last device; nothing is retired",
			slog.String("peer_id", peerID),
			slog.Int("device_count", count),
			slog.String("rule", r.name),
			slog.String("outcome", outcomeNoticeNotLast),
		)
		return
	}
	fingerprint, ok := senderFingerprint(d, peerID, r)
	if !ok {
		return
	}
	if now := time.Now().Unix(); unlinkedAt > now {
		slog.Debug("unlink notice: the unlink time is ahead of this device's clock; it is read as now",
			slog.String("peer_id", peerID),
			slog.Int64("unlinked_at", unlinkedAt),
			slog.Int64("now", now),
		)
		unlinkedAt = now
	}
	live, ok := livePairings(ctx, d, peerID, r)
	if !ok {
		return
	}
	if _, senderLive := live[peerID]; !senderLive {
		slog.Info("unlink notice: it came on a pairing this device has retired; nothing is retired",
			slog.String("peer_id", peerID),
			slog.String("rule", r.name),
			slog.String("outcome", "noop_sender_retired"),
		)
		return
	}
	fresh, err := d.peerMeta.ObserveUnlinkNotice(fingerprint, unlinkedAt)
	if err != nil {
		slog.Warn("unlink notice: the age of this notice could not be established; nothing is retired",
			slog.String("peer_id", peerID),
			slog.Int64("unlinked_at", unlinkedAt),
			slog.Any("err", err),
		)
		return
	}
	if !fresh {
		slog.Debug("unlink notice: a notice of a later unlink was already read; this one is dropped",
			slog.String("peer_id", peerID),
			slog.Int64("unlinked_at", unlinkedAt),
			slog.String("rule", r.name),
			slog.String("outcome", "noop_unlinked_earlier"),
		)
		return
	}
	ids, ok := personPairings(d, peerID, fingerprint, r)
	if !ok {
		return
	}
	t := retireOtherPairings(ctx, d, peerID, ids, live, unlinkedAt, r)
	slog.Info("unlink notice: that person's other device was unlinked",
		slog.String("peer_id", peerID),
		slog.Int64("unlinked_at", unlinkedAt),
		slog.String("rule", r.name),
		slog.Int("pairings_retired", t.retired),
		slog.Int("pairings_already_retired", t.alreadyGone),
		slog.Int("pairings_kept_newer", t.keptNewer),
		slog.Int("pairings_kept_unreadable", t.keptUnreadable),
	)
}

func applyCountFromOlderSender(ctx context.Context, d *daemon, peerID string, count int, sealedAt int64, kind string) {
	r := ruleCountOlderSender
	slog.Debug("device count: this pairing has never shown the unlink-notice marker; the count rule runs",
		slog.String("peer_id", peerID),
		slog.Int("device_count", count),
		slog.Int64("sealed_at", sealedAt),
		slog.String("kind", kind),
		slog.String("rule", r.name),
	)
	fingerprint, ok := senderFingerprint(d, peerID, r)
	if !ok {
		return
	}
	fresh, err := d.peerMeta.ObserveDeviceCount(fingerprint, sealedAt)
	if err != nil {
		slog.Warn("device count: the age of this count could not be established; nothing is retired",
			slog.String("peer_id", peerID),
			slog.Int64("sealed_at", sealedAt),
			slog.Any("err", err),
		)
		return
	}
	if !fresh {
		slog.Debug("device count: this count was sealed before one already read; it is dropped",
			slog.String("peer_id", peerID),
			slog.Int64("sealed_at", sealedAt),
			slog.String("rule", r.name),
			slog.String("outcome", "noop_sealed_earlier"),
		)
		return
	}
	if count > 1 {
		slog.Debug("device count: that person has more than one device; the count is dropped",
			slog.String("peer_id", peerID),
			slog.Int("device_count", count),
			slog.String("rule", r.name),
			slog.String("outcome", "noop_more_than_one"),
		)
		return
	}

	ids, ok := personPairings(d, peerID, fingerprint, r)
	if !ok {
		return
	}
	live, ok := livePairings(ctx, d, peerID, r)
	if !ok {
		return
	}
	if _, senderLive := live[peerID]; !senderLive {
		slog.Info("device count: it came on a pairing this device has retired; nothing is retired",
			slog.String("peer_id", peerID),
			slog.String("rule", r.name),
			slog.String("outcome", "noop_sender_retired"),
		)
		return
	}
	t := retireOtherPairings(ctx, d, peerID, ids, live, sealedAt, r)
	slog.Info("device count: that person says they have one device left",
		slog.String("peer_id", peerID),
		slog.Int64("sealed_at", sealedAt),
		slog.String("rule", r.name),
		slog.Int("pairings_retired", t.retired),
		slog.Int("pairings_already_retired", t.alreadyGone),
		slog.Int("pairings_kept_newer", t.keptNewer),
		slog.Int("pairings_kept_unreadable", t.keptUnreadable),
	)
}

type retireRule struct {
	name           string
	prefix         string
	refKey         string
	retiredMsg     string
	retiredOutcome string
	keptNewer      string
}

var (
	ruleUnlinkNotice = retireRule{
		name:           "unlink_notice",
		prefix:         "unlink notice",
		refKey:         "unlinked_at",
		retiredMsg:     unlinkNoticeRetiredMsg,
		retiredOutcome: outcomeRetiredOnNotice,
		keptNewer:      outcomeKeptNewer,
	}
	ruleCountOlderSender = retireRule{
		name:           "count_older_sender",
		prefix:         "device count",
		refKey:         "sealed_at",
		retiredMsg:     "device count: a pairing was retired and its address dropped",
		retiredOutcome: "retired_on_count_older_sender",
		keptNewer:      "kept_newer_than_count",
	}
)

func senderFingerprint(d *daemon, peerID string, r retireRule) (string, bool) {
	rec, err := d.peerMeta.Get(peerID)
	if err != nil {
		slog.Warn(r.prefix+": the sender's peer meta could not be read; nothing is retired",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return "", false
	}
	if rec.Fingerprint == "" {
		slog.Debug(r.prefix+": the sender has no identity fingerprint yet; nothing is retired",
			slog.String("peer_id", peerID),
			slog.String("rule", r.name),
			slog.String("outcome", "noop_no_fingerprint"),
		)
		return "", false
	}
	return rec.Fingerprint, true
}

func personPairings(d *daemon, peerID, fingerprint string, r retireRule) ([]string, bool) {
	ids, err := d.peerMeta.PeerIDsByFingerprint(fingerprint)
	if err != nil {
		slog.Warn(r.prefix+": that person's peer ids could not be read; nothing is retired",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return nil, false
	}
	if len(ids) <= 1 {
		slog.Debug(r.prefix+": this device holds one pairing with that person; nothing to retire",
			slog.String("peer_id", peerID),
			slog.Int("peer_ids_held", len(ids)),
			slog.String("rule", r.name),
			slog.String("outcome", "noop_single_pairing"),
		)
		return nil, false
	}
	return ids, true
}

func livePairings(ctx context.Context, d *daemon, peerID string, r retireRule) (map[string]struct{}, bool) {
	resp, err := d.backendClient.Peers(ctx)
	if err != nil {
		slog.Warn(r.prefix+": the peer list could not be read; nothing is retired",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return nil, false
	}
	live := make(map[string]struct{})
	for _, p := range resp.Peers {
		if p.RetiredAt == 0 {
			live[p.ID] = struct{}{}
		}
	}
	return live, true
}

type retireTally struct {
	retired, alreadyGone, keptNewer, keptUnreadable int
}

func retireOtherPairings(ctx context.Context, d *daemon, sender string, ids []string, live map[string]struct{}, ref int64, r retireRule) retireTally {
	var t retireTally
	var retired []string
	for _, id := range ids {
		if id == sender {

			continue
		}
		if _, isLive := live[id]; !isLive {
			t.alreadyGone++
			slog.Debug(r.prefix+": that pairing is already retired",
				slog.String("peer_id", id),
				slog.String("rule", r.name),
				slog.String("outcome", "noop_already_retired"),
			)
			continue
		}
		pairedAt, pErr := d.peerMeta.PairedAt(id)
		switch pairingAgeOutcome(pairedAt, pErr, ref) {
		case outcomeKeptUnreadable:
			t.keptUnreadable++
			slog.Warn(r.prefix+": when that pairing was made could not be read; it is kept",
				slog.String("peer_id", id),
				slog.Any("err", pErr),
				slog.String("rule", r.name),
				slog.String("outcome", outcomeKeptUnreadable),
			)
			continue
		case outcomeKeptNewer:
			t.keptNewer++
			slog.Debug(r.prefix+": that pairing was made at or after the time this message speaks of; it is kept",
				slog.String("peer_id", id),
				slog.String("kept_peer_id", sender),
				slog.Int64("paired_at", pairedAt),
				slog.Int64(r.refKey, ref),
				slog.String("rule", r.name),
				slog.String("outcome", r.keptNewer),
			)
			continue
		}
		if _, aErr := d.backendClient.PeerAction(ctx, id, string(ipc.PeerActionRetire)); aErr != nil {
			slog.Warn(r.prefix+": a pairing could not be retired",
				slog.String("peer_id", id),
				slog.Any("err", aErr),
			)
			continue
		}
		t.retired++
		slog.Info(r.retiredMsg,
			slog.String("peer_id", id),
			slog.String("kept_peer_id", sender),
			slog.Int64("paired_at", pairedAt),
			slog.Int64(r.refKey, ref),
			slog.String("rule", r.name),
			slog.String("outcome", r.retiredOutcome),
		)
		retired = append(retired, id)
	}

	afterPairingsRetired(ctx, d, retired, sender)
	return t
}

const unlinkNoticeRetiredMsg = "unlink notice: a pairing was retired and its address dropped"

const (
	outcomeCountOnly = "noop_count_only"

	outcomeNoticeNotLast = "noop_notice_not_last_device"

	outcomeRetiredOnNotice = "retired_on_unlink_notice"

	outcomeKeptUnreadable = "kept_unreadable"
	outcomeKeptNewer      = "kept_newer_than_notice"
)

func pairingAgeOutcome(pairedAt int64, readErr error, ref int64) string {
	if readErr != nil {
		return outcomeKeptUnreadable
	}
	if pairedAt != 0 && pairedAt >= ref {
		return outcomeKeptNewer
	}
	return ""
}
