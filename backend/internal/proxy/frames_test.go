package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func buildFrame(counter, pts uint64, payload []byte) []byte {
	body := make([]byte, 8+8+len(payload)+16)
	binary.BigEndian.PutUint64(body[0:8], counter)
	binary.BigEndian.PutUint64(body[8:16], pts)
	copy(body[16:16+len(payload)], payload)
	frame := make([]byte, 2+len(body))
	binary.BigEndian.PutUint16(frame[0:2], uint16(len(body)))
	copy(frame[2:], body)
	return frame
}

type chunkedReader struct {
	data []byte
	n    int
}

func (c *chunkedReader) Read(p []byte) (int, error) {
	if len(c.data) == 0 {
		return 0, io.EOF
	}
	k := c.n
	if k > len(p) {
		k = len(p)
	}
	if k > len(c.data) {
		k = len(c.data)
	}
	copy(p, c.data[:k])
	c.data = c.data[k:]
	return k, nil
}

func TestFrameConsts_MatchTheWireCeiling(t *testing.T) {

	if maxFrameBody != 65535 {
		t.Fatalf("maxFrameBody = %d, want 65535", maxFrameBody)
	}
	if minFrameBody != 32 {
		t.Fatalf("minFrameBody = %d, want 32 (counter+pts+tag)", minFrameBody)
	}
}

func TestReadFrame_SplitAcrossManyReads(t *testing.T) {
	want := buildFrame(7, 123456, []byte("hello-mic"))
	r := bufio.NewReader(&chunkedReader{data: want, n: 3})
	got, counter, pts, err := readFrame(r, nil)
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	if counter != 7 || pts != 123456 {
		t.Fatalf("counter/pts = %d/%d, want 7/123456", counter, pts)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("frame bytes = %x, want %x", got, want)
	}
}

func TestReadFrame_TwoFramesInOneRead(t *testing.T) {
	a := buildFrame(1, 1000, []byte("A"))
	b := buildFrame(2, 1020, []byte("BB"))
	r := bufio.NewReader(bytes.NewReader(append(append([]byte{}, a...), b...)))

	got1, c1, p1, err := readFrame(r, nil)
	if err != nil {
		t.Fatalf("first readFrame: %v", err)
	}
	if !bytes.Equal(got1, a) || c1 != 1 || p1 != 1000 {
		t.Fatalf("first frame wrong: got %x c=%d p=%d", got1, c1, p1)
	}
	got2, c2, p2, err := readFrame(r, nil)
	if err != nil {
		t.Fatalf("second readFrame: %v", err)
	}
	if !bytes.Equal(got2, b) || c2 != 2 || p2 != 1020 {
		t.Fatalf("second frame wrong: got %x c=%d p=%d", got2, c2, p2)
	}
	if _, _, _, err := readFrame(r, nil); !errors.Is(err, io.EOF) {
		t.Fatalf("third readFrame err = %v, want io.EOF", err)
	}
}

func TestReadFrame_ShortBodyIsParseError(t *testing.T) {

	hdr := make([]byte, 2)
	binary.BigEndian.PutUint16(hdr, 10)
	r := bufio.NewReader(bytes.NewReader(hdr))
	_, _, _, err := readFrame(r, nil)
	var pe *frameParseError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want *frameParseError", err)
	}
	if !bytes.Equal(pe.consumed, hdr) {
		t.Fatalf("consumed = %x, want the 2 header bytes %x", pe.consumed, hdr)
	}
}

func TestReadFrame_EOFCases(t *testing.T) {

	r := bufio.NewReader(bytes.NewReader(nil))
	if _, _, _, err := readFrame(r, nil); !errors.Is(err, io.EOF) {
		t.Fatalf("empty stream err = %v, want io.EOF", err)
	}

	r = bufio.NewReader(bytes.NewReader([]byte{0x00}))
	if _, _, _, err := readFrame(r, nil); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("1-byte stream err = %v, want io.ErrUnexpectedEOF", err)
	}

	full := buildFrame(1, 1000, []byte("hello"))
	r = bufio.NewReader(bytes.NewReader(full[:len(full)-3]))
	if _, _, _, err := readFrame(r, nil); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated-body stream err = %v, want io.ErrUnexpectedEOF", err)
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

func newTestLogger() (*slog.Logger, *slogRecords) {
	recs := newSlogRecords()
	return slog.New(recordingHandler{s: recs}), recs
}

