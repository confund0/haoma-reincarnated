package main

import (
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"haoma-frontend/internal/calls"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/logging"
)

func logLines(t *testing.T, write func(*slog.Logger)) []string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "x.log")
	logger, closer, err := logging.New(logging.Config{Level: "debug", File: path})
	if err != nil {
		t.Fatal(err)
	}
	write(logger)
	if err := closer(); err != nil {
		t.Fatal(err)
	}
	lines, err := readLinesFrom(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	return lines
}

const testCallID = "Y061TApJGdgndoE_TyGfu_FKEbUhZEeE_W5v3VzrFjs"
const testToken = "zAB4Q4L62pyYMGXFqUIVTo5PLEFezNWXsRF0UjkeaiY"

func frame(t *testing.T, ft ipc.FrameType, payload any) ipc.Frame {
	t.Helper()
	f, err := ipc.NewFrame(ft, "", payload)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestCallObsOf(t *testing.T) {
	now := time.Now()
	st := frame(t, ipc.FrameCallStateChanged, ipc.CallStateChangedPayload{Call: ipc.CallEntry{CallID: testCallID, Status: "ringing", Direction: "in"}})
	if o, ok := callObsOf(st, now); !ok || o.state == nil || o.state.Status != "ringing" || o.callID() != testCallID {
		t.Fatalf("state push: %+v %v", o, ok)
	}
	ev := frame(t, ipc.FrameCallStreamEvent, ipc.CallStreamEventPayload{CallID: testCallID, Side: "spk", Type: "stats", FramesIn: 7})
	if o, ok := callObsOf(ev, now); !ok || o.stream == nil || o.stream.FramesIn != 7 {
		t.Fatalf("stream event: %+v %v", o, ok)
	}

	timeline := func(kind events.Kind, body any) ipc.Frame {
		raw, _ := json.Marshal(body)
		e, _ := json.Marshal(events.Event{Kind: kind, Body: raw})
		return frame(t, ipc.FrameTimelineEvent, ipc.TimelineEventPayload{Event: e})
	}
	crumb := timeline(events.KindCallSummary, events.CallSummaryBody{CallID: testCallID, Outcome: events.CallOutcomeAnsweredElsewhere})
	if o, ok := callObsOf(crumb, now); !ok || o.crumb == nil || o.crumb.Outcome != events.CallOutcomeAnsweredElsewhere {
		t.Fatalf("crumb: %+v %v", o, ok)
	}

	raw, _ := json.Marshal(events.CallSummaryBody{CallID: testCallID, Direction: "out", Outcome: events.CallOutcomeCompleted})
	e, _ := json.Marshal(events.Event{Kind: events.KindCallSummary, MsgID: "crumb-msg-id", Direction: events.DirOut, Body: raw})
	if o, ok := callObsOf(frame(t, ipc.FrameTimelineEvent, ipc.TimelineEventPayload{Event: e}), now); !ok || o.crumbMsgID != "crumb-msg-id" || o.crumbDir != events.DirOut {
		t.Fatalf("crumb msg id / direction: %+v %v", o, ok)
	}
	if _, ok := callObsOf(timeline(events.KindText, map[string]string{"text": "hi"}), now); ok {
		t.Fatal("a text row read as a call")
	}
	if _, ok := callObsOf(frame(t, ipc.FrameCallStateChanged, ipc.CallStateChangedPayload{}), now); ok {
		t.Fatal("a push with no call id read as a call")
	}
}

func TestLastStreamersReady(t *testing.T) {
	lines := logLines(t, func(l *slog.Logger) {
		l.Info("streamers ready", slog.String("mic", "/old/haoma-mic"), slog.String("spk", "/old/haoma-spk"))
		l.Info("something else", slog.String("mic", "/nope"))
		l.Info("streamers ready", slog.String("mic", "/bins/fake-streamers/haoma-mic"), slog.String("spk", "/bins/fake-streamers/haoma-spk"))
	})
	if got, ok := lastStreamersReady(lines); !ok || got != "/bins/fake-streamers/haoma-mic" {
		t.Fatalf("got %q %v", got, ok)
	}
	if _, ok := lastStreamersReady(lines[1:2]); ok {
		t.Fatal("found a ready line where there is none")
	}
}

func TestFetchOutcomeIn(t *testing.T) {

	short := testToken[:8] + "…"
	other := "QQQQQQQQ" + testToken[8:]
	lines := logLines(t, func(l *slog.Logger) {
		l.Info("proxy fetch starting", slog.String("token", short), slog.String("peer_url", "http://x.onion/audio/"+testToken))
		l.Warn("proxy fetch: non-200 from peer", slog.String("token", other[:8]+"…"), slog.Int("status", 404))
		l.Warn("proxy fetch: non-200 from peer", slog.String("token", short), slog.Int("status", 409))
	})
	if got := fetchOutcomeIn(lines, testToken); got.kind != "status" || got.status != 409 {
		t.Fatalf("got %s", got)
	}
	if got := fetchOutcomeIn(lines[:1], testToken); got.kind != "" {
		t.Fatalf("a start line read as an answer: %s", got)
	}
	failed := logLines(t, func(l *slog.Logger) {
		l.Warn("proxy fetch: GET failed", slog.String("token", short))
	})
	if got := fetchOutcomeIn(failed, testToken); got.kind != "get failed" {
		t.Fatalf("got %s", got)
	}
	took := logLines(t, func(l *slog.Logger) {
		l.Info("proxy fetch finished", slog.String("token", short), slog.Int64("bytes", 9000))
	})
	if got := fetchOutcomeIn(took, testToken); got.kind != "finished" {
		t.Fatalf("got %s", got)
	}
}

func TestRingTimeoutLogged(t *testing.T) {
	lines := logLines(t, func(l *slog.Logger) {
		l.Info("call stopped ringing here (ring timeout); nothing sent",
			slog.String("call_id", testCallID), slog.Duration("after", 60*time.Second))
	})
	if !ringTimeoutLogged(lines, testCallID) {
		t.Fatalf("not found in %q", lines)
	}
	if ringTimeoutLogged(lines, "AAAAAAAAAAAAAAAAAAAA") {
		t.Fatal("matched another call")
	}
}

func TestStreamURL(t *testing.T) {
	want := "http://abc.onion/audio/" + testToken
	for _, a := range []string{"abc", "abc.onion"} {
		if got := streamURL(a, "audio", testToken); got != want {
			t.Errorf("streamURL(%q) = %q", a, got)
		}
	}
}

func stateObs(at time.Time, status, reason string) callObs {
	return callObs{at: at, state: &ipc.CallEntry{CallID: testCallID, Status: status, FailReason: reason}}
}

func TestNoAnswerEnd(t *testing.T) {
	start := time.Now()
	if err := noAnswerEnd(stateObs(start.Add(31*time.Second), "failed", calls.FailReasonNoAnswer), start); err != nil {
		t.Fatal(err)
	}
	for name, o := range map[string]callObs{
		"too early":  stateObs(start.Add(10*time.Second), "failed", calls.FailReasonNoAnswer),
		"too late":   stateObs(start.Add(70*time.Second), "failed", calls.FailReasonNoAnswer),
		"not failed": stateObs(start.Add(31*time.Second), "ended", ""),
		"reason":     stateObs(start.Add(31*time.Second), "failed", calls.FailReasonSendFail),
	} {
		if noAnswerEnd(o, start) == nil {
			t.Errorf("%s: passed", name)
		}
	}
}

func TestOwnTimerStop(t *testing.T) {
	rang := time.Now()
	if err := ownTimerStop("x", stateObs(rang.Add(60*time.Second), "ended", ""), rang); err != nil {
		t.Fatal(err)
	}

	if ownTimerStop("x", stateObs(rang.Add(31*time.Second), "ended", ""), rang) == nil {
		t.Fatal("a stop at 31 s passed as the 60 s timer")
	}
	if ownTimerStop("x", stateObs(rang.Add(120*time.Second), "ended", ""), rang) == nil {
		t.Fatal("a stop at 120 s passed")
	}
	if ownTimerStop("x", stateObs(rang.Add(60*time.Second), "ended", "answered_elsewhere"), rang) == nil {
		t.Fatal("answered elsewhere passed as a missed call")
	}
}

func TestDeviceStoppedBy(t *testing.T) {
	rang := time.Now()
	if err := deviceStoppedBy("x", stateObs(rang.Add(5*time.Second), "ended", ""), rang, 50*time.Second); err != nil {
		t.Fatal(err)
	}
	if deviceStoppedBy("x", stateObs(rang.Add(55*time.Second), "ended", ""), rang, 50*time.Second) == nil {
		t.Fatal("a late stop passed")
	}
	if deviceStoppedBy("x", stateObs(rang.Add(5*time.Second), "rejected", ""), rang, 50*time.Second) == nil {
		t.Fatal("a rejected call passed as stopped ringing")
	}
}

func TestAwaitCrumb(t *testing.T) {
	old := callCrumbGrace
	callCrumbGrace = 10 * time.Millisecond
	defer func() { callCrumbGrace = old }()
	crumb := func(id string, out events.CallOutcome) callObs {
		return callObs{crumb: &events.CallSummaryBody{CallID: id, Outcome: out}}
	}
	w := &callWatch{r: &rig{name: "x"}, seen: []callObs{crumb("other", events.CallOutcomeMissed), crumb(testCallID, events.CallOutcomeMissed)}}
	if err := w.awaitCrumb(testCallID, events.CallOutcomeMissed, time.Second); err != nil {
		t.Fatal(err)
	}
	if w.awaitCrumb(testCallID, events.CallOutcomeAnsweredElsewhere, time.Second) == nil {
		t.Fatal("a missed call passed as answered elsewhere")
	}
	w.seen = append(w.seen, crumb(testCallID, events.CallOutcomeAnsweredElsewhere))
	if err := w.awaitCrumb(testCallID, events.CallOutcomeMissed, time.Second); err == nil || !strings.Contains(err.Error(), "want one") {
		t.Fatalf("two crumbs for one call passed: %v", err)
	}
	empty := &callWatch{r: &rig{name: "x"}}
	if empty.awaitCrumb(testCallID, events.CallOutcomeMissed, 300*time.Millisecond) == nil {
		t.Fatal("no crumb passed")
	}
}

func TestSyncedCrumb(t *testing.T) {
	old := callCrumbGrace
	callCrumbGrace = 10 * time.Millisecond
	defer func() { callCrumbGrace = old }()
	const m1 = "0123456789abcdef0123456789abcdef"
	const m2 = "fedcba9876543210fedcba9876543210"
	crumb := func(id, msgID string, dir events.Direction, out events.CallOutcome) callObs {
		return callObs{crumb: &events.CallSummaryBody{CallID: id, Outcome: out}, crumbMsgID: msgID, crumbDir: dir}
	}
	from := &callWatch{r: &rig{name: "callee-b"}, seen: []callObs{crumb(testCallID, m1, events.DirOut, events.CallOutcomeCompleted)}}
	watch := func(obs ...callObs) *callWatch { return &callWatch{r: &rig{name: "callee-a"}, seen: obs} }

	pushLog := func(msg, msgID string) func() []string {
		lines := logLines(t, func(l *slog.Logger) { l.Debug(msg, slog.String("msg_id", msgID)) })
		return func() []string { return lines }
	}
	applied := pushLog("rail: call summary applied — local state changed", m1)
	held := pushLog("rail: call summary already held — no second row", m1)
	good := crumb(testCallID, m1, events.DirOut, events.CallOutcomeCompleted)

	for name, log := range map[string]func() []string{"applied": applied, "already held": held} {
		if err := syncedCrumb(from, watch(good), testCallID, events.CallOutcomeCompleted, time.Second, log); err != nil {
			t.Fatalf("push line %s: %v", name, err)
		}
	}
	for name, c := range map[string]struct {
		from, to *callWatch
		log      func() []string
		wantErr  string
	}{
		"nothing arrived":             {from, watch(), applied, "no crumb"},
		"other msg id":                {from, watch(crumb(testCallID, m2, events.DirOut, events.CallOutcomeCompleted)), applied, "msg id"},
		"incoming crumb":              {from, watch(crumb(testCallID, m1, events.DirIn, events.CallOutcomeCompleted)), applied, "direction"},
		"other outcome":               {from, watch(crumb(testCallID, m1, events.DirOut, events.CallOutcomeMissed)), applied, "says"},
		"two crumbs":                  {from, watch(good, good), applied, "holds 2"},
		"crumb of old build":          {watch(crumb(testCallID, "", events.DirOut, events.CallOutcomeCompleted)), watch(crumb(testCallID, "", events.DirOut, events.CallOutcomeCompleted)), applied, "msg id"},
		"caller has none":             {watch(), watch(good), applied, "holds 0"},
		"gap-fill only":               {from, watch(good), func() []string { return nil }, "never logged the rail push"},
		"push line of another msg id": {from, watch(good), pushLog("rail: call summary applied — local state changed", m2), "never logged the rail push"},
		"another line for the msg id": {from, watch(good), pushLog("gapfill: row inserted", m1), "never logged the rail push"},
	} {
		err := syncedCrumb(c.from, c.to, testCallID, events.CallOutcomeCompleted, 300*time.Millisecond, c.log)
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: err = %v, want one naming %q", name, err, c.wantErr)
		}
	}
}

