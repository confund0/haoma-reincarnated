package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/chacha20poly1305"

	"haoma-frontend/internal/events"
	"haoma-frontend/internal/files"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/syncrail"
)

const maxQueuedFileServes = 2 * syncrail.MaxFileRequestIDs

const defaultGapOfferLifetime = time.Hour

const gapOfferLifetimeEnv = "HAOMA_GAPFILL_OFFER_TTL"

func gapOfferLifetimeFromEnv() time.Duration {
	raw := os.Getenv(gapOfferLifetimeEnv)
	if raw == "" {
		return defaultGapOfferLifetime
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 || d > defaultGapOfferLifetime {
		attrs := []any{
			slog.String("env", gapOfferLifetimeEnv),
			slog.String("value", raw),
			slog.Duration("longest", defaultGapOfferLifetime),
		}
		if err != nil {
			attrs = append(attrs, slog.Any("err", err))
		}
		slog.Warn("gap-fill offer lifetime override ignored (not a positive duration of at most the default)", attrs...)
		return defaultGapOfferLifetime
	}
	slog.Info("gap-fill offer lifetime overridden",
		slog.Duration("lifetime", d),
		slog.Duration("default", defaultGapOfferLifetime),
	)
	return d
}

func (d *daemon) gapOfferLifetime() time.Duration {
	if d.gapOfferTTL > 0 {
		return d.gapOfferTTL
	}
	return defaultGapOfferLifetime
}

type fileServeItem struct {
	requester string
	msgID     string
}

type fileServer struct {
	mu sync.Mutex

	queue []fileServeItem

	pending map[fileServeItem]bool

	expireAsked bool

	wake chan struct{}

	serve  func(ctx context.Context, item fileServeItem) time.Time
	expire func(ctx context.Context, now time.Time) time.Time
}

func newFileServer(serve func(context.Context, fileServeItem) time.Time, expire func(context.Context, time.Time) time.Time) *fileServer {
	return &fileServer{
		pending: make(map[fileServeItem]bool),
		wake:    make(chan struct{}, 1),
		serve:   serve,
		expire:  expire,
	}
}

func newDaemonFileServer(d *daemon) *fileServer {
	return newFileServer(
		func(ctx context.Context, item fileServeItem) time.Time {
			return serveFileRequestItem(ctx, d, item.requester, item.msgID)
		},
		func(ctx context.Context, now time.Time) time.Time {
			return expireGapOffers(ctx, d, now)
		},
	)
}

type fileServeEnqueued struct {
	queued  int
	pending int
	dropped int
}

func (s *fileServer) enqueue(requester string, msgIDs []string) fileServeEnqueued {
	var out fileServeEnqueued
	s.mu.Lock()
	for _, id := range msgIDs {
		item := fileServeItem{requester: requester, msgID: id}
		switch {
		case s.pending[item]:
			out.pending++
		case len(s.queue) >= maxQueuedFileServes:
			out.dropped++
		default:
			s.pending[item] = true
			s.queue = append(s.queue, item)
			out.queued++
		}
	}
	s.mu.Unlock()
	if out.queued > 0 {
		s.signal()
	}
	return out
}

func (s *fileServer) askExpiry() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.expireAsked = true
	s.mu.Unlock()
	s.signal()
}

func (s *fileServer) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *fileServer) takeExpiry() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	asked := s.expireAsked
	s.expireAsked = false
	return asked
}

func (s *fileServer) next() (fileServeItem, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) == 0 {
		return fileServeItem{}, false
	}
	item := s.queue[0]
	s.queue[0] = fileServeItem{}
	s.queue = s.queue[1:]
	return item, true
}

func (s *fileServer) done(item fileServeItem) {
	s.mu.Lock()
	delete(s.pending, item)
	s.mu.Unlock()
}

func (s *fileServer) run(ctx context.Context) {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	var armed time.Time
	arm := func(at time.Time) {
		if at.IsZero() || (!armed.IsZero() && !at.Before(armed)) {
			return
		}
		armed = at
		timer.Reset(time.Until(at))
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-timer.C:
			armed = time.Time{}
			s.mu.Lock()
			s.expireAsked = true
			s.mu.Unlock()
		}
		for {
			if ctx.Err() != nil {
				return
			}
			if s.takeExpiry() {

				timer.Stop()
				armed = time.Time{}
				arm(s.expire(ctx, time.Now()))
				continue
			}
			item, ok := s.next()
			if !ok {
				break
			}
			arm(s.serve(ctx, item))
			s.done(item)
		}
	}
}