func TestMediaMeter_ExcessDelay_SteadyThenBurst(t *testing.T) {
	log, _ := newTestLogger()
	mm := newMediaMeter(log, log, "fetch", "tok")

	base := time.Unix(1000, 0)

	for i := 0; i < 10; i++ {
		now := base.Add(time.Duration(i) * 20 * time.Millisecond)
		pts := uint64(now.Add(-100 * time.Millisecond).UnixNano())
		if excess := mm.onFrameIn(now, 80, pts); excess != 0 {
			t.Fatalf("steady frame %d: excess = %d, want 0", i, excess)
		}
	}

	afterPause := base.Add(10 * 20 * time.Millisecond).Add(3 * time.Second)
	burstPts := afterPause.Add(-100 * time.Millisecond).Add(-3 * time.Second)
	excess := mm.onFrameIn(afterPause, 80, uint64(burstPts.UnixNano()))
	if excess < 2900 || excess > 3100 {
		t.Fatalf("burst-start excess = %d ms, want ~3000", excess)
	}
	maxSeen := excess
	var last int64
	for i := 1; i <= 150; i++ {
		pts := burstPts.Add(time.Duration(i) * 20 * time.Millisecond)
		last = mm.onFrameIn(afterPause, 80, uint64(pts.UnixNano()))
		if last > maxSeen {
			maxSeen = last
		}
	}

	if last != 0 {
		t.Fatalf("post-burst excess = %d, want 0", last)
	}
	if maxSeen < 2900 {
		t.Fatalf("max excess seen during burst = %d, want >= ~3000 at its start", maxSeen)
	}
}

func countMsg(msgs []string, want string) int {
	n := 0
	for _, m := range msgs {
		if m == want {
			n++
		}
	}
	return n
}

func TestMediaMeter_LateTick(t *testing.T) {
	log, recs := newTestLogger()
	mm := newMediaMeter(log, log, "serve", "tok")
	start := time.Unix(7000, 0)
	mm.periodStart = start

	mm.tick(start.Add(1300 * time.Millisecond))
	msgs := recs.messages()
	if n := countMsg(msgs, "call media summary"); n != 1 {
		t.Fatalf("summaries logged = %d, want 1", n)
	}
	recs.mu.Lock()
	var periodMs int64
	for _, r := range recs.out {
		if r.Message == "call media summary" {
			r.Attrs(func(a slog.Attr) bool {
				if a.Key == "period_ms" {
					periodMs = a.Value.Int64()
				}
				return true
			})
		}
	}
	recs.mu.Unlock()
	if periodMs != 1300 {
		t.Fatalf("period_ms = %d, want 1300 (the real span, not the fixed 1000)", periodMs)
	}

	mm.tick(start.Add(1300 * time.Millisecond))
	if n := countMsg(recs.messages(), "call media summary"); n != 1 {
		t.Fatalf("summaries logged after a same-instant tick = %d, want still 1", n)
	}
}

func TestMediaMeter_LongPause(t *testing.T) {
	log, recs := newTestLogger()
	mm := newMediaMeter(log, log, "serve", "tok")
	start := time.Unix(8000, 0)
	mm.periodStart = start

	mm.tick(start.Add(10 * time.Second))
	if n := countMsg(recs.messages(), "call media summary"); n != 1 {
		t.Fatalf("summaries logged after a 10 s pause = %d, want 1", n)
	}
	recs.mu.Lock()
	var periodMs int64
	for _, r := range recs.out {
		if r.Message == "call media summary" {
			r.Attrs(func(a slog.Attr) bool {
				if a.Key == "period_ms" {
					periodMs = a.Value.Int64()
				}
				return true
			})
		}
	}
	recs.mu.Unlock()
	if periodMs != 10000 {
		t.Fatalf("period_ms = %d, want 10000", periodMs)
	}

	now := start.Add(10 * time.Second)
	for i := 1; i <= 4; i++ {
		mm.tick(now.Add(time.Duration(i) * 200 * time.Millisecond))
	}
	if n := countMsg(recs.messages(), "call media summary"); n != 1 {
		t.Fatalf("summaries logged after the pause + 800ms of normal ticks = %d, want still 1", n)
	}
}

