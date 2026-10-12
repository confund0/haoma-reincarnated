package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/chacha20poly1305"

	"haoma-frontend/internal/streamers"
)

func TestMain(m *testing.M) {
	switch roleOf(os.Args[0]) {
	case micName, spkName:
		os.Exit(runMain())
	}
	os.Exit(m.Run())
}

const realSealedFrame = "0070000000000000000700000000075bcd15d3075ebcb0832a5e6c6dbbf0d78fd32c34b230367ee33d83406a1bdf80530354a457f1a51562f4b9acd2662e13320b7777ac90df9f2348f37ae426bad3ae57f105c2e53a214254e624e88fe36645fdf0611380e5e9bbb8eef60d7d9491d6c72d"

func fixedKey() []byte {
	k := make([]byte, keyBytes)
	for i := range k {
		k[i] = byte(i)
	}
	return k
}

func TestSealMatchesRealStreamer(t *testing.T) {
	a, err := chacha20poly1305.New(fixedKey())
	if err != nil {
		t.Fatal(err)
	}
	got, err := sealFrame(a, "mic", 7, 123456789, silencePacket)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(got) != realSealedFrame {
		t.Fatalf("frame differs from the real streamer's:\n got %x\nwant %s", got, realSealedFrame)
	}
	if len(silencePacket) != 80 {
		t.Fatalf("silence packet is %d bytes, the real mic sends 80", len(silencePacket))
	}
}

func TestReadFrame(t *testing.T) {
	a, _ := chacha20poly1305.New(fixedKey())
	f1, _ := sealFrame(a, "mic", 1, 10, silencePacket)
	f2, _ := sealFrame(a, "mic", 2, 30, []byte{})
	r := bytes.NewReader(append(append([]byte{}, f1...), f2...))
	for i, want := range []struct{ counter, pts uint64 }{{1, 10}, {2, 30}} {
		c, p, sealed, err := readFrame(r)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if c != want.counter || p != want.pts {
			t.Fatalf("frame %d: counter %d pts %d, want %d %d", i, c, p, want.counter, want.pts)
		}
		if _, err := a.Open(nil, nonce(c), sealed, aad("mic", c, p)); err != nil {
			t.Fatalf("frame %d does not open: %v", i, err)
		}
	}
	if _, _, _, err := readFrame(r); err != io.EOF {
		t.Fatalf("clean end: got %v, want io.EOF", err)
	}

	if _, _, _, err := readFrame(bytes.NewReader(f1[:1])); err != errFrame {
		t.Fatalf("cut in the length: got %v", err)
	}
	if _, _, _, err := readFrame(bytes.NewReader(f1[:40])); err != errFrame {
		t.Fatalf("cut in the body: got %v", err)
	}

	if _, _, _, err := readFrame(bytes.NewReader([]byte{0x00, 0x1F})); err != errFrame {
		t.Fatalf("short length: got %v", err)
	}
}

func TestParseCommand(t *testing.T) {
	for _, c := range []struct {
		line string
		want command
	}{
		{`{"cmd":"mute"}`, command{cmd: "mute"}},
		{`{"cmd":"unmute"}`, command{cmd: "unmute"}},
		{`{"cmd":"stats"}`, command{cmd: "stats"}},
		{`{"cmd":"exit"}`, command{cmd: "exit"}},
		{`{"cmd":"bitrate","kbps":24}`, command{cmd: "bitrate", kbps: 24}},
		{`{  "cmd"  :  "mute"  }`, command{cmd: "mute"}},
		{`{"kbps":48,"cmd":"bitrate"}`, command{cmd: "bitrate", kbps: 48}},
		{`{"cmd":"bitrate","kbps":-1}`, command{cmd: "bitrate", kbps: -1}},
		{``, command{}},
		{`not json`, command{}},
		{`{"cmd":"bogus"}`, command{}},
		{`{"cmd":"bitrate"}`, command{}},
		{`{"foo":"bar"}`, command{}},
		{`{"cmd":}`, command{}},
		{`{"cmd":"mute)`, command{}},
	} {
		if got := parseCommand(c.line); got != c.want {
			t.Errorf("parseCommand(%q) = %+v, want %+v", c.line, got, c.want)
		}
	}
}

func TestAtoi(t *testing.T) {
	for in, want := range map[string]int{"8080": 8080, " 42x": 42, "+7": 7, "-3": -3, "x": 0, "": 0, "99999999999": 0} {
		if got := atoi(in); got != want {
			t.Errorf("atoi(%q) = %d, want %d", in, got, want)
		}
	}
}

