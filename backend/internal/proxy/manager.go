package proxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"haoma/internal/logid"
)

type Modality string

const (
	ModalityAudio  Modality = "audio"
	ModalityVideo  Modality = "video"
	ModalityScreen Modality = "screen"
)

func ParseModality(s string) (Modality, error) {
	switch Modality(s) {
	case ModalityAudio, ModalityVideo, ModalityScreen:
		return Modality(s), nil
	default:
		return "", fmt.Errorf("proxy: unknown modality %q (want audio|video|screen)", s)
	}
}

var (
	ErrTokenInUse = errors.New("proxy: token already registered with different params")

	ErrInvalidLocalPort = errors.New("proxy: invalid local_port")
)

type kind int

const (
	kindServe kind = iota + 1
	kindFetch
)

func (k kind) String() string {
	switch k {
	case kindServe:
		return "serve"
	case kindFetch:
		return "fetch"
	}
	return "unknown"
}

type entry struct {
	token     string
	modality  Modality
	kind      kind
	localPort int

	activeMu sync.Mutex
	active   net.Conn
	closed   bool

	writeCloser io.Closer

	cancel   context.CancelFunc
	done     chan struct{}
	peerHost string
}

type Manager struct {
	log *slog.Logger

	statsLog *slog.Logger

	mu      sync.Mutex
	entries map[string]*entry

	ServeWriteWrap func(modality Modality, w io.Writer) io.Writer
}

func NewManager(log, statsLog *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		log:      log,
		statsLog: statsLog,
		entries:  make(map[string]*entry),
	}
}

func (m *Manager) RegisterServe(token string, modality Modality, localPort int) error {
	if token == "" {
		return errors.New("proxy: empty token")
	}
	if localPort <= 0 || localPort > 65535 {
		return fmt.Errorf("%w: %d", ErrInvalidLocalPort, localPort)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.entries[token]; ok {
		if existing.kind == kindServe && existing.modality == modality && existing.localPort == localPort {
			return nil
		}
		return ErrTokenInUse
	}
	m.entries[token] = &entry{
		token:     token,
		modality:  modality,
		kind:      kindServe,
		localPort: localPort,
	}
	m.log.Info("proxy serve registered",
		slog.String("token", shortToken(token)),
		slog.String("modality", string(modality)),
		slog.Int("local_port", localPort),
	)
	return nil
}

func (m *Manager) StartFetch(parent context.Context, token string, modality Modality, peerURL string, localPort int, hc *http.Client) error {
	if token == "" {
		return errors.New("proxy: empty token")
	}
	if localPort <= 0 || localPort > 65535 {
		return fmt.Errorf("%w: %d", ErrInvalidLocalPort, localPort)
	}
	if peerURL == "" {
		return errors.New("proxy: empty peer_url")
	}
	parsed, err := url.Parse(peerURL)
	if err != nil {
		return fmt.Errorf("proxy: bad peer_url: %w", err)
	}
	if hc == nil {
		return errors.New("proxy: nil http client")
	}

	m.mu.Lock()
	if existing, ok := m.entries[token]; ok {
		m.mu.Unlock()
		if existing.kind == kindFetch && existing.modality == modality && existing.localPort == localPort {
			return nil
		}
		return ErrTokenInUse
	}
	ctx, cancel := context.WithCancel(parent)
	e := &entry{
		token:     token,
		modality:  modality,
		kind:      kindFetch,
		localPort: localPort,
		cancel:    cancel,
		done:      make(chan struct{}),
		peerHost:  parsed.Hostname(),
	}
	m.entries[token] = e
	m.mu.Unlock()

	m.log.Info("proxy fetch starting",
		slog.String("token", shortToken(token)),
		slog.String("modality", string(modality)),
		slog.Int("local_port", localPort),
		slog.String("peer_url", peerURL),
	)

	go func() {
		defer close(e.done)
		m.runFetch(ctx, e, peerURL, hc)

		m.mu.Lock()
		if cur, ok := m.entries[token]; ok && cur == e {
			delete(m.entries, token)
		}
		m.mu.Unlock()
	}()
	return nil
}

func (m *Manager) runFetch(ctx context.Context, e *entry, peerURL string, hc *http.Client) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, peerURL, nil)
	if err != nil {
		m.log.Warn("proxy fetch: build request failed",
			slog.String("token", shortToken(e.token)),
			slog.Any("err", err),
		)
		return
	}
	resp, err := hc.Do(req)
	err = logid.RedactURLError(err)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			m.log.Warn("proxy fetch: GET failed",
				slog.String("token", shortToken(e.token)),
				slog.String("peer_url", peerURL),
				slog.Any("err", err),
			)
		}
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		m.log.Warn("proxy fetch: non-200 from peer",
			slog.String("token", shortToken(e.token)),
			slog.Int("status", resp.StatusCode),
		)
		return
	}

	streamerAddr := "127.0.0.1:" + strconv.Itoa(e.localPort)
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", streamerAddr)
	if err != nil {
		m.log.Warn("proxy fetch: dial streamer failed",
			slog.String("token", shortToken(e.token)),
			slog.String("streamer", streamerAddr),
			slog.Any("err", err),
		)
		return
	}
	defer conn.Close()

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
			resp.Body.Close()
		case <-stop:
		}
	}()

	var (
		n       int64
		copyErr error
	)
	if e.modality == ModalityAudio {
		mm := newMediaMeter(m.log, m.statsLog, "fetch", shortToken(e.token))
		meterStop := make(chan struct{})
		go runMeterTicker(mm, meterStop)
		func() {
			defer close(meterStop)
			n, copyErr = copyFramesFetch(conn, resp.Body, mm)
		}()
		mm.end(time.Now())
	} else {
		bm := newByteMeter(m.log, m.statsLog, "fetch", shortToken(e.token), e.modality)
		meterStop := make(chan struct{})
		go runByteMeterTicker(bm, meterStop)
		func() {
			defer close(meterStop)
			cw := &countingWriter{w: conn, onWrite: bm.onBytes}
			n, copyErr = io.Copy(cw, resp.Body)
		}()
		bm.end(time.Now())
	}

	ended, copyErr := classifyEnded(copyErr)
	if ended != "closed" && ctx.Err() != nil {

		ended, copyErr = "closed", nil
	}
	attrs := []any{
		slog.String("token", shortToken(e.token)),
		slog.String("streamer", streamerAddr),
		slog.Int64("bytes", n),
		slog.String("ended", ended),
	}
	if copyErr != nil {
		attrs = append(attrs, slog.Any("err", copyErr))
	}
	m.log.Info("proxy fetch finished", attrs...)
}

