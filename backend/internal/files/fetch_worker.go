package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"haoma/internal/logid"
	"haoma/internal/xport"
)

type PeerResolver interface {
	OnionsForPeer(peerID string) ([]string, error)
}

type PeerResolverFunc func(peerID string) ([]string, error)

func (f PeerResolverFunc) OnionsForPeer(peerID string) ([]string, error) { return f(peerID) }

type HTTPClientForPeer func(peerID string) (*http.Client, error)

type FetchEvent struct {
	MsgID         string     `json:"msg_id"`
	Token         string     `json:"token"`
	State         FetchState `json:"state"`
	BytesReceived int64      `json:"bytes_received,omitempty"`
	TotalBytes    int64      `json:"total_bytes,omitempty"`
	LastError     string     `json:"last_error,omitempty"`

	Reason FetchFailReason `json:"reason,omitempty"`
	At     int64           `json:"at"`
}

type FetchEventSink interface {
	OnFetchEvent(ev FetchEvent)
}

type FetchEventSinkFunc func(FetchEvent)

func (f FetchEventSinkFunc) OnFetchEvent(ev FetchEvent) { f(ev) }

const progressEmitInterval = time.Second

const fetchTimeout = 10 * time.Minute

const (
	FetchParallelism = 5

	RetryAttemptCap uint16 = 20

	RetryAgeCap = 7 * 24 * time.Hour

	RetryBacklogCap = 20
)

type Worker struct {
	mgr           *Manager
	resolver      PeerResolver
	clientForPeer HTTPClientForPeer
	sink          FetchEventSink
	log           *slog.Logger

	OnDialFailure func(onion string)

	kick chan string
	sem  chan struct{}

	mu       sync.Mutex
	inflight map[string]struct{}
}

func NewWorker(mgr *Manager, resolver PeerResolver, clientForPeer HTTPClientForPeer, sink FetchEventSink, log *slog.Logger) *Worker {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Worker{
		mgr:           mgr,
		resolver:      resolver,
		clientForPeer: clientForPeer,
		sink:          sink,
		log:           log,
		kick:          make(chan string, 64),
		sem:           make(chan struct{}, FetchParallelism),
		inflight:      map[string]struct{}{},
	}
}

func (w *Worker) Kick(token string) {
	if w == nil || token == "" {
		return
	}
	select {
	case w.kick <- token:
	default:

		w.log.Debug("fetch worker kick dropped (queue full)")
	}
}

func (w *Worker) Run(ctx context.Context) {
	if w == nil || w.mgr == nil {
		return
	}
	if w.resolver == nil || w.clientForPeer == nil {
		w.log.Warn("fetch worker not started: missing resolver or http client")
		return
	}

	if err := w.mgr.EnsureStagingDir(); err != nil {
		w.log.Warn("fetch worker: staging dir create failed", slog.Any("err", err))
	}

	pending, err := w.mgr.ListPendingFetches()
	if err != nil {
		w.log.Warn("fetch worker: startup scan failed", slog.Any("err", err))
	}
	for _, f := range pending {
		w.startFetch(ctx, f.Token)
	}
	w.log.Debug("fetch worker started",
		slog.Int("resumed", len(pending)),
	)
	defer w.log.Debug("fetch worker stopped")

	for {
		select {
		case <-ctx.Done():
			return
		case token, ok := <-w.kick:
			if !ok {
				return
			}
			w.startFetch(ctx, token)
		}
	}
}

func (w *Worker) startFetch(ctx context.Context, token string) {
	w.mu.Lock()
	if _, busy := w.inflight[token]; busy {
		w.mu.Unlock()
		w.log.Debug("fetch already in flight; ignoring kick")
		return
	}
	w.inflight[token] = struct{}{}
	w.mu.Unlock()

	select {
	case w.sem <- struct{}{}:
	case <-ctx.Done():
		w.mu.Lock()
		delete(w.inflight, token)
		w.mu.Unlock()
		return
	}

	go func() {
		defer func() {
			<-w.sem
			w.mu.Lock()
			delete(w.inflight, token)
			w.mu.Unlock()
		}()
		w.runOne(ctx, token)
	}()
}

