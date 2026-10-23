package proxy

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type stallProfile struct {
	clean, stall, capFor, every time.Duration
	capBps                      int64
}

var scaledStallProfile = stallProfile{
	clean:  1250 * time.Millisecond,
	stall:  2 * time.Second,
	capBps: 5000,
	capFor: 7500 * time.Millisecond,
	every:  15 * time.Second,
}

type phaseWriter struct {
	p         stallProfile
	w         io.Writer
	done      chan struct{}
	closeOnce sync.Once

	mu    sync.Mutex
	start time.Time
}

func newPhaseWriter(p stallProfile, w io.Writer) *phaseWriter {
	return &phaseWriter{p: p, w: w, done: make(chan struct{})}
}

func (pw *phaseWriter) Close() error {
	pw.closeOnce.Do(func() { close(pw.done) })
	return nil
}

func (pw *phaseWriter) startTime() time.Time {
	pw.mu.Lock()
	defer pw.mu.Unlock()
	return pw.start
}

func (pw *phaseWriter) sleep(d time.Duration) {
	const step = 50 * time.Millisecond
	for d > 0 {
		s := step
		if d < s {
			s = d
		}
		select {
		case <-pw.done:
			return
		default:
		}
		time.Sleep(s)
		d -= s
	}
}

func (pw *phaseWriter) Write(p []byte) (int, error) {
	pw.mu.Lock()
	if pw.start.IsZero() {
		pw.start = time.Now()
	}
	cyc := time.Since(pw.start) % pw.p.every
	pw.mu.Unlock()
	stallEnd := pw.p.clean + pw.p.stall
	capEnd := stallEnd + pw.p.capFor
	switch {
	case cyc < pw.p.clean:
		return pw.w.Write(p)
	case cyc < stallEnd:
		pw.sleep(stallEnd - cyc)
		return pw.w.Write(p)
	case cyc < capEnd:
		n, err := pw.w.Write(p)
		if err == nil && n > 0 {
			pw.sleep(time.Duration(float64(n) / float64(pw.p.capBps) * float64(time.Second)))
		}
		return n, err
	default:
		return pw.w.Write(p)
	}
}

type spkFrame struct {
	at       time.Time
	counter  uint64
	pts      uint64
	excessMs int64
}

type spkReader struct {
	meter *mediaMeter

	mu     sync.Mutex
	frames []spkFrame
}

func (s *spkReader) run(conn net.Conn) {
	br := bufio.NewReader(conn)
	for {
		frame, counter, pts, err := readFrame(br, nil)
		if err != nil {
			return
		}
		now := time.Now()
		ex := s.meter.onFrameIn(now, len(frame), pts)
		s.mu.Lock()
		s.frames = append(s.frames, spkFrame{at: now, counter: counter, pts: pts, excessMs: ex})
		s.mu.Unlock()
	}
}

func (s *spkReader) snapshot() []spkFrame {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]spkFrame(nil), s.frames...)
}

type callDirection struct {
	name   string
	token  string
	mic    *testStreamer
	spk    *testStreamer
	sender *micSender
	writer *phaseWriter
	spkR   *spkReader
	stopAt time.Time
}

func summarySpan(t *testing.T, r tokenRec) (from, to time.Time) {
	t.Helper()
	return r.at.Add(-time.Duration(attrInt(t, r.attrs, "period_ms")) * time.Millisecond), r.at
}

