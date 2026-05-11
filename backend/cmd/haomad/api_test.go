package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"haoma/internal/peers"
	"haoma/internal/eventbus"
	"haoma/internal/ids"
	"haoma/internal/outbox"
	"haoma/internal/store"
	"haoma/internal/xport"
)

func TestMain(m *testing.M) {
	store.DefaultKDFParams = store.KDFParams{
		Time: 1, Memory: 8 * 1024, Threads: 4, KeyLen: 32, SaltLen: 16,
	}
	os.Exit(m.Run())
}

func newTestDaemon(t *testing.T) *daemon {
	t.Helper()
	s, err := store.Unlock(t.TempDir(), "pw")
	if err != nil {
		t.Fatalf("store.Unlock: %v", err)
	}
	t.Cleanup(func() { _ = s.Lock() })
	return &daemon{
		cfg:      config{},
		store:    s,
		registry: peers.NewRegistry(s),
		bus:      &eventbus.Bus{},
	}
}

func newTestBackendInvite(t *testing.T, addresses []string) *peers.BackendInvite {
	t.Helper()
	id, err := peers.NewPeerID()
	if err != nil {
		t.Fatal(err)
	}
	secret, err := peers.NewPeerSecret()
	if err != nil {
		t.Fatal(err)
	}
	hexS := hex.EncodeToString(secret)

	myOnion := "myonion" + strings.Repeat("a", 56-7-len(id)) + id
	return &peers.BackendInvite{
		PeerID:            id,
		Addresses:         addresses,
		InboundSecret:     hexS,
		OutboundSecret:    hexS,
		MyOnionAddr:       myOnion,
		MyOnionPrivateKey: "ZmFrZS1wcml2YXRlLWtleQ==",
	}
}

func get(t *testing.T, srv *httptest.Server, path string) *http.Response {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestAPI_Health(t *testing.T) {
	srv := httptest.NewServer(newTestDaemon(t).apiHandler())
	defer srv.Close()

	resp := get(t, srv, "/health")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["status"] != "ok" {
		t.Errorf("body = %v", body)
	}
}

func TestAPI_Peers_EmptyList(t *testing.T) {
	srv := httptest.NewServer(newTestDaemon(t).apiHandler())
	defer srv.Close()

	resp := get(t, srv, "/peers")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	var body struct{ Peers []peerView }
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Peers) != 0 {
		t.Errorf("peers = %v, want []", body.Peers)
	}
}

func TestAPI_ImportPeer_ThenList(t *testing.T) {
	d := newTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	inv := newTestBackendInvite(t, []string{"alice-onion"})
	raw, _ := json.Marshal(inv)

	resp, err := http.Post(srv.URL+"/peers", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /peers status = %d, want 201", resp.StatusCode)
	}

	listResp := get(t, srv, "/peers")
	defer listResp.Body.Close()
	var body struct{ Peers []peerView }
	_ = json.NewDecoder(listResp.Body).Decode(&body)
	if len(body.Peers) != 1 {
		t.Fatalf("peers = %v, want one entry", body.Peers)
	}
	got := body.Peers[0]
	if got.ID != inv.PeerID {
		t.Errorf("peer ID = %q, want %q", got.ID, inv.PeerID)
	}
}

func TestAPI_PeerView_OmitsSecret(t *testing.T) {
	d := newTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	inv := newTestBackendInvite(t, []string{"alice-onion"})
	raw, _ := json.Marshal(inv)
	resp, _ := http.Post(srv.URL+"/peers", "application/json", bytes.NewReader(raw))
	resp.Body.Close()

	listResp := get(t, srv, "/peers")
	defer listResp.Body.Close()
	raw2, _ := readAll(listResp)
	if strings.Contains(raw2, "inbound_secret") || strings.Contains(raw2, "outbound_secret") || strings.Contains(raw2, inv.InboundSecret) || strings.Contains(raw2, inv.OutboundSecret) {
		t.Errorf("list response leaks secret: %s", raw2)
	}

	getResp := get(t, srv, "/peers/"+inv.PeerID)
	defer getResp.Body.Close()
	raw3, _ := readAll(getResp)
	if strings.Contains(raw3, "inbound_secret") || strings.Contains(raw3, "outbound_secret") || strings.Contains(raw3, inv.InboundSecret) || strings.Contains(raw3, inv.OutboundSecret) {
		t.Errorf("get response leaks secret: %s", raw3)
	}
}