func (w *Worker) runOne(ctx context.Context, token string) {
	row, err := w.mgr.GetFetch(token)
	if err != nil {
		w.log.Debug("fetch row missing on dispatch",
			slog.Any("err", err),
		)
		return
	}

	unlock := w.mgr.fetchLocks.lock(row.MsgID)
	row, err = w.mgr.GetFetch(token)
	if err != nil {
		unlock()
		w.log.Debug("fetch row missing on dispatch",
			slog.Any("err", err),
		)
		return
	}
	switch row.State {
	case FetchStateReady, FetchStateFailedPermanent:
		unlock()

		w.log.Debug("fetch already terminal; skipping",
			slog.String("msg_id", row.MsgID),
			slog.String("state", string(row.State)),
		)
		return
	}

	logger := w.log.With(
		slog.String("msg_id", row.MsgID),
		slog.String("peer_id", row.PeerID),
	)
	logger.Debug("fetch begin")

	offset, err := w.mgr.StagingPartSize(row.MsgID)
	if err != nil {
		unlock()
		w.transitionTransient(row, fmt.Sprintf("staging stat: %v", err), logger)
		return
	}
	row.BytesReceived = offset
	row.State = FetchStateDownloading
	row.LastError = ""
	row.UpdatedAt = time.Now().Unix()
	if err := w.mgr.UpdateFetch(row); err != nil {
		logger.Warn("persist downloading failed", slog.Any("err", err))
	}
	unlock()
	w.emit(row, "")

	if row.ExpectedSize > 0 && offset == row.ExpectedSize {
		var done bool
		if row, done = w.finishFromPart(row, logger); done {
			return
		}
	}

	addrs, err := w.resolver.OnionsForPeer(row.PeerID)
	if err != nil {
		w.transitionTransient(row, fmt.Sprintf("peer resolve: %v", err), logger)
		return
	}
	if len(addrs) == 0 {
		w.transitionTransient(row, "peer has no known addresses", logger)
		return
	}

	hc, err := w.clientForPeer(row.PeerID)
	if err != nil {
		w.transitionTransient(row, fmt.Sprintf("http client: %v", err), logger)
		return
	}

	attemptCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	var lastErr error
	for i, addr := range addrs {
		url := fmt.Sprintf("http://%s.onion%s", addr, row.UrlPath)

		logger.Debug("fetch attempt",
			slog.Int("addr_idx", i),
			slog.Int64("range_start", row.BytesReceived),
		)
		state, finalRow, terminal, err := w.streamOne(attemptCtx, hc, url, row, logger)
		if err != nil && errors.Is(err, xport.ErrCircuitRenewed) {

			logger.Debug("fetch: asking again at once on a new circuit", slog.Int("addr_idx", i))
			if hc2, cerr := w.clientForPeer(row.PeerID); cerr == nil {
				hc = hc2
				state, finalRow, terminal, err = w.streamOne(attemptCtx, hc, url, finalRow, logger)
			}
		}
		if err == nil {
			row = finalRow
			row.State = state
			row.LastError = ""
			row.UpdatedAt = time.Now().Unix()
			if err := w.mgr.UpdateFetch(row); err != nil {
				logger.Warn("persist terminal failed", slog.Any("err", err))
			}
			w.emit(row, "")
			logger.Info("fetch ready",
				slog.Int64("bytes", row.BytesReceived),
			)
			return
		}
		lastErr = err
		if terminal {
			row = finalRow
			w.transitionPermanent(row, err.Error(), permanentReason(err), logger)
			return
		}

		if w.OnDialFailure != nil {
			w.OnDialFailure(addr)
		}

		row = finalRow
	}

	msg := "all addresses failed"
	if lastErr != nil {
		msg = lastErr.Error()
	}
	w.transitionTransient(row, msg, logger)
}

