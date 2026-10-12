package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/logid"
	"haoma-frontend/internal/peerstate"
	"haoma-frontend/internal/syncrail"
)

var errDeleteNamesSibling = errors.New("contact delete: the fingerprint names one of my own devices")

var errRowOfDeletedContact = errors.New("gapfill: row belongs to a contact deleted here")

const (
	personDeletedLine = "contact delete: the person was deleted here"

	contactDeletePushedLine = "contact delete: pushed to a sibling"
)

const deadPeerGrace = time.Hour

type deletedChat struct {
	id     chat.ChatID
	events int
}

type personDeleteResult struct {
	pairings []backendapi.Peer

	chats []deletedChat

	keptChats []*chat.DirectChat

	clearedChats []deletedChat

	kept []string

	events int
}

func (r personDeleteResult) peerFor(peerID string) (backendapi.Peer, bool) {
	for _, p := range r.pairings {
		if p.ID == peerID {
			return p, true
		}
	}
	return backendapi.Peer{}, false
}

func backendNotFound(err error) bool {
	var se *backendapi.StatusError
	return errors.As(err, &se) && se.StatusCode == http.StatusNotFound
}

func deletePairingInBackend(ctx context.Context, d *daemon, peerID string) (backendapi.Peer, error) {
	p, err := d.backendClient.PeerAction(ctx, peerID, string(ipc.PeerActionDelete))
	if err == nil {
		return p, nil
	}
	if backendNotFound(err) {
		slog.Debug("contact delete: haomad had no such pairing; taken as deleted",
			slog.String("peer_id", peerID),
		)
		return backendapi.Peer{ID: peerID}, nil
	}
	return backendapi.Peer{}, err
}

