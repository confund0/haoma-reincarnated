package proxy

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

const ms = time.Millisecond

var queueTestBase = time.Unix(1_700_000_000, 0)

func qf(counter uint64, atOff time.Duration, pts uint64) queuedFrame {
	return queuedFrame{
		b:       buildFrame(counter, pts, []byte(fmt.Sprintf("p%03d", counter))),
		at:      queueTestBase.Add(atOff),
		counter: counter,
		pts:     pts,
	}
}

func TestFrameQueue_DropsStaleKeepsOrderWholeFrames(t *testing.T) {
	q := newFrameQueue()
	var in []queuedFrame
	for i := uint64(0); i < 10; i++ {
		f := qf(i, time.Duration(i)*20*ms, i*uint64(20*ms))
		in = append(in, f)
		q.push(f)
	}

	now := queueTestBase.Add(300 * ms)
	for want := uint64(5); want < 10; want++ {
		out, droppedMs, ageMs, ok := q.popBatch(now, 200*ms, 0, 1)
		if !ok || len(out) != 1 {
			t.Fatalf("pop for counter %d: ok=%v len=%d, want one frame", want, ok, len(out))
		}
		if out[0].counter != want {
			t.Fatalf("got counter %d, want %d (order)", out[0].counter, want)
		}
		if !bytes.Equal(out[0].b, in[want].b) {
			t.Fatalf("counter %d: bytes %x, want the whole frame %x", want, out[0].b, in[want].b)
		}
		wantDropped, wantAge := 0, 0
		if want == 5 {
			wantDropped, wantAge = 100, 300
		}
		if droppedMs != wantDropped || ageMs != wantAge {
			t.Fatalf("counter %d: droppedMs=%d ageMs=%d, want %d, %d", want, droppedMs, ageMs, wantDropped, wantAge)
		}
	}
	q.close()
	if out, _, _, ok := q.popBatch(now, 200*ms, 0, 1); ok || out != nil {
		t.Fatalf("after drain: ok=%v out=%v, want ok=false, no frames", ok, out)
	}
}

func TestFrameQueue_DroppedMsAgeMs_ExactFormula(t *testing.T) {
	const p0 = uint64(10_000_000_000)
	q := newFrameQueue()
	q.push(qf(1, 0, p0))
	q.push(qf(2, 10*ms, p0+uint64(40*ms)))
	q.push(qf(3, 500*ms, p0+uint64(1_234_900_000)))
	out, droppedMs, ageMs, ok := q.popBatch(queueTestBase.Add(650*ms), 200*ms, 0, 1)
	if !ok || len(out) != 1 || out[0].counter != 3 {
		t.Fatalf("ok=%v out=%v, want frame 3", ok, out)
	}
	if droppedMs != 1234 {
		t.Fatalf("droppedMs = %d, want 1234 (pts span from frame 1, truncated)", droppedMs)
	}
	if ageMs != 650 {
		t.Fatalf("ageMs = %d, want 650 (age of frame 1, the first dropped)", ageMs)
	}
}

func TestFrameQueue_DropRunSpansAWait(t *testing.T) {
	const p0 = uint64(50_000_000_000)
	q := newFrameQueue()
	waiting := make(chan struct{}, 4)
	q.waitHook = func() { waiting <- struct{}{} }

	q.push(qf(1, 0, p0))

	type result struct {
		out              []queuedFrame
		droppedMs, ageMs int
		ok               bool
	}
	res := make(chan result, 1)
	now := queueTestBase.Add(1000 * ms)
	go func() {
		out, d, a, ok := q.popBatch(now, 200*ms, 0, 1)
		res <- result{out, d, a, ok}
	}()

	recvWait := func() {
		t.Helper()
		select {
		case <-waiting:
		case <-time.After(2 * time.Second):
			t.Fatalf("popBatch never blocked")
		}
	}

	recvWait()
	q.push(qf(2, 100*ms, p0+uint64(100*ms)))
	recvWait()
	q.push(qf(3, 1000*ms, p0+uint64(900*ms)))

	var r result
	select {
	case r = <-res:
	case <-time.After(2 * time.Second):
		t.Fatalf("popBatch never returned")
	}
	if !r.ok || len(r.out) != 1 || r.out[0].counter != 3 {
		t.Fatalf("ok=%v out=%v, want frame 3", r.ok, r.out)
	}
	if r.droppedMs != 900 || r.ageMs != 1000 {
		t.Fatalf("droppedMs=%d ageMs=%d, want 900, 1000 (measured from frame 1, before both waits)", r.droppedMs, r.ageMs)
	}
}

