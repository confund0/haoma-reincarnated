package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/coder/websocket"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/peerstate"
)

func seedChatPrimaryOnTheOldPairing(t *testing.T, d *daemon, stub *haomadStub) chat.ChatID {
	t.Helper()
	pairedAt := time.Now().Unix() - 3600
	seedAlicePairingAt(t, d, dcAliceOldID, pairedAt)
	seedAlicePairingAt(t, d, dcAliceID, pairedAt)
	first, err := d.chats.CreateDirect(dcAliceOldID)
	if err != nil {
		t.Fatal(err)
	}
	folded, fresh, err := d.createDirectWithDefaults(dcAliceID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh || folded.ID != first.ID {
		t.Fatalf("precondition: the second pairing did not fold into the first chat (fresh=%v, chat %s, want %s)", fresh, folded.ID, first.ID)
	}
	if folded.PeerID != dcAliceOldID || !slices.Equal(folded.PeerIDs, []string{dcAliceOldID, dcAliceID}) {
		t.Fatalf("precondition: primary %s, routes %v; want the old pairing first, as on Bob's device", folded.PeerID, folded.PeerIDs)
	}
	stub.PeerList = []backendapi.Peer{
		{ID: dcAliceID, KnownAddresses: []string{"alice-computer-onion"}},
		{ID: dcAliceOldID, KnownAddresses: []string{"alice-phone-onion"}},
	}
	return first.ID
}

func awaitChatUpdated(t *testing.T, ctx context.Context, conn *websocket.Conn, chatID chat.ChatID, wantPeer string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var seen []string
	for {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("no chat.updated naming %s as the primary of %s; the chat.updated pushes seen named %v (%v)", wantPeer, chatID, seen, err)
		}
		f, err := ipc.Decode(raw)
		if err != nil || f.Type != ipc.FrameChatUpdated {
			continue
		}
		var p ipc.ChatUpdatedPayload
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p.Chat.ChatID != string(chatID) {
			continue
		}
		if p.Chat.PeerID == wantPeer {
			return
		}
		seen = append(seen, p.Chat.PeerID)
	}
}

func sendTextOverIPC(t *testing.T, ctx context.Context, conn *websocket.Conn, id, peerID, text string) {
	t.Helper()
	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendText, id, ipc.SendTextRequest{PeerID: peerID, Text: text}))
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("send_text %s got no answer: %v", id, err)
		}
		f, err := ipc.Decode(raw)
		if err != nil || f.ID != id {
			continue
		}
		if f.Type != ipc.FrameTextSent {
			t.Fatalf("send_text %s answered %s: %s", id, f.Type, f.Payload)
		}
		return
	}
}

func readChat(t *testing.T, d *daemon, id chat.ChatID) *chat.DirectChat {
	t.Helper()
	c, err := d.chats.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return c.(*chat.DirectChat)
}

func TestRetiredRoutes_UnlinkNotice_PrimaryMovesToTheLivePairing(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	chatID := seedChatPrimaryOnTheOldPairing(t, d, stub)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	phone := newAliceSession(t, d, dcAliceOldID, 1)
	phone.sendText(t, ctx, "env-rr-phone-1", 1, time.Now().Unix(), 2)
	computer := newAliceSession(t, d, dcAliceID, 0)
	now := time.Now().Unix()
	computer.sendPresence(t, ctx, "env-rr-notice", 1, now, 1, now)

	assertRetiredExactly(t, stub, dcAliceID, dcAliceOldID)
	got := readChat(t, d, chatID)
	if got.PeerID != dcAliceID {
		t.Fatalf("the chat's primary is %s after the retire, want the live pairing %s", got.PeerID, dcAliceID)
	}
	if !slices.Equal(got.PeerIDs, []string{dcAliceID}) {
		t.Fatalf("the routing set is %v after the retire, want only the live pairing %s", got.PeerIDs, dcAliceID)
	}
	if slices.Contains(got.Members, dcAliceOldID) {
		t.Fatalf("the member list still holds the retired pairing: %v", got.Members)
	}

	viaOld, err := d.chats.GetByDirectPeer(dcAliceOldID)
	if err != nil || viaOld.ID != chatID {
		t.Fatalf("the retired pairing no longer resolves to its chat: %v, %v", viaOld, err)
	}
	awaitChatUpdated(t, ctx, conn, chatID, dcAliceID)

	before := len(stub.SentPeerIDs)
	sendTextOverIPC(t, ctx, conn, "st-live", dcAliceID, "after the unlink")
	sendTextOverIPC(t, ctx, conn, "st-stale", dcAliceOldID, "from a client that still holds the old id")
	if sent := stub.SentPeerIDs[before:]; !slices.Equal(sent, []string{dcAliceID, dcAliceID}) {
		t.Fatalf("the two sends went to %v, want the live pairing %s twice and never the retired one", sent, dcAliceID)
	}
}

