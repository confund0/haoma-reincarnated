package store

import (
	"bytes"
	"errors"
	"os"
	"testing"

	"github.com/dgraph-io/badger/v4"
)

func TestMain(m *testing.M) {
	DefaultKDFParams = testParams()
	os.Exit(m.Run())
}

func TestUnlock_FreshStore(t *testing.T) {
	dir := t.TempDir()
	s, err := Unlock(dir, "correct horse")
	if err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if err := s.Lock(); err != nil {
		t.Fatalf("Lock: %v", err)
	}
}

func TestUnlock_RoundTrip_SamePassphrase(t *testing.T) {
	dir := t.TempDir()
	s, err := Unlock(dir, "pw")
	if err != nil {
		t.Fatalf("Unlock (first): %v", err)
	}
	if err := s.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte("k"), []byte("v"))
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := s.Lock(); err != nil {
		t.Fatalf("Lock: %v", err)
	}

	s2, err := Unlock(dir, "pw")
	if err != nil {
		t.Fatalf("Unlock (second): %v", err)
	}
	defer s2.Lock()

	err = s2.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte("k"))
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			if !bytes.Equal(val, []byte("v")) {
				t.Errorf("value = %q, want %q", val, "v")
			}
			return nil
		})
	})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
}

func TestUnlock_WrongPassphraseFails(t *testing.T) {
	dir := t.TempDir()
	s, err := Unlock(dir, "right")
	if err != nil {
		t.Fatalf("Unlock (first): %v", err)
	}
	if err := s.Lock(); err != nil {
		t.Fatalf("Lock: %v", err)
	}

	if _, err := Unlock(dir, "wrong"); err == nil {
		t.Fatalf("Unlock with wrong passphrase succeeded")
	}
}

func TestLock_ZerosKey(t *testing.T) {
	dir := t.TempDir()
	s, err := Unlock(dir, "pw")
	if err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	keyRef := s.key
	if len(keyRef) == 0 {
		t.Fatalf("key empty after Unlock")
	}
	if err := s.Lock(); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	for i, b := range keyRef {
		if b != 0 {
			t.Fatalf("key byte %d = %x, want 0 after Lock", i, b)
		}
	}
}

func TestLock_Idempotent(t *testing.T) {
	dir := t.TempDir()
	s, err := Unlock(dir, "pw")
	if err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if err := s.Lock(); err != nil {
		t.Fatalf("Lock (first): %v", err)
	}
	if err := s.Lock(); err != nil {
		t.Fatalf("Lock (second): %v", err)
	}
}

func TestUnlock_RefusesNewerSchema(t *testing.T) {
	dir := t.TempDir()
	s, err := Unlock(dir, "pw")
	if err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if err := s.db.Update(func(txn *badger.Txn) error {
		return writeSchemaVersion(txn, 99)
	}); err != nil {
		t.Fatalf("seed schema version: %v", err)
	}
	if err := s.Lock(); err != nil {
		t.Fatalf("Lock: %v", err)
	}

	_, err = Unlock(dir, "pw")
	if !errors.Is(err, ErrSchemaNewer) {
		t.Fatalf("err = %v, want ErrSchemaNewer", err)
	}
}
