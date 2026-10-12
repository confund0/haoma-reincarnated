package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	netProbeTorName = "netprobe"

	netProbeControlTimeout = 30 * time.Second

	netProbeOnionTimeout = 5 * time.Minute

	netProbeDialTimeout = 60 * time.Second

	netProbeDialRetry = 5 * time.Second

	netProbeSettle = 10 * time.Second

	netProbeDownWait = 6 * time.Minute

	netProbeDialEvery = 30 * time.Second

	netProbeHoldCut = 20 * time.Second

	netProbeUpWait = 3 * time.Minute

	netProbeTorStopGrace = 5 * time.Second
)

type netProbeOutcome int

const (
	netProbeNotMeasured netProbeOutcome = iota
	netProbePass
	netProbeNoDown
	netProbeNoUp
)

type netProbeResult struct {
	outcome netProbeOutcome
	reason  string

	downWait, upWait time.Duration
	downSeen         bool
	downAfter        time.Duration
	upAfter          time.Duration

	beforeCut   cutProxyStats
	closedAtCut int
	end         cutProxyStats

	early []string
	other []string

	dials, dialsReached int
	afterPass           string

	torLog     string
	torLogFrom int64
}

func runNetProbe(cfg config) error {
	res := netProbeResult{downWait: netProbeDownWait, upWait: netProbeUpWait, afterPass: "not tried"}
	if err := probeNetwork(cfg, &res); err != nil {
		res.outcome = netProbeNotMeasured
		res.reason = err.Error()
	}
	for _, l := range netProbeResultLines(res) {
		logf("%s", l)
	}
	if res.outcome == netProbeNotMeasured {
		return errors.New("netprobe: not measured, see RESULTS")
	}
	return nil
}

