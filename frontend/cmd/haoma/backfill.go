package main

import (
	"context"
	"errors"
	"log/slog"
	"slices"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/peerstate"
	"haoma-frontend/internal/syncrail"
)

func askBackfill(ctx context.Context, d *daemon, siblingPeerID string, section syncrail.BackfillSection, cursor string, full bool) error {
	return sendBackfillRequest(ctx, d, siblingPeerID, syncrail.BackfillRequestBody{
		Section: section,
		Cursor:  cursor,
		Full:    full,
	})
}

func askBackfillForSweep(ctx context.Context, d *daemon, siblingPeerID string, full bool) error {
	return sendBackfillRequest(ctx, d, siblingPeerID, syncrail.BackfillRequestBody{
		Section:     syncrail.SectionContacts,
		Full:        full,
		DestroySalt: destroySaltReportFor(d, siblingPeerID),
	})
}

func sendBackfillRequest(ctx context.Context, d *daemon, siblingPeerID string, body syncrail.BackfillRequestBody) error {
	logger := slog.With(
		slog.String("sibling_peer_id", siblingPeerID),
		slog.String("section", string(body.Section)),
	)
	if err := sendGapEnvelope(ctx, d, siblingPeerID, syncrail.KindBackfillRequest, body); err != nil {
		logger.Warn("backfill: request send failed; this device stays empty", slog.Any("err", err))
		return err
	}
	logger.Debug("backfill: section requested",
		slog.String("cursor", body.Cursor),
		slog.Bool("destroy_salt_reported", body.DestroySalt != nil),
	)
	return nil
}

func handleBackfillResponse(ctx context.Context, d *daemon, siblingPeerID string, resp syncrail.BackfillResponseBody) {
	logger := slog.With(
		slog.String("sibling_peer_id", siblingPeerID),
		slog.String("section", string(resp.Section)),
	)

	var changed int
	switch resp.Section {
	case syncrail.SectionContacts:

		changed = applyDeletedContacts(ctx, d, logger, resp.DeletedContacts, "sibling_sweep")
		changed += applyContacts(d, logger, siblingPeerID, resp.Contacts)
	case syncrail.SectionChats:
		changed = applyChats(d, logger, resp.Chats)
	case syncrail.SectionSelf:
		if resp.Self != nil {
			if applySelfNick(d, logger, *resp.Self) {
				changed = 1
			}
		} else {
			logger.Debug("backfill: the sibling is still on the placeholder and named itself nothing")
		}
	default:
		logger.Debug("backfill: response names a section this build does not apply")
		return
	}

	d.manualSync.snapshot(siblingPeerID, resp.Full, changed)

	if ctx.Err() != nil {
		return
	}
	if resp.More {
		if resp.Cursor == "" {

			logger.Warn("backfill: answer claims more but names no cursor; abandoning this exchange")
			return
		}
		if err := askBackfill(ctx, d, siblingPeerID, resp.Section, resp.Cursor, resp.Full); err != nil {
			d.manualSync.sendFailed(d, siblingPeerID, resp.Full)
		}
		return
	}

	switch resp.Section {
	case syncrail.SectionContacts:

		if err := askBackfill(ctx, d, siblingPeerID, syncrail.SectionChats, "", resp.Full); err != nil {
			d.manualSync.sendFailed(d, siblingPeerID, resp.Full)
		}
		return
	case syncrail.SectionChats:

		if err := askBackfill(ctx, d, siblingPeerID, syncrail.SectionSelf, "", resp.Full); err != nil {
			d.manualSync.sendFailed(d, siblingPeerID, resp.Full)
		}
		return
	case syncrail.SectionSelf:

	default:

		logger.Warn("backfill: no next step is defined for this section; abandoning this exchange")
		return
	}

	logger.Info("backfill: snapshot complete")

	d.manualSync.journalAsked(siblingPeerID, resp.Full)

	if err := askSibling(ctx, d, siblingPeerID, true, resp.Full); err != nil {
		d.manualSync.sendFailed(d, siblingPeerID, resp.Full)
	}
}

