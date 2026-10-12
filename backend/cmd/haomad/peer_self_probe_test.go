package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"testing"
	"time"

	"haoma/internal/eventbus"
	"haoma/internal/ids"
	"haoma/internal/peers"
	"haoma/internal/xport"
)

func selfReachDaemon(t *testing.T) *daemon {
	t.Helper()
	d := newTestDaemon(t)
	d.selfProbe = newPeerSelfProbe()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	d.cfg.torSocks = ln.Addr().String()
	_ = ln.Close()
	return d
}

func selfReachPeer(t *testing.T, d *daemon, name string) *peers.Peer {
	t.Helper()
	return importPeerWithAddresses(t, d, []string{sid(name)}, sid("my"+name), "key-"+name)
}

func subscribeSelfReach(t *testing.T, d *daemon) <-chan eventbus.Event {
	t.Helper()
	ch, cancel := d.bus.Subscribe(eventbus.TopicPeerSelfReachChanged, 16)
	t.Cleanup(cancel)
	return ch
}

func expectSelfReach(t *testing.T, ch <-chan eventbus.Event, peerID string, want bool) peerSelfReachPayload {
	t.Helper()
	select {
	case ev := <-ch:
		p, ok := ev.Payload.(peerSelfReachPayload)
		if !ok {
			t.Fatalf("payload is %T, want peerSelfReachPayload", ev.Payload)
		}
		if p.PeerID != peerID || p.Ok != want {
			t.Fatalf("event = %+v, want peer %s ok=%v", p, peerID, want)
		}
		return p
	case <-time.After(time.Second):
		t.Fatalf("no peer.self-reach-changed event for peer %s (want ok=%v)", peerID, want)
	}
	return peerSelfReachPayload{}
}

func expectNoSelfReach(t *testing.T, ch <-chan eventbus.Event) {
	t.Helper()
	select {
	case ev := <-ch:
		t.Fatalf("unexpected peer.self-reach-changed event: %+v", ev.Payload)
	case <-time.After(50 * time.Millisecond):
	}
}

func postEnvelopeFrom(t *testing.T, d *daemon, p *peers.Peer, host, id string) {
	t.Helper()
	postEnvelope(t, d, host, signedFrom(p.KnownAddresses[0], p.InboundSecret, id), 200)
}

func signedFrom(from string, secret []byte, id string) xport.Envelope {
	return xport.Sign(xport.Envelope{
		ID:        id,
		From:      from,
		Timestamp: time.Now().Unix(),
		Payload:   []byte("p"),
	}, secret)
}

func postEnvelope(t *testing.T, d *daemon, host string, env xport.Envelope, wantCode int) {
	t.Helper()
	v := &peers.HMACVerifier{Registry: d.registry, OnVerifySuccess: d.onInboundVerified}
	r := xport.ReceiverFunc(func(context.Context, xport.Envelope) error { return nil })
	h := xport.NewServer(ids.SlotUnknown, r, v, nil)
	body, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "http://"+host+".onion/message", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != wantCode {
		t.Fatalf("POST /message: status %d, want %d, body %s", rec.Code, wantCode, rec.Body.String())
	}
}

func TestSelfReach_EnvelopeOnOwnOnionMarksOkOnce(t *testing.T) {
	d := selfReachDaemon(t)
	ch := subscribeSelfReach(t, d)
	p := selfReachPeer(t, d, "own")

	postEnvelopeFrom(t, d, p, p.MyOnionAddr, "own-1")
	expectSelfReach(t, ch, p.ID, true)
	if s, fresh := d.selfProbe.snapshot(p.ID); !s.Ok || !fresh {
		t.Fatalf("cached state = %+v fresh=%v, want a fresh ok", s, fresh)
	}

	postEnvelopeFrom(t, d, p, p.MyOnionAddr, "own-2")
	expectNoSelfReach(t, ch)
}

func TestSelfReach_EnvelopeAfterThePushIntervalPublishesAgain(t *testing.T) {
	d := selfReachDaemon(t)
	ch := subscribeSelfReach(t, d)
	p := selfReachPeer(t, d, "again")

	before := time.Now().Add(-peerSelfReachPushInterval - time.Second)
	if _, publish := d.selfProbe.markEnvelope(p.ID, p.MyOnionAddr, before, nil); !publish {
		t.Fatal("the first envelope was not to be published")
	}

	postEnvelopeFrom(t, d, p, p.MyOnionAddr, "again-1")
	got := expectSelfReach(t, ch, p.ID, true)
	if got.At <= before.Unix() {
		t.Fatalf("published at = %d, want later than the first envelope (%d)", got.At, before.Unix())
	}
}

