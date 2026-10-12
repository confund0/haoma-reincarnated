package chat

import (
	"errors"
	"testing"

	"github.com/dgraph-io/badger/v4"
)

func TestDelete_RemovesTheIndexOfADroppedRoute(t *testing.T) {
	s := NewStore(wbStore(t))
	dc, err := s.CreateDirect("peer-phone")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AttachPeerID(dc.ID, "peer-computer"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.DropRoutes(dc.ID, "peer-computer", []string{"peer-phone"}); err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateDirect("peer-bob")
	if err != nil {
		t.Fatal(err)
	}
	if !s.hasPeerIndex(t, "peer-phone") {
		t.Fatal("precondition: DropRoutes removed the dropped pairing's index entry; it is meant to keep it")
	}
	if err := s.Delete(dc.ID); err != nil {
		t.Fatal(err)
	}
	for _, pid := range []string{"peer-phone", "peer-computer"} {
		if s.hasPeerIndex(t, pid) {
			t.Fatalf("after Delete, the index entry for %s is still there", pid)
		}
	}
	if got, err := s.GetByDirectPeer("peer-bob"); err != nil || got.ID != other.ID {
		t.Fatalf("Delete took another chat's index with it: %+v, %v", got, err)
	}
}

func (s *Store) hasPeerIndex(t *testing.T, peerID string) bool {
	t.Helper()
	found := false
	err := s.st.View(func(txn *badger.Txn) error {
		_, err := txn.Get(byPeerKey(peerID))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}