func TestSawRing(t *testing.T) {
	crumbOnly := &callWatch{r: &rig{name: "x"}, seen: []callObs{{crumb: &events.CallSummaryBody{CallID: testCallID}}}}
	if crumbOnly.sawRing(testCallID) {
		t.Fatal("a crumb read as a ring")
	}
	rang := &callWatch{r: &rig{name: "x"}, seen: []callObs{{state: &ipc.CallEntry{CallID: testCallID, Status: "ringing"}}}}
	if !rang.sawRing(testCallID) || rang.sawRing("other") {
		t.Fatal("sawRing wrong on a call state")
	}
	streamed := &callWatch{r: &rig{name: "x"}, seen: []callObs{{stream: &ipc.CallStreamEventPayload{CallID: testCallID}}}}
	if !streamed.sawRing(testCallID) {
		t.Fatal("sawRing misses a streamer")
	}
}

func TestCrumbRoute(t *testing.T) {
	const msgID = "0123456789abcdef0123456789abcdef"
	lines := logLines(t, func(l *slog.Logger) {
		l.Debug("rail: call summary applied — local state changed", slog.String("msg_id", msgID), slog.String("outcome", "applied"))
	})
	if got := crumbRoute(lines, msgID); got != "by the rail push" {
		t.Fatalf("got %q", got)
	}
	if got := crumbRoute(lines, "ffffffff"+msgID[8:]); !strings.Contains(got, "gap-fill") {
		t.Fatalf("another crumb's line read as this one's: %q", got)
	}
}

