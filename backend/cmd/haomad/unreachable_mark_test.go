package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"haoma/internal/eventbus"
	"haoma/internal/outbox"
	"haoma/internal/peers"
	"haoma/internal/xport"
)

func markPeer(t *testing.T, d *daemon, name string) *peers.Peer {
	t.Helper()
	return importPeerWithAddresses(t, d, []string{sid(name)}, sid("m"+name), "mine-key")
}

func peerViewOf(t *testing.T, d *daemon, peerID string) peerView {
	t.Helper()
	rec := httptest.NewRecorder()
	d.apiHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/peers/"+peerID, nil))
	if rec.Code != 200 {
		t.Fatalf("GET /peers/%s: status %d, body %s", peerID, rec.Code, rec.Body.String())
	}
	var v peerView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode peer view: %v", err)
	}
	return v
}

func subscribeMarks(t *testing.T, d *daemon) <-chan eventbus.Event {
	t.Helper()
	ch, cancel := d.bus.Subscribe(eventbus.TopicPeerUnreachableChanged, 16)
	t.Cleanup(cancel)
	return ch
}

func expectMark(t *testing.T, ch <-chan eventbus.Event, peerID string, want bool) {
	t.Helper()
	select {
	case ev := <-ch:
		obs, ok := ev.Payload.(peerUnreachableObservation)
		if !ok {
			t.Fatalf("payload is %T, want peerUnreachableObservation", ev.Payload)
		}
		if obs.PeerID != peerID || obs.Unreachable != want {
			t.Fatalf("event = %+v, want peer %s unreachable=%v", obs, peerID, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("no peer.unreachable-changed event for peer %s (want unreachable=%v)", peerID, want)
	}
}

func expectNoMark(t *testing.T, ch <-chan eventbus.Event) {
	t.Helper()
	select {
	case ev := <-ch:
		t.Fatalf("unexpected peer.unreachable-changed event: %+v", ev.Payload)
	case <-time.After(50 * time.Millisecond):
	}
}

func receiveFrom(t *testing.T, d *daemon, p *peers.Peer, at time.Time) {
	t.Helper()
	v := &peers.HMACVerifier{
		Registry:        d.registry,
		OnVerifySuccess: d.onInboundVerified,
		Now:             func() time.Time { return at },
	}
	env := xport.Sign(xport.Envelope{
		ID:        "inbound-" + at.Format("150405"),
		From:      p.KnownAddresses[0],
		Timestamp: at.Unix(),
		Payload:   []byte("p"),
	}, p.InboundSecret)
	if err := v.Verify(context.Background(), env); err != nil {
		t.Fatalf("verify inbound envelope: %v", err)
	}
}

func TestUnreachable_ExpiredRowAndNoInboundMarksThePeer(t *testing.T) {
	d := newTestDaemon(t)
	ch := subscribeMarks(t, d)
	addr := sid("expired")
	p := markPeer(t, d, "expired")

	d.noteUndeliveredDest(dest(addr), time.Now().Add(-time.Hour), markMinAttempts)

	if !peerViewOf(t, d, p.ID).Unreachable {
		t.Fatal("GET /peers/{id} says reachable after an envelope expired with nothing received")
	}
	expectMark(t, ch, p.ID, true)
}

func TestUnreachable_InboundAfterTheQueuedRowClears(t *testing.T) {
	d := newTestDaemon(t)
	ch := subscribeMarks(t, d)
	addr := sid("inbafter")
	p := markPeer(t, d, "inbafter")
	now := time.Now()

	d.noteUndeliveredDest(dest(addr), now.Add(-time.Hour), markMinAttempts)
	expectMark(t, ch, p.ID, true)

	receiveFrom(t, d, p, now)

	if peerViewOf(t, d, p.ID).Unreachable {
		t.Fatal("GET /peers/{id} still says unreachable after an envelope came in")
	}
	expectMark(t, ch, p.ID, false)
}

func TestUnreachable_DeliveredSendClears(t *testing.T) {
	d := newTestDaemon(t)
	attachOutbox(t, d)
	wireReachabilityKicks(d)
	ch := subscribeMarks(t, d)
	addr := sid("delivered")
	p := markPeer(t, d, "delivered")

	d.noteUndeliveredDest(dest(addr), time.Now().Add(-time.Hour), markMinAttempts)
	expectMark(t, ch, p.ID, true)

	d.worker.OnSendSuccess(dest(addr))

	if peerViewOf(t, d, p.ID).Unreachable {
		t.Fatal("GET /peers/{id} still says unreachable after a delivered send")
	}
	expectMark(t, ch, p.ID, false)
}

func TestUnreachable_InboundBeforeTheQueuedRowLeavesTheMark(t *testing.T) {
	d := newTestDaemon(t)
	ch := subscribeMarks(t, d)
	addr := sid("inbbefore")
	p := markPeer(t, d, "inbbefore")

	seedUnreachableSent(d)
	now := time.Now()

	receiveFrom(t, d, p, now.Add(-2*time.Hour))
	expectNoMark(t, ch)

	d.noteUndeliveredDest(dest(addr), now.Add(-time.Hour), markMinAttempts)

	if !peerViewOf(t, d, p.ID).Unreachable {
		t.Fatal("GET /peers/{id} says reachable; the only inbound came before the envelope was queued")
	}
	expectMark(t, ch, p.ID, true)
}

func TestUnreachable_IsPerPairing(t *testing.T) {
	d := newTestDaemon(t)
	dead := sid("deadpair")
	pd := markPeer(t, d, "deadpair")
	pl := markPeer(t, d, "livepair")

	d.noteUndeliveredDest(dest(dead), time.Now().Add(-time.Hour), markMinAttempts)

	if !peerViewOf(t, d, pd.ID).Unreachable {
		t.Error("the pairing whose envelope expired is not marked")
	}
	if peerViewOf(t, d, pl.ID).Unreachable {
		t.Error("the other pairing is marked; nothing of it expired")
	}
}

func TestUnreachable_NoRepeatPublish(t *testing.T) {
	d := newTestDaemon(t)
	ch := subscribeMarks(t, d)
	addr := sid("norepeat")
	p := markPeer(t, d, "norepeat")
	now := time.Now()

	d.noteUndeliveredDest(dest(addr), now.Add(-time.Hour), markMinAttempts)
	expectMark(t, ch, p.ID, true)
	receiveFrom(t, d, p, now)
	expectMark(t, ch, p.ID, false)
	receiveFrom(t, d, p, now.Add(time.Second))
	expectNoMark(t, ch)
}

func TestUnreachable_WorkerIsWired(t *testing.T) {
	d := newTestDaemon(t)
	attachOutbox(t, d)
	wireUnreachableMarks(d)
	if d.worker.OnUndelivered == nil {
		t.Fatal("the worker was never handed the give-up hook")
	}
	addr := sid("wired")
	p := markPeer(t, d, "wired")
	d.worker.OnUndelivered(dest(addr), time.Now().Add(-time.Hour), markMinAttempts)
	if !peerViewOf(t, d, p.ID).Unreachable {
		t.Fatal("the worker's give-up hook did not mark the peer")
	}
}

func failRow(t *testing.T, st *outbox.Store, dst, envID string, queuedAt time.Time, attempts int, gaveUp bool) {
	t.Helper()
	row, err := st.Enqueue(dst, xport.Envelope{ID: envID, Timestamp: 1, Payload: []byte("p")}, queuedAt)
	if err != nil {
		t.Fatalf("enqueue %s: %v", envID, err)
	}
	row.GaveUp = gaveUp
	if err := st.Advance(row, outbox.StateFailed, time.Time{}, attempts, 0, 0, "offline", queuedAt.Add(48*time.Hour)); err != nil {
		t.Fatalf("fail %s: %v", envID, err)
	}
}

func sendRow(t *testing.T, st *outbox.Store, dst, envID string, at time.Time) {
	t.Helper()
	row, err := st.Enqueue(dst, xport.Envelope{ID: envID, Timestamp: 1, Payload: []byte("p")}, at)
	if err != nil {
		t.Fatalf("enqueue %s: %v", envID, err)
	}
	if err := st.Advance(row, outbox.StateSent, time.Time{}, 1, 0, 0, "", at); err != nil {
		t.Fatalf("send %s: %v", envID, err)
	}
}

func TestUnreachable_FoldPutsFailedRowsOnPeers(t *testing.T) {
	d := newTestDaemon(t)
	st := attachOutbox(t, d)
	gone, answered := sid("foldgone"), sid("foldanswered")
	pg := markPeer(t, d, "foldgone")
	pa := markPeer(t, d, "foldanswered")
	now := time.Now()

	failRow(t, st, dest(gone), "fold-gone", now.Add(-72*time.Hour), 72, true)
	failRow(t, st, dest(answered), "fold-answered", now.Add(-72*time.Hour), 72, true)
	sendRow(t, st, dest(answered), "fold-answered-sent", now.Add(-71*time.Hour))
	failRow(t, st, dest(sid("nopeer")), "fold-nopeer", now.Add(-72*time.Hour), 72, true)

	foldOutboxIntoMarks(d, st)

	if !peerViewOf(t, d, pg.ID).Unreachable {
		t.Error("the peer with a failed row and nothing since is not marked after the fold")
	}
	if peerViewOf(t, d, pa.ID).Unreachable {
		t.Error("the peer with a delivery after its failed row was queued is marked after the fold")
	}

	before, _ := d.registry.Get(pg.ID)
	foldOutboxIntoMarks(d, st)
	after, _ := d.registry.Get(pg.ID)
	if before.UndeliveredQueuedAt != after.UndeliveredQueuedAt || before.LastDeliveredAt != after.LastDeliveredAt {
		t.Errorf("a second fold changed the record: before %+v, after %+v", before, after)
	}
}

func TestUnreachable_FoldSkipsRowsThatDoNotMark(t *testing.T) {
	d := newTestDaemon(t)
	st := attachOutbox(t, d)
	now := time.Now()
	cases := []struct {
		name     string
		attempts int
		gaveUp   bool
		queued   time.Time
	}{
		{"foldanswered401", 1, false, now.Add(-72 * time.Hour)},
		{"foldfewattempts", markMinAttempts - 1, true, now.Add(-72 * time.Hour)},
		{"foldtooold", 72, true, now.Add(-outbox.SentRetention - 24*time.Hour)},
	}
	for _, c := range cases {
		p := markPeer(t, d, c.name)
		failRow(t, st, dest(sid(c.name)), "row-"+c.name, c.queued, c.attempts, c.gaveUp)
		foldOutboxIntoMarks(d, st)
		if peerViewOf(t, d, p.ID).Unreachable {
			t.Errorf("%s: the fold marked the peer", c.name)
		}
	}
}

func TestUnreachable_GiveUpWithFewAttemptsDoesNotMark(t *testing.T) {
	d := newTestDaemon(t)
	ch := subscribeMarks(t, d)
	p := markPeer(t, d, "fewattempts")

	d.noteUndeliveredDest(dest(sid("fewattempts")), time.Now().Add(-49*time.Hour), 1)

	if peerViewOf(t, d, p.ID).Unreachable {
		t.Fatal("a give-up after one attempt marked the peer")
	}
	expectNoMark(t, ch)
}

func TestUnreachable_AClearAfterARestartIsPublished(t *testing.T) {
	for _, seeded := range []bool{true, false} {
		d := newTestDaemon(t)
		name := "restartseed"
		if !seeded {
			name = "restartnoseed"
		}
		p := markPeer(t, d, name)
		now := time.Now()
		if err := d.registry.NoteUndelivered(p.ID, now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		if seeded {
			seedUnreachableSent(d)
		}
		ch := subscribeMarks(t, d)

		fresh, _ := d.registry.Get(p.ID)
		receiveFrom(t, d, fresh, now)

		expectMark(t, ch, p.ID, false)
	}
}

func TestUnreachable_APublishNobodyGotIsSentAgain(t *testing.T) {
	d := newTestDaemon(t)
	p := markPeer(t, d, "nobodygot")
	now := time.Now()

	d.noteUndeliveredDest(dest(sid("nobodygot")), now.Add(-2*time.Hour), markMinAttempts)

	ch := subscribeMarks(t, d)
	d.noteUndeliveredDest(dest(sid("nobodygot")), now.Add(-time.Hour), markMinAttempts)
	expectMark(t, ch, p.ID, true)
}

func TestUnreachable_AnUnknownPeerNeverMarkedPublishesNothing(t *testing.T) {
	d := newTestDaemon(t)
	seedUnreachableSent(d)
	ch := subscribeMarks(t, d)

	p := markPeer(t, d, "neverseen")
	now := time.Now()

	receiveFrom(t, d, p, now.Add(-2*time.Hour))
	expectNoMark(t, ch)

	d.noteUndeliveredDest(dest(sid("neverseen")), now.Add(-time.Hour), markMinAttempts)
	expectMark(t, ch, p.ID, true)
}

func TestUnreachable_AMarkedPeerThatClearsIsPublished(t *testing.T) {
	d := newTestDaemon(t)
	p := markPeer(t, d, "markedclears")
	now := time.Now()

	d.noteUndeliveredDest(dest(sid("markedclears")), now.Add(-time.Hour), markMinAttempts)

	ch := subscribeMarks(t, d)
	fresh, _ := d.registry.Get(p.ID)
	receiveFrom(t, d, fresh, now)
	expectMark(t, ch, p.ID, false)
}
