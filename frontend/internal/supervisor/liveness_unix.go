//go:build !windows

package supervisor

import (
	"context"
	"errors"
	"syscall"
	"time"
)

func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	return errors.Is(err, syscall.EPERM)
}

func terminateDetached(ctx context.Context, pid int) error {
	if !isProcessAlive(pid) {
		return nil
	}

	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}

	if waitForExit(ctx, pid, gracePeriod) {
		return nil
	}

	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	if waitForExit(ctx, pid, 2*time.Second) {
		return nil
	}
	return errors.New("process did not exit after SIGKILL")
}

func waitForExit(ctx context.Context, pid int, within time.Duration) bool {
	deadline := time.NewTimer(within)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			if !isProcessAlive(pid) {
				return true
			}
		case <-deadline.C:
			return false
		case <-ctx.Done():
			return false
		}
	}
}
