package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"go.mau.fi/libsignal/keys/prekey"
	"go.mau.fi/libsignal/protocol"
	"go.mau.fi/libsignal/serialize"
	libsession "go.mau.fi/libsignal/session"
	"go.mau.fi/libsignal/util/optional"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/pair"
	"haoma-frontend/internal/peerstate"
	"haoma-frontend/internal/session"
	"haoma-frontend/internal/signal"
	"haoma-frontend/internal/store"
)

func init() {
	store.DefaultKDFParams = store.KDFParams{
		Time: 1, Memory: 8 * 1024, Threads: 2, KeyLen: 32, SaltLen: 16,
	}
}

type haomadStub struct {
	URL      string
	Calls    int
	LastBody []byte

	SendCalls      int
	LastSendBody   []byte
	LastSendReq    backendapi.SendRequest
	SendDone       chan struct{}
	sendStatus     int
	sendEnvelopeID string

	PeerActionCalls   int
	LastPeerActionID  string
	LastPeerActionReq string

	Peer backendapi.Peer

	close         func()
	mintedAddress string
	mintedPrivKey string
}

func (s *haomadStub) WaitForSend(t *testing.T, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for i := 0; i < n; i++ {
		select {
		case <-s.SendDone:
		case <-deadline:
			t.Fatalf("WaitForSend: timed out waiting for %d /send calls (got %d)", n, i)
		}
	}
}

func startHaomadStub(t *testing.T, identity []string, peerStatus int) *haomadStub {
	t.Helper()

	mintedAddr := "stubmintAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA1"
	if len(identity) > 0 {
		mintedAddr = identity[0]
	}
	stub := &haomadStub{
		mintedAddress:  mintedAddr,
		mintedPrivKey:  "ZmFrZS1iYXNlNjQta2V5",
		sendStatus:     http.StatusAccepted,
		sendEnvelopeID: "env-stub-0001",
		SendDone:       make(chan struct{}, 16),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /onion/mint", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(backendapi.MintedOnion{
			Address:    stub.mintedAddress,
			PrivateKey: stub.mintedPrivKey,
		})
	})
	mux.HandleFunc("POST /peers", func(w http.ResponseWriter, r *http.Request) {
		stub.Calls++
		stub.LastBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(peerStatus)
	})
	mux.HandleFunc("GET /peers/{id}", func(w http.ResponseWriter, r *http.Request) {
		p := stub.Peer
		if p.ID == "" {
			p.ID = r.PathValue("id")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(p)
	})
	mux.HandleFunc("POST /peers/{id}/action", func(w http.ResponseWriter, r *http.Request) {
		stub.PeerActionCalls++
		stub.LastPeerActionID = r.PathValue("id")
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Action string `json:"action"`
		}
		_ = json.Unmarshal(body, &req)
		stub.LastPeerActionReq = req.Action

		if req.Action == "retire" {
			if stub.Peer.ID == "" {
				stub.Peer.ID = r.PathValue("id")
			}
			stub.Peer.RetiredAt = 1_750_000_000
			stub.Peer.KnownAddresses = nil
		}
		p := stub.Peer
		if p.ID == "" {
			p.ID = r.PathValue("id")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(p)
	})
	mux.HandleFunc("POST /send", func(w http.ResponseWriter, r *http.Request) {
		stub.SendCalls++
		stub.LastSendBody, _ = io.ReadAll(r.Body)
		_ = json.Unmarshal(stub.LastSendBody, &stub.LastSendReq)

		select {
		case stub.SendDone <- struct{}{}:
		default:
		}
		if stub.sendStatus != http.StatusAccepted {
			w.WriteHeader(stub.sendStatus)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(backendapi.SendResponse{
			EnvelopeID: stub.sendEnvelopeID,
		})
	})
	srv := httptest.NewServer(mux)
	stub.URL = srv.URL
	stub.close = srv.Close
	t.Cleanup(srv.Close)
	return stub
}

func newTestDaemon(t *testing.T, stub *haomadStub) (d *daemon, addr, certPEM, token string) {
	t.Helper()
	dir := t.TempDir()

	st, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Lock() })

	sigState, _, err := signal.LoadOrBootstrap(st, 4)
	if err != nil {
		t.Fatal(err)
	}

	stores := signal.NewStores(st, sigState)

	tlsCfg, err := ipc.LoadOrCreateTLS(dir)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := ipc.LoadOrCreateToken(dir)
	if err != nil {
		t.Fatal(err)
	}

	srv := ipc.NewServer(tok)

	bus := events.NewBus()
	d = &daemon{
		dataDir:       dir,
		store:         st,
		signalState:   sigState,
		stores:        stores,
		cipher:        session.New(stores),
		peerSeq:       peerstate.New(st),
		peerMeta:      peerstate.NewMeta(st),
		chats:         chat.NewStore(st),
		events:        events.New(st, bus, nil),
		eventBus:      bus,
		backendClient: backendapi.New(stub.URL, "", nil),
		ipcSrv:        srv,
	}
	srv.OnSession = newSessionDispatcher(d).run

	pushCtx, pushCancel := context.WithCancel(context.Background())
	go pushTimelineEvents(pushCtx, bus, srv)
	t.Cleanup(pushCancel)

	httpSrv := &http.Server{Handler: srv.Handler(), TLSConfig: tlsCfg}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		tlsLn := tls.NewListener(ln, tlsCfg)
		_ = httpSrv.Serve(tlsLn)
	}()
	t.Cleanup(func() {
		shutdownCtx, c := context.WithTimeout(context.Background(), time.Second)
		defer c()
		_ = httpSrv.Shutdown(shutdownCtx)
	})
	return d, ln.Addr().String(), ipc.CertPath(dir), tok
}

func dialTest(t *testing.T, ctx context.Context, addr, certPath, token string) *websocket.Conn {
	t.Helper()
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatal("AppendCertsFromPEM failed")
	}
	tlsCfg := &tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS13}
	httpClient := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}}
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+token)
	conn, _, err := websocket.Dial(ctx, "wss://"+addr+"/ws", &websocket.DialOptions{
		HTTPClient: httpClient,
		HTTPHeader: hdr,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "") })

	hello, _ := ipc.NewFrame(ipc.FrameHello, "h", ipc.HelloPayload{ClientName: "test"})
	b, _ := ipc.Encode(hello)
	_ = conn.Write(ctx, websocket.MessageText, b)
	_, _, err = conn.Read(ctx)
	if err != nil {
		t.Fatalf("welcome: %v", err)
	}
	return conn
}

func writeFrame(t *testing.T, ctx context.Context, conn *websocket.Conn, f ipc.Frame) {
	t.Helper()
	b, _ := ipc.Encode(f)
	if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
}

func readNext(t *testing.T, ctx context.Context, conn *websocket.Conn) ipc.Frame {
	t.Helper()
	for {
		readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, data, err := conn.Read(readCtx)
		cancel()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		f, err := ipc.Decode(data)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if f.Type == ipc.FramePing || f.Type == ipc.FramePong {
			continue
		}
		if f.Type == ipc.FrameError {
			var ep ipc.ErrorPayload
			_ = json.Unmarshal(f.Payload, &ep)
			t.Fatalf("server returned error: code=%s message=%s", ep.Code, ep.Message)
		}
		return f
	}
}

func readUntil(t *testing.T, ctx context.Context, conn *websocket.Conn, wantType ipc.FrameType) ipc.Frame {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		readCtx, cancel := context.WithTimeout(ctx, time.Until(deadline))
		_, data, err := conn.Read(readCtx)
		cancel()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		f, err := ipc.Decode(data)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if f.Type == wantType {
			return f
		}
		if f.Type == ipc.FramePing || f.Type == ipc.FramePong {
			continue
		}
		if f.Type == ipc.FrameError && wantType != ipc.FrameError {
			var ep ipc.ErrorPayload
			_ = json.Unmarshal(f.Payload, &ep)
			t.Fatalf("server returned error instead of %s: code=%s message=%s", wantType, ep.Code, ep.Message)
		}
	}
	t.Fatalf("did not see frame %s within deadline", wantType)
	return ipc.Frame{}
}

