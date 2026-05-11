package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

type Session struct {
	conn *websocket.Conn
	ctx  context.Context

	welcomeAugment func(WelcomePayload) WelcomePayload

	ClientName    string
	ClientVersion string

	pushFilter atomic.Pointer[[]string]
}

func (s *Session) Send(f Frame) error {
	b, err := Encode(f)
	if err != nil {
		return err
	}
	return s.conn.Write(s.ctx, websocket.MessageText, b)
}

func (s *Session) Recv() (Frame, error) {
	_, data, err := s.conn.Read(s.ctx)
	if err != nil {
		return Frame{}, err
	}
	return Decode(data)
}

func (s *Session) Context() context.Context { return s.ctx }

func (s *Session) SetPushFilter(topics []string) []string {
	out := make([]string, 0, len(topics))
	for _, t := range topics {
		t = strings.TrimSpace(t)
		if t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		s.pushFilter.Store(nil)
		return nil
	}
	s.pushFilter.Store(&out)
	return out
}

func (s *Session) AcceptsPush(t FrameType) bool {
	p := s.pushFilter.Load()
	if p == nil || len(*p) == 0 {
		return true
	}
	for _, prefix := range *p {
		if strings.HasPrefix(string(t), prefix) {
			return true
		}
	}
	return false
}

func (s *Session) doHandshake(ctx context.Context) error {
	hsCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	_, data, err := s.conn.Read(hsCtx)
	if err != nil {
		return fmt.Errorf("read hello: %w", err)
	}
	f, err := Decode(data)
	if err != nil {
		return fmt.Errorf("decode hello: %w", err)
	}
	if f.Type != FrameHello {
		return fmt.Errorf("expected hello, got %q", f.Type)
	}
	var hp HelloPayload
	if len(f.Payload) > 0 {
		if err := jsonUnmarshalStrict(f.Payload, &hp); err != nil {
			return fmt.Errorf("decode hello payload: %w", err)
		}
	}
	s.ClientName = hp.ClientName
	s.ClientVersion = hp.ClientVersion

	wp := WelcomePayload{
		DaemonVersion:   DaemonVersion,
		ProtocolVersion: ProtocolVersion,
	}
	if s.welcomeAugment != nil {
		wp = s.welcomeAugment(wp)
	}
	welcome, err := NewFrame(FrameWelcome, f.ID, wp)
	if err != nil {
		return fmt.Errorf("build welcome: %w", err)
	}
	if err := s.Send(welcome); err != nil {
		return fmt.Errorf("send welcome: %w", err)
	}
	return nil
}

func (s *Session) keepalive(ctx context.Context) {
	pings := time.NewTicker(PingInterval)
	defer pings.Stop()

	reads := make(chan Frame, 1)
	readErrs := make(chan error, 1)
	go func() {
		for {
			f, err := s.Recv()
			if err != nil {
				readErrs <- err
				return
			}
			select {
			case reads <- f:
			case <-ctx.Done():
				return
			}
		}
	}()

	lastPongID := ""
	for {
		select {
		case <-ctx.Done():
			return
		case err := <-readErrs:
			if errors.Is(err, context.Canceled) {
				return
			}

			return
		case f := <-reads:
			switch f.Type {
			case FramePing:

				pong, _ := NewFrame(FramePong, f.ID, nil)
				_ = s.Send(pong)
			case FramePong:

				lastPongID = f.ID
				_ = lastPongID
			default:

				ep, _ := NewFrame(FrameError, f.ID, ErrorPayload{
					Code:    "unsupported_frame",
					Message: fmt.Sprintf("frame type %q is not handled in this protocol version", f.Type),
				})
				_ = s.Send(ep)
			}
		case <-pings.C:
			ping, _ := NewFrame(FramePing, "", nil)
			if err := s.Send(ping); err != nil {
				return
			}
		}
	}
}

func jsonUnmarshalStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