func TestMediaMeter_OneSummaryPerSecond_MinAvgMax(t *testing.T) {
	log, recs := newTestLogger()
	mm := newMediaMeter(log, log, "fetch", "tok")

	start := time.Unix(2000, 0)
	mm.periodStart = start

	mm.onFrameIn(start, 80, uint64(start.Add(-10*time.Millisecond).UnixNano()))
	mm.onFrameIn(start.Add(100*time.Millisecond), 80, uint64(start.Add(100*time.Millisecond).Add(-50*time.Millisecond).UnixNano()))
	mm.onFrameIn(start.Add(200*time.Millisecond), 80, uint64(start.Add(200*time.Millisecond).Add(-30*time.Millisecond).UnixNano()))

	mm.tick(start.Add(800 * time.Millisecond))
	for _, msg := range recs.messages() {
		if msg == "call media summary" {
			t.Fatalf("summary logged before the 1 s period elapsed")
		}
	}

	mm.tick(start.Add(1100 * time.Millisecond))
	found := false
	recs.mu.Lock()
	for _, r := range recs.out {
		if r.Message != "call media summary" {
			continue
		}
		found = true
		var minMs, avgMs, maxMs int64
		r.Attrs(func(a slog.Attr) bool {
			switch a.Key {
			case "delay_excess_min_ms":
				minMs = a.Value.Int64()
			case "delay_excess_avg_ms":
				avgMs = a.Value.Int64()
			case "delay_excess_max_ms":
				maxMs = a.Value.Int64()
			}
			return true
		})
		if minMs != 0 {
			t.Fatalf("delay_excess_min_ms = %d, want 0 (the first frame sets the baseline)", minMs)
		}
		if maxMs < 10 || maxMs > 40 {
			t.Fatalf("delay_excess_max_ms = %d, want ~20-40 (50 ms frame relative to the 10 ms baseline)", maxMs)
		}

		if avgMs < 18 || avgMs > 20 {
			t.Fatalf("delay_excess_avg_ms = %d, want ~20", avgMs)
		}
	}
	recs.mu.Unlock()
	if !found {
		t.Fatalf("no summary logged after the 1 s period elapsed")
	}
}

func TestMediaMeter_StallLines_Fetch(t *testing.T) {
	log, recs := newTestLogger()
	mm := newMediaMeter(log, log, "fetch", "tok")

	start := time.Unix(3000, 0)
	mm.onFrameIn(start, 80, uint64(start.UnixNano()))

	mm.tick(start.Add(1200 * time.Millisecond))
	msgs := recs.messages()
	if !containsMsg(msgs, "call media stall start") {
		t.Fatalf("messages = %v, want a stall start", msgs)
	}

	mm.onFrameIn(start.Add(2*time.Second), 80, uint64(start.UnixNano()))
	msgs = recs.messages()
	if !containsMsg(msgs, "call media stall end") {
		t.Fatalf("messages = %v, want a stall end", msgs)
	}
}

func TestMediaMeter_StallLines_Serve(t *testing.T) {
	log, recs := newTestLogger()
	mm := newMediaMeter(log, log, "serve", "tok")

	start := time.Unix(4000, 0)
	mm.onWriteStart(start)

	mm.tick(start.Add(600 * time.Millisecond))
	msgs := recs.messages()
	if !containsMsg(msgs, "call media stall start") {
		t.Fatalf("messages = %v, want a stall start", msgs)
	}

	mm.onWriteDone(start.Add(900*time.Millisecond), 80, 900*time.Millisecond)
	if containsMsg(recs.messages(), "call media stall end") {
		t.Fatalf("messages = %v, want no stall end before onPop", recs.messages())
	}
	mm.onPop(start.Add(900*time.Millisecond), 700, 720, 180)
	msgs = recs.messages()
	if !containsMsg(msgs, "call media stall end") {
		t.Fatalf("messages = %v, want a stall end", msgs)
	}
}

func recAttrs(recs *slogRecords, msg string) []map[string]slog.Value {
	recs.mu.Lock()
	defer recs.mu.Unlock()
	var out []map[string]slog.Value
	for _, r := range recs.out {
		if r.Message != msg {
			continue
		}
		m := map[string]slog.Value{}
		r.Attrs(func(a slog.Attr) bool {
			m[a.Key] = a.Value
			return true
		})
		out = append(out, m)
	}
	return out
}

func attrInt(t *testing.T, a map[string]slog.Value, key string) int64 {
	t.Helper()
	v, ok := a[key]
	if !ok {
		t.Fatalf("attribute %s missing in %v", key, a)
	}
	switch v.Kind() {
	case slog.KindInt64:
		return v.Int64()
	case slog.KindUint64:
		return int64(v.Uint64())
	}
	t.Fatalf("attribute %s = %v, not a number", key, v)
	return 0
}

