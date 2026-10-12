package main

import (
	"sync"
	"testing"
	"time"
)

type collector struct {
	mu      sync.Mutex
	batches [][]string
	fired   chan struct{}
}

func newCollector() *collector {
	return &collector{fired: make(chan struct{}, 16)}
}

func (c *collector) send(batch []string) {
	c.mu.Lock()
	c.batches = append(c.batches, batch)
	c.mu.Unlock()
	c.fired <- struct{}{}
}

func (c *collector) await(t *testing.T, within time.Duration) []string {
	t.Helper()
	select {
	case <-c.fired:
	case <-time.After(within):
		t.Fatalf("no push after %s", within)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.batches[len(c.batches)-1]
}

func (c *collector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.batches)
}

func TestContactsPusher_ABurstCrossesAsOneEnvelope(t *testing.T) {
	c := newCollector()
	p := newContactsPusher(c.send)
	p.timing = contactsPushTiming{debounce: 40 * time.Millisecond, ceiling: time.Second}

	p.note("fp-carol")
	time.Sleep(10 * time.Millisecond)
	p.note("fp-alice")
	time.Sleep(10 * time.Millisecond)
	p.note("fp-bob")

	batch := c.await(t, time.Second)
	if len(batch) != 3 {
		t.Fatalf("batch = %v, want three people in one push", batch)
	}

	if batch[0] != "fp-alice" || batch[2] != "fp-carol" {
		t.Errorf("batch = %v, want it sorted", batch)
	}
	if got := c.count(); got != 1 {
		t.Errorf("%d pushes, want 1", got)
	}
}

func TestContactsPusher_OnePersonNamedOnce(t *testing.T) {
	c := newCollector()
	p := newContactsPusher(c.send)
	p.timing = contactsPushTiming{debounce: 40 * time.Millisecond, ceiling: time.Second}

	p.note("fp-alice")
	p.note("fp-alice")
	p.note("fp-alice")

	batch := c.await(t, time.Second)
	if len(batch) != 1 || batch[0] != "fp-alice" {
		t.Fatalf("batch = %v, want fp-alice once", batch)
	}
}

func TestContactsPusher_TheCeilingStopsSteadyActivityDeferringTheSend(t *testing.T) {
	c := newCollector()
	p := newContactsPusher(c.send)
	p.timing = contactsPushTiming{debounce: 50 * time.Millisecond, ceiling: 120 * time.Millisecond}

	done := make(chan struct{})
	go func() {
		defer close(done)

		for i := 0; i < 9; i++ {
			p.note("fp-alice")
			time.Sleep(30 * time.Millisecond)
		}
	}()

	start := time.Now()
	batch := c.await(t, 220*time.Millisecond)
	if took := time.Since(start); took > 220*time.Millisecond {
		t.Errorf("the push took %s — the ceiling did not cap the debounce", took)
	}
	if len(batch) != 1 {
		t.Fatalf("batch = %v, want one person", batch)
	}
	<-done
}

func TestContactsPusher_IdleSendsNothing(t *testing.T) {
	c := newCollector()
	p := newContactsPusher(c.send)
	p.timing = contactsPushTiming{debounce: 20 * time.Millisecond, ceiling: time.Second}

	time.Sleep(80 * time.Millisecond)
	if got := c.count(); got != 0 {
		t.Errorf("%d pushes on an idle device, want 0", got)
	}
	if p.timer != nil {
		t.Error("a timer was armed with nothing pending")
	}
}

func TestContactsPusher_AnEmptyFingerprintArmsNothing(t *testing.T) {
	c := newCollector()
	p := newContactsPusher(c.send)
	p.timing = contactsPushTiming{debounce: 20 * time.Millisecond, ceiling: time.Second}

	p.note("")

	time.Sleep(80 * time.Millisecond)
	if got := c.count(); got != 0 {
		t.Errorf("%d pushes, want 0 — an empty fingerprint names nobody", got)
	}
}

func TestContactsPusher_AChangeDuringASendStartsTheNextBurst(t *testing.T) {
	c := newCollector()
	var p *contactsPusher
	first := true
	p = newContactsPusher(func(batch []string) {
		if first {
			first = false
			p.note("fp-bob")
		}
		c.send(batch)
	})
	p.timing = contactsPushTiming{debounce: 30 * time.Millisecond, ceiling: time.Second}

	p.note("fp-alice")

	one := c.await(t, time.Second)
	if len(one) != 1 || one[0] != "fp-alice" {
		t.Fatalf("first batch = %v, want fp-alice", one)
	}
	two := c.await(t, time.Second)
	if len(two) != 1 || two[0] != "fp-bob" {
		t.Fatalf("second batch = %v, want fp-bob — the change during the send was lost", two)
	}
}
