package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"haoma-frontend/internal/calls"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/session"
	"haoma-frontend/internal/streamers"
)

type callDevice struct {
	id     string
	cipher *session.Cipher
	seq    uint64
	opened int
}

type callRig struct {
	t    *testing.T
	d    *daemon
	stub *haomadStub
	addr string
	cert string
	tok  string
	env  int
}

func newCallRig(t *testing.T) *callRig {
	t.Helper()
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, cert, tok := newTestDaemon(t, stub)
	mgr, err := calls.NewManager(d.store)
	if err != nil {
		t.Fatal(err)
	}
	d.calls = mgr
	return &callRig{t: t, d: d, stub: stub, addr: addr, cert: cert, tok: tok}
}

func (r *callRig) connect(id string, opk int) *callDevice {
	r.t.Helper()
	msgID, err := msg.NewID()
	if err != nil {
		r.t.Fatal(err)
	}
	w, err := msg.BuildText(1, time.Now().Unix(), msgID, "hello", 0, "", "", nil, 0)
	if err != nil {
		r.t.Fatal(err)
	}
	blob, c := encryptAsAliceOnOPK(r.t, r.d, id, w, opk)
	r.feed(id, blob)
	return &callDevice{id: id, cipher: c, seq: 1}
}

func (r *callRig) feed(peerID string, blob []byte) {
	r.t.Helper()
	r.env++
	processInboxEntry(context.Background(), r.d, railEntry(peerID, fmt.Sprintf("env-call-%d", r.env), blob))
}

func (r *callRig) deliver(dev *callDevice, w *msg.Wrapper) {
	r.t.Helper()
	plain, err := msg.Marshal(w)
	if err != nil {
		r.t.Fatal(err)
	}
	blob, err := dev.cipher.Encrypt(context.Background(), "bob-self", plain)
	if err != nil {
		r.t.Fatalf("device %s encrypt: %v", dev.id, err)
	}
	r.feed(dev.id, blob)
}

func (dev *callDevice) next() uint64 { dev.seq++; return dev.seq }

func (r *callRig) callMsgs(dev *callDevice) []*msg.Wrapper {
	r.t.Helper()
	r.stub.sendMu.Lock()
	peers := append([]string(nil), r.stub.SentPeerIDs...)
	payloads := append([][]byte(nil), r.stub.SentPayloads...)
	kinds := append([]string(nil), r.stub.SentKinds...)
	r.stub.sendMu.Unlock()
	var out []*msg.Wrapper
	for i := dev.opened; i < len(peers); i++ {
		if peers[i] != dev.id {
			continue
		}
		if kinds[i] != "" {

			continue
		}
		plain, err := dev.cipher.Decrypt(context.Background(), "bob-self", payloads[i])
		if err != nil {
			r.t.Fatalf("device %s could not open send %d: %v", dev.id, i, err)
		}
		w, err := msg.Unmarshal(plain)
		if err != nil {
			r.t.Fatal(err)
		}
		switch w.Kind {
		case msg.KindCallOffer, msg.KindCallAccept, msg.KindCallReject, msg.KindCallEnd:
			out = append(out, w)
		}
	}
	dev.opened = len(peers)
	return out
}

func (r *callRig) sentTo(peerID string) bool {
	r.stub.sendMu.Lock()
	defer r.stub.sendMu.Unlock()
	for _, p := range r.stub.SentPeerIDs {
		if p == peerID {
			return true
		}
	}
	return false
}

func (r *callRig) contactWith(ids ...string) *chat.DirectChat {
	r.t.Helper()
	dc, err := r.d.chats.CreateDirect(ids[0])
	if err != nil {
		r.t.Fatal(err)
	}
	for _, id := range ids[1:] {
		if err := r.d.chats.AttachPeerID(dc.ID, id); err != nil {
			r.t.Fatalf("AttachPeerID: %v", err)
		}
	}
	dc, err = r.d.chats.GetByDirectPeer(ids[0])
	if err != nil {
		r.t.Fatal(err)
	}
	if len(dc.FanoutPeerIDs()) != len(ids) {
		r.t.Fatalf("routing set = %v, want %v", dc.FanoutPeerIDs(), ids)
	}
	return dc
}

func (r *callRig) placeCall(dc *chat.DirectChat) calls.State {
	r.t.Helper()
	callID, err := calls.NewCallID()
	if err != nil {
		r.t.Fatal(err)
	}
	modalities := []string{msg.ModalityAudio}
	key, tokens, err := mintCallSecrets(modalities)
	if err != nil {
		r.t.Fatal(err)
	}
	state := calls.State{
		CallID:      callID,
		ChatID:      dc.ID,
		PeerID:      dc.PeerID,
		Direction:   calls.DirOut,
		Status:      calls.StatusOffered,
		Modalities:  modalities,
		StartedAt:   time.Now().Unix(),
		LocalTokens: tokens,
	}
	st, code, err := startCallOffer(context.Background(), r.d, dc, state, key, "bob")
	if err != nil {
		r.t.Fatalf("startCallOffer: code=%s err=%v", code, err)
	}
	return st
}

