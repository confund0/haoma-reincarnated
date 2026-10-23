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
	"regexp"
	"strconv"
	"strings"
	"sync"
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
	exit4OnTerm := flag.Bool("exit4-on-term", false, "")
	exit4AfterReady := flag.Bool("exit4-after-ready", false, "")
	keyFile := flag.String("keyfile", "", "")
	readyRawUnix := flag.String("ready-raw-unix", "", "")
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

	// Test-only hooks (same shape as STREAMERS_TEST_RAW_UNIX below): a
	// run-separated (\x1e) list of extra lines to print, so the L2
	// tests can drive arbitrary summary / event / stats shapes and
	// stderr lines through the real Spawn + readEvents / pumpStderr
	// path rather than reimplementing it.
	if lines := os.Getenv("STREAMERS_TEST_STDERR_LINES"); lines != "" {
		for _, l := range strings.Split(lines, "\x1e") {
			fmt.Fprintln(os.Stderr, l)
		}
	}

	if !*noReady && !*emitError {
		rawUnix := *readyRawUnix
		if env := os.Getenv("STREAMERS_TEST_RAW_UNIX"); env != "" {
			rawUnix = env
		}
		if rawUnix != "" {
			fmt.Printf("{\"type\":\"ready\",\"raw_unix\":\"%s\"}\n", rawUnix)
		} else {
			fmt.Println("{\"type\":\"ready\"}")
		}
		os.Stdout.Sync()
	}
	if lines := os.Getenv("STREAMERS_TEST_PRINT_LINES"); lines != "" {
		for _, l := range strings.Split(lines, "\x1e") {
			fmt.Println(l)
		}
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

	if *exit4AfterReady {
		os.Exit(4)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		<-sigCh
		if *exit4OnTerm {
			// What a real streamer does when a SIGTERM cuts its wait
			// for a connection short.
			os.Exit(4)
		}
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
			if !*ignoreExit && !*exit4OnTerm && strings.Contains(line, "\"exit\"") {
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

func TestSpawn_ReadyRawUnixPropagates(t *testing.T) {
	stub := buildStub(t)
	t.Setenv("STREAMERS_TEST_RAW_UNIX", "haoma-test-mic-9999")

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

	stream, err := mgr.SpawnMic(ctx, "callRP", 33333, bytes.Repeat([]byte{2}, 32), "cam")
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	if stream.RawUnix != "haoma-test-mic-9999" {
		t.Errorf("Stream.RawUnix = %q, want %q", stream.RawUnix, "haoma-test-mic-9999")
	}
	if err := mgr.Teardown("callRP"); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
}

func TestSpawn_NoRawUnix_StaysEmpty(t *testing.T) {
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

	stream, err := mgr.SpawnMic(ctx, "callNR", 33334, bytes.Repeat([]byte{3}, 32), "mic")
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	if stream.RawUnix != "" {
		t.Errorf("Stream.RawUnix = %q, want empty for audio path", stream.RawUnix)
	}
	if err := mgr.Teardown("callNR"); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
}

func TestSpawnCam_RegistersSessionAndRawUnix(t *testing.T) {
	stub := buildStub(t)
	t.Setenv("STREAMERS_TEST_RAW_UNIX", "haoma-cam-test-61001")

	mgr, err := streamers.New(streamers.Config{
		Logger:  discardLogger(),
		MicPath: stub,
		SpkPath: stub,
		CamPath: stub,
		VidPath: stub,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Shutdown)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream, err := mgr.SpawnCam(ctx, "callC", 41001, bytes.Repeat([]byte{4}, 32), "cam")
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	if stream.Side != streamers.SideCam {
		t.Errorf("Stream.Side = %q, want %q", stream.Side, streamers.SideCam)
	}
	if stream.RawUnix != "haoma-cam-test-61001" {
		t.Errorf("Stream.RawUnix = %q, want %q", stream.RawUnix, "haoma-cam-test-61001")
	}
	if got := mgr.Cam("callC"); got != stream {
		t.Errorf("mgr.Cam(callC) returned %v, want %v", got, stream)
	}
	if got := mgr.Mic("callC"); got != nil {
		t.Errorf("mgr.Mic(callC) should be nil, got %v", got)
	}
	if err := mgr.Teardown("callC"); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
}

func TestSpawnVid_RegistersSessionAndRawUnix(t *testing.T) {
	stub := buildStub(t)
	t.Setenv("STREAMERS_TEST_RAW_UNIX", "haoma-vid-test-61002")

	mgr, err := streamers.New(streamers.Config{
		Logger:  discardLogger(),
		MicPath: stub,
		SpkPath: stub,
		CamPath: stub,
		VidPath: stub,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Shutdown)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream, err := mgr.SpawnVid(ctx, "callD", 41002, bytes.Repeat([]byte{5}, 32), "cam")
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	if stream.Side != streamers.SideVid {
		t.Errorf("Stream.Side = %q, want %q", stream.Side, streamers.SideVid)
	}
	if stream.RawUnix != "haoma-vid-test-61002" {
		t.Errorf("Stream.RawUnix = %q, want %q", stream.RawUnix, "haoma-vid-test-61002")
	}
	if got := mgr.Vid("callD"); got != stream {
		t.Errorf("mgr.Vid(callD) returned %v, want %v", got, stream)
	}
	if err := mgr.Teardown("callD"); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
}

func TestSpawnCam_RequiresCamPath(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err = mgr.SpawnCam(ctx, "callX", 41003, bytes.Repeat([]byte{6}, 32), "cam")
	if err == nil {
		t.Fatal("SpawnCam without CamPath should fail")
	}
	if !strings.Contains(err.Error(), "CamPath not configured") {
		t.Errorf("SpawnCam err = %v, want substring %q", err, "CamPath not configured")
	}
}

func TestSpawnVid_RequiresVidPath(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err = mgr.SpawnVid(ctx, "callY", 41004, bytes.Repeat([]byte{7}, 32), "cam")
	if err == nil {
		t.Fatal("SpawnVid without VidPath should fail")
	}
	if !strings.Contains(err.Error(), "VidPath not configured") {
		t.Errorf("SpawnVid err = %v, want substring %q", err, "VidPath not configured")
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
		t.Errorf("teardown elapsed %v — kill ladder ran past the teardown timeout", elapsed)
	}
}

func TestTeardown_StopsCamAndVid(t *testing.T) {
	stub := buildStub(t)

	mgr, err := streamers.New(streamers.Config{
		Logger:  discardLogger(),
		MicPath: stub,
		SpkPath: stub,
		CamPath: stub,
		VidPath: stub,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Shutdown)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := mgr.SpawnMic(ctx, "callQ", 51001, bytes.Repeat([]byte{1}, 32), "mic"); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.SpawnSpk(ctx, "callQ", 51002, bytes.Repeat([]byte{2}, 32), "mic"); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.SpawnCam(ctx, "callQ", 51003, bytes.Repeat([]byte{3}, 32), "cam"); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.SpawnVid(ctx, "callQ", 51004, bytes.Repeat([]byte{4}, 32), "cam"); err != nil {
		t.Fatal(err)
	}

	if mgr.Mic("callQ") == nil || mgr.Spk("callQ") == nil ||
		mgr.Cam("callQ") == nil || mgr.Vid("callQ") == nil {
		t.Fatal("one or more sides missing from session before teardown")
	}

	start := time.Now()
	if err := mgr.Teardown("callQ"); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {

		t.Errorf("teardown took %v (want <3s with graceful exit)", elapsed)
	}
	if mgr.Mic("callQ") != nil || mgr.Spk("callQ") != nil ||
		mgr.Cam("callQ") != nil || mgr.Vid("callQ") != nil {
		t.Errorf("getters still resolve after Teardown")
	}
	if got := mgr.Sessions(); len(got) != 0 {
		t.Errorf("sessions after Teardown: %v", got)
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
	cam := filepath.Join(dir, "haoma-cam")
	vid := filepath.Join(dir, "haoma-vid")
	for _, p := range []string{mic, spk, cam, vid} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gotMic, gotSpk, gotCam, gotVid, err := streamers.Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if gotMic != mic || gotSpk != spk || gotCam != cam || gotVid != vid {
		t.Errorf("Discover = (%q, %q, %q, %q), want (%q, %q, %q, %q)",
			gotMic, gotSpk, gotCam, gotVid, mic, spk, cam, vid)
	}
}

func TestDiscover_VideoOptional(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", "/nowhere")
	mic := filepath.Join(dir, "haoma-mic")
	spk := filepath.Join(dir, "haoma-spk")
	for _, p := range []string{mic, spk} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gotMic, gotSpk, gotCam, gotVid, err := streamers.Discover(dir)
	if err != nil {
		t.Fatalf("Discover with audio-only dir: %v", err)
	}
	if gotMic != mic || gotSpk != spk {
		t.Errorf("audio paths wrong: mic=%q spk=%q", gotMic, gotSpk)
	}
	if gotCam != "" || gotVid != "" {
		t.Errorf("expected empty cam/vid when absent, got cam=%q vid=%q", gotCam, gotVid)
	}
}

func TestDiscover_FailsWhenAbsent(t *testing.T) {
	t.Setenv("HAOMA_STREAMER_DIR", t.TempDir())
	t.Setenv("PATH", "/nowhere")
	if _, _, _, _, err := streamers.Discover(""); err == nil {
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

func wrapStub(t *testing.T, stub string, flags string) string {
	t.Helper()
	wrapper := filepath.Join(t.TempDir(), "wrap.sh")
	src := fmt.Sprintf("#!/bin/sh\nexec %s %s \"$@\"\n", stub, flags)
	if err := os.WriteFile(wrapper, []byte(src), 0o755); err != nil {
		t.Fatal(err)
	}
	return wrapper
}

func TestTeardown_Exit4AfterStopIsClean(t *testing.T) {
	wrapper := wrapStub(t, buildStub(t), "--exit4-on-term")
	mgr, err := streamers.New(streamers.Config{Logger: discardLogger(), MicPath: wrapper, SpkPath: wrapper})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Shutdown)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := mgr.SpawnMic(ctx, "call4", 33333, bytes.Repeat([]byte{1}, 32), "mic"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Teardown("call4"); err != nil {
		t.Fatalf("Teardown after a stop that ended in exit 4: %v, want nil", err)
	}
}

func TestTeardown_Exit4WithoutStopIsReported(t *testing.T) {
	wrapper := wrapStub(t, buildStub(t), "--exit4-after-ready")
	mgr, err := streamers.New(streamers.Config{Logger: discardLogger(), MicPath: wrapper, SpkPath: wrapper})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Shutdown)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := mgr.SpawnMic(ctx, "call5", 33334, bytes.Repeat([]byte{1}, 32), "mic"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	err = mgr.Teardown("call5")
	if err == nil || !strings.Contains(err.Error(), "exit status 4") {
		t.Fatalf("Teardown = %v, want an error naming exit status 4", err)
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func findLine(s, substr string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, substr) {
			return l
		}
	}
	return ""
}

func readUntilType(t *testing.T, events <-chan streamers.Event, want string, timeout time.Duration) streamers.Event {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatalf("events channel closed before type %q arrived", want)
			}
			if ev.Type == want {
				return ev
			}
		case <-deadline:
			t.Fatalf("timed out waiting for type %q", want)
		}
	}
}

func TestReadEvents_RawKeptForSummaryAndEvent_EmptyForStats(t *testing.T) {
	stub := buildStub(t)
	t.Setenv("STREAMERS_TEST_PRINT_LINES", strings.Join([]string{
		`{"type":"summary","period_ms":1000,"frames_in":5}`,
		`{"type":"event","ev":"xrun","xrun_total":3}`,
		`{"type":"stats","bytes_in":1,"bytes_out":2,"frames_in":1,"frames_out":1,"frames_dropped":0,"jitter_ms":0.1,"cpu_pct":0.0}`,
	}, "\x1e"))

	mgr, err := streamers.New(streamers.Config{Logger: discardLogger(), MicPath: stub, SpkPath: stub})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Shutdown)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := mgr.SpawnMic(ctx, "callRaw", 40001, bytes.Repeat([]byte{7}, 32), "mic")
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}

	summary := readUntilType(t, stream.Events(), "summary", 5*time.Second)
	if len(summary.Raw) == 0 {
		t.Error("summary event: Raw is empty, want the raw line kept")
	} else if !bytes.Contains(summary.Raw, []byte(`"frames_in":5`)) {
		t.Errorf("summary Raw = %q, want it to contain the summary's own fields", summary.Raw)
	}

	event := readUntilType(t, stream.Events(), "event", 5*time.Second)
	if len(event.Raw) == 0 {
		t.Error("event event: Raw is empty, want the raw line kept")
	} else if !bytes.Contains(event.Raw, []byte(`"ev":"xrun"`)) {
		t.Errorf("event Raw = %q, want it to contain the event's own fields", event.Raw)
	}

	stats := readUntilType(t, stream.Events(), "stats", 5*time.Second)
	if len(stats.Raw) != 0 {
		t.Errorf("stats event: Raw = %q, want empty (stats keeps its named fields only)", stats.Raw)
	}

	if err := mgr.Teardown("callRaw"); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
}

func TestPumpStderr_DBGLineAtDebug_OthersAtInfo(t *testing.T) {
	stub := buildStub(t)
	t.Setenv("STREAMERS_TEST_STDERR_LINES", strings.Join([]string{
		"[haoma-mic] DBG : queue drained",
		"[haoma-mic] INFO: plain line",
	}, "\x1e"))

	buf := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	mgr, err := streamers.New(streamers.Config{Logger: logger, MicPath: stub, SpkPath: stub})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Shutdown)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := mgr.SpawnMic(ctx, "callDbg", 40002, bytes.Repeat([]byte{8}, 32), "mic")
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}

	var out string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out = buf.String()
		if strings.Contains(out, "queue drained") && strings.Contains(out, "plain line") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := mgr.Teardown("callDbg"); err != nil {
		t.Fatalf("Teardown: %v", err)
	}

	dbgLine := findLine(out, "queue drained")
	if dbgLine == "" || !strings.Contains(dbgLine, "level=DEBUG") {
		t.Errorf("DBG stderr line not logged at Debug: %q", dbgLine)
	}
	infoLine := findLine(out, "plain line")
	if infoLine == "" || !strings.Contains(infoLine, "level=INFO") {
		t.Errorf("plain stderr line not logged at Info: %q", infoLine)
	}
}

var droppedRe = regexp.MustCompile(`dropped=(\d+)`)

func sumDropped(s string) int {
	total := 0
	for _, m := range droppedRe.FindAllStringSubmatch(s, -1) {
		n, err := strconv.Atoi(m[1])
		if err == nil {
			total += n
		}
	}
	return total
}

func TestReadEvents_ChannelFull_LogsDroppedCount(t *testing.T) {
	stub := buildStub(t)
	const floodCount = 20
	lines := make([]string, 0, floodCount)
	for i := 0; i < floodCount; i++ {
		lines = append(lines, fmt.Sprintf(`{"type":"event","ev":"flood","counter":%d}`, i))
	}
	t.Setenv("STREAMERS_TEST_PRINT_LINES", strings.Join(lines, "\x1e"))

	buf := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	mgr, err := streamers.New(streamers.Config{Logger: logger, MicPath: stub, SpkPath: stub})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Shutdown)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := mgr.SpawnMic(ctx, "callFlood", 40003, bytes.Repeat([]byte{9}, 32), "mic")
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	_ = stream

	var out string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out = buf.String()
		if strings.Contains(out, "streamer event dropped") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := mgr.Teardown("callFlood"); err != nil {
		t.Fatalf("Teardown: %v", err)
	}

	wantDropped := 1 + floodCount - 16
	var got int
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		out = buf.String()
		if got = sumDropped(out); got == wantDropped {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if !strings.Contains(out, "streamer event dropped") {
		t.Fatalf("no dropped-event log line; log:\n%s", out)
	}
	if got != wantDropped {
		t.Errorf("total dropped = %d, want %d; log:\n%s", got, wantDropped, out)
	}
}
