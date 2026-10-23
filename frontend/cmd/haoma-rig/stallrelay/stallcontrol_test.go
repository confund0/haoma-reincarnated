package main

import (
	"bytes"
	"fmt"
	"sync"
	"testing"
	"time"
)

var stallTestBase = time.Unix(1_700_000_000, 0)

func TestPeriodicStall_EntersAndExitsOnSchedule(t *testing.T) {
	c := newStallController(10*time.Second, 2*time.Second, 1000, 1<<20, stallTestBase, nil)

	out := c.Submit(stallTestBase.Add(1*time.Second), []byte("abc"))
	if !bytes.Equal(out, []byte("abc")) {
		t.Fatalf("pre-schedule: got %q, want passthrough", out)
	}
	if c.state != stateForward {
		t.Fatalf("pre-schedule: state = %v, want stateForward", c.state)
	}

	out = c.Submit(stallTestBase.Add(10*time.Second), []byte("def"))
	if out != nil {
		t.Fatalf("at schedule: got %q, want nothing forwarded (stalled)", out)
	}
	if c.state != stateStalled {
		t.Fatalf("at schedule: state = %v, want stateStalled", c.state)
	}

	out = c.Submit(stallTestBase.Add(11*time.Second), []byte("ghi"))
	if out != nil {
		t.Fatalf("mid-stall: got %q, want nothing forwarded", out)
	}

	out = c.Submit(stallTestBase.Add(12*time.Second), nil)
	if c.state != stateDraining {
		t.Fatalf("after stallFor: state = %v, want stateDraining", c.state)
	}
	if out != nil {
		t.Fatalf("at the instant the stall ends: got %q, want nothing yet (budget just reset)", out)
	}

	out = c.Submit(stallTestBase.Add(12*time.Second+200*time.Millisecond), nil)
	if len(out) == 0 {
		t.Fatalf("200ms into the drain: want some drained bytes")
	}
}

func TestManualTrigger_FiresImmediatelyAndIgnoresSecond(t *testing.T) {
	var logged []string
	log := func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}
	c := newStallController(0, 5*time.Second, 1000, 1<<20, stallTestBase, log)

	t0 := stallTestBase.Add(time.Second)
	c.Trigger(t0)
	if c.state != stateStalled {
		t.Fatalf("after manual trigger: state = %v, want stateStalled", c.state)
	}
	wantEnd := t0.Add(5 * time.Second)
	if !c.stallEndAt.Equal(wantEnd) {
		t.Fatalf("stallEndAt = %v, want %v", c.stallEndAt, wantEnd)
	}

	t1 := t0.Add(2 * time.Second)
	c.Trigger(t1)
	if !c.stallEndAt.Equal(wantEnd) {
		t.Fatalf("stallEndAt after second trigger = %v, want unchanged %v", c.stallEndAt, wantEnd)
	}
	if len(logged) == 0 || logged[len(logged)-1] != "stall: manual trigger ignored, a stall is already active" {
		t.Fatalf("want a no-op log line for the second trigger, got %v", logged)
	}
}

func TestDrain_CappedThenUncapped(t *testing.T) {
	const capBps = int64(1000)
	c := newStallController(0, time.Second, capBps, 1<<20, stallTestBase, nil)

	t0 := stallTestBase
	c.Trigger(t0)

	backlog := make([]byte, 5000)
	c.Submit(t0.Add(100*time.Millisecond), backlog)

	end := t0.Add(time.Second)
	out := c.Submit(end, nil)
	if int64(len(out)) > capBps {
		t.Fatalf("drain tick at stall end: got %d bytes, want at most a few (budget just reset)", len(out))
	}
	if c.state != stateDraining {
		t.Fatalf("state = %v, want stateDraining", c.state)
	}

	out = c.Submit(end.Add(500*time.Millisecond), nil)
	if int64(len(out)) > 520 {
		t.Fatalf("500ms into drain: got %d bytes, want capped near 500", len(out))
	}
	if c.state != stateDraining {
		t.Fatalf("still draining: state = %v", c.state)
	}

	out = c.Submit(end.Add(8*time.Second), []byte("tail"))
	if c.state != stateForward {
		t.Fatalf("after backlog drains: state = %v, want stateForward", c.state)
	}
	if !bytes.Contains(out, []byte("tail")) {
		t.Fatalf("after backlog drains: want the freshly-arrived bytes forwarded uncapped, got %q", out)
	}
}

func TestBuffer_DropsOldestWhenFull(t *testing.T) {
	c := newStallController(0, 10*time.Second, 1000, 10, stallTestBase, nil)
	t0 := stallTestBase
	c.Trigger(t0)

	c.Submit(t0.Add(time.Millisecond), []byte("0123456789"))
	if len(c.backlog) != 10 {
		t.Fatalf("backlog = %q, want full 10 bytes", c.backlog)
	}
	c.Submit(t0.Add(2*time.Millisecond), []byte("ABCDE"))
	want := []byte("56789ABCDE")
	if !bytes.Equal(c.backlog, want) {
		t.Fatalf("backlog = %q, want %q (oldest 5 bytes dropped)", c.backlog, want)
	}
}

func TestConcurrentSubmitAndTrigger(t *testing.T) {
	c := newStallController(0, time.Millisecond, 1000, 4096, stallTestBase, nil)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		now := stallTestBase
		for i := 0; i < 500; i++ {
			now = now.Add(time.Millisecond)
			c.Submit(now, []byte("xyz"))
		}
	}()
	go func() {
		defer wg.Done()
		now := stallTestBase
		for i := 0; i < 500; i++ {
			now = now.Add(time.Millisecond)
			c.Trigger(now)
		}
	}()
	wg.Wait()
}
