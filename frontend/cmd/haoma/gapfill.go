package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync/atomic"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/gapfill"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/syncrail"
)

type gapfillGate struct{ running atomic.Bool }

func (g *gapfillGate) enter() bool { return g.running.CompareAndSwap(false, true) }
func (g *gapfillGate) leave()      { g.running.Store(false) }

type sweepOutcome string

const (
	sweepStarted sweepOutcome = "started"

	sweepNoSiblings sweepOutcome = "no_siblings"

	sweepAlreadyRunning sweepOutcome = "already_running"
)

func sweepGapFill(ctx context.Context, d *daemon) sweepOutcome {
	sibs := d.siblings()
	if len(sibs) == 0 {

		return sweepNoSiblings
	}
	if !d.gapfill.enter() {
		slog.Debug("gapfill: sweep already running; skipping this trigger")
		return sweepAlreadyRunning
	}
	defer d.gapfill.leave()

	slog.Debug("gapfill: sweep starting", slog.Int("sibling_count", len(sibs)))
	for _, sib := range sibs {
		if ctx.Err() != nil {
			return sweepStarted
		}

		_ = askBackfillForSweep(ctx, d, sib, false)
	}
	return sweepStarted
}

func askSibling(ctx context.Context, d *daemon, siblingPeerID string, reciprocate, full bool) error {
	logger := slog.With(slog.String("sibling_peer_id", siblingPeerID))

	watermark, err := d.watermarks.Get(siblingPeerID)
	if err != nil {
		logger.Warn("gapfill: watermark read failed; not asking this sibling",
			slog.Any("err", err),
		)
		return err
	}
	if err := sendGapEnvelope(ctx, d, siblingPeerID, syncrail.KindGapRequest, syncrail.GapRequestBody{
		Watermark:   watermark,
		Full:        full,
		Reciprocate: reciprocate,
	}); err != nil {
		logger.Warn("gapfill: request send failed; this device stays behind",
			slog.Uint64("watermark", watermark),
			slog.Any("err", err),
		)
		return err
	}
	logger.Debug("gapfill: asked sibling what we missed",
		slog.Uint64("watermark", watermark),
		slog.Bool("reciprocate", reciprocate),
		slog.Bool("full", full),
	)
	return nil
}

func handleGapResponse(ctx context.Context, d *daemon, siblingPeerID string, resp syncrail.GapResponseBody) {
	logger := slog.With(
		slog.String("sibling_peer_id", siblingPeerID),
		slog.Uint64("range_end", resp.RangeEnd),
	)
	logger.Debug("gapfill: batch received", slog.Int("rows", len(resp.Rows)))

	var applied, skipped, held int

	touchedChats := map[chat.ChatID]bool{}
	for _, row := range resp.Rows {
		changed, chatID, err := applyGapRow(ctx, d, logger, row)
		if err != nil {

			if gapRowRetryable(err) {
				held++
				logger.Debug("gapfill: row held for a later sweep — this device has not met that contact yet",
					slog.String("msg_id", row.MsgID),
					slog.Any("err", err),
				)
				continue
			}
			logger.Debug("gapfill: row not applied",
				slog.String("msg_id", row.MsgID),
				slog.Any("err", err),
			)
			skipped++
			continue
		}
		if changed {
			applied++
		}
		if chatID != "" {
			touchedChats[chatID] = true
		}
	}
	for chatID := range touchedChats {
		recomputeChatUnread(ctx, d, chatID)
	}

	var moved bool
	if held > 0 {
		logger.Info("gapfill: holding the sync cursor — some rows belong to contacts this device does not have",
			slog.Int("held", held),
		)
	} else {
		var err error
		moved, err = d.watermarks.Advance(siblingPeerID, resp.RangeEnd)
		if err != nil {
			logger.Warn("gapfill: watermark advance failed; this range will be swept again",
				slog.Any("err", err),
			)
		}
	}
	logger.Debug("gapfill: batch merged",
		slog.Int("rows", len(resp.Rows)),
		slog.Int("applied", applied),
		slog.Int("skipped", skipped),
		slog.Int("held", held),
		slog.Bool("watermark_moved", moved),
		slog.Bool("more", resp.More),
	)

	d.manualSync.batch(d, siblingPeerID, resp.Full, resp.More, applied, held)

	if resp.More && ctx.Err() == nil {
		if err := askSibling(ctx, d, siblingPeerID, false, resp.Full); err != nil {
			d.manualSync.sendFailed(d, siblingPeerID, resp.Full)
		}
		return
	}

	if !resp.More && ctx.Err() == nil {
		askForFiles(ctx, d, siblingPeerID)
	}
}