func TestMediaMeter_ServeSummary_OutDropQueue(t *testing.T) {
	log, recs := newTestLogger()
	mm := newMediaMeter(log, log, "serve", "tok")
	start := time.Unix(10000, 0)
	mm.periodStart = start

	for i := 0; i < 50; i++ {
		mm.onFrameIn(start.Add(time.Duration(i)*20*ms), 80, 0)
	}
	for i := 0; i < 40; i++ {
		mm.onFrameOut()
	}
	mm.onPop(start.Add(300*ms), 300, 320, 180)
	mm.onPop(start.Add(400*ms), 0, 0, 40)
	mm.onPop(start.Add(500*ms), 60, 210, 20)
	mm.tick(start.Add(time.Second))

	for i := 0; i < 50; i++ {
		now := start.Add(time.Second + time.Duration(i)*20*ms)
		mm.onFrameIn(now, 80, 0)
		mm.onPop(now, 0, 0, 0)
		mm.onFrameOut()
	}
	mm.tick(start.Add(2 * time.Second))

	sums := recAttrs(recs, "call media summary")
	if len(sums) != 2 {
		t.Fatalf("%d summaries, want 2", len(sums))
	}
	want := []map[string]int64{
		{"frames_in": 50, "frames_out": 40, "drop_stale_ms": 360, "queue_min_ms": 20, "queue_avg_ms": 80, "queue_max_ms": 180},
		{"frames_in": 50, "frames_out": 50, "drop_stale_ms": 0, "queue_min_ms": 0, "queue_avg_ms": 0, "queue_max_ms": 0},
	}
	for i, w := range want {
		for k, v := range w {
			if got := attrInt(t, sums[i], k); got != v {
				t.Fatalf("summary %d: %s = %d, want %d", i+1, k, got, v)
			}
		}
	}

	drops := recAttrs(recs, "call media drop")
	if len(drops) != 2 {
		t.Fatalf("%d \"call media drop\" lines, want 2", len(drops))
	}
	if got := attrInt(t, drops[1], "dropped_ms"); got != 60 {
		t.Fatalf("second drop line: dropped_ms = %d, want 60", got)
	}
	if got := attrInt(t, drops[1], "since_ms"); got != 1000 {
		t.Fatalf("second drop line: since_ms = %d, want 1000", got)
	}
}

func TestMediaMeter_ServeStallEnd_DroppedMsOfThatStallOnly(t *testing.T) {
	log, recs := newTestLogger()
	mm := newMediaMeter(log, log, "serve", "tok")
	start := time.Unix(11000, 0)
	mm.periodStart = start

	mm.onWriteStart(start)
	mm.tick(start.Add(600 * ms))
	mm.onWriteDone(start.Add(1500*ms), 80, 1500*ms)
	mm.onPop(start.Add(1500*ms), 1300, 1320, 180)

	mm.onPop(start.Add(1510*ms), 40, 220, 0)

	mm.onWriteStart(start.Add(1600 * ms))
	mm.tick(start.Add(2200 * ms))
	mm.onWriteDone(start.Add(2300*ms), 80, 700*ms)
	mm.onPop(start.Add(2300*ms), 500, 520, 100)

	if drops := recAttrs(recs, "call media drop"); len(drops) != 1 || attrInt(t, drops[0], "dropped_ms") != 1300 {
		t.Fatalf("drop lines = %v, want one, dropped_ms=1300", drops)
	}

	ends := recAttrs(recs, "call media stall end")
	if len(ends) != 2 {
		t.Fatalf("%d stall end lines, want 2; messages %v", len(ends), recs.messages())
	}
	for i, w := range []struct{ stallMs, droppedMs int64 }{{1500, 1300}, {700, 500}} {
		if got := attrInt(t, ends[i], "stall_ms"); got != w.stallMs {
			t.Fatalf("stall end %d: stall_ms = %d, want %d", i+1, got, w.stallMs)
		}
		if got := attrInt(t, ends[i], "dropped_ms"); got != w.droppedMs {
			t.Fatalf("stall end %d: dropped_ms = %d, want %d", i+1, got, w.droppedMs)
		}
		if _, ok := ends[i]["reason"]; ok {
			t.Fatalf("stall end %d has a reason: %v", i+1, ends[i])
		}
	}
	sums := recAttrs(recs, "call media summary")
	if len(sums) != 1 {
		t.Fatalf("%d summaries, want 1", len(sums))
	}
	if got := attrInt(t, sums[0], "drop_stale_ms"); got != 1340 {
		t.Fatalf("drop_stale_ms = %d, want 1340 (1300 in the stall + 40 outside it)", got)
	}

	mm.end(start.Add(2400 * ms))
	drops := recAttrs(recs, "call media drop")
	if len(drops) != 2 || attrInt(t, drops[1], "dropped_ms") != 540 {
		t.Fatalf("drop lines after end = %v, want two, the second dropped_ms=540", drops)
	}
}

