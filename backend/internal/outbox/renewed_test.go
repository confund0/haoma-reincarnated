package outbox

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"haoma/internal/xport"
)

type cutSender struct {
	mu    sync.Mutex
	at    []time.Time
	left  []time.Duration
	calls int
}

func (c *cutSender) Send(ctx context.Context, _ string, _ xport.Envelope) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.at = append(c.at, time.Now())
	if dl, ok := ctx.Deadline(); ok {
		c.left = append(c.left, time.Until(dl))
	}
	if c.calls == 1 {
		return nil, fmt.Errorf("xport: post: %w", xport.ErrCircuitRenewed)
	}
	return nil, nil
}

func TestWorker_CircuitRenewedSendsAgainAtOnce(t *testing.T) {
	cs := &cutSender{}
	w, st, bus := newTestWorker(t, cs, nil)
	w.Tick = time.Hour
	w.Backoff = func(int) time.Duration { return time.Hour }
	ch, cancel := bus.Subscribe(4)
	defer cancel()
	startWorker(t, w)

	mustEnqueue(t, w, "http://a.onion", "cut")
	expectStatus(t, ch, StateSent, 2*time.Second)

	cs.mu.Lock()
	calls, at, left := cs.calls, cs.at, cs.left
	cs.mu.Unlock()
	if calls != 2 {
		t.Fatalf("sends = %d, want 2: the cut one and the one at once", calls)
	}
	if gap := at[1].Sub(at[0]); gap > time.Second {
		t.Fatalf("the second send came %v after the first, want at once", gap)
	}
	if len(left) != 2 || left[1] < w.SendTimeout-time.Second {
		t.Fatalf("time left for the second send %v, want the whole SendTimeout %v", left, w.SendTimeout)
	}
	row, err := st.Load("cut")
	if err != nil {
		t.Fatal(err)
	}
	if row.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1: the cut costs no attempt", row.Attempts)
	}
}
