package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"

	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/linkeddevice"
	"haoma-frontend/internal/syncrail"
)

const railSiblingTwo = "5b2e94a07c1d43f8ae60b3d519f78c25"

func linkTwoSiblings(t *testing.T, d *daemon) {
	t.Helper()
	d.linkedDevices = linkeddevice.New(d.store)
	for _, id := range []string{railSiblingID, railSiblingTwo} {
		if _, err := d.linkedDevices.Link(id, time.Now().Unix()); err != nil {
			t.Fatalf("link sibling %q: %v", id, err)
		}
	}
	if len(d.siblings()) != 2 {
		t.Fatalf("fixture: %d siblings, want 2", len(d.siblings()))
	}
}

func deviceSync(t *testing.T, ctx context.Context, conn *websocket.Conn, id string) ipc.DeviceSyncSentResponse {
	t.Helper()
	req, _ := ipc.NewFrame(ipc.FrameDeviceSync, id, ipc.DeviceSyncRequest{Full: true})
	writeFrame(t, ctx, conn, req)
	f := readUntil(t, ctx, conn, ipc.FrameDeviceSyncSent)
	var out ipc.DeviceSyncSentResponse
	if err := json.Unmarshal(f.Payload, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDeviceSync_TheSweepOutlivesTheClient(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	gate := make(chan struct{})
	stub.SendGate = gate
	released := false
	release := func() {
		if !released {
			released = true
			close(gate)
		}
	}
	t.Cleanup(release)
	d, addr, certPath, token := newTestDaemon(t, stub)
	linkTwoSiblings(t, d)
	conn := dialTest(t, ctx, addr, certPath, token)

	if got := deviceSync(t, ctx, conn, "sync-1"); got.Siblings != 2 || got.AlreadyRunning {
		t.Fatalf("reply = %+v, want 2 siblings and a sweep started", got)
	}
	stub.WaitForSendEntered(t, 1, 10*time.Second)

	_ = conn.Close(websocket.StatusNormalClosure, "app in the background")
	time.Sleep(200 * time.Millisecond)
	release()

	stub.WaitForSendEntered(t, 1, 10*time.Second)
}

func TestDeviceSync_TheReplySaysWhenASweepIsAlreadyRunning(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	conn := dialTest(t, ctx, addr, certPath, token)

	d.manualSync.gate = 200 * time.Millisecond
	if !d.gapfill.enter() {
		t.Fatal("fixture: the sweep gate was already taken")
	}
	got := deviceSync(t, ctx, conn, "sync-1")
	if !got.AlreadyRunning || got.Siblings != 1 {
		t.Fatalf("reply = %+v, want already_running with the one sibling", got)
	}
	time.Sleep(200 * time.Millisecond)
	if n := len(stub.SendEntered); n != 0 {
		t.Fatalf("%d send(s) for a request that started no sweep", n)
	}

	d.gapfill.leave()
	got = deviceSync(t, ctx, conn, "sync-2")
	if got.AlreadyRunning {
		t.Fatalf("reply = %+v, want a sweep started once the gate was free", got)
	}
	stub.WaitForSendEntered(t, 1, 10*time.Second)
}

func deviceSyncWith(t *testing.T, ctx context.Context, conn *websocket.Conn, id string, full bool) ipc.DeviceSyncSentResponse {
	t.Helper()
	req, _ := ipc.NewFrame(ipc.FrameDeviceSync, id, ipc.DeviceSyncRequest{Full: full})
	writeFrame(t, ctx, conn, req)
	f := readUntil(t, ctx, conn, ipc.FrameDeviceSyncSent)
	var out ipc.DeviceSyncSentResponse
	if err := json.Unmarshal(f.Payload, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func readSyncFinished(t *testing.T, ctx context.Context, conn *websocket.Conn) ipc.DeviceSyncFinishedPush {
	t.Helper()
	f := readUntil(t, ctx, conn, ipc.FrameDeviceSyncFinished)
	var out ipc.DeviceSyncFinishedPush
	if err := json.Unmarshal(f.Payload, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func snapshotDone(d *daemon, sib string, full bool) {
	handleBackfillResponse(context.Background(), d, sib, syncrail.BackfillResponseBody{
		Section: syncrail.SectionSelf,
		Full:    full,
	})
}

func missedRow(t *testing.T, msgID string) syncrail.RowSnapshot {
	t.Helper()
	body, err := json.Marshal(events.TextBody{Text: "sent while this device was off"})
	if err != nil {
		t.Fatal(err)
	}
	return syncrail.RowSnapshot{
		Fingerprint: railFingerprint,
		MsgID:       msgID,
		Kind:        string(events.KindText),
		Inbound:     true,
		DisplayTs:   1742600000,
		SenderTs:    1742600000,
		Body:        body,
	}
}

func waitNoManualSweep(t *testing.T, d *daemon, within time.Duration) {
	t.Helper()
	until := time.Now().Add(within)
	for time.Now().Before(until) {
		d.manualSync.mu.Lock()
		cur := d.manualSync.cur
		d.manualSync.mu.Unlock()
		if cur == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the manual sweep is still waited for")
}

func TestDeviceSync_FinishedAfterEveryLinkedDeviceAnsweredItsLastBatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	linkTwoSiblings(t, d)
	seedKnownPeer(t, d, railPeerID, railFingerprint)
	conn := dialTest(t, ctx, addr, certPath, token)

	if got := deviceSyncWith(t, ctx, conn, "sync-1", true); got.AlreadyRunning || got.Siblings != 2 {
		t.Fatalf("reply = %+v, want a sweep started with 2 devices", got)
	}

	stub.WaitForSend(t, 2, 5*time.Second)

	handleGapResponse(context.Background(), d, railSiblingTwo, syncrail.GapResponseBody{RangeEnd: 1, Full: true})

	snapshotDone(d, railSiblingID, true)
	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{
		Rows: []syncrail.RowSnapshot{missedRow(t, "missed-1")}, RangeEnd: 5, More: true, Full: true,
	})
	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{
		Rows: []syncrail.RowSnapshot{missedRow(t, "missed-2")}, RangeEnd: 9, Full: true,
	})
	snapshotDone(d, railSiblingTwo, true)

	handleGapResponse(context.Background(), d, railSiblingTwo, syncrail.GapResponseBody{RangeEnd: 2})

	handleGapResponse(context.Background(), d, railSiblingTwo, syncrail.GapResponseBody{
		Rows:     []syncrail.RowSnapshot{missedRow(t, "missed-1"), missedRow(t, "missed-2"), missedRow(t, "missed-3")},
		RangeEnd: 4, Full: true,
	})

	got := readSyncFinished(t, ctx, conn)
	want := ipc.DeviceSyncFinishedPush{Full: true, Siblings: 2, Answered: 2, RowsApplied: 3}
	if got != want {
		t.Fatalf("push = %+v, want %+v", got, want)
	}
	waitNoManualSweep(t, d, time.Second)
}

func TestDeviceSync_TimedOutWhenALinkedDeviceNeverAnswers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	linkTwoSiblings(t, d)
	d.manualSync.idle = 300 * time.Millisecond
	conn := dialTest(t, ctx, addr, certPath, token)

	deviceSyncWith(t, ctx, conn, "sync-1", false)
	stub.WaitForSend(t, 2, 5*time.Second)
	snapshotDone(d, railSiblingID, false)
	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{RangeEnd: 3})

	got := readSyncFinished(t, ctx, conn)
	want := ipc.DeviceSyncFinishedPush{Siblings: 2, Answered: 1, TimedOut: true}
	if got != want {
		t.Fatalf("push = %+v, want %+v", got, want)
	}
	if again := deviceSyncWith(t, ctx, conn, "sync-2", false); again.AlreadyRunning {
		t.Fatalf("reply = %+v, want a sweep started after the last one timed out", again)
	}
}

func TestDeviceSync_ASweepWaitingForAnswersIsAlreadyRunning(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	conn := dialTest(t, ctx, addr, certPath, token)

	deviceSyncWith(t, ctx, conn, "sync-1", false)
	stub.WaitForSend(t, 1, 5*time.Second)
	if got := deviceSyncWith(t, ctx, conn, "sync-2", true); !got.AlreadyRunning {
		t.Fatalf("reply = %+v, want already_running while the first sweep waits", got)
	}
	snapshotDone(d, railSiblingID, false)
	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{RangeEnd: 1})
	if got := readSyncFinished(t, ctx, conn); got.Full || got.Answered != 1 || got.TimedOut {
		t.Fatalf("push = %+v, want the first sweep's end", got)
	}
}

func TestDeviceSync_AnAutomaticSweepSendsNoPush(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	conn := dialTest(t, ctx, addr, certPath, token)
	watcher := dialTest(t, ctx, addr, certPath, token)

	if out := sweepGapFill(context.Background(), d); out != sweepStarted {
		t.Fatalf("automatic sweep = %q, want started", out)
	}
	snapshotDone(d, railSiblingID, false)
	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{RangeEnd: 2})

	if got := deviceSyncWith(t, ctx, conn, "sync-1", true); got.AlreadyRunning {
		t.Fatalf("reply = %+v, want a manual sweep started", got)
	}
	stub.WaitForSend(t, 3, 5*time.Second)
	snapshotDone(d, railSiblingID, true)
	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{RangeEnd: 2, Full: true})

	if got := readSyncFinished(t, ctx, watcher); !got.Full {
		t.Fatalf("first push = %+v; the automatic sweep pushed", got)
	}
}

