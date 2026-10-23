package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const mockDaemonStderrMarker = "mock-daemon-stderr-marker"

var fakeTorMain func()

func TestMain(m *testing.M) {
	if os.Getenv("HAOMA_TEXT_TEST_MODE") == "fake-tor" && fakeTorMain != nil {
		fakeTorMain()
	}
	if os.Getenv("HAOMA_TEXT_TEST_MODE") == "mock-daemon" {
		if dir := os.Getenv("HAOMA_TEXT_TEST_ARGV_DIR"); dir != "" {
			name := filepath.Join(dir, fmt.Sprintf("argv-%d", os.Getpid()))
			_ = os.WriteFile(name, []byte(strings.Join(os.Args[1:], "\x00")), 0o600)

			in, _ := io.ReadAll(os.Stdin)
			_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("stdin-%d", os.Getpid())), in, 0o600)
		}
		fmt.Fprintln(os.Stderr, mockDaemonStderrMarker)
		fmt.Fprintf(os.Stdout, `{"status":"ready","api_addr":"127.0.0.1:1"}`+"\n")
		time.Sleep(5 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestDaemonLogPaths_LogAndStderrCaptureDiffer(t *testing.T) {
	root := t.TempDir()

	cases := []struct {
		name       string
		logName    string
		stderrName string
	}{
		{"haomad", haomadLogName, haomadStderrName},
		{"haoma", haomaLogName, haomaStderrName},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logPath, stderrPath := daemonLogPaths(root, tc.logName, tc.stderrName)
			if logPath == stderrPath {
				t.Errorf("log path and stderr capture path are identical: %s", logPath)
			}
			wantLog := filepath.Join(root, tc.logName)
			wantStderr := filepath.Join(root, tc.stderrName)
			if logPath != wantLog {
				t.Errorf("logPath = %q, want %q", logPath, wantLog)
			}
			if stderrPath != wantStderr {
				t.Errorf("stderrPath = %q, want %q", stderrPath, wantStderr)
			}
		})
	}
}

func TestSpawnAndRun_WiresStderrCaptureNotLogPath(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("chmod root: %v", err)
	}

	vc := newTestController(t)
	t.Setenv("HAOMA_TEXT_TEST_MODE", "mock-daemon")

	opts := spawnOpts{
		haomadBin: os.Args[0],
		haomaBin:  os.Args[0],
		logLevel:  "warn",
		tor:       torSetup{system: true},
	}

	done := make(chan error, 1)
	go func() { done <- spawnAndRun(root, vc, opts) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("spawnAndRun: want error (mock daemons don't serve real IPC), got nil")
		}
		if !strings.Contains(err.Error(), "ipc client") {
			t.Fatalf("spawnAndRun err = %v, want ipc-client failure (both daemons should have spawned first)", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("spawnAndRun did not return within 20s")
	}

	haomadLog, haomadStderr := daemonLogPaths(root, haomadLogName, haomadStderrName)
	haomaLog, haomaStderr := daemonLogPaths(root, haomaLogName, haomaStderrName)

	requireMarker(t, haomadStderr)
	requireMarker(t, haomaStderr)
	requireAbsent(t, haomadLog)
	requireAbsent(t, haomaLog)
}

