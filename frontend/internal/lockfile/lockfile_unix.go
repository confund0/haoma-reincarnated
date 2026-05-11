//go:build !windows

package lockfile

import (
	"os"
	"syscall"
)

var errInUseSyscall error = syscall.EWOULDBLOCK

func flockExclusiveNB(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
