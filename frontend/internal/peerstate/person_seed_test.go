package peerstate_test

import (
	"errors"
	"testing"

	"haoma-frontend/internal/peerstate"
	"haoma-frontend/internal/store"
)

func TestSeedPersonFromPairing_WritesTheContactAPairingJustLearned(t *testing.T) {
	m, _ := newMeta(t)
	const peerID, fingerprint = "peer-1", "fp-alice"

	if _, _, err := m.SetFingerprint(peerID, fingerprint); err != nil {
		t.Fatal(err)
	}

	if _, err := m.SetNick(peerID, "alice", 0); err != nil {
		t.Fatal(err)
	}

	gotFP, outcome, err := m.SeedPersonFromPairing(peerID)
	if err != nil {
		t.Fatal(err)
	}
	if gotFP != fingerprint {
		t.Errorf("fingerprint = %q, want %q", gotFP, fingerprint)
	}
	if outcome != peerstate.PersonSeedCreated {
		t.Errorf("outcome = %q, want created", outcome)
	}
	rec, err := m.GetPerson(fingerprint)
	if err != nil {
		t.Fatalf("the person the push reads is not on record: %v", err)
	}
	if rec.Nick != "alice" {
		t.Errorf("nick on the person record = %q, want alice", rec.Nick)
	}
	if rec.Fingerprint != fingerprint {
		t.Errorf("fingerprint on the person record = %q, want %q", rec.Fingerprint, fingerprint)
	}
}

func TestSeedPersonFromPairing_DoesNotUndoASiblingsNewerName(t *testing.T) {
	m, _ := newMeta(t)
	const peerID, fingerprint = "peer-1", "fp-alice"

	if _, err := m.MergePerson(fingerprint, peerstate.MetaRecord{
		Nick: "alice-renamed", NickAt: 5000,
	}); err != nil {
		t.Fatal(err)
	}

	if _, _, err := m.SetFingerprint(peerID, fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetNick(peerID, "alice", 0); err != nil {
		t.Fatal(err)
	}

	_, outcome, err := m.SeedPersonFromPairing(peerID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome == peerstate.PersonSeedCreated {
		t.Error("outcome = created, but the sibling had already put this person on record")
	}
	rec, err := m.GetPerson(fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Nick != "alice-renamed" {
		t.Errorf("nick = %q, want alice-renamed — the pair-time seed overwrote a newer name", rec.Nick)
	}
	if rec.NickAt != 5000 {
		t.Errorf("nick stamp = %d, want 5000 — the seed moved the stamp it must not touch", rec.NickAt)
	}
}

func TestSeedPersonFromPairing_LeavesTheOperatorsOwnDecisionsAlone(t *testing.T) {
	m, _ := newMeta(t)
	const peerID, fingerprint = "peer-1", "fp-alice"

	if _, err := m.MergePerson(fingerprint, peerstate.MetaRecord{
		Alias: "Boss", AliasAt: 4000,
		Verified: true, VerifiedAt: 4000,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.SetFingerprint(peerID, fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetNick(peerID, "alice", 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.SeedPersonFromPairing(peerID); err != nil {
		t.Fatal(err)
	}

	rec, err := m.GetPerson(fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Alias != "Boss" || rec.AliasAt != 4000 {
		t.Errorf("alias = %q@%d, want Boss@4000", rec.Alias, rec.AliasAt)
	}
	if !rec.Verified || rec.VerifiedAt != 4000 {
		t.Errorf("verified = %v@%d, want true@4000", rec.Verified, rec.VerifiedAt)
	}
	if rec.Nick != "alice" {
		t.Errorf("nick = %q, want alice — the seed had a name to add", rec.Nick)
	}
}

func TestSeedPersonFromPairing_MidPairingWritesNobody(t *testing.T) {
	m, _ := newMeta(t)
	const peerID = "peer-1"

	if _, err := m.SetNick(peerID, "alice", 0); err != nil {
		t.Fatal(err)
	}

	fingerprint, outcome, err := m.SeedPersonFromPairing(peerID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != peerstate.PersonSeedNoFingerprint {
		t.Errorf("outcome = %q, want no_fingerprint", outcome)
	}
	if fingerprint != "" {
		t.Errorf("fingerprint = %q, want none", fingerprint)
	}
	if _, err := m.GetPerson(""); !errors.Is(err, store.ErrNotFound) && err == nil {
		t.Error("a person was filed under an empty fingerprint")
	}
}

func TestSeedPersonFromPairing_ARepeatReportsNoChange(t *testing.T) {
	m, _ := newMeta(t)
	const peerID, fingerprint = "peer-1", "fp-alice"

	if _, _, err := m.SetFingerprint(peerID, fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetNick(peerID, "alice", 0); err != nil {
		t.Fatal(err)
	}
	if _, outcome, err := m.SeedPersonFromPairing(peerID); err != nil {
		t.Fatal(err)
	} else if outcome != peerstate.PersonSeedCreated {
		t.Fatalf("first outcome = %q, want created", outcome)
	}
	_, outcome, err := m.SeedPersonFromPairing(peerID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != peerstate.PersonSeedUnchanged {
		t.Errorf("second outcome = %q, want unchanged", outcome)
	}
}
