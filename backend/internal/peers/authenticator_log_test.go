package peers

import (
	"bytes"
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"haoma/internal/xport"
)

type lockedLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedLog) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestHMACVerifier_UnknownSourceLogLineIsShort(t *testing.T) {
	const onion = "dummybbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbd"
	var buf lockedLog
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	r, _ := newTestRegistry(t)
	v := &HMACVerifier{Registry: r}
	srv := httptest.NewServer(xport.NewServer(-1, xport.ReceiverFunc(func(ctx context.Context, e xport.Envelope) error {
		t.Error("an envelope from an unknown source reached the receiver")
		return nil
	}), v, nil))
	defer srv.Close()

	env := xport.Sign(xport.Envelope{ID: "msg-1", Timestamp: time.Now().Unix(), From: onion, Payload: []byte("x")}, make([]byte, 32))
	c := &xport.Client{HTTP: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := c.Send(ctx, srv.URL, env); err == nil {
		t.Fatal("the send from an unknown source was accepted")
	}

	out := buf.String()
	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "xport: inbound verify failed") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("no verify-failure line:\n%s", out)
	}
	if strings.Contains(line, onion) {
		t.Errorf("the full address reached the verify-failure line:\n%s", line)
	}
	if !strings.Contains(line, "unknown source address: dummybbb…") {
		t.Errorf("the error does not name the short address:\n%s", line)
	}
}
