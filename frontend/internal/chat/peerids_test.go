package chat_test

import (
	"testing"

	"haoma-frontend/internal/chat"
)

func TestCreateDirect_SeedsPeerIDs(t *testing.T) {
	st := newStore(t)
	s := chat.NewStore(st)

	dc, err := s.CreateDirect("peer-alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(dc.PeerIDs) != 1 || dc.PeerIDs[0] != "peer-alice" {
		t.Fatalf("CreateDirect PeerIDs = %v, want [peer-alice]", dc.PeerIDs)
	}
	if dc.PeerID != dc.PeerIDs[0] {
		t.Errorf("invariant broken: PeerID=%q != PeerIDs[0]=%q", dc.PeerID, dc.PeerIDs[0])
	}
}

func TestGet_RoundTripsPeerIDs(t *testing.T) {
	st := newStore(t)
	s := chat.NewStore(st)

	created, err := s.CreateDirect("peer-bob")
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	dc := c.(*chat.DirectChat)
	if len(dc.PeerIDs) != 1 || dc.PeerIDs[0] != "peer-bob" {
		t.Fatalf("round-tripped PeerIDs = %v, want [peer-bob]", dc.PeerIDs)
	}
}

func TestFanoutPeerIDs(t *testing.T) {

	st := newStore(t)
	s := chat.NewStore(st)
	dc, err := s.CreateDirect("peer-alice")
	if err != nil {
		t.Fatal(err)
	}
	if got := dc.FanoutPeerIDs(); len(got) != 1 || got[0] != "peer-alice" {
		t.Fatalf("FanoutPeerIDs = %v, want [peer-alice]", got)
	}

	only := &chat.DirectChat{PeerID: "peer-bob"}
	if got := only.FanoutPeerIDs(); len(got) != 1 || got[0] != "peer-bob" {
		t.Fatalf("fallback FanoutPeerIDs = %v, want [peer-bob]", got)
	}

	empty := &chat.DirectChat{}
	if got := empty.FanoutPeerIDs(); got != nil {
		t.Fatalf("empty FanoutPeerIDs = %v, want nil", got)
	}
}