func runInProcess(t *testing.T, args []string, stdin io.Reader) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(args, stdin, &out, &errOut, make(chan struct{}))
	return code, out.String(), errOut.String()
}

func TestCommandLine(t *testing.T) {
	for _, c := range []struct {
		name     string
		args     []string
		wantCode int
		wantErr  string
	}{
		{"no port", []string{"haoma-mic", "--stream-id", "mic"}, exitArgs, "[haoma-mic] ERR : --port required"},
		{"no stream id", []string{"haoma-spk", "--port", "5000"}, exitArgs, "[haoma-spk] ERR : --stream-id required"},
		{"port 0", []string{"haoma-mic", "--port", "0", "--stream-id", "mic"}, exitArgs, "invalid --port: 0"},
		{"port too high", []string{"haoma-mic", "-p", "70000", "--stream-id", "mic"}, exitArgs, "invalid --port: 70000"},
		{"port with no value", []string{"haoma-mic", "--stream-id", "mic", "--port"}, exitArgs, "unknown arg: --port"},
		{"unknown flag", []string{"haoma-spk", "--port", "5000", "--stream-id", "mic", "--loud"}, exitArgs, "unknown arg: --loud"},
		{"help", []string{"/x/haoma-mic", "--help"}, exitClean, "accepts ONE client, captures mic"},
		{"other name", []string{"haoma-cam", "--port", "5000", "--stream-id", "cam"}, exitArgs, "start it as haoma-mic or haoma-spk"},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, _, errOut := runInProcess(t, c.args, strings.NewReader(""))
			if code != c.wantCode {
				t.Fatalf("exit %d, want %d (stderr %q)", code, c.wantCode, errOut)
			}
			if !strings.Contains(errOut, c.wantErr) {
				t.Fatalf("stderr %q does not hold %q", errOut, c.wantErr)
			}
			if c.wantCode == exitArgs && c.name != "other name" && !strings.Contains(errOut, "usage: ") {
				t.Fatalf("no usage line: %q", errOut)
			}
		})
	}
}

func TestShortKey(t *testing.T) {
	code, out, errOut := runInProcess(t, []string{"haoma-mic", "--port", "5000", "--stream-id", "mic"}, bytes.NewReader(make([]byte, 31)))
	if code != exitKey {
		t.Fatalf("exit %d, want %d", code, exitKey)
	}
	if !strings.Contains(errOut, "short read (31/32 before EOF)") {
		t.Fatalf("stderr %q", errOut)
	}
	if out != "" {
		t.Fatalf("no ready line before the key: got %q", out)
	}
}

func fakeBins(t *testing.T) (mic, spk string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	mic, spk = filepath.Join(dir, micName), filepath.Join(dir, spkName)
	for _, p := range []string{mic, spk} {
		if err := os.Symlink(self, p); err != nil {
			t.Skipf("no symlink here: %v", err)
		}
	}
	return mic, spk
}

func newManager(t *testing.T) *streamers.Manager {
	t.Helper()
	mic, spk := fakeBins(t)

	t.Setenv("HAOMA_STREAMER_DIR", filepath.Dir(mic))
	gotMic, gotSpk, _, _, err := streamers.Discover("")
	if err != nil {
		t.Fatal(err)
	}
	if gotMic != mic || gotSpk != spk {
		t.Fatalf("discover found mic=%s spk=%s, want %s %s", gotMic, gotSpk, mic, spk)
	}
	m, err := streamers.New(streamers.Config{
		Logger:  slog.New(slog.DiscardHandler),
		MicPath: gotMic,
		SpkPath: gotSpk,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Shutdown)
	return m
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func randomKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, keyBytes)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func spawnReady(t *testing.T, ctx context.Context, spawn func(context.Context, string, int, []byte, string) (*streamers.Stream, error), callID string, port int, key []byte) *streamers.Stream {
	t.Helper()
	s, err := spawn(ctx, callID, port, key, "mic")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WaitReady(ctx); err != nil {
		t.Fatalf("ready: %v", err)
	}
	return s
}

func splice(t *testing.T, micPort, spkPort int) (micConn, spkConn net.Conn) {
	t.Helper()
	var err error
	micConn, err = net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(micPort)))
	if err != nil {
		t.Fatal(err)
	}
	spkConn, err = net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(spkPort)))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(spkConn, micConn) }()
	t.Cleanup(func() { micConn.Close(); spkConn.Close() })
	return micConn, spkConn
}

