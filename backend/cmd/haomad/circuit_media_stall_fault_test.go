//go:build faults

package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"haoma/internal/proxy"
)

func TestParseMediaStallFaultValue(t *testing.T) {
	stall, every, capFor, clean, capBps, ok := parseMediaStallFaultValue("stall=8s,every=60s,cap=5000,capfor=30s")
	if !ok {
		t.Fatal("good value did not parse")
	}
	if stall != 8*time.Second || every != 60*time.Second || capFor != 30*time.Second || capBps != 5000 {
		t.Fatalf("parsed = %v/%v/%v/%d, want 8s/60s/30s/5000", stall, every, capFor, capBps)
	}
	if clean != defaultCleanPeriod {
		t.Fatalf("clean = %v, want the default %v when the key is left out", clean, defaultCleanPeriod)
	}

	if _, _, _, clean, _, ok := parseMediaStallFaultValue("stall=8s,every=60s,cap=5000,capfor=30s,clean=2s"); !ok || clean != 2*time.Second {
		t.Fatalf("explicit clean=2s: ok=%v clean=%v, want ok clean=2s", ok, clean)
	}

	for _, bad := range []string{
		"",
		"stall=8s",
		"stall=8s,every=60s,cap=0,capfor=30s",
		"stall=0s,every=60s,cap=5000,capfor=30s",
		"stall=8s,every=60s,cap=five,capfor=30s",
		"stall=8s,every=60s,cap=5000,capfor=30",
		"stall=8s,every=60s,cap=5000,capfor=30s,clean=bogus",
		"stall=8s,every=60s,cap=5000,capfor=60s",
		"stall=8s,every=60s,cap=5000,capfor=50s,clean=10s",
		"garbage",
	} {
		if _, _, _, _, _, ok := parseMediaStallFaultValue(bad); ok {
			t.Fatalf("bad value %q parsed as ok", bad)
		}
	}
}

func TestReadMediaStallFault(t *testing.T) {
	if f := readMediaStallFault(func(string) string { return "" }); f != nil {
		t.Fatal("the fault is on with the variable empty")
	}
	if f := readMediaStallFault(func(k string) string {
		if k == callMediaStallEnv {
			return "garbage"
		}
		return ""
	}); f != nil {
		t.Fatal("a bad value left the fault on")
	}
	f := readMediaStallFault(func(k string) string {
		if k == callMediaStallEnv {
			return "stall=8s,every=60s,cap=5000,capfor=30s"
		}
		return ""
	})
	if f == nil || f.stall != 8*time.Second || f.capBytesPerSec != 5000 || f.clean != defaultCleanPeriod {
		t.Fatalf("fault = %+v, want stall=8s cap=5000 clean=%v", f, defaultCleanPeriod)
	}

	wantStall, wantEvery, wantCapFor, wantClean, wantCap, ok := parseMediaStallFaultValue(defaultMediaStallValue)
	if !ok {
		t.Fatalf("defaultMediaStallValue itself does not parse: %q", defaultMediaStallValue)
	}
	for _, v := range []string{"1", "on"} {
		f := readMediaStallFault(func(k string) string {
			if k == callMediaStallEnv {
				return v
			}
			return ""
		})
		if f == nil || f.stall != wantStall || f.every != wantEvery || f.capFor != wantCapFor ||
			f.clean != wantClean || f.capBytesPerSec != wantCap {
			t.Fatalf("value %q -> %+v, want the defaultMediaStallValue fields %v/%v/%v/%v/%d",
				v, f, wantStall, wantEvery, wantCapFor, wantClean, wantCap)
		}
	}

	var off *mediaStallFault
	if w := off.wrapServe(proxy.ModalityAudio, &bytes.Buffer{}); w == nil {
		t.Fatal("wrapServe on a nil fault returned nil")
	}
}

type fakeClock struct {
	t     time.Time
	slept []time.Duration
}