func TestSpkFramesIn(t *testing.T) {
	stat := func(side string, n uint64) callObs {
		return callObs{stream: &ipc.CallStreamEventPayload{CallID: testCallID, Side: side, Type: "stats", FramesIn: n}}
	}
	w := &callWatch{r: &rig{name: "x"}, seen: []callObs{stat("spk", 10), stat("mic", 900), stat("spk", 40), stat("spk", 30)}}
	if got := w.spkFramesIn(testCallID); got != 40 {
		t.Fatalf("got %d, want 40 (the mic's counters are not the spk's)", got)
	}
	if err := w.awaitSpkFrames(testCallID, 39, time.Second); err != nil {
		t.Fatal(err)
	}
	if w.awaitSpkFrames(testCallID, 40, 300*time.Millisecond) == nil {
		t.Fatal("no new frames passed")
	}
	if !w.sawStream(testCallID) || w.sawStream("other") {
		t.Fatal("sawStream wrong")
	}
}

func TestOfferCounts(t *testing.T) {
	lines := logLines(t, func(l *slog.Logger) {
		l.Info("call offered to every device of the contact", slog.String("call_id", "OTHERCALL0000000"),
			slog.Int("targets", 1), slog.Int("offered", 1), slog.Int("skipped", 0))
		l.Info("call offered to every device of the contact", slog.String("call_id", testCallID),
			slog.Int("targets", 2), slog.Int("offered", 1), slog.Int("skipped", 1))
	})
	got, ok := offerCounts(lines, testCallID)
	if !ok || got != (offered{targets: 2, offered: 1, skipped: 1}) {
		t.Fatalf("got %s %v", got, ok)
	}
	if got == (offered{targets: 2, offered: 2}) {
		t.Fatal("a skipped device read as offered")
	}
	if _, ok := offerCounts(lines[:1], testCallID); ok {
		t.Fatal("another call's line read as this call's")
	}
}