func gapRowRetryable(err error) bool {
	return errors.Is(err, errRailNoPeerForFingerprint) || errors.Is(err, errRailNoChatForPeer)
}

func applyGapRow(ctx context.Context, d *daemon, logger *slog.Logger, row syncrail.RowSnapshot) (changed bool, chatID chat.ChatID, err error) {
	if row.MsgID == "" {
		return false, "", errors.New("snapshot has no msg id")
	}
	if row.Fingerprint == "" {
		return false, "", errors.New("snapshot has no routing fingerprint")
	}

	if skip, gErr := gapRowOfDeletedContact(d, row); gErr == nil && skip {
		return false, "", errRowOfDeletedContact
	}

	chatID, localPeerID, err := resolveChatByFingerprint(d, row.Fingerprint)
	if err != nil {

		return false, "", err
	}

	if events.Kind(row.Kind) == events.KindFile {
		honest, hErr := gapFileBody(row.Body)
		if hErr != nil {
			return false, "", hErr
		}
		if !bytes.Equal(honest, row.Body) {
			logger.Debug("gapfill: file row body arrived with the sibling's own transfer state; stripped",
				slog.String("msg_id", row.MsgID),
				slog.String("outcome", "file_body_stripped"),
			)
		}
		row.Body = honest
	}

	local := gapfill.LocalRow{}
	existing, err := d.events.GetByMsgID(row.MsgID)
	switch {
	case err == nil:
		local = gapfill.LocalRow{
			Present:   true,
			Body:      existing.Body,
			EditedAt:  existing.EditedAt,
			DeletedAt: existing.DeletedAt,
		}
	case errors.Is(err, events.ErrEventNotFound):
	default:
		return false, "", err
	}

	action := gapfill.Decide(local, gapfill.Incoming{
		Body:      row.Body,
		EditedAt:  row.EditedAt,
		DeletedAt: row.DeletedAt,
		Swept:     row.Swept,
	})

	bodyChanged := false
	switch action {
	case gapfill.ActionNone:

	case gapfill.ActionInsert:
		inserted, err := insertGapRow(ctx, d, logger, chatID, localPeerID, row)
		if err != nil {
			return false, "", err
		}
		bodyChanged = inserted

	case gapfill.ActionEdit:
		var body events.TextBody
		if err := json.Unmarshal(row.Body, &body); err != nil {
			return false, "", err
		}

		editor := ""
		if existing.Direction == events.DirIn {
			editor = localPeerID
		}
		if _, err := d.events.ApplyEdit(row.MsgID, body.Text, row.EditedAt, editor); err != nil {
			return false, "", err
		}
		logger.Debug("gapfill: edit merged", slog.String("msg_id", row.MsgID))
		bodyChanged = true

	case gapfill.ActionDelete:
		deleter := ""
		if existing.Direction == events.DirIn {
			deleter = localPeerID
		}
		at := row.DeletedAt
		if at == 0 {

			at = existing.DisplayTs
			if at == 0 {
				at = row.DisplayTs
			}
		}
		if _, err := d.events.ApplyDelete(row.MsgID, at, deleter); err != nil {
			return false, "", err
		}
		logger.Debug("gapfill: tombstone merged",
			slog.String("msg_id", row.MsgID),
			slog.Bool("from_swept_row", row.Swept),
		)
		bodyChanged = true
	}

	convergenceChanged := applyRowConvergence(d, logger, chatID, row)
	return bodyChanged || convergenceChanged, chatID, nil
}

