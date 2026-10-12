package presence_test

import (
	"fmt"
	"testing"
	"time"
)

func TestSubscribeDirty_NothingIsLostWhenTheReaderIsLate(t *testing.T) {
	c, clk := newCache()
	wake, drain, cancel := c.SubscribeDirty()
	defer cancel()

	const peers = 500
	for i := 0; i < peers; i++ {
		id := fmt.Sprintf("peer-%03d", i)
		c.ObserveTechnical(id)
		c.ObserveSending(id)
		c.ObserveHuman(id, "busy")
	}
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("no wake after the burst")
	}
	got := drain()
	if len(got) != peers {
		t.Fatalf("drained %d peers, want %d", len(got), peers)
	}
	for i, id := range got {
		if want := fmt.Sprintf("peer-%03d", i); id != want {
			t.Fatalf("drain[%d] = %q, want %q", i, id, want)
		}
	}
	if again := drain(); len(again) != 0 {
		t.Errorf("a second drain returned %v, want nothing", again)
	}

	clk.latest().fire()
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("no wake after the expiry")
	}
	if got := drain(); len(got) != 1 || got[0] != "peer-499" {
		t.Fatalf("after the expiry drained %v, want [peer-499]", got)
	}
	if snap := c.Snapshot("peer-499"); snap.Chatty != "" || !snap.Sending {
		t.Errorf("peer-499 after its human slot expired: %+v", snap)
	}
}

func TestSubscribeDirty_AMarkAfterTheWakeIsInTheNextDrain(t *testing.T) {
	c, _ := newCache()
	wake, drain, cancel := c.SubscribeDirty()
	defer cancel()

	c.ObserveSending("alice")
	<-wake
	c.ObserveSending("bob")
	got := drain()
	if len(got) != 2 {
		t.Fatalf("drained %v, want alice and bob", got)
	}
	select {
	case <-wake:
		if extra := drain(); len(extra) != 0 {
			t.Errorf("a stale wake drained %v", extra)
		}
	default:
	}
	cancel()
	if _, ok := <-wake; ok {
		t.Error("wake still open after cancel")
	}
	c.ObserveSending("carol")
}
