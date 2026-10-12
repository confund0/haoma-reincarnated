package main

import (
	"errors"
	"log/slog"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
)

const rowFirstEnvelopePushLine = "delivery_status: pushed under the row's first envelope"

func deliveryPush(d *daemon, ds backendapi.DeliveryStatus) (ipc.DeliveryStatusPayload, bool) {
	own := ipc.DeliveryStatusPayload{
		EnvelopeID: ds.EnvelopeID,
		State:      ds.State,
		At:         ds.At,
		Attempts:   ds.Attempts,
		LastError:  clientDetail("delivery_"+ds.State, ds.LastError),
	}
	if d.events == nil {
		return own, true
	}

	row, moved, err := d.events.UpdateDeliveryState(ds.EnvelopeID, ds.State)
	if err != nil {
		if errors.Is(err, events.ErrEventNotFound) {
			slog.Debug("delivery_status: the envelope has no event row",
				slog.String("envelope_id", ds.EnvelopeID),
			)
		} else {
			slog.Warn("persist delivery state failed",
				slog.String("envelope_id", ds.EnvelopeID),
				slog.String("state", ds.State),
				slog.Any("err", err),
			)
		}
		return own, true
	}
	if !moved {
		slog.Debug("delivery_status: the report did not move the row; no push",
			slog.String("envelope_id", ds.EnvelopeID),
			slog.String("row_envelope_id", row.EnvelopeID),
			slog.String("row_state", row.DeliveryState),
			slog.String("report_state", ds.State),
		)
		return ipc.DeliveryStatusPayload{}, false
	}
	return rowPush(row, own), true
}

func rowPush(row events.Event, own ipc.DeliveryStatusPayload) ipc.DeliveryStatusPayload {
	p := own
	p.State = row.DeliveryState
	if row.EnvelopeID != "" {
		p.EnvelopeID = row.EnvelopeID
	}
	if p.EnvelopeID != own.EnvelopeID {
		slog.Debug(rowFirstEnvelopePushLine,
			slog.String("envelope_id", own.EnvelopeID),
			slog.String("row_envelope_id", p.EnvelopeID),
			slog.String("state", p.State),
		)
	}
	return p
}