func TestRetiredRoutes_CountFromOlderSender_PrimaryMovesToTheLivePairing(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	chatID := seedChatPrimaryOnTheOldPairing(t, d, stub)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	computer := newAliceSession(t, d, dcAliceID, 0)
	computer.olderBuild = true
	computer.sendText(t, ctx, "env-rr-count", 1, time.Now().Unix(), 1)

	assertRetiredExactly(t, stub, dcAliceID, dcAliceOldID)
	got := readChat(t, d, chatID)
	if got.PeerID != dcAliceID || !slices.Equal(got.PeerIDs, []string{dcAliceID}) {
		t.Fatalf("after the count rule: primary %s, routes %v; want %s alone", got.PeerID, got.PeerIDs, dcAliceID)
	}
}

func TestRetiredRoutes_OperatorRetire_PrimaryMovesToTheLivePairing(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	chatID := seedChatPrimaryOnTheOldPairing(t, d, stub)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)
	writeFrame(t, ctx, conn, mustFrame(ipc.FramePeerAction, "pa-rr", ipc.PeerActionRequest{
		PeerID: dcAliceOldID, Action: ipc.PeerActionRetire,
	}))
	awaitChatUpdated(t, ctx, conn, chatID, dcAliceID)
	got := readChat(t, d, chatID)
	if got.PeerID != dcAliceID || !slices.Equal(got.PeerIDs, []string{dcAliceID}) {
		t.Fatalf("after the operator's retire: primary %s, routes %v; want %s alone", got.PeerID, got.PeerIDs, dcAliceID)
	}
}

func TestRetiredRoutes_NoLivePairingLeft_ChatStaysRetired(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	seedAlicePairingAt(t, d, dcAliceOldID, time.Now().Unix()-3600)
	dc, err := d.chats.CreateDirect(dcAliceOldID)
	if err != nil {
		t.Fatal(err)
	}
	stub.PeerList = []backendapi.Peer{{ID: dcAliceOldID, KnownAddresses: []string{"alice-phone-onion"}}}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)
	writeFrame(t, ctx, conn, mustFrame(ipc.FramePeerAction, "pa-rr-last", ipc.PeerActionRequest{
		PeerID: dcAliceOldID, Action: ipc.PeerActionRetire,
	}))

	readUntil(t, ctx, conn, ipc.FramePeerUpdated)
	if stub.PeerList[0].RetiredAt == 0 {
		t.Fatal("precondition: the stub did not retire the pairing")
	}
	got := readChat(t, d, dc.ID)
	if got.PeerID != dcAliceOldID || !slices.Equal(got.PeerIDs, []string{dcAliceOldID}) {
		t.Fatalf("a chat with no live pairing left was changed: primary %s, routes %v", got.PeerID, got.PeerIDs)
	}
}

