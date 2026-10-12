package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/crypto/chacha20poly1305"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/files"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/syncrail"
)

func applySiblingOffer(
	ctx context.Context,
	d *daemon,
	logger *slog.Logger,
	chatID chat.ChatID,
	servingPeerID string,
	wrapper *msg.Wrapper,
	offer syncrail.SiblingOffer,
	originDisplayTs int64,
) mirrorOutcome {
	logger = logger.With(slog.String("serving_peer_id", servingPeerID))
	logger.Debug("sibling offer: received", slog.Any("sibling_offer", offer))

	if wrapper.Kind != msg.KindFileOffer {
		logger.Warn("sibling offer: drop — the wrapper is not a file offer",
			slog.String("reason", "sibling_offer_not_a_file"),
		)
		return mirrorNoEffect
	}
	fo, err := wrapper.FileOffer()
	if err != nil {
		logger.Warn("sibling offer: drop — the file offer body does not parse",
			slog.String("reason", "sibling_offer_body_unparseable"),
			slog.Any("err", err),
		)
		return mirrorNoEffect
	}

	if reason := siblingOfferRefusal(wrapper.MsgID, fo, offer); reason != "" {
		logger.Warn("sibling offer: refused; the row lands with no transfer",
			slog.String("reason", reason),
		)
		return appendMirroredFileRow(ctx, d, logger, chatID, wrapper, originDisplayTs, false)
	}
	if siblingOfferIgnored(d, logger) {
		return appendMirroredFileRow(ctx, d, logger, chatID, wrapper, originDisplayTs, false)
	}

	outcome := appendMirroredFileRow(ctx, d, logger, chatID, wrapper, originDisplayTs, false)
	if outcome == mirrorNoEffect {
		logger.Debug("sibling offer: no row here, so no transfer starts",
			slog.String("outcome", "no_row"),
		)
		return outcome
	}
	take := siblingTake{chatID: chatID, direction: files.DirOut}
	if !takeSiblingOffer(ctx, d, logger.With(slog.String("route", take.route())), take, servingPeerID, wrapper.MsgID, fo, offer) {
		return mirrorNoEffect
	}
	return outcome
}

func siblingOfferRefusal(msgID string, fo *msg.FileOfferBody, offer syncrail.SiblingOffer) string {
	if err := offer.Check(); err != nil {
		return "sibling_offer_incomplete"
	}
	switch {
	case offer.StagingID == msgID:
		return "sibling_offer_staging_is_msg_id"
	case fo.Token != offer.Token:
		return "sibling_offer_token_disagrees"
	case fo.Sha256Ciphertext != offer.Sha256Ciphertext:
		return "sibling_offer_hash_disagrees"
	case fo.UrlPath == "":
		return "sibling_offer_no_url_path"
	case fo.Size == 0:
		return "sibling_offer_no_size"
	case offer.BlobSize > files.MaxPullBytes:
		return "sibling_offer_too_large"
	case fo.Size != offer.PlaintextSize+chacha20poly1305.Overhead:

		return "sibling_offer_size_disagrees"
	}
	return ""
}

const (
	siblingDetailFetchFailed = "fetch from my other device failed"
	siblingDetailSealFailed  = "could not seal the file"
	siblingDetailOpenFailed  = "decrypt failed"
)

const (
	siblingRouteSend = "send"

	siblingRouteGapFill = "gapfill"
)

func siblingRoute(meta files.Metadata) string {
	if meta.GapFill {
		return siblingRouteGapFill
	}
	return siblingRouteSend
}

type siblingTake struct {
	chatID    chat.ChatID
	direction files.Direction
	gapFill   bool
}

func (t siblingTake) route() string {
	if t.gapFill {
		return siblingRouteGapFill
	}
	return siblingRouteSend
}