func awaitStats(t *testing.T, s *streamers.Stream, timeout time.Duration, want func(streamers.Event) bool) streamers.Event {
	t.Helper()
	deadline := time.After(timeout)
	var last streamers.Event
	for {
		select {
		case ev, ok := <-s.Events():
			if !ok {
				t.Fatalf("the %s exited; last stats %+v", s.Side, last)
			}
			if ev.Type != "stats" {
				continue
			}
			last = ev
			if want(ev) {
				return ev
			}
		case <-deadline:
			t.Fatalf("no wanted stats from the %s in %s; last %+v", s.Side, timeout, last)
		}
	}
}

func TestMicToSpkThroughHaomaStreamers(t *testing.T) {
	m := newManager(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	outbound := randomKey(t)
	sub, err := streamers.DeriveStreamKey(outbound, "mic")
	if err != nil {
		t.Fatal(err)
	}
	micPort, spkPort := freePort(t), freePort(t)
	mic := spawnReady(t, ctx, m.SpawnMic, "call-1", micPort, sub)
	spk := spawnReady(t, ctx, m.SpawnSpk, "call-1", spkPort, sub)
	splice(t, micPort, spkPort)

	got := awaitStats(t, spk, 10*time.Second, func(ev streamers.Event) bool { return ev.FramesIn >= 50 })
	if got.FramesDropped != 0 {
		t.Fatalf("spk dropped %d frames with the right key", got.FramesDropped)
	}
	if got.BytesIn < got.FramesIn*uint64(frameOverhead+len(silencePacket)) {
		t.Fatalf("bytes_in %d for %d frames", got.BytesIn, got.FramesIn)
	}
	awaitStats(t, mic, 5*time.Second, func(ev streamers.Event) bool { return ev.FramesOut > 0 })

	if err := spk.SendCommand(map[string]any{"cmd": "stats"}); err != nil {
		t.Fatal(err)
	}
	awaitStats(t, spk, time.Second, func(streamers.Event) bool { return true })

	start := time.Now()
	if err := m.Teardown("call-1"); err != nil {
		t.Fatalf("teardown of a connected call: %v", err)
	}
	if d := time.Since(start); d > 900*time.Millisecond {
		t.Fatalf("teardown took %s: the exit command was not taken", d)
	}
}

func TestWrongKeyIsDropped(t *testing.T) {
	m := newManager(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	micKey, _ := streamers.DeriveStreamKey(randomKey(t), "mic")
	spkKey, _ := streamers.DeriveStreamKey(randomKey(t), "mic")
	micPort, spkPort := freePort(t), freePort(t)
	spawnReady(t, ctx, m.SpawnMic, "call-2", micPort, micKey)
	spk := spawnReady(t, ctx, m.SpawnSpk, "call-2", spkPort, spkKey)
	splice(t, micPort, spkPort)
	got := awaitStats(t, spk, 10*time.Second, func(ev streamers.Event) bool { return ev.FramesDropped >= 20 })
	if got.FramesIn != 0 {
		t.Fatalf("spk counted %d frames sealed with another key", got.FramesIn)
	}
}

func TestMicTakesOneClient(t *testing.T) {
	m := newManager(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	key, _ := streamers.DeriveStreamKey(randomKey(t), "mic")
	port := freePort(t)
	spawnReady(t, ctx, m.SpawnMic, "call-3", port, key)
	first, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	_ = first.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, _, err := readFrame(first); err != nil {
		t.Fatalf("no frame on the first connection: %v", err)
	}
	second, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err == nil {
		second.Close()
		t.Fatal("a second client connected to the mic")
	}
}

func TestStopBeforeClientExitsFour(t *testing.T) {
	m := newManager(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	key, _ := streamers.DeriveStreamKey(randomKey(t), "mic")
	spawnReady(t, ctx, m.SpawnMic, "call-4", freePort(t), key)
	start := time.Now()
	err := m.Teardown("call-4")
	if err != nil {
		t.Fatalf("teardown before a client: got %v, want nil (exit 4 after a stop is clean)", err)
	}
	if d := time.Since(start); d < 900*time.Millisecond {
		t.Fatalf("teardown took %s: the exit command was read before a client connected", d)
	}
}
