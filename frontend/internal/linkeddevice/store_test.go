package linkeddevice_test

import (
	"reflect"
	"testing"

	"haoma-frontend/internal/crdt"
	"haoma-frontend/internal/linkeddevice"
	"haoma-frontend/internal/store"
)

func init() {
	store.DefaultKDFParams = store.KDFParams{
		Time: 1, Memory: 8 * 1024, Threads: 2, KeyLen: 32, SaltLen: 16,
	}
}

func newStore(t *testing.T) (*linkeddevice.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Lock() })
	return linkeddevice.New(st), dir
}

func TestEmptyByDefault(t *testing.T) {
	s, _ := newStore(t)
	members, err := s.Members()
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 0 {
		t.Fatalf("fresh store should have no siblings, got %v", members)
	}
}

func TestLinkThenMembers(t *testing.T) {
	s, _ := newStore(t)
	changed, err := s.Link("dev-a", 100)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("first link of a device should report changed")
	}
	members, _ := s.Members()
	if got, want := members, []string{"dev-a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Members = %v, want %v", got, want)
	}
}

func TestReLinkIsAuditOnly(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Link("dev-a", 100); err != nil {
		t.Fatal(err)
	}
	changed, err := s.Link("dev-a", 200)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("re-link of an existing device must report changed=false")
	}
}

func TestUnlinkDrops(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Link("dev-a", 100); err != nil {
		t.Fatal(err)
	}
	changed, err := s.Unlink("dev-a", 200)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("unlinking a live device should report changed")
	}
	members, _ := s.Members()
	if len(members) != 0 {
		t.Fatalf("after unlink expected no siblings, got %v", members)
	}
}

func TestUnlinkIsPermanent(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Link("dev-a", 100); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Unlink("dev-a", 200); err != nil {
		t.Fatal(err)
	}

	changed, err := s.Link("dev-a", 300)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("re-linking an unlinked device must not resurrect it")
	}
	members, _ := s.Members()
	if len(members) != 0 {
		t.Fatalf("unlinked device must stay gone, got %v", members)
	}
}

func TestEmptyPeerIDRejected(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Link("", 100); err == nil {
		t.Fatal("Link with empty peer id should error")
	}
	if _, err := s.Unlink("", 100); err == nil {
		t.Fatal("Unlink with empty peer id should error")
	}
}

func TestPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	st1, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	s := linkeddevice.New(st1)
	if _, err := s.Link("dev-a", 100); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Link("dev-b", 100); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Unlink("dev-a", 200); err != nil {
		t.Fatal(err)
	}

	if err := st1.Lock(); err != nil {
		t.Fatal(err)
	}

	st2, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st2.Lock() })
	s2 := linkeddevice.New(st2)

	members, err := s2.Members()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := members, []string{"dev-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after reopen Members = %v, want %v (dev-a unlinked, dev-b live)", got, want)
	}
}

func TestMergeFromSibling(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Link("dev-a", 100); err != nil {
		t.Fatal(err)
	}

	incoming := crdt.NewTwoPSet()
	incoming.Add("dev-b", 110)
	incoming.Remove("dev-a", 150)

	changed, err := s.Merge(incoming)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("merge that adds dev-b and drops dev-a should report changed")
	}
	members, _ := s.Members()
	if got, want := members, []string{"dev-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after merge Members = %v, want %v", got, want)
	}
}
