package outbox

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"haoma/internal/store"
	"haoma/internal/xport"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := store.Unlock(t.TempDir(), "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Lock() })
	return NewStore(s)
}

func newTestWorker(t *testing.T, sender Sender, ackV AckVerifier) (*Worker, *Store, *Bus) {
	t.Helper()
	s, err := store.Unlock(t.TempDir(), "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Lock() })
	st := NewStore(s)
	bus := &Bus{}
	w := NewWorker(st, sender, ackV, bus)
	w.Backoff = func(attempts int) time.Duration { return 30 * time.Second }
	return w, st, bus
}

func env(id string) xport.Envelope {
	return xport.Envelope{ID: id, Timestamp: 1, Payload: []byte("p")}
}

func mustEnqueue(t *testing.T, w *Worker, dest, id string) {
	t.Helper()
	if err := w.Enqueue(dest, env(id)); err != nil {
		t.Fatalf("Enqueue(%q): %v", id, err)
	}
}

type mockSender struct {
	mu       sync.Mutex
	calls    int
	body     []byte
	failWith error
	failN    int
}

func (m *mockSender) Send(_ context.Context, _ string, _ xport.Envelope) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.failWith != nil {
		return nil, m.failWith
	}
	if m.calls <= m.failN {
		return nil, errors.New("mock: peer offline")
	}
	return m.body, nil
}

func (m *mockSender) setFailWith(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failWith = err
}

func (m *mockSender) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func startWorker(t *testing.T, w *Worker) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run did not exit within 5s of cancel")
		}
	})
}

func waitForCalls(t *testing.T, ms *mockSender, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ms.callCount() >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout: sender calls = %d, want ≥ %d", ms.callCount(), want)
}

func newMutableClock(start time.Time) (now func() time.Time, advance func(time.Duration)) {
	var n atomic.Int64
	n.Store(start.UnixNano())
	now = func() time.Time { return time.Unix(0, n.Load()) }
	advance = func(d time.Duration) { n.Add(int64(d)) }
	return
}

func expectStatus(t *testing.T, ch <-chan DeliveryStatus, want string, timeout time.Duration) DeliveryStatus {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case ds := <-ch:
			if ds.State == want {
				return ds
			}
		case <-time.After(time.Until(deadline)):
			t.Fatalf("timeout waiting for state=%q", want)
			return DeliveryStatus{}
		}
	}
	t.Fatalf("timeout waiting for state=%q", want)
	return DeliveryStatus{}
}

func TestStore_Enqueue_Load(t *testing.T) {
	st := newTestStore(t)
	now := time.Unix(1_700_000_000, 0)
	row, err := st.Enqueue("http://a.onion", env("x"), now)
	if err != nil {
		t.Fatal(err)
	}
	if row.State != StateEnqueued {
		t.Errorf("State = %q, want %q", row.State, StateEnqueued)
	}
	if row.NextAttemptAt != now.UnixNano() {
		t.Errorf("NextAttemptAt = %d, want %d", row.NextAttemptAt, now.UnixNano())
	}

	loaded, err := st.Load("x")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.EnvelopeID != "x" || loaded.Dest != "http://a.onion" {
		t.Errorf("loaded mismatch: %+v", loaded)
	}
}

