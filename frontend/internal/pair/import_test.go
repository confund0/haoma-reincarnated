package pair

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.mau.fi/libsignal/protocol"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/signal"
	"haoma-frontend/internal/store"
)

func buildFullInvite(t *testing.T) (*Invite, *signal.State) {
	t.Helper()
	state, err := signal.Bootstrap(3)
	if err != nil {
		t.Fatal(err)
	}
	inv, _, err := Build(state, state, []string{"remote-onion"}, "remote-peer", nil)
	if err != nil {
		t.Fatal(err)
	}
	return inv, state
}

func freshLocalKeys(t *testing.T) *MyKeys {
	t.Helper()
	state, err := signal.Bootstrap(3)
	if err != nil {
		t.Fatal(err)
	}
	_, mine, err := Build(state, state, []string{"local-onion"}, "local", nil)
	if err != nil {
		t.Fatal(err)
	}
	return mine
}

func newLocalStores(t *testing.T) *signal.Stores {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Lock() })
	state, _, err := signal.LoadOrBootstrap(st, 3)
	if err != nil {
		t.Fatal(err)
	}
	return signal.NewStores(st, state)
}

type captured struct {
	called int
	body   []byte
}

func stubBackend(t *testing.T, status int, c *captured) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/peers" {
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, 404)
			return
		}
		c.called++
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			http.Error(w, "want application/json, got "+ct, 415)
			return
		}
		c.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestImport_HappyPath_PostsPeer_SavesIdentity_EstablishesSession(t *testing.T) {
	stores := newLocalStores(t)
	var cap captured
	backendURL := stubBackend(t, http.StatusCreated, &cap)

	inv, _ := buildFullInvite(t)
	mine := freshLocalKeys(t)

	err := Import(context.Background(), stores, backendapi.New(backendURL, "", nil), inv, mine, fakeMintedOnion())
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	if cap.called != 1 {
		t.Errorf("backend called %d times, want 1", cap.called)
	}
	var got BackendInvite
	if err := json.Unmarshal(cap.body, &got); err != nil {
		t.Fatal(err)
	}
	if got.PeerID != inv.PeerID {
		t.Errorf("backend POST peer_id = %q, want %q", got.PeerID, inv.PeerID)
	}
	if got.InboundSecret != inv.Secret {
		t.Errorf("backend POST inbound_secret = %q, want %q (= partner's outbound)", got.InboundSecret, inv.Secret)
	}
	if got.OutboundSecret != hexEncode(mine.OutboundSecret) {
		t.Errorf("backend POST outbound_secret drift")
	}
	if !stringSliceEqual(got.Addresses, inv.Addresses) {
		t.Errorf("backend addresses = %v, want %v", got.Addresses, inv.Addresses)
	}

	addr := protocol.NewSignalAddress(inv.PeerID, DeviceID)
	remoteKey, err := stores.GetRemoteIdentity(addr)
	if err != nil {
		t.Fatalf("GetRemoteIdentity: %v", err)
	}
	gotHex := hexEncode(remoteKey.Serialize())
	if gotHex != inv.Frontend.Signal.IdentityKey {
		t.Errorf("stored identity key drift:\n got  %s\n want %s", gotHex, inv.Frontend.Signal.IdentityKey)
	}

	contains, err := stores.ContainsSession(context.Background(), addr)
	if err != nil {
		t.Fatal(err)
	}
	if !contains {
		t.Error("no session record after Import; ProcessBundle didn't land")
	}
}

func TestImport_ValidateFails_NoBackendCall(t *testing.T) {
	stores := newLocalStores(t)
	var cap captured
	backendURL := stubBackend(t, http.StatusCreated, &cap)

	inv, _ := buildFullInvite(t)
	inv.Secret = "not-hex"

	err := Import(context.Background(), stores, backendapi.New(backendURL, "", nil), inv, freshLocalKeys(t), fakeMintedOnion())
	if err == nil {
		t.Fatal("expected Import to fail on invalid invite")
	}
	if cap.called != 0 {
		t.Errorf("backend was called despite Validate failure (%d times)", cap.called)
	}
}

func TestImport_NilMyKeys_Errors(t *testing.T) {
	stores := newLocalStores(t)
	var cap captured
	backendURL := stubBackend(t, http.StatusCreated, &cap)

	inv, _ := buildFullInvite(t)
	if err := Import(context.Background(), stores, backendapi.New(backendURL, "", nil), inv, nil, fakeMintedOnion()); err == nil {
		t.Fatal("expected error on nil MyKeys")
	}
	if cap.called != 0 {
		t.Errorf("backend called with nil MyKeys")
	}
}

func TestImport_BackendErrors_NoSessionCreated(t *testing.T) {
	stores := newLocalStores(t)
	var cap captured
	backendURL := stubBackend(t, http.StatusInternalServerError, &cap)

	inv, _ := buildFullInvite(t)

	err := Import(context.Background(), stores, backendapi.New(backendURL, "", nil), inv, freshLocalKeys(t), fakeMintedOnion())
	if err == nil {
		t.Fatal("expected Import to fail on backend 500")
	}
	if !strings.Contains(err.Error(), "backend register") {
		t.Errorf("err lacks context: %v", err)
	}
	addr := protocol.NewSignalAddress(inv.PeerID, DeviceID)
	contains, _ := stores.ContainsSession(context.Background(), addr)
	if contains {
		t.Error("session created despite backend failure")
	}
}

func fakeMintedOnion() backendapi.MintedOnion {
	return backendapi.MintedOnion{
		Address:    "fakeonion4567890123456789012345678901234567890123456789",
		PrivateKey: "ZmFrZS1iYXNlNjQta2V5",
	}
}

func hexEncode(b []byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, x := range b {
		out = append(out, hex[x>>4], hex[x&0x0f])
	}
	return string(out)
}

func TestBackendInvite_MatchesHaomadShape(t *testing.T) {
	be := BackendInvite{
		PeerID:            "deadbeef",
		Addresses:         []string{"x"},
		InboundSecret:     "abcd",
		OutboundSecret:    "ef01",
		MyOnionAddr:       "myonion",
		MyOnionPrivateKey: "Zg==",
	}
	b, _ := json.Marshal(be)
	raw := map[string]any{}
	_ = json.Unmarshal(b, &raw)
	for _, key := range []string{"peer_id", "addresses", "inbound_secret", "outbound_secret", "my_onion_addr", "my_onion_private_key"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("BackendInvite JSON missing expected key %q", key)
		}
	}
}

var _ = bytes.NewReader