func TestDispatch_InviteCreate_ReturnsValidInvite(t *testing.T) {

	stub := startHaomadStub(t, []string{"our-onion-1"}, http.StatusCreated)
	_, addr, certPath, token := newTestDaemon(t, stub)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn := dialTest(t, ctx, addr, certPath, token)

	req, _ := ipc.NewFrame(ipc.FrameInviteCreate, "req-1", ipc.InviteCreateRequest{Nick: "alice"})
	writeFrame(t, ctx, conn, req)

	resp := readUntil(t, ctx, conn, ipc.FrameInviteCreated)
	if resp.ID != "req-1" {
		t.Errorf("correlation id = %q, want req-1", resp.ID)
	}
	var payload ipc.InviteCreatedResponse
	if err := json.Unmarshal(resp.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	inv, err := pair.Parse([]byte(payload.InviteJSON))
	if err != nil {
		t.Fatalf("returned invite doesn't Parse: %v", err)
	}
	if inv.Frontend.Nick != "alice" {
		t.Errorf("invite nickname = %q, want alice", inv.Frontend.Nick)
	}
	if len(inv.Addresses) != 1 || inv.Addresses[0] != "our-onion-1" {
		t.Errorf("invite addresses = %v, want [our-onion-1]", inv.Addresses)
	}
}

func TestDispatch_InviteCreate_BackendDown_ReturnsError(t *testing.T) {

	stub := startHaomadStub(t, []string{"our-onion-1"}, http.StatusCreated)
	stub.mintedAddress = ""
	stub.mintedPrivKey = ""
	_, addr, certPath, token := newTestDaemon(t, stub)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn := dialTest(t, ctx, addr, certPath, token)

	req, _ := ipc.NewFrame(ipc.FrameInviteCreate, "req-1", ipc.InviteCreateRequest{})
	writeFrame(t, ctx, conn, req)

	resp := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	_ = json.Unmarshal(resp.Payload, &ep)
	if ep.Code != "not_ready" {
		t.Errorf("err code = %q, want not_ready (got message: %s)", ep.Code, ep.Message)
	}
}

func TestDispatch_InviteAccept_HappyPath(t *testing.T) {

	stubA := startHaomadStub(t, []string{"alice-onion"}, http.StatusCreated)
	dA, addrA, certA, tokenA := newTestDaemon(t, stubA)
	_ = dA

	stubB := startHaomadStub(t, []string{"bob-onion"}, http.StatusCreated)
	_, addrB, certB, tokenB := newTestDaemon(t, stubB)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	connA := dialTest(t, ctx, addrA, certA, tokenA)
	writeFrame(t, ctx, connA, mustFrame(ipc.FrameInviteCreate, "a1", ipc.InviteCreateRequest{Nick: "alice"}))
	created := readUntil(t, ctx, connA, ipc.FrameInviteCreated)
	var cr ipc.InviteCreatedResponse
	_ = json.Unmarshal(created.Payload, &cr)

	connB := dialTest(t, ctx, addrB, certB, tokenB)
	writeFrame(t, ctx, connB, mustFrame(ipc.FrameInviteCreate, "b0", ipc.InviteCreateRequest{Nick: "bob"}))
	_ = readUntil(t, ctx, connB, ipc.FrameInviteCreated)
	writeFrame(t, ctx, connB, mustFrame(ipc.FrameInviteAccept, "b1", ipc.InviteAcceptRequest(cr)))
	accepted := readUntil(t, ctx, connB, ipc.FrameInviteAccepted)
	if accepted.ID != "b1" {
		t.Errorf("correlation id = %q, want b1", accepted.ID)
	}
	var ac ipc.InviteAcceptedResponse
	if err := json.Unmarshal(accepted.Payload, &ac); err != nil {
		t.Fatal(err)
	}
	if ac.PeerID == "" {
		t.Error("peer_id empty in accepted response")
	}
	if ac.Nick != "alice" {
		t.Errorf("nick = %q, want alice", ac.Nick)
	}
	if ac.IdentityFingerprint == "" {
		t.Error("identity_fingerprint empty — operator can't voice-verify without it")
	}

	if stubB.Calls != 1 {
		t.Errorf("Bob's haomad saw %d POST /peers calls, want 1", stubB.Calls)
	}
}

func TestDispatch_InviteAccept_BadJSON_ReturnsError(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	_, addr, certPath, token := newTestDaemon(t, stub)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn := dialTest(t, ctx, addr, certPath, token)
	writeFrame(t, ctx, conn, mustFrame(ipc.FrameInviteAccept, "x", ipc.InviteAcceptRequest{
		InviteJSON: "this is not an invite",
	}))
	resp := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	_ = json.Unmarshal(resp.Payload, &ep)
	if ep.Code != "bad_invite" {
		t.Errorf("code = %q, want bad_invite (msg: %s)", ep.Code, ep.Message)
	}
}

func TestDispatch_InviteAccept_EmptyJSON_ReturnsError(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	_, addr, certPath, token := newTestDaemon(t, stub)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn := dialTest(t, ctx, addr, certPath, token)
	writeFrame(t, ctx, conn, mustFrame(ipc.FrameInviteAccept, "x", ipc.InviteAcceptRequest{InviteJSON: ""}))
	resp := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	_ = json.Unmarshal(resp.Payload, &ep)
	if ep.Code != "bad_request" {
		t.Errorf("code = %q, want bad_request", ep.Code)
	}
}

func TestDispatch_UnknownFrameType_ReturnsUnsupportedError(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	_, addr, certPath, token := newTestDaemon(t, stub)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn := dialTest(t, ctx, addr, certPath, token)
	writeFrame(t, ctx, conn, mustFrame("fabricated_type", "x", nil))
	resp := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	_ = json.Unmarshal(resp.Payload, &ep)
	if ep.Code != "unsupported_frame" {
		t.Errorf("code = %q, want unsupported_frame", ep.Code)
	}
	if !strings.Contains(ep.Message, "fabricated_type") {
		t.Errorf("message should name the offending type: %s", ep.Message)
	}
}

func TestDispatch_Subscribe_RoundTrip(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	_, addr, certPath, token := newTestDaemon(t, stub)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := dialTest(t, ctx, addr, certPath, token)
	subF, err := ipc.NewFrame(ipc.FrameSubscribe, "sub-1", ipc.SubscribeRequest{
		Topics: []string{" msg. ", "", "delivery."},
	})
	if err != nil {
		t.Fatal(err)
	}
	writeFrame(t, ctx, conn, subF)
	resp := readUntil(t, ctx, conn, ipc.FrameSubscribed)
	if resp.ID != "sub-1" {
		t.Errorf("correlation id = %q, want sub-1", resp.ID)
	}
	var ack ipc.SubscribedResponse
	if err := json.Unmarshal(resp.Payload, &ack); err != nil {
		t.Fatal(err)
	}
	if len(ack.Topics) != 2 || ack.Topics[0] != "msg." || ack.Topics[1] != "delivery." {
		t.Errorf("ack topics = %v, want [msg. delivery.]", ack.Topics)
	}
}

func TestDispatch_Subscribe_FiltersBroadcast(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := dialTest(t, ctx, addr, certPath, token)

	subF, _ := ipc.NewFrame(ipc.FrameSubscribe, "sub-1", ipc.SubscribeRequest{Topics: []string{"msg."}})
	writeFrame(t, ctx, conn, subF)
	_ = readUntil(t, ctx, conn, ipc.FrameSubscribed)

	pushF, _ := ipc.NewFrame(ipc.FrameDeliveryStatus, "", ipc.DeliveryStatusPayload{
		EnvelopeID: "x", State: "sent",
	})
	d.ipcSrv.Broadcast(pushF)

	timelineF, _ := ipc.NewFrame(ipc.FrameTimelineEvent, "", ipc.TimelineEventPayload{
		Event: json.RawMessage(`{}`),
	})
	d.ipcSrv.Broadcast(timelineF)

	got := readUntil(t, ctx, conn, ipc.FrameTimelineEvent)
	if got.Type != ipc.FrameTimelineEvent {
		t.Errorf("got %q, want %q", got.Type, ipc.FrameTimelineEvent)
	}

}

func preEstablishSession(t *testing.T, d *daemon, peerID string) {
	t.Helper()
	remoteState, err := signal.Bootstrap(3)
	if err != nil {
		t.Fatal(err)
	}
	processRemoteBundle(t, d, peerID, remoteState)

	if d.chats != nil {
		if _, err := d.chats.CreateDirect(peerID); err != nil {
			t.Fatalf("preEstablishSession: create direct chat: %v", err)
		}
	}
}

func chatIDForPeer(t *testing.T, d *daemon, peerID string) chat.ChatID {
	t.Helper()
	dc, err := d.chats.GetByDirectPeer(peerID)
	if err != nil {
		t.Fatalf("chatIDForPeer(%q): %v", peerID, err)
	}
	return dc.ID
}

func processRemoteBundle(t *testing.T, d *daemon, peerID string, remote *signal.State) {
	t.Helper()
	addr := protocol.NewSignalAddress(peerID, session.DeviceID)
	opk := remote.OneTimePreKeys[0]
	if opk.ID().IsEmpty {
		t.Fatal("remote state has no OPK")
	}
	spkSig := remote.SignedPreKey.Signature()
	bundle := prekey.NewBundle(
		remote.RegistrationID,
		session.DeviceID,
		optional.NewOptionalUint32(opk.ID().Value),
		remote.SignedPreKey.ID(),
		opk.KeyPair().PublicKey(),
		remote.SignedPreKey.KeyPair().PublicKey(),
		spkSig,
		remote.IdentityKeyPair.PublicKey(),
	)
	ser := serialize.NewJSONSerializer()
	builder := libsession.NewBuilder(d.stores, d.stores, d.stores, d.stores, addr, ser)
	if err := builder.ProcessBundle(context.Background(), bundle); err != nil {
		t.Fatalf("ProcessBundle: %v", err)
	}
}

func TestDispatch_SendText_HappyPath(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000000a"
	preEstablishSession(t, d, peerID)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendText, "s1", ipc.SendTextRequest{
		PeerID: peerID, Text: "hello bob",
	}))

	resp := readUntil(t, ctx, conn, ipc.FrameTextSent)
	if resp.ID != "s1" {
		t.Errorf("correlation id = %q, want s1", resp.ID)
	}
	var p ipc.SendTextResponse
	if err := json.Unmarshal(resp.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.EnvelopeID != "env-stub-0001" {
		t.Errorf("envelope id = %q, want env-stub-0001", p.EnvelopeID)
	}
	if p.SenderSeq != 1 {
		t.Errorf("sender_seq = %d, want 1 (first send)", p.SenderSeq)
	}
	if len(p.MsgID) != 32 {
		t.Errorf("msg_id = %q (len %d), want 32-char hex", p.MsgID, len(p.MsgID))
	}
	if stub.SendCalls != 1 {
		t.Errorf("haomad saw %d POST /send calls, want 1", stub.SendCalls)
	}
	if stub.LastSendReq.PeerID != peerID {
		t.Errorf("haomad got peer_id = %q, want %q", stub.LastSendReq.PeerID, peerID)
	}
	if len(stub.LastSendReq.Payload) == 0 {
		t.Error("haomad got empty payload")
	}

	if string(stub.LastSendReq.Payload) == "hello bob" {
		t.Error("payload looks like plaintext — encryption didn't run")
	}

	ev, err := d.events.GetByMsgID(p.MsgID)
	if err != nil {
		t.Fatalf("GetByMsgID after send: %v", err)
	}
	if ev.Direction != events.DirOut || ev.ChatID != chatIDForPeer(t, d, peerID) || ev.EnvelopeID != p.EnvelopeID {
		t.Errorf("persisted event drift: %+v", ev)
	}
}

