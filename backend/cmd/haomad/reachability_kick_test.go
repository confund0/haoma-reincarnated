package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"haoma/internal/outbox"
	"haoma/internal/peers"
	"haoma/internal/store"
	"haoma/internal/xport"
)

func attachOutbox(t *testing.T, d *daemon) *outbox.Store {
	t.Helper()
	st := outbox.NewStore(d.store)
	d.worker = outbox.NewWorker(st, nil, nil, &outbox.Bus{})
	return st
}

func queueStalled(t *testing.T, st *outbox.Store, dest, envID string) {
	t.Helper()
	now := time.Now()
	row, err := st.Enqueue(dest, xport.Envelope{ID: envID, Timestamp: 1, Payload: []byte("p")}, now)
	if err != nil {
		t.Fatalf("enqueue %s: %v", envID, err)
	}
	if err := st.Advance(row, outbox.StateEnqueued, now.Add(time.Hour), 7, 0, 0, "offline", now); err != nil {
		t.Fatalf("advance %s: %v", envID, err)
	}
	assertNotDue(t, st, envID)
}

func loadRow(t *testing.T, st *outbox.Store, envID string) *outbox.OutboxRow {
	t.Helper()
	row, err := st.Load(envID)
	if err != nil {
		t.Fatalf("load %s: %v", envID, err)
	}
	return row
}

func assertDue(t *testing.T, st *outbox.Store, envID string) {
	t.Helper()
	row := loadRow(t, st, envID)
	if row.NextAttemptAt > time.Now().UnixNano() {
		t.Fatalf("%s is still %s away; the backoff was not reset",
			envID, time.Until(time.Unix(0, row.NextAttemptAt)).Round(time.Second))
	}
}

func assertNotDue(t *testing.T, st *outbox.Store, envID string) {
	t.Helper()
	row := loadRow(t, st, envID)
	if row.NextAttemptAt <= time.Now().UnixNano() {
		t.Fatalf("%s is due; it was expected to still be waiting on backoff", envID)
	}
}

