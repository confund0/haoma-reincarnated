package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/streamers"
)

func findLine(s string, substrs ...string) string {
	for _, l := range strings.Split(s, "\n") {
		ok := true
		for _, sub := range substrs {
			if !strings.Contains(l, sub) {
				ok = false
				break
			}
		}
		if ok {
			return l
		}
	}
	return ""
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

func buildFakeMic(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "haoma-mic")
	cmd := exec.Command("go", "build", "-o", bin, "haoma-frontend/cmd/haoma-rig/fakestreamer")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build fakestreamer: %v\n%s", err, out)
	}
	return bin
}

func TestPumpStreamEvents_SummaryLogsNoPush_StatsPushesNoLog(t *testing.T) {
	micBin := buildFakeMic(t)

	mgr, err := streamers.New(streamers.Config{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		MicPath: micBin,
		SpkPath: micBin,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Shutdown)

	port := freePort(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := mgr.SpawnMic(ctx, "callSummary", port, bytes.Repeat([]byte{3}, 32), "mic")
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}

	peerConn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("dial fake mic: %v", err)
	}
	t.Cleanup(func() { _ = peerConn.Close() })

	buf := &syncBuffer{}
	prevDefault := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prevDefault) })

	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, cert, tok := newTestDaemon(t, stub)
	conn := dialTest(t, ctx, addr, cert, tok)

	statsBuf := &syncBuffer{}
	d.statsLog = slog.New(slog.NewTextHandler(statsBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	pumpStreamEvents(d, "callSummary", streamers.SideMic, stream)

	var statsOut string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		statsOut = statsBuf.String()
		if strings.Contains(statsOut, `msg="call media summary"`) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	summaryLine := findLine(statsOut, `msg="call media summary"`)
	if summaryLine == "" {
		t.Fatalf("no \"call media summary\" log line on statsLog; log:\n%s", statsOut)
	}
	if !strings.Contains(summaryLine, "call=") || !strings.Contains(summaryLine, "side=mic") {
		t.Errorf("summary log line missing call/side: %q", summaryLine)
	}
	if !strings.Contains(summaryLine, "period_ms=") {
		t.Errorf("summary log line missing a raw field (period_ms): %q", summaryLine)
	}
	if strings.Contains(buf.String(), `msg="call media summary"`) {
		t.Errorf("summary line also landed on the main log; log:\n%s", buf.String())
	}

	if err := stream.SendCommand(map[string]any{"cmd": "stats"}); err != nil {
		t.Fatalf("SendCommand(stats): %v", err)
	}

	f := readUntil(t, ctx, conn, ipc.FrameCallStreamEvent)
	var payload ipc.CallStreamEventPayload
	if err := json.Unmarshal(f.Payload, &payload); err != nil {
		t.Fatalf("decode CallStreamEventPayload: %v", err)
	}
	if payload.Type != "stats" {
		t.Errorf("pushed stream-event type = %q, want stats", payload.Type)
	}

	out := buf.String()
	if strings.Contains(out, `msg="call stream event"`) && strings.Contains(out, "type=stats") {
		t.Errorf("stats got its old per-event debug line too; log:\n%s", out)
	}
	if strings.Contains(out, `msg="call stream event"`) && strings.Contains(out, "type=summary") {
		t.Errorf("a summary line was logged as a generic stream event, not through the raw-field path; log:\n%s", out)
	}
}

func TestPumpStreamEvents_StatsLogNil_NoSummary(t *testing.T) {
	micBin := buildFakeMic(t)

	mgr, err := streamers.New(streamers.Config{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		MicPath: micBin,
		SpkPath: micBin,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Shutdown)

	port := freePort(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := mgr.SpawnMic(ctx, "callSummaryOff", port, bytes.Repeat([]byte{3}, 32), "mic")
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	peerConn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("dial fake mic: %v", err)
	}
	t.Cleanup(func() { _ = peerConn.Close() })

	buf := &syncBuffer{}
	prevDefault := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prevDefault) })

	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)

	pumpStreamEvents(d, "callSummaryOff", streamers.SideMic, stream)

	if err := stream.SendCommand(map[string]any{"cmd": "stats"}); err != nil {
		t.Fatalf("SendCommand(stats): %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var out string
	for time.Now().Before(deadline) {
		out = buf.String()
		if strings.Contains(out, `msg="call stream event"`) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if strings.Contains(out, `msg="call media summary"`) {
		t.Errorf("summary line appeared on the main log with --call-stats off; log:\n%s", out)
	}
}