func deletePerson(ctx context.Context, d *daemon, fingerprint string, tomb peerstate.Tombstone, origin string) (personDeleteResult, error) {
	var res personDeleteResult
	if fingerprint == "" {
		return res, errors.New("contact delete: empty fingerprint")
	}
	if d == nil || d.peerMeta == nil || d.chats == nil || d.events == nil || d.backendClient == nil {
		return res, errors.New("contact delete: stores or backend client not wired")
	}
	logger := slog.With(
		slog.String("fingerprint", fingerprint),
		slog.String("origin", origin),
		slog.Int64("deleted_at", tomb.DeletedAt),
	)
	peerIDs, err := d.peerMeta.PeerIDsByFingerprint(fingerprint)
	if err != nil {
		return res, fmt.Errorf("contact delete: read the pairings: %w", err)
	}
	var doomed []string
	for _, pid := range peerIDs {
		mine, sErr := d.readIsSibling(pid)
		if sErr != nil {
			return res, fmt.Errorf("contact delete: read the linked-device set: %w", sErr)
		}
		if mine {
			return res, errDeleteNamesSibling
		}
		pairedAt, pErr := d.peerMeta.PairedAt(pid)
		if pErr != nil {
			return res, fmt.Errorf("contact delete: read when %s was paired: %w", logid.Short(pid), pErr)
		}
		if tomb.Covers(pairedAt) {
			doomed = append(doomed, pid)
		} else {
			res.kept = append(res.kept, pid)
		}
	}

	var firstErr error
	note := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	bound := tomb.Bound()
	for _, dc := range personChats(d, fingerprint, peerIDs) {
		routes := dc.FanoutPeerIDs()
		var keptRoutes, coveredRoutes []string
		for _, pid := range routes {
			switch {
			case slices.Contains(res.kept, pid):
				keptRoutes = append(keptRoutes, pid)
			case slices.Contains(doomed, pid):
				coveredRoutes = append(coveredRoutes, pid)
			}
		}
		adoptedAfter := tomb.AppliedAt > 0 && dc.CreatedAt > tomb.AppliedAt && len(coveredRoutes) == 0
		if len(keptRoutes) > 0 || adoptedAfter {
			after, kErr := keepChatAfterDelete(d, logger, dc, keptRoutes, coveredRoutes)
			if kErr != nil {
				note(kErr)

				doomed = slices.DeleteFunc(doomed, func(id string) bool { return slices.Contains(coveredRoutes, id) })
				continue
			}
			n, cascade, dErr := d.events.DeleteByChatThrough(dc.ID, bound)
			if dErr != nil {
				logger.Warn("contact delete: the chat's rows from before the delete could not be erased",
					slog.String("chat_id", string(dc.ID)),
					slog.Any("err", dErr),
				)
				note(dErr)
			}
			if cascade > 0 {
				logger.Info("peer-delete cascade", slog.String("chat_id", string(dc.ID)), slog.Int("cascade_keys", cascade))
			}
			if n > 0 {
				res.clearedChats = append(res.clearedChats, deletedChat{id: dc.ID, events: n})
				res.events += n
			}
			logger.Info("contact delete: a chat goes on after the delete",
				slog.String("chat_id", string(dc.ID)),
				slog.Int("kept_routes", len(keptRoutes)),
				slog.Bool("adopted_after", adoptedAfter),
				slog.Int("events_erased", n),
			)
			res.keptChats = append(res.keptChats, after)
			continue
		}
		n, cascade, dErr := d.events.DeleteByChat(dc.ID)
		if dErr != nil {
			logger.Warn("contact delete: the chat's history could not be erased",
				slog.String("chat_id", string(dc.ID)),
				slog.Any("err", dErr),
			)
			note(dErr)
			continue
		}
		if cascade > 0 {
			logger.Info("peer-delete cascade", slog.String("chat_id", string(dc.ID)), slog.Int("cascade_keys", cascade))
		}
		if dErr := d.chats.Delete(dc.ID); dErr != nil {
			logger.Warn("contact delete: the chat could not be deleted",
				slog.String("chat_id", string(dc.ID)),
				slog.Any("err", dErr),
			)
			note(dErr)
			continue
		}
		res.chats = append(res.chats, deletedChat{id: dc.ID, events: n})
		res.events += n
	}

	for _, pid := range doomed {
		p, bErr := deletePairingInBackend(ctx, d, pid)
		if bErr != nil {

			logger.Warn("contact delete: haomad did not delete a pairing",
				slog.String("peer_id", pid),
				slog.Any("err", bErr),
			)
			note(bErr)
			continue
		}
		if fErr := d.peerMeta.ForgetPeer(pid); fErr != nil {
			logger.Warn("contact delete: the pairing's record could not be removed", slog.String("peer_id", pid), slog.Any("err", fErr))
			note(fErr)
		}
		res.pairings = append(res.pairings, p)
	}

	if len(res.kept) == 0 && len(res.keptChats) == 0 {
		if fErr := d.peerMeta.ForgetPerson(fingerprint); fErr != nil {
			logger.Warn("contact delete: the person record could not be removed", slog.Any("err", fErr))
			note(fErr)
		}
	}
	logger.Info(personDeletedLine,
		slog.Int("pairings", len(res.pairings)),
		slog.Int("chats", len(res.chats)),
		slog.Int("events", res.events),
		slog.Int("kept_pairings", len(res.kept)),
		slog.Int("kept_chats", len(res.keptChats)),
		slog.Bool("complete", firstErr == nil),
	)
	return res, firstErr
}

func keepChatAfterDelete(d *daemon, logger *slog.Logger, dc *chat.DirectChat, keptRoutes, coveredRoutes []string) (*chat.DirectChat, error) {
	after := dc
	if len(coveredRoutes) == 0 {
		return after, nil
	}
	primary := dc.PeerID
	if slices.Contains(coveredRoutes, primary) {
		if len(keptRoutes) == 0 {
			return dc, fmt.Errorf("contact delete: chat %s has no pairing to move its primary to", dc.ID)
		}
		primary = keptRoutes[0]
	}
	moved, _, err := d.dropRoutes(dc.ID, primary, coveredRoutes)
	if err != nil {
		logger.Warn("contact delete: the covered pairings could not be taken out of a chat that goes on",
			slog.String("chat_id", string(dc.ID)),
			slog.Any("err", err),
		)
		return dc, err
	}
	after = moved
	for _, pid := range coveredRoutes {
		out, err := d.dropPairing(dc.ID, pid)
		if err != nil {
			logger.Warn("contact delete: a covered pairing stayed in the by-peer index",
				slog.String("peer_id", pid),
				slog.String("chat_id", string(dc.ID)),
				slog.Any("err", err),
			)
			return after, err
		}
		after = out
	}
	return after, nil
}

type pairingDeleteKind int

const (
	deleteLocal pairingDeleteKind = iota

	deleteThePairing

	deleteThePerson
)

type pairingDeletePlan struct {
	kind        pairingDeleteKind
	fingerprint string

	otherLive []string
}