const backfillContactsAppliedLine = "backfill: contacts applied"

func applyContacts(d *daemon, logger *slog.Logger, siblingPeerID string, contacts []syncrail.ContactSnapshot) int {
	if d.peerMeta == nil {
		return 0
	}
	merged, refused, reports := 0, 0, 0
	var moved []string
	for _, c := range contacts {
		if c.Fingerprint == "" {
			continue
		}

		gone, tomb, gErr := personDeletedHere(d, c.Fingerprint)
		if gErr != nil {
			logger.Warn("backfill: could not read whether a contact was deleted here; not merged",
				slog.String("fingerprint", c.Fingerprint),
				slog.Any("err", gErr),
			)
			continue
		}
		if gone && c.PairedAt <= tomb.DeletedAt {
			logger.Debug("backfill: a contact deleted here was refused",
				slog.String("fingerprint", c.Fingerprint),
				slog.Int64("deleted_at", tomb.DeletedAt),
				slog.Int64("paired_at", c.PairedAt),
			)
			refused++
			continue
		}
		changed, err := d.peerMeta.MergePerson(c.Fingerprint, peerstate.MetaRecord{
			Nick:       c.Nick,
			NickAt:     c.NickAt,
			Alias:      c.Alias,
			AliasAt:    c.AliasAt,
			Verified:   c.Verified,
			VerifiedAt: c.VerifiedAt,
			Blocked:    c.Blocked,
			BlockedAt:  c.BlockedAt,
		})
		if err != nil {
			logger.Warn("backfill: merging a contact failed", slog.Any("err", err))
			continue
		}
		if changed {
			merged++
			moved = append(moved, c.Fingerprint)
		}
		if siblingPeerID != "" && c.PairedDevicesAtMs > 0 {
			reportMoved, err := d.peerMeta.ObserveSiblingDevices(c.Fingerprint, siblingPeerID, c.PairedDevices, c.PairedDevicesAtMs)
			if err != nil {
				logger.Warn("backfill: what my linked device said about its pairings with a person was not kept",
					slog.String("fingerprint", c.Fingerprint),
					slog.Any("err", err),
				)
			} else if reportMoved {
				reports++

				logger.Debug("backfill: my linked device's pairings with a person changed",
					slog.String("fingerprint", c.Fingerprint),
					slog.Int("devices", len(c.PairedDevices)),
				)
			}
		}
	}
	logger.Debug(backfillContactsAppliedLine,
		slog.Int("received", len(contacts)),
		slog.Int("merged", merged),
		slog.Int("refused_deleted", refused),
		slog.Int("sibling_device_reports_moved", reports),
	)
	if merged == 0 && reports > 0 {

		pushInheritedContactsChanged(d, logger, "sibling_devices")
		return 0
	}
	if merged == 0 {

		return 0
	}

	for _, fp := range moved {
		broadcastPersonPeers(context.Background(), d, fp)
	}
	pushInheritedContactsChanged(d, logger, "backfill_contacts")
	return merged
}

