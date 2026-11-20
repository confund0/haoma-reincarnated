package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
)

func seedInboundRow(t *testing.T, d *daemon, peerID string) chat.ChatID {
	t.Helper()
	cid := chatIDForPeer(t, d, peerID)
	body, _ := json.Marshal(events.TextBody{Text: "hi"})
	if _, err := d.events.AppendInbound(events.InboundParams{
		ChatID: cid, Kind: events.KindText, SenderTs: time.Now().Unix(),
		SenderSeq: 1, EnvelopeID: "env-plan46", MsgID: "1111111111111111bbbbbbbbbbbbbbbb",
		Status: events.DecryptOK, Body: body,
	}); err != nil {
		t.Fatalf("seedInboundRow: %v", err)
	}
	return cid
}

func readAtOf(t *testing.T, d *daemon, cid chat.ChatID) int64 {
	t.Helper()
	rows, err := d.events.List(cid, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	return rows[0].ReadAt
}

func waitForReadAt(t *testing.T, d *daemon, cid chat.ChatID) int64 {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if got := readAtOf(t, d, cid); got != 0 {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("ReadAt still 0 after 2s, want the unlock mark to have landed")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAutoMarkOnArrival_SoftLocked_MarksNothing(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000000a"
	preEstablishSession(t, d, peerID)
	cid := seedInboundRow(t, d, peerID)

	d.clientFocus.Store(&ipc.ClientFocusRequest{ChatID: string(cid), ScrollPosition: 0})
	d.clientSoftLocked.Store(true)

	d.autoMarkOnArrival(context.Background(), cid)

	if got := readAtOf(t, d, cid); got != 0 {
		t.Errorf("ReadAt = %d, want 0 (soft-locked, must not mark)", got)
	}
	if stub.SendCalls != 0 {
		t.Errorf("haomad saw %d /send calls, want 0 (no receipt while soft-locked)", stub.SendCalls)
	}
}

func TestAutoMarkOnArrival_Unlocked_MarksAsBefore(t *testing.T) {

	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000000a"
	preEstablishSession(t, d, peerID)
	cid := seedInboundRow(t, d, peerID)

	d.clientFocus.Store(&ipc.ClientFocusRequest{ChatID: string(cid), ScrollPosition: 0})

	d.autoMarkOnArrival(context.Background(), cid)

	if got := readAtOf(t, d, cid); got == 0 {
		t.Errorf("ReadAt = 0, want non-zero (unlocked, should mark)")
	}
	if stub.SendCalls == 0 {
		t.Errorf("haomad saw 0 /send calls, want > 0 (receipt should ship when unlocked)")
	}
}

func TestHandleMarkRead_SoftLocked_DropsRequestButAnswers(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000000a"
	preEstablishSession(t, d, peerID)
	cid := seedInboundRow(t, d, peerID)
	d.clientSoftLocked.Store(true)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameMarkRead, "m1", ipc.MarkReadRequest{ChatID: string(cid)}))
	resp := readUntil(t, ctx, conn, ipc.FrameMarkedRead, ipc.FrameError)
	if resp.Type != ipc.FrameMarkedRead {
		t.Fatalf("expected marked_read reply (so the caller's pending map doesn't leak); got %s", resp.Type)
	}
	var p ipc.MarkedReadResponse
	if err := json.Unmarshal(resp.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.MarkedCount != 0 {
		t.Errorf("MarkedCount = %d, want 0 (soft-locked, nothing marked)", p.MarkedCount)
	}

	if got := readAtOf(t, d, cid); got != 0 {
		t.Errorf("ReadAt = %d, want 0 (soft-locked, must not mark)", got)
	}
	if stub.SendCalls != 0 {
		t.Errorf("haomad saw %d /send calls, want 0 (no receipt while soft-locked)", stub.SendCalls)
	}
}

func TestHandleClientLockState_Unlock_MarksFocusedChatAtTop(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	sd := newSessionDispatcher(d)
	const peerID = "0000000000000000000000000000000a"
	preEstablishSession(t, d, peerID)
	cid := seedInboundRow(t, d, peerID)

	d.clientFocus.Store(&ipc.ClientFocusRequest{ChatID: string(cid), ScrollPosition: 0})

	d.clientSoftLocked.Store(true)

	frame, _ := ipc.NewFrame(ipc.FrameClientLockState, "", ipc.ClientLockStateRequest{SoftLocked: false})
	sd.handleClientLockState(nil, frame)

	if got := waitForReadAt(t, d, cid); got == 0 {
		t.Errorf("ReadAt = 0, want non-zero (unlock should mark the focused chat)")
	}
}

func TestHandleClientLockState_Unlock_ScrolledAway_MarksNothing(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	sd := newSessionDispatcher(d)
	const peerID = "0000000000000000000000000000000a"
	preEstablishSession(t, d, peerID)
	cid := seedInboundRow(t, d, peerID)

	d.clientFocus.Store(&ipc.ClientFocusRequest{ChatID: string(cid), ScrollPosition: 3})
	d.clientSoftLocked.Store(true)

	frame, _ := ipc.NewFrame(ipc.FrameClientLockState, "", ipc.ClientLockStateRequest{SoftLocked: false})
	sd.handleClientLockState(nil, frame)

	if got := readAtOf(t, d, cid); got != 0 {
		t.Errorf("ReadAt = %d, want 0 (scrolled into history, not looking at the arrival)", got)
	}
}

func TestHandleClientLockState_Unlock_NoFocus_MarksNothing(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	sd := newSessionDispatcher(d)
	const peerID = "0000000000000000000000000000000a"
	preEstablishSession(t, d, peerID)
	cid := seedInboundRow(t, d, peerID)

	d.clientSoftLocked.Store(true)

	frame, _ := ipc.NewFrame(ipc.FrameClientLockState, "", ipc.ClientLockStateRequest{SoftLocked: false})
	sd.handleClientLockState(nil, frame)

	if got := readAtOf(t, d, cid); got != 0 {
		t.Errorf("ReadAt = %d, want 0 (no focus reported, nothing to mark)", got)
	}
}

func TestReadLoop_ClientLockState_OrdersBeforeMarkRead(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000000a"
	preEstablishSession(t, d, peerID)
	cid := seedInboundRow(t, d, peerID)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameClientLockState, "", ipc.ClientLockStateRequest{SoftLocked: true}))
	writeFrame(t, ctx, conn, mustFrame(ipc.FrameMarkRead, "m1", ipc.MarkReadRequest{ChatID: string(cid)}))

	resp := readUntil(t, ctx, conn, ipc.FrameMarkedRead, ipc.FrameError)
	if resp.Type != ipc.FrameMarkedRead {
		t.Fatalf("expected marked_read reply; got %s", resp.Type)
	}
	var p ipc.MarkedReadResponse
	if err := json.Unmarshal(resp.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.MarkedCount != 0 {
		t.Errorf("MarkedCount = %d, want 0 — mark_read must see soft_locked=true even though"+
			" it was sent right behind client_lock_state on the same connection", p.MarkedCount)
	}
}

func TestHandleClientLockState_SameState_MarksNothing(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	sd := newSessionDispatcher(d)
	const peerID = "0000000000000000000000000000000a"
	preEstablishSession(t, d, peerID)
	cid := seedInboundRow(t, d, peerID)
	d.clientFocus.Store(&ipc.ClientFocusRequest{ChatID: string(cid), ScrollPosition: 0})

	frame, _ := ipc.NewFrame(ipc.FrameClientLockState, "", ipc.ClientLockStateRequest{SoftLocked: false})
	sd.handleClientLockState(nil, frame)
	if got := readAtOf(t, d, cid); got != 0 {
		t.Errorf("false->false: ReadAt = %d, want 0", got)
	}

	frame2, _ := ipc.NewFrame(ipc.FrameClientLockState, "", ipc.ClientLockStateRequest{SoftLocked: true})
	sd.handleClientLockState(nil, frame2)
	frame3, _ := ipc.NewFrame(ipc.FrameClientLockState, "", ipc.ClientLockStateRequest{SoftLocked: true})
	sd.handleClientLockState(nil, frame3)
	if got := readAtOf(t, d, cid); got != 0 {
		t.Errorf("true->true: ReadAt = %d, want 0", got)
	}
}
