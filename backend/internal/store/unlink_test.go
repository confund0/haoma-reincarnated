package store

import (
	"errors"
	"testing"
)

func TestUnlink_ArmingWritesTheProofOntoTheNarrowing(t *testing.T) {
	dir := t.TempDir()
	s, err := Unlock(dir, "pw")
	if err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	defer s.Lock()

	rec := UnlinkPending{PeerID: "sibling-1", Deadline: 1700000000, StartedAt: 1699990000}
	if err := s.PutUnlinkPending(rec); err != nil {
		t.Fatalf("PutUnlinkPending: %v", err)
	}
	if got, err := s.GetUnlinkPending(); err != nil {
		t.Fatalf("GetUnlinkPending: %v", err)
	} else if got.Armed() {
		t.Fatal("a fresh narrowing already answers with a proof")
	}

	if err := s.ArmUnlinkSelfErase("sibling-1", "deadbeef"); err != nil {
		t.Fatalf("ArmUnlinkSelfErase: %v", err)
	}
	got, err := s.GetUnlinkPending()
	if err != nil {
		t.Fatalf("GetUnlinkPending: %v", err)
	}
	if !got.Armed() {
		t.Fatalf("the narrowing was not armed: %+v", got)
	}
	if got.Proof != "deadbeef" {
		t.Fatalf("proof = %q, want the armed one", got.Proof)
	}

	if got.Deadline != rec.Deadline || got.StartedAt != rec.StartedAt {
		t.Fatalf("arming moved the window: %+v", got)
	}

	if err := s.ArmUnlinkSelfErase("sibling-1", "deadbeef"); err != nil {
		t.Fatalf("arming twice: %v", err)
	}
}

func TestUnlink_ArmingRefusesWhatItCannotAnswerFor(t *testing.T) {
	dir := t.TempDir()
	s, err := Unlock(dir, "pw")
	if err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	defer s.Lock()

	if err := s.ArmUnlinkSelfErase("sibling-1", "deadbeef"); err == nil {
		t.Fatal("arming succeeded with no narrowing in force")
	}

	if err := s.PutUnlinkPending(UnlinkPending{
		PeerID: "sibling-1", Deadline: 1700000000, StartedAt: 1699990000,
	}); err != nil {
		t.Fatalf("PutUnlinkPending: %v", err)
	}
	if err := s.ArmUnlinkSelfErase("another-device", "deadbeef"); !errors.Is(err, ErrUnlinkAlreadyPending) {
		t.Fatalf("arming another device answered %v, want the in-flight refusal", err)
	}
	if err := s.ArmUnlinkSelfErase("sibling-1", ""); err == nil {
		t.Fatal("arming succeeded with no proof to answer with")
	}

	got, err := s.GetUnlinkPending()
	if err != nil {
		t.Fatalf("GetUnlinkPending: %v", err)
	}
	if got.Armed() {
		t.Fatalf("a refused arming left a proof behind: %+v", got)
	}
}

func TestUnlink_ClearingTakesTheProof(t *testing.T) {
	dir := t.TempDir()
	s, err := Unlock(dir, "pw")
	if err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	defer s.Lock()

	if err := s.PutUnlinkPending(UnlinkPending{
		PeerID: "sibling-1", Deadline: 1700000000, StartedAt: 1699990000,
	}); err != nil {
		t.Fatalf("PutUnlinkPending: %v", err)
	}
	if err := s.ArmUnlinkSelfErase("sibling-1", "deadbeef"); err != nil {
		t.Fatalf("ArmUnlinkSelfErase: %v", err)
	}
	if err := s.ClearUnlinkPending(); err != nil {
		t.Fatalf("ClearUnlinkPending: %v", err)
	}

	got, err := s.GetUnlinkPending()
	if err != nil {
		t.Fatalf("GetUnlinkPending: %v", err)
	}
	if got.InFlight() || got.Proof != "" {
		t.Fatalf("the proof outlived the narrowing: %+v", got)
	}
}
