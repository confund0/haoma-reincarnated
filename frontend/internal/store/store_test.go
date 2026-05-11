package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	DefaultKDFParams = KDFParams{
		Time: 1, Memory: 8 * 1024, Threads: 2, KeyLen: 32, SaltLen: 16,
	}
	os.Exit(m.Run())
}

func TestUnlock_FreshStore_CreatesMeta(t *testing.T) {
	dir := t.TempDir()
	s, err := Unlock(dir, "hunter2")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Lock()

	info, err := os.Stat(filepath.Join(dir, metaFileName))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("meta.json mode = %o, want 0600", perm)
	}

	if _, err := os.Stat(filepath.Join(dir, badgerSubdir)); err != nil {
		t.Errorf("badger dir missing: %v", err)
	}
}

func TestUnlock_RoundTrip_SamePassphrase(t *testing.T) {
	dir := t.TempDir()
	s, err := Unlock(dir, "hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put([]byte("k"), []byte("v")); err != nil {
		t.Fatal(err)
	}
	if err := s.Lock(); err != nil {
		t.Fatal(err)
	}

	s2, err := Unlock(dir, "hunter2")
	if err != nil {
		t.Fatalf("reopen with same passphrase: %v", err)
	}
	defer s2.Lock()
	got, err := s2.Get([]byte("k"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte("v")) {
		t.Errorf("got %q, want v", got)
	}
}

func TestUnlock_WrongPassphrase_Fails(t *testing.T) {
	dir := t.TempDir()
	s, err := Unlock(dir, "correct")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put([]byte("k"), []byte("v")); err != nil {
		t.Fatal(err)
	}
	if err := s.Lock(); err != nil {
		t.Fatal(err)
	}

	_, err = Unlock(dir, "wrong")
	if err == nil {
		t.Fatal("expected error on wrong passphrase")
	}
}

func TestLock_Idempotent(t *testing.T) {
	dir := t.TempDir()
	s, err := Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Lock(); err != nil {
		t.Fatal(err)
	}

	if err := s.Lock(); err != nil {
		t.Errorf("second Lock errored: %v", err)
	}
}

func TestOperations_AfterLock_ReturnErrLocked(t *testing.T) {
	dir := t.TempDir()
	s, err := Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Lock(); err != nil {
		t.Fatal(err)
	}

	if err := s.Put([]byte("k"), []byte("v")); !errors.Is(err, ErrLocked) {
		t.Errorf("Put: err = %v, want ErrLocked", err)
	}
	if _, err := s.Get([]byte("k")); !errors.Is(err, ErrLocked) {
		t.Errorf("Get: err = %v, want ErrLocked", err)
	}
	if err := s.Delete([]byte("k")); !errors.Is(err, ErrLocked) {
		t.Errorf("Delete: err = %v, want ErrLocked", err)
	}
}

func TestGet_MissingKey_ReturnsErrNotFound(t *testing.T) {
	dir := t.TempDir()
	s, err := Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Lock()

	_, err = s.Get([]byte("nope"))
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestDelete_Idempotent(t *testing.T) {
	dir := t.TempDir()
	s, err := Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Lock()

	if err := s.Delete([]byte("never-existed")); err != nil {
		t.Errorf("Delete on absent key: %v", err)
	}
}

func TestSalt_PersistsAcrossUnlock(t *testing.T) {
	dir := t.TempDir()
	s1, err := Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	s1.Lock()

	meta1, _, err := loadMeta(dir)
	if err != nil {
		t.Fatal(err)
	}

	s2, err := Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	s2.Lock()

	meta2, _, err := loadMeta(dir)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(meta1.Salt, meta2.Salt) {
		t.Error("salt changed across Unlock cycles; would orphan the store")
	}
}

func TestMeta_CorruptJSON_Errors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, metaFileName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Unlock(dir, "pw")
	if err == nil {
		t.Fatal("expected error on corrupt meta")
	}
	if !errors.Is(err, ErrMetaCorrupt) {
		t.Errorf("err = %v, want ErrMetaCorrupt", err)
	}
}

func TestMeta_FutureVersion_Errors(t *testing.T) {
	dir := t.TempDir()
	future := []byte(`{"version": 99, "salt": "AAAA", "kdf": {"time":1,"memory_kib":8,"threads":1,"key_len":32,"salt_len":16}}`)
	if err := os.WriteFile(filepath.Join(dir, metaFileName), future, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Unlock(dir, "pw")
	if !errors.Is(err, ErrMetaVersionUnsupported) {
		t.Errorf("err = %v, want ErrMetaVersionUnsupported", err)
	}
}
