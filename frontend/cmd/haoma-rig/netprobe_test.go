package main

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNetProbeTorrcGoesThroughTheProxy(t *testing.T) {
	network := "# written by haoma-rig -chutney from x; do not edit\nTestingTorNetwork 1\nDirAuthority test000a 127.0.0.1:7000 AAAA\nUseDefaultFallbackDirs 0\n"
	got := netProbeTorrc("/d/netprobe", network, "127.0.0.1:4242")
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if last := lines[len(lines)-1]; last != "Socks5Proxy 127.0.0.1:4242" {
		t.Fatalf("last line %q, want the Socks5Proxy line", last)
	}
	for _, want := range []string{
		"DataDirectory /d/netprobe",
		"ControlPort auto",
		"ControlPortWriteToFile /d/netprobe/control.port",
		"CookieAuthentication 1",
		"CookieAuthFile /d/netprobe/control.cookie",
		"SocksPort auto",
		"Log notice file /d/netprobe/tor.log",
		"TestingTorNetwork 1",
		"DirAuthority test000a 127.0.0.1:7000 AAAA",
	} {
		found := false
		for _, l := range lines {
			if l == want {
				found = true
			}
		}
		if !found {
			t.Errorf("torrc has no line %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "%include") {
		t.Errorf("torrc includes another file; only this file may decide where the probe tor goes:\n%s", got)
	}
	if n := strings.Count(got, "Socks5Proxy"); n != 1 {
		t.Errorf("%d Socks5Proxy lines, want 1", n)
	}

	pub := netProbeTorrc("/d/netprobe", "", "127.0.0.1:1")
	if strings.Contains(pub, "TestingTorNetwork") || !strings.HasSuffix(pub, "Socks5Proxy 127.0.0.1:1\n") {
		t.Errorf("public torrc:\n%s", pub)
	}

	joined := netProbeTorrc("/d", "TestingTorNetwork 1", "127.0.0.1:2")
	if !strings.Contains(joined, "TestingTorNetwork 1\nSocks5Proxy 127.0.0.1:2\n") {
		t.Errorf("network lines run into the proxy line:\n%s", joined)
	}
}

func TestParseLiveness(t *testing.T) {
	for line, want := range map[string]string{
		"650 NETWORK_LIVENESS DOWN\r\n": "DOWN",
		"650 NETWORK_LIVENESS UP\r\n":   "UP",
		"650 NETWORK_LIVENESS UP":       "UP",
	} {
		got, ok := parseLiveness(line)
		if !ok || got != want {
			t.Errorf("parseLiveness(%q) = %q %v, want %q", line, got, ok, want)
		}
	}
	for _, line := range []string{
		"250 OK\r\n",
		"650 CIRC 1 BUILT\r\n",
		"650 NETWORK_LIVENESS SIDEWAYS\r\n",
		"NETWORK_LIVENESS DOWN\r\n",
	} {
		if got, ok := parseLiveness(line); ok {
			t.Errorf("parseLiveness(%q) = %q, want no event", line, got)
		}
	}
}

func TestLivenessLatchCountsOnlyAfterTheCut(t *testing.T) {
	t0 := time.Now()
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	var l livenessLatch

	l.see(livenessEvent{"DOWN", at(1)})
	l.cutAt = at(10)
	l.see(livenessEvent{"DOWN", at(5)})
	l.see(livenessEvent{"UP", at(9)})
	if !l.downAt.IsZero() {
		t.Fatalf("a DOWN from before the cut counted")
	}
	if got := strings.Join(l.early, ","); got != "DOWN,DOWN,UP" {
		t.Fatalf("early = %q", got)
	}

	l.see(livenessEvent{"UP", at(11)})
	l.see(livenessEvent{"DOWN", at(40)})
	if !l.downAt.Equal(at(40)) {
		t.Fatalf("downAt = %v, want the DOWN at +40s", l.downAt)
	}
	l.see(livenessEvent{"UP", at(50)})
	if !l.upAt.IsZero() {
		t.Fatalf("an UP before the pass counted")
	}
	l.passAt = at(60)
	l.see(livenessEvent{"DOWN", at(61)})
	l.see(livenessEvent{"UP", at(63)})
	if !l.upAt.Equal(at(63)) || !l.downAt.Equal(at(40)) {
		t.Fatalf("upAt = %v downAt = %v", l.upAt, l.downAt)
	}
	if got := strings.Join(l.other, ","); got != "UP,UP,DOWN" {
		t.Fatalf("other = %q", got)
	}
}

func TestLivenessLatchNoUpWithoutDown(t *testing.T) {
	t0 := time.Now()
	l := livenessLatch{cutAt: t0, passAt: t0.Add(time.Minute)}
	l.see(livenessEvent{"UP", t0.Add(2 * time.Minute)})
	if !l.upAt.IsZero() {
		t.Fatalf("an UP with no DOWN before it counted")
	}
}

func TestBootstrapProgress(t *testing.T) {
	p, line := bootstrapProgress([]string{`status/bootstrap-phase=NOTICE BOOTSTRAP PROGRESS=100 TAG=done SUMMARY="Done"`, "OK"})
	if p != 100 || !strings.Contains(line, "TAG=done") {
		t.Fatalf("got %d %q", p, line)
	}
	if p, _ := bootstrapProgress([]string{`status/bootstrap-phase=NOTICE BOOTSTRAP PROGRESS=4 TAG=conn_done_proxy SUMMARY="Connected to a proxy"`}); p != 4 {
		t.Fatalf("got %d, want 4", p)
	}
	if p, _ := bootstrapProgress([]string{"OK"}); p != -1 {
		t.Fatalf("got %d for a reply with no phase, want -1", p)
	}
	if p, _ := bootstrapProgress([]string{"status/bootstrap-phase=NOTICE BOOTSTRAP PROGRESS=x"}); p != -1 {
		t.Fatalf("got %d for a bad PROGRESS, want -1", p)
	}
}

func TestServiceIDFrom(t *testing.T) {
	if got := serviceIDFrom([]string{"ServiceID=abcdef", "OK"}); got != "abcdef" {
		t.Fatalf("got %q", got)
	}
	if got := serviceIDFrom([]string{"OK"}); got != "" {
		t.Fatalf("got %q from a reply with no ServiceID", got)
	}
}

func TestWatch(t *testing.T) {
	t.Run("time runs out", func(t *testing.T) {
		p := &netProbeRun{events: make(chan livenessEvent), exited: make(chan struct{})}
		st := time.Now()
		if err := p.watch(50*time.Millisecond, never); !errors.Is(err, errWatchTimeout) {
			t.Fatalf("err = %v, want errWatchTimeout", err)
		}
		if took := time.Since(st); took < 50*time.Millisecond || took > 2*time.Second {
			t.Fatalf("took %s", took)
		}
	})
	t.Run("stops on the DOWN after the cut", func(t *testing.T) {
		ev := make(chan livenessEvent, 2)
		p := &netProbeRun{events: ev, exited: make(chan struct{})}
		p.latch.cutAt = time.Now()
		ev <- livenessEvent{"DOWN", p.latch.cutAt.Add(-time.Second)}
		ev <- livenessEvent{"DOWN", p.latch.cutAt.Add(time.Second)}
		if err := p.watch(5*time.Second, func() bool { return !p.latch.downAt.IsZero() }); err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(p.latch.early) != 1 {
			t.Fatalf("early = %v", p.latch.early)
		}
	})
	t.Run("tor exited", func(t *testing.T) {
		exited := make(chan struct{})
		close(exited)
		p := &netProbeRun{events: make(chan livenessEvent), exited: exited}
		if err := p.watch(5*time.Second, never); err == nil || errors.Is(err, errWatchTimeout) {
			t.Fatalf("err = %v, want tor exited", err)
		}
	})
	t.Run("event connection closed", func(t *testing.T) {
		ev := make(chan livenessEvent)
		close(ev)
		p := &netProbeRun{events: ev, exited: make(chan struct{})}
		if err := p.watch(5*time.Second, never); err == nil || errors.Is(err, errWatchTimeout) {
			t.Fatalf("err = %v, want the connection error", err)
		}
	})
}

func acceptAll(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	return ln.Addr().String()
}

func TestDialOnionSocksThroughCutProxy(t *testing.T) {
	p, err := startCutProxy()
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	host, portS, _ := net.SplitHostPort(acceptAll(t))
	port, err := strconv.Atoi(portS)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := dialOnionSocks(ctx, p.addr(), host, port, 2*time.Second); err != nil {
		t.Fatalf("dial while passing: %v", err)
	}
	if s := p.statsNow(); s.Links != 1 {
		t.Fatalf("links = %d, want 1", s.Links)
	}

	p.setCut(true)
	st := time.Now()
	if err := dialOnionSocks(ctx, p.addr(), host, port, 300*time.Millisecond); err == nil {
		t.Fatalf("dial while cut worked")
	}
	if took := time.Since(st); took > 2*time.Second {
		t.Fatalf("dial while cut took %s, timeout was 300ms", took)
	}

	cctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- dialOnionSocks(cctx, p.addr(), host, port, time.Minute) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("a cancelled dial did not end")
	}

	p.setCut(false)
	if err := dialOnionSocks(ctx, p.addr(), host, port, 2*time.Second); err != nil {
		t.Fatalf("dial after the pass: %v", err)
	}
}

func TestDialLoopStops(t *testing.T) {
	p, err := startCutProxy()
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	p.setCut(true)
	var tally dialTally
	stop := startDialLoop(context.Background(), p.addr(), "x.onion", &tally)
	time.Sleep(100 * time.Millisecond)
	done := make(chan struct{})
	go func() { stop(); stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("stop did not end a dial in progress")
	}
	if tries, _ := tally.counts(); tries != 0 {
		t.Fatalf("a stopped dial was counted: %d", tries)
	}
}

func TestNetProbeResultLines(t *testing.T) {
	base := netProbeResult{
		downWait: netProbeDownWait, upWait: netProbeUpWait,
		beforeCut: cutProxyStats{Links: 7}, closedAtCut: 5, end: cutProxyStats{Links: 9, Held: 3},
		dials: 4, dialsReached: 1, afterPass: "reached in 2.0s",
		torLog: "/d/netprobe/tor.log", torLogFrom: 123,
	}
	head := func(r netProbeResult) string { return netProbeResultLines(r)[1] }

	pass := base
	pass.outcome, pass.downAfter, pass.upAfter = netProbePass, 41200*time.Millisecond, 3100*time.Millisecond
	if h := head(pass); !strings.HasPrefix(strings.TrimSpace(h), "PASS") || !strings.Contains(h, "DOWN 41.2s after the cut, UP 3.1s after the pass") {
		t.Errorf("pass head %q", h)
	}

	noDown := base
	noDown.outcome = netProbeNoDown
	if h := head(noDown); !strings.Contains(h, "NO DOWN within 6m") || strings.Contains(h, "PASS") {
		t.Errorf("no-down head %q", h)
	}

	noUp := base
	noUp.outcome, noUp.downAfter = netProbeNoUp, 30*time.Second
	if h := head(noUp); !strings.Contains(h, "NO UP within 3m") || !strings.Contains(h, "DOWN 30.0s") {
		t.Errorf("no-up head %q", h)
	}

	nm := base
	nm.outcome, nm.reason = netProbeNotMeasured, "the cut closed no connection"
	if h := head(nm); !strings.Contains(h, "NOT MEASURED") || !strings.Contains(h, "the cut closed no connection") {
		t.Errorf("not-measured head %q", h)
	}

	if h := head(netProbeResult{}); !strings.Contains(h, "NOT MEASURED") {
		t.Errorf("zero result head %q", h)
	}

	all := strings.Join(netProbeResultLines(pass), "\n")
	for _, want := range []string{
		"RESULTS",
		"7 links before the cut, 5 connections closed at the cut, 3 held while cut",
		"NETWORK_LIVENESS before the cut: none",
		"dials to the onion from the cut on: 4, reached: 1",
		"after the pass: reached in 2.0s",
		"/d/netprobe/tor.log (this run from byte 123)",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("RESULTS has no %q:\n%s", want, all)
		}
	}
}

func TestWaitText(t *testing.T) {
	for d, want := range map[time.Duration]string{
		6 * time.Minute:  "6m",
		3 * time.Minute:  "3m",
		90 * time.Second: "1m30s",
	} {
		if got := waitText(d); got != want {
			t.Errorf("waitText(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestNetProbeRefusesSystemTor(t *testing.T) {
	err := run(config{scenario: scenarioNetProbe, systemTor: true, n: 2})
	if err == nil || !strings.Contains(err.Error(), "-system-tor") {
		t.Fatalf("err = %v, want a refusal of -system-tor", err)
	}
}

func TestNetProbeWaits(t *testing.T) {
	if netProbeDownWait != 6*time.Minute || netProbeUpWait != 3*time.Minute || netProbeDialEvery != 30*time.Second {
		t.Fatalf("waits %s %s %s", netProbeDownWait, netProbeUpWait, netProbeDialEvery)
	}
}
