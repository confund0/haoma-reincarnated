//go:build !windows

package supervisor

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

func terminate(cmd *exec.Cmd, grace time.Duration, exited <-chan struct{}) error {
	if cmd == nil || cmd.Process == nil {
		return errors.New("supervisor: terminate on un-started process")
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {

		if !errors.Is(err, syscall.ESRCH) {
			return err
		}
		return nil
	}

	select {
	case <-exited:
		return nil
	case <-time.After(grace):
	}

	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