func TestDispatch_SendText_PiggybacksPresenceFlag(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000000c"
	preEstablishSession(t, d, peerID)

	state := "away"
	d.presenceOverride.Store(&state)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)
	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendText, "p1", ipc.SendTextRequest{
		PeerID: peerID, Text: "hi",
	}))
	readUntil(t, ctx, conn, ipc.FrameTextSent)

	if got := stub.LastSendReq.PresenceSource; got != backendapi.PresenceSourceHaoma {
		t.Errorf("PresenceSource = %q, want %q", got, backendapi.PresenceSourceHaoma)
	}
}

func TestDispatch_SendText_SecondSendIncrementsSeq(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000000a"
	preEstablishSession(t, d, peerID)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	for i := 1; i <= 3; i++ {
		writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendText, "s", ipc.SendTextRequest{
			PeerID: peerID, Text: "ping",
		}))
		resp := readUntil(t, ctx, conn, ipc.FrameTextSent)
		var p ipc.SendTextResponse
		_ = json.Unmarshal(resp.Payload, &p)
		if p.SenderSeq != uint64(i) {
			t.Errorf("send #%d sender_seq = %d, want %d", i, p.SenderSeq, i)
		}
	}
}

func TestDispatch_SendText_NoPeerID_BadRequest(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	_, addr, certPath, token := newTestDaemon(t, stub)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendText, "s", ipc.SendTextRequest{Text: "hi"}))
	resp := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	_ = json.Unmarshal(resp.Payload, &ep)
	if ep.Code != "bad_request" {
		t.Errorf("code = %q, want bad_request", ep.Code)
	}
	if stub.SendCalls != 0 {
		t.Errorf("haomad saw %d /send calls, want 0", stub.SendCalls)
	}
}

func TestDispatch_SendText_NoText_BadRequest(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	_, addr, certPath, token := newTestDaemon(t, stub)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendText, "s", ipc.SendTextRequest{
		PeerID: "0000000000000000000000000000000a",
	}))
	resp := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	_ = json.Unmarshal(resp.Payload, &ep)
	if ep.Code != "bad_request" {
		t.Errorf("code = %q, want bad_request", ep.Code)
	}
}

func TestDispatch_SendText_NoSession_EncryptFails(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000000a"

	if _, err := d.chats.CreateDirect(peerID); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendText, "s", ipc.SendTextRequest{
		PeerID: peerID,
		Text:   "hi",
	}))
	resp := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	_ = json.Unmarshal(resp.Payload, &ep)
	if ep.Code != "encrypt_failed" {
		t.Errorf("code = %q, want encrypt_failed", ep.Code)
	}
	if !strings.Contains(ep.Message, "no session") {
		t.Errorf("err message lacks 'no session' detail: %q", ep.Message)
	}
	if stub.SendCalls != 0 {
		t.Errorf("haomad saw %d /send calls, want 0", stub.SendCalls)
	}
}

func TestDispatch_SendText_BackendErrors_ReturnsBackendSendError(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	stub.sendStatus = http.StatusInternalServerError
	d, addr, certPath, token := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000000a"
	preEstablishSession(t, d, peerID)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendText, "s", ipc.SendTextRequest{
		PeerID: peerID, Text: "hi",
	}))
	resp := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	_ = json.Unmarshal(resp.Payload, &ep)
	if ep.Code != "backend_send" {
		t.Errorf("code = %q, want backend_send", ep.Code)
	}
}

func TestDispatch_InviteCreate_StashesMyKeysForAcceptPath(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameInviteCreate, "i1", ipc.InviteCreateRequest{Nick: "alice"}))
	resp := readUntil(t, ctx, conn, ipc.FrameInviteCreated)
	var p ipc.InviteCreatedResponse
	_ = json.Unmarshal(resp.Payload, &p)
	inv, err := pair.Parse([]byte(p.InviteJSON))
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := pair.LoadMyKeys(d.store, pasteInPendingHandle)
	if err != nil {
		t.Fatalf("LoadMyKeys after /invite: %v", err)
	}
	if got.PeerID != inv.PeerID {
		t.Errorf("stashed PeerID = %q, want %q", got.PeerID, inv.PeerID)
	}
	wantSecret, _ := decodeHex(inv.Secret)
	if !bytes.Equal(got.OutboundSecret, wantSecret) {
		t.Error("stashed outbound doesn't match the one in the emitted invite")
	}
}

func decodeHex(s string) ([]byte, error) {
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		hi, ok1 := hexDigit(s[2*i])
		lo, ok2 := hexDigit(s[2*i+1])
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("bad hex %q", s)
		}
		out[i] = hi<<4 | lo
	}
	return out, nil
}

func hexDigit(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

func TestDispatch_SendText_PersistsOutboundEvent(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000000a"
	preEstablishSession(t, d, peerID)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendText, "s", ipc.SendTextRequest{
		PeerID: peerID, Text: "audit me",
	}))
	_ = readUntil(t, ctx, conn, ipc.FrameTextSent)

	rows, err := d.events.List(chatIDForPeer(t, d, peerID), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("List = %d rows, want 1", len(rows))
	}
	ev := rows[0]
	if ev.Direction != events.DirOut {
		t.Errorf("direction = %q, want out", ev.Direction)
	}
	if ev.Kind != events.KindText {
		t.Errorf("kind = %q, want text", ev.Kind)
	}
	if ev.SenderSeq != 1 {
		t.Errorf("sender_seq = %d, want 1", ev.SenderSeq)
	}
	if ev.EnvelopeID != "env-stub-0001" {
		t.Errorf("envelope_id = %q, want env-stub-0001", ev.EnvelopeID)
	}
	var body events.TextBody
	if err := json.Unmarshal(ev.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Text != "audit me" {
		t.Errorf("body.text = %q, want 'audit me'", body.Text)
	}
}

