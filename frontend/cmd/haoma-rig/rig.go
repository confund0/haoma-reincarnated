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

	torBin string
	torDir string

	chutneyTorrc string

	streamerDir string

	base string

	torLogFrom    int64
	torLogFromSet bool

	keep bool

	torControl string
	torSocks   string

	giveUp string

	clockOffset string

	ignoreSiblingOffer bool

	skipEarlyReceipt bool

	ignoreEarlyReceipt bool

	ignoreFileRequest bool

	holdGapFillFetch bool

	gapOfferTTL string

	dropFirstDestroySalt bool

	dropContactDeletePush bool

	stallPeerCircuit string

	haomad     *exec.Cmd
	haoma      *exec.Cmd
	haomadAddr string
	haomaAddr  string

	client *ipcclient.Client

	mu   sync.Mutex
	subs []chan ipc.Frame

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

func stderrSink(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}

func (r *rig) launchHaomad() error { return r.startHaomad("127.0.0.1:0") }

func (r *rig) startHaomad(apiAddr string) error {
	if r.ownTor() {
		if err := prepareTorDir(r.name, r.torDir, r.rigBase()); err != nil {
			return err
		}
		if err := syncChutneyTorrc(r.torDir, r.chutneyTorrc); err != nil {
			return err
		}
	}
	stderr, err := stderrSink(filepath.Join(r.root, "haomad.stderr"))
	if err != nil {
		return err
	}

	defer stderr.Close()
	cmd := exec.Command(r.haomadBin, r.haomadArgs(apiAddr)...)
	cmd.Env = childEnv(r.haomadEnv())
	cmd.Stderr = stderr
	ownGroup(cmd, !r.keep)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	logFrom := fileSize(filepath.Join(r.root, "haomad.log"))
	torLogFrom := fileSize(r.torLogPath())
	if !r.torLogFromSet {
		r.torLogFrom, r.torLogFromSet = torLogFrom, true
	}
	launched := time.Now()
	if err := cmd.Start(); err != nil {
		return err
	}
	r.haomad = cmd
	addr, exited, err := readReadyLineWatch(out, readyTimeout)
	if err != nil {
		if r.ownTor() {
			return fmt.Errorf("haomad ready: %w; %s", err, r.torStartFailure(logFrom, torLogFrom))
		}
		return fmt.Errorf("haomad ready: %w", err)
	}
	r.haomadAddr = addr
	if err := r.awaitTor(launched, logFrom, exited); err != nil {
		return fmt.Errorf("haomad tor: %w", err)
	}
	if r.ownTor() {
		if err := checkTorNetwork(r.torLogPath(), torLogFrom, r.chutneyTorrc != ""); err != nil {
			return fmt.Errorf("haomad tor: %w", err)
		}
	}
	return nil
}

func (r *rig) stopHaomad() {
	stopProc(r.name+"/haomad", r.haomad)
	r.haomad = nil
}

func (r *rig) restartHaomad() error { return r.startHaomad(r.haomadAddr) }

func (r *rig) stopHaoma() error {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return fmt.Errorf("%s: rig is torn down — nothing to stop", r.name)
	}
	c, haoma := r.client, r.haoma
	r.client, r.haoma = nil, nil
	r.haomaAddr = ""
	r.mu.Unlock()
	if c != nil {

		c.Close()
	}
	stopProc(r.name+"/haoma", haoma)
	return nil
}

func (r *rig) startHaoma() error {
	r.mu.Lock()
	torn, live, backend := r.stopped, r.haoma != nil, r.haomad != nil
	r.mu.Unlock()
	switch {
	case torn:
		return fmt.Errorf("%s: rig is torn down — startHaoma cannot resurrect it", r.name)
	case live:
		return fmt.Errorf("%s: haoma is still running — stop it first", r.name)
	case !backend:
		return fmt.Errorf("%s: haomad is not running — use start for a full relaunch", r.name)
	}
	logf("[%s] relaunching haoma against the haomad that stayed up …", r.name)
	if err := r.launchHaoma(); err != nil {
		return fmt.Errorf("relaunch haoma: %w", err)
	}
	if err := r.connect(); err != nil {
		return fmt.Errorf("reconnect: %w", err)
	}
	logf("[%s] haoma back on %s, IPC reconnected", r.name, r.haomaAddr)
	return nil
}

func (r *rig) relaunchHaomaWith(set func(*rig)) error {
	r.mu.Lock()
	live := r.haoma != nil
	r.mu.Unlock()
	if live {
		if err := r.stopHaoma(); err != nil {
			return err
		}
	}
	set(r)
	return r.startHaoma()
}