func TestAPI_GetPeer_NotFound(t *testing.T) {
	srv := httptest.NewServer(newTestDaemon(t).apiHandler())
	defer srv.Close()

	resp := get(t, srv, "/peers/nonexistent-id")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestAPI_DeletePeer(t *testing.T) {
	d := newTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	inv := newTestBackendInvite(t, []string{"alice-onion"})
	raw, _ := json.Marshal(inv)
	resp, _ := http.Post(srv.URL+"/peers", "application/json", bytes.NewReader(raw))
	resp.Body.Close()

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/peers/"+inv.PeerID, nil)
	delResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		t.Errorf("DELETE status = %d, want 204", delResp.StatusCode)
	}

	getResp := get(t, srv, "/peers/"+inv.PeerID)
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusNotFound {
		t.Errorf("post-delete GET status = %d, want 404", getResp.StatusCode)
	}
}

func TestAPI_PeerAction_Retire(t *testing.T) {
	d := newTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	inv := newTestBackendInvite(t, []string{"alice-onion"})
	raw, _ := json.Marshal(inv)
	resp, _ := http.Post(srv.URL+"/peers", "application/json", bytes.NewReader(raw))
	resp.Body.Close()

	body := bytes.NewReader([]byte(`{"action":"retire"}`))
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/peers/"+inv.PeerID+"/action", body)
	req.Header.Set("Content-Type", "application/json")
	actResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer actResp.Body.Close()
	if actResp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", actResp.StatusCode)
	}
	var got peerView
	if err := json.NewDecoder(actResp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.ID != inv.PeerID {
		t.Errorf("returned id = %q, want %q", got.ID, inv.PeerID)
	}
	if got.RetiredAt == 0 {
		t.Errorf("RetiredAt = 0, want non-zero")
	}
	if len(got.KnownAddresses) != 0 {
		t.Errorf("KnownAddresses after retire = %v, want empty", got.KnownAddresses)
	}

	peer, err := d.registry.Get(inv.PeerID)
	if err != nil {
		t.Fatal(err)
	}
	if peer.RetiredAt != got.RetiredAt {
		t.Errorf("persisted RetiredAt = %d, response = %d", peer.RetiredAt, got.RetiredAt)
	}
	if peer.InboundSecret != nil || peer.OutboundSecret != nil {
		t.Errorf("secrets still present on retired peer: in=%x out=%x", peer.InboundSecret, peer.OutboundSecret)
	}
}

func TestAPI_PeerAction_Delete(t *testing.T) {
	d := newTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	inv := newTestBackendInvite(t, []string{"alice-onion"})
	raw, _ := json.Marshal(inv)
	resp, _ := http.Post(srv.URL+"/peers", "application/json", bytes.NewReader(raw))
	resp.Body.Close()

	body := bytes.NewReader([]byte(`{"action":"delete"}`))
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/peers/"+inv.PeerID+"/action", body)
	req.Header.Set("Content-Type", "application/json")
	actResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer actResp.Body.Close()
	if actResp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", actResp.StatusCode)
	}
	var got peerView
	if err := json.NewDecoder(actResp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.ID != inv.PeerID {
		t.Errorf("returned id = %q, want %q (pre-delete snapshot)", got.ID, inv.PeerID)
	}

	if _, err := d.registry.Get(inv.PeerID); !errors.Is(err, peers.ErrPeerNotFound) {
		t.Errorf("Get after delete: err = %v, want ErrPeerNotFound", err)
	}
}

func TestAPI_PeerAction_NotFound(t *testing.T) {
	srv := httptest.NewServer(newTestDaemon(t).apiHandler())
	defer srv.Close()
	body := bytes.NewReader([]byte(`{"action":"retire"}`))
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/peers/nonexistent/action", body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestAPI_PeerAction_UnknownAction(t *testing.T) {
	d := newTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	inv := newTestBackendInvite(t, []string{"alice-onion"})
	raw, _ := json.Marshal(inv)
	resp, _ := http.Post(srv.URL+"/peers", "application/json", bytes.NewReader(raw))
	resp.Body.Close()

	body := bytes.NewReader([]byte(`{"action":"clear"}`))
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/peers/"+inv.PeerID+"/action", body)
	req.Header.Set("Content-Type", "application/json")
	actResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer actResp.Body.Close()

	if actResp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (unknown action)", actResp.StatusCode)
	}
}

