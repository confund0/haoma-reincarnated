package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"haoma-frontend/internal/backendapi"
)

func TestHaomadArgsOwnTor(t *testing.T) {
	r := &rig{root: "/r/dev-a", logLevel: "debug", torBin: "/usr/bin/tor", torDir: "/t/dev-a",
		torControl: "127.0.0.1:1", torSocks: "127.0.0.1:2"}
	args := r.haomadArgs("127.0.0.1:0")
	i := slices.Index(args, "--manage-tor")
	if i < 0 || args[i+1] != "/usr/bin/tor" {
		t.Fatalf("no --manage-tor /usr/bin/tor in %v", args)
	}
	j := slices.Index(args, "--tor-data-dir")
	if j < 0 || args[j+1] != "/t/dev-a" {
		t.Fatalf("no --tor-data-dir /t/dev-a in %v", args)
	}
	if slices.Contains(args, "--tor-control") || slices.Contains(args, "--tor-socks") {
		t.Fatalf("own tor passes a shared tor's address: %v", args)
	}
}

func TestHaomadArgsSystemTor(t *testing.T) {
	r := &rig{root: "/r/dev-a", logLevel: "debug", torBin: "/usr/bin/tor"}
	args := r.haomadArgs("127.0.0.1:0")
	if slices.Contains(args, "--manage-tor") || slices.Contains(args, "--tor-data-dir") {
		t.Fatalf("-system-tor starts a tor: %v", args)
	}
	if slices.Contains(args, "--tor-control") {
		t.Fatalf("no address given, but one passed: %v", args)
	}
	r.torControl, r.torSocks = "127.0.0.1:1", "127.0.0.1:2"
	args = r.haomadArgs("127.0.0.1:0")
	i := slices.Index(args, "--tor-control")
	j := slices.Index(args, "--tor-socks")
	if i < 0 || args[i+1] != "127.0.0.1:1" || j < 0 || args[j+1] != "127.0.0.1:2" {
		t.Fatalf("shared tor address not passed: %v", args)
	}
}

func TestHaomadEnvPassword(t *testing.T) {
	own := &rig{bePass: "be", torPass: "shell-pw", torDir: "/t/dev-a"}
	env := own.haomadEnv()

	if v, ok := env["HAOMA_TOR_PASSWORD"]; !ok || v != "" {
		t.Fatalf("own tor: HAOMA_TOR_PASSWORD = %q (set %v), want set and empty", v, ok)
	}
	sys := &rig{bePass: "be", torPass: "pw"}
	if got := sys.haomadEnv()["HAOMA_TOR_PASSWORD"]; got != "pw" {
		t.Fatalf("system tor: HAOMA_TOR_PASSWORD = %q, want pw", got)
	}
	t.Setenv("HAOMA_TOR_PASSWORD", "shell-pw")
	for _, kv := range childEnv(own.haomadEnv()) {
		if kv == "HAOMA_TOR_PASSWORD=shell-pw" {
			t.Fatal("the shell's tor password reached a haomad with its own tor")
		}
	}
}

func TestPrepareTorDir(t *testing.T) {
	base := t.TempDir()
	rigBase := filepath.Join(base, "rig")
	dir := torDirFor(filepath.Join(base, "rig-tor"), "dev-a")
	if dir != filepath.Join(base, "rig-tor", "dev-a") {
		t.Fatalf("torDirFor = %s", dir)
	}
	if err := prepareTorDir("dev-a", dir, rigBase); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o700 {
		t.Fatalf("mode %o, want 700", st.Mode().Perm())
	}

	kept := filepath.Join(dir, "cached-microdesc-consensus")
	if err := os.WriteFile(kept, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := prepareTorDir("dev-a", dir, rigBase); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Fatalf("tor state not kept: %v", err)
	}
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		t.Fatalf("mode %o after reuse, want 700", st.Mode().Perm())
	}
}