func (r *callRig) state(callID string) calls.State {
	r.t.Helper()
	s, err := r.d.calls.GetState(callID)
	if err != nil {
		r.t.Fatalf("GetState: %v", err)
	}
	return s
}

func (r *callRig) accept(dev *callDevice, callID string) {
	r.t.Helper()
	key, tokens, err := mintCallSecrets([]string{msg.ModalityAudio})
	if err != nil {
		r.t.Fatal(err)
	}
	msgID, _ := msg.NewID()
	w, err := msg.BuildCallAccept(dev.next(), time.Now().Unix(), msgID, callID, []string{msg.ModalityAudio}, tokens, key, "", 0)
	if err != nil {
		r.t.Fatal(err)
	}
	r.deliver(dev, w)
}

func (r *callRig) reject(dev *callDevice, callID string) {
	r.t.Helper()
	msgID, _ := msg.NewID()
	w, err := msg.BuildCallReject(dev.next(), time.Now().Unix(), msgID, callID, msg.CallRejectUserDeclined, 0)
	if err != nil {
		r.t.Fatal(err)
	}
	r.deliver(dev, w)
}

func (r *callRig) end(dev *callDevice, callID, reason string) {
	r.t.Helper()
	msgID, _ := msg.NewID()
	w, err := msg.BuildCallEnd(dev.next(), time.Now().Unix(), msgID, callID, reason, 0)
	if err != nil {
		r.t.Fatal(err)
	}
	r.deliver(dev, w)
}

func (r *callRig) offer(dev *callDevice, callID string, ts int64) {
	r.t.Helper()
	key, tokens, err := mintCallSecrets([]string{msg.ModalityAudio})
	if err != nil {
		r.t.Fatal(err)
	}
	msgID, _ := msg.NewID()
	w, err := msg.BuildCallOffer(dev.next(), ts, msgID, callID, []string{msg.ModalityAudio}, tokens, key, "alice", 0)
	if err != nil {
		r.t.Fatal(err)
	}
	r.deliver(dev, w)
}

func wantEnd(t *testing.T, who string, ws []*msg.Wrapper, callID, reason string) {
	t.Helper()
	if len(ws) != 1 {
		t.Fatalf("%s got %d call messages, want exactly one end: %v", who, len(ws), kindsOf(ws))
	}
	if ws[0].Kind != msg.KindCallEnd {
		t.Fatalf("%s got %s, want %s", who, ws[0].Kind, msg.KindCallEnd)
	}
	body, err := ws[0].CallEnd()
	if err != nil {
		t.Fatal(err)
	}
	if body.CallID != callID {
		t.Fatalf("%s got an end for call %q, want %q", who, body.CallID, callID)
	}
	if body.Reason != reason {
		t.Fatalf("%s got an end with reason %q, want %q", who, body.Reason, reason)
	}
}

func wantNone(t *testing.T, who string, ws []*msg.Wrapper) {
	t.Helper()
	if len(ws) != 0 {
		t.Fatalf("%s got call messages %v, want none", who, kindsOf(ws))
	}
}

func kindsOf(ws []*msg.Wrapper) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = string(w.Kind)
		if w.Kind == msg.KindCallEnd {
			if b, err := w.CallEnd(); err == nil && b.Reason != "" {
				out[i] += "(" + b.Reason + ")"
			}
		}
	}
	return out
}

type receiverSpawns struct {
	mu    sync.Mutex
	peers []string
}

func stubCallReceiver(t *testing.T) *receiverSpawns {
	t.Helper()
	rec := &receiverSpawns{}
	prev := spawnCallReceiver
	spawnCallReceiver = func(_ context.Context, _ *daemon, _, peerID, _, _ string, _ []byte) (int, error) {
		rec.mu.Lock()
		rec.peers = append(rec.peers, peerID)
		rec.mu.Unlock()
		return 1, nil
	}
	t.Cleanup(func() { spawnCallReceiver = prev })
	return rec
}

func (l *receiverSpawns) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.peers...)
}

func teardownOrder(t *testing.T, r *callRig) func() []int {
	t.Helper()
	var mu sync.Mutex
	var at []int
	prev := callTeardown
	callTeardown = func(d *daemon, st calls.State) {
		r.stub.sendMu.Lock()
		n := len(r.stub.SentPeerIDs)
		r.stub.sendMu.Unlock()
		mu.Lock()
		at = append(at, n)
		mu.Unlock()
		prev(d, st)
	}
	t.Cleanup(func() { callTeardown = prev })
	return func() []int {
		mu.Lock()
		defer mu.Unlock()
		return append([]int(nil), at...)
	}
}

