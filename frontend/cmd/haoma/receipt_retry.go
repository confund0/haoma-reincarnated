package main

import (
	"context"
	"errors"
	"log/slog"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
)

const (
	deliveryStateFailed    = "failed"
	deliveryStateSent      = "sent"
	deliveryStateDelivered = "delivered"
	deliveryStateRead      = "read"
)

var outboxScanLimit = 10_000

func applyReceiptOutcome(d *daemon, envelopeID, state string) (bool, int) {
	if d.events == nil || envelopeID == "" {
		return false, 0
	}
	switch state {
	case deliveryStateFailed:
		found, cleared, err := d.events.ClearReceiptEnvelope(envelopeID)
		if err != nil {
			slog.Warn("read receipt: clearing stamps after give-up failed (receipt stays lost)",
				slog.String("envelope_id", envelopeID),
				slog.Any("err", err),
			)
			return false, 0
		}
		if !found {
			return false, 0
		}

		slog.Info("read receipt gave up — stamps withdrawn, rows back to pending",
			slog.String("envelope_id", envelopeID),
			slog.Int("rows_cleared", cleared),
		)
		return true, cleared
	case deliveryStateSent, deliveryStateDelivered, deliveryStateRead:
		found, err := d.events.DropReceiptEnvelope(envelopeID)
		if err != nil {
			slog.Warn("read receipt: dropping envelope mapping failed (stale mapping left behind)",
				slog.String("envelope_id", envelopeID),
				slog.Any("err", err),
			)
			return false, 0
		}
		if found {
			slog.Debug("read receipt landed; envelope mapping dropped",
				slog.String("envelope_id", envelopeID),
				slog.String("state", state),
			)
		}
		return found, 0
	default:

		return false, 0
	}
}

func sweepDeliveryReconcile(ctx context.Context, d *daemon) {
	if d.events == nil || d.backendClient == nil {
		return
	}
	if !d.deliveryReconcileRunning.CompareAndSwap(false, true) {
		slog.Debug("delivery reconcile: already running, skipping this edge")
		return
	}
	defer d.deliveryReconcileRunning.Store(false)

	resolved, cleared := reconcileReceiptEnvelopes(ctx, d)
	repaired := reconcileDeliveryMarkers(ctx, d)

	slog.Info("delivery reconcile complete",
		slog.Int("receipts_resolved", resolved),
		slog.Int("receipt_rows_cleared", cleared),
		slog.Int("markers_repaired", repaired),
	)
}

func reconcileReceiptEnvelopes(ctx context.Context, d *daemon) (resolved, cleared int) {
	pending, err := d.events.ListReceiptEnvelopes()
	if err != nil {
		slog.Warn("delivery reconcile: listing receipt envelopes failed", slog.Any("err", err))
		return 0, 0
	}
	if len(pending) == 0 {
		return 0, 0
	}
	slog.Debug("delivery reconcile: receipt envelopes awaiting an outcome",
		slog.Int("count", len(pending)),
	)
	for _, rec := range pending {
		if ctx.Err() != nil {
			return resolved, cleared
		}
		row, err := d.backendClient.OutboxEntry(ctx, rec.EnvelopeID)
		switch {
		case errors.Is(err, backendapi.ErrOutboxEntryNotFound):

			_, n, clearErr := d.events.ClearReceiptEnvelope(rec.EnvelopeID)
			if clearErr != nil {
				slog.Warn("delivery reconcile: clearing unknown envelope failed",
					slog.String("envelope_id", rec.EnvelopeID),
					slog.Any("err", clearErr),
				)
				continue
			}
			resolved++
			cleared += n
			slog.Info("read receipt envelope unknown to haomad — stamps withdrawn",
				slog.String("envelope_id", rec.EnvelopeID),
				slog.Int("rows_cleared", n),
			)
		case err != nil:

			slog.Warn("delivery reconcile: outbox lookup failed, leaving mapping intact",
				slog.String("envelope_id", rec.EnvelopeID),
				slog.Any("err", err),
			)
		default:
			handled, n := applyReceiptOutcome(d, row.EnvelopeID, row.State)
			if !handled {
				slog.Debug("delivery reconcile: receipt still in flight",
					slog.String("envelope_id", rec.EnvelopeID),
					slog.String("state", row.State),
				)
				continue
			}
			resolved++
			cleared += n
		}
	}
	return resolved, cleared
}

func reconcileDeliveryMarkers(ctx context.Context, d *daemon) int {
	since, err := d.events.OutboxReconcileWatermark()
	if err != nil {
		slog.Warn("delivery reconcile: reading watermark failed", slog.Any("err", err))
		return 0
	}
	repaired := 0
	highest := since
	truncated := false
	for _, state := range []string{deliveryStateSent, deliveryStateFailed} {
		if ctx.Err() != nil {
			return repaired
		}

		resp, err := d.backendClient.Outbox(ctx, state, since, outboxScanLimit)
		if err != nil {
			slog.Warn("delivery reconcile: listing outbox failed",
				slog.String("state", state),
				slog.Any("err", err),
			)
			return repaired
		}
		if len(resp.Rows) >= outboxScanLimit {

			truncated = true
			slog.Warn("delivery reconcile: outbox page came back full; coverage is partial this pass",
				slog.String("state", state),
				slog.Int("rows", len(resp.Rows)),
			)
		}
		for _, row := range resp.Rows {
			if row.StateChangedAt > highest {
				highest = row.StateChangedAt
			}
			cur, err := d.events.GetByEnvelopeID(row.EnvelopeID)
			if err != nil {

				if !errors.Is(err, events.ErrEventNotFound) {
					slog.Warn("delivery reconcile: reading current marker failed",
						slog.String("envelope_id", row.EnvelopeID),
						slog.Any("err", err),
					)
				}
				continue
			}
			if cur.DeliveryState == row.State {
				continue
			}
			updated, moved, err := d.events.UpdateDeliveryState(row.EnvelopeID, row.State)
			if err != nil {
				if !errors.Is(err, events.ErrEventNotFound) {
					slog.Warn("delivery reconcile: persisting state failed",
						slog.String("envelope_id", row.EnvelopeID),
						slog.Any("err", err),
					)
				}
				continue
			}
			if !moved {

				reason := "outranked"
				if row.State == events.DeliveryFailed && events.DeliveryAdvances(updated.DeliveryState, events.DeliveryFailed) {
					reason = "other_envelopes_pending"
				}
				slog.Debug("delivery reconcile: outbox report did not move the row",
					slog.String("envelope_id", row.EnvelopeID),
					slog.String("row_state", updated.DeliveryState),
					slog.String("outbox_state", row.State),
					slog.String("reason", reason),
				)
				continue
			}
			slog.Debug("delivery reconcile: marker repaired",
				slog.String("envelope_id", row.EnvelopeID),
				slog.String("from_state", cur.DeliveryState),
				slog.String("to", row.State),
			)
			repaired++

			push(d.ipcSrv, ipc.FrameDeliveryStatus, "", rowPush(updated, ipc.DeliveryStatusPayload{
				EnvelopeID: row.EnvelopeID,
				State:      row.State,
				At:         row.StateChangedAt,
				Attempts:   row.Attempts,
				LastError:  clientDetail("delivery_"+row.State, row.LastError),
			}))
		}
	}

	if truncated {
		return repaired
	}
	if err := d.events.SetOutboxReconcileWatermark(highest); err != nil {
		slog.Warn("delivery reconcile: advancing watermark failed", slog.Any("err", err))
	}
	return repaired
}
