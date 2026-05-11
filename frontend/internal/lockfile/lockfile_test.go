package lockfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAcquire_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.lock")

	lk, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer lk.Release()

	if _, err := os.Stat(path); err != nil {
		t.Errorf("sentinel file not created: %v", err)
	}
	if lk.Path() != path {
		t.Errorf("Path() = %q, want %q", lk.Path(), path)
	}
}

func TestAcquire_DoubleAcquireSameProcessFailsWithErrInUse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.lock")

	lk1, err := Acquire(path)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	defer lk1.Release()

	lk2, err := Acquire(path)
	if err == nil {
		_ = lk2.Release()
		t.Fatal("second Acquire succeeded; want ErrInUse")
	}
	if !errors.Is(err, ErrInUse) {
		t.Errorf("err = %v, want ErrInUse chain", err)
	}
}

func TestAcquire_AfterReleaseSucceeds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.lock")

	lk1, err := Acquire(path)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	if err := lk1.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	lk2, err := Acquire(path)
	if err != nil {
		t.Fatalf("re-Acquire after Release: %v", err)
	}
	defer lk2.Release()
}

func TestRelease_Idempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.lock")

	lk, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := lk.Release(); err != nil {
		t.Fatalf("first Release: %v", err)
	}
	if err := lk.Release(); err != nil {
		t.Errorf("second Release errored: %v", err)
	}
}

func TestRelease_NilLockNoOp(t *testing.T) {
	var lk *Lock
	if err := lk.Release(); err != nil {
		t.Errorf("nil Release: %v", err)
	}
}

func TestAcquire_BadPath(t *testing.T) {

	path := filepath.Join(t.TempDir(), "nope", "vault.lock")
	if _, err := Acquire(path); err == nil {
		t.Fatal("want error on missing parent dir")
	}
}
