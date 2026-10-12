package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/calls"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/store"
)

var callRigNames = []string{"caller", "callee-a", "callee-b"}

const callRigCount = 3

const fakeStreamerDir = "fake-streamers"

const streamerDirEnv = "HAOMA_STREAMER_DIR"

const (
	callerNoAnswer = 30 * time.Second
	calleeRingOut  = 60 * time.Second
)

const (
	callReplyTimeout = 60 * time.Second

	callSignalTimeout = 90 * time.Second

	callAudioTimeout = 90 * time.Second

	callSettle = 3 * time.Second

	fetchProbeTimeout = 90 * time.Second
)

var callCrumbGrace = 3 * time.Second

var callChecks = []string{
	"pair caller<->callee-a", "link callee-a->callee-b", "callee-b inherits the contact",
	"callee-b pairs with caller", "caller holds two pairings in one chat",
	"texts on both pairings", "fake streamers in use",
	"accept: rings both devices", "accept: callee-b answers", "accept: callee-a told answered elsewhere",
	"accept: callee-a crumb", "accept: audio both ways", "accept: the device that did not answer starts no audio",
	"accept: second fetch gets 409", "accept: hang-up ends both",
	"decline: rings both devices", "decline: callee-a declines", "decline: callee-b stops",
	"no answer: rings both devices", "no answer: caller ends at its timer", "no answer: both devices stop",
	"ring timer: rings both devices", "ring timer: devices stop on their own timer", "ring timer: caller comes back",
	"hang-up: rings both devices", "hang-up: both devices stop",
	"reverse: caller rings", "reverse: caller answers", "reverse: audio both ways",
	"reverse: callee-a does not ring", "reverse: hang-up ends both",
	"reverse: callee-a holds callee-b's crumb",
}

type callCast struct {
	caller, a, b *rig

	callerSeesA, callerSeesB string
	aSeesCaller, bSeesCaller string

	callerChat string
	bChat      string

	watch map[*rig]*callWatch
}

func runCallFleet(caller, a, b *rig) *pairingResult {
	res := &pairingResult{a: caller.name, b: a.name + "+" + b.name}
	stop := func(why string) *pairingResult {
		notAsserted(res, callChecks, why)
		return res
	}
	c, ok := setUpCallCast(res, caller, a, b)
	if !ok {
		return stop("the setup failed")
	}
	defer func() {
		for _, w := range c.watch {
			w.close()
		}
	}()

	if !c.caseAccept(res) {
		return stop("a call in the accept case did not end")
	}
	rigSleep(callSettle, "settle: streamers of the last call")
	if !c.caseDecline(res) {
		return stop("a call in the decline case did not end")
	}
	rigSleep(callSettle, "settle: streamers of the last call")
	if !c.caseNoAnswer(res) {
		return stop("a call in the no-answer case did not end")
	}
	rigSleep(callSettle, "settle: streamers of the last call")
	if !c.caseRingTimer(res) {
		return stop("a call in the ring-timer case did not end")
	}
	rigSleep(callSettle, "settle: streamers of the last call")
	if !c.caseHangUp(res) {
		return stop("a call in the hang-up case did not end")
	}
	rigSleep(callSettle, "settle: streamers of the last call")
	c.caseReverse(res)

	notAsserted(res, callChecks, "an earlier step of its case failed")
	for _, ch := range res.checks {
		if !ch.ok {
			logf("[call] %s FAILED: %s", ch.name, ch.err)
		}
	}
	return res
}

func setUpCallCast(res *pairingResult, caller, a, b *rig) (*callCast, bool) {
	c := &callCast{caller: caller, a: a, b: b}
	var err error
	c.callerSeesA, c.aSeesCaller, err = pairOnion(caller, a)
	res.add("pair caller<->callee-a", err)
	if err != nil {
		return nil, false
	}
	if _, err := linkDevices(a, b); err != nil {
		res.add("link callee-a->callee-b", err)
		return nil, false
	}
	res.add("link callee-a->callee-b", nil)
	time.Sleep(linkSettleDelay)

	aChat, err := chatWith(a, c.aSeesCaller)
	if err == nil {
		err = assertBackfilled(b, caller, aChat)
	}
	var callerFP string
	if err == nil {
		callerFP, err = inheritedFingerprint(b, aChat)
	}
	res.add("callee-b inherits the contact", err)
	if err != nil {
		return nil, false
	}

	if _, err := awaitInheritedRow(b, callerFP, func(e ipc.InheritedContactEntry) bool { return e.Offers == 1 && !e.Waiting }); err != nil {
		res.add("callee-b pairs with caller", err)
		return nil, false
	}
	got, sees, err := offerToOneDevice(b, callerFP, []*rig{caller})
	if err == nil && got != caller {
		err = fmt.Errorf("the invitation reached %s", got.name)
	}
	res.add("callee-b pairs with caller", err)
	if err != nil {
		return nil, false
	}
	c.bSeesCaller = sees

	if err := c.findCallerSide(); err != nil {
		res.add("caller holds two pairings in one chat", err)
		return nil, false
	}
	res.add("caller holds two pairings in one chat", nil)
	if c.bChat, err = chatWith(b, c.bSeesCaller); err != nil {
		res.add("texts on both pairings", err)
		return nil, false
	}

	awaitFirstReach(b, caller, c.bSeesCaller)
	if err := awaitSessionClosed(caller, c.callerSeesB); err != nil {
		res.add("texts on both pairings", err)
		return nil, false
	}
	var textErr error
	for _, t := range []struct {
		from, to *rig
		peer     string
	}{
		{b, caller, c.bSeesCaller},
		{caller, a, c.callerSeesA},
		{a, caller, c.aSeesCaller},
		{caller, b, c.callerSeesB},
	} {
		if _, err := deliverText(t.from, t.to, t.peer, "before the calls "+nextCorrID()); err != nil && textErr == nil {
			textErr = fmt.Errorf("%s -> %s: %w", t.from.name, t.to.name, err)
		}
	}
	res.add("texts on both pairings", textErr)
	if textErr != nil {
		return nil, false
	}

	var fakeErr error
	for _, r := range []*rig{caller, a, b} {
		if err := assertFakeStreamers(r); err != nil && fakeErr == nil {
			fakeErr = err
		}
	}
	res.add("fake streamers in use", fakeErr)
	if fakeErr != nil {
		return nil, false
	}

	c.watch = map[*rig]*callWatch{caller: watchCalls(caller), a: watchCalls(a), b: watchCalls(b)}
	return c, true
}