func (r *rig) launchHaoma() error {
	stderr, err := stderrSink(filepath.Join(r.root, "haoma.stderr"))
	if err != nil {
		return err
	}
	defer stderr.Close()
	cmd := exec.Command(r.haomaBin,
		"--cfg-dir", r.root,
		"--addr", "127.0.0.1:0",
		"--backend-addr", "https://"+r.haomadAddr,
		"--log-level", r.logLevel,
		"--log-file", filepath.Join(r.root, "haoma.log"),
	)
	cmd.Env = childEnv(r.haomaEnv())
	cmd.Stderr = stderr
	if !r.keep {

		signalOnRigDeath(cmd)
	}
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

func (r *rig) haomaEnv() map[string]string {
	env := map[string]string{
		"HAOMA_FRONTEND_PASSPHRASE": r.fePass,
	}
	if r.ignoreSiblingOffer {
		env[seedIgnoreSiblingOfferEnv] = "1"
	}
	if r.skipEarlyReceipt {
		env[seedSkipEarlyReceiptEnv] = "1"
	}
	if r.ignoreEarlyReceipt {
		env[seedIgnoreEarlyReceiptEnv] = "1"
	}
	if r.ignoreFileRequest {
		env[seedIgnoreFileRequestEnv] = "1"
	}

	env[streamerDirEnv] = r.streamerDir

	shellCaseSettingsOnce.Do(func() {
		for _, line := range shellCaseSettingsNotice(os.LookupEnv) {
			logf("%s", line)
		}
	})
	env[holdGapFillFetchEnv] = ""
	if r.holdGapFillFetch {
		env[holdGapFillFetchEnv] = "1"
	}
	env[gapOfferTTLEnv] = r.gapOfferTTL
	env[dropFirstDestroySaltEnv] = ""
	if r.dropFirstDestroySalt {
		env[dropFirstDestroySaltEnv] = "1"
	}
	env[dropContactDeletePushEnv] = ""
	if r.dropContactDeletePush {
		env[dropContactDeletePushEnv] = "1"
	}
	return env
}

var shellCaseSettingsOnce sync.Once

func shellCaseSettingsNotice(lookup func(string) (string, bool)) []string {
	var out []string
	for _, c := range []struct{ env, group, file string }{
		{holdGapFillFetchEnv, "file-gapfill-expiry", "siblingfileexpiry.go"},
		{gapOfferTTLEnv, "file-gapfill-expiry", "siblingfileexpiry.go"},
		{dropFirstDestroySaltEnv, "destroy-pin", "destroypin.go"},
		{dropContactDeletePushEnv, "contact-delete", "contactdelete.go"},
	} {
		if v, ok := lookup(c.env); ok && v != "" {
			out = append(out, fmt.Sprintf("IGNORING %s=%q from the harness's environment: only the case %s sets it, on one rig's haoma for part of the case, and every other haoma starts without it (%s)", c.env, v, c.group, c.file))
		}
	}
	return out
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
	r.mu.Lock()
	r.client = c
	r.mu.Unlock()
	go c.Run()
	go r.pump(c)

	deadline := time.Now().Add(connectTimeout)
	for time.Now().Before(deadline) {
		if c.IsConnected() {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("IPC not connected within %s", connectTimeout)
}

func (r *rig) pump(c *ipcclient.Client) {
	for f := range c.Incoming() {
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
	_, err := r.sendReq(t, payload)
	return err
}

func (r *rig) sendReq(t ipc.FrameType, payload any) (string, error) {
	r.mu.Lock()
	c, torn := r.client, r.stopped
	r.mu.Unlock()
	if torn || c == nil {
		state := "stopped (offline gap or mid-restart)"
		if torn {
			state = "torn down"
		}
		logf("[%s] REFUSING %s — rig is %s, nothing was sent", r.name, t, state)
		return "", fmt.Errorf("%s: rig is %s — %s not sent", r.name, state, t)
	}
	id := nextCorrID()
	f, err := ipc.NewFrame(t, id, payload)
	if err != nil {
		return "", err
	}
	c.Send(f)
	return id, nil
}

func (r *rig) stop() error {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return fmt.Errorf("%s: rig is torn down — nothing to stop", r.name)
	}
	c, haoma, haomad := r.client, r.haoma, r.haomad

	r.client, r.haoma, r.haomad = nil, nil, nil
	r.haomaAddr, r.haomadAddr = "", ""
	r.mu.Unlock()

	if c != nil {

		c.Close()
	}
	stopProc(r.name+"/haoma", haoma)
	stopProc(r.name+"/haomad", haomad)
	return nil
}

func (r *rig) start() error {
	r.mu.Lock()
	torn := r.stopped
	live := r.haomad != nil || r.haoma != nil
	r.mu.Unlock()
	if torn {
		return fmt.Errorf("%s: rig is torn down — start cannot resurrect it", r.name)
	}
	if live {
		return fmt.Errorf("%s: start on a rig that is still running — stop it first", r.name)
	}

	if err := r.provision(false); err != nil {
		return fmt.Errorf("re-provision: %w", err)
	}
	logf("[%s] relaunching haomad …", r.name)
	if err := r.launchHaomad(); err != nil {
		return fmt.Errorf("relaunch haomad: %w", err)
	}
	logf("[%s] haomad back on %s", r.name, r.haomadAddr)
	logf("[%s] relaunching haoma …", r.name)
	if err := r.launchHaoma(); err != nil {
		return fmt.Errorf("relaunch haoma: %w", err)
	}
	logf("[%s] haoma back on %s", r.name, r.haomaAddr)
	if err := r.connect(); err != nil {
		return fmt.Errorf("reconnect: %w", err)
	}
	logf("[%s] IPC reconnected", r.name)
	return nil
}

func (r *rig) restart() error {
	if err := r.stop(); err != nil {
		return fmt.Errorf("stop: %w", err)
	}
	if err := r.start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	return nil
}

func (r *rig) teardown() {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return
	}
	r.stopped = true
	c, haoma, haomad := r.client, r.haoma, r.haomad
	r.client, r.haoma, r.haomad = nil, nil, nil
	r.mu.Unlock()

	if c != nil {
		c.Close()
	}
	stopProc(r.name+"/haoma", haoma)
	stopProc(r.name+"/haomad", haomad)
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

	sweepGroup(label, cmd)
}
