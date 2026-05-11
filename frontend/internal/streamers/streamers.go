package streamers

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

type Side string

const (
	SideMic Side = "mic"
	SideSpk Side = "spk"
)

type Event struct {
	Type    string `json:"type"`
	Reason  string `json:"reason,omitempty"`
	Counter uint64 `json:"counter,omitempty"`
	Bytes   uint64 `json:"bytes,omitempty"`
	Muted   bool   `json:"muted,omitempty"`

	BytesIn       uint64  `json:"bytes_in,omitempty"`
	BytesOut      uint64  `json:"bytes_out,omitempty"`
	FramesIn      uint64  `json:"frames_in,omitempty"`
	FramesOut     uint64  `json:"frames_out,omitempty"`
	FramesDropped uint64  `json:"frames_dropped,omitempty"`
	JitterMs      float64 `json:"jitter_ms,omitempty"`
	CpuPct        float64 `json:"cpu_pct,omitempty"`
}

type Stream struct {
	Side   Side
	CallID string

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	logger *slog.Logger

	mu        sync.Mutex
	events    chan Event
	readyCh   chan struct{}
	readyErr  error
	readyOnce sync.Once

	doneCh   chan struct{}
	waitErr  error
	waitOnce sync.Once

	keyDone bool
}

type Session struct {
	CallID string
	Mic    *Stream
	Spk    *Stream
}

type Manager struct {
	logger      *slog.Logger
	micPath     string
	spkPath     string
	tracingFlag bool

	mu       sync.Mutex
	sessions map[string]*Session
}

type Config struct {
	Logger  *slog.Logger
	MicPath string
	SpkPath string
	Trace   bool
}

func New(cfg Config) (*Manager, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.MicPath == "" {
		return nil, errors.New("streamers: MicPath required")
	}
	if cfg.SpkPath == "" {
		return nil, errors.New("streamers: SpkPath required")
	}
	return &Manager{
		logger:      cfg.Logger,
		micPath:     cfg.MicPath,
		spkPath:     cfg.SpkPath,
		tracingFlag: cfg.Trace,
		sessions:    map[string]*Session{},
	}, nil
}

func (m *Manager) MicPath() string { return m.micPath }
func (m *Manager) SpkPath() string { return m.spkPath }

func (m *Manager) Sessions() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		out = append(out, id)
	}
	return out
}

func (m *Manager) Mic(callID string) *Stream {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sess := m.sessions[callID]; sess != nil {
		return sess.Mic
	}
	return nil
}

func (m *Manager) Spk(callID string) *Stream {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sess := m.sessions[callID]; sess != nil {
		return sess.Spk
	}
	return nil
}

func (m *Manager) SpawnMic(ctx context.Context, callID string, port int, key []byte, streamID string) (*Stream, error) {
	return m.spawn(ctx, callID, SideMic, m.micPath, port, key, streamID)
}

func (m *Manager) SpawnSpk(ctx context.Context, callID string, port int, key []byte, streamID string) (*Stream, error) {
	return m.spawn(ctx, callID, SideSpk, m.spkPath, port, key, streamID)
}

func (m *Manager) spawn(ctx context.Context, callID string, side Side, binPath string, port int, key []byte, streamID string) (*Stream, error) {
	if callID == "" {
		return nil, errors.New("streamers: empty callID")
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("streamers: key must be 32 bytes, got %d", len(key))
	}
	if streamID == "" {
		streamID = "mic"
	}
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("streamers: invalid port %d", port)
	}

	args := []string{
		"--port", fmt.Sprintf("%d", port),
		"--stream-id", streamID,
	}
	if m.tracingFlag {
		args = append(args, "--trace")
	}

	cmd := exec.Command(binPath, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("streamers: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("streamers: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("streamers: stderr pipe: %w", err)
	}

	logger := m.logger.With(
		slog.String("streamer_side", string(side)),
		slog.String("call_id", callID),
		slog.Int("port", port),
		slog.String("bin", binPath),
	)

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("streamers: start %s: %w", binPath, err)
	}
	logger.Debug("streamer spawned", slog.Int("pid", cmd.Process.Pid))

	s := &Stream{
		Side:    side,
		CallID:  callID,
		cmd:     cmd,
		stdin:   stdin,
		logger:  logger,
		events:  make(chan Event, 16),
		readyCh: make(chan struct{}),
		doneCh:  make(chan struct{}),
	}

	go s.readEvents(stdout)

	go s.pumpStderr(stderr)

	go s.reap()

	if _, err := stdin.Write(key); err != nil {

		_ = s.killAndReap(2 * time.Second)
		return nil, fmt.Errorf("streamers: write key: %w", err)
	}
	s.keyDone = true

	m.mu.Lock()
	sess, ok := m.sessions[callID]
	if !ok {
		sess = &Session{CallID: callID}
		m.sessions[callID] = sess
	}
	switch side {
	case SideMic:
		sess.Mic = s
	case SideSpk:
		sess.Spk = s
	}
	m.mu.Unlock()

	return s, nil
}