const sessionClosedTimeout = 60 * time.Second

func awaitSessionClosed(r *rig, peerID string) error {
	deadline := time.Now().Add(sessionClosedTimeout)
	for {
		if roundTripClosed(haomaLines(r), peerID) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s never logged its session with %s as up (no \"pairing round-trip closed\" line in %s; the line is at debug level)",
				r.name, short(peerID), sessionClosedTimeout)
		}
		rigSleep(time.Second, "poll: session round trip")
	}
}

func roundTripClosed(lines []string, peerID string) bool {
	if len(peerID) < 8 {
		return false
	}
	tag := "peer_id=" + peerID[:8]
	for _, l := range lines {
		if strings.Contains(l, "(pairing round-trip closed)") && strings.Contains(l, tag) {
			return true
		}
	}
	return false
}

func (c *callCast) findCallerSide() error {
	peers, err := listPeers(c.caller, syncConvergeTimeout)
	if err != nil {
		return err
	}
	fp := ""
	for _, p := range peers {
		if p.ID == c.callerSeesA {
			fp = p.Fingerprint
		}
	}
	if fp == "" {
		return fmt.Errorf("the caller lists no fingerprint for callee-a")
	}
	if err := awaitPairingCount(c.caller, fp, 2); err != nil {
		return err
	}
	if peers, err = listPeers(c.caller, syncConvergeTimeout); err != nil {
		return err
	}
	for _, p := range peers {
		if p.Fingerprint == fp && !p.Sibling && p.RetiredAt == 0 && p.ID != c.callerSeesA {
			c.callerSeesB = p.ID
		}
	}
	if c.callerSeesB == "" {
		return fmt.Errorf("the caller holds no second pairing with the contact")
	}
	chats, err := listChats(c.caller, syncConvergeTimeout)
	if err != nil {
		return err
	}
	ids := map[string]bool{}
	for _, ch := range chats {
		if ch.PeerID == c.callerSeesA || ch.PeerID == c.callerSeesB || (ch.Fingerprint != "" && ch.Fingerprint == fp) {
			ids[ch.ChatID] = true
			c.callerChat = ch.ChatID
		}
	}
	if len(ids) != 1 {
		return fmt.Errorf("the caller holds %d chats with the contact, want 1", len(ids))
	}
	return nil
}

func assertFakeStreamers(r *rig) error {
	lines, err := rigLogLines(r)
	if err != nil {
		return err
	}
	mic, ok := lastStreamersReady(lines)
	if !ok {
		return fmt.Errorf("%s's haoma found no streamers (no \"streamers ready\" line)", r.name)
	}
	if want := filepath.Join(r.streamerDir, "haoma-mic"); mic != want {
		return fmt.Errorf("%s's haoma runs the mic %s, want the fake %s", r.name, mic, want)
	}
	return nil
}

func lastStreamersReady(lines []string) (string, bool) {
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], `msg="streamers ready"`) {
			return logFieldValue(lines[i], "mic")
		}
	}
	return "", false
}

func startCall(r *rig, chatID string) (string, error) {
	sub, cancel := r.subscribe()
	defer cancel()
	id, err := r.sendReq(ipc.FrameStartCall, ipc.StartCallRequest{ChatID: chatID})
	if err != nil {
		return "", err
	}
	f, err := awaitFrame(sub, callReplyTimeout, func(f ipc.Frame) bool {
		return f.ID == id && (f.Type == ipc.FrameCallStarted || f.Type == ipc.FrameError)
	})
	if err != nil {
		return "", fmt.Errorf("%s start_call: %w", r.name, err)
	}
	if f.Type == ipc.FrameError {
		return "", frameErr(r.name+" start_call", f)
	}
	var started ipc.CallStartedResponse
	if err := json.Unmarshal(f.Payload, &started); err != nil || started.CallID == "" {
		return "", fmt.Errorf("%s start_call: no call id in the reply", r.name)
	}
	logf("[%s] call %s started", r.name, short(started.CallID))
	return started.CallID, nil
}

func respondCall(r *rig, callID, action string) (string, error) {
	sub, cancel := r.subscribe()
	defer cancel()
	id, err := r.sendReq(ipc.FrameRespondCall, ipc.RespondCallRequest{CallID: callID, Action: action})
	if err != nil {
		return "", err
	}
	f, err := awaitFrame(sub, callReplyTimeout, func(f ipc.Frame) bool {
		return f.ID == id && (f.Type == ipc.FrameCallResponded || f.Type == ipc.FrameError)
	})
	if err != nil {
		return "", fmt.Errorf("%s %s: %w", r.name, action, err)
	}
	if f.Type == ipc.FrameError {
		return "", frameErr(r.name+" "+action, f)
	}
	var resp ipc.CallRespondedResponse
	if err := json.Unmarshal(f.Payload, &resp); err != nil {
		return "", fmt.Errorf("%s %s: decode reply: %w", r.name, action, err)
	}
	logf("[%s] %s call %s: %s", r.name, action, short(callID), resp.Status)
	return resp.Status, nil
}