func TestRetiredRoutes_Sweep_MovesAChatLeftOnARetiredPrimary(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	chatID := seedChatPrimaryOnTheOldPairing(t, d, stub)
	stub.PeerList[1].RetiredAt = time.Now().Unix() - 60

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	sweepRetiredRoutes(ctx, d)
	awaitChatUpdated(t, ctx, conn, chatID, dcAliceID)
	got := readChat(t, d, chatID)
	if got.PeerID != dcAliceID || !slices.Equal(got.PeerIDs, []string{dcAliceID}) {
		t.Fatalf("after the sweep: primary %s, routes %v; want %s alone", got.PeerID, got.PeerIDs, dcAliceID)
	}

	peers, err := peerRegistry(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if plan := planRetiredRoutes(got, peers, d.pairingFingerprint, ""); plan.outcome != routesOutcomeNone {
		t.Fatalf("a second sweep would still act on the chat: %+v", plan)
	}
	if _, changed, err := d.chats.DropRoutes(chatID, dcAliceID, []string{dcAliceOldID}); err != nil || changed {
		t.Fatalf("writing the same decision again changed the chat (changed=%v, err=%v)", changed, err)
	}
}

func TestRetiredRoutes_SweepRunsWhenTheBackendIsReady(t *testing.T) {
	peers := []backendapi.Peer{
		{ID: dcAliceID, KnownAddresses: []string{"alice-computer-onion"}},
		{ID: dcAliceOldID, RetiredAt: time.Now().Unix() - 60},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /inbox", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(backendapi.InboxResponse{})
	})
	mux.HandleFunc("GET /peers", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(backendapi.PeersResponse{Peers: peers})
	})
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	srv := httptest.NewServer(mux)

	t.Cleanup(srv.Close)

	d := minimalDaemon(t, srv.URL)
	d.peerMeta = peerstate.NewMeta(d.store)
	pairedAt := time.Now().Unix() - 3600
	seedAlicePairingAt(t, d, dcAliceOldID, pairedAt)
	seedAlicePairingAt(t, d, dcAliceID, pairedAt)
	first, err := d.chats.CreateDirect(dcAliceOldID)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.chats.AttachPeerID(first.ID, dcAliceID); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		eventsLoop(ctx, d)
	}()
	t.Cleanup(func() {
		cancel()
		<-loopDone
	})

	eventually(t, "the chat to move off the retired primary after the backend-ready transition", func() bool {
		c, err := d.chats.Get(first.ID)
		return err == nil && c.(*chat.DirectChat).PeerID == dcAliceID
	})
}