func applyRowConvergence(d *daemon, logger *slog.Logger, chatID chat.ChatID, row syncrail.RowSnapshot) (changed bool) {
	if events.Kind(row.Kind) != events.KindText && events.Kind(row.Kind) != events.KindFile {
		return false
	}
	if row.Inbound {

		if row.ReadAt > 0 {
			if _, ok, err := d.events.ApplyInboundReadAt(row.MsgID, row.ReadAt); err != nil {
				logger.Debug("gapfill: read-time merge failed",
					slog.String("msg_id", row.MsgID), slog.Any("err", err))
			} else if ok {
				changed = true
				logger.Debug("gapfill: read time merged from sibling", slog.String("msg_id", row.MsgID))
			}
		}

		logger.Debug("gapfill: inbound receipt claim observed",
			slog.String("msg_id", row.MsgID),
			slog.Bool("claimed", row.ReceiptSentAt > 0),
			slog.Uint64("generation", row.ReceiptGeneration),
		)
		if row.ReceiptSentAt > 0 {
			if _, ok, err := d.events.ApplyReceiptSentClaim(row.MsgID, row.ReceiptSentAt, row.ReceiptGeneration); err != nil {
				logger.Debug("gapfill: receipt-sent claim merge failed",
					slog.String("msg_id", row.MsgID), slog.Any("err", err))
			} else if ok {
				changed = true
				logger.Debug("gapfill: receipt-sent claim merged", slog.String("msg_id", row.MsgID))
			}
		}
		return changed
	}

	if row.ReadAt > 0 {
		if _, ok, err := d.events.ApplyReadReceipt(row.MsgID, row.ReadAt, chatID); err != nil {
			logger.Debug("gapfill: peer read-time merge failed",
				slog.String("msg_id", row.MsgID), slog.Any("err", err))
		} else if ok {
			changed = true
			logger.Debug("gapfill: peer read time merged from sibling", slog.String("msg_id", row.MsgID))
		}
	}
	if row.DeliveryRank != "" || row.DeliveryFailed {
		if _, ok, err := d.events.ApplyDeliverySnapshot(row.MsgID, row.DeliveryRank, row.DeliveryFailed); err != nil {
			logger.Debug("gapfill: delivery rank merge failed",
				slog.String("msg_id", row.MsgID), slog.Any("err", err))
		} else if ok {
			changed = true
			logger.Debug("gapfill: delivery rank merged from sibling",
				slog.String("msg_id", row.MsgID),
				slog.String("rank", row.DeliveryRank),
				slog.Bool("failed", row.DeliveryFailed),
			)
		}
	}
	return changed
}

