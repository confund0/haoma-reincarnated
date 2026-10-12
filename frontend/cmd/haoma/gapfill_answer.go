package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/files"
	"haoma-frontend/internal/mirror"
	"haoma-frontend/internal/syncrail"
)

const gapBatchRows = 50

func handleGapRequest(ctx context.Context, d *daemon, siblingPeerID string, req syncrail.GapRequestBody) {
	logger := slog.With(
		slog.String("sibling_peer_id", siblingPeerID),
		slog.Uint64("their_watermark", req.Watermark),
		slog.Bool("full", req.Full),
	)
	logger.Debug("gapfill: answering sibling request")

	cursor := req.Watermark
	if req.Full {

		cursor = 0
	}

	if !req.Full && req.Watermark > 0 {
		if moved, err := d.watermarks.RecordAck(siblingPeerID, req.Watermark); err != nil {
			logger.Warn("gapfill: could not record the sibling's acknowledgement; the journal will not prune",
				slog.Any("err", err),
			)
		} else if moved {
			logger.Debug("gapfill: sibling acknowledged our journal", slog.Uint64("through_seq", req.Watermark))
		}
	}

	rows, rangeEnd, more, err := buildGapBatch(d, logger, cursor)
	if err != nil {
		logger.Warn("gapfill: could not build a batch; sibling stays behind",
			slog.Any("err", err),
		)
		return
	}
	if rangeEnd == 0 {

		head, hErr := d.events.JournalHead()
		if hErr != nil {
			logger.Warn("gapfill: journal head read failed", slog.Any("err", hErr))
			return
		}
		rangeEnd = head
		more = false
	}

	if err := sendGapEnvelope(ctx, d, siblingPeerID, syncrail.KindGapResponse, syncrail.GapResponseBody{
		Rows:     rows,
		RangeEnd: rangeEnd,
		More:     more,
		Full:     req.Full,
	}); err != nil {
		logger.Warn("gapfill: response send failed; sibling stays behind",
			slog.Int("rows", len(rows)),
			slog.Any("err", err),
		)
		return
	}
	logger.Debug("gapfill: batch answered",
		slog.Int("rows", len(rows)),
		slog.Uint64("range_end", rangeEnd),
		slog.Bool("more", more),
	)

	if req.Reciprocate {
		askSibling(ctx, d, siblingPeerID, false, false)
	}
}

func buildGapBatch(d *daemon, logger *slog.Logger, cursor uint64) (rows []syncrail.RowSnapshot, rangeEnd uint64, more bool, err error) {
	entries, err := d.events.JournalSince(cursor, gapBatchRows)
	if err != nil {
		return nil, 0, false, err
	}
	if len(entries) == 0 {
		return nil, 0, false, nil
	}

	fpCache := map[chat.ChatID]string{}
	emitted := map[string]bool{}

	for _, entry := range entries {
		fingerprint, ok := fpCache[entry.ChatID]
		if !ok {
			fingerprint = fingerprintForChat(d, entry.ChatID)
			fpCache[entry.ChatID] = fingerprint
		}
		if fingerprint == "" {

			logger.Debug("gapfill: skipping a journalled row with no routing fingerprint",
				slog.Uint64("seq", entry.Seq),
			)
			continue
		}

		ev, evErr := d.events.GetByMsgID(entry.MsgID)
		if errors.Is(evErr, events.ErrEventNotFound) {

			rows = append(rows, syncrail.RowSnapshot{
				Fingerprint: fingerprint,
				MsgID:       entry.MsgID,
				Swept:       true,
			})
			emitted[entry.MsgID] = true
			continue
		}
		if evErr != nil {
			logger.Warn("gapfill: row read failed; skipping",
				slog.Uint64("seq", entry.Seq),
				slog.Any("err", evErr),
			)
			continue
		}

		if ev.Kind == events.KindReaction {
			if target, ok := reactionTarget(d, ev); ok && !emitted[target.MsgID] {
				if snap, sOk := snapshotOf(target, fingerprint); sOk {
					rows = append(rows, snap)
					emitted[target.MsgID] = true
				}
			}
		}

		if emitted[ev.MsgID] {

			continue
		}
		snap, ok := snapshotOf(ev, fingerprint)
		if !ok {
			continue
		}
		rows = append(rows, snap)
		emitted[ev.MsgID] = true
	}

	rangeEnd = entries[len(entries)-1].Seq

	more = len(entries) == gapBatchRows
	return rows, rangeEnd, more, nil
}

