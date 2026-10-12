//go:build !windows

package lockfile

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

var errInUseSyscall error = syscall.EWOULDBLOCK

func flockExclusiveNB(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func flockExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

func tryLockDir(path string) (*Lock, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("lockfile: open dir %q: %w", path, err)
	}
	if err := flockExclusiveNB(f); err != nil {
		_ = f.Close()
		if errors.Is(err, errInUseSyscall) {
			return nil, fmt.Errorf("%w: %s", ErrInUse, path)
		}
		return nil, fmt.Errorf("lockfile: lock dir %q: %w", path, err)
	}
	return &Lock{path: path, f: f}, nil
}