func planPairingDelete(ctx context.Context, d *daemon, peerID string, dc *chat.DirectChat) (pairingDeletePlan, error) {
	if d == nil || d.peerMeta == nil || d.backendClient == nil {
		return pairingDeletePlan{kind: deleteLocal}, nil
	}
	mine, err := d.readIsSibling(peerID)
	if err != nil {
		return pairingDeletePlan{}, fmt.Errorf("read the linked-device set: %w", err)
	}
	if mine {
		return pairingDeletePlan{kind: deleteLocal}, nil
	}
	registry, err := peerRegistry(ctx, d)
	if err != nil {
		return pairingDeletePlan{}, fmt.Errorf("read the peer list: %w", err)
	}
	isLive := func(id string) bool {
		p, ok := registry[id]
		return ok && p.RetiredAt == 0
	}
	if !isLive(peerID) {
		return pairingDeletePlan{kind: deleteLocal}, nil
	}
	rec, err := d.peerMeta.Get(peerID)
	if err != nil {
		return pairingDeletePlan{}, fmt.Errorf("read the contact: %w", err)
	}
	fp := rec.Fingerprint
	if fp == "" && dc != nil {
		fp = dc.Fingerprint
	}
	if fp == "" {

		return pairingDeletePlan{kind: deleteLocal}, nil
	}
	others, err := d.peerMeta.PeerIDsByFingerprint(fp)
	if err != nil {
		return pairingDeletePlan{}, fmt.Errorf("read the pairings: %w", err)
	}
	var otherLive []string
	for _, id := range others {
		if id == peerID || !isLive(id) {
			continue
		}
		m, sErr := d.readIsSibling(id)
		if sErr != nil {

			return pairingDeletePlan{}, fmt.Errorf("read the linked-device set: %w", sErr)
		}
		if m {
			continue
		}
		otherLive = append(otherLive, id)
	}
	if len(otherLive) > 0 {
		return pairingDeletePlan{kind: deleteThePairing, fingerprint: fp, otherLive: otherLive}, nil
	}
	return pairingDeletePlan{kind: deleteThePerson, fingerprint: fp}, nil
}

func personChats(d *daemon, fingerprint string, peerIDs []string) []*chat.DirectChat {
	var out []*chat.DirectChat
	add := func(dc *chat.DirectChat) {
		for _, have := range out {
			if have.ID == dc.ID {
				return
			}
		}
		out = append(out, dc)
	}
	if dc, err := d.chats.GetByFingerprint(fingerprint); err == nil {
		add(dc)
	}
	for _, pid := range peerIDs {
		if dc, err := d.chats.GetByDirectPeer(pid); err == nil {
			add(dc)
		}
	}
	return out
}

func pushPersonDeleted(ctx context.Context, d *daemon, res personDeleteResult) {
	if d == nil {
		return
	}

	for _, c := range res.clearedChats {
		recomputeChatUnread(ctx, d, c.id)
	}
	if d.ipcSrv == nil {
		return
	}
	for _, p := range res.pairings {
		push(d.ipcSrv, ipc.FramePeerDeleted, "", ipc.PeerDeletedPayload{PeerID: p.ID})
		slog.Debug("peer.deleted broadcast", slog.String("peer_id", p.ID))
	}

	for _, c := range res.clearedChats {
		push(d.ipcSrv, ipc.FrameChatCleared, "", ipc.ChatClearedPayload{
			ChatID:       string(c.id),
			DeletedCount: c.events,
		})
	}

	for _, dc := range res.keptChats {
		if c, err := d.chats.Get(dc.ID); err == nil {
			if fresh, ok := c.(*chat.DirectChat); ok {
				dc = fresh
			}
		}
		pushChatUpdated(d, dc)
	}
	for _, c := range res.chats {
		push(d.ipcSrv, ipc.FrameChatDeleted, "", ipc.ChatDeletedPayload{
			ChatID:       string(c.id),
			DeletedCount: c.events,
		})
		slog.Debug("chat.deleted broadcast (peer-cascade)",
			slog.String("chat_id", string(c.id)),
			slog.Int("deleted_count", c.events),
		)
	}
	pushInheritedContactsChanged(d, nil, "contact_delete")
}