func TestDispatch_SendText_PushesTimelineEventOnBus(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000000a"
	preEstablishSession(t, d, peerID)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendText, "s", ipc.SendTextRequest{
		PeerID: peerID, Text: "broadcast me",
	}))

	var tl ipc.Frame
	gotTextSent := false
	gotTimeline := false
	for !(gotTextSent && gotTimeline) {
		f := readNext(t, ctx, conn)
		switch f.Type {
		case ipc.FrameTextSent:
			gotTextSent = true
		case ipc.FrameTimelineEvent:
			gotTimeline = true
			tl = f
		}
	}
	var p ipc.TimelineEventPayload
	if err := json.Unmarshal(tl.Payload, &p); err != nil {
		t.Fatal(err)
	}
	var ev events.Event
	if err := json.Unmarshal(p.Event, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.ChatID != chatIDForPeer(t, d, peerID) || ev.Direction != events.DirOut {
		t.Errorf("pushed event = %+v, want chat=<direct with %s> dir=out", ev, peerID)
	}
}

func makeInboundCiphertext(t *testing.T, d *daemon, fromPeerID, text string, seq uint64, ts int64) []byte {
	t.Helper()

	bobAddr := protocol.NewSignalAddress("bob-self", session.DeviceID)
	bobOPK := d.signalState.OneTimePreKeys[0]
	bobSPKSig := d.signalState.SignedPreKey.Signature()
	bobBundle := prekey.NewBundle(
		d.signalState.RegistrationID,
		session.DeviceID,
		optional.NewOptionalUint32(bobOPK.ID().Value),
		d.signalState.SignedPreKey.ID(),
		bobOPK.KeyPair().PublicKey(),
		d.signalState.SignedPreKey.KeyPair().PublicKey(),
		bobSPKSig,
		d.signalState.IdentityKeyPair.PublicKey(),
	)

	aliceDir := t.TempDir()
	aliceStore, err := store.Unlock(aliceDir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = aliceStore.Lock() })
	aliceState, _, err := signal.LoadOrBootstrap(aliceStore, 3)
	if err != nil {
		t.Fatal(err)
	}
	aliceStores := signal.NewStores(aliceStore, aliceState)
	ser := serialize.NewJSONSerializer()
	builder := libsession.NewBuilder(aliceStores, aliceStores, aliceStores, aliceStores, bobAddr, ser)
	if err := builder.ProcessBundle(context.Background(), bobBundle); err != nil {
		t.Fatalf("alice ProcessBundle: %v", err)
	}
	aliceCipher := session.New(aliceStores)

	testMsgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	wrapper, err := msg.BuildText(seq, ts, testMsgID, text, 0, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := msg.Marshal(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := aliceCipher.Encrypt(context.Background(), "bob-self", plain)
	if err != nil {
		t.Fatalf("alice encrypt: %v", err)
	}

	_ = fromPeerID
	return blob
}

func TestProcessInboxEntry_DecryptOK_PersistsAndPushes(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	const aliceID = "0000000000000000000000000000aaaa"
	const senderTs int64 = 1742643890
	blob := makeInboundCiphertext(t, d, aliceID, "hello bob", 7, senderTs)

	entry := backendapi.InboxEntry{
		ArrivalAt: time.Now().UnixNano(),
		PeerID:    aliceID,
		Envelope: backendapi.RawEnvelope{
			ID:        "env-in-1",
			Timestamp: senderTs,
			From:      "alice-onion",
			Kind:      "text",
			Payload:   blob,
			Mac:       []byte{1, 2, 3, 4},
		},
	}
	processInboxEntry(ctx, d, entry)

	rows, err := d.events.List(chatIDForPeer(t, d, aliceID), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("persisted %d rows, want 1", len(rows))
	}
	ev := rows[0]
	if ev.DecryptStatus != events.DecryptOK {
		t.Errorf("decrypt_status = %q, want ok", ev.DecryptStatus)
	}
	if ev.SenderSeq != 7 {
		t.Errorf("sender_seq = %d, want 7", ev.SenderSeq)
	}
	if ev.SenderTs != senderTs {
		t.Errorf("sender_ts = %d, want %d", ev.SenderTs, senderTs)
	}
	if ev.EnvelopeID != "env-in-1" {
		t.Errorf("envelope_id = %q, want env-in-1", ev.EnvelopeID)
	}
	var body events.TextBody
	if err := json.Unmarshal(ev.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Text != "hello bob" {
		t.Errorf("body.text = %q, want 'hello bob'", body.Text)
	}

	tl := readUntil(t, ctx, conn, ipc.FrameTimelineEvent)
	var p ipc.TimelineEventPayload
	if err := json.Unmarshal(tl.Payload, &p); err != nil {
		t.Fatal(err)
	}
	var pushed events.Event
	if err := json.Unmarshal(p.Event, &pushed); err != nil {
		t.Fatal(err)
	}
	if pushed.Direction != events.DirIn || pushed.ChatID != chatIDForPeer(t, d, aliceID) {
		t.Errorf("pushed event = %+v, want in-from-alice-chat", pushed)
	}
}

func TestProcessInboxEntry_DecryptFails_PersistsRawBlob(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	const aliceID = "0000000000000000000000000000aaaa"

	garbage := append([]byte{0x02}, []byte("not a real ciphertext, just bytes")...)

	entry := backendapi.InboxEntry{
		ArrivalAt: time.Now().UnixNano(),
		PeerID:    aliceID,
		Envelope: backendapi.RawEnvelope{
			ID:        "env-bad-1",
			Timestamp: 1742643890,
			From:      "alice-onion",
			Payload:   garbage,
		},
	}
	processInboxEntry(context.Background(), d, entry)

	rows, err := d.events.List(chatIDForPeer(t, d, aliceID), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("persisted %d rows, want 1 (decrypt fail row)", len(rows))
	}
	ev := rows[0]
	if ev.DecryptStatus != events.DecryptFailed {
		t.Errorf("decrypt_status = %q, want failed", ev.DecryptStatus)
	}
	if ev.EnvelopeID != "env-bad-1" {
		t.Errorf("envelope_id = %q, want env-bad-1", ev.EnvelopeID)
	}
	if string(ev.RawBlob) != string(garbage) {
		t.Errorf("raw_blob round-trip drift")
	}
}

func TestProcessInboxEntry_EmptyPeerID_Skipped(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)

	entry := backendapi.InboxEntry{
		ArrivalAt: time.Now().UnixNano(),
		PeerID:    "",
		Envelope: backendapi.RawEnvelope{
			ID:      "env-orphan",
			Payload: []byte{0x02, 0xFF},
		},
	}
	processInboxEntry(context.Background(), d, entry)

	if got, err := d.events.PeekNextRecvSeq(); err == nil && got != 1 {
		t.Errorf("recv_seq advanced to %d for empty-peer entry; should have been skipped", got-1)
	}
}

func TestDispatch_InspectEvent_HappyPath(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)

	body, _ := json.Marshal(events.TextBody{Text: "hello"})
	const inspectPeer = "alice123"
	if _, err := d.chats.CreateDirect(inspectPeer); err != nil {
		t.Fatal(err)
	}
	ev, err := d.events.AppendOutbound(events.OutboundParams{
		ChatID: chatIDForPeer(t, d, inspectPeer), Kind: events.KindText, SenderSeq: 1,
		EnvelopeID: "env-insp-1", MsgID: "mid-insp-1", Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameInspectEvent, "ins-1", ipc.InspectEventRequest{
		MsgID: "mid-insp-1",
	}))
	resp := readUntil(t, ctx, conn, ipc.FrameEventInspected)
	var body1 ipc.EventInspectedResponse
	if err := json.Unmarshal(resp.Payload, &body1); err != nil {
		t.Fatal(err)
	}
	var got events.Event
	if err := json.Unmarshal(body1.Event, &got); err != nil {
		t.Fatal(err)
	}
	if got.RecvSeq != ev.RecvSeq || got.MsgID != "mid-insp-1" || got.ChatID != chatIDForPeer(t, d, inspectPeer) {
		t.Errorf("inspected event drift: %+v", got)
	}
}

