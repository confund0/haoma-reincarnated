package proxy

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type testStreamer struct {
	ln    net.Listener
	port  int
	conns chan net.Conn
}

func startTestStreamer(t *testing.T) *testStreamer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &testStreamer{ln: ln, port: ln.Addr().(*net.TCPAddr).Port, conns: make(chan net.Conn, 4)}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.conns <- c
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *testStreamer) accept(t *testing.T) net.Conn {
	t.Helper()
	select {
	case c := <-s.conns:
		t.Cleanup(func() { _ = c.Close() })
		return c
	case <-time.After(3 * time.Second):
		t.Fatalf("streamer on port %d: haomad never dialed", s.port)
	}
	return nil
}

type micSender struct {
	mu  sync.Mutex
	pts []uint64
}

const micFrameEvery = 20 * time.Millisecond

func (s *micSender) run(conn net.Conn, payload []byte, stopAt time.Time) {
	defer conn.Close()
	start := time.Now()
	for i := uint64(0); ; i++ {
		next := start.Add(time.Duration(i) * micFrameEvery)
		if !next.Before(stopAt) {
			return
		}
		if d := time.Until(next); d > 0 {
			time.Sleep(d)
		}
		pts := uint64(time.Now().UnixNano())
		s.mu.Lock()
		s.pts = append(s.pts, pts)
		s.mu.Unlock()
		if _, err := conn.Write(buildFrame(i, pts, payload)); err != nil {
			return
		}
	}
}

func (s *micSender) sent() []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uint64(nil), s.pts...)
}

type tokenRec struct {
	msg   string
	at    time.Time
	attrs map[string]slog.Value
}

func recsForToken(recs *slogRecords, short string) []tokenRec {
	recs.mu.Lock()
	defer recs.mu.Unlock()
	var out []tokenRec
	for _, r := range recs.out {
		m := map[string]slog.Value{}
		r.Attrs(func(a slog.Attr) bool {
			m[a.Key] = a.Value
			return true
		})
		if v, ok := m["token"]; !ok || v.String() != short {
			continue
		}
		out = append(out, tokenRec{msg: r.Message, at: r.Time, attrs: m})
	}
	return out
}

func filterRecs(rs []tokenRec, msg string) []tokenRec {
	var out []tokenRec
	for _, r := range rs {
		if r.msg == msg {
			out = append(out, r)
		}
	}
	return out
}

func waitForRecord(t *testing.T, recs *slogRecords, msg, short string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if len(filterRecs(recsForToken(recs, short), msg)) > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no %q record for %s after %v; messages %v", msg, short, d, recs.messages())
}

type gateWrite struct {
	at      time.Time
	counter uint64
	pts     uint64
	n       int
	err     error
}

var errGateClosed = errors.New("test: write after Close")

type gateWriter struct {
	w              io.Writer
	failAfterClose bool
	gate           chan struct{}
	entered        chan struct{}
	done           chan struct{}
	releaseOnce    sync.Once
	closeOnce      sync.Once

	mu     sync.Mutex
	calls  int
	closed bool
	writes []gateWrite
}

func newGateWriter(failAfterClose bool) *gateWriter {
	return &gateWriter{
		failAfterClose: failAfterClose,
		gate:           make(chan struct{}),
		entered:        make(chan struct{}),
		done:           make(chan struct{}),
	}
}

func (g *gateWriter) release() { g.releaseOnce.Do(func() { close(g.gate) }) }

func (g *gateWriter) Close() error {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
	g.closeOnce.Do(func() { close(g.done) })
	return nil
}

func (g *gateWriter) Write(p []byte) (int, error) {
	start := time.Now()
	g.mu.Lock()
	first := g.calls == 0
	g.calls++
	g.mu.Unlock()
	if first {
		close(g.entered)
		select {
		case <-g.gate:
		case <-g.done:
		}
	}
	g.mu.Lock()
	fail := g.failAfterClose && g.closed
	g.mu.Unlock()
	rec := gateWrite{at: start}
	if len(p) >= frameHeaderLen {
		rec.counter = binary.BigEndian.Uint64(p[2:10])
		rec.pts = binary.BigEndian.Uint64(p[10:18])
	}
	if fail {
		rec.err = errGateClosed
	} else {
		rec.n, rec.err = g.w.Write(p)
	}
	g.mu.Lock()
	g.writes = append(g.writes, rec)
	g.mu.Unlock()
	return rec.n, rec.err
}

func (g *gateWriter) snapshot() []gateWrite {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]gateWrite(nil), g.writes...)
}

type cancelRaceMode int