func wantTeardownAfterSends(t *testing.T, r *callRig, order func() []int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(order()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	r.stub.sendMu.Lock()
	total := len(r.stub.SentPeerIDs)
	r.stub.sendMu.Unlock()
	if got := order(); len(got) != 1 || got[0] != total {
		t.Fatalf("teardown ran with %v sends out, want once with all %d: the end must leave before the teardown", got, total)
	}
}

const (
	callPhone  = "00000000000000000000000000003001"
	callLaptop = "00000000000000000000000000003002"
	callTablet = "00000000000000000000000000003003"
	callEve    = "00000000000000000000000000003004"
)

func TestCallOffer_RingsEveryDeviceOfTheContact(t *testing.T) {
	r := newCallRig(t)
	dc := r.contactWith(callPhone, callLaptop, callTablet)
	phone := r.connect(callPhone, 0)
	laptop := r.connect(callLaptop, 1)

	st := r.placeCall(dc)

	for _, dev := range []*callDevice{phone, laptop} {
		ws := r.callMsgs(dev)
		if len(ws) != 1 || ws[0].Kind != msg.KindCallOffer {
			t.Fatalf("device %s got %v, want one call_offer", dev.id, kindsOf(ws))
		}
		body, err := ws[0].CallOffer()
		if err != nil {
			t.Fatal(err)
		}
		if body.CallID != st.CallID {
			t.Fatalf("device %s was offered call %q, want %q (one call id for every device)", dev.id, body.CallID, st.CallID)
		}
		if len(body.OutboundKey) != msg.CallOutboundKeyBytes || body.Tokens[msg.ModalityAudio] == "" {
			t.Fatalf("device %s got an offer without media material", dev.id)
		}
	}
	if r.sentTo(callTablet) {
		t.Fatal("a send went to the device with no session")
	}

	got := r.state(st.CallID)
	if want := []string{callPhone, callLaptop, callTablet}; !reflect.DeepEqual(got.Targets, want) {
		t.Fatalf("targets = %v, want %v", got.Targets, want)
	}
	if want := []string{callTablet}; !reflect.DeepEqual(got.Stopped, want) {
		t.Fatalf("stopped = %v, want %v (the offer did not reach it)", got.Stopped, want)
	}
	if got.Status != calls.StatusOffered || got.PeerID != dc.PeerID {
		t.Fatalf("row = %s peer %s, want offered, peer %s", got.Status, got.PeerID, dc.PeerID)
	}
}

func TestCallAccept_FirstAnswerWins(t *testing.T) {
	spawns := stubCallReceiver(t)
	r := newCallRig(t)
	dc := r.contactWith(callPhone, callLaptop, callTablet)
	phone := r.connect(callPhone, 0)
	laptop := r.connect(callLaptop, 1)
	tablet := r.connect(callTablet, 2)
	st := r.placeCall(dc)
	for _, dev := range []*callDevice{phone, laptop, tablet} {
		r.callMsgs(dev)
	}

	r.accept(laptop, st.CallID)

	got := r.state(st.CallID)
	if got.Status != calls.StatusAccepted || got.AnsweredBy != callLaptop {
		t.Fatalf("row = %s answered by %q, want accepted by the laptop", got.Status, got.AnsweredBy)
	}
	if spawned := spawns.get(); !reflect.DeepEqual(spawned, []string{callLaptop}) {
		t.Fatalf("media set up with %v, want only the laptop", spawned)
	}
	wantEnd(t, "phone", r.callMsgs(phone), st.CallID, msg.CallEndAnsweredElsewhere)
	wantEnd(t, "tablet", r.callMsgs(tablet), st.CallID, msg.CallEndAnsweredElsewhere)
	wantNone(t, "laptop", r.callMsgs(laptop))

	if got.PeerID != dc.PeerID || callStateToEntry(got).PeerID != dc.PeerID {
		t.Fatalf("the call's peer changed to %q; want %q whichever device answered", got.PeerID, dc.PeerID)
	}
}

func TestCallAccept_LateSecondAcceptGetsEnd(t *testing.T) {
	spawns := stubCallReceiver(t)
	r := newCallRig(t)
	dc := r.contactWith(callPhone, callLaptop)
	phone := r.connect(callPhone, 0)
	laptop := r.connect(callLaptop, 1)
	st := r.placeCall(dc)
	r.callMsgs(phone)
	r.callMsgs(laptop)

	r.accept(phone, st.CallID)
	wantEnd(t, "laptop at the first answer", r.callMsgs(laptop), st.CallID, msg.CallEndAnsweredElsewhere)

	r.accept(laptop, st.CallID)
	wantEnd(t, "laptop after its late accept", r.callMsgs(laptop), st.CallID, msg.CallEndAnsweredElsewhere)

	r.accept(phone, st.CallID)
	wantNone(t, "phone", r.callMsgs(phone))

	got := r.state(st.CallID)
	if got.Status != calls.StatusAccepted || got.AnsweredBy != callPhone {
		t.Fatalf("row = %s answered by %q, want accepted by the phone", got.Status, got.AnsweredBy)
	}
	if spawned := spawns.get(); !reflect.DeepEqual(spawned, []string{callPhone}) {
		t.Fatalf("media set up with %v, want the phone once", spawned)
	}
}

func TestCallReject_OnOneDeviceEndsTheCallOnAll(t *testing.T) {
	r := newCallRig(t)
	dc := r.contactWith(callPhone, callLaptop, callTablet)
	phone := r.connect(callPhone, 0)
	laptop := r.connect(callLaptop, 1)
	tablet := r.connect(callTablet, 2)
	st := r.placeCall(dc)
	for _, dev := range []*callDevice{phone, laptop, tablet} {
		r.callMsgs(dev)
	}

	order := teardownOrder(t, r)
	r.reject(laptop, st.CallID)
	wantTeardownAfterSends(t, r, order)

	got := r.state(st.CallID)
	if got.Status != calls.StatusRejected || got.FailReason != msg.CallRejectUserDeclined {
		t.Fatalf("row = %s (%q), want rejected (user_declined)", got.Status, got.FailReason)
	}
	wantEnd(t, "phone", r.callMsgs(phone), st.CallID, "")
	wantEnd(t, "tablet", r.callMsgs(tablet), st.CallID, "")
	wantNone(t, "laptop", r.callMsgs(laptop))
}

func TestCallOps_FromANonTargetDeviceAreDropped(t *testing.T) {
	spawns := stubCallReceiver(t)
	r := newCallRig(t)
	dc := r.contactWith(callPhone, callLaptop)
	phone := r.connect(callPhone, 0)
	laptop := r.connect(callLaptop, 1)
	eve := r.connect(callEve, 2)
	st := r.placeCall(dc)
	r.callMsgs(phone)
	r.callMsgs(laptop)
	r.callMsgs(eve)

	r.accept(eve, st.CallID)
	r.reject(eve, st.CallID)
	r.end(eve, st.CallID, "")

	got := r.state(st.CallID)
	if got.Status != calls.StatusOffered || got.AnsweredBy != "" || len(got.Stopped) != 0 {
		t.Fatalf("row = %s answered by %q stopped %v, want untouched", got.Status, got.AnsweredBy, got.Stopped)
	}
	if spawned := spawns.get(); len(spawned) != 0 {
		t.Fatalf("media set up with %v, want none", spawned)
	}
	wantNone(t, "eve", r.callMsgs(eve))
	wantNone(t, "phone", r.callMsgs(phone))
	wantNone(t, "laptop", r.callMsgs(laptop))
}

func TestCallOps_CalleeTakesOnlyTheOfferingDevice(t *testing.T) {
	r := newCallRig(t)
	caller := r.connect(callPhone, 0)
	eve := r.connect(callEve, 1)
	callID, _ := calls.NewCallID()
	r.offer(caller, callID, time.Now().Unix())
	if s := r.state(callID); s.Status != calls.StatusRinging {
		t.Fatalf("status = %s, want ringing", s.Status)
	}

	r.end(eve, callID, "")
	r.reject(eve, callID)
	if s := r.state(callID); s.Status != calls.StatusRinging {
		t.Fatalf("an op from another device moved the call to %s", s.Status)
	}

	r.end(caller, callID, msg.CallEndAnsweredElsewhere)
	s := r.state(callID)
	if s.Status != calls.StatusEnded || s.FailReason != msg.CallEndAnsweredElsewhere {
		t.Fatalf("row = %s (%q), want ended (answered_elsewhere)", s.Status, s.FailReason)
	}
}

func TestCallee_StopsRingingLocallyAndSendsNothing(t *testing.T) {
	prev := calleeRingTimeout
	calleeRingTimeout = 150 * time.Millisecond
	t.Cleanup(func() { calleeRingTimeout = prev })

	r := newCallRig(t)
	caller := r.connect(callPhone, 0)
	r.callMsgs(caller)
	callID, _ := calls.NewCallID()
	r.offer(caller, callID, time.Now().Unix())
	if s := r.state(callID); s.Status != calls.StatusRinging {
		t.Fatalf("status = %s, want ringing", s.Status)
	}

	deadline := time.Now().Add(5 * time.Second)
	for r.state(callID).Status == calls.StatusRinging && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	s := r.state(callID)
	if s.Status != calls.StatusEnded || s.FailReason != "" {
		t.Fatalf("row = %s (%q), want ended with no reason", s.Status, s.FailReason)
	}
	if got := deriveCallOutcome(s); got != events.CallOutcomeMissed {
		t.Fatalf("outcome = %s, want missed", got)
	}
	time.Sleep(100 * time.Millisecond)
	wantNone(t, "the caller device", r.callMsgs(caller))
}

func TestCallee_RingTimerCountsFromReceipt(t *testing.T) {
	r := newCallRig(t)
	caller := r.connect(callPhone, 0)
	callID, _ := calls.NewCallID()
	r.offer(caller, callID, time.Now().Add(-90*time.Second).Unix())
	time.Sleep(150 * time.Millisecond)
	if s := r.state(callID); s.Status != calls.StatusRinging {
		t.Fatalf("status = %s right after an offer 90 s old arrived, want ringing", s.Status)
	}
}

func TestCallee_AnsweredElsewhereIsNotMissed(t *testing.T) {
	r := newCallRig(t)
	caller := r.connect(callPhone, 0)
	callID, _ := calls.NewCallID()
	r.offer(caller, callID, time.Now().Unix())
	r.end(caller, callID, msg.CallEndAnsweredElsewhere)

	s := r.state(callID)
	if got := deriveCallOutcome(s); got != events.CallOutcomeAnsweredElsewhere {
		t.Fatalf("outcome = %s, want %s", got, events.CallOutcomeAnsweredElsewhere)
	}
	rows, err := r.d.events.List(s.ChatID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, ev := range rows {
		if ev.Kind != events.KindCallSummary {
			continue
		}
		var body events.CallSummaryBody
		if err := json.Unmarshal(ev.Body, &body); err != nil {
			t.Fatal(err)
		}
		if body.CallID != callID {
			continue
		}
		found = true
		if body.Outcome != events.CallOutcomeAnsweredElsewhere || body.DurationSeconds != 0 {
			t.Fatalf("summary = %s, %d s; want answered_elsewhere, no duration", body.Outcome, body.DurationSeconds)
		}
	}
	if !found {
		t.Fatal("no call summary row written")
	}

	late := s
	late.AcceptedAt, late.EndedAt = 100, 130
	if got := deriveCallOutcome(late); got != events.CallOutcomeAnsweredElsewhere {
		t.Fatalf("late pick-up outcome = %s, want %s", got, events.CallOutcomeAnsweredElsewhere)
	}
}

func TestCallee_DropsAnOfferOlderThanTheWindow(t *testing.T) {
	r := newCallRig(t)
	caller := r.connect(callPhone, 0)
	r.callMsgs(caller)

	late, _ := calls.NewCallID()
	tooOld := time.Now().Add(-(callOfferMaxAge + 5*time.Second)).Unix()
	r.offer(caller, late, tooOld)
	if _, err := r.d.calls.GetState(late); err != calls.ErrCallNotFound {
		t.Fatalf("a late offer left a call row (err=%v), want none", err)
	}

	recent, _ := calls.NewCallID()
	r.offer(caller, recent, time.Now().Add(-(callOfferMaxAge - 10*time.Second)).Unix())
	if s := r.state(recent); s.Status != calls.StatusRinging {
		t.Fatalf("an offer 10 s inside the limit: status %s, want ringing", s.Status)
	}
	wantNone(t, "the caller device", r.callMsgs(caller))
}

func TestCaller_TimeoutEndsOnEveryRingingDevice(t *testing.T) {
	prev := callOfferTimeout
	callOfferTimeout = 200 * time.Millisecond
	t.Cleanup(func() { callOfferTimeout = prev })

	r := newCallRig(t)
	dc := r.contactWith(callPhone, callLaptop)
	phone := r.connect(callPhone, 0)
	laptop := r.connect(callLaptop, 1)
	order := teardownOrder(t, r)
	st := r.placeCall(dc)
	r.callMsgs(phone)
	r.callMsgs(laptop)

	deadline := time.Now().Add(5 * time.Second)
	for r.state(st.CallID).Status == calls.StatusOffered && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}

	var phoneMsgs, laptopMsgs []*msg.Wrapper
	for time.Now().Before(deadline) {
		phoneMsgs = append(phoneMsgs, r.callMsgs(phone)...)
		laptopMsgs = append(laptopMsgs, r.callMsgs(laptop)...)
		if len(phoneMsgs) > 0 && len(laptopMsgs) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := r.state(st.CallID)
	if got.Status != calls.StatusFailed || got.FailReason != calls.FailReasonNoAnswer {
		t.Fatalf("row = %s (%q), want failed (no_answer)", got.Status, got.FailReason)
	}
	wantEnd(t, "phone", phoneMsgs, st.CallID, "")
	wantEnd(t, "laptop", laptopMsgs, st.CallID, "")
	wantTeardownAfterSends(t, r, order)
}

func TestCaller_HangUpWhileRingingEndsOnAllTargets(t *testing.T) {
	r := newCallRig(t)
	dc := r.contactWith(callPhone, callLaptop, callTablet)
	phone := r.connect(callPhone, 0)
	laptop := r.connect(callLaptop, 1)
	tablet := r.connect(callTablet, 2)
	st := r.placeCall(dc)
	for _, dev := range []*callDevice{phone, laptop, tablet} {
		r.callMsgs(dev)
	}

	r.end(tablet, st.CallID, "")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, r.addr, r.cert, r.tok)
	order := teardownOrder(t, r)
	writeFrame(t, ctx, conn, mustFrame(ipc.FrameRespondCall, "h1", ipc.RespondCallRequest{
		CallID: st.CallID, Action: ipc.CallActionEnd,
	}))
	readUntil(t, ctx, conn, ipc.FrameCallResponded)
	wantTeardownAfterSends(t, r, order)

	if s := r.state(st.CallID); s.Status != calls.StatusEnded {
		t.Fatalf("status = %s, want ended", s.Status)
	}
	wantEnd(t, "phone", r.callMsgs(phone), st.CallID, "")
	wantEnd(t, "laptop", r.callMsgs(laptop), st.CallID, "")
	wantNone(t, "tablet", r.callMsgs(tablet))
}

func TestCaller_EndFromARingingDeviceStopsOnlyThatDevice(t *testing.T) {
	r := newCallRig(t)
	dc := r.contactWith(callPhone, callLaptop)
	phone := r.connect(callPhone, 0)
	laptop := r.connect(callLaptop, 1)
	st := r.placeCall(dc)
	r.callMsgs(phone)
	r.callMsgs(laptop)

	r.end(phone, st.CallID, "")
	got := r.state(st.CallID)
	if got.Status != calls.StatusOffered || !reflect.DeepEqual(got.Stopped, []string{callPhone}) {
		t.Fatalf("row = %s stopped %v, want offered with the phone stopped", got.Status, got.Stopped)
	}
	wantNone(t, "laptop", r.callMsgs(laptop))

	r.end(laptop, st.CallID, "")
	if s := r.state(st.CallID); s.Status != calls.StatusEnded {
		t.Fatalf("status = %s after every device stopped, want ended", s.Status)
	}
	wantNone(t, "phone", r.callMsgs(phone))
	wantNone(t, "laptop", r.callMsgs(laptop))
}

func TestCallAccept_EndFromALosingDeviceKeepsTheCall(t *testing.T) {
	stubCallReceiver(t)
	r := newCallRig(t)
	dc := r.contactWith(callPhone, callLaptop)
	phone := r.connect(callPhone, 0)
	laptop := r.connect(callLaptop, 1)
	st := r.placeCall(dc)
	r.callMsgs(phone)
	r.callMsgs(laptop)

	r.accept(phone, st.CallID)
	r.end(laptop, st.CallID, "")
	r.reject(laptop, st.CallID)

	if s := r.state(st.CallID); s.Status != calls.StatusAccepted || s.AnsweredBy != callPhone {
		t.Fatalf("row = %s answered by %q, want still accepted by the phone", s.Status, s.AnsweredBy)
	}

	r.end(phone, st.CallID, "")
	if s := r.state(st.CallID); s.Status != calls.StatusEnded {
		t.Fatalf("status = %s after the answering device hung up, want ended", s.Status)
	}
}

func failTransitionTo(t *testing.T, status calls.Status, times int) func() int {
	t.Helper()
	var mu sync.Mutex
	var n int32
	failNow := func() bool {
		mu.Lock()
		defer mu.Unlock()
		fail := times < 0 || int(n) < times
		if fail {
			n++
		}
		return fail
	}
	prev := transitionCall
	transitionCall = func(m *calls.Manager, callID string, next calls.Status, reason string, now int64) (calls.State, error) {
		if next == status && failNow() {
			return calls.State{}, errors.New("store write failed")
		}
		return prev(m, callID, next, reason, now)
	}

	prevFail := failCallUnanswered
	if status == calls.StatusFailed {
		failCallUnanswered = func(m *calls.Manager, callID, reason string, now int64) (calls.State, error) {
			if failNow() {
				return calls.State{}, errors.New("store write failed")
			}
			return prevFail(m, callID, reason, now)
		}
	}
	t.Cleanup(func() { transitionCall, failCallUnanswered = prev, prevFail })
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return int(n)
	}
}

