//go:build windows

package lockfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

var errInUseSyscall error = windows.ERROR_LOCK_VIOLATION

func flockExclusiveNB(f *os.File) error {
	return lockFileEx(f, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY)
}

func flockExclusive(f *os.File) error {
	return lockFileEx(f, windows.LOCKFILE_EXCLUSIVE_LOCK)
}

func lockFileEx(f *os.File, flags uint32) error {
	var ol windows.Overlapped
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		flags,
		0,
		0xFFFFFFFF,
		0xFFFFFFFF,
		&ol,
	)
}

func tryLockDir(path string) (*Lock, error) {
	name, err := windows.UTF16PtrFromString(filepath.Join(path, "LOCK"))
	if err != nil {
		return nil, fmt.Errorf("lockfile: dir %q: %w", path, err)
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	switch {
	case err == nil:
		_ = windows.CloseHandle(h)
		return &Lock{path: path}, nil
	case errors.Is(err, windows.ERROR_SHARING_VIOLATION):
		return nil, fmt.Errorf("%w: %s", ErrInUse, path)
	case errors.Is(err, windows.ERROR_FILE_NOT_FOUND), errors.Is(err, windows.ERROR_PATH_NOT_FOUND):
		return &Lock{path: path}, nil
	default:
		return nil, fmt.Errorf("lockfile: open %s\\LOCK: %w", path, err)
	}
}
