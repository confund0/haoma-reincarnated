package streamers_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"haoma-frontend/internal/streamers"
)

const stubSource = `package main

import (
	"bufio"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	crashAfterKey := flag.Bool("crash-after-key", false, "")
	noReady := flag.Bool("no-ready", false, "")
	ignoreExit := flag.Bool("ignore-exit", false, "")
	emitError := flag.Bool("emit-error", false, "")
	keyFile := flag.String("keyfile", "", "")
	port := flag.Int("port", 0, "")
	streamID := flag.String("stream-id", "", "")
	trace := flag.Bool("trace", false, "")
	flag.Parse()

	signal.Ignore(syscall.SIGPIPE)
	fmt.Fprintf(os.Stderr, "stub: port=%d stream-id=%s trace=%v\n", *port, *streamID, *trace)

	key := make([]byte, 32)
	if _, err := io.ReadFull(os.Stdin, key); err != nil {
		fmt.Fprintf(os.Stderr, "stub: read key: %v\n", err)
		os.Exit(2)
	}
	if *keyFile != "" {
		_ = os.WriteFile(*keyFile, []byte(hex.EncodeToString(key)), 0o600)
	}

	if *crashAfterKey {
		os.Exit(1)
	}

	if !*noReady && !*emitError {
		fmt.Println("{\"type\":\"ready\"}")
		os.Stdout.Sync()
	}
	if *emitError {
		fmt.Println("{\"type\":\"error\",\"reason\":\"boom\"}")
		os.Stdout.Sync()
		// Sleep until killed.
		for {
			time.Sleep(time.Second)
		}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		<-sigCh
		if !*ignoreExit {
			os.Exit(0)
		}
		// In ignore-exit mode, swallow SIGTERM. SIGKILL still wins.
		fmt.Fprintln(os.Stderr, "stub: SIGTERM ignored")
	}()

	br := bufio.NewReader(os.Stdin)
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			line = strings.TrimRight(line, "\r\n")
			fmt.Fprintf(os.Stderr, "stub: cmd %s\n", line)
			if !*ignoreExit && strings.Contains(line, "\"exit\"") {
				return
			}
		}
		if err != nil {
			return
		}
	}
}
`

func buildStub(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "stub.go")
	if err := os.WriteFile(src, []byte(stubSource), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "stub")
	cmd := exec.Command("go", "build", "-o", bin, src)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build stub: %v\n%s", err, out)
	}
	return bin
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestSpawn_KeyHandoff_AndReady(t *testing.T) {
	stub := buildStub(t)
	keyFile := filepath.Join(t.TempDir(), "k.hex")

	mgr, err := streamers.New(streamers.Config{
		Logger:  discardLogger(),
		MicPath: stub,
		SpkPath: stub,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Shutdown)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	wantKey := bytes.Repeat([]byte{0xA5}, 32)
	stream, err := mgr.SpawnMic(ctx, "callA", 12345, wantKey, "mic")
	if err != nil {
		t.Fatal(err)
	}

	if err := stream.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}

	_ = keyFile
	if err := mgr.Teardown("callA"); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
}

func TestSpawn_KeyfileDump(t *testing.T) {
	stub := buildStub(t)
	keyFile := filepath.Join(t.TempDir(), "k.hex")
	wantKey := bytes.Repeat([]byte{0xC3}, 32)

	cmd := exec.Command(stub, "--port", "1", "--stream-id", "mic", "--keyfile", keyFile)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := stdin.Write(wantKey); err != nil {
		t.Fatal(err)
	}

	br := make([]byte, 256)
	_, _ = stdout.Read(br)
	if _, err := stdin.Write([]byte("{\"cmd\":\"exit\"}\n")); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	got, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != hex.EncodeToString(wantKey) {
		t.Errorf("key on stub stdin = %s, want %s", got, hex.EncodeToString(wantKey))
	}
}