func TestCallAccept_AcceptedWriteFailsFailsTheCall(t *testing.T) {
	stubCallReceiver(t)
	failTransitionTo(t, calls.StatusAccepted, -1)
	r := newCallRig(t)
	dc := r.contactWith(callPhone, callLaptop)
	phone := r.connect(callPhone, 0)
	laptop := r.connect(callLaptop, 1)
	st := r.placeCall(dc)
	r.callMsgs(phone)
	r.callMsgs(laptop)

	order := teardownOrder(t, r)
	r.accept(laptop, st.CallID)
	wantTeardownAfterSends(t, r, order)

	got := r.state(st.CallID)
	if got.Status != calls.StatusFailed || got.FailReason != calls.FailReasonPersist {
		t.Fatalf("row = %s (%q), want failed (%s)", got.Status, got.FailReason, calls.FailReasonPersist)
	}
	wantEnd(t, "laptop", r.callMsgs(laptop), st.CallID, "")
	wantEnd(t, "phone", r.callMsgs(phone), st.CallID, msg.CallEndAnsweredElsewhere)
}

func TestCallAccept_ClaimWriteFailsEndsThatDevice(t *testing.T) {
	stubCallReceiver(t)
	prev := claimCallAnswer
	claimCallAnswer = func(*calls.Manager, string, string) (calls.State, bool, error) {
		return calls.State{}, false, errors.New("store write failed")
	}
	t.Cleanup(func() { claimCallAnswer = prev })
	r := newCallRig(t)
	dc := r.contactWith(callPhone, callLaptop)
	phone := r.connect(callPhone, 0)
	laptop := r.connect(callLaptop, 1)
	st := r.placeCall(dc)
	r.callMsgs(phone)
	r.callMsgs(laptop)

	r.accept(laptop, st.CallID)

	wantEnd(t, "laptop", r.callMsgs(laptop), st.CallID, "")
	wantNone(t, "phone", r.callMsgs(phone))
	if s := r.state(st.CallID); s.Status != calls.StatusOffered || s.AnsweredBy != "" {
		t.Fatalf("row = %s answered by %q, want still offered", s.Status, s.AnsweredBy)
	}
}