func (w *Worker) finishFromPart(row Fetch, logger *slog.Logger) (updated Fetch, done bool) {
	partPath := w.mgr.StagingPartPath(row.MsgID)
	sum, size, err := fileSha256(partPath)
	if err == nil && size == row.ExpectedSize && sum == row.ExpectedSha256 {
		if err := os.Rename(partPath, w.mgr.StagingPath(row.MsgID)); err != nil {
			logger.Warn("whole .part file: rename failed; downloading again", slog.Any("err", err))
		} else {
			if err := syncDir(w.mgr.StagingDir()); err != nil {
				logger.Warn("fsync staging dir failed", slog.Any("err", err))
			}
			row.State = FetchStateReady
			row.LastError = ""
			row.UpdatedAt = time.Now().Unix()
			if err := w.mgr.UpdateFetch(row); err != nil {
				logger.Warn("persist terminal failed", slog.Any("err", err))
			}
			w.emit(row, "")
			logger.Info("fetch ready",
				slog.Int64("bytes", row.BytesReceived),
				slog.String("source", "whole_part_file"),
			)
			return row, true
		}
	}
	logger.Debug("full-size .part file does not match the offer; downloading from byte 0",
		slog.Any("err", err),
	)
	if terr := os.Truncate(partPath, 0); terr != nil && !errors.Is(terr, os.ErrNotExist) {
		logger.Warn("truncate staging part failed", slog.Any("err", terr))
	}
	row.BytesReceived = 0
	return row, false
}