func copyFramesFetch(dst io.Writer, src io.Reader, mm *mediaMeter) (int64, error) {
	br := bufio.NewReader(src)
	var total int64
	var buf []byte
	for {
		frame, _, pts, err := readFrame(br, buf)
		if err != nil {
			var pe *frameParseError
			if errors.As(err, &pe) {
				mm.onParseError(pe.err)
				n, werr := dst.Write(pe.consumed)
				total += int64(n)
				if werr != nil {
					return total, werr
				}
				n2, cerr := io.Copy(dst, br)
				return total + n2, cerr
			}
			if errors.Is(err, io.EOF) {
				return total, nil
			}

			return total, err
		}
		mm.onFrameIn(time.Now(), len(frame), pts)
		buf = frame
		n, werr := dst.Write(frame)
		total += int64(n)
		if werr != nil {
			return total, werr
		}
	}
}

func (m *Manager) HandleServe(w http.ResponseWriter, r *http.Request, token string) bool {
	m.mu.Lock()
	e, ok := m.entries[token]
	m.mu.Unlock()
	if !ok || e.kind != kindServe {
		return false
	}

	e.activeMu.Lock()
	if e.closed {
		e.activeMu.Unlock()
		http.Error(w, "proxy cancelled", http.StatusGone)
		return true
	}
	if e.active != nil {
		e.activeMu.Unlock()
		http.Error(w, "token in use", http.StatusConflict)
		return true
	}
	e.activeMu.Unlock()

	streamerAddr := "127.0.0.1:" + strconv.Itoa(e.localPort)
	var d net.Dialer
	conn, err := d.DialContext(r.Context(), "tcp", streamerAddr)
	if err != nil {
		m.log.Warn("proxy serve: dial streamer failed",
			slog.String("token", shortToken(token)),
			slog.String("streamer", streamerAddr),
			slog.Any("err", err),
		)
		http.Error(w, "streamer unavailable", http.StatusBadGateway)
		return true
	}

	e.activeMu.Lock()
	if e.closed {
		e.activeMu.Unlock()
		conn.Close()
		http.Error(w, "proxy cancelled", http.StatusGone)
		return true
	}
	if e.active != nil {
		e.activeMu.Unlock()
		conn.Close()
		http.Error(w, "token in use", http.StatusConflict)
		return true
	}
	e.active = conn
	e.activeMu.Unlock()
	defer func() {
		e.activeMu.Lock()
		if e.active == conn {
			e.active = nil
		}
		e.activeMu.Unlock()
		conn.Close()
	}()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	dst := io.Writer(w)
	if flusher != nil {
		dst = &flushWriter{w: w, f: flusher}
	}
	if m.ServeWriteWrap != nil {
		dst = m.ServeWriteWrap(e.modality, dst)
	}
	if c, ok := dst.(io.Closer); ok {
		e.activeMu.Lock()
		e.writeCloser = c
		e.activeMu.Unlock()
	}

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-r.Context().Done():
			conn.Close()
			e.activeMu.Lock()
			wc := e.writeCloser
			e.activeMu.Unlock()
			if wc != nil {
				wc.Close()
			}
		case <-stop:
		}
	}()

	var (
		n       int64
		copyErr error
	)
	if e.modality == ModalityAudio {
		mm := newMediaMeter(m.log, m.statsLog, "serve", shortToken(token))
		meterStop := make(chan struct{})
		go runMeterTicker(mm, meterStop)
		func() {
			defer close(meterStop)
			n, copyErr = copyFramesServe(dst, conn, mm)
		}()
		mm.end(time.Now())
	} else {
		bm := newByteMeter(m.log, m.statsLog, "serve", shortToken(token), e.modality)
		meterStop := make(chan struct{})
		go runByteMeterTicker(bm, meterStop)
		func() {
			defer close(meterStop)
			cw := &countingWriter{w: dst, onWrite: bm.onBytes}
			n, copyErr = io.Copy(cw, conn)
		}()
		bm.end(time.Now())
	}

	ended, copyErr := classifyEnded(copyErr)
	e.activeMu.Lock()
	closedByCancel := e.closed
	e.activeMu.Unlock()
	if !closedByCancel && r.Context().Err() != nil {

		ended, copyErr = "peer_closed", nil
	}
	attrs := []any{
		slog.String("token", shortToken(token)),
		slog.String("streamer", streamerAddr),
		slog.Int64("bytes", n),
		slog.String("ended", ended),
	}
	if copyErr != nil {
		attrs = append(attrs, slog.Any("err", copyErr))
	}
	m.log.Info("proxy serve finished", attrs...)
	return true
}

