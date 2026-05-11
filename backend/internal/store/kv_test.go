package store

import (
	"bytes"
	"errors"
	"testing"

	"github.com/dgraph-io/badger/v4"
)

func TestContact_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := Unlock(dir, "pw")
	if err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	defer s.Lock()

	pub := ContactID("alice-pubkey-bytes")
	if err := s.PutContact(pub, []byte("alice record")); err != nil {
		t.Fatalf("PutContact: %v", err)
	}
	got, err := s.GetContact(pub)
	if err != nil {
		t.Fatalf("GetContact: %v", err)
	}
	if !bytes.Equal(got, []byte("alice record")) {
		t.Errorf("value = %q, want %q", got, "alice record")
	}
}

func TestContact_NotFound(t *testing.T) {
	dir := t.TempDir()
	s, _ := Unlock(dir, "pw")
	defer s.Lock()
	if _, err := s.GetContact(ContactID("nobody")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestContact_Delete(t *testing.T) {
	dir := t.TempDir()
	s, _ := Unlock(dir, "pw")
	defer s.Lock()
	pub := ContactID("x")
	if err := s.PutContact(pub, []byte("v")); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteContact(pub); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetContact(pub); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestContact_List(t *testing.T) {
	dir := t.TempDir()
	s, _ := Unlock(dir, "pw")
	defer s.Lock()
	pubs := []ContactID{ContactID("a"), ContactID("b"), ContactID("c")}
	for _, p := range pubs {
		if err := s.PutContact(p, []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListContacts()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d contacts, want 3", len(got))
	}
	for i, p := range got {
		if !bytes.Equal(p, pubs[i]) {
			t.Errorf("[%d] = %q, want %q", i, p, pubs[i])
		}
	}
}

func TestState_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, _ := Unlock(dir, "pw")
	defer s.Lock()
	if err := s.PutState("rotation", []byte("state-blob")); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetState("rotation")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte("state-blob")) {
		t.Errorf("got %q", got)
	}
}

func TestState_NotFound(t *testing.T) {
	dir := t.TempDir()
	s, _ := Unlock(dir, "pw")
	defer s.Lock()
	if _, err := s.GetState("absent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestState_Delete(t *testing.T) {
	dir := t.TempDir()
	s, _ := Unlock(dir, "pw")
	defer s.Lock()
	if err := s.PutState("k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteState("k"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetState("k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestPrefix_Isolation(t *testing.T) {
	dir := t.TempDir()
	s, _ := Unlock(dir, "pw")
	defer s.Lock()

	pub := ContactID("shared-suffix")
	if err := s.PutContact(pub, []byte("contact-val")); err != nil {
		t.Fatal(err)
	}
	if err := s.PutState("shared-suffix", []byte("state-val")); err != nil {
		t.Fatal(err)
	}

	c, err := s.GetContact(pub)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(c, []byte("contact-val")) {
		t.Errorf("contact = %q, want contact-val", c)
	}
	st, err := s.GetState("shared-suffix")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(st, []byte("state-val")) {
		t.Errorf("state = %q, want state-val", st)
	}
	contacts, _ := s.ListContacts()
	if len(contacts) != 1 {
		t.Errorf("ListContacts returned %d entries, want 1 (state key leaked?)", len(contacts))
	}
}

func TestKV_ErrLockedAfterLock(t *testing.T) {
	dir := t.TempDir()
	s, _ := Unlock(dir, "pw")
	if err := s.Lock(); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		run  func() error
	}{
		{"PutContact", func() error { return s.PutContact(ContactID("x"), []byte("v")) }},
		{"GetContact", func() error { _, err := s.GetContact(ContactID("x")); return err }},
		{"DeleteContact", func() error { return s.DeleteContact(ContactID("x")) }},
		{"ListContacts", func() error { _, err := s.ListContacts(); return err }},
		{"PutAddrIndex", func() error { return s.PutAddrIndex("x", ContactID("p")) }},
		{"GetAddrIndex", func() error { _, err := s.GetAddrIndex("x"); return err }},
		{"DeleteAddrIndex", func() error { return s.DeleteAddrIndex("x") }},
		{"PutState", func() error { return s.PutState("x", []byte("v")) }},
		{"GetState", func() error { _, err := s.GetState("x"); return err }},
		{"DeleteState", func() error { return s.DeleteState("x") }},
		{"View", func() error { return s.View(func(txn *badger.Txn) error { return nil }) }},
		{"Update", func() error { return s.Update(func(txn *badger.Txn) error { return nil }) }},
	}
	for _, c := range cases {
		if err := c.run(); !errors.Is(err, ErrLocked) {
			t.Errorf("%s: err = %v, want ErrLocked", c.name, err)
		}
	}
}

func TestAddrIndex_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, _ := Unlock(dir, "pw")
	defer s.Lock()

	if err := s.PutAddrIndex("alice-slot-0", ContactID("peer-1-hex")); err != nil {
		t.Fatalf("PutAddrIndex: %v", err)
	}
	got, err := s.GetAddrIndex("alice-slot-0")
	if err != nil {
		t.Fatalf("GetAddrIndex: %v", err)
	}
	if !bytes.Equal(got, []byte("peer-1-hex")) {
		t.Errorf("GetAddrIndex = %q, want peer-1-hex", got)
	}
}

func TestAddrIndex_MissReturnsErrNotFound(t *testing.T) {
	dir := t.TempDir()
	s, _ := Unlock(dir, "pw")
	defer s.Lock()

	if _, err := s.GetAddrIndex("no-such-addr"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestAddrIndex_DeleteRemoves(t *testing.T) {
	dir := t.TempDir()
	s, _ := Unlock(dir, "pw")
	defer s.Lock()

	if err := s.PutAddrIndex("alice-slot-0", ContactID("p")); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAddrIndex("alice-slot-0"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAddrIndex("alice-slot-0"); !errors.Is(err, ErrNotFound) {
		t.Errorf("after Delete: err = %v, want ErrNotFound", err)
	}
}

func TestAddrIndex_DoesNotLeakIntoListContacts(t *testing.T) {
	dir := t.TempDir()
	s, _ := Unlock(dir, "pw")
	defer s.Lock()

	if err := s.PutContact(ContactID("peer-id"), []byte("contact")); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAddrIndex("onion-xyz", ContactID("peer-id")); err != nil {
		t.Fatal(err)
	}

	contacts, err := s.ListContacts()
	if err != nil {
		t.Fatal(err)
	}
	if len(contacts) != 1 {
		t.Errorf("ListContacts returned %d, want 1 (addr: leaked?)", len(contacts))
	}
	if !bytes.Equal(contacts[0], ContactID("peer-id")) {
		t.Errorf("ListContacts[0] = %q, want peer-id", contacts[0])
	}
}

func TestPassthrough_ViewUpdate(t *testing.T) {
	dir := t.TempDir()
	s, _ := Unlock(dir, "pw")
	defer s.Lock()

	if err := s.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte("xport-queue:msg1"), []byte("queued"))
	}); err != nil {
		t.Fatal(err)
	}

	err := s.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte("xport-queue:msg1"))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			if !bytes.Equal(val, []byte("queued")) {
				t.Errorf("got %q, want queued", val)
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}