const (
	cancelThenDrain cancelRaceMode = iota

	cancelRacesDrain

	cancelThenWriteFails
)

const raceToken = "racetest-0000000000000000"

func TestHandleServe_CancelDuringDrop(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode cancelRaceMode
	}{
		{"cancel_then_drain", cancelThenDrain},
		{"cancel_races_drain", cancelRacesDrain},
		{"cancel_then_write_fails", cancelThenWriteFails},
	} {
		t.Run(tc.name, func(t *testing.T) { runCancelDuringDrop(t, tc.mode) })
	}
}

type hookedDrop struct {
	q         *frameQueue
	mm        *mediaMeter
	droppedMs int
	ageMs     int
	at        time.Time
}

type meterSnap struct {
	framesTotal, framesIn, framesOut uint64
	dropStaleMs, bytesTotal          int64
	dropHeldRuns                     int
	queueDepthCount                  int64
	stallEndPending, ended           bool
}

func snapMeter(mm *mediaMeter) meterSnap {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	return meterSnap{
		framesTotal: mm.framesTotal, framesIn: mm.framesInPeriod, framesOut: mm.framesOutInPeriod,
		dropStaleMs: mm.dropStaleMsInPeriod, bytesTotal: mm.bytesTotal,
		dropHeldRuns: mm.dropHeldRuns, queueDepthCount: mm.queueDepthCount,
		stallEndPending: mm.stallEndPending, ended: mm.ended,
	}
}