func (c *fakeClock) now() time.Time { return c.t }
func (c *fakeClock) sleep(d time.Duration) {
	c.slept = append(c.slept, d)
	c.t = c.t.Add(d)
}

func wrapWithClock(f *mediaStallFault, dst io.Writer, clock *fakeClock) *mediaStallWriter {
	f.now, f.sleep = clock.now, clock.sleep
	return f.wrapServe(proxy.ModalityAudio, dst).(*mediaStallWriter)
}

func TestMediaStallFault_StallThenCapThenNormal(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}

	f := &mediaStallFault{clean: 0, stall: 8 * time.Second, every: 20 * time.Second, capFor: 5 * time.Second, capBytesPerSec: 1000}
	var dst bytes.Buffer
	sw := wrapWithClock(f, &dst, clock)

	n, err := sw.Write([]byte("hello"))
	if err != nil || n != 5 {
		t.Fatalf("stall-phase write = %d,%v, want 5,nil", n, err)
	}
	if dst.String() != "hello" {
		t.Fatalf("dst = %q, want hello written after the stall", dst.String())
	}
	if len(clock.slept) == 0 || sumDur(clock.slept) != 8*time.Second {
		t.Fatalf("slept = %v, want a total of 8s (in 50ms steps)", clock.slept)
	}

	dst.Reset()
	sleptBefore := sumDur(clock.slept)
	n, err = sw.Write(make([]byte, 1000))
	if err != nil || n != 1000 {
		t.Fatalf("cap-phase write = %d,%v, want 1000,nil", n, err)
	}
	if sumDur(clock.slept)-sleptBefore != time.Second {
		t.Fatalf("cap-phase pace = %v, want 1s", sumDur(clock.slept)-sleptBefore)
	}

	clock.t = clock.t.Add(6 * time.Second)
	nBefore := len(clock.slept)
	n, err = sw.Write([]byte("xy"))
	if err != nil || n != 2 {
		t.Fatalf("normal-phase write = %d,%v, want 2,nil", n, err)
	}
	if len(clock.slept) != nBefore {
		t.Fatalf("normal phase slept %v, want no extra sleep", clock.slept[nBefore:])
	}

	clock.t = clock.t.Add(6 * time.Second)
	nBefore = len(clock.slept)
	_, err = sw.Write([]byte("z"))
	if err != nil {
		t.Fatalf("second-cycle write: %v", err)
	}
	if len(clock.slept) == nBefore {
		t.Fatalf("second cycle did not stall again: slept = %v", clock.slept)
	}
}

func sumDur(ds []time.Duration) time.Duration {
	var total time.Duration
	for _, d := range ds {
		total += d
	}
	return total
}

func TestMediaStallFault_CleanPeriodBeforeFirstStall(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	f := &mediaStallFault{clean: 5 * time.Second, stall: 8 * time.Second, every: 60 * time.Second, capFor: 30 * time.Second, capBytesPerSec: 5000}
	var dst bytes.Buffer
	sw := wrapWithClock(f, &dst, clock)

	n, err := sw.Write([]byte("clean-phase-bytes"))
	if err != nil || n != len("clean-phase-bytes") {
		t.Fatalf("clean-phase write = %d,%v, want %d,nil", n, err, len("clean-phase-bytes"))
	}
	if len(clock.slept) != 0 {
		t.Fatalf("clean phase slept %v, want none", clock.slept)
	}

	clock.t = clock.t.Add(5 * time.Second)
	_, err = sw.Write([]byte("x"))
	if err != nil {
		t.Fatalf("stall-phase write: %v", err)
	}
	if len(clock.slept) == 0 {
		t.Fatalf("stall phase did not sleep: slept = %v", clock.slept)
	}
}