func sendContactDelete(ctx context.Context, d *daemon, deleted syncrail.DeletedContact) {
	sibs := d.siblings()
	if len(sibs) == 0 {
		return
	}
	if contactDeletePushDropped(d, deleted) {
		return
	}
	body := syncrail.ContactDeleteBody{Deleted: []syncrail.DeletedContact{deleted}}
	for _, sib := range sibs {
		if ctx.Err() != nil {
			return
		}
		if err := sendGapEnvelope(ctx, d, sib, syncrail.KindContactDelete, body); err != nil {
			slog.Warn("contact delete: push to a sibling failed; the sweep will carry it",
				slog.String("sibling_peer_id", sib),
				slog.String("fingerprint", deleted.Fingerprint),
				slog.Any("err", err),
			)
			continue
		}
		slog.Info(contactDeletePushedLine,
			slog.String("sibling_peer_id", sib),
			slog.String("fingerprint", deleted.Fingerprint),
			slog.Int64("deleted_at", deleted.DeletedAt),
		)
	}
}

func handleContactDeletePush(ctx context.Context, d *daemon, siblingPeerID string, body syncrail.ContactDeleteBody) {
	logger := slog.With(slog.String("sibling_peer_id", siblingPeerID))
	applyDeletedContacts(ctx, d, logger, body.Deleted, "sibling_push")
}

func applyDeletedContacts(ctx context.Context, d *daemon, logger *slog.Logger, deleted []syncrail.DeletedContact, origin string) int {
	if d == nil || d.peerMeta == nil || len(deleted) == 0 {
		return 0
	}
	changed := 0
	for _, c := range syncrail.UsableDeletedContacts(deleted) {

		now := time.Now().Unix()
		at := c.DeletedAt

		if mine, sErr := fingerprintNamesSibling(d, c.Fingerprint); sErr != nil || mine {
			logger.Warn("contact delete: refused — the fingerprint names one of my own devices, or the linked-device set could not be read",
				slog.String("fingerprint", c.Fingerprint),
				slog.Any("err", sErr),
			)
			continue
		}
		held, moved, err := d.peerMeta.MarkDeleted(c.Fingerprint, at, now)
		if err != nil {
			logger.Warn("contact delete: the tombstone could not be written; the sweep will carry it again",
				slog.String("fingerprint", c.Fingerprint),
				slog.Any("err", err),
			)
			continue
		}
		if !moved && held.Done {
			logger.Debug("contact delete: already applied here — no state change",
				slog.String("fingerprint", c.Fingerprint),
				slog.String("origin", origin),
			)
			continue
		}
		res, dErr := deletePerson(ctx, d, c.Fingerprint, held, origin)
		if errors.Is(dErr, errDeleteNamesSibling) {
			logger.Warn("contact delete: refused — the fingerprint names one of my own devices",
				slog.String("fingerprint", c.Fingerprint),
			)
			continue
		}
		if dErr == nil {
			if mErr := d.peerMeta.MarkDeleteDone(c.Fingerprint, held.DeletedAt); mErr != nil {
				logger.Warn("contact delete: could not record the delete as done", slog.Any("err", mErr))
			}
		}
		pushPersonDeleted(ctx, d, res)
		changed++
	}
	return changed
}

func fingerprintNamesSibling(d *daemon, fingerprint string) (bool, error) {

	if d.stores != nil {
		if kp := d.stores.GetIdentityKeyPair(); kp != nil && kp.PublicKey().Fingerprint() == fingerprint {
			return true, nil
		}
	}
	peerIDs, err := d.peerMeta.PeerIDsByFingerprint(fingerprint)
	if err != nil {
		return false, err
	}
	for _, pid := range peerIDs {
		mine, err := d.readIsSibling(pid)
		if err != nil {
			return false, err
		}
		if mine {
			return true, nil
		}
	}
	return false, nil
}

func personDeletedHere(d *daemon, fingerprint string) (bool, peerstate.Tombstone, error) {
	if d == nil || d.peerMeta == nil || fingerprint == "" {
		return false, peerstate.Tombstone{}, nil
	}
	tomb, found, err := d.peerMeta.Tombstone(fingerprint)
	if err != nil || !found {
		return false, tomb, err
	}
	has, err := d.peerMeta.HasPerson(fingerprint)
	if err != nil {
		return false, tomb, err
	}
	if has {
		return false, tomb, nil
	}
	peerIDs, err := d.peerMeta.PeerIDsByFingerprint(fingerprint)
	if err != nil {
		return false, tomb, err
	}
	for _, pid := range peerIDs {
		pairedAt, pErr := d.peerMeta.PairedAt(pid)
		if pErr != nil {
			return false, tomb, pErr
		}
		if !tomb.Covers(pairedAt) {
			return false, tomb, nil
		}
	}
	return true, tomb, nil
}

