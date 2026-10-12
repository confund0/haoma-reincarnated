package main

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/syncrail"
)

const backfillBatch = 50

func handleBackfillRequest(ctx context.Context, d *daemon, siblingPeerID string, req syncrail.BackfillRequestBody) {
	logger := slog.With(
		slog.String("sibling_peer_id", siblingPeerID),
		slog.String("section", string(req.Section)),
	)
	logger.Debug("backfill: answering sibling request")

	switch req.Section {
	case syncrail.SectionContacts:
		answerContacts(ctx, d, siblingPeerID, req, logger)

		if req.Cursor == "" && req.DestroySalt != nil {
			healDestroySalt(ctx, d, siblingPeerID, *req.DestroySalt, outboxStateOf(d))
		}
	case syncrail.SectionChats:
		answerChats(ctx, d, siblingPeerID, req, logger)
	case syncrail.SectionSelf:
		answerSelf(ctx, d, siblingPeerID, req, logger)
	default:

		logger.Warn("backfill: no answer implemented for this section")
	}
}

func answerContacts(ctx context.Context, d *daemon, siblingPeerID string, req syncrail.BackfillRequestBody, logger *slog.Logger) {
	if d.peerMeta == nil {
		logger.Warn("backfill: peer-meta not wired; answering empty")
		sendBackfill(ctx, d, siblingPeerID, syncrail.BackfillResponseBody{Section: syncrail.SectionContacts, Full: req.Full}, logger)
		return
	}
	all, err := d.peerMeta.Persons()
	if err != nil {
		logger.Warn("backfill: could not enumerate contacts", slog.Any("err", err))
		return
	}

	persons := all[:0]
	for _, p := range all {
		if !leaveOutDeleted(d, logger, p.Fingerprint) {
			persons = append(persons, p)
		}
	}

	start := 0
	if req.Cursor != "" {
		for i, p := range persons {
			start = i
			if p.Fingerprint > req.Cursor {
				break
			}
			start = i + 1
		}
	}
	end := start + backfillBatch
	if end > len(persons) {
		end = len(persons)
	}
	body := syncrail.BackfillResponseBody{Section: syncrail.SectionContacts, Full: req.Full}
	if req.Cursor == "" {

		body.DeletedContacts = tombstonesForSibling(d, logger)
	}
	for _, p := range persons[start:end] {
		body.Contacts = append(body.Contacts, syncrail.ContactSnapshot{
			Fingerprint: p.Fingerprint,
			Nick:        p.Meta.Nick,
			NickAt:      p.Meta.NickAt,
			Alias:       p.Meta.Alias,
			AliasAt:     p.Meta.AliasAt,
			Verified:    p.Meta.Verified,
			VerifiedAt:  p.Meta.VerifiedAt,
			Blocked:     p.Meta.Blocked,
			BlockedAt:   p.Meta.BlockedAt,
			PairedAt:    newestPairingAt(d, p.PeerIDs),

			PairedDevices:     pairedDevicesForSibling(ctx, d, p.Fingerprint),
			PairedDevicesAtMs: time.Now().UnixMilli(),
		})
	}
	if end < len(persons) {
		body.More = true
		body.Cursor = persons[end-1].Fingerprint
	}
	sendBackfill(ctx, d, siblingPeerID, body, logger)
	logger.Debug("backfill: contacts batch answered",
		slog.Int("contacts", len(body.Contacts)),
		slog.Int("deleted_contacts", len(body.DeletedContacts)),
		slog.Bool("more", body.More),
	)
}

func answerChats(ctx context.Context, d *daemon, siblingPeerID string, req syncrail.BackfillRequestBody, logger *slog.Logger) {
	if d.chats == nil {
		logger.Warn("backfill: chat store not wired; answering empty")
		sendBackfill(ctx, d, siblingPeerID, syncrail.BackfillResponseBody{Section: syncrail.SectionChats, Full: req.Full}, logger)
		return
	}
	all, err := d.chats.List()
	if err != nil {
		logger.Warn("backfill: could not list chats", slog.Any("err", err))
		return
	}
	var snaps []syncrail.ChatSnapshot
	for _, c := range all {
		dc, ok := c.(*chat.DirectChat)
		if !ok {

			continue
		}
		fp := fingerprintForChat(d, dc.ID)
		if fp == "" {

			logger.Debug("backfill: skipping a chat with no identifiable person",
				slog.String("chat_id", string(dc.ID)),
			)
			continue
		}
		if leaveOutDeleted(d, logger, fp) {
			continue
		}
		snaps = append(snaps, syncrail.ChatSnapshot{
			ChatID:           string(dc.ID),
			Fingerprint:      fp,
			RetentionSeconds: dc.RetentionTTL,
			RetentionAt:      dc.LastTimerChangeTs,
			Muted:            dc.NotificationsMuted,
			ReceiptsDisabled: dc.DisableReadReceipts,
			NickOverride:     dc.NickOverride,
			SettingsAt: syncrail.SettingsStamps{
				Muted:        dc.SettingsAt.Muted,
				Receipts:     dc.SettingsAt.Receipts,
				NickOverride: dc.SettingsAt.NickOverride,
			},
		})
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].ChatID < snaps[j].ChatID })

	start := 0
	if req.Cursor != "" {
		for i, s := range snaps {
			if s.ChatID > req.Cursor {
				start = i
				break
			}
			start = i + 1
		}
	}
	end := start + backfillBatch
	if end > len(snaps) {
		end = len(snaps)
	}
	body := syncrail.BackfillResponseBody{
		Section: syncrail.SectionChats,
		Chats:   snaps[start:end],
		Full:    req.Full,
	}
	if end < len(snaps) {
		body.More = true
		body.Cursor = snaps[end-1].ChatID
	}
	sendBackfill(ctx, d, siblingPeerID, body, logger)
	logger.Debug("backfill: chats batch answered",
		slog.Int("chats", len(body.Chats)),
		slog.Bool("more", body.More),
	)
}

func answerSelf(ctx context.Context, d *daemon, siblingPeerID string, req syncrail.BackfillRequestBody, logger *slog.Logger) {
	body := syncrail.BackfillResponseBody{Section: syncrail.SectionSelf, Full: req.Full}
	snap, ok := selfNickSnapshot(d)
	if ok {
		body.Self = &snap
	}
	sendBackfill(ctx, d, siblingPeerID, body, logger)
	logger.Debug("backfill: self answered",
		slog.Bool("has_nick", ok),
		slog.Int64("nick_at", snap.NickAt),
	)
}

func sendBackfill(ctx context.Context, d *daemon, siblingPeerID string, body syncrail.BackfillResponseBody, logger *slog.Logger) {
	if err := sendGapEnvelope(ctx, d, siblingPeerID, syncrail.KindBackfillResponse, body); err != nil {
		logger.Warn("backfill: response send failed; sibling stays empty", slog.Any("err", err))
	}
}