func TestPlanRetiredRoutes(t *testing.T) {
	const (
		phone    = "phone"
		computer = "computer"
		stranger = "stranger"
		unknown  = "unknown"
		fpAlice  = "fp-alice"
		fpOther  = "fp-other"
	)
	fps := map[string]string{phone: fpAlice, computer: fpAlice, stranger: fpOther, unknown: fpAlice, "sender": fpAlice}
	fingerprintOf := func(id string) (string, error) {
		if id == "unreadable" {
			return "", errors.New("store down")
		}
		return fps[id], nil
	}
	live := backendapi.Peer{}
	gone := backendapi.Peer{RetiredAt: 1}
	reg := func(states map[string]backendapi.Peer) map[string]backendapi.Peer {
		out := map[string]backendapi.Peer{}
		for id, p := range states {
			p.ID = id
			out[id] = p
		}
		return out
	}
	direct := func(routes ...string) *chat.DirectChat {
		return &chat.DirectChat{PeerID: routes[0], PeerIDs: routes}
	}
	cases := []struct {
		name        string
		dc          *chat.DirectChat
		peers       map[string]backendapi.Peer
		prefer      string
		wantPrimary string
		wantDrop    []string
		wantOutcome string
	}{
		{"primary retired, live pairing of the same person", direct(phone, computer),
			reg(map[string]backendapi.Peer{phone: gone, computer: live}), "", computer, []string{phone}, routesOutcomeMoved},
		{"primary retired, the only live pairing is somebody else", direct(phone, stranger),
			reg(map[string]backendapi.Peer{phone: gone, stranger: live}), "", "", nil, routesOutcomeNoLivePairing},
		{"primary retired, no other pairing", direct(phone),
			reg(map[string]backendapi.Peer{phone: gone}), "", "", nil, routesOutcomeNoLivePairing},
		{"primary retired, the other pairing is retired too", direct(phone, computer),
			reg(map[string]backendapi.Peer{phone: gone, computer: gone}), "", "", nil, routesOutcomeNoLivePairing},
		{"primary retired, the other pairing is not in haomad's list", direct(phone, unknown),
			reg(map[string]backendapi.Peer{phone: gone}), "", "", nil, routesOutcomeNoLivePairing},
		{"primary retired, the other pairing's person cannot be read", direct(phone, "unreadable"),
			reg(map[string]backendapi.Peer{phone: gone, "unreadable": live}), "", "", nil, routesOutcomeNoLivePairing},
		{"primary live, another pairing retired", direct(computer, phone),
			reg(map[string]backendapi.Peer{phone: gone, computer: live}), "", computer, []string{phone}, routesOutcomeDropped},
		{"nothing retired", direct(phone, computer),
			reg(map[string]backendapi.Peer{phone: live, computer: live}), "", "", nil, routesOutcomeNone},
		{"a chat with no pairing here yet", &chat.DirectChat{Fingerprint: fpAlice},
			reg(map[string]backendapi.Peer{}), "", "", nil, routesOutcomeNone},
		{"primary retired, its person cannot be read", direct("unreadable", computer),
			reg(map[string]backendapi.Peer{"unreadable": gone, computer: live}), "", "", nil, routesOutcomeNoFingerprint},
		{"primary retired, its person cannot be read, the chat names the person", &chat.DirectChat{PeerID: "unreadable", PeerIDs: []string{"unreadable", computer}, Fingerprint: fpAlice},
			reg(map[string]backendapi.Peer{"unreadable": gone, computer: live}), "", computer, []string{"unreadable"}, routesOutcomeMoved},
		{"the sender is preferred over an earlier live pairing", direct(phone, computer, "sender"),
			reg(map[string]backendapi.Peer{phone: gone, computer: live, "sender": live}), "sender", "sender", []string{phone}, routesOutcomeMoved},
		{"a preferred pairing of somebody else is not chosen", direct(phone, computer, stranger),
			reg(map[string]backendapi.Peer{phone: gone, computer: live, stranger: live}), stranger, computer, []string{phone}, routesOutcomeMoved},
		{"a preferred pairing that is retired is not chosen", direct(phone, computer, "sender"),
			reg(map[string]backendapi.Peer{phone: gone, computer: live, "sender": gone}), "sender", computer, []string{phone, "sender"}, routesOutcomeMoved},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := planRetiredRoutes(tc.dc, tc.peers, fingerprintOf, tc.prefer)
			if got.primary != tc.wantPrimary || !slices.Equal(got.drop, tc.wantDrop) || got.outcome != tc.wantOutcome {
				t.Fatalf("plan = %+v, want primary %q drop %v outcome %s", got, tc.wantPrimary, tc.wantDrop, tc.wantOutcome)
			}
		})
	}

	noFP := func(id string) (string, error) {
		if id == phone {
			return "", nil
		}
		return fingerprintOf(id)
	}
	dc := &chat.DirectChat{PeerID: phone, PeerIDs: []string{phone, computer}, Fingerprint: fpAlice}
	if got := planRetiredRoutes(dc, reg(map[string]backendapi.Peer{phone: gone, computer: live}), noFP, ""); got.primary != computer {
		t.Fatalf("with the chat's fingerprint standing in: plan = %+v, want the computer", got)
	}
	dc.Fingerprint = fpOther
	if got := planRetiredRoutes(dc, reg(map[string]backendapi.Peer{phone: gone, computer: live}), fingerprintOf, ""); got.outcome != routesOutcomeFingerprintBad {
		t.Fatalf("with the chat and its primary on two people: plan = %+v, want %s", got, routesOutcomeFingerprintBad)
	}
}

