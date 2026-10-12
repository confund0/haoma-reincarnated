package presence_test

import (
	"testing"

	"haoma-frontend/internal/presence"
)

func TestObserveSending_TurnsTheDownArrowOn(t *testing.T) {
	c, _ := newCache()
	ch, cancel := c.Subscribe(8)
	defer cancel()

	c.ObserveSending("alice")

	got := drain(t, ch)
	if got.PeerID != "alice" {
		t.Errorf("peer_id = %q, want alice", got.PeerID)
	}
	want := presence.Snapshot{Sending: true, Effective: presence.EffectiveUnknown}
	if got.Snapshot != want {
		t.Errorf("snapshot = %+v, want %+v", got.Snapshot, want)
	}
	if snap := c.Snapshot("alice"); snap != want {
		t.Errorf("Snapshot = %+v, want %+v", snap, want)
	}

	c.ObserveSending("alice")
	expectSilent(t, ch)
}

func TestObserveSending_ExpiresOnItsOwnTimer(t *testing.T) {
	c, clk := newCache()
	ch, cancel := c.Subscribe(8)
	defer cancel()

	c.ObserveTechnical("alice")
	_ = drain(t, ch)
	techTimer := clk.latest()

	c.ObserveHuman("alice", "busy")
	_ = drain(t, ch)
	humanTimer := clk.latest()

	c.ObserveSending("alice")
	got := drain(t, ch)
	sendTimer := clk.latest()
	if sendTimer == techTimer || sendTimer == humanTimer {
		t.Fatalf("the down arrow shares a timer with another slot")
	}
	want := presence.Snapshot{Accepting: true, Sending: true, Chatty: "busy", Effective: "busy"}
	if got.Snapshot != want {
		t.Fatalf("after the envelope: %+v, want %+v", got.Snapshot, want)
	}

	sendTimer.fire()
	got = drain(t, ch)
	want = presence.Snapshot{Accepting: true, Chatty: "busy", Effective: "busy"}
	if got.Snapshot != want {
		t.Errorf("after the down arrow's TTL: %+v, want %+v", got.Snapshot, want)
	}

	techTimer.fire()
	_ = drain(t, ch)
	humanTimer.fire()
	got = drain(t, ch)
	want = presence.Snapshot{Effective: presence.EffectiveUnknown}
	if got.Snapshot != want {
		t.Errorf("after every TTL: %+v, want %+v", got.Snapshot, want)
	}
}

func TestObserveSending_ReArmedTimerSurvivesTheOldOne(t *testing.T) {
	c, clk := newCache()
	c.ObserveSending("alice")
	first := clk.latest()
	c.ObserveSending("alice")
	second := clk.latest()
	if first == second {
		t.Fatalf("the second envelope did not re-arm the timer")
	}
	first.callback()
	if !c.Snapshot("alice").Sending {
		t.Errorf("the old timer turned the down arrow off")
	}
	second.fire()
	if c.Snapshot("alice").Sending {
		t.Errorf("the down arrow is still on after its TTL")
	}
}

func TestObserveSending_EmptyPeerIgnored(t *testing.T) {
	c, _ := newCache()
	c.ObserveSending("")
	if len(c.All()) != 0 {
		t.Errorf("All() = %d entries, want 0", len(c.All()))
	}
}