func handleFileRequest(d *daemon, siblingPeerID string, body syncrail.FileRequestBody) {
	logger := slog.With(
		slog.String("sibling_peer_id", siblingPeerID),
		slog.Int("msg_ids", len(body.MsgIDs)),
	)
	if fileRequestIgnored(d, logger) {
		return
	}
	if d.fileServer == nil {

		logger.Warn("file request: no serving worker; nothing is served",
			slog.String("outcome", "no_worker"),
		)
		return
	}
	got := d.fileServer.enqueue(siblingPeerID, body.MsgIDs)
	logger.Debug("file request: received",
		slog.Int("queued", got.queued),
		slog.Int("already_pending", got.pending),
		slog.Int("dropped_queue_full", got.dropped),
	)
}

func serveFileRequestItem(ctx context.Context, d *daemon, requester, msgID string) time.Time {
	logger := slog.With(
		slog.String("sibling_peer_id", requester),
		slog.String("msg_id", msgID),
	)
	notServed := func(outcome string, attrs ...any) {
		logger.Debug("file request: not served", append([]any{slog.String("outcome", outcome)}, attrs...)...)
	}

	fault := func(line, outcome string, attrs ...any) {
		if ctx.Err() != nil {
			notServed("shutdown", append([]any{slog.String("cut_short", outcome)}, attrs...)...)
			return
		}
		logger.Warn(line, append([]any{slog.String("outcome", outcome)}, attrs...)...)
	}

	ev, err := d.events.GetByMsgID(msgID)
	switch {
	case errors.Is(err, events.ErrEventNotFound):
		notServed("no_row")
		return time.Time{}
	case err != nil:
		fault("file request: row read failed", "store_fault", slog.Any("err", err))
		return time.Time{}
	case ev.Kind != events.KindFile:
		notServed("not_a_file", slog.String("kind", string(ev.Kind)))
		return time.Time{}
	case ev.DeletedAt != 0:
		notServed("row_deleted")
		return time.Time{}
	}

	meta, err := d.files.GetMeta(msgID)
	switch {
	case errors.Is(err, files.ErrMetaNotFound):
		notServed("no_metadata")
		return time.Time{}
	case err != nil:
		fault("file request: metadata read failed", "store_fault", slog.Any("err", err))
		return time.Time{}
	case meta.ChatID != ev.ChatID:
		notServed("chat_disagrees")
		return time.Time{}
	case meta.State != files.StateReady:
		notServed("not_ready", slog.String("state", string(meta.State)))
		return time.Time{}
	}

	var row FileEventBody
	if err := json.Unmarshal(ev.Body, &row); err != nil {
		notServed("row_body_unparseable")
		return time.Time{}
	}
	fingerprint := fingerprintForChat(d, meta.ChatID)
	if fingerprint == "" {
		notServed("no_fingerprint")
		return time.Time{}
	}

	switch mine, err := d.readIsSibling(requester); {
	case err != nil:
		fault("file request: sibling check failed", "store_fault", slog.Any("err", err))
		return time.Time{}
	case !mine:
		notServed("not_a_sibling")
		return time.Time{}
	}

	kept, err := d.files.GetGapOffer(msgID, requester)
	switch {
	case errors.Is(err, files.ErrGapOfferNotFound):
	case err != nil:
		fault("file request: kept offer read failed", "store_fault", slog.Any("err", err))
		return time.Time{}
	case !gapOfferEnded(d, kept.StagedAt, time.Now()):
		resendKeptOffer(ctx, d, logger, fault, requester, msgID, fingerprint, row, kept)
		return time.Time{}
	default:

		ended := gapOfferTimeOf(kept)
		kept.Wipe()
		if !dropEndedGapOffer(ctx, d, logger, ended) {

			notServed("kept_offer_not_dropped")
			return time.Now().Add(gapOfferDropRetry)
		}
	}

	plaintext, err := d.files.UnsealAtRest(meta.ChatID, msgID)
	switch {
	case errors.Is(err, files.ErrSealedNotFound):
		notServed("no_sealed_copy")
		return time.Time{}
	case err != nil:
		fault("file request: the sealed copy does not open", "sealed_copy_unopened", slog.Any("err", err))
		return time.Time{}
	}
	defer zeroBytes(plaintext)
	if len(plaintext) == 0 {

		notServed("empty_file")
		return time.Time{}
	}

	if row.Size != uint64(len(plaintext))+chacha20poly1305.Overhead {
		notServed("size_disagrees", slog.Uint64("row_size", row.Size), slog.Int("sealed_size", len(plaintext)))
		return time.Time{}
	}
	if ctx.Err() != nil {
		notServed("shutdown")
		return time.Time{}
	}

	st, err := stageForSibling(ctx, d, requester, plaintext)
	zeroBytes(plaintext)
	if err != nil {
		fault("file request: staging failed", "staging_failed", slog.Any("err", err))
		return time.Time{}
	}
	defer wipeSiblingStaging(st)

	metaCheckpoint("file_request_staged", msgID)
	stagedAt := time.Now()
	keep := files.GapOffer{
		MsgID:            msgID,
		RequesterPeerID:  requester,
		StagingID:        st.stagingID,
		Token:            st.token,
		UrlPath:          "/files/" + st.token,
		Key:              append([]byte(nil), st.key...),
		Nonce:            append([]byte(nil), st.nonce...),
		BlobSize:         st.size,
		PlaintextSize:    st.plainLen,
		Sha256Ciphertext: st.sha256Hex,
		StagedAt:         stagedAt.Unix(),
	}
	err = d.files.RecordGapOffer(keep)
	keep.Wipe()
	if err != nil {
		cleanupCtx, cancel := fileServeCleanupContext(ctx)
		dropSiblingBlob(cleanupCtx, d, st.stagingID)
		cancel()
		if errors.Is(err, files.ErrMetaNotFound) {
			notServed("row_gone", slog.String("staging_id", st.stagingID))
			return time.Time{}
		}
		fault("file request: staging id not recorded", "store_fault",
			slog.String("staging_id", st.stagingID), slog.Any("err", err))
		return time.Time{}
	}
	ends := gapOfferEnd(d, stagedAt.Unix())

	if err := offerStagedFile(ctx, d, requester, msgID, fingerprint, row, st); err != nil {
		if !gapOfferNotQueued(err) {

			fault("file request: sibling offer send unconfirmed; its blob stays with the row", "send_unconfirmed",
				slog.String("staging_id", st.stagingID), slog.Any("err", err))
			return ends
		}
		unrecordStagedFile(ctx, d, logger, msgID, requester, st.stagingID)
		fault("file request: sibling offer not sent", "send_failed",
			slog.String("staging_id", st.stagingID), slog.Any("err", err))
		if ctx.Err() != nil {

			return ends
		}
		return time.Time{}
	}
	logger.Debug("file request: served",
		slog.String("outcome", "served"),
		slog.String("staging_id", st.stagingID),
		slog.Uint64("size", st.size),
	)
	return ends
}