func gapRowOfDeletedContact(d *daemon, row syncrail.RowSnapshot) (bool, error) {
	if d == nil || d.peerMeta == nil || row.Fingerprint == "" {
		return false, nil
	}

	if row.Swept {
		return false, nil
	}
	tomb, found, err := d.peerMeta.Tombstone(row.Fingerprint)
	if err != nil || !found {
		return false, err
	}
	return row.DisplayTs <= tomb.Bound(), nil
}

func leaveOutDeleted(d *daemon, logger *slog.Logger, fingerprint string) bool {
	gone, _, err := personDeletedHere(d, fingerprint)
	if err != nil {
		logger.Warn("contact delete: could not read whether a person was deleted here; left out",
			slog.String("fingerprint", fingerprint),
			slog.Any("err", err),
		)
		return true
	}
	if gone {
		logger.Debug("contact delete: a person deleted here is left out",
			slog.String("fingerprint", fingerprint),
		)
	}
	return gone
}

func newestPairingAt(d *daemon, peerIDs []string) int64 {
	var newest int64
	for _, pid := range peerIDs {
		at, err := d.peerMeta.PairedAt(pid)
		if err != nil {
			continue
		}
		if at > newest {
			newest = at
		}
	}
	return newest
}

func tombstonesForSibling(d *daemon, logger *slog.Logger) []syncrail.DeletedContact {
	if d == nil || d.peerMeta == nil {
		return nil
	}
	all, err := d.peerMeta.Tombstones()
	if err != nil {
		logger.Warn("contact delete: the tombstones could not be read; this answer carries none", slog.Any("err", err))
		return nil
	}
	out := make([]syncrail.DeletedContact, 0, len(all))
	for _, t := range all {
		out = append(out, syncrail.DeletedContact{Fingerprint: t.Fingerprint, DeletedAt: t.DeletedAt})
	}
	return out
}

func sweepDeadPeerRows(ctx context.Context, d *daemon) {
	if d == nil || d.peerMeta == nil || d.chats == nil || d.backendClient == nil {
		return
	}
	records, err := d.peerMeta.PeerRecords()
	if err != nil {
		slog.Warn("dead pairing records: the records could not be read", slog.Any("err", err))
		return
	}
	if len(records) == 0 {
		return
	}
	registry, err := peerRegistry(ctx, d)
	if err != nil {
		slog.Warn("dead pairing records: haomad's peer list could not be read; nothing removed", slog.Any("err", err))
		return
	}
	if len(registry) == 0 {

		slog.Warn("dead pairing records: haomad's peer list is empty while records exist; nothing removed",
			slog.Int("records", len(records)),
		)
		return
	}
	cutoff := time.Now().Add(-deadPeerGrace).Unix()
	var dead []string
	for pid, rec := range records {
		if _, live := registry[pid]; live {
			continue
		}
		mine, sErr := d.readIsSibling(pid)
		if sErr != nil || mine {
			continue
		}
		if rec.PairedAt > cutoff {
			continue
		}
		if _, cErr := d.chats.GetByDirectPeer(pid); !errors.Is(cErr, chat.ErrNotFound) {

			continue
		}
		dead = append(dead, pid)
	}
	if len(dead) > 1 && 2*len(dead) > len(records) {

		slog.Warn("dead pairing records: most records would go; nothing removed",
			slog.Int("records", len(records)),
			slog.Int("would_remove", len(dead)),
		)
		return
	}
	var removed []string
	for _, pid := range dead {
		if fErr := d.peerMeta.ForgetPeer(pid); fErr != nil {
			slog.Warn("dead pairing records: a record could not be removed", slog.String("peer_id", pid), slog.Any("err", fErr))
			continue
		}
		removed = append(removed, pid)
		if d.ipcSrv != nil {
			push(d.ipcSrv, ipc.FramePeerDeleted, "", ipc.PeerDeletedPayload{PeerID: pid})
		}
	}
	if len(removed) == 0 {
		return
	}
	slices.Sort(removed)

	short := make([]string, len(removed))
	for i, pid := range removed {
		short[i] = logid.Short(pid)
	}
	slog.Info("dead pairing records: removed the records of pairings haomad no longer has",
		slog.Int("removed", len(removed)),
		slog.Any("peers", short),
	)
}