func TestCaller_TimerRetriesAFailedWrite(t *testing.T) {
	prevT, prevD := callOfferTimeout, callTimerRearmDelay
	callOfferTimeout, callTimerRearmDelay = 100*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { callOfferTimeout, callTimerRearmDelay = prevT, prevD })
	failures := failTransitionTo(t, calls.StatusFailed, 2)

	r := newCallRig(t)
	dc := r.contactWith(callPhone, callLaptop)
	phone := r.connect(callPhone, 0)
	laptop := r.connect(callLaptop, 1)
	st := r.placeCall(dc)
	r.callMsgs(phone)
	r.callMsgs(laptop)

	deadline := time.Now().Add(5 * time.Second)
	var phoneMsgs, laptopMsgs []*msg.Wrapper
	for time.Now().Before(deadline) {
		phoneMsgs = append(phoneMsgs, r.callMsgs(phone)...)
		laptopMsgs = append(laptopMsgs, r.callMsgs(laptop)...)
		if len(phoneMsgs) > 0 && len(laptopMsgs) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := r.state(st.CallID); got.Status != calls.StatusFailed || got.FailReason != calls.FailReasonNoAnswer {
		t.Fatalf("row = %s (%q), want failed (no_answer) after the retries", got.Status, got.FailReason)
	}
	if n := failures(); n != 2 {
		t.Fatalf("the write failed %d times, want 2 before it landed", n)
	}
	wantEnd(t, "phone", phoneMsgs, st.CallID, "")
	wantEnd(t, "laptop", laptopMsgs, st.CallID, "")
}