func gapOfferEnd(d *daemon, stagedAt int64) time.Time {
	return time.Unix(stagedAt, 0).Add(d.gapOfferLifetime())
}

const gapOfferClockSlack = time.Minute

func gapOfferEnded(d *daemon, stagedAt int64, now time.Time) bool {
	staged := time.Unix(stagedAt, 0)
	if staged.After(now.Add(gapOfferClockSlack)) {
		return true
	}
	return !now.Before(staged.Add(d.gapOfferLifetime()))
}

var gapOfferDropRetry = time.Minute

func gapOfferTimeOf(o files.GapOffer) files.GapOfferTime {
	return files.GapOfferTime{MsgID: o.MsgID, RequesterPeerID: o.RequesterPeerID, StagingID: o.StagingID, StagedAt: o.StagedAt}
}

func resendKeptOffer(ctx context.Context, d *daemon, logger *slog.Logger, fault func(string, string, ...any), requester, msgID, fingerprint string, row FileEventBody, kept files.GapOffer) {
	defer kept.Wipe()
	if row.Size != kept.PlaintextSize+chacha20poly1305.Overhead {
		logger.Debug("file request: not served", slog.String("outcome", "size_disagrees"),
			slog.Uint64("row_size", row.Size), slog.Uint64("kept_size", kept.PlaintextSize))
		return
	}
	if err := offerStagedFile(ctx, d, requester, msgID, fingerprint, row, stagingFromKept(kept)); err != nil {
		fault("file request: kept offer not sent again", "resend_failed",
			slog.String("staging_id", kept.StagingID), slog.Any("err", err))
		return
	}
	logger.Debug("file request: served",
		slog.String("outcome", "resent"),
		slog.String("staging_id", kept.StagingID),
		slog.Time("ends", gapOfferEnd(d, kept.StagedAt)),
	)
}

func stagingFromKept(kept files.GapOffer) siblingStaging {
	return siblingStaging{
		stagingID: kept.StagingID,
		token:     kept.Token,
		key:       kept.Key,
		nonce:     kept.Nonce,
		sha256Hex: kept.Sha256Ciphertext,
		size:      kept.BlobSize,
		plainLen:  kept.PlaintextSize,
	}
}

