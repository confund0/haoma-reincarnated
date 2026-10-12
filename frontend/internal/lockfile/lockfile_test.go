package lockfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestAcquireBlocking_WaitsThenTakesIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.lock")

	held, err := AcquireBlocking(path)
	if err != nil {
		t.Fatalf("first AcquireBlocking: %v", err)
	}

	second := make(chan *Lock, 1)
	failed := make(chan error, 1)
	go func() {
		lk, err := AcquireBlocking(path)
		if err != nil {
			failed <- err
			return
		}
		second <- lk
	}()

	select {
	case <-second:
		t.Fatal("second AcquireBlocking returned while the first still held the lock")
	case err := <-failed:
		t.Fatalf("second AcquireBlocking errored instead of waiting: %v", err)
	case <-time.After(250 * time.Millisecond):
	}

	if err := held.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	select {
	case lk := <-second:
		_ = lk.Release()
	case err := <-failed:
		t.Fatalf("second AcquireBlocking errored after release: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("second AcquireBlocking still waiting after the first released")
	}
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
