package peerstate_test

import (
	"testing"

	"haoma-frontend/internal/peerstate"
)

func TestTombstone_NewerWinsAndIsNotAPerson(t *testing.T) {
	m, _ := newMeta(t)
	if _, found, err := m.Tombstone("fp-dave"); err != nil || found {
		t.Fatalf("a fresh store holds a tombstone: found=%v err=%v", found, err)
	}
	held, moved, err := m.MarkDeleted("fp-dave", 100, 150)
	if err != nil || !moved || held.DeletedAt != 100 || held.AppliedAt != 150 || held.Done {
		t.Fatalf("first delete: %+v moved=%v err=%v", held, moved, err)
	}
	if err := m.MarkDeleteDone("fp-dave", 100); err != nil {
		t.Fatal(err)
	}
	for _, at := range []int64{100, 90} {
		held, moved, err = m.MarkDeleted("fp-dave", at, 200)
		if err != nil || moved || held.DeletedAt != 100 || !held.Done {
			t.Fatalf("a delete at %d: %+v moved=%v err=%v, want the held one unchanged", at, held, moved, err)
		}
	}
	held, moved, err = m.MarkDeleted("fp-dave", 300, 310)
	if err != nil || !moved || held.DeletedAt != 300 || held.Done {
		t.Fatalf("a newer delete: %+v moved=%v err=%v, want it held and not done", held, moved, err)
	}

	if err := m.MarkDeleteDone("fp-dave", 100); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := m.Tombstone("fp-dave"); got.Done {
		t.Fatal("the older delete's done marked the newer one")
	}
	persons, err := m.Persons()
	if err != nil || len(persons) != 0 {
		t.Fatalf("a tombstone was read as a person: %+v err=%v", persons, err)
	}
	all, err := m.Tombstones()
	if err != nil || len(all) != 1 || all[0].Fingerprint != "fp-dave" || all[0].DeletedAt != 300 {
		t.Fatalf("Tombstones = %+v err=%v", all, err)
	}
}

func TestTombstone_Covers(t *testing.T) {
	cases := []struct {
		name     string
		tomb     peerstate.Tombstone
		pairedAt int64
		want     bool
	}{
		{"older than both", peerstate.Tombstone{DeletedAt: 100, AppliedAt: 100}, 50, true},
		{"no stamp", peerstate.Tombstone{DeletedAt: 100, AppliedAt: 100}, 0, true},
		{"after the delete", peerstate.Tombstone{DeletedAt: 100, AppliedAt: 100}, 101, false},
		{"the deleting clock ahead; paired here after applying", peerstate.Tombstone{DeletedAt: 500, AppliedAt: 200}, 300, false},
		{"the deleting clock behind; paired here before the delete", peerstate.Tombstone{DeletedAt: 100, AppliedAt: 400}, 150, false},
	}
	for _, c := range cases {
		if got := c.tomb.Covers(c.pairedAt); got != c.want {
			t.Errorf("%s: Covers(%d) = %v, want %v", c.name, c.pairedAt, got, c.want)
		}
	}
}

func TestForget_RemovesTheRecordsAndKeepsTheTombstone(t *testing.T) {
	m, _ := newMeta(t)
	if _, _, err := m.SetFingerprintAt("peer-1", "fp-dave", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := m.MergePerson("fp-dave", peerstate.MetaRecord{Nick: "dave"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.MarkDeleted("fp-dave", 20, 20); err != nil {
		t.Fatal(err)
	}
	if err := m.ForgetPeer("peer-1"); err != nil {
		t.Fatal(err)
	}
	if err := m.ForgetPerson("fp-dave"); err != nil {
		t.Fatal(err)
	}

	if err := m.ForgetPeer("peer-1"); err != nil {
		t.Fatal(err)
	}
	if ids, _ := m.PeerIDsByFingerprint("fp-dave"); len(ids) != 0 {
		t.Fatalf("pairings left: %v", ids)
	}
	if has, _ := m.HasPerson("fp-dave"); has {
		t.Fatal("the person record is still here")
	}
	if _, found, _ := m.Tombstone("fp-dave"); !found {
		t.Fatal("the tombstone went with the records")
	}
}