type callObs struct {
	at     time.Time
	state  *ipc.CallEntry
	stream *ipc.CallStreamEventPayload
	crumb  *events.CallSummaryBody

	crumbMsgID string
	crumbDir   events.Direction
}

func (o callObs) callID() string {
	switch {
	case o.state != nil:
		return o.state.CallID
	case o.stream != nil:
		return o.stream.CallID
	case o.crumb != nil:
		return o.crumb.CallID
	}
	return ""
}

func callObsOf(f ipc.Frame, at time.Time) (callObs, bool) {
	switch f.Type {
	case ipc.FrameCallStateChanged:
		var p ipc.CallStateChangedPayload
		if json.Unmarshal(f.Payload, &p) != nil || p.Call.CallID == "" {
			return callObs{}, false
		}
		return callObs{at: at, state: &p.Call}, true
	case ipc.FrameCallStreamEvent:
		var p ipc.CallStreamEventPayload
		if json.Unmarshal(f.Payload, &p) != nil || p.CallID == "" {
			return callObs{}, false
		}
		return callObs{at: at, stream: &p}, true
	case ipc.FrameTimelineEvent:
		ev, ok := timelineEvent(f)
		if !ok || ev.Kind != events.KindCallSummary {
			return callObs{}, false
		}
		var body events.CallSummaryBody
		if json.Unmarshal(ev.Body, &body) != nil || body.CallID == "" {
			return callObs{}, false
		}
		return callObs{at: at, crumb: &body, crumbMsgID: ev.MsgID, crumbDir: ev.Direction}, true
	}
	return callObs{}, false
}

type callWatch struct {
	r      *rig
	cancel func()
	stop   chan struct{}

	mu   sync.Mutex
	seen []callObs
}

func watchCalls(r *rig) *callWatch {
	sub, cancel := r.subscribe()
	w := &callWatch{r: r, cancel: cancel, stop: make(chan struct{})}
	go func() {
		for {
			select {
			case <-w.stop:
				return
			case f := <-sub:
				if o, ok := callObsOf(f, time.Now()); ok {
					w.mu.Lock()
					w.seen = append(w.seen, o)
					w.mu.Unlock()
				}
			}
		}
	}()
	return w
}

func (w *callWatch) close() {
	w.cancel()
	close(w.stop)
}

func (w *callWatch) snapshot() []callObs {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]callObs(nil), w.seen...)
}