func importPeerWithAddresses(t *testing.T, d *daemon, addrs []string, myOnion, myKey string) *peers.Peer {
	t.Helper()
	inv := newTestBackendInvite(t, addrs)
	inv.MyOnionAddr = myOnion
	inv.MyOnionPrivateKey = myKey
	if rec := importPeer(t, d, inv); rec.Code != 201 {
		t.Fatalf("import: status %d, body %s", rec.Code, rec.Body.String())
	}
	p, err := d.registry.Get(inv.PeerID)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func dest(addr string) string { return "http://" + addr + ".onion" }

func TestRecoveredConnectionMakesTheQueuedRowsDue(t *testing.T) {
	d, f := newTorDaemon(t)
	st := attachOutbox(t, d)

	myOnion, myKey := sid("republish"), "republish-key"
	f.seedKey(myKey, myOnion, false)
	peerAddr := sid("peerback")
	importPeerWithAddresses(t, d, []string{peerAddr}, myOnion, myKey)

	queueStalled(t, st, dest(peerAddr), "stalled")

	closeAndSettle(t, d, f, myOnion)
	if !watchdogReconnect(context.Background(), d, "ping failed", true) {
		t.Fatal("watchdogReconnect reported failure")
	}
	t.Cleanup(func() { _ = d.ctrlConn.Close() })

	assertDue(t, st, "stalled")
}

func TestFirstConnectionOfAProcessLeavesTheBackoff(t *testing.T) {
	d, f := newTorDaemon(t)
	st := attachOutbox(t, d)

	myOnion, myKey := sid("bringup"), "bringup-key"
	f.seedKey(myKey, myOnion, false)
	peerAddr := sid("peerfresh")
	importPeerWithAddresses(t, d, []string{peerAddr}, myOnion, myKey)

	queueStalled(t, st, dest(peerAddr), "survives")

	closeAndSettle(t, d, f, myOnion)
	if err := d.adoptCtrlConn(dialFakeTor(t, d.cfg.torControl)); err != nil {
		t.Fatalf("adoptCtrlConn: %v", err)
	}
	if !f.isServing(myOnion) {
		t.Fatal("the fixture did not publish, so the test proves nothing")
	}

	assertNotDue(t, st, "survives")
}

func TestPasswordRotationLeavesTheBackoff(t *testing.T) {
	d, f := newTorDaemon(t)
	st := attachOutbox(t, d)

	myOnion, myKey := sid("rotate"), "rotate-key"
	f.seedKey(myKey, myOnion, false)
	peerAddr := sid("peerrotate")
	importPeerWithAddresses(t, d, []string{peerAddr}, myOnion, myKey)

	queueStalled(t, st, dest(peerAddr), "rotated")

	closeAndSettle(t, d, f, myOnion)
	if !watchdogReconnect(context.Background(), d, "tor-password kick", false) {
		t.Fatal("watchdogReconnect reported failure")
	}
	t.Cleanup(func() { _ = d.ctrlConn.Close() })
	if !f.isServing(myOnion) {
		t.Fatal("the fixture did not publish, so the test proves nothing")
	}

	assertNotDue(t, st, "rotated")
}

func TestRepublishThatPublishedNothingLeavesTheBackoff(t *testing.T) {
	d, f := newTorDaemon(t)
	st := attachOutbox(t, d)

	myOnion, myKey := sid("nochange"), "nochange-key"
	f.seedKey(myKey, myOnion, false)
	peerAddr := sid("peerheld")
	importPeerWithAddresses(t, d, []string{peerAddr}, myOnion, myKey)

	queueStalled(t, st, dest(peerAddr), "held")

	d.ctrlMu.Lock()
	conn := d.ctrlConn
	d.ctrlMu.Unlock()
	if err := d.republishAllPeerOnions(conn, true); err != nil {
		t.Fatalf("republishAllPeerOnions: %v", err)
	}

	assertNotDue(t, st, "held")
}

func TestAnAcceptedSendMakesTheOtherAddressDue(t *testing.T) {
	d := newTestDaemon(t)
	st := attachOutbox(t, d)
	wireReachabilityKicks(d)

	live, stale := sid("liveaddr"), sid("staleaddr")
	importPeerWithAddresses(t, d, []string{live, stale}, sid("mine"), "mine-key")

	queueStalled(t, st, dest(stale), "otheraddr")

	if d.worker.OnSendSuccess == nil {
		t.Fatal("the worker was never handed the send-success trigger")
	}
	d.worker.OnSendSuccess(dest(live))

	assertDue(t, st, "otheraddr")
}

func TestAnInboundEnvelopeMakesTheQueuedRowsDue(t *testing.T) {
	d := newTestDaemon(t)
	st := attachOutbox(t, d)

	addr := sid("inbound")
	p := importPeerWithAddresses(t, d, []string{addr}, sid("mineinb"), "mineinb-key")

	queueStalled(t, st, dest(addr), "inboundrow")
	d.kickPeers([]string{p.ID}, kickSignalInbound)
	assertDue(t, st, "inboundrow")
}

func TestASecondSignalInsideTheCooldownDoesNothing(t *testing.T) {
	d := newTestDaemon(t)
	st := attachOutbox(t, d)

	addr := sid("storm")
	p := importPeerWithAddresses(t, d, []string{addr}, sid("minestorm"), "minestorm-key")

	queueStalled(t, st, dest(addr), "first")
	d.kickPeers([]string{p.ID}, kickSignalSendOK)
	assertDue(t, st, "first")

	queueStalled(t, st, dest(addr), "second")
	d.kickPeers([]string{p.ID}, kickSignalSendOK)
	assertNotDue(t, st, "second")

	d.kickMu.Lock()
	d.kickLastAt[p.ID] = time.Now().Add(-peerKickCooldown - time.Second)
	d.kickMu.Unlock()
	d.kickPeers([]string{p.ID}, kickSignalSendOK)
	assertDue(t, st, "second")
}

func TestTheCooldownIsNotShorterThanTheBackoffFloor(t *testing.T) {
	if peerKickCooldown < outbox.BackoffBase {
		t.Errorf("peerKickCooldown = %s, under the %s first retry step — "+
			"a repeating signal would dial faster than the backoff allows",
			peerKickCooldown, outbox.BackoffBase)
	}
}

type recordingSender struct {
	mu    sync.Mutex
	dests []string
}

func (s *recordingSender) Send(_ context.Context, dest string, _ xport.Envelope) ([]byte, error) {
	s.mu.Lock()
	s.dests = append(s.dests, dest)
	s.mu.Unlock()
	return nil, errors.New("test sender: never delivers")
}

func (s *recordingSender) sentTo(dest string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.dests {
		if d == dest {
			return true
		}
	}
	return false
}

func waitForSendTo(t *testing.T, s *recordingSender, dest string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s.sentTo(dest) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout: nothing was sent to the control destination")
}

func runWorker(t *testing.T, w *outbox.Worker) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = w.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("outbox worker did not exit")
		}
	})
}