func TestMediaStallFault_PerStreamCycleStart(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0)}
	f := &mediaStallFault{clean: 5 * time.Second, stall: 8 * time.Second, every: 60 * time.Second, capFor: 30 * time.Second, capBytesPerSec: 5000}
	var dstA bytes.Buffer
	swA := wrapWithClock(f, &dstA, clock)
	if _, err := swA.Write([]byte("a")); err != nil {
		t.Fatalf("stream A write: %v", err)
	}

	clock.t = clock.t.Add(5 * time.Second)
	before := len(clock.slept)
	if _, err := swA.Write([]byte("a")); err != nil {
		t.Fatalf("stream A second write: %v", err)
	}
	if len(clock.slept) == before {
		t.Fatalf("stream A did not stall on its second write")
	}

	var dstB bytes.Buffer
	swB := f.wrapServe(proxy.ModalityAudio, &dstB).(*mediaStallWriter)
	before = len(clock.slept)
	if _, err := swB.Write([]byte("b")); err != nil {
		t.Fatalf("stream B write: %v", err)
	}
	if len(clock.slept) != before {
		t.Fatalf("stream B slept on its first write (shared cycle start): slept = %v", clock.slept[before:])
	}
}

func TestMediaStallFault_CancelInterruptsStallSleep(t *testing.T) {
	f := &mediaStallFault{clean: 0, stall: 8 * time.Second, every: 60 * time.Second, capFor: 30 * time.Second, capBytesPerSec: 5000, now: time.Now, sleep: time.Sleep}
	var dst bytes.Buffer
	sw := f.wrapServe(proxy.ModalityAudio, &dst).(*mediaStallWriter)

	done := make(chan struct{})
	go func() {
		defer close(done)
		sw.Write([]byte("x"))
	}()

	time.Sleep(20 * time.Millisecond)
	sw.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Write did not return within 2s of Close — the stall sleep was not interrupted")
	}
}

func TestMediaStallFault_LogsStallOnTheFirstCycle(t *testing.T) {
	f := readMediaStallFault(func(k string) string {
		if k == callMediaStallEnv {
			return "stall=1s,every=60s,cap=5000,capfor=1s,clean=0s"
		}
		return ""
	})
	if f == nil {
		t.Fatal("fault did not read on")
	}
	clock := &fakeClock{t: time.Unix(0, 0)}
	var dst bytes.Buffer
	sw := wrapWithClock(f, &dst, clock)

	recs := newSlogRecords()
	prev := slog.Default()
	slog.SetDefault(slog.New(recordingHandler{s: recs}))
	defer slog.SetDefault(prev)

	if _, err := sw.Write([]byte("x")); err != nil {
		t.Fatalf("write: %v", err)
	}
	found := false
	for _, msg := range recs.messages() {
		if msg == callMediaStalledLine(f.stall) {
			found = true
		}
	}
	if !found {
		t.Fatalf("messages = %v, want the stalled line on cycle 0", recs.messages())
	}
}

type slogRecords struct {
	mu  sync.Mutex
	out []slog.Record
}

func newSlogRecords() *slogRecords { return &slogRecords{} }

type recordingHandler struct{ s *slogRecords }

func (h recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.s.mu.Lock()
	h.s.out = append(h.s.out, r)
	h.s.mu.Unlock()
	return nil
}
func (h recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h recordingHandler) WithGroup(string) slog.Handler      { return h }

func (s *slogRecords) messages() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.out))
	for i, r := range s.out {
		out[i] = r.Message
	}
	return out
}

func TestMediaStallFault_WrapServe_OnlyAudio(t *testing.T) {
	f := &mediaStallFault{stall: time.Second, every: time.Minute, capFor: time.Second, capBytesPerSec: 1}
	var dst bytes.Buffer
	if w := f.wrapServe(proxy.ModalityVideo, &dst); w != io.Writer(&dst) {
		t.Fatal("wrapServe wrapped a non-audio modality")
	}
	if w := f.wrapServe(proxy.ModalityAudio, &dst); w == io.Writer(&dst) {
		t.Fatal("wrapServe did not wrap an audio modality")
	}
}