func TestCaller_TimerRetriesAreBounded(t *testing.T) {
	prevT, prevD, prevN := callOfferTimeout, callTimerRearmDelay, callTimerRearms
	callOfferTimeout, callTimerRearmDelay, callTimerRearms = 50*time.Millisecond, 20*time.Millisecond, 3
	t.Cleanup(func() { callOfferTimeout, callTimerRearmDelay, callTimerRearms = prevT, prevD, prevN })
	failures := failTransitionTo(t, calls.StatusFailed, -1)

	r := newCallRig(t)
	dc := r.contactWith(callPhone)
	r.connect(callPhone, 0)
	st := r.placeCall(dc)

	time.Sleep(600 * time.Millisecond)
	if n := failures(); n != 4 {
		t.Fatalf("the write was tried %d times, want 4 (the first and 3 more)", n)
	}
	if s := r.state(st.CallID); s.Status != calls.StatusOffered {
		t.Fatalf("status = %s, want offered (every write failed)", s.Status)
	}
}

func TestStartCall_OverIPC(t *testing.T) {
	r := newCallRig(t)
	mgr, err := streamers.New(streamers.Config{
		Logger:  slog.New(slog.DiscardHandler),
		MicPath: "/nonexistent/haoma-mic",
		SpkPath: "/nonexistent/haoma-spk",
	})
	if err != nil {
		t.Fatal(err)
	}
	r.d.streamers = mgr
	var lockStore atomic.Bool
	prev := spawnCallSender
	spawnCallSender = func(context.Context, *daemon, string, string, string, []byte) (int, error) {
		if lockStore.Load() {

			_ = r.d.store.Lock()
		}
		return 1, nil
	}
	t.Cleanup(func() { spawnCallSender = prev })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, r.addr, r.cert, r.tok)

	good := r.contactWith(callPhone)
	phone := r.connect(callPhone, 0)
	r.callMsgs(phone)
	writeFrame(t, ctx, conn, mustFrame(ipc.FrameStartCall, "c1", ipc.StartCallRequest{ChatID: string(good.ID)}))
	started := readUntil(t, ctx, conn, ipc.FrameCallStarted)
	var resp ipc.CallStartedResponse
	if err := json.Unmarshal(started.Payload, &resp); err != nil {
		t.Fatal(err)
	}
	if s := r.state(resp.CallID); s.Status != calls.StatusOffered || !reflect.DeepEqual(s.Targets, []string{callPhone}) {
		t.Fatalf("row = %s targets %v, want offered to the phone", s.Status, s.Targets)
	}
	if ws := r.callMsgs(phone); len(ws) != 1 || ws[0].Kind != msg.KindCallOffer {
		t.Fatalf("phone got %v, want one offer", kindsOf(ws))
	}

	unreachable := r.contactWith(callTablet)
	writeFrame(t, ctx, conn, mustFrame(ipc.FrameStartCall, "c2", ipc.StartCallRequest{ChatID: string(unreachable.ID)}))
	if code := readErrorCode(t, ctx, conn); code != "backend_send" {
		t.Fatalf("error code = %q, want backend_send", code)
	}
	rows, err := r.d.calls.ListByChat(unreachable.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != calls.StatusFailed {
		t.Fatalf("rows = %+v, want one failed row", rows)
	}

	lockStore.Store(true)
	writeFrame(t, ctx, conn, mustFrame(ipc.FrameStartCall, "c3", ipc.StartCallRequest{ChatID: string(good.ID)}))
	if code := readErrorCode(t, ctx, conn); code != "persist_failed" {
		t.Fatalf("error code = %q, want persist_failed", code)
	}
}

