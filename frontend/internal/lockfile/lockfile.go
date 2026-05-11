package lockfile

import (
	"errors"
	"fmt"
	"os"
)

var ErrInUse = errors.New("lockfile: in use by another process")

type Lock struct {
	path string
	f    *os.File
}

func Acquire(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("lockfile: open %q: %w", path, err)
	}
	if err := flockExclusiveNB(f); err != nil {
		_ = f.Close()
		if errors.Is(err, errInUseSyscall) {
			return nil, fmt.Errorf("%w: %s", ErrInUse, path)
		}
		return nil, fmt.Errorf("lockfile: lock %q: %w", path, err)
	}
	return &Lock{path: path, f: f}, nil
}

func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

func (l *Lock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}