func TestAKickDoesNotReachTheDeviceBeingRemoved(t *testing.T) {
	d := newTestDaemon(t)
	st := outbox.NewStore(d.store)
	sender := &recordingSender{}
	w := outbox.NewWorker(st, sender, nil, &outbox.Bus{})
	w.Tick = time.Hour

	w.SkipDest = d.unlinkRefusesDest
	d.worker = w

	removedAddr, otherAddr := sid("beingremoved"), sid("ordinarypeer")
	removed := importPeerWithAddresses(t, d, []string{removedAddr}, sid("minerem"), "minerem-key")
	other := importPeerWithAddresses(t, d, []string{otherAddr}, sid("mineoth"), "mineoth-key")

	queueStalled(t, st, dest(removedAddr), "towardsremoved")
	queueStalled(t, st, dest(otherAddr), "towardsother")

	if err := d.store.PutUnlinkPending(store.UnlinkPending{
		PeerID:    removed.ID,
		Deadline:  time.Now().Add(time.Hour).Unix(),
		StartedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("put unlink pending: %v", err)
	}

	runWorker(t, w)
	d.kickPeers([]string{removed.ID, other.ID}, kickSignalInbound)

	waitForSendTo(t, sender, dest(otherAddr), 2*time.Second)
	if sender.sentTo(dest(removedAddr)) {
		t.Error("an envelope left for the device being removed after a kick")
	}
}

func TestAPeerWithNoAddressSpendsNoWindow(t *testing.T) {
	d := newTestDaemon(t)

	attachOutbox(t, d)

	p := importPeerWithAddresses(t, d, []string{sid("hasaddr")}, sid("minenoaddr"), "minenoaddr-key")
	if err := d.registry.Retire(p.ID); err != nil {
		t.Fatalf("retire: %v", err)
	}
	retired, err := d.registry.Get(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(retired.KnownAddresses) != 0 {
		t.Fatalf("the fixture still holds %d addresses, so it proves nothing", len(retired.KnownAddresses))
	}

	d.kickPeers([]string{p.ID}, kickSignalInbound)

	d.kickMu.Lock()
	_, stamped := d.kickLastAt[p.ID]
	d.kickMu.Unlock()
	if stamped {
		t.Error("a peer with no address spent its cooldown window")
	}
}

func TestAFailedKickGivesTheWindowBack(t *testing.T) {
	d := newTestDaemon(t)

	broken, err := store.Unlock(t.TempDir(), "pw")
	if err != nil {
		t.Fatal(err)
	}
	if err := broken.Lock(); err != nil {
		t.Fatal(err)
	}
	d.worker = outbox.NewWorker(outbox.NewStore(broken), nil, nil, &outbox.Bus{})

	p := importPeerWithAddresses(t, d, []string{sid("failkick")}, sid("minefail"), "minefail-key")
	d.kickPeers([]string{p.ID}, kickSignalRepublish)

	d.kickMu.Lock()
	_, stamped := d.kickLastAt[p.ID]
	d.kickMu.Unlock()
	if stamped {
		t.Error("a kick that failed kept the cooldown window")
	}
}

func TestUnwindingLeavesALaterStampAlone(t *testing.T) {
	d := newTestDaemon(t)
	first := time.Now()
	if !d.allowPeerKick("peer-1", first) {
		t.Fatal("the first kick was refused")
	}
	later := first.Add(time.Millisecond)
	d.kickMu.Lock()
	d.kickLastAt["peer-1"] = later
	d.kickMu.Unlock()

	d.forgetPeerKick([]string{"peer-1"}, first)

	d.kickMu.Lock()
	got, ok := d.kickLastAt["peer-1"]
	d.kickMu.Unlock()
	if !ok || !got.Equal(later) {
		t.Error("unwinding one kick dropped a stamp another kick had taken")
	}
}

func TestAnUnknownDestinationKicksNothing(t *testing.T) {
	d := newTestDaemon(t)
	st := attachOutbox(t, d)

	addr := sid("known")
	importPeerWithAddresses(t, d, []string{addr}, sid("mineunk"), "mineunk-key")
	queueStalled(t, st, dest(addr), "untouched")

	d.kickPeerByDest(dest(sid("stranger")), kickSignalSendOK)
	assertNotDue(t, st, "untouched")
}