func TestSelfReach_EnvelopeOnAnotherOnionDoesNotMark(t *testing.T) {
	d := selfReachDaemon(t)
	ch := subscribeSelfReach(t, d)
	x := selfReachPeer(t, d, "xpeer")
	y := selfReachPeer(t, d, "ypeer")

	postEnvelopeFrom(t, d, x, y.MyOnionAddr, "cross-1")
	postEnvelopeFrom(t, d, x, sid("nobody"), "cross-2")
	postEnvelopeFrom(t, d, x, x.KnownAddresses[0], "cross-3")

	expectNoSelfReach(t, ch)
	for _, id := range []string{x.ID, y.ID} {
		if s, _ := d.selfProbe.snapshot(id); s.Ok {
			t.Fatalf("peer %s marked ok by an envelope on another onion: %+v", id, s)
		}
	}
}

func TestSelfReach_FailedProbeAfterFreshEnvelopeKeepsOk(t *testing.T) {
	d := selfReachDaemon(t)
	ch := subscribeSelfReach(t, d)
	p := selfReachPeer(t, d, "kept")

	postEnvelopeFrom(t, d, p, p.MyOnionAddr, "kept-1")
	expectSelfReach(t, ch, p.ID, true)

	s := d.probePeerSelf(context.Background(), p.ID, p.MyOnionAddr, true)
	if !s.Ok {
		t.Fatalf("probe state = %+v, want ok kept from the envelope", s)
	}
	expectNoSelfReach(t, ch)
	if c, _ := d.selfProbe.snapshot(p.ID); !c.Ok {
		t.Fatalf("cached state = %+v after the failed probe, want ok", c)
	}
}

func TestSelfReach_FailedProbeRepublishesANewerEnvelope(t *testing.T) {
	d := selfReachDaemon(t)
	ch := subscribeSelfReach(t, d)
	p := selfReachPeer(t, d, "repub")
	now := time.Now()
	first := now.Add(-peerSelfProbeCacheTTL - 5*time.Second)
	second := first.Add(peerSelfReachPushInterval - 5*time.Second)

	if _, publish := d.selfProbe.markEnvelope(p.ID, p.MyOnionAddr, first, nil); !publish {
		t.Fatal("the first envelope was not to be published")
	}

	if _, publish := d.selfProbe.markEnvelope(p.ID, p.MyOnionAddr, second, nil); publish {
		t.Fatal("an envelope inside the push interval of the cached ok was to be published")
	}

	s := d.probePeerSelf(context.Background(), p.ID, p.MyOnionAddr, false)
	if !s.Ok {
		t.Fatalf("probe state = %+v, want ok from the newer envelope", s)
	}
	got := expectSelfReach(t, ch, p.ID, true)
	if got.At != second.Unix() {
		t.Fatalf("published at = %d, want %d (the newer envelope)", got.At, second.Unix())
	}
}

func TestSelfReach_FailedProbeAfterTheTTLIsPublishedFalse(t *testing.T) {
	d := selfReachDaemon(t)
	ch := subscribeSelfReach(t, d)
	p := selfReachPeer(t, d, "aged")

	d.selfProbe.markEnvelope(p.ID, p.MyOnionAddr, time.Now().Add(-peerSelfProbeCacheTTL-time.Second), nil)

	s := d.probePeerSelf(context.Background(), p.ID, p.MyOnionAddr, false)
	if s.Ok {
		t.Fatalf("probe state = %+v, want the failure", s)
	}
	expectSelfReach(t, ch, p.ID, false)
	if c, _ := d.selfProbe.snapshot(p.ID); c.Ok {
		t.Fatalf("cached state = %+v, want the failure", c)
	}
}

func TestSelfReach_PeerUnderUnlinkIsNotMarked(t *testing.T) {
	d := selfReachDaemon(t)
	ch := subscribeSelfReach(t, d)
	p := selfReachPeer(t, d, "removed")
	if _, err := d.beginUnlink(p.ID); err != nil {
		t.Fatalf("beginUnlink: %v", err)
	}

	postEnvelopeFrom(t, d, p, p.MyOnionAddr, "removed-1")

	expectNoSelfReach(t, ch)
	if s, _ := d.selfProbe.snapshot(p.ID); s.Ok {
		t.Fatalf("the device being removed was marked ok: %+v", s)
	}
}

func TestSelfReach_EnvelopeAfterAFalsePublishesAtOnce(t *testing.T) {
	d := selfReachDaemon(t)
	ch := subscribeSelfReach(t, d)
	p := selfReachPeer(t, d, "flip")
	d.selfProbe.store(peerSelfReachState{PeerID: p.ID, Onion: p.MyOnionAddr, Ok: false, At: time.Now().Add(-5 * time.Second)})

	postEnvelopeFrom(t, d, p, p.MyOnionAddr, "flip-1")
	expectSelfReach(t, ch, p.ID, true)
}

