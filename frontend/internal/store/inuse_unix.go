//go:build unix

package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

func checkNotInUse(badgerDir string) error {
	f, err := os.Open(badgerDir)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("store: no badger directory at %s", badgerDir)
	}
	if err != nil {
		return fmt.Errorf("store: open %s: %w", badgerDir, err)
	}
	defer func() { _ = f.Close() }()

	if err := unix.Flock(int(f.Fd()), unix.LOCK_SH|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return fmt.Errorf("%w: %s", ErrStoreInUse, badgerDir)
		}
		return fmt.Errorf("store: check lock on %s: %w", badgerDir, err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_UN); err != nil {
		return fmt.Errorf("store: release check lock on %s: %w", badgerDir, err)
	}
	return nil
}