func TestMediaMeter_DropLine_RateLimit(t *testing.T) {
	log, recs := newTestLogger()
	mm := newMediaMeter(log, log, "serve", "tok")
	start := time.Unix(13000, 0)
	mm.periodStart = start
	mm.startedAt = start

	for k := 0; k <= 18; k++ {
		age := 210
		if k%3 == 1 {
			age = 300
		}
		mm.onPop(start.Add(time.Duration(100+160*k)*ms), 20, age, 0)
	}

	mm.tick(start.Add(3400 * ms))
	mm.onPop(start.Add(3500*ms), 20, 230, 0)
	mm.end(start.Add(3600 * ms))

	want := []struct{ dropped, age, since int64 }{
		{20, 210, 100},
		{140, 300, 1120},
		{140, 300, 1120},
		{80, 300, 1060},
		{20, 230, 200},
	}
	drops := recAttrs(recs, "call media drop")
	if len(drops) != len(want) {
		t.Fatalf("%d drop lines, want %d: %v", len(drops), len(want), drops)
	}
	var lineSum int64
	for i, w := range want {
		got := [3]int64{attrInt(t, drops[i], "dropped_ms"), attrInt(t, drops[i], "age_ms"), attrInt(t, drops[i], "since_ms")}
		if got != [3]int64{w.dropped, w.age, w.since} {
			t.Fatalf("drop line %d: dropped_ms, age_ms, since_ms = %v, want %v", i+1, got, w)
		}
		lineSum += got[0]
	}
	sums := recAttrs(recs, "call media summary")
	if len(sums) != 1 {
		t.Fatalf("%d summaries, want 1", len(sums))
	}
	if got := attrInt(t, sums[0], "drop_stale_ms"); got != 380 {
		t.Fatalf("drop_stale_ms = %d, want 380 (all 19 runs before the tick)", got)
	}
	if lineSum != 400 {
		t.Fatalf("dropped_ms over the lines = %d, want 400 (all 20 runs)", lineSum)
	}
}

func TestMediaMeter_ServeStallEnd_TwoStallsNoPopBetween(t *testing.T) {
	log, recs := newTestLogger()
	mm := newMediaMeter(log, log, "serve", "tok")
	start := time.Unix(14000, 0)
	mm.periodStart = start
	mm.onWriteStart(start)
	mm.tick(start.Add(600 * ms))
	mm.onWriteDone(start.Add(800*ms), 0, 800*ms)
	mm.onWriteStart(start.Add(800 * ms))
	mm.tick(start.Add(1400 * ms))
	mm.onWriteDone(start.Add(1500*ms), 0, 700*ms)
	mm.onPop(start.Add(1500*ms), 600, 620, 50)
	mm.end(start.Add(1600 * ms))

	ends := recAttrs(recs, "call media stall end")
	if len(ends) != 2 {
		t.Fatalf("%d stall end lines, want 2; messages %v", len(ends), recs.messages())
	}
	if got := [2]int64{attrInt(t, ends[0], "stall_ms"), attrInt(t, ends[0], "dropped_ms")}; got != [2]int64{800, 0} {
		t.Fatalf("stall end 1: stall_ms, dropped_ms = %v, want [800 0]", got)
	}
	if got := ends[0]["reason"].String(); got != "next_stall" {
		t.Fatalf("stall end 1: reason = %q, want next_stall", got)
	}
	if got := [2]int64{attrInt(t, ends[1], "stall_ms"), attrInt(t, ends[1], "dropped_ms")}; got != [2]int64{700, 600} {
		t.Fatalf("stall end 2: stall_ms, dropped_ms = %v, want [700 600]", got)
	}
	if _, ok := ends[1]["reason"]; ok {
		t.Fatalf("stall end 2 has a reason: %v", ends[1])
	}
	if got := attrInt(t, recAttrs(recs, "call media end")[0], "stalls"); got != 2 {
		t.Fatalf("call media end stalls = %d, want 2", got)
	}
}