func TestPrepareTorDirRefusesRigBase(t *testing.T) {
	base := filepath.Join(t.TempDir(), "rig")
	for _, dir := range []string{
		base,
		filepath.Join(base, "dev-a"),

		filepath.Join(base, "rig-tor", "dev-a"),
	} {
		if err := prepareTorDir("dev-a", dir, base); err == nil {
			t.Fatalf("%s is inside the rig base and was taken", dir)
		}
	}

	if err := prepareTorDir("dev-a", base+"-tor", base); err != nil {
		t.Fatalf("%s-tor refused: %v", base, err)
	}
}

func TestPrepareTorDirRefusesRigBaseRelative(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := prepareTorDir("dev-a", "tmp/rig/rig-tor/dev-a", "tmp/rig"); err == nil {
		t.Fatal("a relative tor dir inside a relative base was taken")
	}
	if err := prepareTorDir("dev-a", "tmp/rig-tor/dev-a", "./tmp/../tmp/rig"); err != nil {
		t.Fatalf("a relative tor dir outside the base was refused: %v", err)
	}
}

func TestResolveTorPathsMakesThemAbsolute(t *testing.T) {
	wd := t.TempDir()
	t.Chdir(wd)
	cfg := config{repoRoot: "/repo", torDirs: "tor-dirs", torBin: "bin/tor"}
	if err := resolveTorPaths(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.torDirs != filepath.Join(wd, "tor-dirs") || cfg.torBin != filepath.Join(wd, "bin", "tor") {
		t.Fatalf("torDirs %q, torBin %q", cfg.torDirs, cfg.torBin)
	}
	cfg = config{repoRoot: "/repo", torBin: "/usr/bin/tor"}
	if err := resolveTorPaths(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.torDirs != "/repo/tmp/rig-tor" {
		t.Fatalf("default torDirs %q", cfg.torDirs)
	}
}

func TestTorStartFailureNamesTheCause(t *testing.T) {
	root := t.TempDir()
	torDir := filepath.Join(t.TempDir(), "dev-a")
	if err := os.MkdirAll(torDir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := "time=1 level=ERROR msg=fatal service=haomad err=\"an earlier boot\"\n"
	now := "time=2 level=INFO msg=\"embedded tor: starting\" service=haomad\n" +
		"time=3 level=ERROR msg=fatal service=haomad err=\"embedded tor bootstrap: embedded: tor exited before publishing control port: os: process already finished\"\n"
	if err := os.WriteFile(filepath.Join(root, "haomad.log"), []byte(old+now), 0o600); err != nil {
		t.Fatal(err)
	}
	torOld := "Sep 28 10:00:00.000 [notice] an earlier run\n"
	torNow := "Sep 28 11:00:00.000 [warn] It looks like another Tor process is running with the same data directory.\n" +
		"Sep 28 11:00:05.000 [err] No, it's still there.  Exiting.\n"
	if err := os.WriteFile(filepath.Join(torDir, "tor.log"), []byte(torOld+torNow), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &rig{root: root, torDir: torDir}
	got := r.torStartFailure(int64(len(old)), int64(len(torOld)))
	for _, want := range []string{"tor exited before publishing control port", "another Tor process", "Exiting."} {
		if !strings.Contains(got, want) {
			t.Errorf("failure text lacks %q:\n%s", want, got)
		}
	}
	for _, notWant := range []string{"an earlier boot", "an earlier run"} {
		if strings.Contains(got, notWant) {
			t.Errorf("failure text holds an earlier launch's line %q:\n%s", notWant, got)
		}
	}
}

func TestPreserveRunKeepsThisRunsTorLog(t *testing.T) {
	base := t.TempDir()
	torDir := filepath.Join(t.TempDir(), "dev-a")
	if err := os.MkdirAll(torDir, 0o700); err != nil {
		t.Fatal(err)
	}
	torOld := "an earlier run\n"
	if err := os.WriteFile(filepath.Join(torDir, "tor.log"), []byte(torOld+"this run\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &rig{name: "dev-a", root: filepath.Join(base, "dev-a"), torDir: torDir,
		torLogFrom: int64(len(torOld)), torLogFromSet: true}
	dst := preserveRun(base, "20260928-120000", runPassed.prefix(), []*rig{r})
	got, err := os.ReadFile(filepath.Join(dst, "dev-a", keptTorLog))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "this run\n" {
		t.Fatalf("kept tor log = %q, want this run's part only", got)
	}
}

func TestCheckTorConfig(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "tor")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		cfg     config
		wantErr string
		wantIn  string
	}{
		{"own tor needs no password", config{torBin: bin, torDirs: "/t"}, "", "HAOMA_TOR_PASSWORD is not needed"},
		{"own tor refuses a shared address", config{torBin: bin, torControl: "127.0.0.1:9051", torSocks: "127.0.0.1:9050"}, "need -system-tor", ""},
		{"own tor needs its binary", config{torBin: filepath.Join(t.TempDir(), "none")}, "tor binary", ""},
		{"system tor needs the password", config{systemTor: true}, "HAOMA_TOR_PASSWORD is unset", ""},
		{"system tor, one address", config{systemTor: true, torPass: "pw", torControl: "127.0.0.1:1"}, "together or not at all", ""},
		{"system tor", config{systemTor: true, torPass: "pw"}, "", "system tor at 127.0.0.1:9051"},
	}
	for _, c := range cases {
		line, err := checkTorConfig(&c.cfg)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: err %v, want %q", c.name, err, c.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !strings.Contains(line, c.wantIn) {
			t.Errorf("%s: start line %q lacks %q", c.name, line, c.wantIn)
		}
	}
}

const sampleHaomadLog = `time=2026-09-28T10:00:00.000Z level=INFO msg="tor bootstrap phase" service=haomad bootstrap=100 tag=done summary=Done
time=2026-09-28T10:05:00.000Z level=INFO msg="embedded tor: starting" service=haomad
time=2026-09-28T10:05:01.000Z level=INFO msg="tor bootstrap phase" service=haomad bootstrap=5 tag=conn summary="Connecting to a relay"
time=2026-09-28T10:05:11.000Z level=WARN msg="tor bootstrap warning" service=haomad bootstrap=5 tag=conn warning="Connection refused" reason=CONNECTREFUSED count=3 recommendation=ignore
time=2026-09-28T10:05:21.000Z level=INFO msg="tor bootstrap phase" service=haomad bootstrap=50 tag=loading_descriptors summary="Loading relay descriptors"
`

func TestLastBootstrapStep(t *testing.T) {
	path := filepath.Join(t.TempDir(), "haomad.log")
	if err := os.WriteFile(path, []byte(sampleHaomadLog), 0o600); err != nil {
		t.Fatal(err)
	}
	from := int64(strings.Index(sampleHaomadLog, "time=2026-09-28T10:05:00"))
	step, warn := lastBootstrapStep(path, from)
	if step.progress != 50 || step.tag != "loading_descriptors" || step.summary != "Loading relay descriptors" {
		t.Fatalf("step = %+v", step)
	}
	if warn.warning != "Connection refused" || warn.reason != "CONNECTREFUSED" || warn.tag != "conn" {
		t.Fatalf("warn = %+v", warn)
	}

	onlyOld := int64(strings.Index(sampleHaomadLog, "time=2026-09-28T10:05:00"))
	if err := os.WriteFile(path, []byte(sampleHaomadLog[:onlyOld]), 0o600); err != nil {
		t.Fatal(err)
	}
	if step, _ := lastBootstrapStep(path, onlyOld); step.tag != "" {
		t.Fatalf("read an earlier boot's step: %+v", step)
	}
}

type fakeTor struct {
	readings []backendapi.TorHealth
	started  string
	polls    int
}

func (f *fakeTor) TorInfo(context.Context) (backendapi.TorInfoResponse, error) {
	i := min(f.polls, len(f.readings)-1)
	f.polls++
	return backendapi.TorInfoResponse{Health: f.readings[i]}, nil
}

func (f *fakeTor) SystemInfo(context.Context) (backendapi.SystemInfo, error) {
	return backendapi.SystemInfo{Tor: backendapi.TorInfo{Version: "0.4.9.13", StartedAt: f.started}}, nil
}

func testWait(api torReader, ownTor bool, launched time.Time, logPath string) torWait {
	return torWait{
		api: api, ownTor: ownTor, launched: launched, exited: make(chan struct{}),
		timeout: 300 * time.Millisecond, interval: 10 * time.Millisecond,
		logPath: logPath, torLog: "/t/dev-a/tor.log",
	}
}

func TestTorWaitReady(t *testing.T) {
	launched := time.Now()
	f := &fakeTor{
		readings: []backendapi.TorHealth{{Bootstrap: 5}, {Bootstrap: 50}, {Bootstrap: 100, Ready: true}},
		started:  launched.UTC().Format(time.RFC3339),
	}
	res, err := testWait(f, true, launched, "").run()
	if err != nil {
		t.Fatal(err)
	}
	if res.version != "0.4.9.13" || f.polls != 3 {
		t.Fatalf("res %+v after %d polls", res, f.polls)
	}
}

func TestTorWaitRefusesAnOlderTor(t *testing.T) {
	launched := time.Now()
	f := &fakeTor{
		readings: []backendapi.TorHealth{{Bootstrap: 100, Ready: true}},
		started:  launched.Add(-3 * time.Hour).UTC().Format(time.RFC3339),
	}
	_, err := testWait(f, true, launched, "").run()
	var notOwn errNotOwnTor
	if !errors.As(err, &notOwn) {
		t.Fatalf("a tor started before the launch passed as the rig's own: %v", err)
	}

	if _, err := testWait(f, false, launched, "").run(); err != nil {
		t.Fatalf("system tor: %v", err)
	}
}

func TestTorWaitTimeoutNamesTheStep(t *testing.T) {
	path := filepath.Join(t.TempDir(), "haomad.log")
	if err := os.WriteFile(path, []byte(sampleHaomadLog), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeTor{readings: []backendapi.TorHealth{{Bootstrap: 50}}}
	w := testWait(f, true, time.Now(), path)
	w.logFrom = int64(strings.Index(sampleHaomadLog, "time=2026-09-28T10:05:00"))
	_, err := w.run()
	if err == nil {
		t.Fatal("a tor stuck at 50% passed the wait")
	}
	for _, want := range []string{
		"did not reach 100%",
		"last reading: 50%",
		"loading_descriptors (Loading relay descriptors)",
		`"Connection refused", reason CONNECTREFUSED`,
		"/t/dev-a/tor.log",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("failure text lacks %q:\n%s", want, err)
		}
	}
}

func TestTorWaitHaomadExited(t *testing.T) {
	f := &fakeTor{readings: []backendapi.TorHealth{{Bootstrap: 5}}}
	w := testWait(f, true, time.Now(), "")
	w.timeout = time.Minute
	ended := make(chan struct{})
	close(ended)
	w.exited = ended
	start := time.Now()
	_, err := w.run()
	if err == nil || !strings.Contains(err.Error(), "haomad exited") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("the wait ran on after haomad exited")
	}
}

func TestSlogValue(t *testing.T) {
	line := `time=x level=INFO msg="tor bootstrap phase" service=haomad bootstrap=45 tag=requesting_descriptors summary="Asking for \"relay\" descriptors"`
	if got := slogValue(line, "summary"); got != `Asking for "relay" descriptors` {
		t.Fatalf("summary = %q", got)
	}
	if got := slogValue(line, "tag"); got != "requesting_descriptors" {
		t.Fatalf("tag = %q", got)
	}
	if got := slogValue(line, "reason"); got != "" {
		t.Fatalf("reason = %q", got)
	}
}
