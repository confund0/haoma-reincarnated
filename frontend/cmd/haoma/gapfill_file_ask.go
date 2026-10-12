package main

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/files"
	"haoma-frontend/internal/logid"
	"haoma-frontend/internal/syncrail"
)

const (
	fileRequestInterval = 60 * time.Second

	fileRequestsInFlight = 10

	fileRequestAnswerWait = 5 * time.Minute
)

type fileAsker struct {
	mu   sync.Mutex
	last map[string]time.Time

	settleOwed atomic.Bool

	now func() time.Time
}

func (a *fileAsker) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

func noteWantedFile(d *daemon, logger *slog.Logger, chatID chat.ChatID, msgID string, displayTs int64, route string) {
	if d.files == nil {
		return
	}
	added, err := d.files.AddWantedFile(files.WantedFile{MsgID: msgID, ChatID: chatID, DisplayTs: displayTs})
	if err != nil {
		logger.Warn("file request: wanted file not recorded; it is not asked for",
			slog.String("msg_id", msgID),
			slog.String("landed_by", route),
			slog.Any("err", err),
		)
		return
	}
	logger.Debug("file request: file wanted",
		slog.String("msg_id", msgID),
		slog.String("landed_by", route),
		slog.Bool("added", added),
	)
}

func fillWantedOnce(d *daemon) {
	logger := slog.With(slog.String("step", "wanted_fill"))
	done, err := d.files.WantedFilled()
	if err != nil {
		logger.Warn("file request: wanted set scan marker not read", slog.Any("err", err))
		return
	}
	if done {
		return
	}
	type held struct {
		msgID     string
		chatID    chat.ChatID
		displayTs int64
	}
	var rows []held
	scanned := 0

	if err := d.events.EachFileRow(func(ev events.Event) error {
		scanned++
		if ev.DeletedAt != 0 || ev.MsgID == "" {
			return nil
		}
		rows = append(rows, held{msgID: ev.MsgID, chatID: ev.ChatID, displayTs: ev.DisplayTs})
		return nil
	}); err != nil {
		logger.Warn("file request: wanted set scan failed", slog.Any("err", err))
		return
	}
	added := 0
	for _, r := range rows {
		ok, err := d.files.AddWantedFile(files.WantedFile{MsgID: r.msgID, ChatID: r.chatID, DisplayTs: r.displayTs})
		if err != nil {
			logger.Warn("file request: wanted set scan failed", slog.String("msg_id", r.msgID), slog.Any("err", err))
			return
		}
		if ok {
			added++
		}
	}
	if err := d.files.MarkWantedFilled(); err != nil {
		logger.Warn("file request: wanted set scan marker not written; the scan runs again", slog.Any("err", err))
		return
	}
	logger.Info("file request: wanted set filled from the rows held",
		slog.Int("file_rows", scanned),
		slog.Int("added", added),
	)
}

func askForFiles(ctx context.Context, d *daemon, siblingPeerID string) {
	if d.files == nil || d.events == nil {
		return
	}
	logger := slog.With(slog.String("sibling_peer_id", siblingPeerID))
	a := &d.fileAsker
	a.mu.Lock()
	defer a.mu.Unlock()

	now := a.clock()
	if last, ok := a.last[siblingPeerID]; ok && now.Sub(last) < fileRequestInterval {
		owed := a.settleOwed.Load()
		logger.Debug("file request: not sent",
			slog.String("outcome", "interval"),
			slog.Duration("since_last", now.Sub(last)),
			slog.Bool("settle_owed", owed),
		)
		if owed {

			d.syncTrigger.note()
		}
		return
	}
	a.settleOwed.Store(false)

	fillWantedOnce(d)

	wanted, err := d.files.ListWantedFiles()
	if err != nil {
		logger.Warn("file request: wanted files not read; nothing asked", slog.Any("err", err))
		return
	}
	wanted = liveWantedFiles(d, logger, wanted)
	inFlight, err := gapDownloadsInFlight(d)
	if err != nil {
		logger.Warn("file request: downloads in flight not read; nothing asked", slog.Any("err", err))
		return
	}
	ids, asked := pickFileRequest(wanted, inFlight, now, fileRequestsInFlight)
	if len(ids) == 0 {
		logger.Debug("file request: not sent",
			slog.String("outcome", notSentOutcome(len(wanted), asked)),
			slog.Int("wanted", len(wanted)),
			slog.Int("downloading", inFlight),
			slog.Int("asked_unanswered", asked),
		)
		return
	}
	if ctx.Err() != nil {
		return
	}
	if err := sendFileRequest(ctx, d, siblingPeerID, ids); err != nil {
		if ctx.Err() != nil {

			logger.Debug("file request: not sent", slog.String("outcome", "shutdown"))
			return
		}
		logger.Warn("file request: send failed; the next sweep asks again",
			slog.Int("msg_ids", len(ids)),
			slog.Any("err", err),
		)
		return
	}
	if a.last == nil {
		a.last = make(map[string]time.Time)
	}
	a.last[siblingPeerID] = now
	if err := d.files.MarkWantedAsked(ids, now.Unix()); err != nil {

		logger.Warn("file request: turn not recorded", slog.Any("err", err))
	}
	logger.Info("file request: sent",
		slog.Int("msg_ids", len(ids)),
		slog.Int("wanted", len(wanted)),
		slog.Int("downloading", inFlight),
		slog.Int("asked_unanswered", asked),
	)
	logger.Debug("file request: files asked for", slog.Any("msg_ids", shortIDs(ids)))
}