func TestDeviceSync_TheWaitOutlivesTheClient(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	asker := dialTest(t, ctx, addr, certPath, token)

	deviceSyncWith(t, ctx, asker, "sync-1", false)
	stub.WaitForSend(t, 1, 5*time.Second)
	_ = asker.Close(websocket.StatusNormalClosure, "app in the background")
	time.Sleep(200 * time.Millisecond)

	later := dialTest(t, ctx, addr, certPath, token)
	snapshotDone(d, railSiblingID, false)
	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{RangeEnd: 1})
	if got := readSyncFinished(t, ctx, later); got.Answered != 1 || got.TimedOut {
		t.Fatalf("push = %+v, want the sweep's end", got)
	}
}

func TestDeviceSync_TheWaitEndsWithTheRelayContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	relay, stopRelay := context.WithCancel(context.Background())
	defer stopRelay()
	d.relayCtx.Store(relay)
	conn := dialTest(t, ctx, addr, certPath, token)

	deviceSyncWith(t, ctx, conn, "sync-1", false)
	stub.WaitForSend(t, 1, 5*time.Second)
	stopRelay()
	waitNoManualSweep(t, d, 2*time.Second)
}

func TestDeviceSync_AFailedFirstAskEndsTheSweepAtOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	stub.sendStatus = http.StatusInternalServerError
	d, addr, certPath, token := newTestDaemon(t, stub)
	linkTwoSiblings(t, d)
	conn := dialTest(t, ctx, addr, certPath, token)
	watcher := dialTest(t, ctx, addr, certPath, token)

	deviceSyncWith(t, ctx, conn, "sync-1", false)
	got := readSyncFinished(t, ctx, watcher)
	want := ipc.DeviceSyncFinishedPush{Siblings: 2, Unreachable: 2}
	if got != want {
		t.Fatalf("push = %+v, want %+v", got, want)
	}
	waitNoManualSweep(t, d, time.Second)
	if again := deviceSyncWith(t, ctx, conn, "sync-2", false); again.AlreadyRunning {
		t.Fatalf("reply = %+v, want a sweep started after the failed one", again)
	}
}