func TestStore_Enqueue_Duplicate(t *testing.T) {
	st := newTestStore(t)
	now := time.Now()
	if _, err := st.Enqueue("http://a.onion", env("dup"), now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Enqueue("http://a.onion", env("dup"), now); !errors.Is(err, ErrDuplicate) {
		t.Errorf("second enqueue = %v, want ErrDuplicate", err)
	}
}

func TestStore_Enqueue_RejectsEmptyID(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.Enqueue("http://a.onion", xport.Envelope{}, time.Now()); err == nil {
		t.Fatal("expected error for empty ID")
	}
}

func TestStore_Advance_ToSent(t *testing.T) {
	st := newTestStore(t)
	base := time.Unix(1_700_000_000, 0)
	row, _ := st.Enqueue("http://a.onion", env("s1"), base)

	if err := st.Advance(row, StateSent, time.Time{}, 1, 0, 0, "", base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	loaded, err := st.Load("s1")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != StateSent {
		t.Errorf("State = %q, want sent", loaded.State)
	}
	if loaded.NextAttemptAt != 0 {
		t.Errorf("NextAttemptAt = %d, want 0 (terminal)", loaded.NextAttemptAt)
	}

	due, err := st.ListDue(base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Errorf("ListDue after sent = %d rows, want 0", len(due))
	}
}

func TestStore_Advance_Reschedule(t *testing.T) {
	st := newTestStore(t)
	base := time.Unix(1_700_000_000, 0)
	row, _ := st.Enqueue("http://a.onion", env("r1"), base)

	nextAt := base.Add(30 * time.Second)
	if err := st.Advance(row, StateEnqueued, nextAt, 1, 0, 0, "offline", base); err != nil {
		t.Fatal(err)
	}

	due, _ := st.ListDue(base.Add(25 * time.Second))
	if len(due) != 0 {
		t.Errorf("ListDue at t+25s = %d, want 0", len(due))
	}

	due, _ = st.ListDue(base.Add(35 * time.Second))
	if len(due) != 1 {
		t.Errorf("ListDue at t+35s = %d, want 1", len(due))
	}
}

func TestStore_ListByState(t *testing.T) {
	st := newTestStore(t)
	base := time.Unix(1_700_000_000, 0)

	r1, _ := st.Enqueue("http://a.onion", env("e1"), base)
	r2, _ := st.Enqueue("http://b.onion", env("e2"), base)

	_ = st.Advance(r1, StateSent, time.Time{}, 1, 0, 0, "", base.Add(time.Second))

	_ = r2

	sent, err := st.ListByState(StateSent, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || sent[0].EnvelopeID != "e1" {
		t.Errorf("ListByState(sent) = %v, want [e1]", sent)
	}

	queued, err := st.ListByState(StateEnqueued, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 || queued[0].EnvelopeID != "e2" {
		t.Errorf("ListByState(enqueued) = %v, want [e2]", queued)
	}
}

func TestStore_ListByState_SinceCursor(t *testing.T) {
	st := newTestStore(t)
	base := time.Unix(1_700_000_000, 0)

	r1, _ := st.Enqueue("http://a.onion", env("old"), base)
	_ = st.Advance(r1, StateSent, time.Time{}, 1, 0, 0, "", base.Add(time.Second))

	r2, _ := st.Enqueue("http://b.onion", env("new"), base)
	laterTime := base.Add(10 * time.Second)
	_ = st.Advance(r2, StateSent, time.Time{}, 1, 0, 0, "", laterTime)

	all, _ := st.ListByState(StateSent, base.Add(time.Second).UnixNano(), 100)
	if len(all) != 2 {
		t.Errorf("since=old: got %d, want 2", len(all))
	}

	fresh, _ := st.ListByState(StateSent, base.Add(5*time.Second).UnixNano(), 100)
	if len(fresh) != 1 || fresh[0].EnvelopeID != "new" {
		t.Errorf("since=mid: got %v, want [new]", fresh)
	}
}

func TestStore_KickByDests(t *testing.T) {
	st := newTestStore(t)
	base := time.Unix(1_700_000_000, 0)

	r, _ := st.Enqueue("http://a.onion", env("k1"), base)

	futureAt := base.Add(5 * time.Minute)
	_ = st.Advance(r, StateEnqueued, futureAt, 1, 0, 0, "offline", base)

	now := base.Add(10 * time.Second)
	n, err := st.KickByDests([]string{"http://a.onion"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("KickByDests = %d, want 1", n)
	}

	due, _ := st.ListDue(now)
	if len(due) != 1 {
		t.Errorf("after kick ListDue = %d, want 1", len(due))
	}
}

func TestStore_KickByDests_AlreadyDue_NoOp(t *testing.T) {
	st := newTestStore(t)
	now := time.Now()
	_, _ = st.Enqueue("http://a.onion", env("alreadydue"), now)

	n, err := st.KickByDests([]string{"http://a.onion"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("KickByDests on already-due = %d, want 0", n)
	}
}

func TestStore_KickAll(t *testing.T) {
	st := newTestStore(t)
	base := time.Unix(1_700_000_000, 0)
	future := base.Add(5 * time.Minute)

	a1, _ := st.Enqueue("http://a.onion", env("ka1"), base)
	_ = st.Advance(a1, StateEnqueued, future, 1, 0, 0, "offline", base)
	a2, _ := st.Enqueue("http://a.onion", env("ka2"), base)
	_ = st.Advance(a2, StateEnqueued, future, 1, 0, 0, "offline", base)
	b1, _ := st.Enqueue("http://b.onion", env("kb1"), base)
	_ = st.Advance(b1, StateEnqueued, future, 2, 0, 0, "offline", base)
	now := base.Add(10 * time.Second)

	_, _ = st.Enqueue("http://c.onion", env("kc1"), now)

	n, dests, err := st.KickAll(nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("KickAll moved %d rows, want 3", n)
	}
	sort.Strings(dests)
	if want := []string{"http://a.onion", "http://b.onion"}; !reflect.DeepEqual(dests, want) {
		t.Errorf("KickAll destinations = %v, want %v", dests, want)
	}
	for _, id := range []string{a1.EnvelopeID, a2.EnvelopeID, b1.EnvelopeID} {
		row, err := st.Load(id)
		if err != nil {
			t.Fatal(err)
		}
		if row.NextAttemptAt != now.UnixNano() || !row.Kicked {
			t.Errorf("row %s: next=%d kicked=%v, want next=%d kicked=true", id, row.NextAttemptAt, row.Kicked, now.UnixNano())
		}
	}
	due, _ := st.ListDue(now)
	if len(due) != 4 {
		t.Errorf("after KickAll ListDue = %d, want 4", len(due))
	}

	n, dests, err = st.KickAll(nil, now)
	if err != nil || n != 0 || len(dests) != 0 {
		t.Errorf("second KickAll = %d %v %v, want 0 [] nil", n, dests, err)
	}
}

func TestStore_KickAll_RowDueBeforeTheWrite(t *testing.T) {
	st := newTestStore(t)
	base := time.Unix(1_700_000_000, 0)
	now := base.Add(10 * time.Second)
	r, _ := st.Enqueue("http://a.onion", env("kr1"), base)
	_ = st.Advance(r, StateEnqueued, base.Add(5*time.Minute), 1, 0, 0, "offline", base)

	kickBetweenReadAndWrite = func() {
		row, err := st.Load(r.EnvelopeID)
		if err != nil {
			t.Error(err)
			return
		}

		if err := st.Advance(row, StateEnqueued, base, 1, 0, 0, "offline", base); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { kickBetweenReadAndWrite = nil })

	n, dests, err := st.KickAll(nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 || len(dests) != 0 {
		t.Errorf("KickAll = %d %v, want 0 rows: the row turned due before the write", n, dests)
	}
	row, _ := st.Load(r.EnvelopeID)
	if row.Kicked || row.NextAttemptAt != base.UnixNano() {
		t.Errorf("row rewritten: kicked=%v next=%d, want kicked=false next=%d", row.Kicked, row.NextAttemptAt, base.UnixNano())
	}
}

func TestStore_GC_TerminalRows(t *testing.T) {
	st := newTestStore(t)
	base := time.Unix(1_700_000_000, 0)

	r1, _ := st.Enqueue("http://a.onion", env("g1"), base)
	_ = st.Advance(r1, StateSent, time.Time{}, 1, 0, 0, "", base)

	r2, _ := st.Enqueue("http://a.onion", env("g2"), base)
	_ = st.Advance(r2, StateFailed, time.Time{}, 12, 0, 0, "dead-letter", base)

	_, _ = st.Enqueue("http://a.onion", env("g3"), base)

	n, err := st.GC(base.Add(6 * 24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("early GC removed %d, want 0", n)
	}

	n, err = st.GC(base.Add(8 * 24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("GC at 8d removed %d, want 1 (g1)", n)
	}
	if _, err := st.Load("g1"); err == nil {
		t.Error("g1 should be gone after GC")
	}

	n, err = st.GC(base.Add(31 * 24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("GC at 31d removed %d, want 1 (g2)", n)
	}

	if _, err := st.Load("g3"); err != nil {
		t.Errorf("enqueued row g3 removed by GC: %v", err)
	}
}

func TestBus_FanOut(t *testing.T) {
	b := &Bus{}
	ch1, cancel1 := b.Subscribe(8)
	ch2, cancel2 := b.Subscribe(8)
	defer cancel1()
	defer cancel2()

	b.publish(DeliveryStatus{EnvelopeID: "x", State: StateSent})

	for _, ch := range []<-chan DeliveryStatus{ch1, ch2} {
		select {
		case ds := <-ch:
			if ds.EnvelopeID != "x" || ds.State != StateSent {
				t.Errorf("unexpected ds: %+v", ds)
			}
		case <-time.After(100 * time.Millisecond):
			t.Error("timed out waiting for delivery status")
		}
	}
}

func TestBus_DropOnFull(t *testing.T) {
	b := &Bus{}
	ch, cancel := b.Subscribe(0)
	defer cancel()

	b.publish(DeliveryStatus{EnvelopeID: "x"})
	select {
	case <-ch:
		t.Error("expected drop on full channel")
	default:
	}
}

func TestBus_Cancel_RemovesSubscriber(t *testing.T) {
	b := &Bus{}
	_, cancel := b.Subscribe(4)
	cancel()

	b.mu.Lock()
	n := len(b.subs)
	b.mu.Unlock()
	if n != 0 {
		t.Errorf("after cancel subs len = %d, want 0", n)
	}
}

func TestWorker_Enqueue_Sent_EmptyBody(t *testing.T) {
	ms := &mockSender{body: nil}
	w, _, bus := newTestWorker(t, ms, nil)
	base := time.Unix(1_700_000_000, 0)
	w.now = func() time.Time { return base }
	w.Tick = time.Hour

	ch, cancel := bus.Subscribe(4)
	defer cancel()
	startWorker(t, w)

	mustEnqueue(t, w, "http://a.onion", "sent1")
	expectStatus(t, ch, StateSent, time.Second)

	if ms.callCount() != 1 {
		t.Errorf("calls = %d, want 1", ms.callCount())
	}
	row, err := w.store.Load("sent1")
	if err != nil {
		t.Fatal(err)
	}
	if row.State != StateSent {
		t.Errorf("persisted state = %q, want sent", row.State)
	}
}

func TestWorker_Enqueue_Sent_ValidAck(t *testing.T) {
	ms := &mockSender{body: []byte(`{"id":"ack1","kind":"sent_ack"}`)}
	ackV := AckVerifierFunc(func(_ context.Context, body []byte, _ string) error {
		return nil
	})
	w, _, bus := newTestWorker(t, ms, ackV)
	w.Tick = time.Hour

	ch, cancel := bus.Subscribe(4)
	defer cancel()
	startWorker(t, w)

	mustEnqueue(t, w, "http://a.onion", "ack-ok")
	expectStatus(t, ch, StateSent, time.Second)
}

func TestWorker_AckVerify_FailThenTerminal(t *testing.T) {
	ms := &mockSender{body: []byte(`{"id":"a","kind":"sent_ack"}`)}
	ackV := AckVerifierFunc(func(_ context.Context, _ []byte, _ string) error {
		return errors.New("bad mac")
	})
	w, _, bus := newTestWorker(t, ms, ackV)
	w.AckVerifyMaxTries = 3
	w.Tick = time.Hour
	now, advance := newMutableClock(time.Unix(1_700_000_000, 0))
	w.now = now

	ch, cancel := bus.Subscribe(8)
	defer cancel()
	startWorker(t, w)

	mustEnqueue(t, w, "http://a.onion", "badack")

	waitForCalls(t, ms, 1, time.Second)

	advance(35 * time.Second)
	w.signalDest("http://a.onion")
	waitForCalls(t, ms, 2, time.Second)
	select {
	case ds := <-ch:
		t.Fatalf("unexpected early event: %+v", ds)
	case <-time.After(50 * time.Millisecond):
	}

	advance(35 * time.Second)
	w.signalDest("http://a.onion")
	ds := expectStatus(t, ch, StateFailed, time.Second)
	if ds.LastError == "" {
		t.Error("LastError should be set on ack failure")
	}
}

func TestWorker_Send401_TerminalImmediate(t *testing.T) {
	ms := &mockSender{failWith: &xport.PeerHTTPError{StatusCode: http.StatusUnauthorized, Body: "bad key"}}
	w, _, bus := newTestWorker(t, ms, nil)
	w.Tick = time.Hour

	ch, cancel := bus.Subscribe(4)
	defer cancel()
	startWorker(t, w)

	mustEnqueue(t, w, "http://a.onion", "401case")
	expectStatus(t, ch, StateFailed, time.Second)
	if ms.callCount() != 1 {
		t.Errorf("calls = %d, want 1 (no retries on 401)", ms.callCount())
	}
}

func TestWorker_OnDialFailure_FiresOnTransport_SkipsOnPeerHTTP(t *testing.T) {

	ms := &mockSender{failWith: errors.New("offline")}
	w, _, _ := newTestWorker(t, ms, nil)
	w.Tick = time.Hour
	var (
		mu             sync.Mutex
		transportCalls []string
	)
	w.OnDialFailure = func(dest string) {
		mu.Lock()
		defer mu.Unlock()
		transportCalls = append(transportCalls, dest)
	}
	startWorker(t, w)
	mustEnqueue(t, w, "http://a.onion", "transient")
	waitForCalls(t, ms, 1, time.Second)

	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	if len(transportCalls) != 1 || transportCalls[0] != "http://a.onion" {
		t.Errorf("transport callback fired %v, want [http://a.onion]", transportCalls)
	}
	mu.Unlock()

	ms2 := &mockSender{failWith: &xport.PeerHTTPError{StatusCode: http.StatusInternalServerError, Body: "boom"}}
	w2, _, _ := newTestWorker(t, ms2, nil)
	w2.Tick = time.Hour
	var peerCalls []string
	w2.OnDialFailure = func(dest string) {
		mu.Lock()
		defer mu.Unlock()
		peerCalls = append(peerCalls, dest)
	}
	startWorker(t, w2)
	mustEnqueue(t, w2, "http://b.onion", "peer500")
	waitForCalls(t, ms2, 1, time.Second)
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	if len(peerCalls) != 0 {
		t.Errorf("peer-http callback fired %v, want empty (peer was reached)", peerCalls)
	}
	mu.Unlock()
}

func TestWorker_TransientFailure_Reschedule(t *testing.T) {
	ms := &mockSender{failWith: errors.New("offline")}
	w, _, _ := newTestWorker(t, ms, nil)
	w.Tick = time.Hour
	now, _ := newMutableClock(time.Unix(1_700_000_000, 0))
	w.now = now
	startWorker(t, w)

	mustEnqueue(t, w, "http://a.onion", "transient")
	waitForCalls(t, ms, 1, time.Second)

	w.signalDest("http://a.onion")
	time.Sleep(50 * time.Millisecond)
	if ms.callCount() != 1 {
		t.Errorf("calls after re-signal (still inside backoff) = %d, want 1", ms.callCount())
	}

	row, _ := w.store.Load("transient")
	if row.State != StateEnqueued {
		t.Errorf("state = %q, want enqueued", row.State)
	}
}

func TestWorker_DeadLetter_MaxAttempts(t *testing.T) {
	ms := &mockSender{failWith: errors.New("offline")}
	w, _, bus := newTestWorker(t, ms, nil)
	w.MaxAttempts = 3
	w.Tick = time.Hour
	now, advance := newMutableClock(time.Unix(1_700_000_000, 0))
	w.now = now

	ch, cancel := bus.Subscribe(4)
	defer cancel()
	startWorker(t, w)

	mustEnqueue(t, w, "http://a.onion", "deadletter")
	waitForCalls(t, ms, 1, time.Second)

	for i := 2; i <= w.MaxAttempts; i++ {
		advance(35 * time.Second)
		w.signalDest("http://a.onion")
		waitForCalls(t, ms, i, time.Second)
	}
	expectStatus(t, ch, StateFailed, time.Second)
}

func TestWorker_DeadLetter_Age(t *testing.T) {
	ms := &mockSender{failWith: errors.New("offline")}
	w, _, bus := newTestWorker(t, ms, nil)
	w.MaxAttempts = 100
	w.DeadLetterAge = 5 * time.Second
	w.Tick = time.Hour
	now, advance := newMutableClock(time.Unix(1_700_000_000, 0))
	w.now = now

	ch, cancel := bus.Subscribe(4)
	defer cancel()
	startWorker(t, w)

	mustEnqueue(t, w, "http://a.onion", "agekill")
	waitForCalls(t, ms, 1, time.Second)

	advance(35 * time.Second)
	w.signalDest("http://a.onion")
	expectStatus(t, ch, StateFailed, time.Second)
}

func TestWorker_KickByDests(t *testing.T) {
	ms := &mockSender{failWith: errors.New("offline")}
	w, _, _ := newTestWorker(t, ms, nil)
	w.Tick = time.Hour
	now, _ := newMutableClock(time.Unix(1_700_000_000, 0))
	w.now = now
	startWorker(t, w)

	mustEnqueue(t, w, "http://a.onion", "kick1")
	mustEnqueue(t, w, "http://b.onion", "kick2")

	waitForCalls(t, ms, 2, time.Second)

	n, err := w.KickByDests([]string{"http://a.onion"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("KickByDests = %d, want 1", n)
	}

	ms.setFailWith(nil)
	waitForCalls(t, ms, 3, time.Second)

	time.Sleep(30 * time.Millisecond)
	if ms.callCount() != 3 {
		t.Errorf("post-kick calls = %d, want 3 (a only, b still in backoff)", ms.callCount())
	}
}

func TestWorker_KickAll(t *testing.T) {
	ms := &mockSender{failWith: errors.New("offline")}
	w, _, _ := newTestWorker(t, ms, nil)
	w.Tick = time.Hour
	now, _ := newMutableClock(time.Unix(1_700_000_000, 0))
	w.now = now
	startWorker(t, w)

	mustEnqueue(t, w, "http://a.onion", "kall1")
	mustEnqueue(t, w, "http://b.onion", "kall2")
	waitForCalls(t, ms, 2, time.Second)

	ms.setFailWith(nil)
	rows, dests, err := w.KickAll(nil)
	if err != nil {
		t.Fatal(err)
	}
	if rows != 2 || dests != 2 {
		t.Errorf("KickAll = %d rows, %d destinations, want 2, 2", rows, dests)
	}
	waitForCalls(t, ms, 4, time.Second)
}

func TestWorker_Run_ExitsOnContextCancel(t *testing.T) {
	ms := &mockSender{}
	w, _, _ := newTestWorker(t, ms, nil)
	w.Tick = 50 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	time.Sleep(80 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run returned %v, want Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit")
	}
}

func TestWorker_IdempotentRestart(t *testing.T) {

	s, err := store.Unlock(t.TempDir(), "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Lock() })
	st := NewStore(s)

	now := time.Now()
	if _, err := st.Enqueue("http://a.onion", env("persist"), now); err != nil {
		t.Fatal(err)
	}

	ms := &mockSender{}
	bus := &Bus{}
	w := NewWorker(st, ms, nil, bus)
	due, err := w.store.ListDue(now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].EnvelopeID != "persist" {
		t.Errorf("persisted row not found after worker restart: %v", due)
	}
}

func TestWorker_Gate_Closed_NoSend(t *testing.T) {
	ms := &mockSender{body: nil}
	w, _, _ := newTestWorker(t, ms, nil)
	w.Tick = time.Hour
	w.Gate = func() bool { return false }
	startWorker(t, w)

	mustEnqueue(t, w, "http://a.onion", "gated")

	time.Sleep(100 * time.Millisecond)
	if ms.callCount() != 0 {
		t.Errorf("sender called %d times with gate closed, want 0", ms.callCount())
	}
	row, err := w.store.Load("gated")
	if err != nil {
		t.Fatal(err)
	}
	if row.State != StateEnqueued {
		t.Errorf("state = %q, want enqueued", row.State)
	}
}

func TestWorker_Gate_Open_Sends(t *testing.T) {
	ms := &mockSender{body: nil}
	w, _, bus := newTestWorker(t, ms, nil)
	w.Tick = time.Hour
	w.Gate = func() bool { return true }

	ch, cancel := bus.Subscribe(4)
	defer cancel()
	startWorker(t, w)

	mustEnqueue(t, w, "http://a.onion", "open")
	expectStatus(t, ch, StateSent, time.Second)
}

func TestWorker_GateOpening_WakesEveryDestination(t *testing.T) {
	for _, tc := range []struct {
		name  string
		watch time.Duration
		want  int
	}{
		{"watched", 10 * time.Millisecond, 2},

		{"not watched", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ms := &mockSender{body: nil}
			w, _, _ := newTestWorker(t, ms, nil)
			w.Tick = time.Hour
			w.GateWatch = tc.watch
			var open atomic.Bool
			w.Gate = open.Load
			startWorker(t, w)

			mustEnqueue(t, w, "http://a.onion", "a1")
			mustEnqueue(t, w, "http://b.onion", "b1")
			time.Sleep(50 * time.Millisecond)
			if ms.callCount() != 0 {
				t.Fatalf("sent %d with the gate closed", ms.callCount())
			}
			for _, id := range []string{"a1", "b1"} {
				row, err := w.store.Load(id)
				if err != nil {
					t.Fatal(err)
				}
				if row.Attempts != 0 || row.State != StateEnqueued {
					t.Fatalf("%s: attempts %d state %q before the gate opened", id, row.Attempts, row.State)
				}
			}

			open.Store(true)
			time.Sleep(300 * time.Millisecond)
			if got := ms.callCount(); got != tc.want {
				t.Fatalf("sends after the gate opened = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestWorker_GateWatchDefault(t *testing.T) {
	w, _, _ := newTestWorker(t, &mockSender{}, nil)
	if w.GateWatch <= 0 || w.GateWatch > 2*time.Second {
		t.Fatalf("GateWatch = %s, want a watch of at most 2s", w.GateWatch)
	}
}

func TestWorker_Gate_NilAlwaysDrains(t *testing.T) {
	ms := &mockSender{body: nil}
	w, _, bus := newTestWorker(t, ms, nil)
	w.Tick = time.Hour

	ch, cancel := bus.Subscribe(4)
	defer cancel()
	startWorker(t, w)

	mustEnqueue(t, w, "http://a.onion", "nogate")
	expectStatus(t, ch, StateSent, time.Second)
}

type perDestSender struct {
	mu         sync.Mutex
	blockers   map[string]chan struct{}
	delay      time.Duration
	log        []string
	inFlight   map[string]int
	maxPerDest int
}

func newPerDestSender() *perDestSender {
	return &perDestSender{
		blockers: map[string]chan struct{}{},
		inFlight: map[string]int{},
	}
}

func (p *perDestSender) Send(ctx context.Context, dest string, env xport.Envelope) ([]byte, error) {
	p.mu.Lock()
	p.inFlight[dest]++
	if p.inFlight[dest] > p.maxPerDest {
		p.maxPerDest = p.inFlight[dest]
	}
	blocker := p.blockers[dest]
	delay := p.delay
	p.mu.Unlock()

	if blocker != nil {
		select {
		case <-blocker:
		case <-ctx.Done():
			p.mu.Lock()
			p.inFlight[dest]--
			p.mu.Unlock()
			return nil, ctx.Err()
		}
	}
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			p.mu.Lock()
			p.inFlight[dest]--
			p.mu.Unlock()
			return nil, ctx.Err()
		}
	}

	p.mu.Lock()
	p.inFlight[dest]--
	p.log = append(p.log, dest+":"+env.ID)
	p.mu.Unlock()
	return nil, nil
}

func (p *perDestSender) snapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.log))
	copy(out, p.log)
	return out
}

func TestWorker_SlowDestDoesNotBlockFast(t *testing.T) {
	ps := newPerDestSender()
	slowGate := make(chan struct{})
	ps.blockers["http://slow.onion"] = slowGate

	w, _, _ := newTestWorker(t, ps, nil)
	w.Tick = time.Hour
	startWorker(t, w)

	mustEnqueue(t, w, "http://slow.onion", "slow1")

	time.Sleep(20 * time.Millisecond)
	mustEnqueue(t, w, "http://fast.onion", "fast1")

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		log := ps.snapshot()
		for _, entry := range log {
			if entry == "http://fast.onion:fast1" {
				close(slowGate)
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(slowGate)
	t.Fatalf("fast Dest did not deliver while slow was blocked; log=%v", ps.snapshot())
}

func TestWorker_PerDestSerial(t *testing.T) {
	ps := newPerDestSender()
	ps.delay = 30 * time.Millisecond

	w, _, bus := newTestWorker(t, ps, nil)
	w.Tick = time.Hour

	ch, cancel := bus.Subscribe(8)
	defer cancel()
	startWorker(t, w)

	mustEnqueue(t, w, "http://a.onion", "a-msg-1")
	mustEnqueue(t, w, "http://a.onion", "a-msg-2")
	mustEnqueue(t, w, "http://a.onion", "a-msg-3")

	for i := 0; i < 3; i++ {
		expectStatus(t, ch, StateSent, time.Second)
	}

	if ps.maxPerDest != 1 {
		t.Errorf("max in-flight per Dest = %d, want 1 (per-Dest must be serial)", ps.maxPerDest)
	}
	want := []string{
		"http://a.onion:a-msg-1",
		"http://a.onion:a-msg-2",
		"http://a.onion:a-msg-3",
	}
	got := ps.snapshot()
	if fmt.Sprintf("%v", got) != fmt.Sprintf("%v", want) {
		t.Errorf("delivery order: got %v, want %v", got, want)
	}
}

func TestWorker_MultipleDestsConcurrent(t *testing.T) {
	ps := newPerDestSender()
	gate := make(chan struct{})
	dests := []string{"http://d1.onion", "http://d2.onion", "http://d3.onion", "http://d4.onion"}
	for _, d := range dests {
		ps.blockers[d] = gate
	}

	w, _, _ := newTestWorker(t, ps, nil)
	w.Tick = time.Hour
	startWorker(t, w)

	for i, d := range dests {
		mustEnqueue(t, w, d, fmt.Sprintf("id-%d", i))
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		ps.mu.Lock()
		total := 0
		for _, n := range ps.inFlight {
			total += n
		}
		ps.mu.Unlock()
		if total == len(dests) {
			close(gate)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(gate)
	ps.mu.Lock()
	total := 0
	for _, n := range ps.inFlight {
		total += n
	}
	ps.mu.Unlock()
	t.Fatalf("peak concurrent Sends = %d, want %d", total, len(dests))
}

type blockingSender struct {
	startedOnce sync.Once
	started     chan struct{}
	release     chan struct{}
}

func newBlockingSender() *blockingSender {
	return &blockingSender{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (b *blockingSender) Send(ctx context.Context, _ string, _ xport.Envelope) ([]byte, error) {
	b.startedOnce.Do(func() { close(b.started) })
	select {
	case <-b.release:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestWorker_Drain_NoInFlight_ReturnsImmediately(t *testing.T) {
	w, _, _ := newTestWorker(t, &mockSender{body: nil}, nil)
	startWorker(t, w)

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w.Drain(ctx)
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("Drain with no in-flight took %v, want near-zero", elapsed)
	}
}

func TestWorker_Drain_WaitsForInFlight(t *testing.T) {
	bs := newBlockingSender()
	w, _, _ := newTestWorker(t, bs, nil)
	w.Tick = time.Hour
	startWorker(t, w)

	mustEnqueue(t, w, "http://a.onion", "x")

	select {
	case <-bs.started:
	case <-time.After(2 * time.Second):
		t.Fatal("Send did not start")
	}

	drainReturned := make(chan struct{})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		w.Drain(ctx)
		close(drainReturned)
	}()

	select {
	case <-drainReturned:
		t.Fatal("Drain returned before Send was released")
	case <-time.After(150 * time.Millisecond):
	}

	close(bs.release)

	select {
	case <-drainReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("Drain did not return after Send completed")
	}
}

func TestWorker_Drain_GraceExpires(t *testing.T) {
	bs := newBlockingSender()
	w, _, _ := newTestWorker(t, bs, nil)
	w.Tick = time.Hour
	startWorker(t, w)

	mustEnqueue(t, w, "http://a.onion", "x")

	select {
	case <-bs.started:
	case <-time.After(2 * time.Second):
		t.Fatal("Send did not start")
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	w.Drain(ctx)
	elapsed := time.Since(start)
	if elapsed < 80*time.Millisecond || elapsed > 300*time.Millisecond {
		t.Errorf("Drain took %v, want ~100ms (grace expiry)", elapsed)
	}

	close(bs.release)
}

func TestWorker_Drain_NewRowsSkipped(t *testing.T) {
	ms := &mockSender{body: nil}
	w, _, _ := newTestWorker(t, ms, nil)
	w.Tick = time.Hour
	startWorker(t, w)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	w.Drain(ctx)

	mustEnqueue(t, w, "http://a.onion", "after-drain")
	time.Sleep(150 * time.Millisecond)
	if got := ms.callCount(); got != 0 {
		t.Errorf("sender called %d times after Drain, want 0", got)
	}
}

func TestDefaults_AgeBindsBeforeAttempts(t *testing.T) {
	w := NewWorker(newTestStore(t), &mockSender{}, nil, &Bus{})
	if w.DeadLetterAge != DefaultGiveUpAge {
		t.Fatalf("DeadLetterAge = %s, want %s", w.DeadLetterAge, DefaultGiveUpAge)
	}

	var span time.Duration
	for n := 1; n < w.MaxAttempts; n++ {
		span += w.Backoff(n)
	}
	if span <= w.DeadLetterAge {
		t.Errorf("attempt limit spans %s, which binds before the %s age gate — "+
			"raise DefaultMaxAttempts or the age stops being the real window",
			span.Round(time.Minute), w.DeadLetterAge)
	}

	row := &OutboxRow{Attempts: 40, KickedAttempts: 39}
	if got := row.CountedAttempts(); got != 1 {
		t.Fatalf("CountedAttempts = %d, want 1 — kicked attempts are being charged to "+
			"the limit, so the span above no longer bounds anything", got)
	}

	st := newTestStore(t)
	base := time.Unix(1_700_000_000, 0)
	r, err := st.Enqueue("http://a.onion", env("premise"), base)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Advance(r, StateEnqueued, base.Add(time.Hour), 1, 0, 0, "offline", base); err != nil {
		t.Fatal(err)
	}
	if _, err := st.KickByDests([]string{"http://a.onion"}, base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	kicked, err := st.Load("premise")
	if err != nil {
		t.Fatal(err)
	}
	if !kicked.Kicked {
		t.Error("a kicked row is not marked, so the attempt that follows is charged to the limit")
	}
}

type sentHook struct {
	mu    sync.Mutex
	dests []string
}

func (h *sentHook) fire(dest string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dests = append(h.dests, dest)
}

func (h *sentHook) seen() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.dests...)
}

func waitForHook(t *testing.T, h *sentHook, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(h.seen()) >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout: OnSendSuccess fired %d times, want >= %d", len(h.seen()), want)
}

func TestWorker_OnSendSuccessFiresOnAVerifiedAck(t *testing.T) {
	ms := &mockSender{body: []byte(`{"acked_id":"sent1"}`)}
	verified := 0
	ackV := AckVerifierFunc(func(_ context.Context, _ []byte, _ string) error {
		verified++
		return nil
	})
	w, _, _ := newTestWorker(t, ms, ackV)
	w.Tick = time.Hour
	h := &sentHook{}
	w.OnSendSuccess = h.fire
	startWorker(t, w)

	mustEnqueue(t, w, "http://a.onion", "sent1")
	waitForHook(t, h, 1, 2*time.Second)

	if verified == 0 {
		t.Error("the ack verifier never ran, so this is the back-compat path, not the production one")
	}
	if got := h.seen(); got[0] != "http://a.onion" {
		t.Errorf("OnSendSuccess dest = %q, want http://a.onion", got[0])
	}
	row, err := w.store.Load("sent1")
	if err != nil {
		t.Fatal(err)
	}
	if row.State != StateSent {
		t.Errorf("state = %q, want sent — the hook fired for a row that was not sent", row.State)
	}
}

func TestWorker_OnSendSuccessSilentOnAnUnauthenticatedAck(t *testing.T) {
	t.Run("empty body", func(t *testing.T) {
		ms := &mockSender{}
		ackV := AckVerifierFunc(func(_ context.Context, _ []byte, _ string) error {
			t.Error("the verifier ran on an empty body")
			return nil
		})
		w, _, bus := newTestWorker(t, ms, ackV)
		w.Tick = time.Hour
		h := &sentHook{}
		w.OnSendSuccess = h.fire
		ch, cancel := bus.Subscribe(4)
		defer cancel()
		startWorker(t, w)

		mustEnqueue(t, w, "http://a.onion", "emptyack")
		expectStatus(t, ch, StateSent, 2*time.Second)
		time.Sleep(50 * time.Millisecond)
		if got := h.seen(); len(got) != 0 {
			t.Errorf("OnSendSuccess fired %d times on an empty ack body, want 0", len(got))
		}
	})

	t.Run("no verifier", func(t *testing.T) {
		ms := &mockSender{body: []byte(`{"acked_id":"noverifier"}`)}
		w, _, bus := newTestWorker(t, ms, nil)
		w.Tick = time.Hour
		h := &sentHook{}
		w.OnSendSuccess = h.fire
		ch, cancel := bus.Subscribe(4)
		defer cancel()
		startWorker(t, w)

		mustEnqueue(t, w, "http://a.onion", "noverifier")
		expectStatus(t, ch, StateSent, 2*time.Second)
		time.Sleep(50 * time.Millisecond)
		if got := h.seen(); len(got) != 0 {
			t.Errorf("OnSendSuccess fired %d times with no ack verifier, want 0", len(got))
		}
	})
}

func TestWorker_OnSendSuccessSilentOnFailure(t *testing.T) {
	ms := &mockSender{failWith: errors.New("offline")}
	w, _, bus := newTestWorker(t, ms, nil)
	w.Tick = time.Hour
	w.MaxAttempts = 2
	now, advance := newMutableClock(time.Unix(1_700_000_000, 0))
	w.now = now
	h := &sentHook{}
	w.OnSendSuccess = h.fire
	ch, cancel := bus.Subscribe(4)
	defer cancel()
	startWorker(t, w)

	mustEnqueue(t, w, "http://a.onion", "failed1")
	waitForCalls(t, ms, 1, time.Second)
	if got := h.seen(); len(got) != 0 {
		t.Fatalf("OnSendSuccess fired %d times on a rescheduled send, want 0", len(got))
	}

	advance(35 * time.Second)
	w.signalDest("http://a.onion")
	expectStatus(t, ch, StateFailed, time.Second)
	time.Sleep(50 * time.Millisecond)

	if got := h.seen(); len(got) != 0 {
		t.Errorf("OnSendSuccess fired %d times on a row that reached failed, want 0", len(got))
	}
}

func TestKickedAttemptsDoNotSpendTheGiveUpLimit(t *testing.T) {
	ms := &mockSender{failWith: errors.New("offline")}
	w, st, _ := newTestWorker(t, ms, nil)
	w.Tick = time.Hour
	w.MaxAttempts = 3
	w.DeadLetterAge = 48 * time.Hour
	now, _ := newMutableClock(time.Unix(1_700_000_000, 0))
	w.now = now
	startWorker(t, w)

	mustEnqueue(t, w, "http://a.onion", "limited")
	waitForCalls(t, ms, 1, time.Second)

	for i := 2; i <= 7; i++ {
		if _, err := w.KickByDests([]string{"http://a.onion"}); err != nil {
			t.Fatal(err)
		}
		waitForCalls(t, ms, i, 2*time.Second)
	}

	row, err := st.Load("limited")
	if err != nil {
		t.Fatal(err)
	}
	if row.State != StateEnqueued {
		t.Fatalf("state = %q after 6 kicked attempts, want enqueued — the kick spent the give-up limit", row.State)
	}
	if row.Attempts != 7 {
		t.Errorf("Attempts = %d, want 7 — the dial count must stay honest", row.Attempts)
	}
	if row.KickedAttempts != 6 {
		t.Errorf("KickedAttempts = %d, want 6", row.KickedAttempts)
	}
	if got := row.CountedAttempts(); got != 1 {
		t.Errorf("CountedAttempts = %d, want 1 — only the first attempt was scheduled by the backoff", got)
	}
}

func TestAKickedAttemptWithABadAckSpendsNoGiveUpLimit(t *testing.T) {
	ms := &mockSender{body: []byte(`{"id":"a","kind":"sent_ack"}`)}
	ackV := AckVerifierFunc(func(_ context.Context, _ []byte, _ string) error {
		return errors.New("bad mac")
	})
	w, st, _ := newTestWorker(t, ms, ackV)
	w.Tick = time.Hour
	w.MaxAttempts = 3
	w.AckVerifyMaxTries = 99
	w.DeadLetterAge = 48 * time.Hour

	w.Backoff = func(attempts int) time.Duration { return time.Duration(attempts) * time.Minute }
	base := time.Unix(1_700_000_000, 0)
	now, _ := newMutableClock(base)
	w.now = now
	startWorker(t, w)

	mustEnqueue(t, w, "http://a.onion", "badack-kicked")
	waitForCalls(t, ms, 1, time.Second)

	first, err := st.Load("badack-kicked")
	if err != nil {
		t.Fatal(err)
	}
	firstWait := time.Unix(0, first.NextAttemptAt).Sub(base)

	for i := 2; i <= 7; i++ {
		if _, err := w.KickByDests([]string{"http://a.onion"}); err != nil {
			t.Fatal(err)
		}
		waitForCalls(t, ms, i, 2*time.Second)
	}

	row, err := st.Load("badack-kicked")
	if err != nil {
		t.Fatal(err)
	}
	if row.State != StateEnqueued {
		t.Fatalf("state = %q after 6 kicked attempts with a bad ack, want enqueued", row.State)
	}
	if row.Attempts != 7 {
		t.Errorf("Attempts = %d, want 7 — the dial count must stay honest", row.Attempts)
	}
	if row.KickedAttempts != 6 {
		t.Errorf("KickedAttempts = %d, want 6 — a bad ack does not make a kicked attempt chargeable", row.KickedAttempts)
	}
	if got := row.CountedAttempts(); got != 1 {
		t.Errorf("CountedAttempts = %d, want 1 — only the first attempt was scheduled by the backoff", got)
	}
	if got := time.Unix(0, row.NextAttemptAt).Sub(base); got != firstWait {
		t.Errorf("wait after six kicked bad acks = %s, want the unchanged %s", got, firstWait)
	}
}

func TestAKickedAttemptDoesNotAdvanceTheRamp(t *testing.T) {
	ms := &mockSender{failWith: errors.New("offline")}
	w, st, _ := newTestWorker(t, ms, nil)
	w.Tick = time.Hour
	w.Backoff = func(attempts int) time.Duration { return time.Duration(attempts) * time.Minute }
	base := time.Unix(1_700_000_000, 0)
	now, _ := newMutableClock(base)
	w.now = now
	startWorker(t, w)

	mustEnqueue(t, w, "http://a.onion", "ramp")
	waitForCalls(t, ms, 1, time.Second)

	row, err := st.Load("ramp")
	if err != nil {
		t.Fatal(err)
	}
	firstWait := time.Unix(0, row.NextAttemptAt).Sub(base)

	for i := 2; i <= 4; i++ {
		if _, err := w.KickByDests([]string{"http://a.onion"}); err != nil {
			t.Fatal(err)
		}
		waitForCalls(t, ms, i, 2*time.Second)
	}

	row, err = st.Load("ramp")
	if err != nil {
		t.Fatal(err)
	}
	if got := time.Unix(0, row.NextAttemptAt).Sub(base); got != firstWait {
		t.Errorf("wait after three kicked failures = %s, want the unchanged %s", got, firstWait)
	}
}

func TestTheAgeGateStillBindsOnAKickedRow(t *testing.T) {
	ms := &mockSender{failWith: errors.New("offline")}
	w, _, bus := newTestWorker(t, ms, nil)
	w.Tick = time.Hour
	w.MaxAttempts = 1000
	w.DeadLetterAge = 5 * time.Second
	now, advance := newMutableClock(time.Unix(1_700_000_000, 0))
	w.now = now
	ch, cancel := bus.Subscribe(4)
	defer cancel()
	startWorker(t, w)

	mustEnqueue(t, w, "http://a.onion", "agekick")
	waitForCalls(t, ms, 1, time.Second)

	advance(10 * time.Second)
	if _, err := w.KickByDests([]string{"http://a.onion"}); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, ch, StateFailed, 2*time.Second)
}

func row(id string, attempts int) *OutboxRow {
	return &OutboxRow{EnvelopeID: id, Dest: "http://a.onion", Attempts: attempts}
}

func ids(rows []*OutboxRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.EnvelopeID)
	}
	return out
}

func TestOrderPass_FreshRowsGoBeforeRowsThatAlreadyFailed(t *testing.T) {
	got := ids(orderPass([]*OutboxRow{
		row("failed-twice", 2),
		row("fresh-1", 0),
		row("failed-once", 1),
		row("fresh-2", 0),
	}, "http://a.onion"))

	want := []string{"fresh-1", "fresh-2", "failed-twice", "failed-once"}
	if fmt.Sprintf("%v", got) != fmt.Sprintf("%v", want) {
		t.Errorf("pass order: got %v, want %v", got, want)
	}
}

func TestOrderPass_KeepsTheDueOrderInsideEachGroup(t *testing.T) {
	got := ids(orderPass([]*OutboxRow{
		row("a", 0), row("b", 3), row("c", 0), row("d", 1), row("e", 0),
	}, "http://a.onion"))

	want := []string{"a", "c", "e", "b", "d"}
	if fmt.Sprintf("%v", got) != fmt.Sprintf("%v", want) {
		t.Errorf("pass order: got %v, want %v", got, want)
	}
}

func TestRowsForDest_TakesOnlyItsOwn(t *testing.T) {
	mine := rowsForDest([]*OutboxRow{
		{EnvelopeID: "x", Dest: "http://a.onion"},
		{EnvelopeID: "y", Dest: "http://b.onion"},
		{EnvelopeID: "z", Dest: "http://a.onion"},
	}, "http://a.onion")

	if got := ids(mine); fmt.Sprintf("%v", got) != `[x z]` {
		t.Errorf("rows for dest: got %v, want [x z]", got)
	}
}

type deadRowSender struct {
	mu       sync.Mutex
	stuckID  string
	seen     int
	blocking bool
	log      []string
	release  chan struct{}
}

func (d *deadRowSender) startBlocking() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.blocking = true
}

func (d *deadRowSender) Send(ctx context.Context, _ string, env xport.Envelope) ([]byte, error) {
	d.mu.Lock()
	if env.ID == d.stuckID {
		d.seen++
		blocking := d.blocking
		d.mu.Unlock()
		if !blocking {
			return nil, errors.New("mock: peer offline")
		}
		select {
		case <-d.release:
		case <-ctx.Done():
		}
		return nil, errors.New("mock: peer offline")
	}
	d.log = append(d.log, env.ID)
	d.mu.Unlock()
	return nil, nil
}

func (d *deadRowSender) sentIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, len(d.log))
	copy(out, d.log)
	return out
}

func (d *deadRowSender) attemptsOnStuck() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.seen
}

func TestWorker_AFreshRowOvertakesARowThatAlreadyFailed(t *testing.T) {
	ds := &deadRowSender{stuckID: "stuck", release: make(chan struct{})}
	defer close(ds.release)

	var open atomic.Bool
	open.Store(true)

	w, _, _ := newTestWorker(t, ds, nil)

	w.Backoff = func(int) time.Duration { return 0 }
	w.Tick = 20 * time.Millisecond
	w.Gate = func() bool { return open.Load() }
	startWorker(t, w)

	mustEnqueue(t, w, "http://a.onion", "stuck")
	deadline := time.Now().Add(2 * time.Second)
	for ds.attemptsOnStuck() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("the stuck row was never attempted")
		}
		time.Sleep(5 * time.Millisecond)
	}

	open.Store(false)
	time.Sleep(100 * time.Millisecond)
	ds.startBlocking()
	mustEnqueue(t, w, "http://a.onion", "fresh")
	open.Store(true)

	deadline = time.Now().Add(3 * time.Second)
	for {
		if got := ds.sentIDs(); len(got) > 0 {
			if got[0] != "fresh" {
				t.Fatalf("first send = %q, want \"fresh\"", got[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the fresh row never went out; it waited behind the row that already failed")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