func probeNetwork(cfg config, res *netProbeResult) error {
	dir := torDirFor(cfg.torDirs, netProbeTorName)
	if err := prepareTorDir(netProbeTorName, dir, cfg.base); err != nil {
		return fmt.Errorf("tor data dir: %w", err)
	}

	_ = os.Remove(filepath.Join(dir, "control.port"))
	_ = os.Remove(filepath.Join(dir, "control.cookie"))

	proxy, err := startCutProxy()
	if err != nil {
		return err
	}
	defer proxy.close()
	defer func() { res.end = proxy.statsNow() }()

	torrc := filepath.Join(dir, "torrc")
	if err := os.WriteFile(torrc, []byte(netProbeTorrc(dir, cfg.chutneyTorrc, proxy.addr())), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", torrc, err)
	}
	res.torLog = filepath.Join(dir, "tor.log")
	res.torLogFrom = fileSize(res.torLog)
	logf("[%s] cut proxy on %s; starting %s on %s", netProbeTorName, proxy.addr(), cfg.torBin, dir)
	tor, err := startNetProbeTor(cfg.torBin, torrc, res.torLog, res.torLogFrom)
	if err != nil {
		return err
	}
	defer tor.stop()

	ctl, err := tor.awaitControl(dir)
	if err != nil {
		return err
	}
	defer ctl.close()
	if err := tor.awaitBootstrap(ctl); err != nil {
		return err
	}
	if err := checkTorNetwork(res.torLog, res.torLogFrom, cfg.chutney != ""); err != nil {
		return err
	}
	if s := proxy.statsNow(); s.Links == 0 {
		return errors.New("tor reached 100% with no link through the cut proxy: it does not use the proxy, so a cut would prove nothing")
	}
	logf("[%s] tor at 100%% through the proxy (%+v)", netProbeTorName, proxy.statsNow())
	socks, err := ctl.socksListener()
	if err != nil {
		return fmt.Errorf("tor's SOCKS listener: %w", err)
	}

	ev, err := dialTorControl(dir)
	if err != nil {
		return fmt.Errorf("control connection for events: %w", err)
	}
	if _, err := ev.cmd("SETEVENTS NETWORK_LIVENESS"); err != nil {
		ev.close()
		return err
	}

	_ = ev.c.SetDeadline(time.Time{})
	events, stopEvents := readLiveness(ev)
	defer stopEvents()
	probe := &netProbeRun{events: events, exited: tor.exited, tor: tor}

	hs, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("onion target: %w", err)
	}
	var hsWG sync.WaitGroup
	hsWG.Add(1)
	go func() {
		defer hsWG.Done()
		for {
			c, err := hs.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	defer func() { _ = hs.Close(); hsWG.Wait() }()

	lines, err := ctl.cmd("ADD_ONION NEW:ED25519-V3 Flags=DiscardPK Port=80," + hs.Addr().String())
	if err != nil {
		return err
	}
	id := serviceIDFrom(lines)
	if id == "" {
		return fmt.Errorf("ADD_ONION gave no ServiceID: %q", lines)
	}
	onion := id + ".onion"
	logf("[%s] onion published; dialling it through tor until it answers", netProbeTorName)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := probe.awaitOnion(ctx, socks, onion); err != nil {
		return err
	}
	if err := probe.watch(netProbeSettle, never); err != nil && !errors.Is(err, errWatchTimeout) {
		return err
	}

	res.beforeCut = proxy.statsNow()
	probe.latch.cutAt = time.Now()
	res.closedAtCut = proxy.setCut(true)
	logf("[%s] CUT: the proxy closed %d connections (%+v)", netProbeTorName, res.closedAtCut, res.beforeCut)
	if res.closedAtCut == 0 {
		return errors.New("the cut closed no connection: tor had no link through the proxy, so a cut would prove nothing")
	}
	var tally dialTally
	stopDials := startDialLoop(ctx, socks, onion, &tally)
	defer func() {
		stopDials()
		res.dials, res.dialsReached = tally.counts()
		res.early, res.other = probe.latch.early, probe.latch.other
	}()

	err = probe.watch(netProbeDownWait, func() bool { return !probe.latch.downAt.IsZero() })
	switch {
	case errors.Is(err, errWatchTimeout):
		res.outcome = netProbeNoDown
		return nil
	case err != nil:
		return err
	}
	res.downSeen = true
	res.downAfter = probe.latch.downAt.Sub(probe.latch.cutAt)
	logf("[%s] DOWN %s after the cut; the cut stays %s more", netProbeTorName, res.downAfter.Round(100*time.Millisecond), netProbeHoldCut)
	if err := probe.watch(netProbeHoldCut, never); err != nil && !errors.Is(err, errWatchTimeout) {
		return err
	}

	probe.latch.passAt = time.Now()
	n := proxy.setCut(false)
	logf("[%s] PASS: the proxy passes again; closed %d held connections", netProbeTorName, n)
	err = probe.watch(netProbeUpWait, func() bool { return !probe.latch.upAt.IsZero() })
	switch {
	case errors.Is(err, errWatchTimeout):
		res.outcome = netProbeNoUp
	case err != nil:
		return err
	default:
		res.outcome = netProbePass
		res.upAfter = probe.latch.upAt.Sub(probe.latch.passAt)
	}
	stopDials()
	st := time.Now()
	if err := dialOnionSocks(ctx, socks, onion, 80, netProbeDialTimeout); err != nil {
		res.afterPass = fmt.Sprintf("not reached in %s: %v", time.Since(st).Round(100*time.Millisecond), err)
	} else {
		res.afterPass = fmt.Sprintf("reached in %s", time.Since(st).Round(100*time.Millisecond))
	}
	return nil
}

func netProbeTorrc(dir, network, proxy string) string {
	var b strings.Builder
	b.WriteString("# written by haoma-rig -scenario netprobe; written again each run\n")
	fmt.Fprintf(&b, "DataDirectory %s\n", dir)
	b.WriteString("ControlPort auto\n")
	fmt.Fprintf(&b, "ControlPortWriteToFile %s\n", filepath.Join(dir, "control.port"))
	b.WriteString("CookieAuthentication 1\n")
	fmt.Fprintf(&b, "CookieAuthFile %s\n", filepath.Join(dir, "control.cookie"))
	b.WriteString("SocksPort auto\n")
	b.WriteString("AvoidDiskWrites 1\n")
	fmt.Fprintf(&b, "Log notice file %s\n", filepath.Join(dir, "tor.log"))
	b.WriteString("SafeLogging 1\n")
	if network != "" {
		b.WriteString(network)
		if !strings.HasSuffix(network, "\n") {
			b.WriteString("\n")
		}
	}
	fmt.Fprintf(&b, "Socks5Proxy %s\n", proxy)
	return b.String()
}

type netProbeTor struct {
	cmd     *exec.Cmd
	exited  chan struct{}
	torLog  string
	logFrom int64
	once    sync.Once
}

func startNetProbeTor(bin, torrc, torLog string, logFrom int64) (*netProbeTor, error) {
	cmd := exec.Command(bin, "-f", torrc, "--quiet")
	ownGroup(cmd, true)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start tor: %w", err)
	}
	t := &netProbeTor{cmd: cmd, exited: make(chan struct{}), torLog: torLog, logFrom: logFrom}
	go func() {
		_ = cmd.Wait()
		close(t.exited)
	}()
	return t, nil
}

