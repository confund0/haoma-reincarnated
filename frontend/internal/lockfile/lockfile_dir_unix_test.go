//go:build !windows

package lockfile

import (
	"errors"
	"testing"
)

func TestTryLockDir_SecondHandleIsInUse(t *testing.T) {
	dir := t.TempDir()
	lk1, err := TryLockDir(dir)
	if err != nil {
		t.Fatalf("first TryLockDir: %v", err)
	}
	lk2, err := TryLockDir(dir)
	if err == nil {
		_ = lk2.Release()
		t.Fatal("second TryLockDir succeeded; want ErrInUse")
	}
	if !errors.Is(err, ErrInUse) {
		t.Errorf("err = %v, want ErrInUse chain", err)
	}
	if err := lk1.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	lk3, err := TryLockDir(dir)
	if err != nil {
		t.Fatalf("TryLockDir after Release: %v", err)
	}
	_ = lk3.Release()
}