func TestDeviceSync_AnAnsweredDeviceDoesNotKeepASilentOnesWaitOpen(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	linkTwoSiblings(t, d)
	d.manualSync.idle = 300 * time.Millisecond
	conn := dialTest(t, ctx, addr, certPath, token)

	deviceSyncWith(t, ctx, conn, "sync-1", false)
	stub.WaitForSend(t, 2, 5*time.Second)
	snapshotDone(d, railSiblingID, false)
	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{RangeEnd: 3})
	answeredAt := time.Now()

	stop := make(chan struct{})
	fed := make(chan struct{})
	go func() {
		defer close(fed)
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		until := time.After(3 * time.Second)
		for {
			select {
			case <-stop:
				return
			case <-until:
				return
			case <-tick.C:
				d.manualSync.snapshot(railSiblingID, false, 0)
				d.manualSync.batch(d, railSiblingID, false, true, 0, 0)
			}
		}
	}()
	defer func() { close(stop); <-fed }()

	got := readSyncFinished(t, ctx, conn)
	if waited := time.Since(answeredAt); waited > 2*time.Second {
		t.Fatalf("the push came %v after the first device answered; its traffic kept the wait open", waited)
	}
	if !got.TimedOut || got.Answered != 1 {
		t.Fatalf("push = %+v, want timed out with one device answered", got)
	}
}

