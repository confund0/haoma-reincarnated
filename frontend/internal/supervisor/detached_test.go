package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func healthOK(context.Context, string) error { return nil }

func healthFail(want error) HealthCheckFunc {
	return func(context.Context, string) error { return want }
}

func writeRuntimeFileForTest(t *testing.T, path string, info RuntimeInfo) {
	t.Helper()
	body := fmt.Sprintf(`{"pid":%d,"api_addr":%q,"started_at":%q}`,
		info.PID, info.APIAddr, info.StartedAt.UTC().Format(time.RFC3339Nano))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write runtime file: %v", err)
	}
}

func TestReadRuntimeFile_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "haomad.runtime.json")
	want := RuntimeInfo{PID: 1234, APIAddr: "127.0.0.1:7891", StartedAt: time.Now().UTC().Truncate(time.Second)}
	writeRuntimeFileForTest(t, path, want)

	got, err := ReadRuntimeFile(path)
	if err != nil {
		t.Fatalf("ReadRuntimeFile: %v", err)
	}
	if got.PID != want.PID || got.APIAddr != want.APIAddr {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestReadRuntimeFile_Validation(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"empty", "", "empty"},
		{"garbage", "not json", "parse"},
		{"missing pid", `{"api_addr":"x"}`, "invalid PID"},
		{"zero pid", `{"pid":0,"api_addr":"x"}`, "invalid PID"},
		{"empty addr", `{"pid":1,"api_addr":""}`, "api_addr empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name+".json")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := ReadRuntimeFile(path)
			if err == nil {
				t.Fatal("want error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestReadRuntimeFile_NotExist(t *testing.T) {
	_, err := ReadRuntimeFile(filepath.Join(t.TempDir(), "nope.json"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want os.ErrNotExist", err)
	}
}

func TestIsProcessAlive_Self(t *testing.T) {
	if !isProcessAlive(os.Getpid()) {
		t.Error("isProcessAlive(self) returned false")
	}
}

func TestIsProcessAlive_BadPID(t *testing.T) {
	if isProcessAlive(0) {
		t.Error("isProcessAlive(0) returned true")
	}
	if isProcessAlive(-1) {
		t.Error("isProcessAlive(-1) returned true")
	}
}

func TestIsProcessAlive_DeadProcess(t *testing.T) {

	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "HAOMA_SUPERVISOR_TEST_MODE=exit-immediately")
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {

		t.Logf("wait: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for isProcessAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if isProcessAlive(pid) {
		t.Errorf("isProcessAlive(reaped pid %d) returned true", pid)
	}
}

func TestAttachOrSpawn_AttachToLiveTokenAccepted(t *testing.T) {
	dir := t.TempDir()
	runtimePath := filepath.Join(dir, "haomad.runtime.json")
	logPath := filepath.Join(dir, "child.log")

	writeRuntimeFileForTest(t, runtimePath, RuntimeInfo{
		PID:       os.Getpid(),
		APIAddr:   "127.0.0.1:9999",
		StartedAt: time.Now().UTC(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	d, err := AttachOrSpawn(ctx, "haomad", "/nonexistent/binary", nil, nil,
		runtimePath, logPath, healthOK)
	if err != nil {
		t.Fatalf("AttachOrSpawn: %v", err)
	}
	if d.PID != os.Getpid() {
		t.Errorf("PID = %d, want %d (test self)", d.PID, os.Getpid())
	}
	if d.APIAddr != "127.0.0.1:9999" {
		t.Errorf("APIAddr = %q, want 127.0.0.1:9999", d.APIAddr)
	}
}

func TestAttachOrSpawn_AttachWithFailingHealthCheckErrors(t *testing.T) {
	dir := t.TempDir()
	runtimePath := filepath.Join(dir, "haomad.runtime.json")
	logPath := filepath.Join(dir, "child.log")

	writeRuntimeFileForTest(t, runtimePath, RuntimeInfo{
		PID:       os.Getpid(),
		APIAddr:   "127.0.0.1:9999",
		StartedAt: time.Now().UTC(),
	})

	wantErr := errors.New("token mismatch")
	_, err := AttachOrSpawn(context.Background(), "haomad", "/nonexistent/binary", nil, nil,
		runtimePath, logPath, healthFail(wantErr))
	if err == nil {
		t.Fatal("want error from failing health check, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want chain to include %v", err, wantErr)
	}
	if !strings.Contains(err.Error(), "health check failed") {
		t.Errorf("err = %v, want descriptive message", err)
	}
}

func TestAttachOrSpawn_StaleRuntimeFile_FallsThroughToSpawn(t *testing.T) {
	dir := t.TempDir()
	runtimePath := filepath.Join(dir, "haomad.runtime.json")
	logPath := filepath.Join(dir, "child.log")

	writeRuntimeFileForTest(t, runtimePath, RuntimeInfo{
		PID:     999999,
		APIAddr: "127.0.0.1:9000",
	})

	t.Setenv("HAOMA_SUPERVISOR_TEST_MODE", "ready-then-block")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	d, err := AttachOrSpawn(ctx, "mock", os.Args[0],
		[]string{"-test.run=^$"}, nil, runtimePath, logPath, healthOK)
	if err != nil {
		t.Fatalf("AttachOrSpawn: %v", err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer stopCancel()
		_ = d.Stop(stopCtx)
	}()

	if d.PID == 999999 {
		t.Error("attached to dead PID instead of spawning")
	}
	if d.APIAddr != "127.0.0.1:9991" {
		t.Errorf("APIAddr = %q, want 127.0.0.1:9991 (from spawn ready-line)", d.APIAddr)
	}
	if !isProcessAlive(d.PID) {
		t.Errorf("spawned PID %d not alive", d.PID)
	}

	if _, err := os.Stat(runtimePath); !os.IsNotExist(err) {
		t.Errorf("stale runtime file not cleaned up: %v", err)
	}
}

func TestAttachOrSpawn_NoRuntimeFile_Spawns(t *testing.T) {
	dir := t.TempDir()
	runtimePath := filepath.Join(dir, "haomad.runtime.json")
	logPath := filepath.Join(dir, "child.log")

	t.Setenv("HAOMA_SUPERVISOR_TEST_MODE", "ready-then-block")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	d, err := AttachOrSpawn(ctx, "mock", os.Args[0],
		[]string{"-test.run=^$"}, nil, runtimePath, logPath, healthOK)
	if err != nil {
		t.Fatalf("AttachOrSpawn: %v", err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer stopCancel()
		_ = d.Stop(stopCtx)
	}()
	if !isProcessAlive(d.PID) {
		t.Errorf("spawned PID %d not alive", d.PID)
	}
}

func TestAttachOrSpawn_MalformedRuntimeFile_Errors(t *testing.T) {
	dir := t.TempDir()
	runtimePath := filepath.Join(dir, "haomad.runtime.json")
	logPath := filepath.Join(dir, "child.log")

	if err := os.WriteFile(runtimePath, []byte("not json at all"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := AttachOrSpawn(context.Background(), "mock", os.Args[0],
		[]string{"-test.run=^$"}, nil, runtimePath, logPath, healthOK)
	if err == nil {
		t.Fatal("want error on malformed runtime file, got nil")
	}
	if !strings.Contains(err.Error(), "runtime file") {
		t.Errorf("err = %v, want runtime-file context", err)
	}
}

func TestAttachOrSpawn_SpawnThenAttach_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	runtimePath := filepath.Join(dir, "haomad.runtime.json")
	logPath := filepath.Join(dir, "child.log")

	t.Setenv("HAOMA_SUPERVISOR_TEST_MODE", "ready-write-runtime")
	t.Setenv("HAOMA_TEST_RUNTIME_PATH", runtimePath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	first, err := AttachOrSpawn(ctx, "mock", os.Args[0],
		[]string{"-test.run=^$"}, nil, runtimePath, logPath, healthOK)
	if err != nil {
		t.Fatalf("first AttachOrSpawn (spawn): %v", err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer stopCancel()
		_ = first.Stop(stopCtx)
	}()

	if _, err := os.Stat(runtimePath); err != nil {
		t.Fatalf("runtime file not written by child: %v", err)
	}

	second, err := AttachOrSpawn(ctx, "mock", os.Args[0],
		[]string{"-test.run=^$"}, nil, runtimePath, logPath, healthOK)
	if err != nil {
		t.Fatalf("second AttachOrSpawn (attach): %v", err)
	}
	if second.PID != first.PID {
		t.Errorf("second.PID = %d, want %d (attach should reuse)", second.PID, first.PID)
	}
	if second.APIAddr != first.APIAddr {
		t.Errorf("second.APIAddr = %q, want %q", second.APIAddr, first.APIAddr)
	}
}

func TestDetached_Stop_Graceful(t *testing.T) {
	dir := t.TempDir()
	runtimePath := filepath.Join(dir, "haomad.runtime.json")
	logPath := filepath.Join(dir, "child.log")

	t.Setenv("HAOMA_SUPERVISOR_TEST_MODE", "ready-then-block")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	d, err := AttachOrSpawn(ctx, "mock", os.Args[0],
		[]string{"-test.run=^$"}, nil, runtimePath, logPath, healthOK)
	if err != nil {
		t.Fatalf("AttachOrSpawn: %v", err)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	start := time.Now()
	if err := d.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > gracePeriod {
		t.Errorf("Stop took %v, want < %v (graceful path should not escalate)", elapsed, gracePeriod)
	}

	if isProcessAlive(d.PID) {
		t.Errorf("PID %d still alive after Stop", d.PID)
	}
}

func TestDetached_Stop_Idempotent(t *testing.T) {
	d := &Detached{Name: "phantom", PID: 999999, APIAddr: "x", runtimePath: filepath.Join(t.TempDir(), "n.json")}

	if err := d.Stop(context.Background()); err != nil {
		t.Errorf("Stop on dead PID: %v", err)
	}

	if err := d.Stop(context.Background()); err != nil {
		t.Errorf("Stop second call: %v", err)
	}
}

func TestDetached_Stop_KillFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows terminate is unconditional Kill; no SIGTERM-ignoring fallback to assert")
	}

	prev := gracePeriod
	gracePeriod = 200 * time.Millisecond
	defer func() { gracePeriod = prev }()

	dir := t.TempDir()
	runtimePath := filepath.Join(dir, "haomad.runtime.json")
	logPath := filepath.Join(dir, "child.log")

	t.Setenv("HAOMA_SUPERVISOR_TEST_MODE", "ignore-sigterm")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	d, err := AttachOrSpawn(ctx, "mock", os.Args[0],
		[]string{"-test.run=^$"}, nil, runtimePath, logPath, healthOK)
	if err != nil {
		t.Fatalf("AttachOrSpawn: %v", err)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	if err := d.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if isProcessAlive(d.PID) {
		t.Errorf("PID %d still alive after Stop with kill fallback", d.PID)
	}
}

func TestAttachOrSpawn_SpawnReadyLineGarbage_KillsChild(t *testing.T) {
	dir := t.TempDir()
	runtimePath := filepath.Join(dir, "haomad.runtime.json")
	logPath := filepath.Join(dir, "child.log")

	t.Setenv("HAOMA_SUPERVISOR_TEST_MODE", "garbage")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := AttachOrSpawn(ctx, "mock", os.Args[0],
		[]string{"-test.run=^$"}, nil, runtimePath, logPath, healthOK)
	if err == nil {
		t.Fatal("want ready-line parse error, got nil")
	}
	if !strings.Contains(err.Error(), "ready-line") {
		t.Errorf("err = %v, want ready-line context", err)
	}
}