func readErrorCode(t *testing.T, ctx context.Context, conn *websocket.Conn) string {
	t.Helper()
	f := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	if err := json.Unmarshal(f.Payload, &ep); err != nil {
		t.Fatal(err)
	}
	return ep.Code
}

func TestCallAccept_FailWriteAlsoFailsStillEndsTheWinner(t *testing.T) {
	stubCallReceiver(t)
	failTransitionTo(t, calls.StatusAccepted, -1)
	failTransitionTo(t, calls.StatusFailed, -1)
	r := newCallRig(t)
	dc := r.contactWith(callPhone, callLaptop)
	phone := r.connect(callPhone, 0)
	laptop := r.connect(callLaptop, 1)
	st := r.placeCall(dc)
	r.callMsgs(phone)
	r.callMsgs(laptop)

	r.accept(laptop, st.CallID)

	wantEnd(t, "laptop", r.callMsgs(laptop), st.CallID, "")
	wantEnd(t, "phone", r.callMsgs(phone), st.CallID, msg.CallEndAnsweredElsewhere)
}

func TestCallee_AnswerStartingBeatsTheRingTimer(t *testing.T) {
	prevR, prevD := calleeRingTimeout, callTimerRearmDelay
	calleeRingTimeout, callTimerRearmDelay = 100*time.Millisecond, 30*time.Millisecond
	t.Cleanup(func() { calleeRingTimeout, callTimerRearmDelay = prevR, prevD })
	stubCallReceiver(t)
	prevS := spawnCallSender
	spawnCallSender = func(context.Context, *daemon, string, string, string, []byte) (int, error) {
		time.Sleep(400 * time.Millisecond)
		return 1, nil
	}
	t.Cleanup(func() { spawnCallSender = prevS })

	r := newCallRig(t)
	mgr, err := streamers.New(streamers.Config{
		Logger:  slog.New(slog.DiscardHandler),
		MicPath: "/nonexistent/haoma-mic",
		SpkPath: "/nonexistent/haoma-spk",
	})
	if err != nil {
		t.Fatal(err)
	}
	r.d.streamers = mgr
	caller := r.connect(callPhone, 0)
	r.callMsgs(caller)
	callID, _ := calls.NewCallID()
	r.offer(caller, callID, time.Now().Unix())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, r.addr, r.cert, r.tok)
	writeFrame(t, ctx, conn, mustFrame(ipc.FrameRespondCall, "a1", ipc.RespondCallRequest{
		CallID: callID, Action: ipc.CallActionAccept,
	}))
	resp := readUntil(t, ctx, conn, ipc.FrameCallResponded)
	var got ipc.CallRespondedResponse
	if err := json.Unmarshal(resp.Payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != string(calls.StatusAccepted) {
		t.Fatalf("reply status = %s, want accepted", got.Status)
	}
	time.Sleep(200 * time.Millisecond)
	if s := r.state(callID); s.Status != calls.StatusAccepted {
		t.Fatalf("status = %s, want accepted", s.Status)
	}
	if ws := r.callMsgs(caller); len(ws) != 1 || ws[0].Kind != msg.KindCallAccept {
		t.Fatalf("caller device got %v, want one accept", kindsOf(ws))
	}
}

func TestCaller_OfferTimerStopsOnAnswerAndEnd(t *testing.T) {
	stubCallReceiver(t)
	r := newCallRig(t)
	dc := r.contactWith(callPhone, callLaptop)
	phone := r.connect(callPhone, 0)
	laptop := r.connect(callLaptop, 1)

	armed := func(callID string) bool {
		_, ok := callOfferTimers.Load(callID)
		return ok
	}

	answered := r.placeCall(dc)
	if !armed(answered.CallID) {
		t.Fatal("no timer armed for a new call")
	}
	r.accept(laptop, answered.CallID)
	if armed(answered.CallID) {
		t.Fatal("the timer is still armed after the call was accepted")
	}

	declined := r.placeCall(dc)
	r.reject(phone, declined.CallID)
	if armed(declined.CallID) {
		t.Fatal("the timer is still armed after the call was declined")
	}
}