func takeSiblingOffer(
	ctx context.Context,
	d *daemon,
	logger *slog.Logger,
	take siblingTake,
	servingPeerID, msgID string,
	fo *msg.FileOfferBody,
	offer syncrail.SiblingOffer,
) bool {
	if d.files == nil {
		logger.Warn("sibling offer: not taken — files manager not wired",
			slog.String("reason", "no_files_manager"),
		)
		return false
	}
	now := time.Now().Unix()
	meta := files.Metadata{
		MsgID:            msgID,
		ChatID:           take.chatID,
		Direction:        take.direction,
		OriginalName:     fo.Name,
		Mime:             fo.Mime,
		Size:             fo.Size,
		Sha256Ciphertext: fo.Sha256Ciphertext,
		State:            files.StatePending,

		FetchedFromSibling: true,
		GapFill:            take.gapFill,
		StagingID:          offer.StagingID,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	held := files.SiblingOffer{
		MsgID:         msgID,
		ServingPeerID: servingPeerID,
		StagingID:     offer.StagingID,
		Token:         offer.Token,
		UrlPath:       fo.UrlPath,
		Key:           append([]byte(nil), offer.Key...),
		Nonce:         append([]byte(nil), offer.Nonce...),
		BlobSize:      offer.BlobSize,
		PlaintextSize: offer.PlaintextSize,

		Sha256Ciphertext: offer.Sha256Ciphertext,
	}

	var err error
	if take.gapFill {
		err = d.files.TakeGapSiblingOffer(meta, held)
	} else {
		err = d.files.PutMetaAndSiblingOffer(meta, held)
	}
	if errors.Is(err, files.ErrMetaHeld) {
		logger.Debug("sibling offer: discarded — another offer for this msg id was already taken",
			slog.String("outcome", "already_held"),
		)
		return true
	}
	if err != nil {
		logger.Warn("sibling offer: not taken — metadata and key write failed",
			slog.String("reason", "take_write_failed"),
			slog.Any("err", err),
		)
		return false
	}
	logger.Debug("sibling offer: taken; metadata and key held",
		slog.String("state", string(files.StatePending)),
	)

	if take.gapFill && gapFillFetchHeldAtTake(d, logger, offer.StagingID) {

		return true
	}

	resp, err := enqueueSiblingFetch(ctx, d, held)
	if err != nil {
		logger.Warn("sibling offer: fetch not enqueued; the backend-ready sweep asks again",
			slog.Any("err", err),
		)
		stampFileEventState(d, take.chatID, msgID, files.StateFailedTransient, 0, siblingDetailFetchFailed)
		return true
	}
	logger.Debug("sibling offer: fetch enqueued on haomad",
		slog.String("backend_state", resp.State),
	)
	applySiblingFetchState(ctx, d, logger, msgID, files.State(resp.State), uint64(resp.BytesReceived), "")
	return true
}

func applySiblingFetchState(ctx context.Context, d *daemon, logger *slog.Logger, msgID string, state files.State, bytes uint64, reason string) {
	switch state {
	case files.StatePending, files.StateDownloading:
		stampFileEventState(d, "", msgID, state, bytes, "")
	case files.StateFailedTransient:
		stampFileEventState(d, "", msgID, state, bytes, siblingDetailFetchFailed)
	case files.StateFailedPermanent:
		siblingFetchFailedPermanent(d, logger, msgID, bytes, reason)
	case files.StateReady:
		if err := finalizeSiblingFetch(ctx, d, msgID); err != nil {
			logger.Warn("sibling fetch: pull or seal failed; the backend-ready sweep tries again", slog.Any("err", err))
			stampFileEventState(d, "", msgID, files.StateFailedTransient, bytes, siblingDetailSealFailed)
		}
	default:
		logger.Debug("sibling fetch: ignoring unknown fetch state")
	}
}

func enqueueSiblingFetch(ctx context.Context, d *daemon, held files.SiblingOffer) (backendapi.FetchFileResponse, error) {
	if d.backendClient == nil {
		return backendapi.FetchFileResponse{}, errors.New("no backend client")
	}
	slog.Debug("sibling fetch: asking haomad for the blob",
		slog.String("msg_id", held.MsgID),
		slog.String("staging_id", held.StagingID),
		slog.String("serving_peer_id", held.ServingPeerID),
	)
	enqueueCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return d.backendClient.FetchFile(enqueueCtx, backendapi.FetchFileRequest{
		MsgID:          held.StagingID,
		PeerID:         held.ServingPeerID,
		Token:          held.Token,
		UrlPath:        held.UrlPath,
		ExpectedSize:   int64(held.BlobSize),
		ExpectedSha256: held.Sha256Ciphertext,
	})
}

func siblingFetchMsgID(d *daemon, haomadID string) (string, bool, error) {
	if d.files == nil {
		return "", false, nil
	}
	msgID, err := d.files.SiblingOfferMsgID(haomadID)
	switch {
	case err == nil:
		return msgID, true, nil
	case errors.Is(err, files.ErrSiblingOfferNotFound):
		return "", false, nil
	default:
		return "", false, err
	}
}

func handleSiblingFetchEvent(ctx context.Context, d *daemon, msgID string, ev backendapi.FileFetchEvent) {
	logger := slog.With(
		slog.String("msg_id", msgID),
		slog.String("staging_id", ev.MsgID),
		slog.String("state", ev.State),
	)
	if _, err := d.files.GetSiblingOffer(msgID); err != nil {
		if !errors.Is(err, files.ErrSiblingOfferNotFound) {
			logger.Warn("sibling fetch: held offer read failed; event left for the sweep", slog.Any("err", err))
			return
		}
		if files.State(ev.State) != files.StateReady {
			logger.Debug("sibling fetch: event for a settled file; no row change",
				slog.String("outcome", "settled_ignored"),
			)
			return
		}
		logger.Debug("sibling fetch: ready for a settled file; asking haomad to drop its copy",
			slog.String("outcome", "settled_dropped"),
		)
		if d.backendClient != nil {
			dropCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := d.backendClient.DropStagingBlob(dropCtx, ev.MsgID, ev.Token); err != nil {
				logger.Debug("sibling fetch: haomad copy drop failed", slog.Any("err", err))
			}
		}
		return
	}
	logger.Debug("sibling fetch: haomad event")
	applySiblingFetchState(ctx, d, logger, msgID, files.State(ev.State), uint64(ev.BytesReceived), ev.Reason)
}

func finalizeSiblingFetch(ctx context.Context, d *daemon, msgID string) error {
	logger := slog.With(slog.String("msg_id", msgID))
	if _, busy := d.siblingSealBusy.LoadOrStore(msgID, struct{}{}); busy {
		logger.Debug("sibling fetch: another call is sealing this file; this one does nothing",
			slog.String("outcome", "already_running"),
		)
		return nil
	}
	defer d.siblingSealBusy.Delete(msgID)

	meta, err := d.files.GetMeta(msgID)
	if errors.Is(err, files.ErrMetaNotFound) {
		logger.Debug("sibling fetch: the row is gone; dropping any held key",
			slog.String("outcome", "row_gone"),
		)
		return d.files.DeleteSiblingOffer(msgID)
	}
	if err != nil {
		return fmt.Errorf("read metadata: %w", err)
	}
	logger = logger.With(slog.String("chat_id", string(meta.ChatID)))
	switch meta.State {
	case files.StateReady:
		return settleSealedSiblingRow(d, logger, meta)
	case files.StateFailedPermanent, files.StateExpired:
		logger.Debug("sibling fetch: the file is settled; dropping any held key",
			slog.String("state", string(meta.State)),
			slog.String("outcome", "settled"),
		)
		return d.files.DeleteSiblingOffer(msgID)
	}
	held, err := d.files.GetSiblingOffer(msgID)
	if errors.Is(err, files.ErrSiblingOfferNotFound) {
		logger.Debug("sibling fetch: no held offer; nothing to seal",
			slog.String("outcome", "no_offer"),
		)
		return nil
	}
	if err != nil {
		return fmt.Errorf("read held sibling offer: %w", err)
	}
	logger = logger.With(slog.String("serving_peer_id", held.ServingPeerID))

	if stagingExists(d, msgID) {
		logger.Debug("sibling fetch: blob already pulled from haomad")
	} else {
		logger.Debug("sibling fetch: pulling the blob from haomad")
		if err := pullStagedCiphertextAs(ctx, d, held.StagingID, msgID, held.Token, held.BlobSize); err != nil {
			return err
		}
		logger.Debug("sibling fetch: blob pulled")
	}

	sealedPath, err := d.files.DecryptSealMoveTrimmed(meta.ChatID, msgID, held.Key, held.Nonce, int(held.PlaintextSize))
	if errors.Is(err, files.ErrAEADOpen) || errors.Is(err, files.ErrPlaintextLength) {
		logger.Warn("sibling fetch: the held key does not open the blob; failed permanently",
			slog.String("outcome", "decrypt_failed"),
		)
		stampFileEventState(d, meta.ChatID, msgID, files.StateFailedPermanent, meta.Size, siblingDetailOpenFailed)

		recordGapFailReason(d, logger, meta, files.FailIntegrity)
		if dErr := d.files.DeleteSiblingOffer(msgID); dErr != nil {
			logger.Warn("sibling fetch: dropping the held key failed", slog.Any("err", dErr))
		}
		if meta.GapFill {
			gapFileSettled(d, logger)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("seal: %w", err)
	}
	if err := siblingFinalizeCheckpoint("sealed"); err != nil {
		return err
	}

	meta.SealedPath = sealedPath
	meta.State = files.StateReady
	meta.UpdatedAt = time.Now().Unix()
	if err := d.files.MarkSiblingFileSealed(meta); errors.Is(err, files.ErrSiblingTakeReplaced) {

		logger.Debug("sibling fetch: a newer offer was taken during the seal; this one writes nothing",
			slog.String("outcome", "take_replaced"),
		)
		return nil
	} else if errors.Is(err, files.ErrSiblingFileGone) {
		logger.Debug("sibling fetch: the file was deleted during the seal; removing the sealed copy",
			slog.String("outcome", "deleted_meanwhile"),
		)
		if dErr := d.files.DeleteSealed(meta.ChatID, msgID); dErr != nil && !errors.Is(dErr, files.ErrSealedNotFound) {
			logger.Warn("sibling fetch: removing the sealed copy of a deleted file failed", slog.Any("err", dErr))
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("persist sealed metadata: %w", err)
	}
	if err := siblingFinalizeCheckpoint("meta_ready"); err != nil {
		return err
	}
	if err := settleSealedSiblingRow(d, logger, meta); err != nil {
		return err
	}

	logger.Info("sibling offer: file sealed",
		slog.String("outcome", "sealed"),
		slog.String("route", siblingRoute(meta)),
	)
	if meta.GapFill {
		gapFileSettled(d, logger)
	}
	return nil
}

func settleSealedSiblingRow(d *daemon, logger *slog.Logger, meta files.Metadata) error {
	if ev, err := d.events.GetByMsgID(meta.MsgID); err == nil {
		var body FileEventBody
		if json.Unmarshal(ev.Body, &body) != nil || body.State != string(files.StateReady) {
			stampFileEventState(d, meta.ChatID, meta.MsgID, files.StateReady, meta.Size, "")
			logger.Debug("sibling fetch: row moved to ready", slog.String("outcome", "row_ready"))
		}
	}
	if err := siblingFinalizeCheckpoint("row_ready"); err != nil {
		return err
	}
	if err := d.files.DeleteSiblingOffer(meta.MsgID); err != nil {
		return fmt.Errorf("drop the held key: %w", err)
	}
	return nil
}

var siblingFinalizeCheckpoint = func(step string) error { return nil }

func sweepSiblingFetches(ctx context.Context, d *daemon) {
	if d.files == nil || d.backendClient == nil {
		return
	}
	held, err := d.files.ListSiblingOffers()
	if err != nil {
		slog.Warn("sibling fetch sweep: list failed", slog.Any("err", err))
		return
	}
	slog.Debug("sibling fetch sweep: start", slog.Int("held", len(held)))
	for _, o := range held {
		if ctx.Err() != nil {
			return
		}
		logger := slog.With(slog.String("msg_id", o.MsgID))
		meta, err := d.files.GetMeta(o.MsgID)
		switch {
		case errors.Is(err, files.ErrMetaNotFound):
			logger.Debug("sibling fetch sweep: the row is gone; dropping the held key",
				slog.String("outcome", "row_gone"),
			)
			_ = d.files.DeleteSiblingOffer(o.MsgID)
			continue
		case err != nil:
			logger.Warn("sibling fetch sweep: metadata read failed; left for the next sweep", slog.Any("err", err))
			continue
		}
		switch meta.State {
		case files.StateReady:

			if err := settleSealedSiblingRow(d, logger, meta); err != nil {
				logger.Warn("sibling fetch sweep: settling a sealed file failed; left for the next sweep", slog.Any("err", err))
			}
			continue
		case files.StateFailedPermanent, files.StateExpired:
			logger.Debug("sibling fetch sweep: the row is settled; dropping the held key",
				slog.String("state", string(meta.State)),
				slog.String("outcome", "settled"),
			)
			_ = d.files.DeleteSiblingOffer(o.MsgID)
			continue
		}

		if meta.GapFill && gapFillFetchHeldAtSweep(d, logger) {

			continue
		}

		if stagingExists(d, o.MsgID) {
			if err := finalizeSiblingFetch(ctx, d, o.MsgID); err != nil {
				logger.Warn("sibling fetch sweep: seal failed; left for the next sweep", slog.Any("err", err))
				continue
			}
			logger.Debug("sibling fetch sweep: sealed a blob already pulled", slog.String("outcome", "sealed_local"))
			continue
		}

		resp, eErr := enqueueSiblingFetch(ctx, d, o)
		if eErr != nil {
			logger.Warn("sibling fetch sweep: haomad not reached; left for the next sweep", slog.Any("err", eErr))
			continue
		}
		switch files.State(resp.State) {
		case files.StateReady:
			if err := finalizeSiblingFetch(ctx, d, o.MsgID); err != nil {
				logger.Warn("sibling fetch sweep: pull or seal failed; left for the next sweep", slog.Any("err", err))
				continue
			}
			logger.Debug("sibling fetch sweep: pulled and sealed", slog.String("outcome", "sealed"))
		case files.StateFailedPermanent:
			logger.Warn("sibling fetch sweep: haomad failed the fetch permanently",
				slog.String("outcome", "failed_permanent"),
			)
			stampFileEventState(d, meta.ChatID, o.MsgID, files.StateFailedPermanent, meta.Size, siblingDetailFetchFailed)
			_ = d.files.DeleteSiblingOffer(o.MsgID)
		default:
			logger.Debug("sibling fetch sweep: haomad holds the fetch; nothing to do yet",
				slog.String("backend_state", resp.State),
				slog.String("outcome", "left_to_haomad"),
			)
		}
	}
}