func TestCall_TwoWay_StallCycle_DelayExcessBounded(t *testing.T) {
	prof := scaledStallProfile
	const (
		stagger = 700 * time.Millisecond
		boundMs = int64(250)
	)
	runFor := prof.clean + prof.stall + prof.capFor + 2250*time.Millisecond
	stallSeenMs := (prof.stall * 8 / 10).Milliseconds()
	payload := bytes.Repeat([]byte{0xa5}, 80)

	serveLog, serveRecs := newTestLogger()
	mServe := NewManager(serveLog, serveLog)
	wrapped := make(chan *phaseWriter, 2)
	mServe.ServeWriteWrap = func(modality Modality, w io.Writer) io.Writer {
		if modality != ModalityAudio {
			return w
		}
		pw := newPhaseWriter(prof, w)
		wrapped <- pw
		return pw
	}
	fetchLog, fetchRecs := newTestLogger()
	mFetch := NewManager(fetchLog, fetchLog)
	discardLog, _ := newTestLogger()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !mServe.HandleServe(w, r, strings.TrimPrefix(r.URL.Path, "/audio/")) {
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	dirs := []*callDirection{
		{name: "A->B", token: "callAtoB-0000000000000000"},
		{name: "B->A", token: "callBtoA-0000000000000000"},
	}
	t.Cleanup(func() {
		for _, dir := range dirs {
			mServe.Cancel(dir.token)
			mFetch.Cancel(dir.token)
		}
	})
	var wg sync.WaitGroup
	for i, dir := range dirs {
		if i > 0 {
			time.Sleep(stagger)
		}
		dir.mic = startTestStreamer(t)
		dir.spk = startTestStreamer(t)
		if err := mServe.RegisterServe(dir.token, ModalityAudio, dir.mic.port); err != nil {
			t.Fatalf("%s RegisterServe: %v", dir.name, err)
		}
		if err := mFetch.StartFetch(ctx, dir.token, ModalityAudio, srv.URL+"/audio/"+dir.token, dir.spk.port, srv.Client()); err != nil {
			t.Fatalf("%s StartFetch: %v", dir.name, err)
		}
		micConn := dir.mic.accept(t)

		select {
		case dir.writer = <-wrapped:
		case <-time.After(3 * time.Second):
			t.Fatalf("%s: ServeWriteWrap was never called", dir.name)
		}
		spkConn := dir.spk.accept(t)
		dir.sender = &micSender{}
		dir.spkR = &spkReader{meter: newMediaMeter(discardLog, nil, "fetch", "spk")}
		dir.stopAt = time.Now().Add(runFor)
		wg.Add(2)
		go func() { defer wg.Done(); dir.spkR.run(spkConn) }()
		go func() { defer wg.Done(); dir.sender.run(micConn, payload, dir.stopAt) }()
	}
	allDone := make(chan struct{})
	go func() { wg.Wait(); close(allDone) }()
	select {
	case <-allDone:
	case <-time.After(runFor + stagger + 10*time.Second):
		t.Fatalf("the call did not end: a sender or a spk reader is still running")
	}

	if off := dirs[1].writer.startTime().Sub(dirs[0].writer.startTime()); off < stagger*7/10 {
		t.Fatalf("the directions' cycles start %v apart, want at least %v (out of phase)", off, stagger*7/10)
	}

	for _, dir := range dirs {
		short := shortToken(dir.token)
		ws := dir.writer.startTime()
		stallStart := ws.Add(prof.clean)
		stallEnd := stallStart.Add(prof.stall)
		capEnd := stallEnd.Add(prof.capFor)

		frames := dir.spkR.snapshot()
		sent := dir.sender.sent()
		if len(frames) < 100 {
			t.Fatalf("%s: spk got %d frames, want hundreds", dir.name, len(frames))
		}
		for i := 1; i < len(frames); i++ {
			if frames[i].counter <= frames[i-1].counter {
				t.Fatalf("%s: spk got counter %d after %d", dir.name, frames[i].counter, frames[i-1].counter)
			}
		}

		stallIdx := 1
		for i := 2; i < len(frames); i++ {
			if frames[i].at.Sub(frames[i-1].at) > frames[stallIdx].at.Sub(frames[stallIdx-1].at) {
				stallIdx = i
			}
		}
		stallFrame := frames[stallIdx]
		stallGap := stallFrame.at.Sub(frames[stallIdx-1].at)
		var maxAfter, maxPostCap int64
		capDelivered, postCapN := 0, 0
		for _, f := range frames[stallIdx+1:] {
			if f.excessMs > maxAfter {
				maxAfter = f.excessMs
			}
			if f.at.After(stallEnd) && !f.at.After(capEnd) {
				capDelivered++
			}
			if f.at.After(capEnd.Add(300 * time.Millisecond)) {
				postCapN++
				if f.excessMs > maxPostCap {
					maxPostCap = f.excessMs
				}
			}
		}
		capSent := 0
		for _, p := range sent {
			if at := time.Unix(0, int64(p)); !at.Before(stallEnd) && !at.After(capEnd) {
				capSent++
			}
		}

		fetchAll := recsForToken(fetchRecs, short)
		var sumAfterMax, stallPeriodMax int64 = -1, -1
		capPeriods := 0
		for _, r := range filterRecs(fetchAll, "call media summary") {
			from, to := summarySpan(t, r)
			dmax := attrInt(t, r.attrs, "delay_excess_max_ms")
			switch {
			case !from.After(stallFrame.at) && !to.Before(stallFrame.at):
				stallPeriodMax = dmax
			case from.After(stallFrame.at):
				if dmax > sumAfterMax {
					sumAfterMax = dmax
				}
				if !to.After(capEnd) && attrInt(t, r.attrs, "frames_in") >= 30 {
					capPeriods++
				}
			}
		}
		fetchEnd := filterRecs(fetchAll, "call media end")
		if len(fetchEnd) != 1 {
			t.Fatalf("%s: %d fetch \"call media end\" lines, want 1", dir.name, len(fetchEnd))
		}
		fetchEndMax := attrInt(t, fetchEnd[0].attrs, "delay_excess_max_ms")

		var stallCapIn, stallCapOut int64
		capDropPeriods, recoveredPeriods := 0, 0
		for _, r := range filterRecs(recsForToken(serveRecs, short), "call media summary") {
			from, to := summarySpan(t, r)
			in, out := attrInt(t, r.attrs, "frames_in"), attrInt(t, r.attrs, "frames_out")
			drop := attrInt(t, r.attrs, "drop_stale_ms")
			if !from.Before(stallStart) && !to.After(capEnd) {
				stallCapIn += in
				stallCapOut += out
				if from.After(stallEnd.Add(100*time.Millisecond)) && drop > 0 {
					capDropPeriods++
				}
			}
			if from.After(capEnd.Add(100*time.Millisecond)) && !to.After(dir.stopAt) && drop == 0 && in-out <= 1 && out-in <= 1 && in >= 40 {
				recoveredPeriods++
			}
		}

		dropped := len(sent) - len(frames)
		capDroppedPct := 0.0
		if capSent > 0 {
			capDroppedPct = 100 * float64(capSent-capDelivered) / float64(capSent)
		}
		t.Logf("direction=%s max_excess_ms=%d frames_dropped=%d dropped_pct=%.1f%% frames_sent=%d frames_delivered=%d cap_dropped_pct=%.1f%% stall_frame_excess_ms=%d stall_gap_ms=%d fetch_summary_max_after_stall_ms=%d fetch_summary_stall_period_max_ms=%d fetch_end_delay_excess_max_ms=%d post_cap_max_excess_ms=%d",
			dir.name, maxAfter, dropped, 100*float64(dropped)/float64(len(sent)), len(sent), len(frames),
			capDroppedPct, stallFrame.excessMs, stallGap.Milliseconds(), sumAfterMax, stallPeriodMax, fetchEndMax, maxPostCap)

		if stallGap.Milliseconds() < stallSeenMs || stallFrame.excessMs < stallSeenMs {
			t.Errorf("%s: largest arrival gap %v, its frame's excess %d ms; want both about the %v stall (>= %d ms)",
				dir.name, stallGap, stallFrame.excessMs, prof.stall, stallSeenMs)
		}
		if stallPeriodMax < stallSeenMs || fetchEndMax < stallSeenMs {
			t.Errorf("%s: fetch delay_excess_max_ms %d in the stall's summary, %d at the end; want both >= %d",
				dir.name, stallPeriodMax, fetchEndMax, stallSeenMs)
		}

		if maxAfter > boundMs {
			t.Errorf("%s: a frame after the stall had delay excess %d ms, want <= %d", dir.name, maxAfter, boundMs)
		}
		if sumAfterMax > boundMs {
			t.Errorf("%s: a fetch summary after the stall has delay_excess_max_ms %d, want <= %d", dir.name, sumAfterMax, boundMs)
		}
		if capPeriods < 4 {
			t.Errorf("%s: %d fetch summaries in the cap phase with >= 30 frames, want >= 4", dir.name, capPeriods)
		}
		if minCap := int(prof.capFor / micFrameEvery / 2); capDelivered < minCap {
			t.Errorf("%s: %d frames reached spk in the cap phase, want >= %d", dir.name, capDelivered, minCap)
		}

		if stallCapIn < 100 || stallCapOut >= stallCapIn {
			t.Errorf("%s: serve frames_in %d, frames_out %d over the stall and the cap; want frames_out below frames_in, frames_in >= 100",
				dir.name, stallCapIn, stallCapOut)
		}
		if capDropPeriods < 1 {
			t.Errorf("%s: no serve summary in the cap phase has drop_stale_ms > 0", dir.name)
		}

		if recoveredPeriods < 1 || postCapN < 20 || maxPostCap > 50 {
			t.Errorf("%s: after the cap: %d clean serve summaries (want >= 1), %d frames at spk (want >= 20), max excess %d ms (want <= 50)",
				dir.name, recoveredPeriods, postCapN, maxPostCap)
		}
	}
}
