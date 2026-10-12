package peers

import (
	"testing"
	"time"
)

func TestPeer_Unreachable(t *testing.T) {
	const queued = 1_700_000_000
	cases := []struct {
		name      string
		p         Peer
		wantGone  bool
		reasoning string
	}{
		{"no envelope expired", Peer{LastPassiveAt: queued - 100}, false,
			"nothing expired, so nothing marks the pairing"},
		{"expired, nothing received ever", Peer{UndeliveredQueuedAt: queued}, true,
			"an envelope expired and the peer never sent anything"},
		{"expired, inbound before it was queued", Peer{UndeliveredQueuedAt: queued, LastPassiveAt: queued - 1}, true,
			"what came before the envelope was queued says nothing about after"},
		{"expired, inbound after it was queued", Peer{UndeliveredQueuedAt: queued, LastPassiveAt: queued + 1}, false,
			"the peer sent something after the envelope was queued"},
		{"expired, inbound in the same second", Peer{UndeliveredQueuedAt: queued, LastPassiveAt: queued}, false,
			"a tie counts as an answer: a wrong mark asks for a delete"},
		{"expired, delivered send after it was queued", Peer{UndeliveredQueuedAt: queued, LastDeliveredAt: queued + 1}, false,
			"a send to the peer was delivered after the envelope was queued"},
		{"expired, delivered send in the same second", Peer{UndeliveredQueuedAt: queued, LastDeliveredAt: queued}, false,
			"a tie counts as an answer here too"},
		{"expired, delivered send before it was queued", Peer{UndeliveredQueuedAt: queued, LastDeliveredAt: queued - 1}, true,
			"a delivery before the envelope was queued says nothing about after"},
		{"retired", Peer{UndeliveredQueuedAt: queued, RetiredAt: queued + 10}, false,
			"a retired peer takes no traffic, so nothing could ever clear it"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.p.Unreachable(); got != c.wantGone {
				t.Errorf("Unreachable() = %v, want %v: %s", got, c.wantGone, c.reasoning)
			}
		})
	}
}

func TestRegistry_NoteUndeliveredAndDeliveredKeepTheNewest(t *testing.T) {
	r, _ := newTestRegistry(t)
	p := freshPeer(t, "notetest")
	if err := r.Add(p); err != nil {
		t.Fatal(err)
	}
	t0 := time.Unix(1_700_000_000, 0)

	for _, at := range []time.Time{t0.Add(time.Hour), t0} {
		if err := r.NoteUndelivered(p.ID, at); err != nil {
			t.Fatal(err)
		}
		if err := r.NoteDelivered(p.ID, at); err != nil {
			t.Fatal(err)
		}
	}
	got, err := r.Get(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := t0.Add(time.Hour).Unix(); got.UndeliveredQueuedAt != want {
		t.Errorf("UndeliveredQueuedAt = %d, want %d (the newest)", got.UndeliveredQueuedAt, want)
	}
	if want := t0.Add(time.Hour).Unix(); got.LastDeliveredAt != want {
		t.Errorf("LastDeliveredAt = %d, want %d (the newest)", got.LastDeliveredAt, want)
	}
}

func TestRegistry_TouchPresenceKeepsTheNewest(t *testing.T) {
	r, _ := newTestRegistry(t)
	p := freshPeer(t, "touchnewest")
	if err := r.Add(p); err != nil {
		t.Fatal(err)
	}
	t0 := time.Unix(1_700_000_000, 0)
	for _, at := range []time.Time{t0.Add(time.Hour), t0} {
		if err := r.TouchPresence(p.ID, at, "haoma"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := r.Get(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := t0.Add(time.Hour).Unix()
	if got.LastPassiveAt != want || got.LastActiveAt != want {
		t.Errorf("LastPassiveAt %d, LastActiveAt %d, want both %d (the newest)", got.LastPassiveAt, got.LastActiveAt, want)
	}
}

func TestRegistry_NoteUndelivered_UnknownPeer(t *testing.T) {
	r, _ := newTestRegistry(t)
	if err := r.NoteUndelivered("nope", time.Now()); err != ErrPeerNotFound {
		t.Errorf("err = %v, want ErrPeerNotFound", err)
	}
}
