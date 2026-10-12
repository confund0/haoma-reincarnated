package peerstate_test

import (
	"testing"

	"haoma-frontend/internal/peerstate"
)

const fpAlice = "fp-alice"

func seedPairing(t *testing.T, m *peerstate.Meta, peerID, fingerprint string) {
	t.Helper()
	if _, _, err := m.SetFingerprint(peerID, fingerprint); err != nil {
		t.Fatal(err)
	}
}

func TestGet_AnAliasFromASiblingWinsOverTheContactsOwnNick(t *testing.T) {
	m, _ := newMeta(t)
	seedPairing(t, m, "peer-1", fpAlice)
	if _, err := m.SetNick("peer-1", "alice", 500); err != nil {
		t.Fatal(err)
	}

	if _, err := m.MergePerson(fpAlice, peerstate.MetaRecord{Alias: "Boss", AliasAt: 1000}); err != nil {
		t.Fatal(err)
	}

	rec, err := m.Get("peer-1")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Alias != "Boss" {
		t.Errorf("alias = %q, want Boss (the sibling's alias, read under the peer id)", rec.Alias)
	}
	if rec.Nick != "alice" {
		t.Errorf("nick = %q, want alice (the contact's own name is still there)", rec.Nick)
	}
	if got := peerstate.Resolve(rec, "peer-1"); got != "Boss" {
		t.Errorf("label = %q, want Boss (alias beats nick)", got)
	}
}

func TestSetAlias_ReadsBackUnderEveryPairingWithThatPerson(t *testing.T) {
	m, _ := newMeta(t)
	seedPairing(t, m, "peer-1", fpAlice)
	seedPairing(t, m, "peer-2", fpAlice)

	if _, err := m.SetAlias("peer-1", "Boss", 1000); err != nil {
		t.Fatal(err)
	}
	rec, err := m.Get("peer-2")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Alias != "Boss" || rec.AliasAt != 1000 {
		t.Errorf("alias=%q at=%d under the second pairing, want Boss/1000", rec.Alias, rec.AliasAt)
	}
}

func TestSetAlias_LandsInTheSetASiblingIsSent(t *testing.T) {
	m, _ := newMeta(t)
	seedPairing(t, m, "peer-1", fpAlice)
	if _, err := m.SetAlias("peer-1", "Boss", 1000); err != nil {
		t.Fatal(err)
	}

	persons, err := m.Persons()
	if err != nil {
		t.Fatal(err)
	}
	if len(persons) != 1 {
		t.Fatalf("persons = %d, want 1", len(persons))
	}
	if persons[0].Fingerprint != fpAlice {
		t.Errorf("fingerprint = %q, want %q", persons[0].Fingerprint, fpAlice)
	}
	if persons[0].Meta.Alias != "Boss" || persons[0].Meta.AliasAt != 1000 {
		t.Errorf("alias=%q at=%d, want Boss/1000", persons[0].Meta.Alias, persons[0].Meta.AliasAt)
	}
}

func TestSetAlias_ClearingFallsBackToTheNick(t *testing.T) {
	m, _ := newMeta(t)
	seedPairing(t, m, "peer-1", fpAlice)
	if _, err := m.SetNick("peer-1", "alice", 500); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetAlias("peer-1", "Boss", 1000); err != nil {
		t.Fatal(err)
	}
	changed, err := m.SetAlias("peer-1", "", 2000)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("clearing an alias should report a change")
	}
	rec, _ := m.Get("peer-1")
	if rec.Alias != "" {
		t.Errorf("alias = %q, want empty", rec.Alias)
	}
	if rec.AliasAt != 2000 {
		t.Errorf("AliasAt = %d, want 2000 (a clear is a write and carries its stamp)", rec.AliasAt)
	}
	if got := peerstate.Resolve(rec, "peer-1"); got != "alice" {
		t.Errorf("label = %q, want alice", got)
	}
}

func TestSetAlias_ChangedReportsARealMove(t *testing.T) {
	m, _ := newMeta(t)
	seedPairing(t, m, "peer-1", fpAlice)
	changed, err := m.SetAlias("peer-1", "Boss", 1000)
	if err != nil || !changed {
		t.Fatalf("first write: changed=%v err=%v, want changed=true", changed, err)
	}
	changed, err = m.SetAlias("peer-1", "Boss", 2000)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("re-writing the same alias should report no change")
	}
	if rec, _ := m.Get("peer-1"); rec.AliasAt != 2000 {
		t.Errorf("AliasAt = %d, want 2000 (a same-value write still stamps)", rec.AliasAt)
	}
}

func TestSetAlias_WithNoFingerprintYet(t *testing.T) {
	m, _ := newMeta(t)
	if _, err := m.SetAlias("peer-1", "Boss", 1000); err != nil {
		t.Fatal(err)
	}
	if rec, _ := m.Get("peer-1"); rec.Alias != "Boss" {
		t.Errorf("alias = %q before the fingerprint, want Boss", rec.Alias)
	}
	seedPairing(t, m, "peer-1", fpAlice)
	if rec, _ := m.Get("peer-1"); rec.Alias != "Boss" {
		t.Errorf("alias = %q after the fingerprint, want Boss", rec.Alias)
	}

	if _, err := m.SetAlias("peer-1", "Chief", 2000); err != nil {
		t.Fatal(err)
	}
	if rec, _ := m.Get("peer-1"); rec.Alias != "Chief" {
		t.Errorf("alias = %q, want Chief", rec.Alias)
	}
}

func TestGet_AnInheritedContactIsNamedByTheirPersonRecord(t *testing.T) {
	m, _ := newMeta(t)

	if _, err := m.MergePerson(fpAlice, peerstate.MetaRecord{
		Nick: "alice", Alias: "Boss", AliasAt: 1000,
		Verified: true, VerifiedAt: 1000,
	}); err != nil {
		t.Fatal(err)
	}

	seedPairing(t, m, "peer-1", fpAlice)

	rec, err := m.Get("peer-1")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Alias != "Boss" || rec.Nick != "alice" || !rec.Verified {
		t.Errorf("record = %+v, want the inherited alias, nick and trust bit", rec)
	}
}