func TestAPI_PeerAction_RejectsUnknownField(t *testing.T) {
	srv := httptest.NewServer(newTestDaemon(t).apiHandler())
	defer srv.Close()
	body := bytes.NewReader([]byte(`{"action":"retire","extra":"nope"}`))
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/peers/any/action", body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestAPI_ImportPeer_RejectsWrongContentType(t *testing.T) {
	srv := httptest.NewServer(newTestDaemon(t).apiHandler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/peers", "text/plain", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want 415", resp.StatusCode)
	}
}

func TestAPI_ImportPeer_RejectsBadInvite(t *testing.T) {
	srv := httptest.NewServer(newTestDaemon(t).apiHandler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/peers", "application/json", strings.NewReader(`{"rogue":"field"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestAPI_Stats(t *testing.T) {
	d := newTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	inv := newTestBackendInvite(t, []string{"alice-onion"})
	raw, _ := json.Marshal(inv)
	resp, _ := http.Post(srv.URL+"/peers", "application/json", bytes.NewReader(raw))
	resp.Body.Close()

	statsResp := get(t, srv, "/stats")
	defer statsResp.Body.Close()
	var stats peers.Stats
	_ = json.NewDecoder(statsResp.Body).Decode(&stats)
	if stats.Total != 1 {
		t.Errorf("stats.Total = %d, want 1", stats.Total)
	}
}

func TestAPI_OnionMint_NotInitialized(t *testing.T) {

	d := newTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/onion/mint", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
}

type mockSender struct {
	mu   sync.Mutex
	err  error
	sent []struct {
		dest string
		env  xport.Envelope
	}
}

func (m *mockSender) Send(_ context.Context, dest string, env xport.Envelope) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, struct {
		dest string
		env  xport.Envelope
	}{dest, env})
	return nil, m.err
}

func daemonWithIdentity(t *testing.T) (*daemon, *mockSender) {
	t.Helper()
	d := newTestDaemon(t)
	d.inbox = newInbox(d.store, d.bus)
	ms := &mockSender{}
	d.worker = outbox.NewWorker(outbox.NewStore(d.store), ms, nil, &outbox.Bus{})
	return d, ms
}

