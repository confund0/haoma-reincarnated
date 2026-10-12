package main

import (
	"context"
	"sync"
	"testing"
	"time"
)

var testTiming = syncTriggerTiming{
	debounce:    5 * time.Second,
	ceiling:     30 * time.Second,
	minInterval: 60 * time.Second,
}

var t0 = time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

func TestSweepDelayFirstGrowthWaitsTheDebounce(t *testing.T) {
	wait, reason := sweepDelay(t0, t0, time.Time{}, testTiming)
	if wait != testTiming.debounce {
		t.Errorf("wait = %s, want the debounce %s", wait, testTiming.debounce)
	}
	if reason != "debounce" {
		t.Errorf("reason = %q, want debounce", reason)
	}
}

func TestSweepDelayEachWriteRestartsTheDebounce(t *testing.T) {
	first := t0
	later := t0.Add(3 * time.Second)
	wait, reason := sweepDelay(later, first, time.Time{}, testTiming)
	if wait != testTiming.debounce {
		t.Errorf("wait = %s, want the debounce restarted at %s", wait, testTiming.debounce)
	}
	if reason != "debounce" {
		t.Errorf("reason = %q, want debounce", reason)
	}
}

func TestSweepDelayCeilingStopsAnEndlessDeferral(t *testing.T) {
	first := t0

	now := t0.Add(28 * time.Second)
	wait, reason := sweepDelay(now, first, time.Time{}, testTiming)
	if want := 2 * time.Second; wait != want {
		t.Errorf("wait = %s, want %s — the ceiling did not pull the deadline in", wait, want)
	}
	if reason != "ceiling" {
		t.Errorf("reason = %q, want ceiling", reason)
	}
}

func TestSweepDelayPastTheCeilingIsImmediate(t *testing.T) {
	wait, reason := sweepDelay(t0.Add(90*time.Second), t0, time.Time{}, testTiming)
	if wait != 0 {
		t.Errorf("wait = %s, want 0", wait)
	}
	if reason != "ceiling" {
		t.Errorf("reason = %q, want ceiling", reason)
	}
}

func TestSweepDelayFloorOutranksTheDebounce(t *testing.T) {
	last := t0
	now := t0.Add(10 * time.Second)
	wait, reason := sweepDelay(now, now, last, testTiming)
	if want := 50 * time.Second; wait != want {
		t.Errorf("wait = %s, want %s — the minimum interval did not hold the sweep back", wait, want)
	}
	if reason != "minimum_interval" {
		t.Errorf("reason = %q, want minimum_interval", reason)
	}
}

func TestSweepDelayFloorOutranksTheCeiling(t *testing.T) {
	last := t0
	first := t0.Add(1 * time.Second)
	now := t0.Add(40 * time.Second)
	wait, reason := sweepDelay(now, first, last, testTiming)
	if want := 20 * time.Second; wait != want {
		t.Errorf("wait = %s, want %s — the ceiling was allowed to break the floor", wait, want)
	}
	if reason != "minimum_interval" {
		t.Errorf("reason = %q, want minimum_interval", reason)
	}
}

func TestSweepDelayFloorStopsMatteringOnceItHasPassed(t *testing.T) {
	last := t0
	now := t0.Add(2 * time.Minute)
	wait, reason := sweepDelay(now, now, last, testTiming)
	if wait != testTiming.debounce {
		t.Errorf("wait = %s, want the debounce %s", wait, testTiming.debounce)
	}
	if reason != "debounce" {
		t.Errorf("reason = %q, want debounce", reason)
	}
}

func TestNoteCoalescesAndNeverBlocks(t *testing.T) {
	tr := newSyncTrigger(func(context.Context) sweepOutcome { return sweepStarted })
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			tr.note()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("note() blocked — a store write would be waiting on the rail")
	}
	if got := len(tr.signal); got != 1 {
		t.Errorf("the signal channel holds %d entries, want 1 — a burst is not being coalesced", got)
	}
}

func TestNoteOnNilTriggerIsSafe(t *testing.T) {
	var tr *syncTrigger
	tr.note()
}

func TestRunSweepsOnGrowthAndNotBefore(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	fired := make(chan struct{}, 4)
	tr := newSyncTrigger(func(context.Context) sweepOutcome {
		mu.Lock()
		calls++
		mu.Unlock()
		fired <- struct{}{}
		return sweepStarted
	})
	tr.timing = syncTriggerTiming{
		debounce:    20 * time.Millisecond,
		ceiling:     200 * time.Millisecond,
		minInterval: 0,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tr.run(ctx)

	select {
	case <-fired:
		t.Fatal("the trigger swept with nothing having changed — an idle device must cost nothing")
	case <-time.After(150 * time.Millisecond):
	}

	tr.note()
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("journal growth never produced a sweep")
	}

	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 1 {
		t.Errorf("the sweep ran %d times for one burst, want 1", got)
	}
}

func TestRunHoldsASecondBurstBehindTheFloor(t *testing.T) {
	fired := make(chan time.Time, 4)
	tr := newSyncTrigger(func(context.Context) sweepOutcome {
		fired <- time.Now()
		return sweepStarted
	})
	tr.timing = syncTriggerTiming{
		debounce:    10 * time.Millisecond,
		ceiling:     100 * time.Millisecond,
		minInterval: 300 * time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tr.run(ctx)

	tr.note()
	var first time.Time
	select {
	case first = <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("the first burst never swept")
	}

	tr.note()
	select {
	case second := <-fired:
		if gap := second.Sub(first); gap < 250*time.Millisecond {
			t.Errorf("the second sweep followed the first after %s, want at least the minimum interval", gap)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the second burst never swept — the floor became a permanent block")
	}
}

func TestRunFoldsGrowthThatArrivesDuringASweep(t *testing.T) {
	inSweep := make(chan struct{}, 4)
	release := make(chan struct{})
	var once sync.Once
	tr := newSyncTrigger(func(context.Context) sweepOutcome {
		inSweep <- struct{}{}
		once.Do(func() { <-release })
		return sweepStarted
	})
	tr.timing = syncTriggerTiming{
		debounce:    10 * time.Millisecond,
		ceiling:     100 * time.Millisecond,
		minInterval: 0,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tr.run(ctx)

	tr.note()
	select {
	case <-inSweep:
	case <-time.After(2 * time.Second):
		t.Fatal("the first sweep never started")
	}

	tr.note()
	close(release)

	select {
	case <-inSweep:
	case <-time.After(3 * time.Second):
		t.Fatal("growth that arrived while a sweep was running was dropped — the mutation it named would never reach a sibling")
	}
}

func TestRunExitsOnContextCancel(t *testing.T) {
	tr := newSyncTrigger(func(context.Context) sweepOutcome { return sweepStarted })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		tr.run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the trigger goroutine outlived its context")
	}
}