func TestRetiredRoutes_FoldIntoARetiredChat_MovesItToTheNewPairing(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	seedAlicePairingAt(t, d, dcAliceOldID, time.Now().Unix()-3600)
	old, err := d.chats.CreateDirect(dcAliceOldID)
	if err != nil {
		t.Fatal(err)
	}
	stub.PeerList = []backendapi.Peer{{ID: dcAliceOldID, RetiredAt: time.Now().Unix() - 60}}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	seedAlicePairingAt(t, d, dcAliceID, time.Now().Unix())
	stub.PeerList = append(stub.PeerList, backendapi.Peer{ID: dcAliceID, KnownAddresses: []string{"alice-new-onion"}})
	folded, fresh, err := d.createDirectWithDefaults(dcAliceID)
	if err != nil || fresh || folded.ID != old.ID {
		t.Fatalf("precondition: the new pairing did not fold into the old chat (fresh=%v, err=%v)", fresh, err)
	}
	if folded.PeerID != dcAliceOldID {
		t.Fatalf("precondition: the fold moved the primary itself to %s", folded.PeerID)
	}
	pushPeerPaired(ctx, d, dcAliceID, "test")

	got := readChat(t, d, old.ID)
	if got.PeerID != dcAliceID || !slices.Equal(got.PeerIDs, []string{dcAliceID}) {
		t.Fatalf("after the pairing: primary %s, routes %v; want the new pairing %s alone", got.PeerID, got.PeerIDs, dcAliceID)
	}
	awaitChatUpdated(t, ctx, conn, old.ID, dcAliceID)

	row, err := refreshedPeerEntry(ctx, d, dcAliceID)
	if err != nil {
		t.Fatal(err)
	}
	if row.ChatID != string(old.ID) {
		t.Fatalf("the new pairing's row opens chat %q, want %s", row.ChatID, old.ID)
	}
}

