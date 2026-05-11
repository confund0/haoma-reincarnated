package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	switch os.Getenv("HAOMA_SUPERVISOR_TEST_MODE") {
	case "":
		os.Exit(m.Run())
	case "ready-then-block":
		emitReady("127.0.0.1:9991")
		blockForever()
	case "exit-immediately":

		os.Exit(0)
	case "exit-after-stderr":

		fmt.Fprintln(os.Stderr, "synthetic-stderr-from-mock")
		os.Exit(0)
	case "garbage":

		fmt.Fprintln(os.Stdout, "not a ready line, sorry")
		blockForever()
	case "ignore-sigterm":

		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGTERM)
		go func() {
			for range c {

			}
		}()
		emitReady("127.0.0.1:9992")
		blockForever()
	case "ready-write-runtime":

		path := os.Getenv("HAOMA_TEST_RUNTIME_PATH")
		if path == "" {
			fmt.Fprintln(os.Stderr, "ready-write-runtime: HAOMA_TEST_RUNTIME_PATH unset")
			os.Exit(2)
		}
		writeMockRuntimeFile(path, "127.0.0.1:9993")
		emitReady("127.0.0.1:9993")
		blockForever()
	default:
		fmt.Fprintf(os.Stderr, "unknown HAOMA_SUPERVISOR_TEST_MODE=%q\n", os.Getenv("HAOMA_SUPERVISOR_TEST_MODE"))
		os.Exit(2)
	}
}

func emitReady(addr string) {
	fmt.Fprintf(os.Stdout, `{"status":"ready","api_addr":%q}`+"\n", addr)
}

func blockForever() {
	select {}
}

func writeMockRuntimeFile(path, addr string) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	body := fmt.Sprintf(`{"pid":%d,"api_addr":%q,"started_at":%q}`, os.Getpid(), addr, now)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "writeMockRuntimeFile: %v\n", err)
		os.Exit(2)
	}
}

func spawnMockMode(t *testing.T, mode string) (*Child, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "child.log")

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "HAOMA_SUPERVISOR_TEST_MODE="+mode)
	cmd.Stderr = logFile

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = logFile.Close()
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		t.Fatalf("start: %v", err)
	}

	c := &Child{
		Name:      "mock-" + mode,
		cmd:       cmd,
		stderrLog: logFile,
		readyCh:   make(chan readyResult, 1),
		waitCh:    make(chan struct{}),
	}
	go c.readStdout(stdout)
	go c.reap()

	t.Cleanup(func() {

		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		select {
		case <-c.waitCh:
		case <-time.After(2 * time.Second):
		}
	})

	return c, logPath
}

func TestSpawn_ReadyLine_HappyPath(t *testing.T) {
	c, _ := spawnMockMode(t, "ready-then-block")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	addr, err := c.WaitReady(ctx)
	if err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	if addr != "127.0.0.1:9991" {
		t.Errorf("api_addr = %q, want %q", addr, "127.0.0.1:9991")
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	if err := c.Stop(stopCtx); err != nil {

		t.Logf("Stop returned %v (expected for signal-killed mock)", err)
	}
}

func TestSpawn_ExitBeforeReady(t *testing.T) {
	c, _ := spawnMockMode(t, "exit-immediately")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.WaitReady(ctx)
	if err == nil {
		t.Fatal("WaitReady: want error, got nil")
	}
	if !strings.Contains(err.Error(), "exited before ready") {
		t.Errorf("WaitReady err = %v, want 'exited before ready'", err)
	}
}

func TestSpawn_StderrCaptured(t *testing.T) {
	c, logPath := spawnMockMode(t, "exit-after-stderr")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := c.WaitReady(ctx); err == nil {
		t.Fatal("WaitReady: want exit-before-ready error, got nil")
	}

	select {
	case <-c.waitCh:
	case <-time.After(2 * time.Second):
		t.Fatal("waitCh never closed")
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), "synthetic-stderr-from-mock") {
		t.Errorf("stderr log %q does not contain mock marker; got %q", logPath, data)
	}
}

func TestSpawn_GarbageFirstLine(t *testing.T) {
	c, _ := spawnMockMode(t, "garbage")
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		_ = c.Stop(stopCtx)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.WaitReady(ctx)
	if err == nil {
		t.Fatal("WaitReady: want parse error, got nil")
	}
	if !strings.Contains(err.Error(), "expected ready-line") {
		t.Errorf("WaitReady err = %v, want parse error", err)
	}
}

func TestStop_Graceful(t *testing.T) {
	c, _ := spawnMockMode(t, "ready-then-block")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	start := time.Now()
	_ = c.Stop(stopCtx)
	elapsed := time.Since(start)
	if elapsed > gracePeriod {
		t.Errorf("Stop took %v, want < %v (graceful path should not escalate)", elapsed, gracePeriod)
	}
}

func TestStop_KillFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows terminate is unconditional Kill; no fallback to assert")
	}

	prev := gracePeriod
	gracePeriod = 200 * time.Millisecond
	defer func() { gracePeriod = prev }()

	c, _ := spawnMockMode(t, "ignore-sigterm")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	err := c.Stop(stopCtx)
	if err == nil {
		t.Fatal("Stop: want SIGKILL exit error, got nil")
	}

	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("Stop err = %v; want *exec.ExitError wrapping signal", err)
	}
	ws, ok := ee.ProcessState.Sys().(syscall.WaitStatus)
	if !ok {
		t.Fatalf("ProcessState.Sys() = %T; want syscall.WaitStatus", ee.ProcessState.Sys())
	}
	if !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Errorf("wait status = %+v; want signaled+SIGKILL", ws)
	}
}

func TestShutdown_Group(t *testing.T) {
	a, _ := spawnMockMode(t, "ready-then-block")
	b, _ := spawnMockMode(t, "ready-then-block")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := a.WaitReady(ctx); err != nil {
		t.Fatalf("a WaitReady: %v", err)
	}
	if _, err := b.WaitReady(ctx); err != nil {
		t.Fatalf("b WaitReady: %v", err)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()

	_ = Shutdown(stopCtx, a, b, nil)

	select {
	case <-a.waitCh:
	default:
		t.Error("a.waitCh not closed after Shutdown")
	}
	select {
	case <-b.waitCh:
	default:
		t.Error("b.waitCh not closed after Shutdown")
	}
}

func TestParseReadyLine(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"happy", `{"status":"ready","api_addr":"127.0.0.1:7891"}`, "127.0.0.1:7891", false},
		{"leading whitespace", `  {"status":"ready","api_addr":"127.0.0.1:1"}`, "127.0.0.1:1", false},
		{"empty", "", "", true},
		{"plain text", "ready", "", true},
		{"wrong status", `{"status":"hello","api_addr":"x"}`, "", true},
		{"empty addr", `{"status":"ready","api_addr":""}`, "", true},
		{"whitespace addr", `{"status":"ready","api_addr":"   "}`, "", true},
		{"bad json", `{"status":"ready"`, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseReadyLine([]byte(tc.in))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("addr = %q, want %q", got, tc.want)
			}
		})
	}
}