func TestDispatch_InspectEvent_NotFound(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusCreated)
	_, addr, certPath, token := newTestDaemon(t, stub)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameInspectEvent, "ins-2", ipc.InspectEventRequest{
		MsgID: "nonexistent",
	}))
	errF := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	_ = json.Unmarshal(errF.Payload, &ep)
	if ep.Code != "not_found" {
		t.Errorf("code = %q, want not_found", ep.Code)
	}
}

func TestDispatch_InspectEvent_EmptyMsgID(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusCreated)
	_, addr, certPath, token := newTestDaemon(t, stub)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameInspectEvent, "ins-3", ipc.InspectEventRequest{
		MsgID: "",
	}))
	errF := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	_ = json.Unmarshal(errF.Payload, &ep)
	if ep.Code != "bad_request" {
		t.Errorf("code = %q, want bad_request", ep.Code)
	}
}

func appendTestEvents(t *testing.T, d *daemon, peerID string, n int) {
	t.Helper()
	if _, err := d.chats.CreateDirect(peerID); err != nil {
		t.Fatal(err)
	}
	cid := chatIDForPeer(t, d, peerID)
	body, _ := json.Marshal(events.TextBody{Text: "m"})
	for i := 0; i < n; i++ {
		if _, err := d.events.AppendOutbound(events.OutboundParams{
			ChatID: cid, Kind: events.KindText, SenderSeq: uint64(i),
			EnvelopeID: fmt.Sprintf("env-%s-out-%d", peerID, i), Body: body,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := d.events.AppendInbound(events.InboundParams{
			ChatID: cid, Kind: events.KindText, SenderTs: 1000 + int64(i),
			EnvelopeID: fmt.Sprintf("env-%s-in-%d", peerID, i), Status: events.DecryptOK, Body: body,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDispatch_PeerAction_Retire(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusCreated)
	stub.Peer = backendapi.Peer{ID: "alice123", KnownAddresses: []string{"alice-onion"}}
	d, addr, certPath, token := newTestDaemon(t, stub)
	appendTestEvents(t, d, "alice123", 3)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	req := mustFrame(ipc.FramePeerAction, "pa-1", ipc.PeerActionRequest{
		PeerID: "alice123", Action: ipc.PeerActionRetire,
	})
	writeFrame(t, ctx, conn, req)
	resp := readUntil(t, ctx, conn, ipc.FramePeerActionApplied)

	var body ipc.PeerActionAppliedResponse
	if err := json.Unmarshal(resp.Payload, &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Action != ipc.PeerActionRetire {
		t.Errorf("Action = %q, want retire", body.Action)
	}
	if body.Peer.ID != "alice123" || body.Peer.RetiredAt == 0 {
		t.Errorf("peer snapshot not retired: %+v", body.Peer)
	}
	if body.DeletedCount != 0 {
		t.Errorf("DeletedCount = %d, want 0 (retire doesn't wipe history)", body.DeletedCount)
	}
	if stub.PeerActionCalls != 1 || stub.LastPeerActionReq != "retire" {
		t.Errorf("backend /action: calls=%d action=%q, want 1 retire", stub.PeerActionCalls, stub.LastPeerActionReq)
	}

	rows, err := d.events.List(chatIDForPeer(t, d, "alice123"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 6 {
		t.Errorf("retire wiped history: %d rows left, want 6", len(rows))
	}

	pu := readUntil(t, ctx, conn, ipc.FramePeerUpdated)
	var pup ipc.PeerUpdatedPayload
	if err := json.Unmarshal(pu.Payload, &pup); err != nil {
		t.Fatal(err)
	}
	if pup.Peer.ID != "alice123" || pup.Peer.RetiredAt == 0 {
		t.Errorf("peer.updated broadcast: peer=%+v want alice123 retired", pup.Peer)
	}
}

func TestDispatch_ChatAction_Clear(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusCreated)
	stub.Peer = backendapi.Peer{ID: "alice123", KnownAddresses: []string{"alice-onion"}}
	d, addr, certPath, token := newTestDaemon(t, stub)
	appendTestEvents(t, d, "alice123", 4)
	appendTestEvents(t, d, "bob999", 2)

	aliceChatID := chatIDForPeer(t, d, "alice123")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	req := mustFrame(ipc.FrameChatAction, "ca-1", ipc.ChatActionRequest{
		ChatID: string(aliceChatID), Action: ipc.ChatActionClear,
	})
	writeFrame(t, ctx, conn, req)
	resp := readUntil(t, ctx, conn, ipc.FrameChatActionApplied)

	var body ipc.ChatActionAppliedResponse
	if err := json.Unmarshal(resp.Payload, &body); err != nil {
		t.Fatal(err)
	}
	if body.Action != ipc.ChatActionClear {
		t.Errorf("Action = %q, want clear", body.Action)
	}
	if body.DeletedCount != 8 {
		t.Errorf("DeletedCount = %d, want 8 (4 out + 4 in)", body.DeletedCount)
	}
	if stub.PeerActionCalls != 0 {
		t.Errorf("backend /action hit %d times on clear; clear must not touch backend", stub.PeerActionCalls)
	}

	if rows, _ := d.events.List(aliceChatID, 0, 0); len(rows) != 0 {
		t.Errorf("alice rows after clear = %d, want 0", len(rows))
	}
	if rows, _ := d.events.List(chatIDForPeer(t, d, "bob999"), 0, 0); len(rows) != 4 {
		t.Errorf("bob rows after alice clear = %d, want 4", len(rows))
	}
	if _, err := d.chats.GetByDirectPeer("alice123"); err != nil {
		t.Errorf("chat record gone after clear — should survive: %v", err)
	}

	cc := readUntil(t, ctx, conn, ipc.FrameChatCleared)
	var ccp ipc.ChatClearedPayload
	if err := json.Unmarshal(cc.Payload, &ccp); err != nil {
		t.Fatal(err)
	}
	if ccp.ChatID != string(aliceChatID) || ccp.DeletedCount != 8 {
		t.Errorf("chat.cleared broadcast: chat_id=%q deleted=%d, want %s 8", ccp.ChatID, ccp.DeletedCount, aliceChatID)
	}
}

func TestDispatch_ChatAction_Delete(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusCreated)
	stub.Peer = backendapi.Peer{ID: "alice123", KnownAddresses: []string{"alice-onion"}}
	d, addr, certPath, token := newTestDaemon(t, stub)
	appendTestEvents(t, d, "alice123", 2)
	aliceChatID := chatIDForPeer(t, d, "alice123")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	req := mustFrame(ipc.FrameChatAction, "ca-2", ipc.ChatActionRequest{
		ChatID: string(aliceChatID), Action: ipc.ChatActionDelete,
	})
	writeFrame(t, ctx, conn, req)
	resp := readUntil(t, ctx, conn, ipc.FrameChatActionApplied)

	var body ipc.ChatActionAppliedResponse
	if err := json.Unmarshal(resp.Payload, &body); err != nil {
		t.Fatal(err)
	}
	if body.Action != ipc.ChatActionDelete {
		t.Errorf("Action = %q, want delete", body.Action)
	}
	if body.DeletedCount != 4 {
		t.Errorf("DeletedCount = %d, want 4 (2 out + 2 in)", body.DeletedCount)
	}
	if stub.PeerActionCalls != 0 {
		t.Errorf("backend /action hit %d times on chat delete; must not touch backend", stub.PeerActionCalls)
	}

	if _, err := d.chats.GetByDirectPeer("alice123"); err == nil {
		t.Errorf("chat record still present after chat delete; want gone")
	}
	if rows, _ := d.events.List(aliceChatID, 0, 0); len(rows) != 0 {
		t.Errorf("events still present after chat delete: %d", len(rows))
	}

	cd := readUntil(t, ctx, conn, ipc.FrameChatDeleted)
	var cdp ipc.ChatDeletedPayload
	if err := json.Unmarshal(cd.Payload, &cdp); err != nil {
		t.Fatal(err)
	}
	if cdp.ChatID != string(aliceChatID) || cdp.DeletedCount != 4 {
		t.Errorf("chat.deleted broadcast: chat_id=%q deleted=%d, want %s 4", cdp.ChatID, cdp.DeletedCount, aliceChatID)
	}
}

func TestDispatch_ListChats(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusCreated)
	stub.Peer = backendapi.Peer{ID: "alice123"}
	d, addr, certPath, token := newTestDaemon(t, stub)
	if _, err := d.chats.CreateDirect("alice123"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.chats.CreateDirect("bob999"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	req := mustFrame(ipc.FrameListChats, "lc-1", nil)
	writeFrame(t, ctx, conn, req)
	resp := readUntil(t, ctx, conn, ipc.FrameChatsListed)

	var body ipc.ChatsListedResponse
	if err := json.Unmarshal(resp.Payload, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Chats) != 2 {
		t.Fatalf("got %d chats, want 2", len(body.Chats))
	}
	for _, c := range body.Chats {
		if c.Kind != ipc.ChatKindDirect {
			t.Errorf("kind = %q, want direct", c.Kind)
		}
		if c.PeerID == "" {
			t.Errorf("PeerID missing on DM entry: %+v", c)
		}
	}
}

func TestDispatch_PeerAction_Delete(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusCreated)
	stub.Peer = backendapi.Peer{ID: "alice123", KnownAddresses: []string{"alice-onion"}}
	d, addr, certPath, token := newTestDaemon(t, stub)
	if _, err := d.peerMeta.SetNick("alice123", "alice", 0); err != nil {
		t.Fatal(err)
	}
	appendTestEvents(t, d, "alice123", 2)
	aliceChatID := chatIDForPeer(t, d, "alice123")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	req := mustFrame(ipc.FramePeerAction, "pa-3", ipc.PeerActionRequest{
		PeerID: "alice123", Action: ipc.PeerActionDelete,
	})
	writeFrame(t, ctx, conn, req)
	resp := readUntil(t, ctx, conn, ipc.FramePeerActionApplied)

	var body ipc.PeerActionAppliedResponse
	if err := json.Unmarshal(resp.Payload, &body); err != nil {
		t.Fatal(err)
	}
	if body.Action != ipc.PeerActionDelete {
		t.Errorf("Action = %q, want delete", body.Action)
	}
	if body.DeletedCount != 4 {
		t.Errorf("DeletedCount = %d, want 4 (2 out + 2 in)", body.DeletedCount)
	}
	if body.Peer.Label != "alice" {
		t.Errorf("peer snapshot missing: Label = %q, want alice (pre-delete)", body.Peer.Label)
	}
	if stub.PeerActionCalls != 1 || stub.LastPeerActionReq != "delete" {
		t.Errorf("backend /action: calls=%d action=%q, want 1 delete", stub.PeerActionCalls, stub.LastPeerActionReq)
	}
	if rows, _ := d.events.List(aliceChatID, 0, 0); len(rows) != 0 {
		t.Errorf("alice rows after delete = %d, want 0", len(rows))
	}

	if _, err := d.chats.GetByDirectPeer("alice123"); err == nil {
		t.Errorf("chat still present after delete; want gone")
	}

	pd := readUntil(t, ctx, conn, ipc.FramePeerDeleted)
	var pdp ipc.PeerDeletedPayload
	if err := json.Unmarshal(pd.Payload, &pdp); err != nil {
		t.Fatal(err)
	}
	if pdp.PeerID != "alice123" {
		t.Errorf("peer.deleted broadcast: peer_id = %q, want alice123", pdp.PeerID)
	}
	cd := readUntil(t, ctx, conn, ipc.FrameChatDeleted)
	var cdp ipc.ChatDeletedPayload
	if err := json.Unmarshal(cd.Payload, &cdp); err != nil {
		t.Fatal(err)
	}
	if cdp.ChatID != string(aliceChatID) || cdp.DeletedCount != 4 {
		t.Errorf("chat.deleted broadcast: chat_id=%q deleted=%d, want %s 4", cdp.ChatID, cdp.DeletedCount, aliceChatID)
	}
}

func TestDispatch_PeerAction_UnknownAction(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusCreated)
	stub.Peer = backendapi.Peer{ID: "alice123"}
	_, addr, certPath, token := newTestDaemon(t, stub)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	req := mustFrame(ipc.FramePeerAction, "pa-4", ipc.PeerActionRequest{
		PeerID: "alice123", Action: ipc.PeerAction("bogus"),
	})
	writeFrame(t, ctx, conn, req)
	errF := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	_ = json.Unmarshal(errF.Payload, &ep)
	if ep.Code != "bad_request" {
		t.Errorf("code = %q, want bad_request", ep.Code)
	}
}

func TestDispatch_SendEdit_HappyPath(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000000a"
	preEstablishSession(t, d, peerID)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendText, "s1", ipc.SendTextRequest{
		PeerID: peerID, Text: "hello bov",
	}))
	sent := readUntil(t, ctx, conn, ipc.FrameTextSent)
	var sp ipc.SendTextResponse
	_ = json.Unmarshal(sent.Payload, &sp)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendEdit, "e1", ipc.SendEditRequest{
		PeerID: peerID, TargetMsgID: sp.MsgID, Text: "hello bob",
	}))
	editResp := readUntil(t, ctx, conn, ipc.FrameEditSent)
	if editResp.ID != "e1" {
		t.Errorf("correlation id = %q, want e1", editResp.ID)
	}
	var ep ipc.SendEditResponse
	if err := json.Unmarshal(editResp.Payload, &ep); err != nil {
		t.Fatal(err)
	}
	if ep.TargetMsgID != sp.MsgID {
		t.Errorf("echoed target = %q, want %q", ep.TargetMsgID, sp.MsgID)
	}
	if ep.MsgID == "" || ep.MsgID == sp.MsgID {
		t.Errorf("edit msg_id = %q should differ from target %q", ep.MsgID, sp.MsgID)
	}
	if ep.SenderSeq != 2 {
		t.Errorf("edit sender_seq = %d, want 2 (second send)", ep.SenderSeq)
	}
	if stub.SendCalls != 2 {
		t.Errorf("haomad saw %d /send calls, want 2 (text + edit)", stub.SendCalls)
	}

	ev, err := d.events.GetByMsgID(sp.MsgID)
	if err != nil {
		t.Fatalf("GetByMsgID target after edit: %v", err)
	}
	var tb events.TextBody
	_ = json.Unmarshal(ev.Body, &tb)
	if tb.Text != "hello bob" {
		t.Errorf("mutated text = %q, want 'hello bob'", tb.Text)
	}
	if ev.EditedAt == 0 {
		t.Error("EditedAt not stamped on target row")
	}

	if _, err := d.events.GetByMsgID(ep.MsgID); !errors.Is(err, events.ErrEventNotFound) {
		t.Errorf("edit msg_id unexpectedly indexed: %v", err)
	}
}

func TestDispatch_SendEdit_UnknownTarget(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000000a"
	preEstablishSession(t, d, peerID)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendEdit, "e", ipc.SendEditRequest{
		PeerID: peerID, TargetMsgID: "deadbeefdeadbeefdeadbeefdeadbeef", Text: "x",
	}))
	resp := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	_ = json.Unmarshal(resp.Payload, &ep)
	if ep.Code != "unknown_target" {
		t.Errorf("code = %q, want unknown_target", ep.Code)
	}
	if stub.SendCalls != 0 {
		t.Errorf("haomad saw %d /send calls, want 0 (nothing to edit)", stub.SendCalls)
	}
	_ = d
}