func TestFrameQueue_CapSpanEvicts(t *testing.T) {
	q := newFrameQueue()
	const n = 250
	maxLen := int(queueCapSpan/(20*ms)) + 1
	for i := uint64(0); i < n; i++ {
		q.push(qf(i, time.Duration(i)*20*ms, i*uint64(20*ms)))
		q.mu.Lock()
		l := len(q.frames)
		span := q.frames[l-1].at.Sub(q.frames[0].at)
		q.mu.Unlock()
		if l > maxLen || span > queueCapSpan {
			t.Fatalf("after push %d: len=%d span=%v, want len<=%d span<=%v", i, l, span, maxLen, queueCapSpan)
		}
	}
	q.mu.Lock()
	oldest := q.frames[0].counter
	q.mu.Unlock()
	if oldest != 149 {
		t.Fatalf("oldest kept counter = %d, want 149", oldest)
	}
	now := queueTestBase.Add(4980 * ms)
	out, droppedMs, ageMs, ok := q.popBatch(now, 200*ms, 0, 1)
	if !ok || len(out) != 1 || out[0].counter != 239 {
		t.Fatalf("ok=%v out=%v, want frame 239", ok, out)
	}
	if droppedMs != 4780 || ageMs != 4980 {
		t.Fatalf("droppedMs=%d ageMs=%d, want 4780, 4980 (run starts at evicted frame 0)", droppedMs, ageMs)
	}

	if _, d, a, _ := q.popBatch(now, 200*ms, 0, 1); d != 0 || a != 0 {
		t.Fatalf("second pop: droppedMs=%d ageMs=%d, want 0, 0", d, a)
	}
}

func TestFrameQueue_Closed(t *testing.T) {
	now := queueTestBase.Add(1000 * ms)

	q := newFrameQueue()
	q.close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if out, d, a, ok := q.popBatch(now, 200*ms, 0, 1); ok || out != nil || d != 0 || a != 0 {
			t.Errorf("closed empty: out=%v d=%d a=%d ok=%v, want nothing, ok=false", out, d, a, ok)
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("popBatch on a closed empty queue blocked")
	}

	q = newFrameQueue()
	q.push(qf(1, 900*ms, 1))
	q.push(qf(2, 920*ms, 2))
	q.close()
	for _, want := range []uint64{1, 2} {
		out, _, _, ok := q.popBatch(now, 200*ms, 0, 1)
		if !ok || len(out) != 1 || out[0].counter != want {
			t.Fatalf("closed non-empty: ok=%v out=%v, want frame %d", ok, out, want)
		}
	}
	if _, _, _, ok := q.popBatch(now, 200*ms, 0, 1); ok {
		t.Fatalf("closed, drained: ok=true, want false")
	}

	q = newFrameQueue()
	q.push(qf(1, 0, 1))
	q.close()
	if out, d, a, ok := q.popBatch(now, 200*ms, 0, 1); ok || out != nil || d != 0 || a != 0 {
		t.Fatalf("closed, only stale: out=%v d=%d a=%d ok=%v, want nothing, ok=false", out, d, a, ok)
	}
	if c, r := q.rawTail(); c != nil || r != nil {
		t.Fatalf("rawTail after close = %v, %v, want nil, nil", c, r)
	}
}

type timedRecord struct {
	at      time.Time
	counter uint64
	pts     uint64
	b       []byte
}

type recordingWriter struct {
	firstDelay time.Duration
	mu         sync.Mutex
	writes     []timedRecord
	all        bytes.Buffer
}