func TestMediaStartLines(t *testing.T) {
	probe := "PROBEtok" + testToken[8:]
	haomadProbe := logLines(t, func(l *slog.Logger) {
		l.Info("proxy fetch starting", slog.String("token", probe[:8]+"…"), slog.String("modality", "audio"))
		l.Warn("proxy fetch: non-200 from peer", slog.String("token", probe[:8]+"…"), slog.Int("status", 409))
	})
	quiet := logLines(t, func(l *slog.Logger) {
		l.Info("call state changed (inbound)", slog.String("call_id", testCallID), slog.String("status", "ended"))
		l.Debug("streamer spawned", slog.String("call_id", "OTHERCALL0000000"), slog.Int("pid", 1))
	})
	if got := mediaStartLines(quiet, haomadProbe, testCallID, probe); len(got) != 0 {
		t.Fatalf("a device with no media for the call flagged: %v", got)
	}

	spawned := logLines(t, func(l *slog.Logger) {
		l.With(slog.String("call_id", testCallID)).Debug("streamer spawned", slog.Int("pid", 1))
	})
	senderUp := logLines(t, func(l *slog.Logger) {
		l.Info("call sender leg up", slog.String("call_id", testCallID), slog.String("modality", "audio"))
	})
	receiverUp := logLines(t, func(l *slog.Logger) {
		l.Info("call receiver leg up", slog.String("call_id", testCallID), slog.String("modality", "audio"))
	})
	serve := logLines(t, func(l *slog.Logger) {
		l.Info("proxy serve registered", slog.String("token", "OWNTOKEN…"), slog.Int("local_port", 4000))
	})
	fetch := logLines(t, func(l *slog.Logger) {
		l.Info("proxy fetch starting", slog.String("token", testToken[:8]+"…"), slog.Int("local_port", 4001))
	})
	for name, c := range map[string]struct{ haoma, haomad []string }{
		"streamer spawned": {spawned, haomadProbe},
		"sender leg":       {senderUp, haomadProbe},
		"receiver leg":     {receiverUp, haomadProbe},
		"own serve":        {quiet, append(serve, haomadProbe...)},
		"own fetch":        {quiet, append(fetch, haomadProbe...)},
	} {
		if got := mediaStartLines(c.haoma, c.haomad, testCallID, probe); len(got) != 1 {
			t.Errorf("%s: got %v, want one line", name, got)
		}
	}

	if got := mediaStartLines(quiet, haomadProbe, testCallID, ""); len(got) != 1 {
		t.Fatalf("with no probe token: got %v", got)
	}
}

func TestRoundTripClosed(t *testing.T) {
	const peer = "dcb0ae8912345678dcb0ae8912345678"
	lines := logLines(t, func(l *slog.Logger) {
		l.Debug("A0: responder presence echo sent (pairing round-trip closed)", slog.String("peer_id", "aaaaaaaa12345678"))
		l.Warn("A0: responder presence echo failed", slog.String("peer_id", peer))
	})
	if roundTripClosed(lines, peer) {
		t.Fatal("another peer's line, or a failed echo, read as this session closed")
	}
	lines = append(lines, logLines(t, func(l *slog.Logger) {
		l.Debug("A0: responder presence echo sent (pairing round-trip closed)", slog.String("peer_id", peer))
	})...)
	if !roundTripClosed(lines, peer) {
		t.Fatalf("not found in %q", lines)
	}
}
