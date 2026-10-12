package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func init() { fakeTorMain = fakeTor }

func fakeTor() {
	mode := os.Getenv("HAOMA_TEXT_FAKE_TOR")
	torrc := os.Args[2]
	if mode != "no-lock" {
		f, err := os.OpenFile(filepath.Join(filepath.Dir(torrc), "lock"), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			os.Exit(3)
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			os.Exit(4)
		}
		switch mode {
		case "ignore":
			signal.Ignore(syscall.SIGTERM)
		case "drop-lock":
			ch := make(chan os.Signal, 1)
			signal.Notify(ch, syscall.SIGTERM)
			go func() {
				<-ch
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			}()
		}
	}
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

func torDir(t *testing.T) (torrc string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "tor")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "torrc")
}

func startProc(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
}

func startFakeTor(t *testing.T, torrc, mode string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-f", torrc, "--quiet")
	cmd.Env = append(os.Environ(), "HAOMA_TEXT_TEST_MODE=fake-tor", "HAOMA_TEXT_FAKE_TOR="+mode)
	startProc(t, cmd)
	return cmd
}

func awaitLock(t *testing.T, torrc string, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		id, err := fileIDOf(filepath.Join(filepath.Dir(torrc), "lock"))
		if err == nil {
			locks, _ := os.ReadFile("/proc/locks")
			if lockHolders(locks, id)[pid] {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("pid %d does not hold the lock of %s", pid, torrc)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func alive(pid int) bool { _, ok := liveProc(pid); return ok }

func noRuntime(t *testing.T) string { return filepath.Join(t.TempDir(), "haomad.runtime.json") }

func TestSweepEndsALeftoverTor(t *testing.T) {
	torrc := torDir(t)
	tor := startFakeTor(t, torrc, "")
	awaitLock(t, torrc, tor.Process.Pid)

	res := endLeftoverTors(noRuntime(t), torrc, 2*time.Second)
	if len(res.ended) != 1 || res.ended[0].pid != tor.Process.Pid {
		t.Fatalf("ended %v, want pid %d; result %+v", res.ended, tor.Process.Pid, res)
	}
	if alive(tor.Process.Pid) {
		t.Fatal("the leftover tor still runs")
	}
}

func TestSweepLeavesAProcessThatOnlyNamesTheTorrc(t *testing.T) {
	torrc := torDir(t)
	tail := exec.Command("/bin/sh", "-c", "sleep 30; :", "-f", torrc)
	startProc(t, tail)

	tor := startFakeTor(t, torrc, "")
	awaitLock(t, torrc, tor.Process.Pid)

	res := endLeftoverTors(noRuntime(t), torrc, time.Second)
	for _, p := range res.ended {
		if p.pid == tail.Process.Pid {
			t.Fatalf("a process that only names the torrc was ended: %+v", res)
		}
	}
	if !alive(tail.Process.Pid) {
		t.Fatal("the process that only names the torrc was ended")
	}
}

func TestSweepLeavesAProcessWithoutTheLock(t *testing.T) {
	torrc := torDir(t)

	if err := os.WriteFile(filepath.Join(filepath.Dir(torrc), "lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tor := startFakeTor(t, torrc, "no-lock")
	time.Sleep(200 * time.Millisecond)

	res := endLeftoverTors(noRuntime(t), torrc, time.Second)
	if len(res.ended) != 0 || !alive(tor.Process.Pid) {
		t.Fatalf("a process without the lock was ended: %+v", res)
	}
}

func TestSweepLeavesATorBehindAWrapper(t *testing.T) {
	torrc := torDir(t)

	outer := exec.Command("/bin/sh", "-c",
		`/bin/sh -c '"$0" -f "$1" --quiet; :' "$0" "$1"; :`,
		os.Args[0], torrc, "--manage-tor", "/usr/local/bin/tor-wrapper")
	outer.Env = append(os.Environ(), "HAOMA_TEXT_TEST_MODE=fake-tor", "HAOMA_TEXT_FAKE_TOR=")
	startProc(t, outer)

	var torPid int
	deadline := time.Now().Add(3 * time.Second)
	for torPid == 0 {
		for _, p := range (torSweep{torrc: torrc, lock: filepath.Join(filepath.Dir(torrc), "lock"), uid: os.Getuid()}).allOnTorrc() {
			torPid = p
		}
		if time.Now().After(deadline) {
			t.Fatal("the stand-in tor did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	awaitLock(t, torrc, torPid)

	res := endLeftoverTors(noRuntime(t), torrc, time.Second)
	if len(res.ended) != 0 || !alive(torPid) {
		t.Fatalf("a running haomad's tor behind a wrapper was ended: %+v", res)
	}
}

func TestSweepSkipsWhileTheRuntimeFileNamesARunningHaomad(t *testing.T) {
	torrc := torDir(t)
	tor := startFakeTor(t, torrc, "")
	awaitLock(t, torrc, tor.Process.Pid)
	runtimePath := filepath.Join(t.TempDir(), "haomad.runtime.json")
	body := fmt.Sprintf(`{"pid":%d,"api_addr":"127.0.0.1:1","started_at":"2026-01-01T00:00:00Z"}`, os.Getpid())
	if err := os.WriteFile(runtimePath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	res := endLeftoverTors(runtimePath, torrc, time.Second)
	if res.skipped == "" || len(res.ended) != 0 || !alive(tor.Process.Pid) {
		t.Fatalf("swept while haomad runs: %+v", res)
	}
}

func TestSweepSkipsARefusedSignal(t *testing.T) {
	torrc := torDir(t)
	tor := startFakeTor(t, torrc, "")
	awaitLock(t, torrc, tor.Process.Pid)
	killProcess = func(int, syscall.Signal) error { return syscall.EPERM }
	t.Cleanup(func() { killProcess = syscall.Kill })

	done := make(chan sweepResult, 1)
	go func() { done <- endLeftoverTors(noRuntime(t), torrc, time.Second) }()
	select {
	case res := <-done:
		if len(res.refused) != 1 || res.refused[0].pid != tor.Process.Pid || len(res.ended) != 0 {
			t.Fatalf("result %+v, want the pid refused and nothing ended", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the sweep did not return")
	}
}

func TestSweepChecksAgainBeforeSIGKILL(t *testing.T) {
	for _, tc := range []struct {
		mode      string
		wantAlive bool
	}{
		{"drop-lock", true},
		{"ignore", false},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			torrc := torDir(t)
			tor := startFakeTor(t, torrc, tc.mode)
			awaitLock(t, torrc, tor.Process.Pid)

			res := endLeftoverTors(noRuntime(t), torrc, 300*time.Millisecond)
			if got := alive(tor.Process.Pid); got != tc.wantAlive {
				t.Fatalf("alive after the sweep = %v, want %v; result %+v", got, tc.wantAlive, res)
			}
			if tc.wantAlive && (len(res.changed) != 1 || len(res.ended) != 0) {
				t.Fatalf("result %+v, want the pid named as changed", res)
			}
		})
	}
}

func TestUidsAre(t *testing.T) {
	status := []byte("Name:\ttor\nUid:\t1001\t1001\t1001\t1001\nGid:\t1001\t1001\t1001\t1001\n")
	if !uidsAre(status, 1001) {
		t.Fatal("same user not matched")
	}
	if uidsAre(status, 1002) {
		t.Fatal("another user matched")
	}
	if uidsAre([]byte("Uid:\t1001\t0\t0\t0\n"), 1001) {
		t.Fatal("a process running as root matched on its real uid")
	}
}

func TestIsTorArgv(t *testing.T) {
	torrc := "/r/tor/torrc"
	for _, tc := range []struct {
		argv []string
		want bool
	}{
		{[]string{"/usr/bin/tor", "-f", torrc, "--quiet"}, true},
		{[]string{"tail", "-f", torrc}, false},
		{[]string{"/usr/bin/tor", "-f", torrc}, false},
		{[]string{"/usr/bin/tor", "-f", "/other/torrc", "--quiet"}, false},
		{[]string{"/usr/bin/tor", "-f", torrc, "--quiet", "x"}, false},
	} {
		if got := isTorArgv(tc.argv, torrc); got != tc.want {
			t.Fatalf("isTorArgv(%q) = %v", tc.argv, got)
		}
	}
}

func TestLockHolders(t *testing.T) {
	locks := []byte("1: FLOCK  ADVISORY  WRITE 1234 08:02:5678 0 EOF\n" +
		"1: -> FLOCK  ADVISORY  WRITE 999 08:02:5678 0 EOF\n" +
		"2: POSIX  ADVISORY  WRITE 42 fd:01:5678 0 EOF\n")
	got := lockHolders(locks, fileID{major: 8, minor: 2, ino: 5678})
	if !got[1234] || got[999] || got[42] || len(got) != 1 {
		t.Fatalf("holders = %v, want only 1234", got)
	}
}

func TestParseProcStat(t *testing.T) {
	comm, state, ppid, ok := parseProcStat([]byte("123 (tor (x) y) S 1 123 123 0 -1"))
	if !ok || comm != "tor (x) y" || state != 'S' || ppid != 1 {
		t.Fatalf("got %q %q %d %v", comm, state, ppid, ok)
	}
	if _, _, _, ok := parseProcStat([]byte("garbage")); ok {
		t.Fatal("garbage parsed")
	}
}

func (s torSweep) allOnTorrc() []int {
	entries, _ := os.ReadDir("/proc")
	var out []int
	for _, e := range entries {
		var pid int
		if _, err := fmt.Sscanf(e.Name(), "%d", &pid); err != nil {
			continue
		}
		if argv, ok := processArgv(pid); ok && isTorArgv(argv, s.torrc) && alive(pid) {
			out = append(out, pid)
		}
	}
	return out
}