func TestMediaMeter_ServeStallEnd_NoPopThenEnd(t *testing.T) {
	log, recs := newTestLogger()
	mm := newMediaMeter(log, log, "serve", "tok")
	start := time.Unix(12000, 0)
	mm.onWriteStart(start)
	mm.tick(start.Add(600 * ms))
	mm.onWriteDone(start.Add(800*ms), 0, 800*ms)
	mm.end(start.Add(900 * ms))
	ends := recAttrs(recs, "call media stall end")
	if len(ends) != 1 {
		t.Fatalf("%d stall end lines, want 1; messages %v", len(ends), recs.messages())
	}
	if got := attrInt(t, ends[0], "stall_ms"); got != 800 {
		t.Fatalf("stall_ms = %d, want 800", got)
	}
	if got := attrInt(t, ends[0], "dropped_ms"); got != 0 {
		t.Fatalf("dropped_ms = %d, want 0", got)
	}
	if got := ends[0]["reason"].String(); got != "stream_end" {
		t.Fatalf("reason = %q, want stream_end", got)
	}
	msgs := recs.messages()
	if msgs[len(msgs)-1] != "call media end" {
		t.Fatalf("messages = %v, want call media end last", msgs)
	}
}

func TestMediaMeter_ParseErrorDisablesStallDetection(t *testing.T) {
	t.Run("fetch", func(t *testing.T) {
		log, recs := newTestLogger()
		mm := newMediaMeter(log, log, "fetch", "tok")
		start := time.Unix(9000, 0)
		mm.onFrameIn(start, 80, uint64(start.UnixNano()))
		mm.onParseError(errShortFrameBody)

		mm.tick(start.Add(5 * time.Second))
		if containsMsg(recs.messages(), "call media stall start") {
			t.Fatalf("messages = %v, want no stall after a parse error", recs.messages())
		}
	})
	t.Run("serve", func(t *testing.T) {
		log, recs := newTestLogger()
		mm := newMediaMeter(log, log, "serve", "tok")
		start := time.Unix(9000, 0)
		mm.onWriteStart(start)
		mm.onParseError(errShortFrameBody)

		mm.tick(start.Add(5 * time.Second))
		if containsMsg(recs.messages(), "call media stall start") {
			t.Fatalf("messages = %v, want no stall after a parse error", recs.messages())
		}
	})
}

func TestMediaMeter_NoStallBeforeFirstFrame(t *testing.T) {
	log, recs := newTestLogger()
	mm := newMediaMeter(log, log, "fetch", "tok")
	start := time.Unix(5000, 0)
	mm.periodStart = start

	mm.tick(start.Add(10 * time.Second))
	if containsMsg(recs.messages(), "call media stall start") {
		t.Fatalf("messages = %v, want no stall before the first frame", recs.messages())
	}

	mm.onFrameIn(start.Add(10*time.Second), 80, uint64(start.Add(10*time.Second).UnixNano()))
	mm.tick(start.Add(12 * time.Second))
	if !containsMsg(recs.messages(), "call media stall start") {
		t.Fatalf("messages = %v, want a stall start once a first frame set lastFrameAt", recs.messages())
	}
}

func TestMediaMeter_EndClosesOpenFetchStall(t *testing.T) {
	log, recs := newTestLogger()
	mm := newMediaMeter(log, log, "fetch", "tok")
	start := time.Unix(6000, 0)
	mm.onFrameIn(start, 80, uint64(start.UnixNano()))

	mm.tick(start.Add(1500 * time.Millisecond))
	if !containsMsg(recs.messages(), "call media stall start") {
		t.Fatalf("messages = %v, want a stall start", recs.messages())
	}

	mm.end(start.Add(2500 * time.Millisecond))
	found := false
	recs.mu.Lock()
	for _, r := range recs.out {
		if r.Message != "call media stall end" {
			continue
		}
		var reason string
		var stallMs int64
		r.Attrs(func(a slog.Attr) bool {
			switch a.Key {
			case "reason":
				reason = a.Value.String()
			case "stall_ms":
				stallMs = a.Value.Int64()
			}
			return true
		})
		if reason == "stream_end" {
			found = true

			if stallMs < 2400 || stallMs > 2600 {
				t.Fatalf("stall_ms = %d, want ~2500", stallMs)
			}
		}
	}
	recs.mu.Unlock()
	if !found {
		t.Fatalf("messages = %v, want a stall end with reason=stream_end", recs.messages())
	}

	recs.mu.Lock()
	for _, r := range recs.out {
		if r.Message != "call media end" {
			continue
		}
		var stalls int64
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "stalls" {
				stalls = a.Value.Int64()
			}
			return true
		})
		if stalls != 1 {
			t.Fatalf("call media end stalls = %d, want 1", stalls)
		}
	}
	recs.mu.Unlock()
}