func TestSpawnAndRun_PassesCallStatsToBoth(t *testing.T) {
	for _, tc := range []struct {
		name      string
		callStats bool
	}{
		{"on", true},
		{"off (default)", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatalf("chmod root: %v", err)
			}
			argvDir := t.TempDir()
			vc := newTestController(t)
			t.Setenv("HAOMA_TEXT_TEST_MODE", "mock-daemon")
			t.Setenv("HAOMA_TEXT_TEST_ARGV_DIR", argvDir)

			opts := spawnOpts{
				haomadBin: os.Args[0],
				haomaBin:  os.Args[0],
				logLevel:  "warn",
				tor:       torSetup{system: true},
				callStats: tc.callStats,
			}
			done := make(chan error, 1)
			go func() { done <- spawnAndRun(root, vc, opts) }()
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), "ipc client") {
					t.Fatalf("spawnAndRun err = %v, want the ipc-client failure after both spawns", err)
				}
			case <-time.After(20 * time.Second):
				t.Fatal("spawnAndRun did not return within 20s")
			}

			files, _ := filepath.Glob(filepath.Join(argvDir, "argv-*"))
			if len(files) != 2 {
				t.Fatalf("%d argv files written, want 2", len(files))
			}
			var haomadArgv, haomaArgv []string
			for _, f := range files {
				raw, err := os.ReadFile(f)
				if err != nil {
					t.Fatal(err)
				}
				argv := strings.Split(string(raw), "\x00")
				isHaomad := false
				for _, a := range argv {
					if a == "--runtime-file" {
						isHaomad = true
					}
				}
				if isHaomad {
					haomadArgv = argv
				} else {
					haomaArgv = argv
				}
			}
			if haomadArgv == nil || haomaArgv == nil {
				t.Fatalf("could not tell haomad's argv from haoma's among %v", files)
			}
			haveHaomad := strings.Contains(strings.Join(haomadArgv, "\x00"), "--call-stats")
			haveHaoma := strings.Contains(strings.Join(haomaArgv, "\x00"), "--call-stats")
			if tc.callStats {
				if !haveHaomad {
					t.Fatalf("callStats=true: no --call-stats in haomad argv %q", haomadArgv)
				}
				if !haveHaoma {
					t.Fatalf("callStats=true: no --call-stats in haoma argv %q", haomaArgv)
				}
			} else {
				if haveHaomad {
					t.Fatalf("callStats=false: --call-stats present in haomad argv %q", haomadArgv)
				}
				if haveHaoma {
					t.Fatalf("callStats=false: --call-stats present in haoma argv %q", haomaArgv)
				}
			}
		})
	}
}

func requireMarker(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("read %s: %v", path, err)
		return
	}
	if !strings.Contains(string(data), mockDaemonStderrMarker) {
		t.Errorf("%s does not contain stderr marker; got %q", path, data)
	}
}

func requireAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		data, _ := os.ReadFile(path)
		t.Errorf("%s unexpectedly exists (stderr capture misdirected here?); contents: %q", path, data)
	}
}

func TestSpawnAndRun_PassesOwnTorToHaomad(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("chmod root: %v", err)
	}
	argvDir := t.TempDir()
	vc := newTestController(t)
	if err := vc.SetTorPassword("vault-tor-pw"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAOMA_TEXT_TEST_MODE", "mock-daemon")
	t.Setenv("HAOMA_TEXT_TEST_ARGV_DIR", argvDir)

	opts := spawnOpts{
		haomadBin: os.Args[0],
		haomaBin:  os.Args[0],
		logLevel:  "warn",
		tor:       torSetup{bin: "/usr/bin/tor"}.withDir(root),
	}
	done := make(chan error, 1)
	go func() { done <- spawnAndRun(root, vc, opts) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "ipc client") {
			t.Fatalf("spawnAndRun err = %v, want the ipc-client failure after both spawns", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("spawnAndRun did not return within 20s")
	}

	files, _ := filepath.Glob(filepath.Join(argvDir, "argv-*"))
	var haomadArgv []string
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		argv := strings.Split(string(raw), "\x00")

		for _, a := range argv {
			if a == "--runtime-file" {
				haomadArgv = argv
			}
		}
	}
	if haomadArgv == nil {
		t.Fatalf("no haomad command line among %d written", len(files))
	}

	ins, _ := filepath.Glob(filepath.Join(argvDir, "stdin-*"))
	if len(ins) != 2 {
		t.Fatalf("%d secrets bundles written, want 2", len(ins))
	}
	for _, f := range ins {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "haomad_token") {
			t.Fatalf("%s holds no secrets bundle: %q", f, raw)
		}
		if strings.Contains(string(raw), "vault-tor-pw") {
			t.Fatalf("the vault's tor password reached a daemon of an own-tor run: %s", f)
		}
	}
	got := torSetupFromArgs(haomadArgv)
	want := torSetup{bin: "/usr/bin/tor", dir: filepath.Join(root, torDirName)}
	if got != want {
		t.Fatalf("haomad started with %+v, want %+v; argv %q", got, want, haomadArgv)
	}
}