func (m *Manager) Cancel(token string) bool {
	m.mu.Lock()
	e, ok := m.entries[token]
	if !ok {
		m.mu.Unlock()
		return false
	}
	delete(m.entries, token)
	m.mu.Unlock()

	switch e.kind {
	case kindServe:
		e.activeMu.Lock()
		e.closed = true
		if e.active != nil {
			e.active.Close()
			e.active = nil
		}
		wc := e.writeCloser
		e.activeMu.Unlock()

		if wc != nil {
			wc.Close()
		}
	case kindFetch:
		if e.cancel != nil {
			e.cancel()
		}
	}
	m.log.Info("proxy cancelled",
		slog.String("token", shortToken(token)),
		slog.String("kind", e.kind.String()),
	)
	return true
}

func (m *Manager) Has(token string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.entries[token]
	return ok
}

func (m *Manager) FetchingFrom(match func(host string) bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.entries {
		if e.kind == kindFetch && match(e.peerHost) {
			return true
		}
	}
	return false
}

func copyFramesServe(dst io.Writer, src io.Reader, mm *mediaMeter) (int64, error) {
	q := newFrameQueue()
	readErrCh := make(chan error, 1)
	go func() {
		readErrCh <- readFramesServe(q, src, mm)
	}()

	total, writeErr := writeFramesServe(dst, q, mm)

	if writeErr != nil {
		if c, ok := src.(io.Closer); ok {
			c.Close()
		}
	}

	readErr := <-readErrCh

	if writeErr != nil {
		return total, writeErr
	}
	return total, readErr
}

func readFramesServe(q *frameQueue, src io.Reader, mm *mediaMeter) error {
	defer q.close()
	br := bufio.NewReader(src)
	for {
		frame, counter, pts, err := readFrame(br, nil)
		if err != nil {
			var pe *frameParseError
			if errors.As(err, &pe) {
				mm.onParseError(pe.err)
				q.closeRaw(pe.consumed, br)
				return nil
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		now := time.Now()
		mm.onFrameIn(now, len(frame), pts)
		q.push(queuedFrame{b: frame, at: now, counter: counter, pts: pts})
	}
}

var serveWritePopHook func(q *frameQueue, mm *mediaMeter, droppedMs, ageMs int)

func writeFramesServe(dst io.Writer, q *frameQueue, mm *mediaMeter) (int64, error) {
	var total int64
	for {
		batch, droppedMs, ageMs, ok := q.popBatch(time.Now(), staleFrameMaxAge, 0, 1)
		if !ok {

			break
		}
		if serveWritePopHook != nil {
			serveWritePopHook(q, mm, droppedMs, ageMs)
		}
		now := time.Now()
		mm.onPop(now, droppedMs, ageMs, q.depthMs(now))
		for _, f := range batch {
			n, err := timedWrite(dst, f.b, mm)
			total += int64(n)
			if err != nil {
				return total, err
			}
			mm.onFrameOut()
		}
	}
	consumed, rest := q.rawTail()
	if rest == nil {
		return total, nil
	}

	n, err := timedWrite(dst, consumed, mm)
	total += int64(n)
	if err != nil {
		return total, err
	}
	n2, err := io.Copy(dst, rest)
	return total + n2, err
}

func timedWrite(dst io.Writer, p []byte, mm *mediaMeter) (int, error) {
	start := time.Now()
	mm.onWriteStart(start)
	n, err := dst.Write(p)
	mm.onWriteDone(time.Now(), n, time.Since(start))
	return n, err
}

type flushWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func (fw *flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if err == nil {
		fw.f.Flush()
	}
	return n, err
}

func classifyEnded(err error) (ended string, logErr error) {
	switch {
	case err == nil:
		return "eof", nil
	case errors.Is(err, net.ErrClosed), errors.Is(err, context.Canceled):
		return "closed", nil
	default:
		return "error", err
	}
}

func shortToken(t string) string {
	if len(t) < 12 {
		return t
	}
	return t[:8] + "…"
}
