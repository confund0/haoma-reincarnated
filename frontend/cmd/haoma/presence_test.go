package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/linkeddevice"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/session"
	"haoma-frontend/internal/signal"
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

func TestDispatch_PushPresence_TargetedSendReachesEveryDeviceOfThePerson(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const phone = "00000000000000000000000000003001"
	const laptop = "00000000000000000000000000003002"
	preEstablishSession(t, d, phone)
	attachSecondDevice(t, d, phone, laptop)

	state := msg.PresenceBusy
	d.presenceOverride.Store(&state)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FramePushPresence, "p1", ipc.PushPresenceRequest{
		Target: phone,
	}))

	stub.WaitForSend(t, 2, 3*time.Second)
	if got := stub.SentPeerIDs; len(got) != 2 || got[0] != phone || got[1] != laptop {
		t.Fatalf("presence addressed %v, want [%s %s] — the targeted send must fan out over the conversation's routing set", got, phone, laptop)
	}

	if bytes.Equal(stub.SentPayloads[0], stub.SentPayloads[1]) {
		t.Error("both devices got byte-identical ciphertext — the encrypt is not per device")
	}
}

func TestDispatch_PushPresence_TargetedSendResolvesFromEitherDevice(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const phone = "00000000000000000000000000003101"
	const laptop = "00000000000000000000000000003102"
	preEstablishSession(t, d, phone)
	attachSecondDevice(t, d, phone, laptop)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FramePushPresence, "p1", ipc.PushPresenceRequest{
		Target: laptop,
	}))

	stub.WaitForSend(t, 2, 3*time.Second)
	if got := stub.SentPeerIDs; len(got) != 2 || got[0] != phone || got[1] != laptop {
		t.Fatalf("presence addressed %v, want [%s %s] — naming the second device must resolve the same conversation", got, phone, laptop)
	}
}

func TestDispatch_PushPresence_TargetedSendToADeviceWithNoConversation(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const stray = "00000000000000000000000000003201"
	remote, err := signal.Bootstrap(3)
	if err != nil {
		t.Fatal(err)
	}

	processRemoteBundle(t, d, stray, remote)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FramePushPresence, "p1", ipc.PushPresenceRequest{
		Target: stray,
	}))

	stub.WaitForSend(t, 1, 3*time.Second)
	if got := stub.SentPeerIDs; len(got) != 1 || got[0] != stray {
		t.Fatalf("presence addressed %v, want [%s] — an unresolved device is still told on its own", got, stray)
	}
}

func TestDispatch_PushPresence_BroadcastSkipsMyOwnDevices(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const contact = "00000000000000000000000000003301"
	const myOther = "00000000000000000000000000003302"
	const retired = "00000000000000000000000000003303"
	preEstablishSession(t, d, contact)
	preEstablishSession(t, d, myOther)
	preEstablishSession(t, d, retired)

	d.linkedDevices = linkeddevice.New(d.store)
	if _, err := d.linkedDevices.Link(myOther, time.Now().Unix()); err != nil {
		t.Fatalf("link my other device: %v", err)
	}
	stub.PeerList = []backendapi.Peer{
		{ID: contact},
		{ID: myOther},
		{ID: retired, RetiredAt: 1_750_000_000},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FramePushPresence, "p1", ipc.PushPresenceRequest{}))

	stub.WaitForSend(t, 1, 3*time.Second)

	select {
	case <-stub.SendDone:
		t.Fatalf("broadcast sent more than once; addressed %v", stub.SentPeerIDs)
	case <-time.After(500 * time.Millisecond):
	}
	if got := stub.SentPeerIDs; len(got) != 1 || got[0] != contact {
		t.Fatalf("broadcast addressed %v, want [%s] only", got, contact)
	}
}

func TestShipPresence_DeadlineIsReportedApartFromADeviceRefusing(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	const phone = "00000000000000000000000000003601"
	const laptop = "00000000000000000000000000003602"
	preEstablishSession(t, d, phone)
	attachSecondDevice(t, d, phone, laptop)

	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	stub.SendGate = gate
	defer close(gate)

	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()

	err := d.shipPresenceToPersonBehind(ctx, phone, msg.PresenceBusy)
	if err == nil {
		t.Fatal("a fan-out cut short by the local deadline reported success — a reader cannot tell it from a device that has no session")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error does not unwrap to the deadline: %v", err)
	}
	if !strings.Contains(err.Error(), "ran out of time") {
		t.Errorf("error text does not name the timeout: %v", err)
	}

	stub.WaitForSend(t, 1, 2*time.Second)
	if got := stub.SentPeerIDs; len(got) != 1 || got[0] != phone {
		t.Fatalf("devices enqueued for = %v, want [%s] — this test is only about a fan-out that got part way", got, phone)
	}
}

func TestShipPresence_PrepareFailureIsNotReportedAsABackendSend(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	const stray = "00000000000000000000000000003701"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := d.shipPresenceToDevice(ctx, stray, msg.PresenceAvailable)
	if err == nil {
		t.Fatal("a send to a device with no session reported success")
	}
	if !errors.Is(err, session.ErrNoSession) {
		t.Errorf("error does not unwrap to ErrNoSession: %v", err)
	}
	if strings.Contains(err.Error(), "backend send") {
		t.Errorf("a prepare failure is reported as a backend send failure: %v", err)
	}
	if !strings.Contains(err.Error(), "nothing reached haomad") {
		t.Errorf("error text does not say the send never left this process: %v", err)
	}
	if stub.SendCalls != 0 {
		t.Errorf("haomad saw %d /send calls for a device with no session, want 0", stub.SendCalls)
	}
}

func TestDispatch_ClientFocus_PresencePushReachesEveryDeviceOfThePerson(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const phone = "00000000000000000000000000003401"
	const laptop = "00000000000000000000000000003402"
	preEstablishSession(t, d, phone)
	dc := attachSecondDevice(t, d, phone, laptop)

	state := msg.PresenceAway
	d.presenceOverride.Store(&state)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameClientFocus, "f1", ipc.ClientFocusRequest{
		ChatID:         string(dc.ID),
		ScrollPosition: 0,
	}))

	stub.WaitForSend(t, 2, 3*time.Second)
	if got := stub.SentPeerIDs; len(got) != 2 || got[0] != phone || got[1] != laptop {
		t.Fatalf("chat-open push addressed %v, want [%s %s]", got, phone, laptop)
	}
}

func TestDispatch_ClientFocus_PresencePushOutlivesTheClient(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const phone = "00000000000000000000000000003501"
	const laptop = "00000000000000000000000000003502"
	preEstablishSession(t, d, phone)
	dc := attachSecondDevice(t, d, phone, laptop)

	release := make(chan struct{})
	stub.SendGate = release

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)
	writeFrame(t, ctx, conn, mustFrame(ipc.FrameClientFocus, "f1", ipc.ClientFocusRequest{
		ChatID:         string(dc.ID),
		ScrollPosition: 0,
	}))

	stub.WaitForSendEntered(t, 1, 3*time.Second)
	_ = conn.Close(websocket.StatusNormalClosure, "client gone")
	close(release)

	stub.WaitForSend(t, 2, 5*time.Second)
	if got := stub.SentPeerIDs; len(got) != 2 || got[0] != phone || got[1] != laptop {
		t.Fatalf("chat-open push addressed %v after the client disconnected, want [%s %s]", got, phone, laptop)
	}
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