func TestDispatch_SendEdit_RejectsEditingPeerMessage(t *testing.T) {

	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000000a"
	preEstablishSession(t, d, peerID)
	cid := chatIDForPeer(t, d, peerID)

	body, _ := json.Marshal(events.TextBody{Text: "their message"})
	inbound, err := d.events.AppendInbound(events.InboundParams{
		ChatID: cid, Kind: events.KindText, SenderTs: time.Now().Unix(),
		SenderSeq: 1, EnvelopeID: "env-in", MsgID: "1111111111111111aaaaaaaaaaaaaaaa",
		Status: events.DecryptOK, Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendEdit, "e", ipc.SendEditRequest{
		PeerID: peerID, TargetMsgID: inbound.MsgID, Text: "forged",
	}))
	resp := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	_ = json.Unmarshal(resp.Payload, &ep)
	if ep.Code != "not_author" {
		t.Errorf("code = %q, want not_author", ep.Code)
	}
	if stub.SendCalls != 0 {
		t.Errorf("haomad saw %d /send calls, want 0 (rejected pre-encrypt)", stub.SendCalls)
	}
}

func TestDispatch_SendEdit_EditWindowExpired(t *testing.T) {

	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000000a"
	preEstablishSession(t, d, peerID)
	cid := chatIDForPeer(t, d, peerID)

	oldNow := time.Now().Add(-25 * time.Hour)
	d.events = events.New(d.store, d.eventBus, func() time.Time { return oldNow })
	body, _ := json.Marshal(events.TextBody{Text: "ancient history"})
	out, err := d.events.AppendOutbound(events.OutboundParams{
		ChatID: cid, Kind: events.KindText, SenderSeq: 1,
		EnvelopeID: "env-old", MsgID: "22222222aaaaaaaabbbbbbbbcccccccc", Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	d.events = events.New(d.store, d.eventBus, time.Now)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendEdit, "e", ipc.SendEditRequest{
		PeerID: peerID, TargetMsgID: out.MsgID, Text: "too late",
	}))
	resp := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	_ = json.Unmarshal(resp.Payload, &ep)
	if ep.Code != "edit_window_expired" {
		t.Errorf("code = %q, want edit_window_expired", ep.Code)
	}
	if stub.SendCalls != 0 {
		t.Errorf("haomad saw %d /send calls, want 0 (window check precedes send)", stub.SendCalls)
	}
}