func (t *netProbeTor) stop() {
	t.once.Do(func() {
		select {
		case <-t.exited:
		default:
			_ = t.cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-t.exited:
			case <-time.After(netProbeTorStopGrace):
				logf("[%s] tor did not exit within %s of SIGTERM — killing it", netProbeTorName, netProbeTorStopGrace)
				_ = t.cmd.Process.Kill()
				<-t.exited
			}
		}
		sweepGroup(netProbeTorName, t.cmd)
	})
}

func (t *netProbeTor) exitText() string {
	tail := tailLines(t.torLog, t.logFrom, torLogTailLines)
	if len(tail) == 0 {
		tail = []string{"(nothing in tor.log since this launch)"}
	}
	return fmt.Sprintf("tor exited; %s, last lines: %s", t.torLog, strings.Join(tail, " | "))
}

func (t *netProbeTor) awaitControl(dir string) (*rigTorControl, error) {
	deadline := time.Now().Add(netProbeControlTimeout)
	var lastErr error
	for {
		if raw, err := os.ReadFile(filepath.Join(dir, "control.port")); err == nil && strings.Contains(string(raw), "PORT=") {
			ctl, err := dialTorControl(dir)
			if err == nil {
				return ctl, nil
			}
			lastErr = err
		}
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("no control connection to tor within %s (last error: %v)", netProbeControlTimeout, lastErr)
		}
		select {
		case <-t.exited:
			return nil, errors.New(t.exitText())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (t *netProbeTor) awaitBootstrap(ctl *rigTorControl) error {
	deadline := time.Now().Add(torBootstrapTimeout)
	last := ""
	for {
		lines, err := ctl.cmd("GETINFO status/bootstrap-phase")
		if err != nil {
			return fmt.Errorf("read tor's bootstrap: %w", err)
		}
		progress, line := bootstrapProgress(lines)
		if progress == 100 {
			return nil
		}
		if line != last {
			logf("[%s] tor bootstrap: %s", netProbeTorName, line)
			last = line
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("tor did not reach 100%% within %s; last step: %s; tor's own log: %s", torBootstrapTimeout, last, t.torLog)
		}
		select {
		case <-t.exited:
			return errors.New(t.exitText())
		case <-time.After(torPollInterval):
		}
	}
}

func bootstrapProgress(lines []string) (int, string) {
	for _, l := range lines {
		v, ok := strings.CutPrefix(l, "status/bootstrap-phase=")
		if !ok {
			continue
		}
		for _, f := range strings.Fields(v) {
			if p, ok := strings.CutPrefix(f, "PROGRESS="); ok {
				n, err := strconv.Atoi(p)
				if err != nil {
					return -1, v
				}
				return n, v
			}
		}
		return -1, v
	}
	return -1, strings.Join(lines, " ")
}

func serviceIDFrom(lines []string) string {
	for _, l := range lines {
		if v, ok := strings.CutPrefix(l, "ServiceID="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

type livenessEvent struct {
	state string
	at    time.Time
}

func parseLiveness(line string) (string, bool) {
	v, ok := strings.CutPrefix(strings.TrimRight(line, "\r\n"), "650 NETWORK_LIVENESS ")
	if !ok {
		return "", false
	}
	switch v = strings.TrimSpace(v); v {
	case "UP", "DOWN":
		return v, true
	}
	return "", false
}

func readLiveness(ev *rigTorControl) (<-chan livenessEvent, func()) {
	out := make(chan livenessEvent, 64)
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(out)
		for {
			s, err := ev.br.ReadString('\n')
			if err != nil {
				return
			}
			at := time.Now()
			state, ok := parseLiveness(s)
			if !ok {
				continue
			}
			logf("[%s] tor event: NETWORK_LIVENESS %s", netProbeTorName, state)
			select {
			case out <- livenessEvent{state: state, at: at}:
			case <-done:
				return
			}
		}
	}()
	var once sync.Once
	return out, func() {
		once.Do(func() {
			close(done)
			ev.close()
			wg.Wait()
		})
	}
}

type livenessLatch struct {
	cutAt, passAt time.Time
	downAt, upAt  time.Time
	early         []string
	other         []string
}

func (l *livenessLatch) see(e livenessEvent) {
	switch {
	case l.cutAt.IsZero() || e.at.Before(l.cutAt):
		l.early = append(l.early, e.state)
	case e.state == "DOWN" && l.downAt.IsZero():
		l.downAt = e.at
	case e.state == "UP" && l.upAt.IsZero() && !l.downAt.IsZero() &&
		!l.passAt.IsZero() && !e.at.Before(l.passAt):
		l.upAt = e.at
	default:
		l.other = append(l.other, e.state)
	}
}

type netProbeRun struct {
	events <-chan livenessEvent
	exited <-chan struct{}
	tor    *netProbeTor
	latch  livenessLatch
}

var errWatchTimeout = errors.New("time ran out")

func never() bool { return false }

func (p *netProbeRun) watch(d time.Duration, done func() bool) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	for !done() {
		select {
		case e, ok := <-p.events:
			if !ok {
				return errors.New("the control connection that carries NETWORK_LIVENESS closed")
			}
			p.latch.see(e)
		case <-p.exited:
			if p.tor != nil {
				return errors.New(p.tor.exitText())
			}
			return errors.New("tor exited")
		case <-timer.C:
			return errWatchTimeout
		}
	}
	return nil
}

func (p *netProbeRun) awaitOnion(ctx context.Context, socks, onion string) error {
	deadline := time.Now().Add(netProbeOnionTimeout)
	for {
		st := time.Now()
		err := dialOnionSocks(ctx, socks, onion, 80, netProbeDialTimeout)
		took := time.Since(st).Round(100 * time.Millisecond)
		if err == nil {
			logf("[%s] dial to the onion: reached in %s", netProbeTorName, took)
			return nil
		}
		logf("[%s] dial to the onion: %v after %s", netProbeTorName, err, took)
		if !time.Now().Before(deadline) {
			return fmt.Errorf("the onion was not reached through this tor within %s (last error: %v)", netProbeOnionTimeout, err)
		}
		if err := p.watch(netProbeDialRetry, never); err != nil && !errors.Is(err, errWatchTimeout) {
			return err
		}
	}
}

func dialOnionSocks(ctx context.Context, socks, host string, port int, timeout time.Duration) error {
	if len(host) > 255 {
		return fmt.Errorf("host name of %d bytes, SOCKS5 allows 255", len(host))
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	c, err := d.DialContext(ctx, "tcp", socks)
	if err != nil {
		return err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	_ = c.SetDeadline(time.Now().Add(timeout))
	err = socksConnectHost(c, host, port)
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func socksConnectHost(c net.Conn, host string, port int) error {
	if _, err := c.Write([]byte{5, 1, 0}); err != nil {
		return err
	}
	var greet [2]byte
	if _, err := io.ReadFull(c, greet[:]); err != nil {
		return err
	}
	if greet[0] != 5 || greet[1] != 0 {
		return fmt.Errorf("socks greeting answered %d %d", greet[0], greet[1])
	}
	req := []byte{5, 1, 0, 3, byte(len(host))}
	req = append(req, host...)
	req = append(req, byte(port>>8), byte(port))
	if _, err := c.Write(req); err != nil {
		return err
	}
	var head [4]byte
	if _, err := io.ReadFull(c, head[:]); err != nil {
		return err
	}
	if head[1] != 0 {
		return fmt.Errorf("socks reply %d", head[1])
	}
	var rest int
	switch head[3] {
	case 1:
		rest = 4 + 2
	case 4:
		rest = 16 + 2
	case 3:
		var n [1]byte
		if _, err := io.ReadFull(c, n[:]); err != nil {
			return err
		}
		rest = int(n[0]) + 2
	default:
		return fmt.Errorf("socks reply address type %d", head[3])
	}
	_, err := io.ReadFull(c, make([]byte, rest))
	return err
}

type dialTally struct {
	mu             sync.Mutex
	tries, reached int
}

func (t *dialTally) add(ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tries++
	if ok {
		t.reached++
	}
}

func (t *dialTally) counts() (tries, reached int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.tries, t.reached
}

func startDialLoop(parent context.Context, socks, onion string, tally *dialTally) (stop func()) {
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			st := time.Now()
			err := dialOnionSocks(ctx, socks, onion, 80, netProbeDialTimeout)
			if ctx.Err() != nil {
				return
			}
			tally.add(err == nil)
			took := time.Since(st).Round(100 * time.Millisecond)
			if err == nil {
				logf("[%s] dial to the onion: reached in %s", netProbeTorName, took)
			} else {
				logf("[%s] dial to the onion: %v after %s", netProbeTorName, err, took)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(netProbeDialEvery):
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			wg.Wait()
		})
	}
}

func waitText(d time.Duration) string {
	if d > 0 && d%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return d.String()
}

func secondsText(d time.Duration) string {
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func netProbeResultLines(r netProbeResult) []string {
	var head string
	switch r.outcome {
	case netProbePass:
		head = fmt.Sprintf("PASS          netprobe  NETWORK_LIVENESS DOWN %s after the cut, UP %s after the pass",
			secondsText(r.downAfter), secondsText(r.upAfter))
	case netProbeNoDown:
		head = fmt.Sprintf("NO DOWN       netprobe  NO DOWN within %s of the cut (a finding about tor, not a harness failure)",
			waitText(r.downWait))
	case netProbeNoUp:
		head = fmt.Sprintf("NO UP         netprobe  DOWN %s after the cut, but NO UP within %s of the pass (a finding about tor, not a harness failure)",
			secondsText(r.downAfter), waitText(r.upWait))
	default:
		head = "NOT MEASURED  netprobe  " + r.reason
		if r.downSeen {
			head += fmt.Sprintf(" (DOWN had come %s after the cut)", secondsText(r.downAfter))
		}
	}
	listText := func(s []string) string {
		if len(s) == 0 {
			return "none"
		}
		return strings.Join(s, ", ")
	}
	return []string{
		"──────── RESULTS ────────",
		"  " + head,
		fmt.Sprintf("  proxy: %d links before the cut, %d connections closed at the cut, %d held while cut, %d links in all",
			r.beforeCut.Links, r.closedAtCut, r.end.Held, r.end.Links),
		"  NETWORK_LIVENESS before the cut: " + listText(r.early),
		"  NETWORK_LIVENESS after the cut, not counted: " + listText(r.other),
		fmt.Sprintf("  dials to the onion from the cut on: %d, reached: %d", r.dials, r.dialsReached),
		"  dial to the onion after the pass: " + r.afterPass,
		fmt.Sprintf("  tor log: %s (this run from byte %d)", r.torLog, r.torLogFrom),
		"─────────────────────────",
	}
}