func (w *Worker) streamOne(ctx context.Context, hc *http.Client, url string, row Fetch, logger *slog.Logger) (state FetchState, updated Fetch, terminal bool, err error) {
	updated = row

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", updated, false, fmt.Errorf("build request: %w", err)
	}
	if updated.BytesReceived > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", updated.BytesReceived))
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", updated, false, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:

		if updated.BytesReceived != 0 {
			logger.Debug("server ignored range; restarting from 0")
			if err := os.Truncate(w.mgr.StagingPartPath(updated.MsgID), 0); err != nil && !errors.Is(err, os.ErrNotExist) {
				return "", updated, false, fmt.Errorf("truncate staging: %w", err)
			}
			updated.BytesReceived = 0
		}
	case http.StatusPartialContent:

	case http.StatusNotFound:
		return "", updated, true, permanentErr(FetchFailBlobGone, "peer 404 (token unknown)")
	case http.StatusGone:
		return "", updated, true, permanentErr(FetchFailBlobGone, "peer 410 (token invalidated)")
	case http.StatusRequestedRangeNotSatisfiable:

		_ = os.Truncate(w.mgr.StagingPartPath(updated.MsgID), 0)
		updated.BytesReceived = 0
		return "", updated, false, fmt.Errorf("peer 416 (range not satisfiable); will restart")
	default:
		return "", updated, false, fmt.Errorf("peer status %d", resp.StatusCode)
	}

	partPath := w.mgr.StagingPartPath(updated.MsgID)
	f, err := os.OpenFile(partPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, fileMode)
	if err != nil {
		return "", updated, false, fmt.Errorf("open staging: %w", err)
	}
	defer f.Close()

	hasher := sha256.New()
	if updated.BytesReceived > 0 {
		seed, openErr := os.Open(partPath)
		if openErr != nil {
			return "", updated, false, fmt.Errorf("open staging for hash seed: %w", openErr)
		}
		if _, err := io.CopyN(hasher, seed, updated.BytesReceived); err != nil {
			seed.Close()
			return "", updated, false, fmt.Errorf("hash seed: %w", err)
		}
		seed.Close()
	}

	mw := io.MultiWriter(f, hasher)
	buf := make([]byte, 32*1024)
	lastEmit := time.Now()
	for {
		if cerr := ctx.Err(); cerr != nil {
			return "", updated, false, fmt.Errorf("ctx: %w", cerr)
		}
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := mw.Write(buf[:n]); werr != nil {
				return "", updated, false, fmt.Errorf("write staging: %w", werr)
			}
			updated.BytesReceived += int64(n)
			if updated.ExpectedSize > 0 && updated.BytesReceived > updated.ExpectedSize {
				return "", updated, true, permanentErr(FetchFailIntegrity, fmt.Sprintf("oversized: declared %d, received %d", updated.ExpectedSize, updated.BytesReceived))
			}
			if time.Since(lastEmit) >= progressEmitInterval {
				w.emit(updated, "")
				lastEmit = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", updated, false, fmt.Errorf("read body: %w", rerr)
		}
	}
	if err := f.Sync(); err != nil {
		return "", updated, false, fmt.Errorf("fsync staging: %w", err)
	}

	if err := f.Close(); err != nil {
		return "", updated, false, fmt.Errorf("close staging: %w", err)
	}

	updated.UpdatedAt = time.Now().Unix()
	if err := w.mgr.UpdateFetch(updated); err != nil {
		logger.Warn("persist progress failed", slog.Any("err", err))
	}

	if updated.ExpectedSize > 0 && updated.BytesReceived != updated.ExpectedSize {
		return "", updated, true, permanentErr(FetchFailIntegrity, fmt.Sprintf("size mismatch: declared %d, received %d", updated.ExpectedSize, updated.BytesReceived))
	}
	gotSha := hex.EncodeToString(hasher.Sum(nil))
	if updated.ExpectedSha256 != "" && gotSha != updated.ExpectedSha256 {
		return "", updated, true, permanentErr(FetchFailIntegrity, fmt.Sprintf("sha256 mismatch: want %s got %s", updated.ExpectedSha256, gotSha))
	}

	if err := os.Rename(partPath, w.mgr.StagingPath(updated.MsgID)); err != nil {
		return "", updated, false, fmt.Errorf("rename staging part: %w", err)
	}

	if err := syncDir(w.mgr.StagingDir()); err != nil {
		logger.Warn("fsync staging dir failed", slog.Any("err", err))
	}

	return FetchStateReady, updated, false, nil
}

type permanentError struct {
	reason FetchFailReason
	msg    string
}

func (e *permanentError) Error() string { return e.msg }

func permanentErr(reason FetchFailReason, msg string) error {
	return &permanentError{reason: reason, msg: msg}
}

func permanentReason(err error) FetchFailReason {
	var pe *permanentError
	if errors.As(err, &pe) {
		return pe.reason
	}
	return FetchFailIntegrity
}

const fetchTokenRedacted = "<token>"

func redactFetchError(msg, token string) string {
	msg = logid.WithholdOnions(msg)
	if token != "" {
		msg = strings.ReplaceAll(msg, token, fetchTokenRedacted)
	}
	return msg
}

func (w *Worker) transitionTransient(row Fetch, msg string, logger *slog.Logger) {
	msg = redactFetchError(msg, row.Token)
	row.State = FetchStateFailedTransient
	row.LastError = msg
	row.UpdatedAt = time.Now().Unix()

	kicked := row.Kicked
	row.Kicked = false
	if !kicked && row.RetryAttempts < ^uint16(0) {
		row.RetryAttempts++
	}
	if err := w.mgr.UpdateFetch(row); err != nil {
		logger.Warn("persist transient failed", slog.Any("err", err))
	}
	logger.Info("fetch failed transient",
		slog.String("err", msg),
		slog.Int("retry_attempts", int(row.RetryAttempts)),
		slog.Bool("was_kicked", kicked),
	)
	w.emit(row, msg)
}

