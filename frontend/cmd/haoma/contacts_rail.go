package main

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/peerstate"
	"haoma-frontend/internal/syncrail"
)

type contactsPushTiming struct {
	debounce time.Duration

	ceiling time.Duration
}

var defaultContactsPushTiming = contactsPushTiming{
	debounce: 5 * time.Second,
	ceiling:  30 * time.Second,
}

type contactsPusher struct {
	mu      sync.Mutex
	pending map[string]struct{}
	first   time.Time
	timer   *time.Timer

	timing contactsPushTiming
	send   func(fingerprints []string)
}

func newContactsPusher(send func([]string)) *contactsPusher {
	return &contactsPusher{
		pending: map[string]struct{}{},
		timing:  defaultContactsPushTiming,
		send:    send,
	}
}

func (p *contactsPusher) note(fingerprint string) {
	if p == nil || fingerprint == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, seen := p.pending[fingerprint]; !seen {
		p.pending[fingerprint] = struct{}{}
	}
	now := time.Now()
	if p.first.IsZero() {
		p.first = now
	}
	wait := p.timing.debounce

	if by := p.first.Add(p.timing.ceiling).Sub(now); by < wait {
		wait = by
	}
	if wait < 0 {
		wait = 0
	}
	if p.timer == nil {
		p.timer = time.AfterFunc(wait, p.fire)
		return
	}
	p.timer.Reset(wait)
}

func (p *contactsPusher) fire() {
	p.mu.Lock()
	batch := make([]string, 0, len(p.pending))
	for fp := range p.pending {
		batch = append(batch, fp)
	}
	p.pending = map[string]struct{}{}
	p.first = time.Time{}
	p.mu.Unlock()

	if len(batch) == 0 {
		return
	}

	sort.Strings(batch)
	p.send(batch)
}

func noteContactChanged(d *daemon, fingerprint string) {
	if d == nil || d.contactsPush == nil || fingerprint == "" {
		return
	}
	d.contactsPush.note(fingerprint)
	slog.Debug("contacts: a person changed here; the push is armed",
		slog.String("fingerprint", fingerprint),
	)
}

