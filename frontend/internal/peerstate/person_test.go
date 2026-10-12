package peerstate_test

import (
	"errors"
	"testing"

	"haoma-frontend/internal/peerstate"
	"haoma-frontend/internal/store"
)

func TestGetPerson_UnknownIsNotAnError(t *testing.T) {
	m, _ := newMeta(t)
	if _, err := m.GetPerson("fp-nobody"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown person = %v, want ErrNotFound", err)
	}
	if _, err := m.GetPerson(""); err == nil {
		t.Error("an empty fingerprint should be refused, not treated as a wildcard")
	}
}

func TestMergePerson_FillsWhatThisDeviceDoesNotHave(t *testing.T) {
	m, _ := newMeta(t)
	changed, err := m.MergePerson("fp-alice", peerstate.MetaRecord{Nick: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("first description of a person should report a change")
	}
	got, err := m.GetPerson("fp-alice")
	if err != nil {
		t.Fatal(err)
	}
	if got.Nick != "alice" {
		t.Errorf("nick = %q, want the unstamped inherited name", got.Nick)
	}
	if got.Fingerprint != "fp-alice" {
		t.Errorf("fingerprint = %q, want it stamped on the record", got.Fingerprint)
	}
}

func TestMergePerson_NewerStampWins_OlderIsIgnored(t *testing.T) {
	m, _ := newMeta(t)
	if _, err := m.MergePerson("fp-alice", peerstate.MetaRecord{Nick: "old", NickAt: 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.MergePerson("fp-alice", peerstate.MetaRecord{Nick: "new", NickAt: 200}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.MergePerson("fp-alice", peerstate.MetaRecord{Nick: "stale", NickAt: 150}); err != nil {
		t.Fatal(err)
	}
	got, _ := m.GetPerson("fp-alice")
	if got.Nick != "new" {
		t.Errorf("nick = %q, want the newest stamp to hold", got.Nick)
	}
	if got.NickAt != 200 {
		t.Errorf("nick stamp = %d, want 200", got.NickAt)
	}
}

func TestMergePerson_FieldsMergeIndependently(t *testing.T) {
	m, _ := newMeta(t)
	if _, err := m.MergePerson("fp-alice", peerstate.MetaRecord{
		Nick: "alice", NickAt: 300, Alias: "work-alice", AliasAt: 100,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := m.MergePerson("fp-alice", peerstate.MetaRecord{
		Nick: "ali", NickAt: 200, Alias: "climbing partner", AliasAt: 400,
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := m.GetPerson("fp-alice")
	if got.Nick != "alice" {
		t.Errorf("nick = %q — an older nick rode in on a newer alias", got.Nick)
	}
	if got.Alias != "climbing partner" {
		t.Errorf("alias = %q, want the newer one", got.Alias)
	}
}

func TestMergePerson_UnstampedAbsenceNeverClearsTrust(t *testing.T) {
	m, _ := newMeta(t)
	if _, err := m.MergePerson("fp-alice", peerstate.MetaRecord{
		Verified: true, VerifiedAt: 500, Blocked: true, BlockedAt: 500,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.MergePerson("fp-alice", peerstate.MetaRecord{Nick: "alice"}); err != nil {
		t.Fatal(err)
	}
	got, _ := m.GetPerson("fp-alice")
	if !got.Verified {
		t.Error("verification was cleared by a record that simply did not mention it")
	}
	if !got.Blocked {
		t.Error("a block was cleared by a record that simply did not mention it")
	}
}

func TestMergePerson_NewerStampCanClearTrust(t *testing.T) {
	m, _ := newMeta(t)
	if _, err := m.MergePerson("fp-alice", peerstate.MetaRecord{Blocked: true, BlockedAt: 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.MergePerson("fp-alice", peerstate.MetaRecord{Blocked: false, BlockedAt: 200}); err != nil {
		t.Fatal(err)
	}
	got, _ := m.GetPerson("fp-alice")
	if got.Blocked {
		t.Error("an explicit, newer unblock should hold")
	}
}

func TestMergePerson_RepeatReportsNoChange(t *testing.T) {
	m, _ := newMeta(t)
	rec := peerstate.MetaRecord{Nick: "alice", NickAt: 100}
	if _, err := m.MergePerson("fp-alice", rec); err != nil {
		t.Fatal(err)
	}
	changed, err := m.MergePerson("fp-alice", rec)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("re-applying an identical record reported a change")
	}
}

func TestMergePerson_RefusesEmptyFingerprint(t *testing.T) {
	m, _ := newMeta(t)
	if _, err := m.MergePerson("", peerstate.MetaRecord{Nick: "nobody"}); err == nil {
		t.Error("a person with no fingerprint cannot be filed and must be refused")
	}
}

func TestPersons_GroupsPairingsByHuman(t *testing.T) {
	m, _ := newMeta(t)
	for _, pid := range []string{"peer-1", "peer-2"} {
		if _, _, err := m.SetFingerprint(pid, "fp-alice"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := m.SetFingerprint("peer-3", "fp-bob"); err != nil {
		t.Fatal(err)
	}
	people, err := m.Persons()
	if err != nil {
		t.Fatal(err)
	}
	if len(people) != 2 {
		t.Fatalf("got %d people from three pairings, want 2", len(people))
	}
	for _, p := range people {
		if p.Fingerprint == "fp-alice" && len(p.PeerIDs) != 2 {
			t.Errorf("alice has %v, want both pairings", p.PeerIDs)
		}
	}
}

func TestPersons_SkipsPairingsWithNoFingerprintYet(t *testing.T) {
	m, _ := newMeta(t)
	if _, err := m.SetNick("peer-unpaired", "half-known", 100); err != nil {
		t.Fatal(err)
	}
	people, err := m.Persons()
	if err != nil {
		t.Fatal(err)
	}
	if len(people) != 0 {
		t.Errorf("got %v, want nobody — that pairing has no person to name", people)
	}
}

func TestPersons_IncludesPeopleWithNoPairingHere(t *testing.T) {
	m, _ := newMeta(t)
	if _, err := m.MergePerson("fp-inherited", peerstate.MetaRecord{Nick: "never met"}); err != nil {
		t.Fatal(err)
	}
	people, err := m.Persons()
	if err != nil {
		t.Fatal(err)
	}
	if len(people) != 1 {
		t.Fatalf("got %d, want the inherited person", len(people))
	}
	if people[0].Meta.Nick != "never met" {
		t.Errorf("nick = %q, want the inherited name to travel on", people[0].Meta.Nick)
	}
	if len(people[0].PeerIDs) != 0 {
		t.Errorf("routing ids = %v, want none — this device has no pairing with them", people[0].PeerIDs)
	}
}

func TestPeerIDsByFingerprint_RefusesEmpty(t *testing.T) {
	m, _ := newMeta(t)
	if _, err := m.PeerIDsByFingerprint(""); err == nil {
		t.Error("an empty fingerprint would match every un-seeded peer and must be refused")
	}
}