func (w *recordingWriter) Write(p []byte) (int, error) {
	start := time.Now()
	w.mu.Lock()
	first := len(w.writes) == 0
	w.mu.Unlock()
	if first && w.firstDelay > 0 {
		time.Sleep(w.firstDelay)
	}
	rec := timedRecord{at: start, b: append([]byte(nil), p...)}
	if len(p) >= frameHeaderLen {
		rec.counter = binary.BigEndian.Uint64(p[2:10])
		rec.pts = binary.BigEndian.Uint64(p[10:18])
	}
	w.mu.Lock()
	w.writes = append(w.writes, rec)
	w.all.Write(p)
	w.mu.Unlock()
	return len(p), nil
}

func countRecs(recs *slogRecords, msg string) int {
	return countMsg(recs.messages(), msg)
}

func runSlowServe(t *testing.T) (*recordingWriter, []byte, *slogRecords) {
	t.Helper()
	log, recs := newTestLogger()
	mm := newMediaMeter(log, nil, "serve", "tok")
	pr, pw := io.Pipe()
	var frames [][]byte
	var sent []byte
	for i := uint64(0); i < 30; i++ {
		f := buildFrame(i, i*uint64(20*ms), []byte(fmt.Sprintf("payload-%03d", i)))
		frames = append(frames, f)
		sent = append(sent, f...)
	}
	go func() {
		for _, f := range frames {
			if _, err := pw.Write(f); err != nil {
				return
			}
			time.Sleep(20 * ms)
		}
		pw.Close()
	}()
	dst := &recordingWriter{firstDelay: 500 * ms}
	if _, err := copyFramesServe(dst, pr, mm); err != nil {
		t.Fatalf("copyFramesServe: %v", err)
	}
	mm.end(time.Now())
	return dst, sent, recs
}

func TestCopyFramesServe_SlowWriter_DropsStale(t *testing.T) {
	dst, _, recs := runSlowServe(t)
	if len(dst.writes) >= 30 {
		t.Fatalf("dst got %d frames, want fewer than 30 (stale ones dropped)", len(dst.writes))
	}
	if len(dst.writes) < 2 || dst.writes[1].counter <= 1 {
		t.Fatalf("first frame after the stall: %+v, want a counter past 1", dst.writes)
	}
	for i := 1; i < len(dst.writes); i++ {
		if dst.writes[i].counter <= dst.writes[i-1].counter {
			t.Fatalf("counters not rising: %d after %d", dst.writes[i].counter, dst.writes[i-1].counter)
		}
	}
	if countRecs(recs, "call media drop") == 0 {
		t.Fatalf("no \"call media drop\" line; got %v", recs.messages())
	}
}

func TestCopyFramesServe_HugeMaxAge_NoDrop(t *testing.T) {
	saved := staleFrameMaxAge
	staleFrameMaxAge = time.Hour
	defer func() { staleFrameMaxAge = saved }()

	dst, sent, recs := runSlowServe(t)
	if !bytes.Equal(dst.all.Bytes(), sent) {
		t.Fatalf("dst got %d bytes in %d writes, want all %d bytes", dst.all.Len(), len(dst.writes), len(sent))
	}
	if n := countRecs(recs, "call media drop"); n != 0 {
		t.Fatalf("%d \"call media drop\" lines, want 0", n)
	}
}

func TestCopyFramesServe_ParseErrorTail_SlowWriter_ByteExact(t *testing.T) {
	good := buildFrame(1, 1000, []byte("ok"))
	badHdr := []byte{0x00, 0x01}
	rest := bytes.Repeat([]byte("raw-tail-bytes-after-the-bad-header|"), 200)
	stream := append(append(append([]byte{}, good...), badHdr...), rest...)

	log, recs := newTestLogger()
	mm := newMediaMeter(log, nil, "serve", "tok")
	dst := &recordingWriter{firstDelay: 300 * ms}
	n, err := copyFramesServe(dst, bytes.NewReader(stream), mm)
	if err != nil {
		t.Fatalf("copyFramesServe: %v", err)
	}
	if n != int64(len(stream)) || !bytes.Equal(dst.all.Bytes(), stream) {
		t.Fatalf("n=%d, dst %d bytes; want every one of %d bytes, in order", n, dst.all.Len(), len(stream))
	}
	if countRecs(recs, "call media parse error") != 1 {
		t.Fatalf("want one \"call media parse error\" line, got %v", recs.messages())
	}

	mm.mu.Lock()
	out := mm.framesOutInPeriod
	mm.mu.Unlock()
	if out != 1 {
		t.Fatalf("frames_out = %d, want 1 (the raw tail is not counted)", out)
	}
}