func (s *Stream) readEvents(stdout io.Reader) {
	defer close(s.events)
	br := bufio.NewReaderSize(stdout, 4096)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			var ev Event
			if jerr := json.Unmarshal(line, &ev); jerr != nil {
				s.logger.Debug("streamer stdout: non-json line ignored",
					slog.String("line", string(line)),
				)
			} else {
				switch ev.Type {
				case "ready":
					s.readyOnce.Do(func() { close(s.readyCh) })
				case "error":
					s.readyOnce.Do(func() {
						s.readyErr = fmt.Errorf("streamer error: %s", ev.Reason)
						close(s.readyCh)
					})
				}
				select {
				case s.events <- ev:
				default:

					s.logger.Debug("streamer event dropped (consumer slow)")
				}
			}
		}
		if err != nil {

			s.readyOnce.Do(func() {
				s.readyErr = errors.New("streamer exited before ready")
				close(s.readyCh)
			})
			return
		}
	}
}

func (s *Stream) pumpStderr(stderr io.Reader) {
	br := bufio.NewReaderSize(stderr, 4096)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			s.logger.Info("streamer stderr",
				slog.String("line", string(trimRight(line))),
			)
		}
		if err != nil {
			return
		}
	}
}

func trimRight(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

func (s *Stream) reap() {
	defer close(s.doneCh)
	s.waitOnce.Do(func() {
		s.waitErr = s.cmd.Wait()
	})
}

func (s *Stream) WaitReady(ctx context.Context) error {
	select {
	case <-s.readyCh:
		return s.readyErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Stream) Events() <-chan Event { return s.events }

func (s *Stream) SendCommand(cmd map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.keyDone {
		return errors.New("streamers: SendCommand before key write")
	}
	raw, err := json.Marshal(cmd)
	if err != nil {
		return fmt.Errorf("streamers: marshal command: %w", err)
	}
	raw = append(raw, '\n')
	if _, err := s.stdin.Write(raw); err != nil {
		return fmt.Errorf("streamers: write command: %w", err)
	}
	return nil
}

func (s *Stream) teardown(ctx context.Context) error {

	select {
	case <-s.doneCh:
		return swallowSignalKill(s.waitErr)
	default:
	}

	if err := s.SendCommand(map[string]any{"cmd": "exit"}); err != nil {
		s.logger.Debug("streamer teardown: exit command failed (likely already gone)",
			slog.Any("err", err),
		)
	}
	if waitDone(s.doneCh, exitGrace) {
		return swallowSignalKill(s.waitErr)
	}

	if s.cmd.Process != nil {
		if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			s.logger.Warn("streamer teardown: SIGTERM failed", slog.Any("err", err))
		}
	}
	if waitDone(s.doneCh, sigtermGrace) {
		return swallowSignalKill(s.waitErr)
	}

	if s.cmd.Process != nil {
		s.logger.Warn("streamer teardown: escalating to SIGKILL")
		if err := s.cmd.Process.Kill(); err != nil && !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("streamers: kill: %w", err)
		}
	}
	select {
	case <-s.doneCh:
		return swallowSignalKill(s.waitErr)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func swallowSignalKill(err error) error {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return err
	}
	if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok {
		if ws.Signaled() {
			switch ws.Signal() {
			case syscall.SIGTERM, syscall.SIGKILL:
				return nil
			}
		}
	}
	return err
}

func (s *Stream) killAndReap(grace time.Duration) error {
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	if waitDone(s.doneCh, grace) {
		return s.waitErr
	}
	return errors.New("streamers: kill timeout")
}

func waitDone(ch <-chan struct{}, d time.Duration) bool {
	select {
	case <-ch:
		return true
	case <-time.After(d):
		return false
	}
}

func (m *Manager) Teardown(callID string) error {
	m.mu.Lock()
	sess, ok := m.sessions[callID]
	if ok {
		delete(m.sessions, callID)
	}
	m.mu.Unlock()
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), totalTeardownBudget)
	defer cancel()
	var errs []error
	if sess.Mic != nil {
		if err := sess.Mic.teardown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("mic: %w", err))
		}
	}
	if sess.Spk != nil {
		if err := sess.Spk.teardown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("spk: %w", err))
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) Shutdown() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		if err := m.Teardown(id); err != nil {
			m.logger.Warn("streamers: shutdown teardown failed",
				slog.String("call_id", id),
				slog.Any("err", err),
			)
		}
	}
}

var exitGrace = 1 * time.Second

var sigtermGrace = 2 * time.Second

var totalTeardownBudget = 6 * time.Second