func TestDeviceSync_TheCountIncludesTheSnapshotAndHeldRows(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	seedKnownPeer(t, d, railPeerID, railFingerprint)
	conn := dialTest(t, ctx, addr, certPath, token)

	deviceSyncWith(t, ctx, conn, "sync-1", true)
	stub.WaitForSend(t, 1, 5*time.Second)
	handleBackfillResponse(context.Background(), d, railSiblingID, syncrail.BackfillResponseBody{
		Section:  syncrail.SectionContacts,
		Full:     true,
		Contacts: []syncrail.ContactSnapshot{{Fingerprint: "bb22cc33dd44ee55ff66007788990011", Nick: "nina", NickAt: 1742600000}},
	})
	snapshotDone(d, railSiblingID, true)

	unknown := missedRow(t, "held-1")
	unknown.Fingerprint = "cc33dd44ee55ff6600778899001122aa"
	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{
		Rows: []syncrail.RowSnapshot{missedRow(t, "placed-1"), unknown}, RangeEnd: 2, Full: true,
	})

	got := readSyncFinished(t, ctx, conn)
	want := ipc.DeviceSyncFinishedPush{Full: true, Siblings: 1, Answered: 1, RowsApplied: 1, SnapshotApplied: 1, Held: 1}
	if got != want {
		t.Fatalf("push = %+v, want %+v", got, want)
	}
}

func TestDeviceSync_ARequestWaitsForAnAutomaticSweepsGate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	conn := dialTest(t, ctx, addr, certPath, token)

	if !d.gapfill.enter() {
		t.Fatal("fixture: the sweep gate was already taken")
	}
	time.AfterFunc(300*time.Millisecond, d.gapfill.leave)
	if got := deviceSyncWith(t, ctx, conn, "sync-1", true); got.AlreadyRunning {
		t.Fatalf("reply = %+v, want a sweep started once the automatic one let go", got)
	}
	stub.WaitForSend(t, 1, 5*time.Second)
	if !d.manualSync.waiting() {
		t.Fatal("no manual sweep is waiting for answers")
	}
}

func TestDeviceSync_AnAutomaticSweepRunsWhileAManualOneWaits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	conn := dialTest(t, ctx, addr, certPath, token)

	deviceSyncWith(t, ctx, conn, "sync-1", false)
	stub.WaitForSend(t, 1, 5*time.Second)
	until := time.Now().Add(2 * time.Second)
	for d.gapfill.running.Load() && time.Now().Before(until) {
		time.Sleep(5 * time.Millisecond)
	}
	if !d.manualSync.waiting() {
		t.Fatal("fixture: the manual sweep is not waiting")
	}
	if out := sweepGapFill(context.Background(), d); out != sweepStarted {
		t.Fatalf("automatic sweep = %q while a manual one waits, want started", out)
	}
}
