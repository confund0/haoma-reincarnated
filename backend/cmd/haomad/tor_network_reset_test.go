package main

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"haoma/internal/tor/health"
)

func newResetDaemon(t *testing.T) (*daemon, *fakeTor) {
	t.Helper()
	d, f := newTorDaemon(t)
	d.cfg.manageTor = "/usr/bin/tor"
	d.cfg.torSocks = f.socksAddr
	return d, f
}

var wantResetSetconfs = []string{
	`SocksPort="127.0.0.1:40001"`,
	`DisableNetwork="1"`,
	`DisableNetwork="0"`,
}

func TestTorNetworkReset_PinThenToggleThenKick(t *testing.T) {
	d, f := newResetDaemon(t)
	st := attachOutbox(t, d)
	queueStalled(t, st, livePeerDest(t, d, "g"), "nc-g1")
	logs := captureLog(t)

	res := d.networkChanged(t.Context(), networkChangeSourceOS)

	if got := f.setconfs(); !reflect.DeepEqual(got, wantResetSetconfs) {
		t.Errorf("SETCONFs = %q, want %q", got, wantResetSetconfs)
	}
	if res.ResetFailed || res.Failed || res.Rows != 1 {
		t.Errorf("result = %+v, want the reset done and one row kicked", res)
	}
	assertDue(t, st, "nc-g1")
	if got := d.socksAddr(); got != "127.0.0.1:40001" {
		t.Errorf("haomad dials %s after the reset, want the pinned 127.0.0.1:40001", got)
	}
	if len(logLines(logs, `msg="`+torNetworkResetLine+`"`, "socks_moved=false", "took_ms=")) != 1 {
		t.Errorf("no single %q line with socks_moved=false:\n%s", torNetworkResetLine, logs.String())
	}

	reset := logLines(logs, `msg="`+torNetworkResetLine+`"`)
	kicked := logLines(logs, `msg="`+networkChangeKickedLine+`"`)
	if len(reset) != 1 || len(kicked) != 1 || indexOfLine(logs, reset[0]) > indexOfLine(logs, kicked[0]) {
		t.Errorf("the reset line does not come before the kicked line:\n%s", logs.String())
	}
}

func indexOfLine(buf *syncBuffer, line string) int {
	for i, l := range logLines(buf) {
		if l == line {
			return i
		}
	}
	return -1
}

func TestTorNetworkReset_NotOnSourceTor(t *testing.T) {
	d, f := newResetDaemon(t)
	attachOutbox(t, d)
	if res := d.networkChanged(t.Context(), networkChangeSourceTor); res.Dropped || res.ResetFailed {
		t.Fatalf("result = %+v", res)
	}
	if got := f.setconfs(); len(got) != 0 {
		t.Errorf("a source=tor run sent %q", got)
	}
}

func TestTorNetworkReset_NotOnASystemTor(t *testing.T) {
	d, f := newResetDaemon(t)
	d.cfg.manageTor = ""
	attachOutbox(t, d)
	if res := d.networkChanged(t.Context(), networkChangeSourceOS); res.ResetFailed {
		t.Fatalf("result = %+v; a reset left out is not a failed one", res)
	}
	if got := f.setconfs(); len(got) != 0 {
		t.Errorf("a system tor got %q", got)
	}
}

func TestTorNetworkReset_FailureDoesNotBlockTheKick(t *testing.T) {
	for name, setup := range map[string]func(d *daemon, f *fakeTor){
		"tor refuses the toggle": func(_ *daemon, f *fakeTor) {
			f.setconfReply["DisableNetwork"] = "552 Unrecognized option\r\n"
		},
		"tor refuses the pin": func(_ *daemon, f *fakeTor) {
			f.setconfReply["SocksPort"] = "513 Unacceptable option value\r\n"
		},
		"no control connection": func(d *daemon, _ *fakeTor) {
			d.ctrlConn = nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			d, f := newResetDaemon(t)
			st := attachOutbox(t, d)
			queueStalled(t, st, livePeerDest(t, d, "h"), "nc-h1")
			logs := captureLog(t)
			setup(d, f)

			res := d.networkChanged(t.Context(), networkChangeSourceOS)

			if !res.ResetFailed || res.Failed || res.Rows != 1 {
				t.Errorf("result = %+v, want reset_failed with the row kicked", res)
			}
			assertDue(t, st, "nc-h1")
			if len(logLines(logs, "level=WARN", `msg="`+torNetworkResetFailedLine+`"`)) != 1 {
				t.Errorf("no Warn %q line:\n%s", torNetworkResetFailedLine, logs.String())
			}
			if len(logLines(logs, `msg="`+torNetworkResetLine+`"`)) != 0 {
				t.Error("a failed reset logged the done line too")
			}

			if res := d.networkChanged(t.Context(), networkChangeSourceOS); !res.Dropped {
				t.Errorf("second call = %+v, want dropped", res)
			}
		})
	}
}