func TestSelfReach_RotationKeepsNothingFromTheOldOnion(t *testing.T) {
	d := selfReachDaemon(t)
	ch := subscribeSelfReach(t, d)
	p := selfReachPeer(t, d, "rot")
	oldOnion, newOnion := p.MyOnionAddr, sid("newrot")

	d.selfProbe.markEnvelope(p.ID, oldOnion, time.Now().Add(-5*time.Second), nil)
	s := d.probePeerSelf(context.Background(), p.ID, newOnion, true)
	if s.Ok {
		t.Fatalf("probe on the new onion = %+v, want the failure", s)
	}
	expectSelfReach(t, ch, p.ID, false)

	d.selfProbe.markEnvelope(p.ID, oldOnion, time.Now().Add(-5*time.Second), nil)
	if _, published := d.selfProbe.markEnvelope(p.ID, newOnion, time.Now(), nil); !published {
		t.Fatal("an envelope on the new onion was held back by an ok on the old one")
	}
}

func TestSelfReach_ProbeAfterRotationDoesNotUseTheOldOnionsOk(t *testing.T) {
	d := selfReachDaemon(t)
	ch := subscribeSelfReach(t, d)
	p := selfReachPeer(t, d, "snap")

	postEnvelopeFrom(t, d, p, p.MyOnionAddr, "snap-1")
	expectSelfReach(t, ch, p.ID, true)
	if _, err := d.registry.RotateOwnOnion(p.ID, sid("newsnap"), "newsnap-key"); err != nil {
		t.Fatalf("RotateOwnOnion: %v", err)
	}
	rotated, err := d.registry.Get(p.ID)
	if err != nil {
		t.Fatal(err)
	}

	s := d.probePeerSelf(context.Background(), p.ID, rotated.MyOnionAddr, false)
	if s.Ok || s.Onion != rotated.MyOnionAddr {
		t.Fatalf("probe for the new onion = %+v, want a failed dial on %s", s, rotated.MyOnionAddr)
	}
	expectSelfReach(t, ch, p.ID, false)
}

func TestSelfReach_UnverifiedEnvelopeOnOwnOnionDoesNotMark(t *testing.T) {
	d := selfReachDaemon(t)
	ch := subscribeSelfReach(t, d)
	p := selfReachPeer(t, d, "unver")

	postEnvelope(t, d, p.MyOnionAddr, signedFrom(p.KnownAddresses[0], []byte("not-the-secret-of-this-peer-xxxx"), "unver-1"), 401)
	postEnvelope(t, d, p.MyOnionAddr, signedFrom(sid("stranger"), p.InboundSecret, "unver-2"), 401)

	expectNoSelfReach(t, ch)
	if s, _ := d.selfProbe.snapshot(p.ID); s.Ok {
		t.Fatalf("an unverified envelope marked the peer ok: %+v", s)
	}
}

func TestSelfReach_EnvelopeOnThePreviousOnionDoesNotMark(t *testing.T) {
	d := selfReachDaemon(t)
	ch := subscribeSelfReach(t, d)
	p := selfReachPeer(t, d, "prev")
	oldOnion := p.MyOnionAddr
	if _, err := d.registry.RotateOwnOnion(p.ID, sid("newprev"), "newprev-key"); err != nil {
		t.Fatalf("RotateOwnOnion: %v", err)
	}
	rotated, err := d.registry.Get(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.PrevMyOnion == nil || rotated.PrevMyOnion.Address != oldOnion {
		t.Fatalf("PrevMyOnion = %+v, want the old onion %s", rotated.PrevMyOnion, oldOnion)
	}

	postEnvelopeFrom(t, d, rotated, oldOnion, "prev-1")

	expectNoSelfReach(t, ch)
	if s, _ := d.selfProbe.snapshot(p.ID); s.Ok {
		t.Fatalf("an envelope on the previous onion marked the peer ok: %+v", s)
	}
}

func TestSelfReach_PublishRunsUnderTheCacheLock(t *testing.T) {
	sp := newPeerSelfProbe()
	held := func(what string) func(peerSelfReachState) {
		return func(peerSelfReachState) {
			if sp.mu.TryLock() {
				sp.mu.Unlock()
				t.Errorf("%s published with the cache lock free", what)
			}
		}
	}
	now := time.Now()
	if _, published := sp.markEnvelope("peer", "onion", now.Add(-peerSelfProbeCacheTTL-time.Second), held("an envelope")); !published {
		t.Fatal("the envelope was not published")
	}
	if _, published := sp.settle(peerSelfReachState{PeerID: "peer", Onion: "onion", At: now, Err: "x"}, now, held("a probe")); !published {
		t.Fatal("the failed probe was not published")
	}
}

func TestSelfReach_OkProbeKeepsTheLaterAt(t *testing.T) {
	sp := newPeerSelfProbe()
	now := time.Now()
	sp.markEnvelope("peer", "onion", now, nil)

	s, published := sp.settle(peerSelfReachState{PeerID: "peer", Onion: "onion", Ok: true, At: now.Add(-10 * time.Second)}, now, nil)
	if !s.Ok || !s.At.Equal(now) {
		t.Fatalf("state = %+v, want ok at the envelope's time %v", s, now)
	}
	if published {
		t.Fatal("an ok probe older than the cached ok was published")
	}
	if c, _ := sp.snapshot("peer"); !c.At.Equal(now) {
		t.Fatalf("cached At = %v, want %v", c.At, now)
	}
}
