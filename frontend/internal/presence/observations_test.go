package presence_test

import (
	"testing"
	"time"
)

func TestSubscribeObservations_EveryObservationAndExpiry(t *testing.T) {
	c, clk := newCache()
	ch, cancel := c.SubscribeObservations(8)
	defer cancel()

	next := func(what string) string {
		t.Helper()
		select {
		case id := <-ch:
			return id
		case <-time.After(200 * time.Millisecond):
			t.Fatalf("%s: no observation on the bus", what)
			return ""
		}
	}

	c.ObserveTechnical("alice")
	if got := next("first answer"); got != "alice" {
		t.Errorf("first answer: %q, want alice", got)
	}
	c.ObserveTechnical("alice")
	if got := next("second answer, no transition"); got != "alice" {
		t.Errorf("second answer: %q, want alice", got)
	}
	c.ObserveHuman("bob", "busy")
	if got := next("human state"); got != "bob" {
		t.Errorf("human state: %q, want bob", got)
	}
	clk.latest().fire()
	if got := next("expiry"); got != "bob" {
		t.Errorf("expiry: %q, want bob", got)
	}
	c.ObserveSending("carol")
	if got := next("envelope"); got != "carol" {
		t.Errorf("envelope: %q, want carol", got)
	}
	clk.latest().fire()
	if got := next("down arrow expiry"); got != "carol" {
		t.Errorf("down arrow expiry: %q, want carol", got)
	}
}