func TestTorNetworkReset_ListenerMovedDialerFollows(t *testing.T) {
	d, f := newResetDaemon(t)
	attachOutbox(t, d)
	f.moveAlways = true
	logs := captureLog(t)

	if res := d.networkChanged(t.Context(), networkChangeSourceOS); res.ResetFailed {
		t.Fatalf("result = %+v", res)
	}
	if got := d.socksAddr(); got != "127.0.0.1:40002" {
		t.Errorf("haomad dials %s, want the moved listener 127.0.0.1:40002", got)
	}
	if len(logLines(logs, `msg="`+torNetworkResetLine+`"`, "socks_moved=true")) != 1 {
		t.Errorf("no %q line with socks_moved=true:\n%s", torNetworkResetLine, logs.String())
	}
}

func TestTorNetworkReset_OwnLivenessEventsDoNotRunTheHandler(t *testing.T) {
	d, f := newResetDaemon(t)
	st := attachOutbox(t, d)
	f.toggleEvents = true
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	logs := captureLog(t)
	d.watchNetworkLiveness(ctx, d.ctrlConn)

	f.push(t, 1, "650 NETWORK_LIVENESS DOWN")
	waitFor(t, "the DOWN before the reset", func() bool { return livenessDown(d) })

	if res := d.networkChanged(t.Context(), networkChangeSourceOS); res.ResetFailed {
		t.Fatalf("result = %+v", res)
	}

	f.push(t, 1, "650 NETWORK_LIVENESS UP")
	waitFor(t, "the toggle's events and the UP in the quiet window", func() bool { return livenessSeen(d) == 4 })
	queueStalled(t, st, livePeerDest(t, d, "i"), "nc-i1")

	if got := logLines(logs, `msg="`+networkChangeKickedLine+`"`, "source=tor"); len(got) != 0 {
		t.Errorf("the reset's own events ran the handler as source=tor: %q", got)
	}
	if src := gateSource(d); src != networkChangeSourceOS {
		t.Errorf("the gate's last run is %q, want os", src)
	}
	if !livenessDown(d) {
		t.Fatal("the DOWN from before the reset was dropped; tor's UP after it would not run the handler")
	}
	assertNotDue(t, st, "nc-i1")

	d.liveness.mu.Lock()
	d.liveness.quietUntil = time.Now().Add(-time.Second)
	d.liveness.mu.Unlock()
	f.push(t, 1, "650 NETWORK_LIVENESS UP")
	waitFor(t, "the row kicked by tor's UP after the window", rowDue(st, "nc-i1"))
	if src := gateSource(d); src != networkChangeSourceTor {
		t.Errorf("the gate's last run is %q, want tor", src)
	}
}

func gateSource(d *daemon) string {
	d.netChange.mu.Lock()
	defer d.netChange.mu.Unlock()
	return d.netChange.source
}

func livenessSeen(d *daemon) int {
	d.liveness.mu.Lock()
	defer d.liveness.mu.Unlock()
	return d.liveness.seen
}

func TestNetworkLiveness_OwnReset(t *testing.T) {
	var l networkLiveness
	t0 := time.Now()
	l.observeAt("DOWN", t0)
	l.ownResetStart()
	if fire, own := l.observeAt("UP", t0.Add(time.Second)); fire || !own {
		t.Errorf("UP during the reset: fire=%v own=%v, want ignored", fire, own)
	}
	end := t0.Add(2 * time.Second)
	l.ownResetEnd(end)
	if fire, own := l.observeAt("UP", end.Add(livenessOwnResetQuiet-time.Millisecond)); fire || !own {
		t.Errorf("UP inside the quiet window: fire=%v own=%v, want ignored", fire, own)
	}
	if fire, own := l.observeAt("UP", end.Add(livenessOwnResetQuiet)); !fire || own {
		t.Errorf("UP after the window, DOWN before the reset: fire=%v own=%v, want fire", fire, own)
	}

	var quiet networkLiveness
	quiet.ownResetStart()
	quiet.observeAt("DOWN", t0.Add(time.Second))
	quiet.ownResetEnd(end)
	if fire, own := quiet.observeAt("UP", end.Add(time.Minute)); fire || own {
		t.Errorf("UP after the window, only the reset's own DOWN before: fire=%v own=%v, want neither", fire, own)
	}
}