func TestDispatch_SendEdit_BadRequest_Empty(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	_, addr, certPath, token := newTestDaemon(t, stub)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	cases := []struct {
		name string
		req  ipc.SendEditRequest
	}{
		{"no peer_id", ipc.SendEditRequest{TargetMsgID: "x", Text: "y"}},
		{"no target", ipc.SendEditRequest{PeerID: "00000000000000000000000000000001", Text: "y"}},
		{"no text", ipc.SendEditRequest{PeerID: "00000000000000000000000000000001", TargetMsgID: "x"}},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendEdit, fmt.Sprintf("b%d", i), c.req))
			resp := readUntil(t, ctx, conn, ipc.FrameError)
			var ep ipc.ErrorPayload
			_ = json.Unmarshal(resp.Payload, &ep)
			if ep.Code != "bad_request" {
				t.Errorf("code = %q, want bad_request", ep.Code)
			}
		})
	}
}

func TestDispatch_SendDelete_HappyPath(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000000a"
	preEstablishSession(t, d, peerID)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendText, "s1", ipc.SendTextRequest{
		PeerID: peerID, Text: "wrong window",
	}))
	sent := readUntil(t, ctx, conn, ipc.FrameTextSent)
	var sp ipc.SendTextResponse
	_ = json.Unmarshal(sent.Payload, &sp)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendDelete, "d1", ipc.SendDeleteRequest{
		PeerID: peerID, TargetMsgID: sp.MsgID,
	}))
	delResp := readUntil(t, ctx, conn, ipc.FrameDeleteSent)
	if delResp.ID != "d1" {
		t.Errorf("correlation id = %q, want d1", delResp.ID)
	}
	var dp ipc.SendDeleteResponse
	if err := json.Unmarshal(delResp.Payload, &dp); err != nil {
		t.Fatal(err)
	}
	if dp.TargetMsgID != sp.MsgID {
		t.Errorf("echoed target = %q, want %q", dp.TargetMsgID, sp.MsgID)
	}
	if dp.MsgID == "" || dp.MsgID == sp.MsgID {
		t.Errorf("delete msg_id = %q should differ from target %q", dp.MsgID, sp.MsgID)
	}
	if dp.SenderSeq != 2 {
		t.Errorf("delete sender_seq = %d, want 2 (second send)", dp.SenderSeq)
	}
	if stub.SendCalls != 2 {
		t.Errorf("haomad saw %d /send calls, want 2 (text + delete)", stub.SendCalls)
	}

	ev, err := d.events.GetByMsgID(sp.MsgID)
	if err != nil {
		t.Fatalf("GetByMsgID target after delete: %v", err)
	}
	if ev.Body != nil {
		t.Errorf("body = %q, want nil", string(ev.Body))
	}
	if ev.DeletedAt == 0 {
		t.Error("DeletedAt not stamped on target row")
	}

	if _, err := d.events.GetByMsgID(dp.MsgID); !errors.Is(err, events.ErrEventNotFound) {
		t.Errorf("delete msg_id unexpectedly indexed: %v", err)
	}
}

func TestDispatch_SendDelete_HappyPath_KindFile(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000000a"
	preEstablishSession(t, d, peerID)
	cid := chatIDForPeer(t, d, peerID)

	body := json.RawMessage(`{"original_name":"photo.jpg"}`)
	out, err := d.events.AppendOutbound(events.OutboundParams{
		ChatID: cid, Kind: events.KindFile, MsgID: "44444444444444444444444444444444",
		Body: body, SenderSeq: 1, EnvelopeID: "env-file",
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendDelete, "df", ipc.SendDeleteRequest{
		PeerID: peerID, TargetMsgID: out.MsgID,
	}))
	delResp := readUntil(t, ctx, conn, ipc.FrameDeleteSent)
	if delResp.ID != "df" {
		t.Errorf("correlation id = %q, want df", delResp.ID)
	}
	var dp ipc.SendDeleteResponse
	if err := json.Unmarshal(delResp.Payload, &dp); err != nil {
		t.Fatal(err)
	}
	if dp.TargetMsgID != out.MsgID {
		t.Errorf("echoed target = %q, want %q", dp.TargetMsgID, out.MsgID)
	}

	ev, err := d.events.GetByMsgID(out.MsgID)
	if err != nil {
		t.Fatalf("GetByMsgID after delete: %v", err)
	}
	if ev.DeletedAt == 0 {
		t.Errorf("DeletedAt not stamped on KindFile target")
	}
}

func TestDispatch_SendDelete_UnknownTarget(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000000a"
	preEstablishSession(t, d, peerID)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendDelete, "d", ipc.SendDeleteRequest{
		PeerID: peerID, TargetMsgID: "deadbeefdeadbeefdeadbeefdeadbeef",
	}))
	resp := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	_ = json.Unmarshal(resp.Payload, &ep)
	if ep.Code != "unknown_target" {
		t.Errorf("code = %q, want unknown_target", ep.Code)
	}
	if stub.SendCalls != 0 {
		t.Errorf("haomad saw %d /send calls, want 0 (nothing to delete)", stub.SendCalls)
	}
	_ = d
}

func TestDispatch_SendDelete_RejectsDeletingPeerMessage(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000000a"
	preEstablishSession(t, d, peerID)
	cid := chatIDForPeer(t, d, peerID)

	body, _ := json.Marshal(events.TextBody{Text: "their message"})
	inbound, err := d.events.AppendInbound(events.InboundParams{
		ChatID: cid, Kind: events.KindText, SenderTs: time.Now().Unix(),
		SenderSeq: 1, EnvelopeID: "env-in", MsgID: "1111111111111111aaaaaaaaaaaaaaaa",
		Status: events.DecryptOK, Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendDelete, "d", ipc.SendDeleteRequest{
		PeerID: peerID, TargetMsgID: inbound.MsgID,
	}))
	resp := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	_ = json.Unmarshal(resp.Payload, &ep)

	if ep.Code != "not_deletable" {
		t.Errorf("code = %q, want not_deletable", ep.Code)
	}
	if stub.SendCalls != 0 {
		t.Errorf("haomad saw %d /send calls, want 0 (rejected pre-encrypt)", stub.SendCalls)
	}
}

func TestDispatch_SendDelete_WindowExpired(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000000a"
	preEstablishSession(t, d, peerID)
	cid := chatIDForPeer(t, d, peerID)

	oldNow := time.Now().Add(-25 * time.Hour)
	d.events = events.New(d.store, d.eventBus, func() time.Time { return oldNow })
	body, _ := json.Marshal(events.TextBody{Text: "ancient history"})
	out, err := d.events.AppendOutbound(events.OutboundParams{
		ChatID: cid, Kind: events.KindText, SenderSeq: 1,
		EnvelopeID: "env-old", MsgID: "33333333aaaaaaaabbbbbbbbcccccccc", Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	d.events = events.New(d.store, d.eventBus, time.Now)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendDelete, "d", ipc.SendDeleteRequest{
		PeerID: peerID, TargetMsgID: out.MsgID,
	}))
	resp := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	_ = json.Unmarshal(resp.Payload, &ep)

	if ep.Code != "not_deletable" {
		t.Errorf("code = %q, want not_deletable", ep.Code)
	}
	if stub.SendCalls != 0 {
		t.Errorf("haomad saw %d /send calls, want 0 (window check precedes send)", stub.SendCalls)
	}
}