func TestMediaMeter_StatsLogNil_NoSummary(t *testing.T) {
	t.Run("fetch", func(t *testing.T) {
		log, recs := newTestLogger()
		mm := newMediaMeter(log, nil, "fetch", "tok")
		start := time.Unix(11000, 0)
		mm.periodStart = start
		mm.onFrameIn(start, 80, uint64(start.UnixNano()))

		mm.tick(start.Add(1200 * time.Millisecond))
		if !containsMsg(recs.messages(), "call media stall start") {
			t.Fatalf("messages = %v, want a stall start", recs.messages())
		}
		if containsMsg(recs.messages(), "call media summary") {
			t.Fatalf("messages = %v, want no summary with statsLog nil", recs.messages())
		}
	})
	t.Run("serve", func(t *testing.T) {
		log, recs := newTestLogger()
		mm := newMediaMeter(log, nil, "serve", "tok")
		start := time.Unix(12000, 0)
		mm.periodStart = start

		mm.tick(start.Add(1200 * time.Millisecond))
		if containsMsg(recs.messages(), "call media summary") {
			t.Fatalf("messages = %v, want no summary with statsLog nil", recs.messages())
		}
	})
}

func TestMediaMeter_StatsLogDistinct_SummaryGoesThere(t *testing.T) {
	log, logRecs := newTestLogger()
	statsLog, statsRecs := newTestLogger()
	mm := newMediaMeter(log, statsLog, "fetch", "tok")
	start := time.Unix(13000, 0)
	mm.periodStart = start

	mm.onFrameIn(start, 80, uint64(start.UnixNano()))

	mm.tick(start.Add(1200 * time.Millisecond))
	if !containsMsg(logRecs.messages(), "call media stall start") {
		t.Fatalf("log messages = %v, want a stall start", logRecs.messages())
	}
	if containsMsg(statsRecs.messages(), "call media stall start") {
		t.Fatalf("statsLog messages = %v, want no stall start", statsRecs.messages())
	}

	if !containsMsg(statsRecs.messages(), "call media summary") {
		t.Fatalf("statsLog messages = %v, want a summary", statsRecs.messages())
	}
	if containsMsg(logRecs.messages(), "call media summary") {
		t.Fatalf("log messages = %v, want no summary", logRecs.messages())
	}

	mm.end(start.Add(1500 * time.Millisecond))
	if !containsMsg(logRecs.messages(), "call media end") {
		t.Fatalf("log messages = %v, want call media end", logRecs.messages())
	}
	if containsMsg(statsRecs.messages(), "call media end") {
		t.Fatalf("statsLog messages = %v, want no call media end", statsRecs.messages())
	}
}

func TestByteMeter_StatsLogNilVsDistinct(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		log, recs := newTestLogger()
		bm := newByteMeter(log, nil, "fetch", "tok", ModalityVideo)
		start := time.Unix(14000, 0)
		bm.periodStart = start
		bm.onBytes(100)

		bm.tick(start.Add(1200 * time.Millisecond))
		if containsMsg(recs.messages(), "call media summary") {
			t.Fatalf("messages = %v, want no summary with statsLog nil", recs.messages())
		}

		bm.end(start.Add(1500 * time.Millisecond))
		if !containsMsg(recs.messages(), "call media end") {
			t.Fatalf("messages = %v, want call media end", recs.messages())
		}
	})
	t.Run("distinct", func(t *testing.T) {
		log, logRecs := newTestLogger()
		statsLog, statsRecs := newTestLogger()
		bm := newByteMeter(log, statsLog, "serve", "tok", ModalityScreen)
		start := time.Unix(15000, 0)
		bm.periodStart = start
		bm.onBytes(100)

		bm.tick(start.Add(1200 * time.Millisecond))
		if !containsMsg(statsRecs.messages(), "call media summary") {
			t.Fatalf("statsLog messages = %v, want a summary", statsRecs.messages())
		}
		if containsMsg(logRecs.messages(), "call media summary") {
			t.Fatalf("log messages = %v, want no summary", logRecs.messages())
		}

		bm.end(start.Add(1500 * time.Millisecond))
		if !containsMsg(logRecs.messages(), "call media end") {
			t.Fatalf("log messages = %v, want call media end", logRecs.messages())
		}
		if containsMsg(statsRecs.messages(), "call media end") {
			t.Fatalf("statsLog messages = %v, want no call media end", statsRecs.messages())
		}
	})
}

