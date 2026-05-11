//go:build windows

package lockfile

import (
	"os"

	"golang.org/x/sys/windows"
)

var errInUseSyscall error = windows.ERROR_LOCK_VIOLATION

func flockExclusiveNB(f *os.File) error {
	var ol windows.Overlapped
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		0xFFFFFFFF,
		0xFFFFFFFF,
		&ol,
	)
}