func TestAPI_Send_HappyPath(t *testing.T) {
	d, _ := daemonWithIdentity(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	inv := newTestBackendInvite(t, []string{"alice-onion-56chars-yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy"})
	_, _ = d.registry.Import(inv)

	req := sendRequest{PeerID: inv.PeerID, Payload: []byte("ciphertext-bytes")}
	raw, _ := json.Marshal(req)
	resp, err := http.Post(srv.URL+"/send", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	var body sendResponse
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.EnvelopeID == "" {
		t.Errorf("response.envelope_id empty")
	}

	rows, err := d.worker.ListByState(outbox.StateEnqueued, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("outbox rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.EnvelopeID != body.EnvelopeID {
		t.Errorf("outbox row.EnvelopeID = %q, want %q", row.EnvelopeID, body.EnvelopeID)
	}

	outbound, _ := hex.DecodeString(inv.OutboundSecret)
	if err := xport.Verify(row.Envelope, outbound); err != nil {
		t.Errorf("outbox envelope fails HMAC verify: %v", err)
	}
	if row.Envelope.From != inv.MyOnionAddr {
		t.Errorf("envelope.From = %q, want %q (peer.MyOnionAddr per ADR-043)", row.Envelope.From, inv.MyOnionAddr)
	}
	wantDest := "http://alice-onion-56chars-yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy.onion"
	if row.Dest != wantDest {
		t.Errorf("dest = %q, want %q", row.Dest, wantDest)
	}
}

func TestAPI_Send_AlwaysEnqueues(t *testing.T) {

	d, _ := daemonWithIdentity(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	inv := newTestBackendInvite(t, []string{"alice-onion"})
	_, _ = d.registry.Import(inv)

	req := sendRequest{PeerID: inv.PeerID, Payload: []byte("x")}
	raw, _ := json.Marshal(req)
	resp, err := http.Post(srv.URL+"/send", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	rows, _ := d.worker.ListByState(outbox.StateEnqueued, 0, 10)
	if len(rows) != 1 {
		t.Errorf("outbox rows = %d, want 1", len(rows))
	}
}

func TestAPI_Send_UnknownPeer(t *testing.T) {
	d, _ := daemonWithIdentity(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	req := sendRequest{PeerID: "no-such-peer", Payload: []byte("x")}
	raw, _ := json.Marshal(req)
	resp, err := http.Post(srv.URL+"/send", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestAPI_Send_RetiredPeer_Returns410(t *testing.T) {
	d, _ := daemonWithIdentity(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	const onion = "shared-onion-for-retirement-56chars-xxxxxxxxxxxx"
	oldInv := newTestBackendInvite(t, []string{onion})
	if _, err := d.registry.Import(oldInv); err != nil {
		t.Fatal(err)
	}
	newInv := newTestBackendInvite(t, []string{onion})
	if _, err := d.registry.Import(newInv); err != nil {
		t.Fatal(err)
	}

	req := sendRequest{PeerID: oldInv.PeerID, Payload: []byte("ghost")}
	raw, _ := json.Marshal(req)
	resp, err := http.Post(srv.URL+"/send", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("status = %d, want 410", resp.StatusCode)
	}
	var body struct {
		Error     string `json:"error"`
		RetiredAt int64  `json:"retired_at"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Error != "peer_retired" {
		t.Errorf("body.error = %q, want peer_retired", body.Error)
	}
	if body.RetiredAt == 0 {
		t.Errorf("body.retired_at = 0, want non-zero")
	}
}

func TestAPI_ImportPeer_ReturnsRetiredList(t *testing.T) {
	d, _ := daemonWithIdentity(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	const onion = "collision-onion-56chars-yyyyyyyyyyyyyyyyyyyyyyyy"
	oldInv := newTestBackendInvite(t, []string{onion})
	if _, err := d.registry.Import(oldInv); err != nil {
		t.Fatal(err)
	}

	newInv := newTestBackendInvite(t, []string{onion})
	raw, _ := json.Marshal(newInv)
	resp, err := http.Post(srv.URL+"/peers", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	var body struct {
		Retired []string `json:"retired"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Retired) != 1 || body.Retired[0] != oldInv.PeerID {
		t.Errorf("body.retired = %v, want [%s]", body.Retired, oldInv.PeerID)
	}
}

func TestAPI_Send_MissingPeerID(t *testing.T) {
	d, _ := daemonWithIdentity(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	raw, _ := json.Marshal(sendRequest{Payload: []byte("x")})
	resp, err := http.Post(srv.URL+"/send", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestAPI_Send_PeerMissingMyOnion(t *testing.T) {
	d, _ := daemonWithIdentity(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	inv := newTestBackendInvite(t, []string{"alice-onion-bare"})
	inv.MyOnionAddr = ""
	inv.MyOnionPrivateKey = ""
	if _, err := d.registry.Import(inv); err != nil {
		t.Fatal(err)
	}

	req := sendRequest{PeerID: inv.PeerID, Payload: []byte("y")}
	raw, _ := json.Marshal(req)
	resp, err := http.Post(srv.URL+"/send", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFailedDependency {
		t.Errorf("status = %d, want 424", resp.StatusCode)
	}
}

func TestAPI_IDSStats_NoEngine(t *testing.T) {
	d := newTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	resp := get(t, srv, "/ids/stats")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 (no IDS engine)", resp.StatusCode)
	}
}

func TestAPI_IDSStats_WithEngine(t *testing.T) {
	d := newTestDaemon(t)
	d.ids = ids.New()
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	d.ids.Observe(context.Background(), ids.Event{Kind: ids.EventUnknownSource, SourceAddr: "x"})

	resp := get(t, srv, "/ids/stats")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var stats ids.Stats
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		t.Fatal(err)
	}
	if stats.EventCounts["unknown_source"] != 1 {
		t.Errorf("EventCounts = %v", stats.EventCounts)
	}
}

func TestAPI_Events_NoBus(t *testing.T) {
	d := newTestDaemon(t)
	d.bus = nil
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	resp := get(t, srv, "/events")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 (no event bus)", resp.StatusCode)
	}
}

func TestAPI_Events_StreamsNotifications(t *testing.T) {
	d := newTestDaemon(t)
	d.ids = ids.New()

	bridgeCtx, bridgeCancel := context.WithCancel(context.Background())
	defer bridgeCancel()
	go bridgeIDSToBus(bridgeCtx, d.ids, d.bus)

	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	time.Sleep(50 * time.Millisecond)
	d.ids.Observe(context.Background(), ids.Event{
		Kind: ids.EventBadMAC, SourceAddr: "alice", PeerID: "alice-id",
	})

	scanner := bufio.NewScanner(resp.Body)
	deadline := time.After(1 * time.Second)
	got := make(chan string, 1)
	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				got <- strings.TrimPrefix(line, "data: ")
				return
			}
		}
	}()
	select {
	case data := <-got:
		var n ids.Notification
		if err := json.Unmarshal([]byte(data), &n); err != nil {
			t.Errorf("unmarshal SSE payload: %v (raw: %s)", err, data)
		}
		if n.Event.Kind != ids.EventBadMAC {
			t.Errorf("event.kind = %v, want bad_mac", n.Event.Kind)
		}
		if n.Event.SourceAddr != "alice" {
			t.Errorf("event.source_addr = %q", n.Event.SourceAddr)
		}
	case <-deadline:
		t.Fatal("no SSE frame received within 1s")
	}
}

func TestAPI_Events_EmitsHeartbeat(t *testing.T) {
	old := eventsHeartbeat
	eventsHeartbeat = 50 * time.Millisecond
	defer func() { eventsHeartbeat = old }()

	d := newTestDaemon(t)
	d.ids = ids.New()
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	deadline := time.After(500 * time.Millisecond)
	got := make(chan bool, 1)
	go func() {
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), ": keepalive") {
				got <- true
				return
			}
		}
	}()
	select {
	case <-got:

	case <-deadline:
		t.Fatal("no heartbeat received within 500ms")
	}
}

func TestParseTopicFilter(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{",,", nil},
		{"peer.", []string{"peer."}},
		{"peer.,chat.", []string{"peer.", "chat."}},
		{" peer. , chat. ", []string{"peer.", "chat."}},
		{"peer.,,chat.", []string{"peer.", "chat."}},
		{"peer.presence-changed", []string{"peer.presence-changed"}},
	}
	for _, tc := range cases {
		got := parseTopicFilter(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("parseTopicFilter(%q) = %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("parseTopicFilter(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}

func TestTopicMatchesFilter(t *testing.T) {
	cases := []struct {
		topic  string
		filter []string
		want   bool
	}{
		{"peer.presence-changed", nil, true},
		{"peer.presence-changed", []string{}, true},
		{"peer.presence-changed", []string{"peer."}, true},
		{"chat.settings-changed", []string{"peer."}, false},
		{"chat.settings-changed", []string{"peer.", "chat."}, true},
		{"system.ids-event", []string{"peer.", "chat."}, false},
		{"peer.presence-changed", []string{"peer.presence-changed"}, true},
	}
	for _, tc := range cases {
		got := topicMatchesFilter(tc.topic, tc.filter)
		if got != tc.want {
			t.Errorf("topicMatchesFilter(%q, %v) = %v, want %v", tc.topic, tc.filter, got, tc.want)
		}
	}
}

func TestAPI_Events_TopicFilter(t *testing.T) {
	d := newTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/events?topic=peer.", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	time.Sleep(50 * time.Millisecond)

	d.bus.Publish(eventbus.TopicSystemIDSEvent, map[string]string{"kind": "noise"})
	d.bus.Publish(eventbus.TopicPeerPresenceChanged, peerPresenceObservation{PeerID: "alice", Source: "haoma"})

	scanner := bufio.NewScanner(resp.Body)
	deadline := time.After(1 * time.Second)
	got := make(chan string, 4)
	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event: ") {
				got <- strings.TrimPrefix(line, "event: ")
			}
		}
	}()

	select {
	case ev := <-got:
		if ev != eventbus.TopicPeerPresenceChanged {
			t.Errorf("first event = %q, want %q", ev, eventbus.TopicPeerPresenceChanged)
		}
	case <-deadline:
		t.Fatal("no peer.* event within 1s")
	}

	select {
	case ev := <-got:
		t.Errorf("unexpected post-filter event: %q (filter should have suppressed it)", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestAPI_Events_TopicFilter_MultiPrefix(t *testing.T) {
	d := newTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/events?topic=peer.,inbox.", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	time.Sleep(50 * time.Millisecond)

	d.bus.Publish(eventbus.TopicSystemIDSEvent, "x")
	d.bus.Publish(eventbus.TopicPeerPresenceChanged, peerPresenceObservation{PeerID: "alice"})
	d.bus.Publish(eventbus.TopicInboxReceived, inboxEntry{PeerID: "alice"})

	scanner := bufio.NewScanner(resp.Body)
	got := make(chan string, 4)
	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event: ") {
				got <- strings.TrimPrefix(line, "event: ")
			}
		}
	}()

	seen := map[string]bool{}
	deadline := time.After(1 * time.Second)
	for len(seen) < 2 {
		select {
		case ev := <-got:
			if ev == eventbus.TopicSystemIDSEvent {
				t.Errorf("filter leaked %q", ev)
			}
			seen[ev] = true
		case <-deadline:
			t.Fatalf("only saw %v within 1s, want peer.* + inbox.*", seen)
		}
	}
	if !seen[eventbus.TopicPeerPresenceChanged] {
		t.Errorf("missing peer.presence-changed event; saw %v", seen)
	}
	if !seen[eventbus.TopicInboxReceived] {
		t.Errorf("missing inbox.received event; saw %v", seen)
	}
}

func TestAPI_Inbox_Empty(t *testing.T) {
	d := newTestDaemon(t)
	d.inbox = newInbox(d.store, d.bus)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	resp := get(t, srv, "/inbox")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	var body struct {
		Entries []inboxEntry
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Entries) != 0 {
		t.Errorf("entries = %d, want 0", len(body.Entries))
	}
}

func TestAPI_Inbox_ListsStoredEntries(t *testing.T) {
	d := newTestDaemon(t)
	d.inbox = newInbox(d.store, d.bus)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	for i, payload := range [][]byte{[]byte("one"), []byte("two"), []byte("three")} {
		entry := inboxEntry{
			ArrivalAt: time.Now().Add(time.Duration(i) * time.Millisecond).UnixNano(),
			PeerID:    "peer-" + strings.Repeat("a", i+1),
			Envelope:  xport.Envelope{ID: "env-" + strings.Repeat("z", i+1), From: "onion", Payload: payload},
		}
		if err := d.inbox.Put(entry); err != nil {
			t.Fatal(err)
		}
	}

	resp := get(t, srv, "/inbox")
	defer resp.Body.Close()
	var body struct {
		Entries []inboxEntry
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(body.Entries))
	}
}

func TestAPI_Inbox_SinceFilter(t *testing.T) {
	d := newTestDaemon(t)
	d.inbox = newInbox(d.store, d.bus)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	base := time.Now().UnixNano()
	for i := 0; i < 5; i++ {
		_ = d.inbox.Put(inboxEntry{
			ArrivalAt: base + int64(i),
			Envelope:  xport.Envelope{ID: "e" + strings.Repeat("x", i+1)},
		})
	}

	resp := get(t, srv, "/inbox?since="+strconv.FormatInt(base+2, 10))
	defer resp.Body.Close()
	var body struct {
		Entries []inboxEntry
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Entries) != 2 {
		t.Errorf("entries = %d, want 2 (>%d)", len(body.Entries), base+2)
	}
}

func TestAPI_Inbox_Delete(t *testing.T) {
	d := newTestDaemon(t)
	d.inbox = newInbox(d.store, d.bus)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	_ = d.inbox.Put(inboxEntry{
		ArrivalAt: time.Now().UnixNano(),
		Envelope:  xport.Envelope{ID: "to-delete"},
	})

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/inbox/to-delete", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}

	n, err := d.inbox.Count()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("inbox count = %d, want 0", n)
	}
}

func TestAPI_Inbox_Delete_NotFound(t *testing.T) {
	d := newTestDaemon(t)
	d.inbox = newInbox(d.store, d.bus)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/inbox/ghost", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func readAll(resp *http.Response) (string, error) {
	buf := new(bytes.Buffer)
	_, err := buf.ReadFrom(resp.Body)
	return buf.String(), err
}