func containsMsg(msgs []string, want string) bool {
	for _, m := range msgs {
		if m == want {
			return true
		}
	}
	return false
}

func manyFrames(n int) []byte {
	var out []byte
	for i := 0; i < n; i++ {
		out = append(out, buildFrame(uint64(i), uint64(i)*20_000_000, []byte(fmt.Sprintf("payload-%03d", i)))...)
	}
	return out
}

func TestCopyFramesServe_ManyFramesSplitIntoChunks_ByteExact(t *testing.T) {
	want := manyFrames(25)
	log, _ := newTestLogger()
	mm := newMediaMeter(log, log, "serve", "tok")
	var dst bytes.Buffer
	n, err := copyFramesServe(&dst, &chunkedReader{data: want, n: 3}, mm)
	if err != nil {
		t.Fatalf("copyFramesServe: %v", err)
	}
	if n != int64(len(want)) {
		t.Fatalf("n = %d, want %d", n, len(want))
	}
	if !bytes.Equal(dst.Bytes(), want) {
		t.Fatalf("dst != want (len %d vs %d)", dst.Len(), len(want))
	}
}

func TestCopyFramesFetch_ManyFramesSplitIntoChunks_ByteExact(t *testing.T) {
	want := manyFrames(25)
	log, _ := newTestLogger()
	mm := newMediaMeter(log, log, "fetch", "tok")
	var dst bytes.Buffer
	n, err := copyFramesFetch(&dst, &chunkedReader{data: want, n: 3}, mm)
	if err != nil {
		t.Fatalf("copyFramesFetch: %v", err)
	}
	if n != int64(len(want)) {
		t.Fatalf("n = %d, want %d", n, len(want))
	}
	if !bytes.Equal(dst.Bytes(), want) {
		t.Fatalf("dst != want (len %d vs %d)", dst.Len(), len(want))
	}
}

func TestCopyFramesServe_ParseErrorReplaysConsumedBytes_ByteExact(t *testing.T) {
	good := buildFrame(1, 1000, []byte("ok"))
	badHdr := []byte{0x00, 0x01}
	rest := []byte("raw-tail-bytes-after-the-bad-header")
	stream := append(append(append([]byte{}, good...), badHdr...), rest...)

	log, _ := newTestLogger()
	mm := newMediaMeter(log, log, "serve", "tok")
	var dst bytes.Buffer
	n, err := copyFramesServe(&dst, bytes.NewReader(stream), mm)
	if err != nil {
		t.Fatalf("copyFramesServe: %v", err)
	}
	if n != int64(len(stream)) {
		t.Fatalf("n = %d, want %d (every byte, including the bad header)", n, len(stream))
	}
	if !bytes.Equal(dst.Bytes(), stream) {
		t.Fatalf("dst != the original stream: got %x, want %x", dst.Bytes(), stream)
	}
}

func TestCopyFramesFetch_ParseErrorReplaysConsumedBytes_ByteExact(t *testing.T) {
	good := buildFrame(1, 1000, []byte("ok"))
	badHdr := []byte{0x00, 0x01}
	rest := []byte("raw-tail-bytes-after-the-bad-header")
	stream := append(append(append([]byte{}, good...), badHdr...), rest...)

	log, _ := newTestLogger()
	mm := newMediaMeter(log, log, "fetch", "tok")
	var dst bytes.Buffer
	n, err := copyFramesFetch(&dst, bytes.NewReader(stream), mm)
	if err != nil {
		t.Fatalf("copyFramesFetch: %v", err)
	}
	if n != int64(len(stream)) {
		t.Fatalf("n = %d, want %d", n, len(stream))
	}
	if !bytes.Equal(dst.Bytes(), stream) {
		t.Fatalf("dst != the original stream: got %x, want %x", dst.Bytes(), stream)
	}
}