func shortIDs(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = logid.Short(id)
	}
	return out
}

func notSentOutcome(wanted, askedUnanswered int) string {
	switch {
	case wanted == 0:
		return "nothing_wanted"
	case askedUnanswered == wanted:
		return "all_asked_recently"
	default:
		return "in_flight_full"
	}
}

func pickFileRequest(wanted []files.WantedFile, downloading int, now time.Time, inFlightLimit int) (ids []string, askedUnanswered int) {
	answerFrom := now.Add(-fileRequestAnswerWait).Unix()
	var turn []files.WantedFile
	for _, w := range wanted {
		if w.AskedAt > answerFrom {
			askedUnanswered++
			continue
		}
		turn = append(turn, w)
	}
	budget := min(inFlightLimit-downloading-askedUnanswered, syncrail.MaxFileRequestIDs)
	if len(turn) == 0 || budget <= 0 {
		return nil, askedUnanswered
	}
	sort.Slice(turn, func(i, j int) bool {
		if turn[i].AskedAt != turn[j].AskedAt {
			return turn[i].AskedAt < turn[j].AskedAt
		}
		if turn[i].DisplayTs != turn[j].DisplayTs {
			return turn[i].DisplayTs > turn[j].DisplayTs
		}
		return turn[i].MsgID < turn[j].MsgID
	})
	if len(turn) > budget {
		turn = turn[:budget]
	}
	ids = make([]string, len(turn))
	for i, w := range turn {
		ids[i] = w.MsgID
	}
	return ids, askedUnanswered
}

func liveWantedFiles(d *daemon, logger *slog.Logger, wanted []files.WantedFile) []files.WantedFile {
	out := wanted[:0]
	for _, w := range wanted {
		stale := ""
		ev, err := d.events.GetByMsgID(w.MsgID)
		switch {
		case errors.Is(err, events.ErrEventNotFound):
			stale = "no_row"
		case err != nil:
			logger.Warn("file request: wanted file check failed", slog.String("msg_id", w.MsgID), slog.Any("err", err))
			continue
		case ev.Kind != events.KindFile:
			stale = "not_a_file"
		case ev.DeletedAt != 0:
			stale = "row_deleted"
		}
		if stale == "" {
			switch _, mErr := d.files.GetMeta(w.MsgID); {
			case mErr == nil:
				stale = "metadata_held"
			case !errors.Is(mErr, files.ErrMetaNotFound):
				logger.Warn("file request: wanted file check failed", slog.String("msg_id", w.MsgID), slog.Any("err", mErr))
				continue
			}
		}
		if stale != "" {
			if err := d.files.DeleteWantedFile(w.MsgID); err != nil {
				logger.Warn("file request: wanted file not removed", slog.String("msg_id", w.MsgID), slog.Any("err", err))
			}
			logger.Debug("file request: file no longer wanted",
				slog.String("msg_id", w.MsgID),
				slog.String("outcome", stale),
			)
			continue
		}
		out = append(out, w)
	}
	return out
}

func gapDownloadsInFlight(d *daemon) (int, error) {
	held, err := d.files.ListSiblingOffers()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, o := range held {
		meta, err := d.files.GetMeta(o.MsgID)
		if errors.Is(err, files.ErrMetaNotFound) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if !meta.GapFill {
			continue
		}

		switch meta.State {
		case files.StatePending, files.StateDownloading, files.StateAwaitingKey:
			n++
		}
	}
	return n, nil
}