func runServeWithStats(t *testing.T, firstDelay time.Duration, n int) *slogRecords {
	t.Helper()
	log, recs := newTestLogger()
	mm := newMediaMeter(log, log, "serve", "tok")
	meterStop := make(chan struct{})
	go runMeterTicker(mm, meterStop)
	pr, pw := io.Pipe()
	go func() {
		for i := 0; i < n; i++ {
			f := buildFrame(uint64(i), uint64(i)*uint64(20*ms), []byte(fmt.Sprintf("payload-%03d", i)))
			if _, err := pw.Write(f); err != nil {
				return
			}
			time.Sleep(20 * ms)
		}
		pw.Close()
	}()
	dst := &recordingWriter{firstDelay: firstDelay}
	_, err := copyFramesServe(dst, pr, mm)
	close(meterStop)
	if err != nil {
		t.Fatalf("copyFramesServe: %v", err)
	}
	mm.end(time.Now())
	return recs
}

func TestCopyFramesServe_Stall_SummaryAndStallEndShowTheDrop(t *testing.T) {
	recs := runServeWithStats(t, 2*time.Second, 225)

	drops := recAttrs(recs, "call media drop")
	if len(drops) == 0 {
		t.Fatalf("no \"call media drop\" line; messages %v", recs.messages())
	}
	var dropLinesMs int64
	for _, d := range drops {
		dropLinesMs += attrInt(t, d, "dropped_ms")
	}
	firstDropMs := attrInt(t, drops[0], "dropped_ms")
	if firstDropMs < 1500 || firstDropMs > 1900 {
		t.Fatalf("first drop run dropped_ms = %d, want 1500-1900 (2 s stall minus the 200 ms staleFrameMaxAge)", firstDropMs)
	}

	ends := recAttrs(recs, "call media stall end")
	if len(ends) != 1 {
		t.Fatalf("%d stall end lines, want 1; messages %v", len(ends), recs.messages())
	}
	if got := attrInt(t, ends[0], "dropped_ms"); got != firstDropMs {
		t.Fatalf("stall end dropped_ms = %d, want %d (the first drop run after the stall)", got, firstDropMs)
	}
	if got := attrInt(t, ends[0], "stall_ms"); got < 1950 {
		t.Fatalf("stall end stall_ms = %d, want >= 1950", got)
	}

	sums := recAttrs(recs, "call media summary")
	if len(sums) < 4 {
		t.Fatalf("%d summaries, want at least 4", len(sums))
	}
	first := sums[0]
	if got := attrInt(t, first, "frames_in"); got < 40 {
		t.Fatalf("summary 1: frames_in = %d, want ~50", got)
	}
	for _, k := range []string{"frames_out", "drop_stale_ms", "queue_min_ms", "queue_avg_ms", "queue_max_ms"} {
		if got := attrInt(t, first, k); got != 0 {
			t.Fatalf("summary 1 (inside the stall): %s = %d, want 0", k, got)
		}
	}

	var sumDropMs, sumIn, sumOut int64
	dropPeriods := 0
	for i, s := range sums {
		t.Logf("summary %d: %v", i+1, s)
		d := attrInt(t, s, "drop_stale_ms")
		sumDropMs += d
		sumIn += attrInt(t, s, "frames_in")
		sumOut += attrInt(t, s, "frames_out")
		if d < 1000 {
			continue
		}
		dropPeriods++

		if qmax := attrInt(t, s, "queue_max_ms"); qmax <= 0 || qmax > 250 {
			t.Fatalf("summary %d (the drop): queue_max_ms = %d, want 1-250 (the newest frames left after the drop)", i+1, qmax)
		}
	}
	if dropPeriods != 1 {
		t.Fatalf("%d summaries with drop_stale_ms >= 1000, want 1; summaries %v", dropPeriods, sums)
	}
	if sumDropMs != dropLinesMs {
		t.Fatalf("drop_stale_ms over the summaries = %d, dropped_ms over the drop lines = %d, want equal", sumDropMs, dropLinesMs)
	}

	droppedFrames := dropLinesMs / 20
	if gap := sumIn - sumOut; gap < droppedFrames-2 || gap > droppedFrames+2 {
		t.Fatalf("frames_in - frames_out over the summaries = %d - %d = %d, want %d (the frames dropped) +-2", sumIn, sumOut, gap, droppedFrames)
	}

	last := sums[len(sums)-1]
	if got := attrInt(t, last, "drop_stale_ms"); got != 0 {
		t.Fatalf("last summary (caught up): drop_stale_ms = %d, want 0", got)
	}
	if got := attrInt(t, last, "queue_max_ms"); got > 40 {
		t.Fatalf("last summary (caught up): queue_max_ms = %d, want <= 40", got)
	}
	in, out := attrInt(t, last, "frames_in"), attrInt(t, last, "frames_out")
	if in < 40 || out < in-1 || out > in+1 {
		t.Fatalf("last summary (caught up): frames_in = %d, frames_out = %d, want ~50 and within 1", in, out)
	}
}