func (w *callWatch) await(timeout time.Duration, what string, pred func(callObs) bool) (callObs, error) {
	deadline := time.Now().Add(timeout)
	for {
		for _, o := range w.snapshot() {
			if pred(o) {
				return o, nil
			}
		}
		if time.Now().After(deadline) {
			return callObs{}, fmt.Errorf("%s: no %s within %s (%s)", w.r.name, what, timeout, w.describe(""))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (w *callWatch) describe(callID string) string {
	var states []string
	for _, o := range w.snapshot() {
		if callID != "" && o.callID() != callID {
			continue
		}
		if o.state != nil {
			s := o.state.Status
			if o.state.FailReason != "" {
				s += "(" + o.state.FailReason + ")"
			}
			states = append(states, short(o.state.CallID)+":"+s)
		}
		if o.crumb != nil {
			states = append(states, short(o.crumb.CallID)+":crumb "+string(o.crumb.Outcome))
		}
	}
	if len(states) == 0 {
		return "saw no call state"
	}
	if len(states) > 12 {
		states = states[len(states)-12:]
	}
	return "saw " + strings.Join(states, ", ")
}

func (w *callWatch) awaitStatus(callID string, timeout time.Duration, statuses ...string) (callObs, error) {
	o, err := w.await(timeout, "call "+short(callID)+" "+strings.Join(statuses, "|"), func(o callObs) bool {
		if o.state == nil || o.state.CallID != callID {
			return false
		}
		for _, s := range statuses {
			if o.state.Status == s {
				return true
			}
		}
		return false
	})
	if err != nil {
		return o, fmt.Errorf("%w; for this call %s", err, w.describe(callID))
	}
	return o, nil
}

func (w *callWatch) awaitCrumb(callID string, want events.CallOutcome, timeout time.Duration) error {
	if _, err := w.await(timeout, "crumb for call "+short(callID), func(o callObs) bool {
		return o.crumb != nil && o.crumb.CallID == callID
	}); err != nil {
		return err
	}
	time.Sleep(callCrumbGrace)
	var got []string
	for _, o := range w.snapshot() {
		if o.crumb != nil && o.crumb.CallID == callID {
			got = append(got, string(o.crumb.Outcome))
		}
	}
	if len(got) != 1 || got[0] != string(want) {
		return fmt.Errorf("%s wrote crumb(s) %v for call %s, want one %q", w.r.name, got, short(callID), want)
	}
	return nil
}

func (w *callWatch) spkFramesIn(callID string) uint64 {
	var n uint64
	for _, o := range w.snapshot() {
		if o.stream != nil && o.stream.CallID == callID && o.stream.Side == "spk" && o.stream.Type == "stats" && o.stream.FramesIn > n {
			n = o.stream.FramesIn
		}
	}
	return n
}

func (w *callWatch) awaitSpkFrames(callID string, above uint64, timeout time.Duration) error {
	_, err := w.await(timeout, fmt.Sprintf("spk stats over %d frames in for call %s", above, short(callID)), func(o callObs) bool {
		return o.stream != nil && o.stream.CallID == callID && o.stream.Side == "spk" && o.stream.Type == "stats" && o.stream.FramesIn > above
	})
	return err
}

func (w *callWatch) sawRing(callID string) bool {
	for _, o := range w.snapshot() {
		if (o.state != nil || o.stream != nil) && o.callID() == callID {
			return true
		}
	}
	return false
}

func (w *callWatch) crumbsOf(callID string) []callObs {
	var out []callObs
	for _, o := range w.snapshot() {
		if o.crumb != nil && o.crumb.CallID == callID {
			out = append(out, o)
		}
	}
	return out
}

func syncedCrumb(from, to *callWatch, callID string, want events.CallOutcome, timeout time.Duration, toLog func() []string) error {
	origin := from.crumbsOf(callID)
	if len(origin) != 1 {
		return fmt.Errorf("%s holds %d crumbs for call %s, want 1", from.r.name, len(origin), short(callID))
	}
	o := origin[0]
	if o.crumbMsgID == "" || o.crumbDir != events.DirOut {
		return fmt.Errorf("%s's crumb for call %s has msg id %q and direction %q, want a msg id and out",
			from.r.name, short(callID), o.crumbMsgID, o.crumbDir)
	}
	if _, err := to.await(timeout, "crumb for call "+short(callID)+" from "+from.r.name, func(x callObs) bool {
		return x.crumb != nil && x.crumb.CallID == callID
	}); err != nil {
		return err
	}
	time.Sleep(callCrumbGrace)
	got := to.crumbsOf(callID)
	if len(got) != 1 {
		return fmt.Errorf("%s holds %d crumbs for call %s, want 1", to.r.name, len(got), short(callID))
	}
	g := got[0]
	switch {
	case g.crumbMsgID != o.crumbMsgID:
		return fmt.Errorf("%s's crumb for call %s has msg id %q, %s's has %q",
			to.r.name, short(callID), g.crumbMsgID, from.r.name, o.crumbMsgID)
	case g.crumbDir != events.DirOut:
		return fmt.Errorf("%s's crumb for call %s has direction %q, want out", to.r.name, short(callID), g.crumbDir)
	case g.crumb.Outcome != want:
		return fmt.Errorf("%s's crumb for call %s says %q, want %q", to.r.name, short(callID), g.crumb.Outcome, want)
	}
	deadline := time.Now().Add(timeout)
	for !pushLineFor(toLog(), o.crumbMsgID) {
		if time.Now().After(deadline) {
			return fmt.Errorf("%s holds the crumb for call %s but never logged the rail push for msg id %s (no %q or %q line within %s)",
				to.r.name, short(callID), short(o.crumbMsgID), pushAppliedLine, pushHeldLine, timeout)
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil
}

const (
	pushAppliedLine = "rail: call summary applied"
	pushHeldLine    = "rail: call summary already held"
)

func pushLineFor(lines []string, msgID string) bool {
	if len(msgID) < 8 {
		return false
	}
	tag := "msg_id=" + msgID[:8]
	for _, l := range lines {
		if strings.Contains(l, tag) && (strings.Contains(l, pushAppliedLine) || strings.Contains(l, pushHeldLine)) {
			return true
		}
	}
	return false
}

func (w *callWatch) sawStream(callID string) bool {
	for _, o := range w.snapshot() {
		if o.stream != nil && o.stream.CallID == callID {
			return true
		}
	}
	return false
}

type ringing struct {
	callID  string
	started time.Time
	rang    map[*rig]time.Time
}

func (c *callCast) ringBoth(label string) (ringing, error) {
	mark := haomaLogMark(c.caller)
	callID, err := startCall(c.caller, c.callerChat)
	if err != nil {
		return ringing{}, err
	}
	started := time.Now()
	rang := map[*rig]time.Time{}
	var ringErr error
	for _, r := range []*rig{c.a, c.b} {
		o, err := c.watch[r].awaitStatus(callID, callSignalTimeout, string(calls.StatusRinging))
		if err != nil {
			if ringErr == nil {
				ringErr = err
			}
			continue
		}
		if o.state.Direction != string(calls.DirIn) {
			ringErr = fmt.Errorf("%s rings with direction %q", r.name, o.state.Direction)
		}
		rang[r] = o.at
	}
	lines, _ := mark.lines()
	counts, found := offerCounts(lines, callID)
	switch {
	case ringErr != nil:
		ringErr = fmt.Errorf("%w (caller's offer line: %s)", ringErr, counts)
	case !found:
		ringErr = fmt.Errorf("the caller logged no offer line for call %s", short(callID))
	case counts != (offered{targets: 2, offered: 2}):
		ringErr = fmt.Errorf("the caller's offer line says %s, want targets=2 offered=2 skipped=0", counts)
	default:
		logf("[call] %s: call %s rings on callee-a and callee-b", label, short(callID))
	}
	return ringing{callID: callID, started: started, rang: rang}, ringErr
}

type offered struct{ targets, offered, skipped int }

func (o offered) String() string {
	return fmt.Sprintf("targets=%d offered=%d skipped=%d", o.targets, o.offered, o.skipped)
}

func offerCounts(lines []string, callID string) (offered, bool) {
	if len(callID) < 8 {
		return offered{}, false
	}
	tag := "call_id=" + callID[:8]
	for _, l := range lines {
		if !strings.Contains(l, "call offered to every device of the contact") || !strings.Contains(l, tag) {
			continue
		}
		num := func(key string) int {
			v, _ := logFieldValue(l, key)
			n, err := strconv.Atoi(v)
			if err != nil {
				return -1
			}
			return n
		}
		return offered{targets: num("targets"), offered: num("offered"), skipped: num("skipped")}, true
	}
	return offered{}, false
}

func (c *callCast) awaitEnded(callID string, rigs ...*rig) bool {
	ok := true
	for _, r := range rigs {
		if _, err := c.watch[r].awaitStatus(callID, callSignalTimeout+callerNoAnswer,
			string(calls.StatusEnded), string(calls.StatusRejected), string(calls.StatusFailed)); err != nil {
			logf("[call] call %s did not end on %s: %v", short(callID), r.name, err)
			ok = false
		}
	}
	return ok
}

func (c *callCast) caseAccept(res *pairingResult) bool {

	aHaoma := haomaLogMark(c.a)
	aHaomad := markLog(filepath.Join(c.a.root, "haomad.log"))
	rg, err := c.ringBoth("accept")
	callID := rg.callID
	res.add("accept: rings both devices", err)
	if callID == "" {
		return true
	}
	if err != nil {

		_, _ = respondCall(c.caller, callID, ipc.CallActionEnd)
		return c.awaitEnded(callID, c.caller)
	}

	status, err := respondCall(c.b, callID, ipc.CallActionAccept)
	if err == nil && status != string(calls.StatusAccepted) {
		err = fmt.Errorf("callee-b's answer left the call %s", status)
	}
	if err == nil {
		_, err = c.watch[c.caller].awaitStatus(callID, callSignalTimeout, string(calls.StatusAccepted))
	}
	res.add("accept: callee-b answers", err)
	if err != nil {
		_, _ = respondCall(c.caller, callID, ipc.CallActionEnd)
		return c.awaitEnded(callID, c.caller, c.a, c.b)
	}

	o, err := c.watch[c.a].awaitStatus(callID, callSignalTimeout, string(calls.StatusEnded), string(calls.StatusRejected), string(calls.StatusFailed))
	if err == nil && (o.state.Status != string(calls.StatusEnded) || o.state.FailReason != "answered_elsewhere") {
		err = fmt.Errorf("callee-a's call went %s (%q), want ended (answered_elsewhere)", o.state.Status, o.state.FailReason)
	}
	res.add("accept: callee-a told answered elsewhere", err)
	res.add("accept: callee-a crumb", c.watch[c.a].awaitCrumb(callID, events.CallOutcomeAnsweredElsewhere, callSignalTimeout))

	audioErr := c.watch[c.caller].awaitSpkFrames(callID, 0, callAudioTimeout)
	if err := c.watch[c.b].awaitSpkFrames(callID, 0, callAudioTimeout); err != nil && audioErr == nil {
		audioErr = err
	}
	res.add("accept: audio both ways", audioErr)

	probeToken := ""
	if audioErr == nil {
		var err error
		probeToken, err = c.secondFetch(callID)
		res.add("accept: second fetch gets 409", err)
	} else {
		res.add("accept: second fetch gets 409", fmt.Errorf("not asserted: no stream was connected"))
	}

	hl, _ := aHaoma.lines()
	dl, _ := aHaomad.lines()
	if found := mediaStartLines(hl, dl, callID, probeToken); len(found) > 0 {
		res.add("accept: the device that did not answer starts no audio", fmt.Errorf("callee-a set up media for the call another device answered: %s", strings.Join(found, " | ")))
	} else {
		res.add("accept: the device that did not answer starts no audio", nil)
	}

	status, err = respondCall(c.caller, callID, ipc.CallActionEnd)
	if err == nil && status != string(calls.StatusEnded) {
		err = fmt.Errorf("the caller's hang-up left the call %s", status)
	}
	if err == nil {
		_, err = c.watch[c.b].awaitStatus(callID, callSignalTimeout, string(calls.StatusEnded))
	}
	if err == nil {
		err = c.watch[c.caller].awaitCrumb(callID, events.CallOutcomeCompleted, callSignalTimeout)
	}
	if err == nil {
		err = c.watch[c.b].awaitCrumb(callID, events.CallOutcomeCompleted, callSignalTimeout)
	}
	res.add("accept: hang-up ends both", err)
	return c.awaitEnded(callID, c.caller, c.a, c.b)
}

func (c *callCast) secondFetch(callID string) (string, error) {
	token, err := remoteToken(c.a, callID)
	if err != nil {
		return "", err
	}
	return token, c.fetchAgain(callID, token)
}

func (c *callCast) fetchAgain(callID, token string) error {
	bc, err := backendClientFor(c.a)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), fetchProbeTimeout)
	defer cancel()
	peer, err := bc.Peer(ctx, c.aSeesCaller)
	if err != nil {
		return fmt.Errorf("callee-a's haomad has no record of the caller: %w", err)
	}
	if len(peer.KnownAddresses) == 0 {
		return fmt.Errorf("callee-a's haomad knows no address of the caller")
	}
	url := streamURL(peer.KnownAddresses[0], "audio", token)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer ln.Close()
	took := make(chan struct{}, 1)
	go func() {
		if conn, err := ln.Accept(); err == nil {
			took <- struct{}{}
			conn.Close()
		}
	}()

	before := c.watch[c.b].spkFramesIn(callID)
	mark := markLog(filepath.Join(c.a.root, "haomad.log"))
	if err := bc.ProxyFetch(ctx, backendapi.ProxyFetchRequest{
		Token:     token,
		Modality:  "audio",
		PeerURL:   url,
		LocalPort: ln.Addr().(*net.TCPAddr).Port,
	}); err != nil {
		return fmt.Errorf("callee-a's haomad refused the fetch: %w", err)
	}
	defer func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		_ = bc.ProxyCancel(cctx, token)
	}()

	var outcome fetchOutcome
	deadline := time.After(fetchProbeTimeout)
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
wait:
	for {
		select {
		case <-took:
			return fmt.Errorf("the second fetch was given the stream: haomad dialled the listener")
		case <-deadline:
			break wait
		case <-tick.C:
			lines, _ := mark.lines()
			if outcome = fetchOutcomeIn(lines, token); outcome.kind != "" {
				break wait
			}
		}
	}
	select {
	case <-took:
		return fmt.Errorf("the second fetch was given the stream: haomad dialled the listener (%s)", outcome)
	case <-time.After(2 * time.Second):
	}
	if outcome.kind != "status" || outcome.status != 409 {
		return fmt.Errorf("the second fetch got %s, want status 409", outcome)
	}
	logf("[call] the second fetch of the caller's stream got 409")

	if err := c.watch[c.b].awaitSpkFrames(callID, before+50, 30*time.Second); err != nil {
		return fmt.Errorf("callee-b's sound stopped after the second fetch: %w", err)
	}
	return nil
}

func streamURL(address, modality, token string) string {
	return fmt.Sprintf("http://%s.onion/%s/%s", strings.TrimSuffix(address, ".onion"), modality, token)
}

type fetchOutcome struct {
	kind   string
	status int
}

func (o fetchOutcome) String() string {
	switch o.kind {
	case "":
		return "no answer in haomad's log"
	case "status":
		return "status " + strconv.Itoa(o.status)
	}
	return o.kind
}

func fetchOutcomeIn(lines []string, token string) fetchOutcome {
	if len(token) < 8 {
		return fetchOutcome{}
	}
	tag := "token=" + token[:8]
	for _, l := range lines {
		if !strings.Contains(l, tag) {
			continue
		}
		switch {
		case strings.Contains(l, "proxy fetch: non-200 from peer"):
			v, _ := logFieldValue(l, "status")
			n, _ := strconv.Atoi(v)
			return fetchOutcome{kind: "status", status: n}
		case strings.Contains(l, "proxy fetch: GET failed"):
			return fetchOutcome{kind: "get failed"}
		case strings.Contains(l, "proxy fetch finished"):
			return fetchOutcome{kind: "finished"}
		}
	}
	return fetchOutcome{}
}

func remoteToken(r *rig, callID string) (string, error) {
	if err := r.stopHaoma(); err != nil {
		return "", err
	}
	token, readErr := func() (string, error) {
		st, err := store.UnlockReadOnly(r.frontendDir(), r.fePass)
		if err != nil {
			return "", fmt.Errorf("open %s's store: %w", r.name, err)
		}
		defer st.Lock()
		m, err := calls.NewManager(st)
		if err != nil {
			return "", err
		}
		s, err := m.GetState(callID)
		if err != nil {
			return "", fmt.Errorf("%s holds no row for call %s: %w", r.name, short(callID), err)
		}
		t := s.RemoteTokens["audio"]
		if t == "" {
			return "", fmt.Errorf("%s's row for call %s holds no audio token", r.name, short(callID))
		}
		return t, nil
	}()
	if err := r.startHaoma(); err != nil {
		return "", fmt.Errorf("start %s's haoma again: %w", r.name, err)
	}
	return token, readErr
}

func (c *callCast) caseDecline(res *pairingResult) bool {
	rg, err := c.ringBoth("decline")
	callID := rg.callID
	res.add("decline: rings both devices", err)
	if callID == "" {
		return true
	}
	if err != nil {
		_, _ = respondCall(c.caller, callID, ipc.CallActionEnd)
		return c.awaitEnded(callID, c.caller)
	}

	status, err := respondCall(c.a, callID, ipc.CallActionReject)
	if err == nil && status != string(calls.StatusRejected) {
		err = fmt.Errorf("callee-a's decline left the call %s", status)
	}
	if err == nil {
		_, err = c.watch[c.caller].awaitStatus(callID, callSignalTimeout, string(calls.StatusRejected))
	}
	if err == nil {
		err = c.watch[c.caller].awaitCrumb(callID, events.CallOutcomeRejected, callSignalTimeout)
	}
	if err == nil {
		err = c.watch[c.a].awaitCrumb(callID, events.CallOutcomeRejected, callSignalTimeout)
	}
	res.add("decline: callee-a declines", err)

	o, err := c.watch[c.b].awaitStatus(callID, callSignalTimeout, string(calls.StatusEnded), string(calls.StatusRejected), string(calls.StatusFailed))
	if err == nil && o.state.Status != string(calls.StatusEnded) {
		err = fmt.Errorf("callee-b's call went %s, want ended", o.state.Status)
	}
	if err == nil {
		err = c.watch[c.b].awaitCrumb(callID, events.CallOutcomeMissed, callSignalTimeout)
	}
	res.add("decline: callee-b stops", err)
	return c.awaitEnded(callID, c.caller, c.a, c.b)
}

func (c *callCast) caseNoAnswer(res *pairingResult) bool {
	rg, err := c.ringBoth("no answer")
	callID, rang, started := rg.callID, rg.rang, rg.started
	res.add("no answer: rings both devices", err)
	if callID == "" {
		return true
	}

	o, err := c.watch[c.caller].awaitStatus(callID, callerNoAnswer+callSignalTimeout,
		string(calls.StatusEnded), string(calls.StatusRejected), string(calls.StatusFailed))
	if err == nil {
		err = noAnswerEnd(o, started)
	}
	res.add("no answer: caller ends at its timer", err)

	var stopErr error
	for _, r := range []*rig{c.a, c.b} {
		o, err := c.watch[r].awaitStatus(callID, calleeRingOut+callSignalTimeout,
			string(calls.StatusEnded), string(calls.StatusRejected), string(calls.StatusFailed))
		if err == nil {
			err = deviceStoppedBy(r.name, o, rang[r], calleeRingOut+15*time.Second)
		}
		if err == nil {
			err = c.watch[r].awaitCrumb(callID, events.CallOutcomeMissed, callSignalTimeout)
		}
		if err != nil && stopErr == nil {
			stopErr = err
		}
		if err == nil {
			logf("[call] no answer: %s stopped %s after it rang (%s)", r.name,
				o.at.Sub(rang[r]).Truncate(time.Second), ringStopCause(r, callID))
		}
	}
	res.add("no answer: both devices stop", stopErr)
	return c.awaitEnded(callID, c.caller, c.a, c.b)
}

func noAnswerEnd(o callObs, started time.Time) error {
	if o.state.Status != string(calls.StatusFailed) || o.state.FailReason != calls.FailReasonNoAnswer {
		return fmt.Errorf("the caller's call went %s (%q), want failed (%s)", o.state.Status, o.state.FailReason, calls.FailReasonNoAnswer)
	}
	after := o.at.Sub(started)
	if after < callerNoAnswer-5*time.Second || after > callerNoAnswer+20*time.Second {
		return fmt.Errorf("the caller ended %s after the start, want about %s", after.Truncate(time.Second), callerNoAnswer)
	}
	return nil
}

func deviceStoppedBy(name string, o callObs, rang time.Time, limit time.Duration) error {
	if o.state.Status != string(calls.StatusEnded) || o.state.FailReason != "" {
		return fmt.Errorf("%s's call went %s (%q), want ended", name, o.state.Status, o.state.FailReason)
	}
	if after := o.at.Sub(rang); after > limit {
		return fmt.Errorf("%s stopped %s after it rang, want at most %s", name, after.Truncate(time.Second), limit)
	}
	return nil
}

func ringStopCause(r *rig, callID string) string {
	if ringTimeoutLogged(haomaLines(r), callID) {
		return "its own ring timer"
	}
	return "the caller's end"
}

func ringTimeoutLogged(lines []string, callID string) bool {
	if len(callID) < 8 {
		return false
	}
	tag := "call_id=" + callID[:8]
	for _, l := range lines {
		if strings.Contains(l, "call stopped ringing here (ring timeout); nothing sent") && strings.Contains(l, tag) {
			return true
		}
	}
	return false
}

func (c *callCast) caseRingTimer(res *pairingResult) bool {
	rg, err := c.ringBoth("ring timer")
	callID, rang, started := rg.callID, rg.rang, rg.started
	res.add("ring timer: rings both devices", err)
	if callID == "" {
		return true
	}
	if err != nil {
		_, _ = respondCall(c.caller, callID, ipc.CallActionEnd)
		return c.awaitEnded(callID, c.caller)
	}

	if since := time.Since(started); since > callerNoAnswer-5*time.Second {
		res.add("ring timer: devices stop on their own timer",
			fmt.Errorf("not asserted: the devices rang %s after the call_started reply, too close to the caller's own timer", since.Truncate(time.Second)))
		res.add("ring timer: caller comes back", fmt.Errorf("not asserted: the caller was not stopped"))
		return c.awaitEnded(callID, c.caller, c.a, c.b)
	}
	if err := c.caller.stopHaoma(); err != nil {
		res.add("ring timer: devices stop on their own timer", fmt.Errorf("not asserted: stop the caller's haoma: %w", err))
		res.add("ring timer: caller comes back", err)
		return false
	}
	logf("[call] ring timer: the caller's haoma is down; the devices must stop on their own")

	var stopErr error
	for _, r := range []*rig{c.a, c.b} {
		o, err := c.watch[r].awaitStatus(callID, calleeRingOut+callSignalTimeout,
			string(calls.StatusEnded), string(calls.StatusRejected), string(calls.StatusFailed))
		if err == nil {
			err = ownTimerStop(r.name, o, rang[r])
		}
		if err == nil && !ringTimeoutLogged(haomaLines(r), callID) {
			err = fmt.Errorf("%s stopped, but its haoma did not log its ring timer", r.name)
		}
		if err == nil {
			err = c.watch[r].awaitCrumb(callID, events.CallOutcomeMissed, callSignalTimeout)
		}
		if err != nil && stopErr == nil {
			stopErr = err
		}
	}
	res.add("ring timer: devices stop on their own timer", stopErr)

	err = c.caller.startHaoma()
	if err == nil {

		err = c.callerReaches()
	}
	res.add("ring timer: caller comes back", err)
	if err != nil {
		return false
	}

	return c.awaitEnded(callID, c.a, c.b)
}

func (c *callCast) callerReaches() error {
	for _, t := range []struct {
		from, to *rig
		peer     string
	}{
		{c.caller, c.a, c.callerSeesA},
		{c.a, c.caller, c.aSeesCaller},
		{c.caller, c.b, c.callerSeesB},
		{c.b, c.caller, c.bSeesCaller},
	} {
		if _, err := deliverText(t.from, t.to, t.peer, "after the restart "+nextCorrID()); err != nil {
			return fmt.Errorf("%s -> %s after the caller's restart: %w", t.from.name, t.to.name, err)
		}
	}
	return nil
}

func mediaStartLines(haoma, haomad []string, callID, exceptToken string) []string {
	var out []string
	if len(callID) >= 8 {
		tag := "call_id=" + callID[:8]
		for _, l := range haoma {
			if !strings.Contains(l, tag) {
				continue
			}
			for _, m := range []string{`msg="streamer spawned"`, `msg="call sender leg `, `msg="call receiver leg `} {
				if strings.Contains(l, m) {
					out = append(out, "haoma: "+trimTime(l))
					break
				}
			}
		}
	}
	skip := ""
	if len(exceptToken) >= 8 {
		skip = "token=" + exceptToken[:8]
	}
	for _, l := range haomad {
		if !strings.Contains(l, `msg="proxy serve registered"`) && !strings.Contains(l, `msg="proxy fetch starting"`) {
			continue
		}
		if skip != "" && strings.Contains(l, skip) {
			continue
		}
		out = append(out, "haomad: "+trimTime(l))
	}
	return out
}

func ownTimerStop(name string, o callObs, rang time.Time) error {
	if o.state.Status != string(calls.StatusEnded) || o.state.FailReason != "" {
		return fmt.Errorf("%s's call went %s (%q), want ended", name, o.state.Status, o.state.FailReason)
	}
	after := o.at.Sub(rang)
	if after < calleeRingOut-5*time.Second || after > calleeRingOut+20*time.Second {
		return fmt.Errorf("%s stopped %s after it rang, want about %s", name, after.Truncate(time.Second), calleeRingOut)
	}
	return nil
}

func (c *callCast) caseHangUp(res *pairingResult) bool {
	rg, err := c.ringBoth("hang-up")
	callID, rang := rg.callID, rg.rang
	res.add("hang-up: rings both devices", err)
	if callID == "" {
		return true
	}
	status, err := respondCall(c.caller, callID, ipc.CallActionEnd)
	if err == nil && status != string(calls.StatusEnded) {
		err = fmt.Errorf("the caller's hang-up left the call %s", status)
	}
	if err == nil {
		err = c.watch[c.caller].awaitCrumb(callID, events.CallOutcomeCancelled, callSignalTimeout)
	}
	for _, r := range []*rig{c.a, c.b} {
		if err != nil {
			break
		}
		var o callObs
		o, err = c.watch[r].awaitStatus(callID, callSignalTimeout, string(calls.StatusEnded), string(calls.StatusRejected), string(calls.StatusFailed))
		if err == nil {

			err = deviceStoppedBy(r.name, o, rang[r], calleeRingOut-10*time.Second)
		}
		if err == nil {
			err = c.watch[r].awaitCrumb(callID, events.CallOutcomeMissed, callSignalTimeout)
		}
		if err == nil && ringTimeoutLogged(haomaLines(r), callID) {
			err = fmt.Errorf("%s stopped on its own ring timer, not on the caller's end", r.name)
		}
	}
	res.add("hang-up: both devices stop", err)
	return c.awaitEnded(callID, c.caller, c.a, c.b)
}

func haomaLines(r *rig) []string {
	lines, _ := rigLogLines(r)
	return lines
}

func (c *callCast) caseReverse(res *pairingResult) {
	callID, err := startCall(c.b, c.bChat)
	if err == nil {
		_, err = c.watch[c.caller].awaitStatus(callID, callSignalTimeout, string(calls.StatusRinging))
	}
	res.add("reverse: caller rings", err)
	if err != nil {
		if callID != "" {
			_, _ = respondCall(c.b, callID, ipc.CallActionEnd)
			c.awaitEnded(callID, c.b)
		}
		return
	}

	status, err := respondCall(c.caller, callID, ipc.CallActionAccept)
	if err == nil && status != string(calls.StatusAccepted) {
		err = fmt.Errorf("the caller's answer left the call %s", status)
	}
	if err == nil {
		_, err = c.watch[c.b].awaitStatus(callID, callSignalTimeout, string(calls.StatusAccepted))
	}
	res.add("reverse: caller answers", err)

	audioErr := err
	if audioErr == nil {
		audioErr = c.watch[c.b].awaitSpkFrames(callID, 0, callAudioTimeout)
	}
	if audioErr == nil {
		audioErr = c.watch[c.caller].awaitSpkFrames(callID, 0, callAudioTimeout)
	}
	res.add("reverse: audio both ways", audioErr)

	status, err = respondCall(c.b, callID, ipc.CallActionEnd)
	if err == nil && status != string(calls.StatusEnded) {
		err = fmt.Errorf("callee-b's hang-up left the call %s", status)
	}
	if err == nil {
		_, err = c.watch[c.caller].awaitStatus(callID, callSignalTimeout, string(calls.StatusEnded))
	}
	if err == nil {
		err = c.watch[c.caller].awaitCrumb(callID, events.CallOutcomeCompleted, callSignalTimeout)
	}
	if err == nil {
		err = c.watch[c.b].awaitCrumb(callID, events.CallOutcomeCompleted, callSignalTimeout)
	}
	res.add("reverse: hang-up ends both", err)

	crumbErr := err
	if crumbErr != nil {
		crumbErr = fmt.Errorf("not asserted: the hang-up step failed: %w", err)
	} else {
		crumbErr = syncedCrumb(c.watch[c.b], c.watch[c.a], callID, events.CallOutcomeCompleted, callSignalTimeout,
			func() []string { return haomaLines(c.a) })
	}
	res.add("reverse: callee-a holds callee-b's crumb", crumbErr)
	if crumbErr == nil {
		logf("[call] reverse: callee-a's crumb came %s", crumbRoute(haomaLines(c.a), c.watch[c.b].crumbsOf(callID)[0].crumbMsgID))
	}

	if c.watch[c.a].sawRing(callID) {
		res.add("reverse: callee-a does not ring", fmt.Errorf("callee-a saw callee-b's own call: %s", c.watch[c.a].describe(callID)))
	} else {
		res.add("reverse: callee-a does not ring", nil)
	}
	c.awaitEnded(callID, c.caller, c.b)
}

func crumbRoute(lines []string, msgID string) string {
	if len(msgID) >= 8 {
		tag := "msg_id=" + msgID[:8]
		for _, l := range lines {
			if strings.Contains(l, pushAppliedLine) && strings.Contains(l, tag) {
				return "by the rail push"
			}
		}
	}
	return "by gap-fill (no \"rail: call summary applied\" line for it)"
}

func buildFakeStreamers(repoRoot, dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	mic := filepath.Join(dir, "haoma-mic")
	logf("building ./cmd/haoma-rig/fakestreamer → %s", mic)
	cmd := exec.Command("go", "build", "-o", mic, "./cmd/haoma-rig/fakestreamer")
	cmd.Dir = filepath.Join(repoRoot, "frontend")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("build fakestreamer: %w\n%s", err, out)
	}
	return copyExecutable(mic, filepath.Join(dir, "haoma-spk"))
}

func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func checkFakeStreamers(dir string) error {
	for _, name := range []string{"haoma-mic", "haoma-spk"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("fake streamer missing: %s (run with -build)", filepath.Join(dir, name))
			}
			return err
		}
	}
	return nil
}