func expireGapOffers(ctx context.Context, d *daemon, now time.Time) time.Time {
	offers, err := d.files.ListGapOfferTimes()
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("file request: kept offers not read", slog.Any("err", err))
		}
		return now.Add(gapOfferDropRetry)
	}
	var next time.Time
	soonest := func(t time.Time) {
		if next.IsZero() || t.Before(next) {
			next = t
		}
	}
	dropped, failed := 0, 0
	for _, o := range offers {
		switch {
		case !gapOfferEnded(d, o.StagedAt, now):
			soonest(gapOfferEnd(d, o.StagedAt))
		case ctx.Err() != nil:

		case dropEndedGapOffer(ctx, d, slog.Default(), o):
			dropped++
		default:
			failed++
			soonest(now.Add(gapOfferDropRetry))
		}
	}
	slog.Debug("file request: kept offers checked",
		slog.Int("kept", len(offers)-dropped),
		slog.Int("dropped", dropped),
		slog.Int("drop_failed", failed),
	)
	return next
}

func dropEndedGapOffer(ctx context.Context, d *daemon, logger *slog.Logger, o files.GapOfferTime) bool {
	logger = logger.With(
		slog.String("msg_id", o.MsgID),
		slog.String("sibling_peer_id", o.RequesterPeerID),
		slog.String("staging_id", o.StagingID),
	)
	if d.backendClient == nil || !d.backendReachable.Load() {
		return false
	}
	if err := dropStagedForRequest(ctx, d, o.MsgID, o.StagingID); err != nil {
		if ctx.Err() == nil {
			logger.Warn("file request: kept offer's blob not dropped after its hour; tried again", slog.Any("err", err))
		}
		return false
	}
	if err := d.files.DeleteGapOffer(o.MsgID, o.RequesterPeerID, o.StagingID); err != nil {
		if ctx.Err() == nil {
			logger.Warn("file request: kept offer not removed after its hour", slog.Any("err", err))
		}
		return false
	}
	logger.Debug("file request: kept offer dropped after its hour", slog.String("outcome", "expired"))
	return true
}

func dropStagedForRequest(ctx context.Context, d *daemon, msgID, stagingID string) error {
	if d.backendClient == nil {
		return errors.New("gapfill: no backend client")
	}
	dropCtx, cancel := fileServeCleanupContext(ctx)
	err := d.backendClient.DropFile(dropCtx, stagingID)
	cancel()
	if err != nil {
		return err
	}
	return d.files.RemoveSiblingStagingID(msgID, stagingID)
}

func checkKeptGapOffers(d *daemon) {
	d.fileServer.askExpiry()
}

func offerStagedFile(ctx context.Context, d *daemon, requester, msgID, fingerprint string, row FileEventBody, st siblingStaging) error {
	defer wipeSiblingStaging(st)
	fo, err := json.Marshal(msg.FileOfferBody{
		Token:            st.token,
		UrlPath:          "/files/" + st.token,
		Name:             row.Name,
		Size:             st.plainLen + chacha20poly1305.Overhead,
		Mime:             row.Mime,
		Sha256Ciphertext: st.sha256Hex,
		Caption:          row.Caption,
	})
	if err != nil {

		return fmt.Errorf("%w: file offer will not marshal", errGapOfferNotQueued)
	}
	return sendGapSiblingOffer(ctx, d, requester, syncrail.GapSiblingOfferBody{
		Fingerprint: fingerprint,
		MsgID:       msgID,
		FileOffer:   fo,
		SiblingOffer: syncrail.SiblingOffer{
			StagingID:        st.stagingID,
			Token:            st.token,
			Key:              append([]byte(nil), st.key...),
			Nonce:            append([]byte(nil), st.nonce...),
			BlobSize:         st.size,
			PlaintextSize:    st.plainLen,
			Sha256Ciphertext: st.sha256Hex,
		},
	})
}

func unrecordStagedFile(ctx context.Context, d *daemon, logger *slog.Logger, msgID, requester, stagingID string) {
	if ctx.Err() != nil {
		return
	}
	logger = logger.With(slog.String("staging_id", stagingID))
	if err := d.files.DeleteGapOffer(msgID, requester, stagingID); err != nil {

		if ctx.Err() == nil {
			logger.Warn("file request: staging id of an unsent offer not removed", slog.Any("err", err))
		}
		return
	}
	if err := dropStagedForRequest(ctx, d, msgID, stagingID); err != nil {
		if ctx.Err() == nil {
			logger.Warn("file request: blob of an unsent offer not dropped; it goes with the row", slog.Any("err", err))
		}
	}
}

const fileServeCleanupTimeout = 2 * time.Second

func fileServeCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), fileServeCleanupTimeout)
}

func wipeSiblingStaging(st siblingStaging) {
	zeroBytes(st.key)
	zeroBytes(st.nonce)
}
