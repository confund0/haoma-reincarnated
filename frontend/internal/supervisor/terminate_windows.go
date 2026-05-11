//go:build windows

package supervisor

import (
	"errors"
	"os/exec"
	"time"
)

func terminate(cmd *exec.Cmd, _ time.Duration, _ <-chan struct{}) error {
	if cmd == nil || cmd.Process == nil {
		return errors.New("supervisor: terminate on un-started process")
	}
	if err := cmd.Process.Kill(); err != nil {
		return err
	}
	return nil
}