func TestCopyFramesServe_NoStall_SummaryOutEqualsIn(t *testing.T) {
	recs := runServeWithStats(t, 0, 110)
	sums := recAttrs(recs, "call media summary")
	if len(sums) < 2 {
		t.Fatalf("%d summaries, want at least 2", len(sums))
	}
	for i, s := range sums {
		in, out := attrInt(t, s, "frames_in"), attrInt(t, s, "frames_out")
		if in < 40 || out < in-1 || out > in+1 {
			t.Fatalf("summary %d: frames_in = %d, frames_out = %d, want ~50 and within 1", i+1, in, out)
		}
		if got := attrInt(t, s, "drop_stale_ms"); got != 0 {
			t.Fatalf("summary %d: drop_stale_ms = %d, want 0", i+1, got)
		}
		if got := attrInt(t, s, "queue_max_ms"); got > 5 {
			t.Fatalf("summary %d: queue_max_ms = %d, want ~0", i+1, got)
		}
	}
	for _, msg := range []string{"call media drop", "call media stall start", "call media stall end"} {
		if n := countRecs(recs, msg); n != 0 {
			t.Fatalf("%d %q lines, want 0", n, msg)
		}
	}
}

type failingWriter struct{}

var errTestWrite = errors.New("test: write failed")

func (failingWriter) Write([]byte) (int, error) { return 0, errTestWrite }

type slowSecondReader struct {
	parts [][]byte
	pause time.Duration
	reads int
}

func (r *slowSecondReader) Read(p []byte) (int, error) {
	r.reads++
	if r.reads == 2 {
		time.Sleep(r.pause)
	}
	if len(r.parts) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.parts[0])
	r.parts = r.parts[1:]
	return n, nil
}

func TestCopyFramesServe_NoMeterCallAfterReturn(t *testing.T) {
	log, _ := newTestLogger()
	mm := newMediaMeter(log, nil, "serve", "tok")
	src := &slowSecondReader{
		parts: [][]byte{buildFrame(0, 0, []byte("a")), buildFrame(1, 1, []byte("b"))},
		pause: 100 * ms,
	}
	_, err := copyFramesServe(failingWriter{}, src, mm)
	if !errors.Is(err, errTestWrite) {
		t.Fatalf("err = %v, want the write error", err)
	}
	mm.end(time.Now())
	frames := func() uint64 {
		mm.mu.Lock()
		defer mm.mu.Unlock()
		return mm.framesTotal
	}
	atEnd := frames()
	time.Sleep(250 * ms)
	if later := frames(); later != atEnd || atEnd != 2 {
		t.Fatalf("frames_in at end() = %d, %d later; want 2 both times (no mm call after return)", atEnd, later)
	}
}

func TestCopyFramesServe_WriteErrorClosesSrc(t *testing.T) {
	log, _ := newTestLogger()
	mm := newMediaMeter(log, nil, "serve", "tok")
	local, remote := net.Pipe()
	defer remote.Close()
	go func() { _, _ = remote.Write(buildFrame(0, 0, []byte("a"))) }()
	done := make(chan error, 1)
	go func() {
		_, err := copyFramesServe(failingWriter{}, local, mm)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, errTestWrite) {
			t.Fatalf("err = %v, want the write error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("copyFramesServe hung after a write error with src still open")
	}
}
