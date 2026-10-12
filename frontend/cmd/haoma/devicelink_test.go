package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"go.mau.fi/libsignal/keys/prekey"
	"go.mau.fi/libsignal/protocol"
	"go.mau.fi/libsignal/serialize"
	libsession "go.mau.fi/libsignal/session"
	"go.mau.fi/libsignal/util/optional"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/linkeddevice"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/pair"
	"haoma-frontend/internal/session"
	"haoma-frontend/internal/signal"
	"haoma-frontend/internal/store"
)

type linkStub struct {
	URL string

	onion string

	mintMu sync.Mutex
	mints  int

	livePeers []backendapi.Peer

	inviterPayload  []byte
	acceptedPayload chan []byte
	waitFeed        chan []byte

	sends chan backendapi.SendRequest

	inviteHandle string
	inviteWords  []string
	reach        chan backendapi.PairOnionProbe
	streamOpen   chan struct{}
	streamOnce   sync.Once

	acceptStatus int
	delMu        sync.Mutex
	deleted      []string
}

func (s *linkStub) deletedOnions() []string {
	s.delMu.Lock()
	defer s.delMu.Unlock()
	return append([]string(nil), s.deleted...)
}

func newLinkStub(t *testing.T, onion string) *linkStub {
	t.Helper()
	s := &linkStub{
		onion:           onion,
		acceptedPayload: make(chan []byte, 4),
		waitFeed:        make(chan []byte, 4),
		sends:           make(chan backendapi.SendRequest, 8),
		inviteHandle:    "handle-stub-1",
		inviteWords:     []string{"acid", "acorn", "acre", "acts", "afar", "affix", "aged"},
		reach:           make(chan backendapi.PairOnionProbe, 8),
		streamOpen:      make(chan struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /onion/mint", func(w http.ResponseWriter, _ *http.Request) {
		s.mintMu.Lock()
		s.mints++
		s.mintMu.Unlock()
		_ = json.NewEncoder(w).Encode(backendapi.MintedOnion{Address: s.onion, PrivateKey: "ZmFrZQ=="})
	})
	mux.HandleFunc("GET /peers", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(backendapi.PeersResponse{Peers: s.livePeers})
	})

	mux.HandleFunc("GET /peers/{id}", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(backendapi.Peer{ID: r.PathValue("id")})
	})
	mux.HandleFunc("POST /peers", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("POST /send", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req backendapi.SendRequest
		_ = json.Unmarshal(body, &req)
		select {
		case s.sends <- req:
		default:
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(backendapi.SendResponse{EnvelopeID: "env-link-1"})
	})
	mux.HandleFunc("POST /onion/del", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Address string `json:"address"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		s.delMu.Lock()
		s.deleted = append(s.deleted, req.Address)
		s.delMu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /pair/onion/accept", func(w http.ResponseWriter, r *http.Request) {
		if s.acceptStatus != 0 {
			_, _ = io.Copy(io.Discard, r.Body)
			http.Error(w, `{"error":"refused"}`, s.acceptStatus)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req backendapi.PairOnionAcceptRequest
		_ = json.Unmarshal(body, &req)
		select {
		case s.acceptedPayload <- req.Payload:
		default:
		}
		_ = json.NewEncoder(w).Encode(backendapi.PairOnionAcceptResponse{InviterPayload: s.inviterPayload})
	})
	mux.HandleFunc("POST /pair/onion/invite", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(backendapi.PairOnionInviteResponse{
			HandleID:  s.inviteHandle,
			Words:     s.inviteWords,
			ExpiresAt: time.Now().Add(10 * time.Minute).Unix(),
		})
	})

	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		s.streamOnce.Do(func() { close(s.streamOpen) })
		for {
			select {
			case <-r.Context().Done():
				return
			case obs := <-s.reach:
				body, err := json.Marshal(obs)
				if err != nil {
					return
				}
				if _, err := fmt.Fprintf(w, "event: pair.onion-probe\ndata: %s\n\n", body); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	})
	mux.HandleFunc("POST /pair/onion/wait/{handle}", func(w http.ResponseWriter, r *http.Request) {
		select {
		case payload := <-s.waitFeed:
			_ = json.NewEncoder(w).Encode(backendapi.PairOnionWaitResponse{JoinerPayload: payload})
		case <-r.Context().Done():
			w.WriteHeader(http.StatusGatewayTimeout)
		}
	})
	srv := httptest.NewServer(mux)
	s.URL = srv.URL
	t.Cleanup(srv.Close)
	return s
}

func linkDaemon(t *testing.T, stub *linkStub) (d *daemon, addr, certPath, token string) {
	t.Helper()
	d, addr, certPath, token = newTestDaemon(t, startHaomadStub(t, []string{"unused-onion"}, http.StatusCreated))
	d.backendClient = backendapi.New(stub.URL, "", nil)
	d.linkedDevices = linkeddevice.New(d.store)
	return d, addr, certPath, token
}

func (s *linkStub) mintCount() int {
	s.mintMu.Lock()
	defer s.mintMu.Unlock()
	return s.mints
}

func (s *linkStub) awaitReachStream(t *testing.T) {
	t.Helper()
	select {
	case <-s.streamOpen:
	case <-time.After(10 * time.Second):
		t.Fatal("the offering daemon never opened a reachability stream — nothing would ever tell it the rendezvous went live")
	}
}

func fingerprintOf(d *daemon) string {
	return d.signalState.IdentityKeyPair.PublicKey().Fingerprint()
}

func linkedMembers(t *testing.T, d *daemon) []string {
	t.Helper()
	members, err := d.linkedDevices.Members()
	if err != nil {
		t.Fatalf("read linked-device set: %v", err)
	}
	return members
}

func TestDeviceLink_FullHandshake_SiblingsWithoutCommingling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	offerStub := newLinkStub(t, "offer-onion")
	joinStub := newLinkStub(t, "join-onion")
	dOffer, offerAddr, offerCert, offerToken := linkDaemon(t, offerStub)
	dJoin, joinAddr, joinCert, joinToken := linkDaemon(t, joinStub)

	offerFingerprint := fingerprintOf(dOffer)
	joinFingerprintBefore := fingerprintOf(dJoin)
	joinRegIDBefore := dJoin.signalState.RegistrationID
	if offerFingerprint == joinFingerprintBefore {
		t.Fatal("fixture is degenerate: both daemons bootstrapped the same identity")
	}

	const handleID = "handle-link-1"
	offerInv, offerMine, err := pair.Build(dOffer.signalState, dOffer.stores, []string{offerStub.onion}, "", nil)
	if err != nil {
		t.Fatalf("build offering invite: %v", err)
	}
	offerInvBytes, err := offerInv.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	joinStub.inviterPayload = offerInvBytes
	if err := pair.SaveMyKeys(dOffer.store, handleID, offerMine, backendapi.MintedOnion{
		Address: offerStub.onion, PrivateKey: "ZmFrZQ==",
	}); err != nil {
		t.Fatalf("stash offering keys: %v", err)
	}

	go func() {
		select {
		case payload := <-joinStub.acceptedPayload:
			offerStub.waitFeed <- payload
		case <-ctx.Done():
		}
	}()

	go func() {
		select {
		case req := <-offerStub.sends:
			if !waitForDeviceLinkPending(dJoin, offerInv.PeerID) {
				return
			}
			processInboxEntry(ctx, dJoin, railEntry(offerInv.PeerID, "env-bundle-1", req.Payload))
		case <-ctx.Done():
		}
	}()

	offerConn := dialTest(t, ctx, offerAddr, offerCert, offerToken)

	go newSessionDispatcher(dOffer).runDeviceLinkWait(ctx, nil, handleID)

	conn := dialTest(t, ctx, joinAddr, joinCert, joinToken)
	req, _ := ipc.NewFrame(ipc.FrameDeviceLinkAccept, "req-link", ipc.DeviceLinkAcceptRequest{
		Words: []string{"one", "two", "three", "four", "five", "six", "seven"},
	})
	writeFrame(t, ctx, conn, req)

	resp := readUntil(t, ctx, conn, ipc.FrameDeviceLinkAccepted)
	var accepted ipc.DeviceLinkAcceptedResponse
	if err := json.Unmarshal(resp.Payload, &accepted); err != nil {
		t.Fatal(err)
	}

	if accepted.AdoptedFingerprint != offerFingerprint {
		t.Fatalf("reported adopted fingerprint = %q, want the offering device's %q", accepted.AdoptedFingerprint, offerFingerprint)
	}
	if got := fingerprintOf(dJoin); got != offerFingerprint {
		t.Fatalf("joining device fingerprint = %q, want %q — without this a peer cannot fold the two devices into one chat", got, offerFingerprint)
	}
	if accepted.PeerID != offerInv.PeerID {
		t.Fatalf("accepted peer id = %q, want the offering device's %q", accepted.PeerID, offerInv.PeerID)
	}

	if dJoin.signalState.RegistrationID != joinRegIDBefore {
		t.Fatalf("joining device registration id = %d, want its own %d — linking transfers identity only", dJoin.signalState.RegistrationID, joinRegIDBefore)
	}

	if got := linkedMembers(t, dJoin); len(got) != 1 || got[0] != offerInv.PeerID {
		t.Fatalf("joining device linked-device set = %v, want [%s]", got, offerInv.PeerID)
	}

	joinerPeerID := waitForOneLinkedDevice(t, dOffer)

	if chatExistsForPeer(dJoin, offerInv.PeerID) {
		t.Fatal("joining device created a chat for its sibling — a sibling is SELF, never a conversation")
	}
	if chatExistsForPeer(dOffer, joinerPeerID) {
		t.Fatal("offering device created a chat for its sibling")
	}
	assertNoChats(t, dJoin)
	assertNoChats(t, dOffer)
	assertNoContactRow(t, dJoin, offerInv.PeerID)
	assertNoContactRow(t, dOffer, joinerPeerID)

	readLinkedDeviceRow(t, ctx, conn, offerInv.PeerID)
	readLinkedDeviceRow(t, ctx, offerConn, joinerPeerID)
}

func readLinkedDeviceRow(t *testing.T, ctx context.Context, conn *websocket.Conn, peerID string) {
	t.Helper()
	for {
		f := readUntil(t, ctx, conn, ipc.FramePeerUpdated)
		var p ipc.PeerUpdatedPayload
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			t.Fatalf("decode peer.updated: %v", err)
		}
		if p.Peer.ID != peerID {
			continue
		}
		if !p.Peer.Sibling {
			t.Fatalf("peer.updated for the linked device %s says Sibling false; a client draws it as a contact", peerID)
		}
		return
	}
}

func waitForDeviceLinkPending(d *daemon, peerID string) bool {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if deviceLinkPending(d, peerID) {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

func waitForOneLinkedDevice(t *testing.T, d *daemon) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if members := linkedMembers(t, d); len(members) == 1 {
			return members[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("offering device did not record its sibling within the deadline; set = %v", linkedMembers(t, d))
	return ""
}

func assertNoChats(t *testing.T, d *daemon) {
	t.Helper()
	chats, err := d.chats.List()
	if err != nil {
		t.Fatalf("list chats: %v", err)
	}
	if len(chats) != 0 {
		t.Fatalf("linking created %d chat(s); it must create none", len(chats))
	}
}

func assertNoContactRow(t *testing.T, d *daemon, peerID string) {
	t.Helper()
	rec, err := d.peerMeta.Get(peerID)
	if err != nil {
		t.Fatalf("read peer meta for %q: %v", peerID, err)
	}
	if rec.Alias != "" || rec.Nick != "" || rec.Fingerprint != "" {
		t.Fatalf("linking wrote an address-book row for a sibling (alias=%q nick=%q fingerprint set=%t)",
			rec.Alias, rec.Nick, rec.Fingerprint != "")
	}
}

func TestDeviceLink_IdentityBundle_CarriesNoWireKindHintToHaomad(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	stub := newLinkStub(t, "offer-onion")
	d, _, _, _ := linkDaemon(t, stub)
	d.backendClient = backendapi.New(stub.URL, "", nil)

	const peerID = "00000000000000000000000000001111"
	preEstablishSession(t, d, peerID)

	if err := d.sendIdentityBundle(ctx, peerID); err != nil {
		t.Fatalf("sendIdentityBundle: %v", err)
	}
	select {
	case req := <-stub.sends:
		if req.Kind != "" {
			t.Fatalf("send carried wire-kind hint %q; the router must not learn this envelope is a device link", req.Kind)
		}
		if req.PresenceSource != "" {
			t.Fatalf("send carried presence source %q", req.PresenceSource)
		}
		if len(req.Payload) == 0 {
			t.Fatal("send carried an empty payload")
		}
	case <-ctx.Done():
		t.Fatal("no /send observed for the identity bundle")
	}
}

func TestDeviceLink_Offer_ReportsRendezvousReachability(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stub := newLinkStub(t, "offer-onion")
	stub.inviteHandle = "handle-reach-1"
	_, addr, certPath, token := linkDaemon(t, stub)

	conn := dialTest(t, ctx, addr, certPath, token)
	req, _ := ipc.NewFrame(ipc.FrameDeviceLinkOffer, "req-offer", ipc.DeviceLinkOfferRequest{})
	writeFrame(t, ctx, conn, req)

	startedFrame := readUntil(t, ctx, conn, ipc.FrameDeviceLinkStarted)
	var started ipc.DeviceLinkStartedResponse
	if err := json.Unmarshal(startedFrame.Payload, &started); err != nil {
		t.Fatal(err)
	}
	if started.HandleID != stub.inviteHandle {
		t.Fatalf("started handle = %q, want %q", started.HandleID, stub.inviteHandle)
	}
	stub.awaitReachStream(t)

	stub.reach <- backendapi.PairOnionProbe{HandleID: "handle-someone-else", Attempt: 1, Ready: true, At: 100}

	stub.reach <- backendapi.PairOnionProbe{
		HandleID: started.HandleID,
		Attempt:  1,
		Error:    `Post "http://offer-onion.onion:2928/pair": context deadline exceeded`,
		At:       101,
	}

	firstFrame := readUntil(t, ctx, conn, ipc.FrameDeviceLinkProbe)
	var first ipc.DeviceLinkProbePush
	if err := json.Unmarshal(firstFrame.Payload, &first); err != nil {
		t.Fatal(err)
	}
	if first.HandleID != started.HandleID {
		t.Fatalf("forwarded a report for %q; only this offer's own rendezvous may be reported (want %q)", first.HandleID, started.HandleID)
	}
	if first.Ready {
		t.Fatal("a failed attempt was reported ready — the other device would dial a rendezvous that is not up")
	}
	if first.Attempt != 1 {
		t.Errorf("attempt = %d, want 1", first.Attempt)
	}
	if first.Error != probeUnreachable {
		t.Errorf("error token = %q, want the stable %q", first.Error, probeUnreachable)
	}
	if body := string(firstFrame.Payload); strings.Contains(body, "onion") || strings.Contains(body, "http") {
		t.Fatalf("the prober's raw error crossed to the client, address and all: %s", body)
	}

	stub.reach <- backendapi.PairOnionProbe{HandleID: started.HandleID, Attempt: 3, Ready: true, At: 102}

	readyFrame := readUntil(t, ctx, conn, ipc.FrameDeviceLinkProbe)
	var ready ipc.DeviceLinkProbePush
	if err := json.Unmarshal(readyFrame.Payload, &ready); err != nil {
		t.Fatal(err)
	}
	if !ready.Ready {
		t.Fatalf("second report = %+v, want the ready one", ready)
	}
	if ready.Attempt != 3 || ready.At != 102 {
		t.Errorf("ready report = attempt %d at %d, want attempt 3 at 102", ready.Attempt, ready.At)
	}
	if ready.Error != "" {
		t.Errorf("verified readiness carried token %q; it must be empty", ready.Error)
	}
	if readyFrame.ID != "" {
		t.Errorf("reachability rides as a push; correlation id = %q, want empty", readyFrame.ID)
	}
}

func TestDeviceLink_ReachToken_KeepsGiveUpAndDropsTheRest(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"no error at all", "", ""},
		{"the prober gave up", probeGaveUp, probeGaveUp},
		{"a dial that named the onion", `Post "http://abc.onion:2928/pair": context deadline exceeded`, probeUnreachable},
		{"anything else", "connection refused", probeUnreachable},
	}
	for _, c := range cases {
		if got := reachToken(c.raw); got != c.want {
			t.Errorf("%s: reachToken(%q) = %q, want %q", c.name, c.raw, got, c.want)
		}
	}
}

func TestDeviceLink_AcceptRefused_WhenDeviceHoldsAChat(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	stub := newLinkStub(t, "join-onion")
	d, addr, certPath, token := linkDaemon(t, stub)

	const peerID = "00000000000000000000000000002222"
	preEstablishSession(t, d, peerID)
	fpBefore := fingerprintOf(d)

	conn := dialTest(t, ctx, addr, certPath, token)
	req, _ := ipc.NewFrame(ipc.FrameDeviceLinkAccept, "req-1", ipc.DeviceLinkAcceptRequest{
		Words: []string{"a", "b", "c", "d", "e", "f", "g"},
	})
	writeFrame(t, ctx, conn, req)

	f := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	if err := json.Unmarshal(f.Payload, &ep); err != nil {
		t.Fatal(err)
	}
	if ep.Code != reasonDeviceNotEmpty {
		t.Fatalf("refusal code = %q, want the stable token %q", ep.Code, reasonDeviceNotEmpty)
	}

	if got := fingerprintOf(d); got != fpBefore {
		t.Fatalf("identity changed on a refused accept (%q → %q)", fpBefore, got)
	}
	if got := linkedMembers(t, d); len(got) != 0 {
		t.Fatalf("linked-device set = %v after a refused accept, want empty", got)
	}
	if !chatExistsForPeer(d, peerID) {
		t.Fatal("the pre-existing chat is gone: a refusal must not wipe anything")
	}
}

func TestDeviceLink_AcceptRefused_WhenBackendHoldsAContact(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	stub := newLinkStub(t, "join-onion")
	stub.livePeers = []backendapi.Peer{{ID: "00000000000000000000000000003333"}}
	d, addr, certPath, token := linkDaemon(t, stub)

	conn := dialTest(t, ctx, addr, certPath, token)
	req, _ := ipc.NewFrame(ipc.FrameDeviceLinkAccept, "req-1", ipc.DeviceLinkAcceptRequest{
		Words: []string{"a", "b", "c", "d", "e", "f", "g"},
	})
	writeFrame(t, ctx, conn, req)

	f := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	if err := json.Unmarshal(f.Payload, &ep); err != nil {
		t.Fatal(err)
	}
	if ep.Code != reasonDeviceNotEmpty {
		t.Fatalf("refusal code = %q, want %q", ep.Code, reasonDeviceNotEmpty)
	}
	if got := linkedMembers(t, d); len(got) != 0 {
		t.Fatalf("linked-device set = %v after a refused accept, want empty", got)
	}
}

func TestDeviceLink_AcceptRefused_WhenAlreadyLinked(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	stub := newLinkStub(t, "join-onion")
	d, addr, certPath, token := linkDaemon(t, stub)
	const existing = "00000000000000000000000000004444"
	if _, err := d.linkedDevices.Link(existing, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}

	conn := dialTest(t, ctx, addr, certPath, token)
	req, _ := ipc.NewFrame(ipc.FrameDeviceLinkAccept, "req-1", ipc.DeviceLinkAcceptRequest{
		Words: []string{"a", "b", "c", "d", "e", "f", "g"},
	})
	writeFrame(t, ctx, conn, req)

	f := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	if err := json.Unmarshal(f.Payload, &ep); err != nil {
		t.Fatal(err)
	}
	if ep.Code != reasonAlreadyLinked {
		t.Fatalf("refusal code = %q, want %q", ep.Code, reasonAlreadyLinked)
	}
	if got := linkedMembers(t, d); len(got) != 1 || got[0] != existing {
		t.Fatalf("linked-device set = %v, want the pre-existing [%s] untouched", got, existing)
	}
}

func TestDeviceLink_OfferRefused_WhenAlreadyLinked(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	stub := newLinkStub(t, "offer-onion")
	d, addr, certPath, token := linkDaemon(t, stub)
	const existing = "00000000000000000000000000005555"
	if _, err := d.linkedDevices.Link(existing, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	minted := stub.mintCount()

	conn := dialTest(t, ctx, addr, certPath, token)
	req, _ := ipc.NewFrame(ipc.FrameDeviceLinkOffer, "req-1", ipc.DeviceLinkOfferRequest{})
	writeFrame(t, ctx, conn, req)

	f := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	if err := json.Unmarshal(f.Payload, &ep); err != nil {
		t.Fatal(err)
	}
	if ep.Code != reasonAlreadyLinked {
		t.Fatalf("refusal code = %q, want %q", ep.Code, reasonAlreadyLinked)
	}
	if got := stub.mintCount(); got != minted {
		t.Errorf("onions minted = %d, want %d — a refused offer must mint nothing", got, minted)
	}
	if got := linkedMembers(t, d); len(got) != 1 || got[0] != existing {
		t.Fatalf("linked-device set = %v, want the pre-existing [%s] untouched", got, existing)
	}
}

func TestDeviceLink_List_ReturnsLiveMembersAndOmitsUnlinked(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	stub := newLinkStub(t, "onion")
	d, addr, certPath, token := linkDaemon(t, stub)

	const (
		live    = "0000000000000000000000000000aaa1"
		dropped = "0000000000000000000000000000aaa2"
	)
	if _, err := d.linkedDevices.Link(live, 1_700_000_100); err != nil {
		t.Fatal(err)
	}
	if _, err := d.linkedDevices.Link(dropped, 1_700_000_200); err != nil {
		t.Fatal(err)
	}
	if _, err := d.linkedDevices.Unlink(dropped, 1_700_000_300); err != nil {
		t.Fatal(err)
	}

	conn := dialTest(t, ctx, addr, certPath, token)
	req, _ := ipc.NewFrame(ipc.FrameDeviceLinksList, "req-list", ipc.DeviceLinksListRequest{})
	writeFrame(t, ctx, conn, req)

	f := readUntil(t, ctx, conn, ipc.FrameDeviceLinksListed)
	if f.ID != "req-list" {
		t.Errorf("correlation id = %q, want req-list", f.ID)
	}
	var listed ipc.DeviceLinksListedResponse
	if err := json.Unmarshal(f.Payload, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Devices) != 1 {
		t.Fatalf("listed %d device(s), want exactly the one live member: %+v", len(listed.Devices), listed.Devices)
	}
	if listed.Devices[0].PeerID != live {
		t.Fatalf("listed peer id = %q, want %q", listed.Devices[0].PeerID, live)
	}
	if listed.Devices[0].LinkedAt != 1_700_000_100 {
		t.Fatalf("linked_at = %d, want the recorded 1700000100", listed.Devices[0].LinkedAt)
	}
}

func TestDeviceLink_List_EmptyOnAnUnlinkedDevice(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	stub := newLinkStub(t, "onion")
	_, addr, certPath, token := linkDaemon(t, stub)

	conn := dialTest(t, ctx, addr, certPath, token)
	req, _ := ipc.NewFrame(ipc.FrameDeviceLinksList, "req-list", ipc.DeviceLinksListRequest{})
	writeFrame(t, ctx, conn, req)

	f := readUntil(t, ctx, conn, ipc.FrameDeviceLinksListed)
	var listed ipc.DeviceLinksListedResponse
	if err := json.Unmarshal(f.Payload, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Devices) != 0 {
		t.Fatalf("listed %d device(s) on an unlinked device, want none", len(listed.Devices))
	}
}

func TestDeviceLink_UnsolicitedBundle_IsDroppedAndLeavesNoRow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)

	const peerID = "00000000000000000000000000005555"
	fpBefore := fingerprintOf(d)
	payload := hostileIdentityBundle(t, d, "")

	processInboxEntry(ctx, d, railEntry(peerID, "env-hostile-1", payload))

	if got := fingerprintOf(d); got != fpBefore {
		t.Fatalf("identity changed on an unsolicited bundle (%q → %q) — only a pending link may adopt", fpBefore, got)
	}
	chatID := chatIDForPeer(t, d, peerID)
	rows, err := d.events.List(chatID, 0, 100)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("an identity bundle produced %d timeline row(s); it must produce none — a persisted row would put an identity private key in the event log", len(rows))
	}
}

func hostileIdentityBundle(t *testing.T, d *daemon, selfNick string) []byte {
	t.Helper()

	selfAddr := protocol.NewSignalAddress("device-under-test", session.DeviceID)
	selfOPK := d.signalState.OneTimePreKeys[0]
	selfSPKSig := d.signalState.SignedPreKey.Signature()
	selfBundle := prekey.NewBundle(
		d.signalState.RegistrationID,
		session.DeviceID,
		optional.NewOptionalUint32(selfOPK.ID().Value),
		d.signalState.SignedPreKey.ID(),
		selfOPK.KeyPair().PublicKey(),
		d.signalState.SignedPreKey.KeyPair().PublicKey(),
		selfSPKSig,
		d.signalState.IdentityKeyPair.PublicKey(),
	)

	remoteDir := t.TempDir()
	remoteStore, err := store.Unlock(remoteDir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = remoteStore.Lock() })
	remoteState, _, err := signal.LoadOrBootstrap(remoteStore, 3)
	if err != nil {
		t.Fatal(err)
	}
	remoteStores := signal.NewStores(remoteStore, remoteState)
	ser := serialize.NewJSONSerializer()
	builder := libsession.NewBuilder(remoteStores, remoteStores, remoteStores, remoteStores, selfAddr, ser)
	if err := builder.ProcessBundle(t.Context(), selfBundle); err != nil {
		t.Fatalf("remote ProcessBundle: %v", err)
	}

	privArr := remoteState.IdentityKeyPair.PrivateKey().Serialize()
	msgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	wrapper, err := msg.BuildDeviceLinkIdentity(1, time.Now().Unix(), msgID,
		remoteState.IdentityKeyPair.PublicKey().Serialize(), privArr[:], selfNick, 0)
	if err != nil {
		t.Fatalf("build identity bundle: %v", err)
	}
	plain, err := msg.Marshal(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := session.New(remoteStores).Encrypt(t.Context(), "device-under-test", plain)
	if err != nil {
		t.Fatalf("remote encrypt: %v", err)
	}
	return blob
}

func TestDeviceLink_CompletedBroadcastSurvivesABrokenSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)

	other := dialTest(t, ctx, addr, certPath, token)

	sendFailed := false
	announceDeviceLinkCompleted(d, func(ipc.Frame) error {
		sendFailed = true
		return fmt.Errorf("write: connection closed")
	}, "handle-link-1", "peer-my-other-device")

	if !sendFailed {
		t.Fatal("the fixture never sent the reply, so nothing was broken to survive")
	}
	f := readUntil(t, ctx, other, ipc.FrameInheritedContactsChanged)
	var p ipc.InheritedContactsChangedPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		t.Fatalf("decode pushed contacts: %v", err)
	}
}

func TestDeviceLink_CompletedBroadcastGoesOutWithNoSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	other := dialTest(t, ctx, addr, certPath, token)

	announceDeviceLinkCompleted(d, nil, "handle-link-1", "peer-my-other-device")

	readUntil(t, ctx, other, ipc.FrameInheritedContactsChanged)
}
