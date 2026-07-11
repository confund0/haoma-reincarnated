package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/ipcclient"
)

const (
	readyTimeout   = 90 * time.Second
	connectTimeout = 20 * time.Second
	settleDelay    = 5 * time.Second
)

type rig struct {
	name      string
	root      string
	bePass    string
	fePass    string
	torPass   string
	logLevel  string
	haomadBin string
	haomaBin  string

	haomad     *exec.Cmd
	haoma      *exec.Cmd
	haomadAddr string
	haomaAddr  string

	client *ipcclient.Client

	mu      sync.Mutex
	subs    []chan ipc.Frame
	stopped bool
}

func (r *rig) frontendDir() string { return filepath.Join(r.root, "frontend") }

func (r *rig) provision(wipe bool) error {
	if wipe {
		if err := os.RemoveAll(r.root); err != nil {
			return err
		}
	}
	return os.MkdirAll(r.root, 0o700)
}

func (r *rig) launchHaomad() error {
	stderr, err := os.Create(filepath.Join(r.root, "haomad.stderr"))
	if err != nil {
		return err
	}
	cmd := exec.Command(r.haomadBin,
		"--cfg-dir", r.root,
		"--api-addr", "127.0.0.1:0",
		"--log-level", r.logLevel,
		"--log-file", filepath.Join(r.root, "haomad.log"),
	)
	cmd.Env = childEnv(map[string]string{
		"HAOMA_PASSPHRASE":   r.bePass,
		"HAOMA_TOR_PASSWORD": r.torPass,
	})
	cmd.Stderr = stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	r.haomad = cmd
	addr, err := readReadyLine(out, readyTimeout)
	if err != nil {
		return fmt.Errorf("haomad ready: %w", err)
	}
	r.haomadAddr = addr
	return nil
}

func (r *rig) launchHaoma() error {
	stderr, err := os.Create(filepath.Join(r.root, "haoma.stderr"))
	if err != nil {
		return err
	}
	cmd := exec.Command(r.haomaBin,
		"--cfg-dir", r.root,
		"--addr", "127.0.0.1:0",
		"--backend-addr", "https://"+r.haomadAddr,
		"--log-level", r.logLevel,
		"--log-file", filepath.Join(r.root, "haoma.log"),
	)
	cmd.Env = childEnv(map[string]string{
		"HAOMA_FRONTEND_PASSPHRASE": r.fePass,
	})
	cmd.Stderr = stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	r.haoma = cmd
	addr, err := readReadyLine(out, readyTimeout)
	if err != nil {
		return fmt.Errorf("haoma ready: %w", err)
	}
	r.haomaAddr = addr
	return nil
}

func (r *rig) connect() error {
	c, err := ipcclient.New(ipcclient.Config{
		FrontendDir: r.frontendDir(),
		Addr:        r.haomaAddr,
		ClientName:  "haoma-rig",
	})
	if err != nil {
		return err
	}
	r.client = c
	go c.Run()
	go r.pump()

	deadline := time.Now().Add(connectTimeout)
	for time.Now().Before(deadline) {
		if c.IsConnected() {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("IPC not connected within %s", connectTimeout)
}

func (r *rig) pump() {
	for f := range r.client.Incoming() {
		r.mu.Lock()
		for _, ch := range r.subs {
			select {
			case ch <- f:
			default:
			}
		}
		r.mu.Unlock()
	}
}

func (r *rig) subscribe() (<-chan ipc.Frame, func()) {
	ch := make(chan ipc.Frame, 256)
	r.mu.Lock()
	r.subs = append(r.subs, ch)
	r.mu.Unlock()
	cancel := func() {
		r.mu.Lock()
		for i, c := range r.subs {
			if c == ch {
				r.subs = append(r.subs[:i], r.subs[i+1:]...)
				break
			}
		}
		r.mu.Unlock()
	}
	return ch, cancel
}

func (r *rig) send(t ipc.FrameType, payload any) error {
	f, err := ipc.NewFrame(t, nextCorrID(), payload)
	if err != nil {
		return err
	}
	r.client.Send(f)
	return nil
}

func (r *rig) teardown() {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return
	}
	r.stopped = true
	r.mu.Unlock()

	if r.client != nil {
		r.client.Close()
	}
	stopProc(r.name+"/haoma", r.haoma)
	stopProc(r.name+"/haomad", r.haomad)
}

func stopProc(label string, cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		logf("[%s] did not exit on SIGTERM — killing", label)
		_ = cmd.Process.Kill()
		<-done
	}
}