func noteContactChangedForPeer(d *daemon, peerID string) {
	if d == nil || d.peerMeta == nil || peerID == "" {
		return
	}
	rec, err := d.peerMeta.Get(peerID)
	if err != nil {
		slog.Debug("contacts: could not read a peer to arm the push",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return
	}
	noteContactChanged(d, rec.Fingerprint)
}

func notePairingCompleted(d *daemon, peerID, source string) {
	if d == nil || d.peerMeta == nil || peerID == "" {

		slog.Debug("contacts: a completed pairing was not recorded — no peer id or no contact storage",
			slog.String("peer_id", peerID),
			slog.String("source", source),
		)
		return
	}
	mine, err := d.readIsSibling(peerID)
	if err != nil {
		slog.Warn("contacts: a completed pairing was left for the sweep — this device could not say whether the peer is one of its own",
			slog.String("peer_id", peerID),
			slog.String("source", source),
			slog.Any("err", err),
		)
		return
	}
	if mine {
		slog.Debug("contacts: a completed pairing names one of my own devices; no person is written",
			slog.String("peer_id", peerID),
			slog.String("source", source),
		)
		return
	}
	fingerprint, outcome, err := d.peerMeta.SeedPersonFromPairing(peerID)
	if err != nil {
		slog.Warn("contacts: the person behind a completed pairing was not written",
			slog.String("peer_id", peerID),
			slog.String("source", source),
			slog.Any("err", err),
		)
	} else {
		slog.Debug("contacts: a pairing completed",
			slog.String("peer_id", peerID),
			slog.String("source", source),
			slog.String("fingerprint", fingerprint),
			slog.String("outcome", string(outcome)),
		)
	}
	noteContactChangedForPeer(d, peerID)
}

func contactsPushBody(d *daemon, fingerprints []string) syncrail.ContactsBody {
	body := syncrail.ContactsBody{}
	if d == nil || d.peerMeta == nil {
		return body
	}
	persons, err := d.peerMeta.Persons()
	if err != nil {
		slog.Warn("contacts: the people held here could not be read; the sweep will carry them",
			slog.Any("err", err),
		)
		return body
	}
	byFingerprint := make(map[string]peerstate.Person, len(persons))
	for _, p := range persons {
		byFingerprint[p.Fingerprint] = p
	}
	for _, fp := range fingerprints {
		p, known := byFingerprint[fp]
		if !known {
			slog.Debug("contacts: a marked person is not readable; the sweep will carry them",
				slog.String("fingerprint", fp),
			)
			continue
		}

		if anySibling(d, p.PeerIDs) {
			slog.Debug("contacts: a marked person names one of my own devices; not pushed",
				slog.String("fingerprint", fp),
			)
			continue
		}
		if leaveOutDeleted(d, slog.Default(), fp) {
			continue
		}
		body.Contacts = append(body.Contacts, syncrail.ContactSnapshot{
			Fingerprint: fp,
			Nick:        p.Meta.Nick,
			NickAt:      p.Meta.NickAt,
			Alias:       p.Meta.Alias,
			AliasAt:     p.Meta.AliasAt,
			Verified:    p.Meta.Verified,
			VerifiedAt:  p.Meta.VerifiedAt,
			Blocked:     p.Meta.Blocked,
			BlockedAt:   p.Meta.BlockedAt,
			PairedAt:    newestPairingAt(d, p.PeerIDs),

			PairedDevices:     pairedDevicesForSibling(context.Background(), d, fp),
			PairedDevicesAtMs: time.Now().UnixMilli(),
		})
		if id, ok := contactChatIdentity(d, fp); ok {
			body.Chats = append(body.Chats, id)
		}
	}
	return body
}

func anySibling(d *daemon, peerIDs []string) bool {
	for _, peerID := range peerIDs {
		mine, err := d.readIsSibling(peerID)
		if err != nil {
			slog.Warn("contacts: could not say whether a pairing is one of my own devices; not pushed",
				slog.String("peer_id", peerID),
				slog.Any("err", err),
			)
			return true
		}
		if mine {
			return true
		}
	}
	return false
}

func contactChatIdentity(d *daemon, fingerprint string) (syncrail.ChatIdentity, bool) {
	if d.chats == nil {
		return syncrail.ChatIdentity{}, false
	}
	dc, found := chatForPerson(d, fingerprint)
	if !found {
		slog.Debug("contacts: no conversation here belongs to a person being pushed",
			slog.String("fingerprint", fingerprint),
		)
		return syncrail.ChatIdentity{}, false
	}
	return syncrail.ChatIdentity{
		ChatID:      string(dc.ID),
		Fingerprint: fingerprint,
	}, true
}

func applyChatIdentities(d *daemon, logger *slog.Logger, chats []syncrail.ChatIdentity) {
	if d == nil || d.chats == nil {
		return
	}
	created := 0
	for _, c := range chats {
		if c.ChatID == "" || c.Fingerprint == "" {
			continue
		}

		if gone, _, gErr := personDeletedHere(d, c.Fingerprint); gErr != nil || gone {
			logger.Debug("contacts: the chat of a contact deleted here was refused",
				slog.String("chat_id", c.ChatID),
				slog.Bool("read_failed", gErr != nil),
			)
			continue
		}
		if existing, found := chatForPerson(d, c.Fingerprint); found {
			logger.Debug("contacts: that person already has a conversation here; settings untouched",
				slog.String("fingerprint", c.Fingerprint),
				slog.String("chat_id", string(existing.ID)),
			)
			continue
		}
		if _, err := d.chats.CreateDirectEdgeless(chat.ChatID(c.ChatID), c.Fingerprint); err != nil {
			logger.Warn("contacts: could not adopt a conversation",
				slog.String("chat_id", c.ChatID),
				slog.Any("err", err),
			)
			continue
		}
		logger.Debug("contacts: conversation adopted for a person with no pairing here",
			slog.String("fingerprint", c.Fingerprint),
			slog.String("chat_id", c.ChatID),
		)
		created++
	}
	logger.Debug("contacts: conversations applied",
		slog.Int("received", len(chats)),
		slog.Int("created", created),
	)
	if created == 0 {
		return
	}

	pushInheritedContactsChanged(d, logger, "contacts_push")
}

func sendContactsPush(ctx context.Context, d *daemon, fingerprints []string) {
	if d == nil || d.peerMeta == nil {
		return
	}
	sibs := d.siblings()
	if len(sibs) == 0 {

		return
	}
	body := contactsPushBody(d, fingerprints)
	if len(body.Contacts) == 0 {
		return
	}
	for _, sib := range sibs {
		if ctx.Err() != nil {
			return
		}

		if err := sendGapEnvelope(ctx, d, sib, syncrail.KindContacts, body); err != nil {

			slog.Debug("contacts: push failed; the sweep will carry it",
				slog.String("sibling_peer_id", sib),
				slog.Int("people", len(body.Contacts)),
				slog.Any("err", err),
			)
			continue
		}
		slog.Debug("contacts: pushed to a sibling",
			slog.String("sibling_peer_id", sib),
			slog.Int("people", len(body.Contacts)),
		)
	}
}

func handleContactsPush(ctx context.Context, d *daemon, siblingPeerID string, body syncrail.ContactsBody) {
	logger := slog.With(
		slog.String("sibling_peer_id", siblingPeerID),
		slog.Int("people", len(body.Contacts)),
		slog.Int("conversations", len(body.Chats)),
	)
	applyContacts(d, logger, siblingPeerID, body.Contacts)
	applyChatIdentities(d, logger, body.Chats)
	logger.Debug("contacts: a sibling's changes applied")
	_ = ctx
}

func broadcastPersonPeers(ctx context.Context, d *daemon, fingerprint string) {
	if d == nil || d.peerMeta == nil || fingerprint == "" {
		return
	}
	peerIDs, err := d.peerMeta.PeerIDsByFingerprint(fingerprint)
	if err != nil {
		slog.Debug("contacts: could not resolve a person's pairings to redraw",
			slog.String("fingerprint", fingerprint),
			slog.Any("err", err),
		)
		return
	}
	for _, peerID := range peerIDs {
		pushPeerMetaUpdated(ctx, d, peerID)
	}
	slog.Debug("contacts: redraw pushed for a person",
		slog.String("fingerprint", fingerprint),
		slog.Int("pairings", len(peerIDs)),
	)
}

func personChangedLocally(ctx context.Context, d *daemon, peerID string) {
	pushPeerMetaUpdated(ctx, d, peerID)
	noteContactChangedForPeer(d, peerID)
}