func applyChats(d *daemon, logger *slog.Logger, chats []syncrail.ChatSnapshot) int {
	if d.chats == nil {
		return 0
	}
	created, settingsMoved := 0, 0
	for _, cs := range chats {
		if cs.ChatID == "" || cs.Fingerprint == "" {
			continue
		}

		if gone, _, gErr := personDeletedHere(d, cs.Fingerprint); gErr != nil || gone {
			logger.Debug("backfill: the chat of a contact deleted here was refused",
				slog.String("chat_id", cs.ChatID),
				slog.Bool("read_failed", gErr != nil),
			)
			continue
		}

		if existing, found := chatForPerson(d, cs.Fingerprint); found {
			if mergeChatSettings(d, logger, existing.ID, cs) {
				settingsMoved++
			}
			continue
		}
		dc, err := d.chats.CreateDirectEdgeless(chat.ChatID(cs.ChatID), cs.Fingerprint)
		if err != nil {
			logger.Warn("backfill: could not adopt a conversation",
				slog.String("chat_id", cs.ChatID),
				slog.Any("err", err),
			)
			continue
		}
		if cs.RetentionSeconds > 0 || cs.RetentionAt > 0 {

			if err := d.chats.SetRetentionAndTimerTs(dc.ID, cs.RetentionSeconds, cs.RetentionAt); err != nil {
				logger.Warn("backfill: retention not applied", slog.Any("err", err))
			}
		}

		mergeChatSettings(d, logger, dc.ID, cs)
		created++
	}
	logger.Debug("backfill: chats applied",
		slog.Int("received", len(chats)),
		slog.Int("created", created),
	)
	if created == 0 {
		return settingsMoved
	}

	pushInheritedContactsChanged(d, logger, "backfill_chats")
	return created + settingsMoved
}

func chatForPerson(d *daemon, fingerprint string) (*chat.DirectChat, bool) {
	if d == nil || d.chats == nil || fingerprint == "" {
		return nil, false
	}
	if dc, err := d.chats.GetByFingerprint(fingerprint); err == nil {
		return dc, true
	} else if !errors.Is(err, chat.ErrNotFound) {
		slog.Warn("chat-by-person lookup failed", slog.Any("err", err))
		return nil, false
	}
	if d.peerMeta == nil {
		return nil, false
	}
	peerIDs, err := d.peerMeta.PeerIDsByFingerprint(fingerprint)
	if err != nil {
		slog.Warn("peers-by-person lookup failed", slog.Any("err", err))
		return nil, false
	}
	for _, pid := range peerIDs {
		if dc, err := d.chats.GetByDirectPeer(pid); err == nil {
			return dc, true
		}
	}
	return nil, false
}

func mergeChatSettings(d *daemon, logger *slog.Logger, chatID chat.ChatID, cs syncrail.ChatSnapshot) (moved bool) {
	if d == nil || d.chats == nil || chatID == "" {
		return false
	}

	if cs.RetentionSeconds > 0 || cs.RetentionAt > 0 {
		if err := d.chats.SetRetentionAndTimerTs(chatID, cs.RetentionSeconds, cs.RetentionAt); err != nil {
			logger.Warn("backfill: retention not applied", slog.Any("err", err))
		}
	}

	fields, err := d.chats.MergeSettings(chatID, chat.SettingsUpdate{
		Muted:        cs.Muted,
		Receipts:     cs.ReceiptsDisabled,
		NickOverride: cs.NickOverride,
		At: chat.SettingsStamps{
			Muted:        cs.SettingsAt.Muted,
			Receipts:     cs.SettingsAt.Receipts,
			NickOverride: cs.SettingsAt.NickOverride,
		},
	})
	if err != nil {
		logger.Warn("backfill: chat settings not merged",
			slog.String("chat_id", string(chatID)),
			slog.Any("err", err),
		)
		return false
	}
	if len(fields) == 0 {
		logger.Debug("backfill: chat settings already agreed",
			slog.String("chat_id", string(chatID)),
		)
		return false
	}
	logger.Debug("backfill: chat settings merged from sibling",
		slog.String("chat_id", string(chatID)),
		slog.Any("fields", fields),
	)

	if !slices.Contains(fields, "receipts") || !cs.ReceiptsDisabled {
		return true
	}
	n, err := d.events.SuppressPendingReadReceipts(chatID)
	if err != nil {
		logger.Warn("backfill: pending receipts not suppressed after inheriting the toggle",
			slog.String("chat_id", string(chatID)),
			slog.Any("err", err),
		)
		return true
	}
	logger.Debug("backfill: pending receipts suppressed after inheriting a receipts-off toggle",
		slog.String("chat_id", string(chatID)),
		slog.Int("rows", n),
	)
	return true
}
