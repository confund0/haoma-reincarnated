package linkeddevice_test

import (
	"errors"
	"testing"

	"haoma-frontend/internal/crdt"
)

func TestBeforeGrowRunsBeforeALinkIsWritten(t *testing.T) {
	s, _ := newStore(t)
	calls := 0
	s.SetBeforeGrow(func() error {
		calls++
		members, err := s.Members()
		if err != nil {
			t.Fatalf("Members: %v", err)
		}
		if calls == 1 && len(members) != 0 {
			t.Errorf("the hook ran after the link was written: %v", members)
		}
		return nil
	})
	if _, err := s.Link("peer-a", 10); err != nil {
		t.Fatalf("Link: %v", err)
	}
	other := crdt.TwoPSet{}
	other.Add("peer-b", 11)
	if _, err := s.Merge(other); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if _, err := s.Unlink("peer-a", 12); err != nil {
		t.Fatalf("Unlink: %v", err)
	}
	if calls != 2 {
		t.Errorf("hook ran %d times, want 2 (Link and Merge)", calls)
	}
}

func TestBeforeGrowErrorRefusesTheLink(t *testing.T) {
	s, _ := newStore(t)
	s.SetBeforeGrow(func() error { return errors.New("seeded") })
	if _, err := s.Link("peer-a", 10); err == nil {
		t.Error("Link went on after the hook failed")
	}
	other := crdt.TwoPSet{}
	other.Add("peer-b", 11)
	if _, err := s.Merge(other); err == nil {
		t.Error("Merge went on after the hook failed")
	}
	if members, err := s.Members(); err != nil || len(members) != 0 {
		t.Errorf("Members = %v, %v; want none", members, err)
	}
}
