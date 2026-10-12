package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/files"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/syncrail"
)

func handleGapSiblingOffer(ctx context.Context, d *daemon, siblingPeerID string, body syncrail.GapSiblingOfferBody) {
	defer body.Wipe()
	logger := slog.With(
		slog.String("serving_peer_id", siblingPeerID),
		slog.String("msg_id", body.MsgID),
		slog.String("route", siblingRouteGapFill),
	)
	logger.Debug("sibling offer on gap-fill: received", slog.Any("sibling_offer", body.SiblingOffer))

	fo, reason := gapSiblingOfferRefusal(body)
	if reason != "" {
		logger.Warn("sibling offer on gap-fill: refused",
			slog.String("reason", reason),
			slog.String("outcome", "refused"),
		)
		return
	}
	notTaken := func(outcome string) {
		logger.Debug("sibling offer on gap-fill: not taken", slog.String("outcome", outcome))
	}
	if d.events == nil {
		logger.Warn("sibling offer on gap-fill: not taken — event log not wired",
			slog.String("reason", "no_event_log"),
		)
		return
	}

	ev, err := d.events.GetByMsgID(body.MsgID)
	switch {
	case errors.Is(err, events.ErrEventNotFound):
		notTaken("no_row")
		return
	case err != nil:
		logger.Warn("sibling offer on gap-fill: not taken — row read failed", slog.Any("err", err))
		return
	case ev.DeletedAt != 0:
		notTaken("row_deleted")
		return
	}
	if reason := gapOfferRowRefusal(d, body, fo, ev); reason != "" {
		logger.Warn("sibling offer on gap-fill: refused",
			slog.String("reason", reason),
			slog.String("outcome", "refused"),
		)
		return
	}

	direction := files.DirOut
	if ev.Direction == events.DirIn {
		direction = files.DirIn
	}
	takeSiblingOffer(ctx, d, logger, siblingTake{
		chatID:    ev.ChatID,
		direction: direction,
		gapFill:   true,
	}, siblingPeerID, body.MsgID, fo, body.SiblingOffer)
}

func gapOfferRowRefusal(d *daemon, body syncrail.GapSiblingOfferBody, fo *msg.FileOfferBody, ev events.Event) string {
	if ev.Kind != events.KindFile {
		return "gap_offer_row_not_a_file"
	}
	chatID, _, err := resolveChatByFingerprint(d, body.Fingerprint)
	if err != nil || chatID != ev.ChatID {
		return "gap_offer_fingerprint_disagrees"
	}
	var row FileEventBody
	if err := json.Unmarshal(ev.Body, &row); err != nil {
		return "gap_offer_row_body_unparseable"
	}
	if row.Size != fo.Size {
		return "gap_offer_size_disagrees_with_row"
	}
	return ""
}

func gapSiblingOfferRefusal(body syncrail.GapSiblingOfferBody) (*msg.FileOfferBody, string) {
	var fo msg.FileOfferBody
	if err := json.Unmarshal(body.FileOffer, &fo); err != nil {

		return nil, "sibling_offer_body_unparseable"
	}
	if reason := siblingOfferRefusal(body.MsgID, &fo, body.SiblingOffer); reason != "" {
		return nil, reason
	}
	return &fo, ""
}

func sendFileRequest(ctx context.Context, d *daemon, siblingPeerID string, msgIDs []string) error {
	body := syncrail.FileRequestBody{MsgIDs: msgIDs}
	if err := body.Check(); err != nil {
		return err
	}
	return sendGapEnvelope(ctx, d, siblingPeerID, syncrail.KindFileRequest, body)
}

func sendGapSiblingOffer(ctx context.Context, d *daemon, siblingPeerID string, body syncrail.GapSiblingOfferBody) error {
	defer body.Wipe()
	if err := body.Check(); err != nil {
		return fmt.Errorf("%w: %w", errGapOfferNotQueued, err)
	}
	if _, reason := gapSiblingOfferRefusal(body); reason != "" {
		return fmt.Errorf("%w: refused before send: %s", errGapOfferNotQueued, reason)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("%w: will not marshal", errGapOfferNotQueued)
	}
	defer func() {
		for i := range raw {
			raw[i] = 0
		}
	}()
	key, err := d.railKey()
	if err != nil {
		return fmt.Errorf("%w: %w", errGapOfferNotQueued, err)
	}
	if d.backendClient == nil {
		return fmt.Errorf("%w: no backend client", errGapOfferNotQueued)
	}

	sealed, err := syncrail.SealEnvelope(key, syncrail.Envelope{
		V:    syncrail.Version,
		Kind: syncrail.KindGapSiblingOffer,
		Ts:   time.Now().Unix(),
		Body: raw,
	})
	if err != nil {
		return fmt.Errorf("%w: seal: %w", errGapOfferNotQueued, err)
	}
	if _, err := d.backendClient.Send(ctx, backendapi.SendRequest{
		PeerID:  siblingPeerID,
		Payload: sealed,
	}); err != nil {
		return fmt.Errorf("gapfill: sibling offer on gap-fill send: %w", err)
	}
	return nil
}

var errGapOfferNotQueued = errors.New("gapfill: sibling offer on gap-fill not queued")

func gapOfferNotQueued(err error) bool {
	if errors.Is(err, errGapOfferNotQueued) {
		return true
	}
	var status *backendapi.StatusError
	return errors.As(err, &status)
}