func insertGapRow(ctx context.Context, d *daemon, logger *slog.Logger, chatID chat.ChatID, localPeerID string, row syncrail.RowSnapshot) (bool, error) {
	switch events.Kind(row.Kind) {
	case events.KindText, events.KindFile:
		if row.Inbound {

			if _, err := d.events.AppendInbound(events.InboundParams{
				ChatID:              chatID,
				Kind:                events.Kind(row.Kind),
				DisplayTs:           row.DisplayTs,
				SenderTs:            row.SenderTs,
				SenderPeerID:        localPeerID,
				MsgID:               row.MsgID,
				ExpireSeconds:       row.ExpireSeconds,
				Status:              events.DecryptOK,
				Body:                row.Body,
				MirroredFromSibling: true,
			}); err != nil {
				if errors.Is(err, events.ErrDuplicateMsgID) {
					return logGapRowAlreadyHeld(logger, row), nil
				}
				return false, err
			}

			maybeSuppressInboundReceipt(d, chatID, row.MsgID)
			if events.Kind(row.Kind) == events.KindFile {
				noteWantedFile(d, logger, chatID, row.MsgID, row.DisplayTs, "gapfill")
			}
			break
		}

		out, err := d.events.AppendOutbound(events.OutboundParams{
			ChatID:              chatID,
			Kind:                events.Kind(row.Kind),
			DisplayTs:           row.DisplayTs,
			MsgID:               row.MsgID,
			ExpireSeconds:       row.ExpireSeconds,
			Body:                row.Body,
			MirroredFromSibling: true,
		})
		if err != nil {
			if errors.Is(err, events.ErrDuplicateMsgID) {
				return logGapRowAlreadyHeld(logger, row), nil
			}
			return false, err
		}
		logger.Debug("gapfill: recovered outbound row seeded",
			slog.String("msg_id", row.MsgID),
			slog.String("delivery_state", out.DeliveryState),
		)
		if events.Kind(row.Kind) == events.KindFile {
			noteWantedFile(d, logger, chatID, row.MsgID, row.DisplayTs, "gapfill")
		}

	case events.KindReaction:
		var body events.ReactionBody
		if err := json.Unmarshal(row.Body, &body); err != nil {
			return false, err
		}
		reactor := ""
		if row.Inbound {
			reactor = localPeerID
		}
		if _, err := d.events.AppendReactionBreadcrumb(
			body.TargetMsgID, body.Emoji, reactor, row.MsgID, body.At, row.ExpireSeconds,
		); err != nil {
			if errors.Is(err, events.ErrDuplicateMsgID) {
				return logGapRowAlreadyHeld(logger, row), nil
			}

			return false, err
		}

	case events.KindCallSummary:

		if row.Inbound {
			return false, errSyncedCallSummaryNotOutgoing
		}
		inserted, err := appendSyncedCallSummary(d, chatID, row.MsgID, row.DisplayTs, row.ExpireSeconds, row.Body)
		if errors.Is(err, errSyncedCallSummaryExpired) {

			logger.Debug("gapfill: call summary already expired — not written",
				slog.String("msg_id", row.MsgID),
				slog.String("outcome", "noop_expired"),
			)
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !inserted {
			return logGapRowAlreadyHeld(logger, row), nil
		}

	default:
		return false, nil
	}

	bumpChatActivity(ctx, d, chatID, row.DisplayTs)
	logger.Debug("gapfill: row inserted",
		slog.String("msg_id", row.MsgID),
		slog.String("kind", row.Kind),
		slog.Bool("inbound", row.Inbound),
	)
	return true, nil
}

func logGapRowAlreadyHeld(logger *slog.Logger, row syncrail.RowSnapshot) bool {
	logger.Debug("gapfill: row already held — no second row",
		slog.String("msg_id", row.MsgID),
		slog.String("kind", row.Kind),
		slog.Bool("inbound", row.Inbound),
		slog.String("outcome", "noop_duplicate"),
	)
	return false
}

func (sd *sessionDispatcher) handleDeviceSync(_ context.Context, sess *ipc.Session, f ipc.Frame) {
	var req ipc.DeviceSyncRequest
	if len(f.Payload) > 0 {
		if err := json.Unmarshal(f.Payload, &req); err != nil {
			sendError(sess, f.ID, reasonBadPayload, err.Error())
			return
		}
	}
	d := sd.d
	sibs := d.siblings()
	slog.Debug("gapfill: manual sweep requested",
		slog.Int("sibling_count", len(sibs)),
		slog.Bool("full", req.Full),
	)

	alreadyRunning := false
	if len(sibs) > 0 {
		sweepCtx := d.relayContext()
		switch {
		case d.manualSync.waiting():
			alreadyRunning = true
			slog.Debug("gapfill: manual sweep skipped; the last one is still waiting for answers")
		case !enterGateWithin(sweepCtx, &d.gapfill, d.manualSync.gateBound()):
			alreadyRunning = true
			slog.Debug("gapfill: manual sweep skipped; the gate stayed taken",
				slog.Duration("waited", d.manualSync.gateBound()),
			)
		default:
			sw := d.manualSync.begin(sibs, req.Full)
			if sw == nil {
				d.gapfill.leave()
				alreadyRunning = true
				slog.Debug("gapfill: manual sweep skipped; the last one is still waiting for answers")
				break
			}
			go func() {
				func() {
					defer d.gapfill.leave()
					for _, sib := range sibs {
						if sweepCtx.Err() != nil {
							return
						}

						if err := askBackfillForSweep(sweepCtx, d, sib, req.Full); err != nil {

							d.manualSync.sendFailed(d, sib, req.Full)
						}
					}
				}()

				d.manualSync.wait(sweepCtx, d, sw)
			}()
		}
	}

	resp, err := ipc.NewFrame(ipc.FrameDeviceSyncSent, f.ID, ipc.DeviceSyncSentResponse{
		Siblings:       len(sibs),
		Full:           req.Full,
		AlreadyRunning: alreadyRunning,
	})
	if err != nil {
		sendError(sess, f.ID, reasonEncodeFrame, err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("gapfill: send sync-sent frame failed", slog.Any("err", err))
	}
}

func pruneJournalForSiblings(d *daemon) {
	sibs := d.siblings()
	if len(sibs) == 0 {

		return
	}

	acked, err := d.watermarks.AllAcks()
	if err != nil {
		slog.Warn("gapfill: acknowledgement scan failed; journal not pruned", slog.Any("err", err))
		return
	}
	var floor uint64
	for i, sib := range sibs {
		wm := acked[sib]
		if wm == 0 {

			return
		}
		if i == 0 || wm < floor {
			floor = wm
		}
	}
	if floor == 0 {
		return
	}
	pruned, err := d.events.JournalPrune(floor)
	if err != nil {
		slog.Warn("gapfill: journal prune failed", slog.Any("err", err))
		return
	}
	if pruned > 0 {
		slog.Debug("gapfill: journal pruned",
			slog.Int("entries", pruned),
			slog.Uint64("through_seq", floor),
		)
	}
}