func TestDispatch_SendDelete_BadRequest_Empty(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	_, addr, certPath, token := newTestDaemon(t, stub)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	cases := []struct {
		name string
		req  ipc.SendDeleteRequest
	}{
		{"no peer_id", ipc.SendDeleteRequest{TargetMsgID: "x"}},
		{"no target", ipc.SendDeleteRequest{PeerID: "00000000000000000000000000000001"}},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendDelete, fmt.Sprintf("b%d", i), c.req))
			resp := readUntil(t, ctx, conn, ipc.FrameError)
			var ep ipc.ErrorPayload
			_ = json.Unmarshal(resp.Payload, &ep)
			if ep.Code != "bad_request" {
				t.Errorf("code = %q, want bad_request", ep.Code)
			}
		})
	}
}

func TestDispatch_SendReaction_HappyPath_OnPeerMessage(t *testing.T) {

	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000000a"
	preEstablishSession(t, d, peerID)
	cid := chatIDForPeer(t, d, peerID)

	body, _ := json.Marshal(events.TextBody{Text: "their message"})
	inbound, err := d.events.AppendInbound(events.InboundParams{
		ChatID: cid, Kind: events.KindText, SenderTs: time.Now().Unix(),
		SenderSeq: 1, EnvelopeID: "env-in", MsgID: "1111111111111111aaaaaaaaaaaaaaaa",
		Status: events.DecryptOK, Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendReaction, "r1", ipc.SendReactionRequest{
		PeerID: peerID, TargetMsgID: inbound.MsgID, Emoji: "👍",
	}))
	resp := readUntil(t, ctx, conn, ipc.FrameReactionSent)
	if resp.ID != "r1" {
		t.Errorf("correlation id = %q, want r1", resp.ID)
	}
	var rp ipc.SendReactionResponse
	if err := json.Unmarshal(resp.Payload, &rp); err != nil {
		t.Fatal(err)
	}
	if rp.TargetMsgID != inbound.MsgID {
		t.Errorf("echoed target = %q, want %q", rp.TargetMsgID, inbound.MsgID)
	}
	if rp.MsgID == "" || rp.MsgID == inbound.MsgID {
		t.Errorf("reaction msg_id = %q should differ from target %q", rp.MsgID, inbound.MsgID)
	}
	if stub.SendCalls != 1 {
		t.Errorf("haomad saw %d /send calls, want 1", stub.SendCalls)
	}

	rows, err := d.events.List(cid, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var reactions int
	for _, r := range rows {
		if r.Kind == events.KindReaction {
			reactions++
			if r.Direction != events.DirOut {
				t.Errorf("breadcrumb direction = %q, want out (self reactor)", r.Direction)
			}
			if r.SenderPeerID != "" {
				t.Errorf("breadcrumb SenderPeerID = %q, want empty (self)", r.SenderPeerID)
			}
			var rb events.ReactionBody
			_ = json.Unmarshal(r.Body, &rb)
			if rb.Emoji != "👍" || rb.TargetMsgID != inbound.MsgID {
				t.Errorf("breadcrumb body drift: %+v", rb)
			}
		}
	}
	if reactions != 1 {
		t.Errorf("got %d reaction breadcrumbs, want 1", reactions)
	}
}

func TestDispatch_SendReaction_EmptyEmojiIsRemoval(t *testing.T) {

	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000000a"
	preEstablishSession(t, d, peerID)
	cid := chatIDForPeer(t, d, peerID)

	body, _ := json.Marshal(events.TextBody{Text: "target"})
	inbound, err := d.events.AppendInbound(events.InboundParams{
		ChatID: cid, Kind: events.KindText, SenderTs: time.Now().Unix(),
		SenderSeq: 1, EnvelopeID: "env-in", MsgID: "2222222222222222aaaaaaaaaaaaaaaa",
		Status: events.DecryptOK, Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendReaction, "r", ipc.SendReactionRequest{
		PeerID: peerID, TargetMsgID: inbound.MsgID, Emoji: "",
	}))
	readUntil(t, ctx, conn, ipc.FrameReactionSent)
	if stub.SendCalls != 1 {
		t.Errorf("haomad saw %d /send calls, want 1", stub.SendCalls)
	}

	rows, _ := d.events.List(cid, 0, 0)
	var found bool
	for _, r := range rows {
		if r.Kind == events.KindReaction {
			var rb events.ReactionBody
			_ = json.Unmarshal(r.Body, &rb)
			if rb.Emoji == "" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("no empty-emoji removal breadcrumb found")
	}
}

func TestDispatch_SendReaction_UnknownTarget(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000000a"
	preEstablishSession(t, d, peerID)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendReaction, "r", ipc.SendReactionRequest{
		PeerID: peerID, TargetMsgID: "deadbeefdeadbeefdeadbeefdeadbeef", Emoji: "👍",
	}))
	resp := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	_ = json.Unmarshal(resp.Payload, &ep)
	if ep.Code != "unknown_target" {
		t.Errorf("code = %q, want unknown_target", ep.Code)
	}
	if stub.SendCalls != 0 {
		t.Errorf("haomad saw %d /send calls, want 0", stub.SendCalls)
	}
	_ = d
}

func TestDispatch_SendReaction_RejectsTombstonedTarget(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000000a"
	preEstablishSession(t, d, peerID)
	cid := chatIDForPeer(t, d, peerID)

	body, _ := json.Marshal(events.TextBody{Text: "soon-to-be-deleted"})
	out, err := d.events.AppendOutbound(events.OutboundParams{
		ChatID: cid, Kind: events.KindText, SenderSeq: 1,
		EnvelopeID: "env-o", MsgID: "3333333333333333aaaaaaaaaaaaaaaa", Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.events.ApplyDelete(out.MsgID, time.Now().Unix(), ""); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendReaction, "r", ipc.SendReactionRequest{
		PeerID: peerID, TargetMsgID: out.MsgID, Emoji: "👍",
	}))
	resp := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	_ = json.Unmarshal(resp.Payload, &ep)
	if ep.Code != "unsupported_kind" {
		t.Errorf("code = %q, want unsupported_kind", ep.Code)
	}
	if stub.SendCalls != 0 {
		t.Errorf("haomad saw %d /send calls, want 0 (tombstoned target)", stub.SendCalls)
	}
}

func TestDispatch_SendReaction_NoTimeWindow(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000000a"
	preEstablishSession(t, d, peerID)
	cid := chatIDForPeer(t, d, peerID)

	oldNow := time.Now().Add(-25 * time.Hour)
	d.events = events.New(d.store, d.eventBus, func() time.Time { return oldNow })
	body, _ := json.Marshal(events.TextBody{Text: "ancient"})
	out, err := d.events.AppendOutbound(events.OutboundParams{
		ChatID: cid, Kind: events.KindText, SenderSeq: 1,
		EnvelopeID: "env-old", MsgID: "44444444aaaaaaaabbbbbbbbcccccccc", Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	d.events = events.New(d.store, d.eventBus, time.Now)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendReaction, "r", ipc.SendReactionRequest{
		PeerID: peerID, TargetMsgID: out.MsgID, Emoji: "😂",
	}))
	readUntil(t, ctx, conn, ipc.FrameReactionSent)
	if stub.SendCalls != 1 {
		t.Errorf("haomad saw %d /send calls, want 1 (no window)", stub.SendCalls)
	}
}

func TestDispatch_SendReaction_BadRequest_Empty(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	_, addr, certPath, token := newTestDaemon(t, stub)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	cases := []struct {
		name string
		req  ipc.SendReactionRequest
	}{
		{"no peer_id", ipc.SendReactionRequest{TargetMsgID: "x", Emoji: "👍"}},
		{"no target", ipc.SendReactionRequest{PeerID: "00000000000000000000000000000001", Emoji: "👍"}},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendReaction, fmt.Sprintf("b%d", i), c.req))
			resp := readUntil(t, ctx, conn, ipc.FrameError)
			var ep ipc.ErrorPayload
			_ = json.Unmarshal(resp.Payload, &ep)
			if ep.Code != "bad_request" {
				t.Errorf("code = %q, want bad_request", ep.Code)
			}
		})
	}
}

func mustFrame(t ipc.FrameType, id string, payload any) ipc.Frame {
	f, err := ipc.NewFrame(t, id, payload)
	if err != nil {
		panic(err)
	}
	return f
}

var _ io.Reader
