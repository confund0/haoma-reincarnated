package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

var DaemonVersion = "dev"

var (
	PingInterval = 15 * time.Second
	PongTimeout  = 30 * time.Second
)

type Server struct {
	token string

	OnSession func(ctx context.Context, s *Session)

	WelcomeAugment func(WelcomePayload) WelcomePayload

	mu       sync.RWMutex
	sessions map[*Session]struct{}
}

func NewServer(token string) *Server {
	return &Server{
		token:    token,
		sessions: map[*Session]struct{}{},
	}
}

func (s *Server) Broadcast(f Frame) {
	s.mu.RLock()
	sessions := make([]*Session, 0, len(s.sessions))
	for sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.mu.RUnlock()
	for _, sess := range sessions {
		if !sess.AcceptsPush(f.Type) {
			continue
		}
		_ = sess.Send(f)
	}
}

func (s *Server) SessionCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.sessions)
}

func (s *Server) register(sess *Session) {
	s.mu.Lock()
	s.sessions[sess] = struct{}{}
	s.mu.Unlock()
}

func (s *Server) unregister(sess *Session) {
	s.mu.Lock()
	delete(s.sessions, sess)
	s.mu.Unlock()
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", s.handleWS)
	mux.HandleFunc("GET /v1/health", s.handleHealth)
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":           "ok",
		"daemon_version":   DaemonVersion,
		"protocol_version": ProtocolVersion,
	})
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if err := s.checkBearer(r); err != nil {
		http.Error(w, "unauthorized: "+err.Error(), http.StatusUnauthorized)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		OriginPatterns:     []string{"*"},
	})
	if err != nil {
		slog.Warn("WS accept failed", slog.Any("err", err))
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	sess := &Session{conn: conn, ctx: ctx, welcomeAugment: s.WelcomeAugment}
	if err := sess.doHandshake(ctx); err != nil {
		slog.Warn("IPC handshake failed", slog.Any("err", err))
		return
	}

	s.register(sess)
	defer s.unregister(sess)

	if s.OnSession != nil {
		s.OnSession(ctx, sess)
		return
	}

	sess.keepalive(ctx)
}

func (s *Server) checkBearer(r *http.Request) error {
	h := r.Header.Get("Authorization")
	if h == "" {
		return errors.New("missing Authorization header")
	}
	if !strings.HasPrefix(h, "Bearer ") {
		return errors.New("expected Bearer scheme")
	}
	return CheckToken(s.token, strings.TrimPrefix(h, "Bearer "))
}
