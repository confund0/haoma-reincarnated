package peerstate

import (
	"testing"

	"haoma-frontend/internal/store"
)

func TestSetAliasWritesThePersonRecordOnly(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Lock() })
	m := NewMeta(st)

	if _, _, err := m.SetFingerprint("peer-1", "fp-alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetAlias("peer-1", "Boss", 1000); err != nil {
		t.Fatal(err)
	}

	peer, err := m.peerRecord("peer-1")
	if err != nil {
		t.Fatal(err)
	}
	if peer.Alias != "" || peer.AliasAt != 0 {
		t.Errorf("peer record holds alias=%q at=%d, want nothing: the person is the only home", peer.Alias, peer.AliasAt)
	}
	person, err := m.GetPerson("fp-alice")
	if err != nil {
		t.Fatal(err)
	}
	if person.Alias != "Boss" || person.AliasAt != 1000 {
		t.Errorf("person record holds alias=%q at=%d, want Boss/1000", person.Alias, person.AliasAt)
	}
}
