package presence_test

import (
	"sync"
	"testing"
	"time"

	"haoma-frontend/internal/presence"
)

type fakeTimer struct {
	mu       sync.Mutex
	callback func()
	fired    bool
	stopped  bool
}

func (t *fakeTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped || t.fired {
		return false
	}
	t.stopped = true
	return true
}

func (t *fakeTimer) Reset(time.Duration) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	wasActive := !t.stopped && !t.fired
	t.stopped = false
	t.fired = false
	return wasActive
}

func (t *fakeTimer) fire() {
	t.mu.Lock()
	if t.stopped || t.fired {
		t.mu.Unlock()
		return
	}
	t.fired = true
	cb := t.callback
	t.mu.Unlock()
	cb()
}

type fakeClock struct {
	mu     sync.Mutex
	timers []*fakeTimer
}

func (c *fakeClock) afterFunc(_ time.Duration, f func()) presence.Timer {
	t := &fakeTimer{callback: f}
	c.mu.Lock()
	c.timers = append(c.timers, t)
	c.mu.Unlock()
	return t
}

func (c *fakeClock) latest() *fakeTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(c.timers)
	if n == 0 {
		return nil
	}
	return c.timers[n-1]
}

func drain(t *testing.T, ch <-chan presence.Change) presence.Change {
	t.Helper()
	select {
	case c := <-ch:
		return c
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected a Change but the bus was silent")
		return presence.Change{}
	}
}

func expectSilent(t *testing.T, ch <-chan presence.Change) {
	t.Helper()
	select {
	case c := <-ch:
		t.Fatalf("expected no Change but got %+v", c)
	case <-time.After(50 * time.Millisecond):
	}
}

func newCache() (*presence.Cache, *fakeClock) {
	clk := &fakeClock{}
	c := presence.NewWithOptions(presence.Options{
		AfterFunc: clk.afterFunc,
	})
	return c, clk
}

func TestObserveHuman_Available_EmitsAvailable(t *testing.T) {
	c, _ := newCache()
	ch, cancel := c.Subscribe(8)
	defer cancel()

	c.ObserveHuman("alice", "available")

	got := drain(t, ch)
	if got.PeerID != "alice" {
		t.Errorf("peer_id = %q, want alice", got.PeerID)
	}
	if got.Snapshot.Effective != presence.EffectiveAvailable {
		t.Errorf("effective = %q, want %q", got.Snapshot.Effective, presence.EffectiveAvailable)
	}
	if got.Snapshot.Chatty != "available" {
		t.Errorf("chatty = %q, want available", got.Snapshot.Chatty)
	}
	if got.Snapshot.Accepting {
		t.Errorf("accepting = true, want false")
	}
}

func TestObserveTechnicalOnly_EmitsAccepting(t *testing.T) {
	c, _ := newCache()
	ch, cancel := c.Subscribe(8)
	defer cancel()

	c.ObserveTechnical("bob")

	got := drain(t, ch)
	if got.PeerID != "bob" {
		t.Errorf("peer_id = %q, want bob", got.PeerID)
	}
	if got.Snapshot.Effective != presence.EffectiveAccepting {
		t.Errorf("effective = %q, want %q", got.Snapshot.Effective, presence.EffectiveAccepting)
	}
	if !got.Snapshot.Accepting {
		t.Errorf("accepting = false, want true")
	}
	if got.Snapshot.Chatty != "" {
		t.Errorf("chatty = %q, want empty", got.Snapshot.Chatty)
	}
}

func TestHumanWinsOverTechnical(t *testing.T) {
	c, _ := newCache()
	ch, cancel := c.Subscribe(8)
	defer cancel()

	c.ObserveHuman("alice", "available")
	first := drain(t, ch)
	if first.Snapshot.Effective != presence.EffectiveAvailable {
		t.Fatalf("first effective = %q, want available", first.Snapshot.Effective)
	}

	c.ObserveTechnical("alice")
	expectSilent(t, ch)

	snap := c.Snapshot("alice")
	if !snap.Accepting {
		t.Errorf("accepting = false, want true")
	}
	if snap.Chatty != "available" {
		t.Errorf("chatty = %q, want available", snap.Chatty)
	}
	if snap.Effective != presence.EffectiveAvailable {
		t.Errorf("effective = %q, want available", snap.Effective)
	}
}

func TestSameStateTwice_NoSecondEvent(t *testing.T) {
	c, _ := newCache()
	ch, cancel := c.Subscribe(8)
	defer cancel()

	c.ObserveHuman("alice", "away")
	first := drain(t, ch)
	if first.Snapshot.Effective != presence.EffectiveAway {
		t.Fatalf("first = %q, want away", first.Snapshot.Effective)
	}

	c.ObserveHuman("alice", "away")
	expectSilent(t, ch)
}

func TestStateTransition_AvailableToAway(t *testing.T) {
	c, _ := newCache()
	ch, cancel := c.Subscribe(8)
	defer cancel()

	c.ObserveHuman("alice", "available")
	_ = drain(t, ch)

	c.ObserveHuman("alice", "away")
	got := drain(t, ch)
	if got.Snapshot.Effective != presence.EffectiveAway {
		t.Errorf("effective = %q, want away", got.Snapshot.Effective)
	}
}