func TestTorNetworkReset_WatchdogTickPutsTheNetworkBack(t *testing.T) {
	d, f := newResetDaemon(t)
	attachOutbox(t, d)
	d.torPoller = health.New("127.0.0.1:1", "")
	logs := captureLog(t)
	f.setconfReply["DisableNetwork=0"] = "553 Unable to set option: Failed to bind one of the listener ports.\r\n"

	if res := d.networkChanged(t.Context(), networkChangeSourceOS); !res.ResetFailed {
		t.Fatalf("result = %+v, want reset_failed", res)
	}
	if !torNetworkOff(d) {
		t.Fatal("tor's network may be off, and nothing marks it")
	}
	if len(logLines(logs, "level=ERROR", "may be OFF")) != 1 {
		t.Errorf("no Error line for tor's network left off:\n%s", logs.String())
	}

	watchdogTick(t.Context(), d)
	if !torNetworkOff(d) {
		t.Fatal("the mark was cleared while tor still refused")
	}
	delete(f.setconfReply, "DisableNetwork=0")
	before := countOn(f, 1)
	watchdogTick(t.Context(), d)
	if got := countOn(f, 1) - before; got != 1 {
		t.Errorf("the tick sent DisableNetwork=0 %d times, want 1", got)
	}
	if torNetworkOff(d) {
		t.Error("the mark stayed after tor took DisableNetwork=0")
	}

	if got := d.socksAddr(); got != "127.0.0.1:40002" {
		t.Errorf("haomad dials %s, want the un-pinned listener 127.0.0.1:40002", got)
	}

	before = countOn(f, 1)
	watchdogTick(t.Context(), d)
	if got := countOn(f, 1) - before; got != 0 {
		t.Errorf("a tick with tor's network on sent DisableNetwork=0 %d times", got)
	}
}

func torNetworkOff(d *daemon) bool {
	d.ctrlMu.Lock()
	defer d.ctrlMu.Unlock()
	return d.torNetworkOff
}

func countOn(f *fakeTor, conn int) int {
	n := 0
	for _, c := range f.sent() {
		if c.conn == conn && strings.HasPrefix(c.line, "SETCONF ") && strings.Contains(c.line, `DisableNetwork="0"`) {
			n++
		}
	}
	return n
}

func TestTorNetworkReset_UnpinsWhenThePinnedPortCannotBind(t *testing.T) {
	d, f := newResetDaemon(t)
	attachOutbox(t, d)
	d.torPoller = health.New("127.0.0.1:1", "")
	f.refuseOnPinned = true
	logs := captureLog(t)

	if res := d.networkChanged(t.Context(), networkChangeSourceOS); !res.ResetFailed {
		t.Fatalf("result = %+v, want reset_failed", res)
	}
	want := append(append([]string(nil), wantResetSetconfs...), `DisableNetwork="0"`, `SocksPort="auto" DisableNetwork="0"`)
	if got := f.setconfs(); !reflect.DeepEqual(got, want) {
		t.Errorf("SETCONFs = %q, want %q", got, want)
	}
	if torNetworkOff(d) {
		t.Error("the mark stayed after tor took DisableNetwork=0 un-pinned")
	}
	f.mu.Lock()
	netOff := f.netOff
	f.mu.Unlock()
	if netOff {
		t.Error("the fake's network is still off")
	}
	if got := d.socksAddr(); got != "127.0.0.1:40002" {
		t.Errorf("haomad dials %s, want the un-pinned listener 127.0.0.1:40002", got)
	}
	if len(logLines(logs, "level=WARN", "SOCKS port un-pinned", "socks_moved=true")) != 1 {
		t.Errorf("no Warn line with socks_moved=true:\n%s", logs.String())
	}

	n := len(f.setconfs())
	watchdogTick(t.Context(), d)
	if got := len(f.setconfs()); got != n {
		t.Errorf("a tick after the restore sent %q", f.setconfs()[n:])
	}
}

func TestTorNetworkReset_ReconnectPutsTheNetworkBack(t *testing.T) {
	d, f := newResetDaemon(t)
	d.torNetworkOff = true
	if !watchdogReconnect(t.Context(), d, "test", false) {
		t.Fatal("reconnect failed")
	}
	if got := countOn(f, 2); got != 1 {
		t.Errorf("DisableNetwork=0 on the new connection %d times, want once", got)
	}
	if torNetworkOff(d) {
		t.Error("the mark stayed after the network was put back")
	}
}

func TestTorNetworkReset_FetchRetryAfterTheReset(t *testing.T) {
	saved := resetRetryDelay
	resetRetryDelay = 100 * time.Millisecond
	t.Cleanup(func() { resetRetryDelay = saved })

	d, _ := newResetDaemon(t)
	attachOutbox(t, d)
	d.files = newFilesTestDaemon(t).files
	dial, tok := attachFailedFetch(t, d)
	if res := d.networkChanged(t.Context(), networkChangeSourceOS); res.ResetFailed || res.Fetches != 1 {
		t.Fatalf("result = %+v", res)
	}

	waitRetried(t, d, dial, tok, 2, 1)

	d.scheduleResetRetry()
	d.scheduleResetRetry()
	waitRetried(t, d, dial, tok, 3, 1)
	time.Sleep(3 * resetRetryDelay)
	if n := dial.n.Load(); n != 3 {
		t.Errorf("dials = %d after two resets in a row, want 3 (one retry)", n)
	}

	ctx, cancel := context.WithCancel(context.Background())
	d.bgCtx = ctx
	d.scheduleResetRetry()
	cancel()
	time.Sleep(3 * resetRetryDelay)
	if n := dial.n.Load(); n != 3 {
		t.Errorf("dials = %d after a cancelled retry, want 3", n)
	}
}