func TestTeardown_GracefulExit(t *testing.T) {
	stub := buildStub(t)
	mgr, err := streamers.New(streamers.Config{
		Logger:  discardLogger(),
		MicPath: stub,
		SpkPath: stub,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Shutdown)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := mgr.SpawnMic(ctx, "callA", 11111, bytes.Repeat([]byte{1}, 32), "mic"); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.SpawnSpk(ctx, "callA", 11112, bytes.Repeat([]byte{2}, 32), "mic"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := mgr.Teardown("callA"); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed > 1500*time.Millisecond {
		t.Errorf("graceful teardown took %v (want <1.5s)", elapsed)
	}
	if got := mgr.Sessions(); len(got) != 0 {
		t.Errorf("sessions after Teardown: %v", got)
	}
}

func TestTeardown_EscalatesPastIgnoreExit(t *testing.T) {
	stub := buildStub(t)
	mgr, err := streamers.New(streamers.Config{
		Logger:  discardLogger(),
		MicPath: stub + "-mic",
		SpkPath: stub,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Shutdown)

	wrapper := filepath.Join(t.TempDir(), "wrap.sh")
	wrapperSrc := fmt.Sprintf("#!/bin/sh\nexec %s --ignore-exit \"$@\"\n", stub)
	if err := os.WriteFile(wrapper, []byte(wrapperSrc), 0o755); err != nil {
		t.Fatal(err)
	}
	mgr2, err := streamers.New(streamers.Config{
		Logger:  discardLogger(),
		MicPath: wrapper,
		SpkPath: wrapper,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr2.Shutdown)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := mgr2.SpawnMic(ctx, "callA", 22222, bytes.Repeat([]byte{1}, 32), "mic"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := mgr2.Teardown("callA"); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed < 900*time.Millisecond {
		t.Errorf("teardown elapsed %v — too fast, didn't escalate", elapsed)
	}
	if elapsed > 6*time.Second {
		t.Errorf("teardown elapsed %v — kill ladder ran past budget", elapsed)
	}
}

func TestSpawn_ErrorEvent_FailsWaitReady(t *testing.T) {
	stub := buildStub(t)
	wrapper := filepath.Join(t.TempDir(), "err.sh")
	if err := os.WriteFile(wrapper, []byte(fmt.Sprintf("#!/bin/sh\nexec %s --emit-error \"$@\"\n", stub)), 0o755); err != nil {
		t.Fatal(err)
	}
	mgr, err := streamers.New(streamers.Config{
		Logger:  discardLogger(),
		MicPath: wrapper,
		SpkPath: wrapper,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Shutdown)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream, err := mgr.SpawnMic(ctx, "callE", 22223, bytes.Repeat([]byte{1}, 32), "mic")
	if err != nil {
		t.Fatal(err)
	}
	if rerr := stream.WaitReady(ctx); rerr == nil {
		t.Errorf("WaitReady returned nil after streamer emitted error")
	}
	_ = mgr.Teardown("callE")
}

func TestDiscover_FromDir(t *testing.T) {
	dir := t.TempDir()
	mic := filepath.Join(dir, "haoma-mic")
	spk := filepath.Join(dir, "haoma-spk")
	for _, p := range []string{mic, spk} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gotMic, gotSpk, err := streamers.Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if gotMic != mic || gotSpk != spk {
		t.Errorf("Discover = (%q, %q), want (%q, %q)", gotMic, gotSpk, mic, spk)
	}
}

func TestDiscover_FailsWhenAbsent(t *testing.T) {
	t.Setenv("HAOMA_STREAMER_DIR", t.TempDir())
	t.Setenv("PATH", "/nowhere")
	if _, _, err := streamers.Discover(""); err == nil {
		t.Errorf("Discover succeeded on empty env")
	}
}

func TestDeriveStreamKey_DeterministicAndDistinctPerStreamID(t *testing.T) {
	master := bytes.Repeat([]byte{0x42}, 32)
	a, err := streamers.DeriveStreamKey(master, "mic")
	if err != nil {
		t.Fatal(err)
	}
	b, err := streamers.DeriveStreamKey(master, "mic")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Errorf("HKDF not deterministic")
	}
	c, err := streamers.DeriveStreamKey(master, "cam")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, c) {
		t.Errorf("info=mic and info=cam produced the same key")
	}
	if len(a) != 32 {
		t.Errorf("derived key length = %d, want 32", len(a))
	}
}

func TestDeriveStreamKey_RejectsBadInputs(t *testing.T) {
	if _, err := streamers.DeriveStreamKey(nil, "mic"); err == nil {
		t.Errorf("nil key accepted")
	}
	if _, err := streamers.DeriveStreamKey(bytes.Repeat([]byte{0}, 32), ""); err == nil {
		t.Errorf("empty stream-id accepted")
	}
}