func TestTimerB_Expiry_HumanGone_FallBackToAccepting(t *testing.T) {
	c, clk := newCache()
	ch, cancel := c.Subscribe(8)
	defer cancel()

	c.ObserveTechnical("alice")
	_ = drain(t, ch)
	techTimer := clk.latest()

	c.ObserveHuman("alice", "busy")
	_ = drain(t, ch)
	humanTimer := clk.latest()

	if techTimer == humanTimer {
		t.Fatalf("expected separate timers per slot")
	}

	humanTimer.fire()

	got := drain(t, ch)
	if got.Snapshot.Effective != presence.EffectiveAccepting {
		t.Errorf("effective = %q, want accepting", got.Snapshot.Effective)
	}
	if !got.Snapshot.Accepting {
		t.Errorf("accepting = false, want true")
	}
	if got.Snapshot.Chatty != "" {
		t.Errorf("chatty = %q, want empty", got.Snapshot.Chatty)
	}
}

func TestTimerA_Expiry_AcceptingGone_NoChatty_GoesUnknown(t *testing.T) {
	c, clk := newCache()
	ch, cancel := c.Subscribe(8)
	defer cancel()

	c.ObserveTechnical("alice")
	_ = drain(t, ch)
	techTimer := clk.latest()

	techTimer.fire()

	got := drain(t, ch)
	if got.Snapshot.Effective != presence.EffectiveUnknown {
		t.Errorf("effective = %q, want unknown", got.Snapshot.Effective)
	}
}

func TestBothTimers_ExpireInOrder(t *testing.T) {
	c, clk := newCache()
	ch, cancel := c.Subscribe(8)
	defer cancel()

	c.ObserveTechnical("alice")
	_ = drain(t, ch)
	techTimer := clk.latest()

	c.ObserveHuman("alice", "available")
	_ = drain(t, ch)
	humanTimer := clk.latest()

	humanTimer.fire()
	got := drain(t, ch)
	if got.Snapshot.Effective != presence.EffectiveAccepting {
		t.Fatalf("after human expiry: effective = %q, want accepting", got.Snapshot.Effective)
	}

	techTimer.fire()
	got = drain(t, ch)
	if got.Snapshot.Effective != presence.EffectiveUnknown {
		t.Errorf("after technical expiry: effective = %q, want unknown", got.Snapshot.Effective)
	}
}

func TestTimerA_Expiry_WhileChattyFresh_NoEvent(t *testing.T) {
	c, clk := newCache()
	ch, cancel := c.Subscribe(8)
	defer cancel()

	c.ObserveTechnical("alice")
	_ = drain(t, ch)
	techTimer := clk.latest()

	c.ObserveHuman("alice", "available")
	_ = drain(t, ch)

	techTimer.fire()
	expectSilent(t, ch)

	snap := c.Snapshot("alice")
	if snap.Accepting {
		t.Errorf("accepting = true, want false (timer fired)")
	}
	if snap.Chatty != "available" {
		t.Errorf("chatty = %q, want available", snap.Chatty)
	}
	if snap.Effective != presence.EffectiveAvailable {
		t.Errorf("effective = %q, want available", snap.Effective)
	}
}

func TestSnapshot_UnknownPeer_ReturnsUnknown(t *testing.T) {
	c, _ := newCache()
	snap := c.Snapshot("ghost")
	if snap.Effective != presence.EffectiveUnknown {
		t.Errorf("effective = %q, want unknown", snap.Effective)
	}
	if snap.Accepting {
		t.Errorf("accepting = true, want false")
	}
	if snap.Chatty != "" {
		t.Errorf("chatty = %q, want empty", snap.Chatty)
	}
}

func TestAll_ReturnsEverySeenPeer(t *testing.T) {
	c, _ := newCache()
	c.ObserveHuman("alice", "available")
	c.ObserveTechnical("bob")

	all := c.All()
	if len(all) != 2 {
		t.Fatalf("len(All) = %d, want 2", len(all))
	}
	if all["alice"].Effective != presence.EffectiveAvailable {
		t.Errorf("alice effective = %q, want available", all["alice"].Effective)
	}
	if all["bob"].Effective != presence.EffectiveAccepting {
		t.Errorf("bob effective = %q, want accepting", all["bob"].Effective)
	}
}

func TestSubscribe_DropOnFull_DoesNotBlockPublisher(t *testing.T) {
	c, _ := newCache()

	_, cancel := c.Subscribe(1)
	defer cancel()

	done := make(chan struct{})
	go func() {
		c.ObserveHuman("alice", "available")
		c.ObserveHuman("alice", "away")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("ObserveHuman blocked on a full subscriber buffer")
	}
}

func TestEmptyPeerID_Ignored(t *testing.T) {
	c, _ := newCache()
	ch, cancel := c.Subscribe(8)
	defer cancel()

	c.ObserveTechnical("")
	c.ObserveHuman("", "available")

	expectSilent(t, ch)
	if len(c.All()) != 0 {
		t.Errorf("All() = %d entries, want 0", len(c.All()))
	}
}

func TestEmptyHumanState_IsNoop(t *testing.T) {
	c, _ := newCache()
	ch, cancel := c.Subscribe(8)
	defer cancel()

	c.ObserveTechnical("alice")
	_ = drain(t, ch)

	c.ObserveHuman("alice", "")
	expectSilent(t, ch)
	if !c.Snapshot("alice").Accepting {
		t.Errorf("accepting unexpectedly cleared by empty-state observation")
	}
}

func TestResubscribe_ReceivesFutureChangesOnly(t *testing.T) {
	c, _ := newCache()

	ch1, cancel1 := c.Subscribe(8)
	c.ObserveHuman("alice", "available")
	_ = drain(t, ch1)
	cancel1()

	ch2, cancel2 := c.Subscribe(8)
	defer cancel2()
	expectSilent(t, ch2)

	c.ObserveHuman("alice", "away")
	got := drain(t, ch2)
	if got.Snapshot.Effective != presence.EffectiveAway {
		t.Errorf("effective = %q, want away", got.Snapshot.Effective)
	}
}
