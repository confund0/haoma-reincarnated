package supervisor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

type RuntimeInfo struct {
	PID       int       `json:"pid"`
	APIAddr   string    `json:"api_addr"`
	StartedAt time.Time `json:"started_at"`

	ManageTor  string `json:"manage_tor,omitempty"`
	TorDataDir string `json:"tor_data_dir,omitempty"`
}

type Detached struct {
	Name      string
	PID       int
	APIAddr   string
	StartedAt time.Time

	ManageTor  string
	TorDataDir string

	runtimePath string

	cmd      *exec.Cmd
	waitCh   chan struct{}
	waitErr  error
	waitOnce sync.Once
}

func (d *Detached) Attached() bool { return d.cmd == nil }

type HealthCheckFunc func(ctx context.Context, addr string) error

func AttachOrSpawn(
	ctx context.Context,
	name, binPath string,
	args []string,
	secretsBlob []byte,
	runtimePath, stderrLogPath string,
	healthCheck HealthCheckFunc,
) (*Detached, error) {
	if name == "" {
		return nil, errors.New("supervisor: AttachOrSpawn requires a name")
	}

	info, err := ReadRuntimeFile(runtimePath)
	switch {
	case err == nil:
		if isProcessAlive(info.PID) {
			if healthCheck != nil {
				if hcErr := healthCheck(ctx, info.APIAddr); hcErr != nil {
					return nil, fmt.Errorf("supervisor: %s: PID %d alive at %s but health check failed (mismatched token? unrelated process?): %w",
						name, info.PID, info.APIAddr, hcErr)
				}
			}
			return &Detached{
				Name:        name,
				PID:         info.PID,
				APIAddr:     info.APIAddr,
				StartedAt:   info.StartedAt,
				ManageTor:   info.ManageTor,
				TorDataDir:  info.TorDataDir,
				runtimePath: runtimePath,
			}, nil
		}
		_ = os.Remove(runtimePath)
	case errors.Is(err, os.ErrNotExist):

	default:
		return nil, fmt.Errorf("supervisor: %s: runtime file %s: %w", name, runtimePath, err)
	}

	return spawnDetached(ctx, name, binPath, args, secretsBlob, stderrLogPath, runtimePath)
}

func FindRunning(name, runtimePath string) (*Detached, error) {
	info, err := ReadRuntimeFile(runtimePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("supervisor: %s: runtime file %s: %w", name, runtimePath, err)
	}
	if !isProcessAlive(info.PID) {
		return nil, nil
	}
	return &Detached{
		Name:        name,
		PID:         info.PID,
		APIAddr:     info.APIAddr,
		StartedAt:   info.StartedAt,
		ManageTor:   info.ManageTor,
		TorDataDir:  info.TorDataDir,
		runtimePath: runtimePath,
	}, nil
}

func spawnDetached(
	ctx context.Context,
	name, binPath string,
	args []string,
	secretsBlob []byte,
	stderrLogPath, runtimePath string,
) (*Detached, error) {
	if binPath == "" {
		return nil, errors.New("supervisor: spawnDetached requires binPath")
	}

	if err := rotateCaptureFile(stderrLogPath); err != nil {
		return nil, fmt.Errorf("supervisor: rotate stderr log %q: %w", stderrLogPath, err)
	}
	logFile, err := os.OpenFile(stderrLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("supervisor: open stderr log %q: %w", stderrLogPath, err)
	}

	cmd := exec.Command(binPath, args...)
	cmd.Stdin = bytes.NewReader(secretsBlob)
	cmd.Stderr = logFile
	applyDetach(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("supervisor: stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("supervisor: start %q: %w", binPath, err)
	}

	pid := cmd.Process.Pid
	startedAt := time.Now().UTC()

	addr, br, readyErr := readReadyLine(ctx, stdout)
	if readyErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = logFile.Close()
		return nil, fmt.Errorf("supervisor: %s: ready-line: %w", name, readyErr)
	}

	d := &Detached{
		Name:        name,
		PID:         pid,
		APIAddr:     addr,
		StartedAt:   startedAt,
		runtimePath: runtimePath,
		cmd:         cmd,
		waitCh:      make(chan struct{}),
	}

	go func() { _, _ = io.Copy(io.Discard, br) }()

	go func() {
		defer close(d.waitCh)
		d.waitOnce.Do(func() {
			d.waitErr = d.cmd.Wait()
			_ = logFile.Close()
		})
	}()

	return d, nil
}

func readReadyLine(ctx context.Context, stdout io.Reader) (string, *bufio.Reader, error) {
	br := bufio.NewReader(stdout)
	type result struct {
		addr string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, readErr := br.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := bytes.TrimRight(line, "\r\n")
			addr, parseErr := parseReadyLine(trimmed)
			if parseErr != nil {
				ch <- result{err: fmt.Errorf("expected ready-line, got %q: %w", string(trimmed), parseErr)}
				return
			}
			ch <- result{addr: addr}
			return
		}
		if readErr != nil {
			ch <- result{err: fmt.Errorf("stdout closed before ready: %w", readErr)}
			return
		}
	}()
	select {
	case r := <-ch:
		return r.addr, br, r.err
	case <-ctx.Done():
		return "", br, ctx.Err()
	}
}

func (d *Detached) Stop(ctx context.Context) error {
	if d.PID <= 0 {
		return errors.New("supervisor: Detached.Stop with invalid PID")
	}
	if !isProcessAlive(d.PID) {
		if d.waitCh != nil {
			select {
			case <-d.waitCh:
			default:
			}
		}
		_ = os.Remove(d.runtimePath)
		return nil
	}

	if d.cmd != nil {

		if err := terminate(d.cmd, gracePeriod, d.waitCh); err != nil {

			_ = err
		}
		select {
		case <-d.waitCh:
		case <-ctx.Done():
			return fmt.Errorf("supervisor: %s stop wait: %w", d.Name, ctx.Err())
		}
		_ = os.Remove(d.runtimePath)
		return nil
	}

	if err := terminateDetached(ctx, d.PID); err != nil {
		return fmt.Errorf("supervisor: %s: terminate PID %d: %w", d.Name, d.PID, err)
	}
	_ = os.Remove(d.runtimePath)
	return nil
}

func ReadRuntimeFile(path string) (RuntimeInfo, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return RuntimeInfo{}, err
	}
	if len(raw) == 0 {
		return RuntimeInfo{}, errors.New("runtime file is empty")
	}
	var info RuntimeInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return RuntimeInfo{}, fmt.Errorf("parse: %w", err)
	}
	if info.PID <= 0 {
		return RuntimeInfo{}, fmt.Errorf("invalid PID %d", info.PID)
	}
	if info.APIAddr == "" {
		return RuntimeInfo{}, errors.New("api_addr empty")
	}
	return info, nil
}