func (w *Worker) transitionPermanent(row Fetch, msg string, reason FetchFailReason, logger *slog.Logger) {
	msg = redactFetchError(msg, row.Token)
	row.State = FetchStateFailedPermanent
	row.FailReason = reason
	row.LastError = msg
	row.UpdatedAt = time.Now().Unix()
	if err := w.mgr.UpdateFetch(row); err != nil {
		logger.Warn("persist permanent failed", slog.Any("err", err))
	}

	if derr := w.mgr.DeleteStaging(row.MsgID); derr != nil {
		logger.Warn("staging cleanup after permanent failure", slog.Any("err", derr))
	}
	logger.Warn("fetch failed permanent", slog.String("err", msg), slog.String("reason", string(reason)))
	w.emit(row, msg)
}

func (w *Worker) emit(row Fetch, errMsg string) {
	if w.sink == nil {
		return
	}
	w.sink.OnFetchEvent(FetchEvent{
		MsgID:         row.MsgID,
		Token:         row.Token,
		State:         row.State,
		BytesReceived: row.BytesReceived,
		TotalBytes:    row.ExpectedSize,
		LastError:     errMsg,
		Reason:        row.FailReason,
		At:            time.Now().Unix(),
	})
}

func (w *Worker) RetryAllFailed(ctx context.Context, isRetired func(peerID string) bool) (int, error) {
	return w.retryAllFailed(ctx, isRetired, false)
}

func (w *Worker) RetryAllFailedKicked(ctx context.Context, isRetired func(peerID string) bool) (int, error) {
	return w.retryAllFailed(ctx, isRetired, true)
}

func (w *Worker) retryAllFailed(ctx context.Context, isRetired func(peerID string) bool, kicked bool) (int, error) {
	if w == nil || w.mgr == nil {
		return 0, errors.New("fetch worker not initialised")
	}
	rows, err := w.mgr.ListFailedTransientFetches()
	if err != nil {
		return 0, fmt.Errorf("list failed transient: %w", err)
	}
	if len(rows) == 0 {
		return 0, nil
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CreatedAt != rows[j].CreatedAt {
			return rows[i].CreatedAt < rows[j].CreatedAt
		}
		return rows[i].Token < rows[j].Token
	})

	logger := w.log.With(slog.Int("failed_transient_total", len(rows)))

	if len(rows) > RetryBacklogCap {
		excess := rows[:len(rows)-RetryBacklogCap]
		rows = rows[len(rows)-RetryBacklogCap:]
		for _, row := range excess {
			rl := logger.With(
				slog.String("msg_id", row.MsgID),
			)
			w.transitionPermanent(row, "retry: backlog cap exceeded", FetchFailLimit, rl)
		}
	}

	now := time.Now()
	enqueued := 0
	for _, row := range rows {
		rl := logger.With(
			slog.String("msg_id", row.MsgID),
			slog.String("peer_id", row.PeerID),
		)
		if isRetired != nil && isRetired(row.PeerID) {
			w.transitionPermanent(row, "retry: peer retired", FetchFailLimit, rl)
			continue
		}
		if row.RetryAttempts >= RetryAttemptCap {
			w.transitionPermanent(row, fmt.Sprintf("retry: attempt cap reached (%d)", row.RetryAttempts), FetchFailLimit, rl)
			continue
		}
		if row.CreatedAt > 0 && now.Sub(time.Unix(row.CreatedAt, 0)) > RetryAgeCap {
			w.transitionPermanent(row, "retry: row age cap exceeded", FetchFailLimit, rl)
			continue
		}
		if kicked && !row.Kicked {
			row.Kicked = true
			if err := w.mgr.UpdateFetch(row); err != nil {

				rl.Warn("retry: could not mark the fetch kicked", slog.Any("err", err))
			}
		}
		w.Kick(row.Token)
		enqueued++
	}
	logger.Info("retry sweep done",
		slog.Int("enqueued", enqueued),
	)
	return enqueued, nil
}
