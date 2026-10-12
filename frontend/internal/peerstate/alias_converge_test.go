package peerstate_test

import (
	"testing"

	"haoma-frontend/internal/peerstate"
)

func mergeFrom(t *testing.T, m *peerstate.Meta, fp string, in peerstate.MetaRecord) {
	t.Helper()
	in.Fingerprint = fp
	if _, err := m.MergePerson(fp, in); err != nil {
		t.Fatal(err)
	}
}

func TestMergePerson_ASiblingsOlderNameDoesNotUndoAClear(t *testing.T) {
	m, _ := newMeta(t)
	seedPairing(t, m, "peer-1", fpAlice)
	if _, err := m.SetAlias("peer-1", "Boss", 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetAlias("peer-1", "", 2000); err != nil {
		t.Fatal(err)
	}

	mergeFrom(t, m, fpAlice, peerstate.MetaRecord{Alias: "Boss", AliasAt: 1000})

	rec, err := m.Get("peer-1")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Alias != "" {
		t.Errorf("alias = %q, want empty (the clear stands)", rec.Alias)
	}
	if rec.AliasAt != 2000 {
		t.Errorf("AliasAt = %d, want 2000 (the clear keeps its own stamp)", rec.AliasAt)
	}
}

func TestMergePerson_ASiblingsNewerClearTakesTheAliasAway(t *testing.T) {
	m, _ := newMeta(t)
	seedPairing(t, m, "peer-1", fpAlice)
	if _, err := m.SetAlias("peer-1", "Boss", 1000); err != nil {
		t.Fatal(err)
	}

	mergeFrom(t, m, fpAlice, peerstate.MetaRecord{Alias: "", AliasAt: 2000})

	rec, _ := m.Get("peer-1")
	if rec.Alias != "" {
		t.Errorf("alias = %q, want empty", rec.Alias)
	}
	if rec.AliasAt != 2000 {
		t.Errorf("AliasAt = %d, want 2000", rec.AliasAt)
	}
}

func TestMergePerson_TheNewerOfTwoClearsSetsTheStamp(t *testing.T) {
	m, _ := newMeta(t)
	seedPairing(t, m, "peer-1", fpAlice)
	if _, err := m.SetAlias("peer-1", "Boss", 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetAlias("peer-1", "", 1500); err != nil {
		t.Fatal(err)
	}

	mergeFrom(t, m, fpAlice, peerstate.MetaRecord{Alias: "", AliasAt: 2000})

	rec, _ := m.Get("peer-1")
	if rec.AliasAt != 2000 {
		t.Errorf("AliasAt = %d, want 2000 (the newer clear sets the stamp)", rec.AliasAt)
	}

	mergeFrom(t, m, fpAlice, peerstate.MetaRecord{Alias: "Boss", AliasAt: 1800})

	if rec, _ = m.Get("peer-1"); rec.Alias != "" {
		t.Errorf("alias = %q, want empty (1800 loses to 2000)", rec.Alias)
	}
}

func TestMergePerson_EqualStampsSettleTheSameWayOnBothDevices(t *testing.T) {
	left, _ := newMeta(t)
	seedPairing(t, left, "peer-1", fpAlice)
	if _, err := left.SetAlias("peer-1", "Anna", 1000); err != nil {
		t.Fatal(err)
	}
	mergeFrom(t, left, fpAlice, peerstate.MetaRecord{Alias: "Zoe", AliasAt: 1000})

	right, _ := newMeta(t)
	seedPairing(t, right, "peer-1", fpAlice)
	if _, err := right.SetAlias("peer-1", "Zoe", 1000); err != nil {
		t.Fatal(err)
	}
	mergeFrom(t, right, fpAlice, peerstate.MetaRecord{Alias: "Anna", AliasAt: 1000})

	l, _ := left.Get("peer-1")
	r, _ := right.Get("peer-1")
	if l.Alias != r.Alias {
		t.Errorf("left = %q, right = %q — the tie settled two different ways", l.Alias, r.Alias)
	}
}

func TestSetAlias_AWriteFromBehindStillBeatsTheSibling(t *testing.T) {
	m, _ := newMeta(t)
	seedPairing(t, m, "peer-1", fpAlice)

	mergeFrom(t, m, fpAlice, peerstate.MetaRecord{Alias: "Zoe", AliasAt: 1030})

	if _, err := m.SetAlias("peer-1", "Anna", 1000); err != nil {
		t.Fatal(err)
	}
	rec, _ := m.Get("peer-1")
	if rec.Alias != "Anna" {
		t.Fatalf("alias = %q, want Anna", rec.Alias)
	}
	if rec.AliasAt <= 1030 {
		t.Errorf("AliasAt = %d, want more than 1030 (the write must outrank the sibling)", rec.AliasAt)
	}

	mergeFrom(t, m, fpAlice, peerstate.MetaRecord{Alias: "Zoe", AliasAt: 1030})

	if rec, _ = m.Get("peer-1"); rec.Alias != "Anna" {
		t.Errorf("alias = %q, want Anna (the newer write stands)", rec.Alias)
	}
}

func TestSetFingerprint_AnAliasWrittenBeforeItMovesToThePerson(t *testing.T) {
	m, _ := newMeta(t)
	if _, err := m.SetAlias("peer-1", "Boss", 1000); err != nil {
		t.Fatal(err)
	}
	seedPairing(t, m, "peer-1", fpAlice)

	person, err := m.GetPerson(fpAlice)
	if err != nil {
		t.Fatal(err)
	}
	if person.Alias != "Boss" || person.AliasAt != 1000 {
		t.Errorf("person alias=%q at=%d, want Boss/1000", person.Alias, person.AliasAt)
	}

	persons, err := m.Persons()
	if err != nil {
		t.Fatal(err)
	}
	if len(persons) != 1 || persons[0].Meta.Alias != "Boss" {
		t.Fatalf("persons = %+v, want one carrying Boss", persons)
	}

	if _, err := m.SetAlias("peer-1", "", 2000); err != nil {
		t.Fatal(err)
	}
	rec, _ := m.Get("peer-1")
	if rec.Alias != "" {
		t.Errorf("alias = %q, want empty (nothing left under the peer id)", rec.Alias)
	}
}