func TestRetiredRoutes_TwoRetiredInOneLoop_MoveOnceToTheSender(t *testing.T) {
	const dcAliceOlderID = "0000000000000000000000000000a10f"
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	pairedAt := time.Now().Unix() - 3600
	for _, id := range []string{dcAliceOldID, dcAliceOlderID, dcAliceID} {
		seedAlicePairingAt(t, d, id, pairedAt)
	}
	first, err := d.chats.CreateDirect(dcAliceOldID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{dcAliceOlderID, dcAliceID} {
		if err := d.chats.AttachPeerID(first.ID, id); err != nil {
			t.Fatal(err)
		}
	}
	stub.PeerList = []backendapi.Peer{
		{ID: dcAliceOldID, KnownAddresses: []string{"a"}},
		{ID: dcAliceOlderID, KnownAddresses: []string{"b"}},
		{ID: dcAliceID, KnownAddresses: []string{"c"}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)
	sender := newAliceSession(t, d, dcAliceID, 0)
	now := time.Now().Unix()
	sender.sendPresence(t, ctx, "env-rr-two", 1, now, 1, now)
	assertRetiredExactly(t, stub, dcAliceID, dcAliceOldID, dcAliceOlderID)

	var named []string
	rows := 0
	readCtx, readCancel := context.WithTimeout(ctx, 5*time.Second)
	defer readCancel()
	for rows < 2 {
		_, raw, err := conn.Read(readCtx)
		if err != nil {
			t.Fatalf("the two retired peer rows were not pushed (got %d): %v", rows, err)
		}
		f, err := ipc.Decode(raw)
		if err != nil {
			continue
		}
		switch f.Type {
		case ipc.FrameChatUpdated:
			var p ipc.ChatUpdatedPayload
			if json.Unmarshal(f.Payload, &p) == nil && p.Chat.ChatID == string(first.ID) {
				named = append(named, p.Chat.PeerID)
			}
		case ipc.FramePeerUpdated:
			var p ipc.PeerUpdatedPayload
			if json.Unmarshal(f.Payload, &p) == nil && p.Peer.RetiredAt != 0 {
				rows++
			}
		}
	}
	if !slices.Equal(named, []string{dcAliceID}) {
		t.Fatalf("chat.updated for the chat named %v, want the sender %s once", named, dcAliceID)
	}
	got := readChat(t, d, first.ID)
	if got.PeerID != dcAliceID || !slices.Equal(got.PeerIDs, []string{dcAliceID}) {
		t.Fatalf("primary %s, routes %v; want the sender alone", got.PeerID, got.PeerIDs)
	}
}

func TestPeerRegistry_RetriesOnce(t *testing.T) {
	prev := peerListRetryPause
	peerListRetryPause = time.Millisecond
	t.Cleanup(func() { peerListRetryPause = prev })
	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /peers", func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, "restarting", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(backendapi.PeersResponse{Peers: []backendapi.Peer{{ID: dcAliceID}}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	d := minimalDaemon(t, srv.URL)
	peers, err := peerRegistry(context.Background(), d)
	if err != nil {
		t.Fatalf("after one failure: %v", err)
	}
	if _, ok := peers[dcAliceID]; !ok || calls != 2 {
		t.Fatalf("calls=%d peers=%v; want two calls and the list", calls, peers)
	}
}

func deleteOverIPC(t *testing.T, ctx context.Context, conn *websocket.Conn, id, peerID string) {
	t.Helper()
	writeFrame(t, ctx, conn, mustFrame(ipc.FramePeerAction, id, ipc.PeerActionRequest{PeerID: peerID, Action: ipc.PeerActionDelete}))
	readUntil(t, ctx, conn, ipc.FramePeerActionApplied)
}

func seedRows(t *testing.T, d *daemon, chatID chat.ChatID, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		body, _ := json.Marshal(map[string]string{"text": "kept"})
		if _, err := d.events.AppendInbound(events.InboundParams{
			ChatID: chatID, Kind: events.KindText, SenderTs: 1000 + int64(i),
			EnvelopeID: fmt.Sprintf("env-keep-%d", i), Status: events.DecryptOK, Body: body,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPeerDelete_RetiredPairing_KeepsTheChat(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	chatID := seedChatPrimaryOnTheOldPairing(t, d, stub)
	seedRows(t, d, chatID, 3)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)
	writeFrame(t, ctx, conn, mustFrame(ipc.FramePeerAction, "pa-ret", ipc.PeerActionRequest{PeerID: dcAliceOldID, Action: ipc.PeerActionRetire}))
	awaitChatUpdated(t, ctx, conn, chatID, dcAliceID)

	deleteOverIPC(t, ctx, conn, "pa-del", dcAliceOldID)

	got := readChat(t, d, chatID)
	if got.PeerID != dcAliceID {
		t.Fatalf("the chat is on %s after the delete, want %s", got.PeerID, dcAliceID)
	}
	rows, err := d.events.List(chatID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("%d rows left, want the 3 the chat held", len(rows))
	}
	if _, err := d.chats.GetByDirectPeer(dcAliceOldID); !errors.Is(err, chat.ErrNotFound) {
		t.Fatalf("the deleted pairing still resolves to a chat: %v", err)
	}
	if viaLive, err := d.chats.GetByDirectPeer(dcAliceID); err != nil || viaLive.ID != chatID {
		t.Fatalf("the live pairing lost its chat: %v", err)
	}
}

func TestPeerDelete_PrimaryOrLastLiveRoute(t *testing.T) {
	cases := []struct {
		name     string
		delete   string
		retire   string
		wantChat bool
	}{
		{"the primary, another pairing live", dcAliceOldID, "", true},
		{"not the primary, no other pairing live", dcAliceID, dcAliceOldID, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
			d, addr, certPath, token := newTestDaemon(t, stub)
			chatID := seedChatPrimaryOnTheOldPairing(t, d, stub)
			seedRows(t, d, chatID, 2)
			for i := range stub.PeerList {
				if stub.PeerList[i].ID == c.retire {
					stub.PeerList[i].RetiredAt = time.Now().Unix() - 60
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			conn := dialTest(t, ctx, addr, certPath, token)
			deleteOverIPC(t, ctx, conn, "pa-del", c.delete)
			_, err := d.chats.Get(chatID)
			if !c.wantChat {
				if !errors.Is(err, chat.ErrNotFound) {
					t.Fatalf("the chat survived the delete: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("the chat went with one of two live pairings: %v", err)
			}
			got := readChat(t, d, chatID)
			if got.PeerID != dcAliceID || !slices.Equal(got.PeerIDs, []string{dcAliceID}) {
				t.Fatalf("primary %s, routes %v; want the live %s alone", got.PeerID, got.PeerIDs, dcAliceID)
			}
			if rows, _ := d.events.List(chatID, 0, 0); len(rows) != 2 {
				t.Fatalf("%d rows left, want the 2 the chat held", len(rows))
			}
			if _, err := d.chats.GetByDirectPeer(dcAliceOldID); !errors.Is(err, chat.ErrNotFound) {
				t.Fatalf("the deleted pairing still resolves to a chat: %v", err)
			}
		})
	}
}
