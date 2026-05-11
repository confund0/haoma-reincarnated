package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/msg"
)

func TestDispatch_SetPresenceOverride_StoresAndResetsEffectiveState(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	if got := d.effectivePresenceState(); got != msg.PresenceAvailable {
		t.Errorf("default effective state = %q, want %q", got, msg.PresenceAvailable)
	}

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSetPresenceOverride, "p1", ipc.SetPresenceOverrideRequest{
		State: msg.PresenceBusy,
	}))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && d.effectivePresenceState() != msg.PresenceBusy {
		time.Sleep(10 * time.Millisecond)
	}
	if got := d.effectivePresenceState(); got != msg.PresenceBusy {
		t.Errorf("after override busy: effective = %q, want %q", got, msg.PresenceBusy)
	}

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSetPresenceOverride, "p2", ipc.SetPresenceOverrideRequest{
		State: "",
	}))
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && d.effectivePresenceState() != msg.PresenceAvailable {
		time.Sleep(10 * time.Millisecond)
	}
	if got := d.effectivePresenceState(); got != msg.PresenceAvailable {
		t.Errorf("after reset: effective = %q, want %q", got, msg.PresenceAvailable)
	}
}

func TestDispatch_SetPresenceOverride_RejectsUnknownState(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	_, addr, certPath, token := newTestDaemon(t, stub)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSetPresenceOverride, "p1", ipc.SetPresenceOverrideRequest{
		State: "lurking",
	}))
	resp := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	_ = json.Unmarshal(resp.Payload, &ep)
	if ep.Code != "bad_request" {
		t.Errorf("err code = %q, want bad_request (msg: %s)", ep.Code, ep.Message)
	}
}

func TestDispatch_PushPresence_SendsKindPresenceWithSourceHaoma(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000000a"
	preEstablishSession(t, d, peerID)

	state := msg.PresenceAway
	d.presenceOverride.Store(&state)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FramePushPresence, "p1", ipc.PushPresenceRequest{
		Target: peerID,
	}))

	stub.WaitForSend(t, 1, 2*time.Second)
	if stub.SendCalls != 1 {
		t.Fatalf("haomad stub saw %d POST /send calls, want 1", stub.SendCalls)
	}
	if stub.LastSendReq.PeerID != peerID {
		t.Errorf("PeerID = %q, want %q", stub.LastSendReq.PeerID, peerID)
	}
	if stub.LastSendReq.Kind != backendapi.WireKindPresence {
		t.Errorf("Kind = %q, want %q", stub.LastSendReq.Kind, backendapi.WireKindPresence)
	}
	if stub.LastSendReq.PresenceSource != backendapi.PresenceSourceHaoma {
		t.Errorf("PresenceSource = %q, want %q", stub.LastSendReq.PresenceSource, backendapi.PresenceSourceHaoma)
	}
	if len(stub.LastSendReq.Payload) == 0 {
		t.Error("Payload empty; expected libsignal ciphertext of the presence wrapper")
	}
}

func TestDispatch_PushPresence_BroadcastEmptyTargetHitsListPeers(t *testing.T) {

	t.Skip("broadcast-empty-target path needs a /peers stub; covered in C.2 integration tests")
}

func TestDispatch_ClientFocus_FiresPresencePushForDirectChat(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000000c"
	preEstablishSession(t, d, peerID)
	chatID := chatIDForPeer(t, d, peerID)

	state := msg.PresenceBusy
	d.presenceOverride.Store(&state)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameClientFocus, "f1", ipc.ClientFocusRequest{
		ChatID:         string(chatID),
		ScrollPosition: 0,
	}))

	stub.WaitForSend(t, 1, 2*time.Second)
	if stub.SendCalls != 1 {
		t.Fatalf("haomad stub saw %d POST /send calls, want 1", stub.SendCalls)
	}
	if stub.LastSendReq.PeerID != peerID {
		t.Errorf("PeerID = %q, want %q", stub.LastSendReq.PeerID, peerID)
	}
	if stub.LastSendReq.Kind != backendapi.WireKindPresence {
		t.Errorf("Kind = %q, want %q", stub.LastSendReq.Kind, backendapi.WireKindPresence)
	}
	if stub.LastSendReq.PresenceSource != backendapi.PresenceSourceHaoma {
		t.Errorf("PresenceSource = %q, want %q", stub.LastSendReq.PresenceSource, backendapi.PresenceSourceHaoma)
	}
}

func TestDispatch_ClientFocus_EmptyChatIDDoesNotFirePresence(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	_, addr, certPath, token := newTestDaemon(t, stub)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameClientFocus, "f1", ipc.ClientFocusRequest{
		ChatID: "",
	}))

	select {
	case <-stub.SendDone:
		t.Fatalf("focus push with empty ChatID fired a /send (got %d)", stub.SendCalls)
	case <-time.After(200 * time.Millisecond):
	}
}