func runCancelDuringDrop(t *testing.T, mode cancelRaceMode) {
	short := shortToken(raceToken)
	log, recs := newTestLogger()
	m := NewManager(log, log)
	gw := newGateWriter(mode == cancelThenWriteFails)
	m.ServeWriteWrap = func(_ Modality, w io.Writer) io.Writer {
		gw.w = w
		return gw
	}

	atDrop := make(chan hookedDrop, 1)
	resume := make(chan struct{})
	var resumeOnce sync.Once
	doResume := func() { resumeOnce.Do(func() { close(resume) }) }
	cancelRet := make(chan bool, 1)
	var hookOnce sync.Once

	t.Cleanup(func() { serveWritePopHook = nil })
	serveWritePopHook = func(q *frameQueue, mm *mediaMeter, droppedMs, ageMs int) {
		if droppedMs == 0 && ageMs == 0 {
			return
		}
		hookOnce.Do(func() {
			atDrop <- hookedDrop{q: q, mm: mm, droppedMs: droppedMs, ageMs: ageMs, at: time.Now()}
			if mode == cancelRacesDrain {
				go func() { cancelRet <- m.Cancel(raceToken) }()
				return
			}
			<-resume
		})
	}

	mic := startTestStreamer(t)
	if err := m.RegisterServe(raceToken, ModalityAudio, mic.port); err != nil {
		t.Fatalf("RegisterServe: %v", err)
	}
	handlerDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		if !m.HandleServe(w, r, raceToken) {
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	t.Cleanup(func() {
		m.Cancel(raceToken)
		gw.release()
		doResume()
	})

	bodyCh := make(chan []byte, 1)
	go func() {
		resp, err := srv.Client().Get(srv.URL + "/audio/" + raceToken)
		if err != nil {
			bodyCh <- nil
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		bodyCh <- b
	}()

	conn := mic.accept(t)
	sender := &micSender{}
	var sendWG sync.WaitGroup
	sendWG.Add(1)
	go func() {
		defer sendWG.Done()
		sender.run(conn, bytes.Repeat([]byte{0x5a}, 80), time.Now().Add(10*time.Second))
	}()

	select {
	case <-gw.entered:
	case <-time.After(3 * time.Second):
		t.Fatalf("the first write never started")
	}
	waitForRecord(t, recs, "call media stall start", short, 3*time.Second)
	gw.release()

	var d hookedDrop
	select {
	case d = <-atDrop:
	case <-time.After(3 * time.Second):
		t.Fatalf("no popBatch found a drop run after the stall")
	}

	hold := time.Duration(0)
	switch mode {
	case cancelThenDrain:
		if !m.Cancel(raceToken) {
			t.Fatalf("Cancel returned false")
		}

		deadline := time.Now().Add(2 * time.Second)
		for {
			d.q.mu.Lock()
			closed := d.q.closed
			d.q.mu.Unlock()
			if closed {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the queue was not closed after Cancel")
			}
			time.Sleep(time.Millisecond)
		}
		time.Sleep(staleFrameMaxAge + 50*time.Millisecond)
		hold = time.Since(d.at)
		doResume()
	case cancelThenWriteFails:
		if !m.Cancel(raceToken) {
			t.Fatalf("Cancel returned false")
		}
		hold = time.Since(d.at)
		doResume()
	case cancelRacesDrain:

	}

	select {
	case <-handlerDone:
	case <-time.After(5 * time.Second):
		t.Fatalf("HandleServe did not return after a Cancel during a drop")
	}
	var body []byte
	select {
	case body = <-bodyCh:
	case <-time.After(3 * time.Second):
		t.Fatalf("the GET did not end after HandleServe returned")
	}
	_ = conn.Close()
	sendWG.Wait()
	if mode == cancelRacesDrain {
		select {
		case ok := <-cancelRet:
			if !ok {
				t.Fatalf("Cancel returned false")
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("Cancel did not return")
		}
	}

	s1 := snapMeter(d.mm)
	if !s1.ended {
		t.Fatalf("HandleServe returned, but the meter has not ended")
	}
	n1 := len(recs.messages())
	time.Sleep(200 * time.Millisecond)
	if s2, n2 := snapMeter(d.mm), len(recs.messages()); s2 != s1 || n2 != n1 {
		t.Fatalf("meter changed after end(): %+v then %+v, %d then %d log lines", s1, s2, n1, n2)
	}
	all := recsForToken(recs, short)
	lastMeter := -1
	cancelledAt, firstDropAt := -1, -1
	for i, r := range all {
		switch r.msg {
		case "call media summary", "call media drop", "call media stall start", "call media stall end", "call media end":
			lastMeter = i
		}
		if r.msg == "proxy cancelled" && cancelledAt < 0 {
			cancelledAt = i
		}
		if r.msg == "call media drop" && firstDropAt < 0 {
			firstDropAt = i
		}
	}
	if lastMeter < 0 || all[lastMeter].msg != "call media end" {
		t.Fatalf("last meter line is not \"call media end\": %v", recs.messages())
	}
	if mode != cancelRacesDrain && (cancelledAt < 0 || firstDropAt < cancelledAt) {
		t.Fatalf("want \"proxy cancelled\" before the first \"call media drop\" (the rig's order); got %v", recs.messages())
	}
	if mode != cancelThenWriteFails {
		if fin := filterRecs(all, "proxy serve finished"); len(fin) != 1 || fin[0].attrs["ended"].String() != "closed" {
			t.Fatalf("\"proxy serve finished\" = %v, want one line with ended=closed", fin)
		}
	}

	writes := gw.snapshot()
	var okWrites []gateWrite
	for i, w := range writes {
		if i > 0 && w.counter <= writes[i-1].counter {
			t.Fatalf("write %d: counter %d after %d", i, w.counter, writes[i-1].counter)
		}
		if w.err == nil {
			okWrites = append(okWrites, w)
		}
	}
	if len(writes) < 2 || writes[0].counter != 0 {
		t.Fatalf("writes %+v: want frame 0 (the stalled write) and the frame kept after the drop", writes)
	}
	switch mode {
	case cancelThenDrain:

		if len(writes) != 2 || len(okWrites) != 2 {
			t.Fatalf("%d writes (%d ok), want exactly 2: frame 0 and the kept frame; %+v", len(writes), len(okWrites), writes)
		}
	case cancelThenWriteFails:
		if len(writes) != 2 || len(okWrites) != 1 || !errors.Is(writes[1].err, errGateClosed) {
			t.Fatalf("writes %+v: want frame 0 ok, then the kept frame failing", writes)
		}
	}
	const slack = 30 * time.Millisecond
	for i := 1; i < len(okWrites); i++ {
		w := okWrites[i]
		age := w.at.Sub(time.Unix(0, int64(w.pts)))
		limit := staleFrameMaxAge + slack
		if i == 1 {
			limit += hold
		}
		if age > limit {
			t.Fatalf("write of counter %d started when the frame was %v old, want <= %v", w.counter, age, limit)
		}
	}
	var wantBody []uint64
	wantLen := 0
	for _, w := range okWrites {
		wantBody = append(wantBody, w.counter)
		wantLen += w.n
	}
	if len(body) != wantLen {
		t.Fatalf("GET body %d bytes, want %d (the writes that went through)", len(body), wantLen)
	}
	br := bufio.NewReader(bytes.NewReader(body))
	for i := 0; ; i++ {
		_, counter, _, err := readFrame(br, nil)
		if errors.Is(err, io.EOF) {
			if i != len(wantBody) {
				t.Fatalf("GET body has %d frames, want %d", i, len(wantBody))
			}
			break
		}
		if err != nil {
			t.Fatalf("GET body frame %d: %v", i, err)
		}
		if i >= len(wantBody) || counter != wantBody[i] {
			t.Fatalf("GET body frame %d has counter %d, want %v", i, counter, wantBody)
		}
	}

	pts := sender.sent()
	var gapMs, firstGapMs int64 = 0, -1
	gapFrames := 0
	for i := 1; i < len(writes); i++ {
		a, b := writes[i-1].counter, writes[i].counter
		if b == a+1 {
			continue
		}
		if b >= uint64(len(pts)) {
			t.Fatalf("written counter %d was never sent (%d sent)", b, len(pts))
		}
		v := int64(int((int64(pts[b]) - int64(pts[a+1])) / int64(time.Millisecond)))
		if firstGapMs < 0 {
			firstGapMs = v
		}
		gapMs += v
		gapFrames += int(b - a - 1)
	}
	if firstGapMs != int64(d.droppedMs) {
		t.Fatalf("first counter gap is worth %d ms, the hooked pop reported dropped_ms %d", firstGapMs, d.droppedMs)
	}
	drops := filterRecs(all, "call media drop")
	if len(drops) == 0 {
		t.Fatalf("no \"call media drop\" line")
	}
	var dropLinesMs int64
	for _, r := range drops {
		dropLinesMs += attrInt(t, r.attrs, "dropped_ms")
		if age := attrInt(t, r.attrs, "age_ms"); age <= staleFrameMaxAge.Milliseconds() {
			t.Fatalf("drop line age_ms %d, want above %d", age, staleFrameMaxAge.Milliseconds())
		}
	}
	if dropLinesMs != gapMs {
		t.Fatalf("drop lines sum to %d ms, the counter gaps in dst to %d ms", dropLinesMs, gapMs)
	}
	if got := attrInt(t, drops[0].attrs, "dropped_ms"); got != int64(d.droppedMs) {
		t.Fatalf("first drop line dropped_ms %d, hooked pop %d", got, d.droppedMs)
	}
	if got := attrInt(t, drops[0].attrs, "age_ms"); got != int64(d.ageMs) {
		t.Fatalf("first drop line age_ms %d, hooked pop %d", got, d.ageMs)
	}
	ends := filterRecs(all, "call media stall end")
	if len(ends) != 1 {
		t.Fatalf("%d stall end lines, want 1: %v", len(ends), recs.messages())
	}
	if got := attrInt(t, ends[0].attrs, "dropped_ms"); got != int64(d.droppedMs) {
		t.Fatalf("stall end dropped_ms %d, want the hooked pop's %d", got, d.droppedMs)
	}
	if r, ok := ends[0].attrs["reason"]; ok {
		t.Fatalf("stall end has reason=%s: it was logged by end(), not by the hooked pop's onPop", r.String())
	}
	var sumIn, sumOut, sumDrop int64
	for _, r := range filterRecs(all, "call media summary") {
		sumIn += attrInt(t, r.attrs, "frames_in")
		sumOut += attrInt(t, r.attrs, "frames_out")
		sumDrop += attrInt(t, r.attrs, "drop_stale_ms")
	}
	if sumDrop+s1.dropStaleMs != dropLinesMs {
		t.Fatalf("drop_stale_ms: summaries %d + open period %d, drop lines %d", sumDrop, s1.dropStaleMs, dropLinesMs)
	}
	if sumOut+int64(s1.framesOut) != int64(len(okWrites)) {
		t.Fatalf("frames_out: summaries %d + open period %d, writes that went through %d", sumOut, s1.framesOut, len(okWrites))
	}
	if sumIn+int64(s1.framesIn) != int64(s1.framesTotal) {
		t.Fatalf("frames_in: summaries %d + open period %d, total %d", sumIn, s1.framesIn, s1.framesTotal)
	}
	endLines := filterRecs(all, "call media end")
	if len(endLines) != 1 || attrInt(t, endLines[0].attrs, "frames_in") != int64(s1.framesTotal) {
		t.Fatalf("\"call media end\" lines %v, want one with frames_in %d", endLines, s1.framesTotal)
	}
	if s1.framesTotal < uint64(len(writes)+gapFrames) {
		t.Fatalf("frames_in %d, but %d frames were written or dropped in gaps", s1.framesTotal, len(writes)+gapFrames)
	}
	t.Logf("mode %d: hooked pop dropped_ms=%d age_ms=%d, hold %v, writes %d (%d ok), frames_in %d, drop lines %d ms",
		mode, d.droppedMs, d.ageMs, hold, len(writes), len(okWrites), s1.framesTotal, dropLinesMs)
}
