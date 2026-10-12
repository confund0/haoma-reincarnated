package xport

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestServer_LogsFromShort(t *testing.T) {
	const onion = "dummybbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbd"
	var buf lockedBuffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	for _, fail := range []bool{false, true} {
		srv := httptest.NewServer(newTestHandler(ReceiverFunc(func(ctx context.Context, e Envelope) error {
			if fail {
				return errors.New("receiver refused")
			}
			return nil
		})))
		c := &Client{HTTP: srv.Client()}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = c.Send(ctx, srv.URL, Envelope{ID: "abc-123", Timestamp: time.Now().Unix(), From: onion, Payload: []byte("x")})
		cancel()
		srv.Close()
	}

	out := buf.String()
	if strings.Contains(out, onion) {
		t.Errorf("the full onion reached the log:\n%s", out)
	}
	for _, msg := range []string{"xport: inbound envelope", "xport: envelope received", "xport: receiver error"} {
		found := false
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, msg) && strings.Contains(line, "from=dummybbb…") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no %q line with the short from:\n%s", msg, out)
		}
	}
}