func siblingFetchFailedPermanent(d *daemon, logger *slog.Logger, msgID string, bytes uint64, reason string) {
	logger = logger.With(slog.String("fail_reason", reason))
	meta, err := d.files.GetMeta(msgID)
	if errors.Is(err, files.ErrMetaNotFound) {
		logger.Debug("sibling fetch: failed for a file no longer held; no row change",
			slog.String("outcome", "row_gone"),
		)
		return
	}
	if err != nil {
		logger.Warn("sibling fetch: metadata read failed; the failure is final", slog.Any("err", err))
		stampFileEventState(d, "", msgID, files.StateFailedPermanent, bytes, siblingDetailFetchFailed)
		return
	}
	if meta.State == files.StateReady || meta.State == files.StateExpired {

		logger.Debug("sibling fetch: failed for a file already settled; no row change",
			slog.String("state", string(meta.State)),
			slog.String("outcome", "settled_ignored"),
		)
		return
	}
	askAgain := reason == backendapi.FetchFailBlobGone || reason == backendapi.FetchFailLimit
	switch {
	case !meta.GapFill:
		stampFileEventState(d, meta.ChatID, msgID, files.StateFailedPermanent, bytes, siblingDetailFetchFailed)
		return
	case !askAgain:
		logger.Warn("sibling offer on gap-fill: download failed; final",
			slog.String("outcome", "final"),
			slog.Int("asks", meta.GapAsks),
		)
		stampFileEventState(d, meta.ChatID, msgID, files.StateFailedPermanent, bytes, siblingDetailFetchFailed)
		recordGapFailReason(d, logger, meta, reason)
		gapFileSettled(d, logger)
		return
	case meta.GapAsks >= files.MaxGapAsks:
		logger.Warn("sibling offer on gap-fill: download failed; the ask limit is reached, so it is final until retried",
			slog.String("outcome", "ask_limit"),
			slog.Int("asks", meta.GapAsks),
		)
		stampFileEventState(d, meta.ChatID, msgID, files.StateFailedPermanent, bytes, siblingDetailFetchFailed)
		recordGapFailReason(d, logger, meta, reason)
		gapFileSettled(d, logger)
		return
	}
	if requeueGapFile(d, logger, msgID, func(m files.Metadata) int { return m.GapAsks }) {
		logger.Info("sibling offer on gap-fill: download failed; the file is asked for again",
			slog.String("outcome", "asked_again"),
			slog.Int("asks", meta.GapAsks),
		)
		gapFileSettled(d, logger)
	}
}

func gapFileSettled(d *daemon, logger *slog.Logger) {
	if d.files == nil {
		return
	}
	wanted, err := d.files.ListWantedFiles()
	if err != nil {
		logger.Warn("file request: wanted files not read; no sweep armed after a settled download", slog.Any("err", err))
		return
	}
	if len(wanted) == 0 {
		return
	}
	d.fileAsker.settleOwed.Store(true)
	d.syncTrigger.note()
	logger.Debug("file request: a gap-fill download settled; a sweep is armed",
		slog.Int("wanted", len(wanted)),
	)
}

func requeueGapFile(d *daemon, logger *slog.Logger, msgID string, asks func(files.Metadata) int) bool {
	ev, err := d.events.GetByMsgID(msgID)
	if errors.Is(err, events.ErrEventNotFound) {
		logger.Debug("sibling offer on gap-fill: the row is gone; not asked for again",
			slog.String("outcome", "row_gone"),
		)
		return false
	}
	if err != nil {
		logger.Warn("sibling offer on gap-fill: not asked for again — row read failed", slog.Any("err", err))
		return false
	}
	if ev.DeletedAt != 0 {
		logger.Debug("sibling offer on gap-fill: the row is deleted; not asked for again",
			slog.String("outcome", "row_gone"),
		)
		return false
	}
	dropped, err := d.files.RequeueGapFile(msgID, ev.DisplayTs, asks)
	if err != nil {
		if errors.Is(err, files.ErrMetaNotFound) || errors.Is(err, files.ErrGapFileSettled) {
			logger.Debug("sibling offer on gap-fill: nothing to ask for again",
				slog.String("outcome", "settled_or_gone"),
			)
			return false
		}
		logger.Warn("sibling offer on gap-fill: not asked for again — store write failed", slog.Any("err", err))
		return false
	}
	stampFileEventState(d, dropped.ChatID, msgID, files.StatePending, 0, "")
	return true
}

func recordGapFailReason(d *daemon, logger *slog.Logger, meta files.Metadata, reason string) {
	if !meta.GapFill {
		return
	}
	err := d.files.UpdateMeta(meta.MsgID, func(m *files.Metadata) error {
		m.FailReason = reason
		return nil
	})
	if err != nil && !errors.Is(err, files.ErrMetaNotFound) {
		logger.Warn("sibling offer on gap-fill: failure reason not recorded", slog.Any("err", err))
	}
}

func retryGapFiles(d *daemon) {
	if d.files == nil || d.events == nil {
		return
	}
	logger := slog.With(slog.String("step", "retry"))
	reset, err := d.files.ResetWantedAsks()
	if err != nil {
		logger.Warn("file request: ask counts not reset", slog.Any("err", err))
	}
	failed, err := d.files.ListGapFillFailed()
	if err != nil {
		logger.Warn("file request: failed gap-fill downloads not read", slog.Any("err", err))
	}
	requeued := 0
	for _, m := range failed {
		if m.FailReason == files.FailIntegrity {
			continue
		}
		l := logger.With(slog.String("msg_id", m.MsgID))
		if requeueGapFile(d, l, m.MsgID, func(files.Metadata) int { return 0 }) {
			requeued++
		}
	}
	logger.Info("file request: retry verb reset the gap-fill asks",
		slog.Int("wanted_reset", reset),
		slog.Int("asked_again", requeued),
	)
	if reset+requeued > 0 {
		d.syncTrigger.note()
	}
}
