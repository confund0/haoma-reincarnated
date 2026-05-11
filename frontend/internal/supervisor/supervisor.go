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
	"strings"
	"sync"
	"time"
)

type readyLine struct {
	Status  string `json:"status"`
	APIAddr string `json:"api_addr"`
}

type Child struct {
	Name string

	cmd       *exec.Cmd
	stderrLog *os.File

	readyCh chan readyResult

	waitCh   chan struct{}
	waitErr  error
	waitOnce sync.Once
}

type readyResult struct {
	addr string
	err  error
}

func Spawn(name, binPath string, args []string, secretsBlob []byte, stderrLogPath string) (*Child, error) {
	if name == "" {
		return nil, errors.New("supervisor: Spawn requires a name")
	}
	if binPath == "" {
		return nil, errors.New("supervisor: Spawn requires binPath")
	}

	logFile, err := os.OpenFile(stderrLogPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("supervisor: open stderr log %q: %w", stderrLogPath, err)
	}

	cmd := exec.Command(binPath, args...)
	cmd.Stdin = bytes.NewReader(secretsBlob)
	cmd.Stderr = logFile

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("supervisor: stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("supervisor: start %q: %w", binPath, err)
	}

	c := &Child{
		Name:      name,
		cmd:       cmd,
		stderrLog: logFile,
		readyCh:   make(chan readyResult, 1),
		waitCh:    make(chan struct{}),
	}

	go c.readStdout(stdout)

	go c.reap()

	return c, nil
}

func (c *Child) readStdout(stdout io.ReadCloser) {
	defer stdout.Close()
	br := bufio.NewReader(stdout)
	delivered := false

	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := bytes.TrimRight(line, "\r\n")
			if !delivered {
				addr, parseErr := parseReadyLine(trimmed)
				if parseErr == nil {
					c.readyCh <- readyResult{addr: addr}
					delivered = true
					continue
				}

				c.readyCh <- readyResult{
					err: fmt.Errorf("supervisor: %s stdout: expected ready-line, got %q: %w", c.Name, string(trimmed), parseErr),
				}
				delivered = true
				continue
			}

		}
		if err != nil {

			return
		}
	}
}

func parseReadyLine(line []byte) (string, error) {

	trimmed := bytes.TrimLeft(line, " \t")
	if len(trimmed) == 0 {
		return "", errors.New("empty line")
	}
	if trimmed[0] != '{' {
		return "", errors.New("not a JSON object")
	}
	var rl readyLine
	if err := json.Unmarshal(trimmed, &rl); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	if rl.Status != "ready" {
		return "", fmt.Errorf("status=%q (want %q)", rl.Status, "ready")
	}
	if strings.TrimSpace(rl.APIAddr) == "" {
		return "", errors.New("api_addr empty")
	}
	return rl.APIAddr, nil
}

func (c *Child) reap() {
	defer close(c.waitCh)
	c.waitOnce.Do(func() {
		c.waitErr = c.cmd.Wait()

		_ = c.stderrLog.Close()
	})
}

func (c *Child) WaitReady(ctx context.Context) (string, error) {
	select {
	case res := <-c.readyCh:
		return res.addr, res.err
	case <-c.waitCh:

		select {
		case res := <-c.readyCh:
			return res.addr, res.err
		default:
		}
		if c.waitErr != nil {
			return "", fmt.Errorf("supervisor: %s exited before ready: %w", c.Name, c.waitErr)
		}
		return "", fmt.Errorf("supervisor: %s exited before ready", c.Name)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (c *Child) Stop(ctx context.Context) error {
	if c.cmd.Process == nil {
		return errors.New("supervisor: Stop on un-started child")
	}

	select {
	case <-c.waitCh:

		return c.waitErr
	default:
	}

	if err := terminate(c.cmd, gracePeriod, c.waitCh); err != nil {

		_ = err
	}

	select {
	case <-c.waitCh:
		return c.waitErr
	case <-ctx.Done():
		return fmt.Errorf("supervisor: %s stop wait: %w", c.Name, ctx.Err())
	}
}

var gracePeriod = 5 * time.Second

func Shutdown(ctx context.Context, children ...*Child) error {
	if len(children) == 0 {
		return nil
	}

	var wg sync.WaitGroup
	errs := make([]error, len(children))
	for i, ch := range children {
		if ch == nil {
			continue
		}
		wg.Add(1)
		go func(i int, ch *Child) {
			defer wg.Done()
			if err := ch.Stop(ctx); err != nil {
				errs[i] = fmt.Errorf("%s: %w", ch.Name, err)
			}
		}(i, ch)
	}
	wg.Wait()

	return errors.Join(errs...)
}
