package proxy

import (
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

	cancel context.CancelFunc
	done   chan struct{}
}

type Manager struct {
	log *slog.Logger

	mu      sync.Mutex
	entries map[string]*entry
}

func NewManager(log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		log:     log,
		entries: make(map[string]*entry),
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
	if _, err := url.Parse(peerURL); err != nil {
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

	n, copyErr := io.Copy(conn, resp.Body)
	m.log.Info("proxy fetch finished",
		slog.String("token", shortToken(e.token)),
		slog.String("streamer", streamerAddr),
		slog.Int64("bytes", n),
		slog.Any("err", copyErr),
	)
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

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-r.Context().Done():
			conn.Close()
		case <-stop:
		}
	}()

	dst := io.Writer(w)
	if flusher != nil {
		dst = &flushWriter{w: w, f: flusher}
	}
	n, copyErr := io.Copy(dst, conn)
	m.log.Info("proxy serve finished",
		slog.String("token", shortToken(token)),
		slog.String("streamer", streamerAddr),
		slog.Int64("bytes", n),
		slog.Any("err", copyErr),
	)
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
		e.activeMu.Unlock()
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

func shortToken(t string) string {
	if len(t) < 12 {
		return t
	}
	return t[:8] + "…"
}
