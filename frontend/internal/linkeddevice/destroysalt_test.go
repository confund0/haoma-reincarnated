package linkeddevice_test

import (
	"bytes"
	"testing"

	"haoma-frontend/internal/linkeddevice"
)

func TestSiblingSalt_NothingHeldIsNotAnError(t *testing.T) {
	s, _ := newStore(t)
	rec, held, err := s.SiblingSalt("sibling-1")
	if err != nil {
		t.Fatalf("SiblingSalt on a fresh store: %v", err)
	}
	if held {
		t.Fatal("a fresh store reports a sibling salt is held")
	}
	if len(rec.Salt) != 0 {
		t.Fatalf("a fresh store carries a salt: %q", rec.Salt)
	}
}

func TestSiblingSalt_FirstPutIsNotAReplacement(t *testing.T) {
	s, _ := newStore(t)
	replaced, err := s.PutSiblingSalt("sibling-1", linkeddevice.SiblingSalt{
		Salt: []byte("first-salt"), SetAt: 10, ReceivedAt: 11,
	})
	if err != nil {
		t.Fatalf("PutSiblingSalt: %v", err)
	}
	if replaced {
		t.Fatal("the first salt for a sibling reported a replacement")
	}
	rec, held, err := s.SiblingSalt("sibling-1")
	if err != nil {
		t.Fatalf("SiblingSalt: %v", err)
	}
	if !held {
		t.Fatal("the salt was not held after a put")
	}
	if !bytes.Equal(rec.Salt, []byte("first-salt")) {
		t.Fatalf("salt = %q, want first-salt", rec.Salt)
	}
	if rec.SetAt != 10 || rec.ReceivedAt != 11 {
		t.Fatalf("timestamps = %d/%d, want 10/11", rec.SetAt, rec.ReceivedAt)
	}
}

func TestSiblingSalt_SecondPutReportsAReplacement(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.PutSiblingSalt("sibling-1", linkeddevice.SiblingSalt{Salt: []byte("first-salt")}); err != nil {
		t.Fatalf("first PutSiblingSalt: %v", err)
	}
	replaced, err := s.PutSiblingSalt("sibling-1", linkeddevice.SiblingSalt{Salt: []byte("second-salt")})
	if err != nil {
		t.Fatalf("second PutSiblingSalt: %v", err)
	}
	if !replaced {
		t.Fatal("replacing a salt did not report a replacement")
	}
	rec, _, err := s.SiblingSalt("sibling-1")
	if err != nil {
		t.Fatalf("SiblingSalt: %v", err)
	}
	if !bytes.Equal(rec.Salt, []byte("second-salt")) {
		t.Fatalf("salt = %q, want second-salt", rec.Salt)
	}
}

func TestSiblingSalt_IsKeyedPerSibling(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.PutSiblingSalt("sibling-1", linkeddevice.SiblingSalt{Salt: []byte("one")}); err != nil {
		t.Fatalf("PutSiblingSalt: %v", err)
	}
	if _, err := s.PutSiblingSalt("sibling-2", linkeddevice.SiblingSalt{Salt: []byte("two")}); err != nil {
		t.Fatalf("PutSiblingSalt: %v", err)
	}
	one, _, err := s.SiblingSalt("sibling-1")
	if err != nil {
		t.Fatalf("SiblingSalt: %v", err)
	}
	two, _, err := s.SiblingSalt("sibling-2")
	if err != nil {
		t.Fatalf("SiblingSalt: %v", err)
	}
	if !bytes.Equal(one.Salt, []byte("one")) || !bytes.Equal(two.Salt, []byte("two")) {
		t.Fatalf("salts crossed: %q / %q", one.Salt, two.Salt)
	}
}

func TestSiblingSalt_DoesNotJoinTheLinkedDeviceSet(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.PutSiblingSalt("sibling-1", linkeddevice.SiblingSalt{Salt: []byte("one")}); err != nil {
		t.Fatalf("PutSiblingSalt: %v", err)
	}
	members, err := s.Members()
	if err != nil {
		t.Fatalf("Members: %v", err)
	}
	if len(members) != 0 {
		t.Fatalf("storing a salt added members: %v", members)
	}
}

func TestSiblingSalt_RefusesEmptyInput(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.PutSiblingSalt("", linkeddevice.SiblingSalt{Salt: []byte("one")}); err == nil {
		t.Fatal("a salt with no peer id was accepted")
	}
	if _, err := s.PutSiblingSalt("sibling-1", linkeddevice.SiblingSalt{}); err == nil {
		t.Fatal("an empty salt was accepted")
	}
}