func snapshotOf(ev events.Event, fingerprint string) (syncrail.RowSnapshot, bool) {
	switch ev.Kind {
	case events.KindText, events.KindFile, events.KindReaction:
	case events.KindCallSummary:

		if ev.Direction != events.DirOut {
			return syncrail.RowSnapshot{}, false
		}
	default:

		return syncrail.RowSnapshot{}, false
	}
	if ev.MsgID == "" {
		return syncrail.RowSnapshot{}, false
	}
	body := ev.Body
	if ev.Kind == events.KindFile {

		honest, err := gapFileBody(ev.Body)
		if err != nil {
			slog.Warn("gapfill: file row body does not parse; row not shipped",
				slog.String("msg_id", ev.MsgID),
				slog.Any("err", err),
			)
			return syncrail.RowSnapshot{}, false
		}
		body = honest
	}
	snap := syncrail.RowSnapshot{
		Fingerprint:   fingerprint,
		MsgID:         ev.MsgID,
		Kind:          string(ev.Kind),
		Inbound:       ev.Direction == events.DirIn,
		DisplayTs:     ev.DisplayTs,
		SenderTs:      ev.SenderTs,
		ExpireSeconds: ev.ExpireSeconds,
		Body:          body,
		EditedAt:      ev.EditedAt,
		DeletedAt:     ev.DeletedAt,
	}

	if ev.Kind != events.KindText && ev.Kind != events.KindFile {
		return snap, true
	}
	switch ev.Direction {
	case events.DirIn:

		snap.ReadAt = ev.ReadAt
		if ev.ReceiptClaimOrigin && ev.ReadReceiptSentAt != 0 &&
			ev.ReadReceiptSentAt != events.ReadReceiptSuppressedSentinel {
			snap.ReceiptSentAt = ev.ReadReceiptSentAt
			snap.ReceiptGeneration = ev.ReceiptClaimGeneration
		}
	case events.DirOut:

		snap.ReadAt = ev.ReadAt
		if ev.DeliveryState != "" && events.IsDeliveryRank(ev.DeliveryState) {
			snap.DeliveryRank = ev.DeliveryState
		}
		if ev.DeliveryState == events.DeliveryFailed {
			snap.DeliveryFailed = true
		}
	}
	return snap, true
}

func gapFileBody(raw json.RawMessage) (json.RawMessage, error) {
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return raw, nil
	}
	var in FileEventBody
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("decode file body: %w", err)
	}
	return json.Marshal(FileEventBody{
		Name:             in.Name,
		Size:             in.Size,
		Mime:             in.Mime,
		Sha256Ciphertext: in.Sha256Ciphertext,
		State:            string(files.StatePending),
		Caption:          in.Caption,
	})
}

func reactionTarget(d *daemon, reaction events.Event) (events.Event, bool) {
	var body events.ReactionBody
	if err := json.Unmarshal(reaction.Body, &body); err != nil {
		return events.Event{}, false
	}
	if body.TargetMsgID == "" {
		return events.Event{}, false
	}
	target, err := d.events.GetByMsgID(body.TargetMsgID)
	if err != nil {
		return events.Event{}, false
	}
	return target, true
}

func fingerprintForChat(d *daemon, chatID chat.ChatID) string {
	if d == nil || d.chats == nil || d.peerMeta == nil {
		return ""
	}
	c, err := d.chats.Get(chatID)
	if err != nil {
		return ""
	}
	dc, ok := c.(*chat.DirectChat)
	if !ok {
		return ""
	}

	if dc.Fingerprint != "" {
		return dc.Fingerprint
	}
	for _, pid := range dc.FanoutPeerIDs() {
		rec, err := d.peerMeta.Get(pid)
		if err == nil && rec.Fingerprint != "" {
			return rec.Fingerprint
		}
	}
	return ""
}

func sendGapEnvelope(ctx context.Context, d *daemon, siblingPeerID string, kind syncrail.Kind, body any) error {
	_, err := sendGapEnvelopeID(ctx, d, siblingPeerID, kind, body)
	return err
}

func sendGapEnvelopeID(ctx context.Context, d *daemon, siblingPeerID string, kind syncrail.Kind, body any) (string, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	key, err := d.railKey()
	if err != nil {
		return "", err
	}
	if d.backendClient == nil {
		return "", errors.New("gapfill: no backend client")
	}
	return mirror.SealAndSend(ctx, d.backendClient, key, siblingPeerID, syncrail.Envelope{
		V:    syncrail.Version,
		Kind: kind,
		Ts:   time.Now().Unix(),
		Body: raw,
	})
}
