//go:build windows

package supervisor

import (
	"context"
	"errors"
	"syscall"
	"time"
)

const (
	processQueryLimitedInformation = 0x1000
	stillActive                    = 259
)

func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)

	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}

func terminateDetached(ctx context.Context, pid int) error {
	if !isProcessAlive(pid) {
		return nil
	}

	h, err := syscall.OpenProcess(syscall.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(h)
	if err := syscall.TerminateProcess(h, 1); err != nil {
		return err
	}

	if waitForExit(ctx, pid, gracePeriod) {
		return nil
	}
	return errors.New("process did not exit after TerminateProcess")
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
