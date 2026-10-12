package chat_test

import (
	"errors"
	"slices"
	"testing"

	"haoma-frontend/internal/chat"
)

func foldedChat(t *testing.T, s *chat.Store, first, second string) *chat.DirectChat {
	t.Helper()
	dc, err := s.CreateDirect(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AttachPeerID(dc.ID, second); err != nil {
		t.Fatal(err)
	}
	return dc
}

func TestDropRoutes_MovesThePrimaryAndKeepsTheIndex(t *testing.T) {
	s := chat.NewStore(newStore(t))
	dc := foldedChat(t, s, "peer-phone", "peer-computer")

	got, changed, err := s.DropRoutes(dc.ID, "peer-computer", []string{"peer-phone"})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("the first call changed nothing")
	}
	if got.PeerID != "peer-computer" || !slices.Equal(got.PeerIDs, []string{"peer-computer"}) {
		t.Fatalf("primary %s, routes %v; want peer-computer alone", got.PeerID, got.PeerIDs)
	}
	if slices.Contains(got.Members, "peer-phone") {
		t.Fatalf("members %v still hold the retired pairing", got.Members)
	}
	stored, err := s.GetByDirectPeer("peer-computer")
	if err != nil || stored.PeerID != "peer-computer" {
		t.Fatalf("the stored row does not carry the move: %+v, %v", stored, err)
	}
	viaOld, err := s.GetByDirectPeer("peer-phone")
	if err != nil || viaOld.ID != dc.ID {
		t.Fatalf("the retired pairing no longer finds its chat: %+v, %v", viaOld, err)
	}

	_, changed, err = s.DropRoutes(dc.ID, "peer-computer", []string{"peer-phone"})
	if err != nil || changed {
		t.Fatalf("the second call: changed=%v err=%v; want no change", changed, err)
	}
}

func TestDropRoutes_RefusesAPrimaryNotInTheSet(t *testing.T) {
	s := chat.NewStore(newStore(t))
	dc := foldedChat(t, s, "peer-phone", "peer-computer")

	if _, _, err := s.DropRoutes(dc.ID, "peer-elsewhere", []string{"peer-phone"}); !errors.Is(err, chat.ErrRoutesChanged) {
		t.Fatalf("err = %v, want ErrRoutesChanged", err)
	}
	if _, _, err := s.DropRoutes(dc.ID, "peer-phone", []string{"peer-phone"}); !errors.Is(err, chat.ErrRoutesChanged) {
		t.Fatalf("a primary that is also dropped: err = %v, want ErrRoutesChanged", err)
	}
	got, err := s.GetByDirectPeer("peer-phone")
	if err != nil {
		t.Fatal(err)
	}
	if got.PeerID != "peer-phone" || !slices.Equal(got.PeerIDs, []string{"peer-phone", "peer-computer"}) {
		t.Fatalf("a refused call changed the chat: primary %s, routes %v", got.PeerID, got.PeerIDs)
	}
}

func TestDropPairing(t *testing.T) {
	s := chat.NewStore(newStore(t))
	dc := foldedChat(t, s, "peer-phone", "peer-computer")
	if _, err := s.DropPairing(dc.ID, "peer-phone"); !errors.Is(err, chat.ErrRoutesChanged) {
		t.Fatalf("dropping the primary: err = %v, want ErrRoutesChanged", err)
	}
	got, err := s.DropPairing(dc.ID, "peer-computer")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.PeerIDs, []string{"peer-phone"}) || slices.Contains(got.Members, "peer-computer") {
		t.Fatalf("routes %v, members %v; want peer-phone alone", got.PeerIDs, got.Members)
	}
	if _, err := s.GetByDirectPeer("peer-computer"); !errors.Is(err, chat.ErrNotFound) {
		t.Fatalf("the dropped pairing still resolves: %v", err)
	}
	other, err := s.CreateDirect("peer-bob")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DropPairing(dc.ID, "peer-bob"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetByDirectPeer("peer-bob"); err != nil || got.ID != other.ID {
		t.Fatalf("another chat's index entry was taken: %+v, %v", got, err)
	}
}
