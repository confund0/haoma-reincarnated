package outbox

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"haoma/internal/xport"
)

type undeliveredHook struct {
	mu      sync.Mutex
	dests   []string
	queued  []time.Time
	counted []int
}

func (h *undeliveredHook) fire(dest string, queuedAt time.Time, countedAttempts int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dests = append(h.dests, dest)
	h.queued = append(h.queued, queuedAt)
	h.counted = append(h.counted, countedAttempts)
}

func (h *undeliveredHook) seen() ([]string, []time.Time, []int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.dests...), append([]time.Time(nil), h.queued...), append([]int(nil), h.counted...)
}

func waitForUndelivered(h *undeliveredHook, want int) {
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if d, _, _ := h.seen(); len(d) >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestWorker_OnUndeliveredFiresOnGiveUpWithTheQueueTime(t *testing.T) {
	ms := &mockSender{failWith: errors.New("offline")}
	w, st, bus := newTestWorker(t, ms, nil)
	w.MaxAttempts = 100
	w.DeadLetterAge = 5 * time.Second
	w.Tick = time.Hour
	queuedAt := time.Unix(1_700_000_000, 0)
	now, advance := newMutableClock(queuedAt)
	w.now = now
	h := &undeliveredHook{}
	w.OnUndelivered = h.fire

	ch, cancel := bus.Subscribe(4)
	defer cancel()
	startWorker(t, w)

	mustEnqueue(t, w, "http://a.onion", "undelivered1")
	waitForCalls(t, ms, 1, time.Second)
	if dests, _, _ := h.seen(); len(dests) != 0 {
		t.Fatalf("OnUndelivered fired %d times on a rescheduled send, want 0", len(dests))
	}

	advance(35 * time.Second)
	w.signalDest("http://a.onion")
	expectStatus(t, ch, StateFailed, time.Second)
	waitForUndelivered(h, 1)

	dests, queued, counted := h.seen()
	if len(dests) != 1 {
		t.Fatalf("OnUndelivered fired %d times, want 1", len(dests))
	}
	if dests[0] != "http://a.onion" {
		t.Errorf("dest = %q, want http://a.onion", dests[0])
	}
	if !queued[0].Equal(queuedAt) {
		t.Errorf("queuedAt = %v, want the enqueue time %v (not the give-up time)", queued[0], queuedAt)
	}
	if counted[0] != 2 {
		t.Errorf("countedAttempts = %d, want 2", counted[0])
	}
	row, err := st.Load("undelivered1")
	if err != nil {
		t.Fatal(err)
	}
	if !row.GaveUp {
		t.Error("the persisted row does not say GaveUp")
	}
}

func TestWorker_OnUndeliveredSilentOnSent(t *testing.T) {
	ms := &mockSender{}
	w, _, bus := newTestWorker(t, ms, nil)
	w.Tick = time.Hour
	h := &undeliveredHook{}
	w.OnUndelivered = h.fire

	ch, cancel := bus.Subscribe(4)
	defer cancel()
	startWorker(t, w)

	mustEnqueue(t, w, "http://a.onion", "delivered1")
	expectStatus(t, ch, StateSent, time.Second)
	time.Sleep(50 * time.Millisecond)
	if dests, _, _ := h.seen(); len(dests) != 0 {
		t.Errorf("OnUndelivered fired %d times on a sent row, want 0", len(dests))
	}
}

func TestWorker_OnUndeliveredSilentOn401(t *testing.T) {
	ms := &mockSender{failWith: &xport.PeerHTTPError{StatusCode: http.StatusUnauthorized, Body: "bad key"}}
	w, st, bus := newTestWorker(t, ms, nil)
	w.Tick = time.Hour
	h := &undeliveredHook{}
	w.OnUndelivered = h.fire

	ch, cancel := bus.Subscribe(4)
	defer cancel()
	startWorker(t, w)

	mustEnqueue(t, w, "http://a.onion", "u401")
	expectStatus(t, ch, StateFailed, time.Second)
	time.Sleep(50 * time.Millisecond)
	if dests, _, _ := h.seen(); len(dests) != 0 {
		t.Errorf("OnUndelivered fired %d times on a 401, want 0", len(dests))
	}
	if row, err := st.Load("u401"); err != nil || row.GaveUp {
		t.Errorf("a 401 row is marked GaveUp (err %v)", err)
	}
}

func TestWorker_OnUndeliveredSilentOnBadAcks(t *testing.T) {
	ms := &mockSender{body: []byte(`{"id":"a","kind":"sent_ack"}`)}
	ackV := AckVerifierFunc(func(_ context.Context, _ []byte, _ string) error {
		return errors.New("bad mac")
	})
	w, st, bus := newTestWorker(t, ms, ackV)
	w.AckVerifyMaxTries = 2
	w.Tick = time.Hour
	now, advance := newMutableClock(time.Unix(1_700_000_000, 0))
	w.now = now
	h := &undeliveredHook{}
	w.OnUndelivered = h.fire

	ch, cancel := bus.Subscribe(8)
	defer cancel()
	startWorker(t, w)

	mustEnqueue(t, w, "http://a.onion", "ubadack")
	waitForCalls(t, ms, 1, time.Second)
	advance(35 * time.Second)
	w.signalDest("http://a.onion")
	expectStatus(t, ch, StateFailed, time.Second)
	time.Sleep(50 * time.Millisecond)
	if dests, _, _ := h.seen(); len(dests) != 0 {
		t.Errorf("OnUndelivered fired %d times on bad acks, want 0", len(dests))
	}
	if row, err := st.Load("ubadack"); err != nil || row.GaveUp {
		t.Errorf("a bad-ack row is marked GaveUp (err %v)", err)
	}
}
