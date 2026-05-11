package backendapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeBackend struct {
	*httptest.Server

	responses map[string]http.HandlerFunc
}

func newFakeBackend(t *testing.T) *fakeBackend {
	t.Helper()
	fb := &fakeBackend{responses: map[string]http.HandlerFunc{}}
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if h := fb.responses[r.Method+" "+r.URL.Path]; h != nil {
			h(w, r)
			return
		}
		http.Error(w, "fake: no stub for "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	})
	fb.Server = httptest.NewServer(mux)
	t.Cleanup(fb.Close)
	return fb
}

func (fb *fakeBackend) on(method, path string, h http.HandlerFunc) {
	fb.responses[method+" "+path] = h
}

func jsonReply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func TestClient_SendsBearerToken(t *testing.T) {
	const wantToken = "haomad-test-token-abc123"

	type seen struct{ method, auth string }
	var got []seen

	fb := newFakeBackend(t)
	record := func(method string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			got = append(got, seen{method: method, auth: r.Header.Get("Authorization")})
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "{}")
		}
	}
	fb.on("GET", "/health", record("GET /health"))
	fb.on("DELETE", "/inbox/abc", func(w http.ResponseWriter, r *http.Request) {
		got = append(got, seen{method: "DELETE /inbox", auth: r.Header.Get("Authorization")})
		w.WriteHeader(http.StatusNoContent)
	})

	c := New(fb.URL, wantToken, nil)
	if _, err := c.Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if err := c.DeleteInboxEntry(context.Background(), "abc"); err != nil {
		t.Fatalf("DeleteInboxEntry: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("got %d requests, want 2", len(got))
	}
	for _, g := range got {
		if g.auth != "Bearer "+wantToken {
			t.Errorf("%s: Authorization = %q, want %q", g.method, g.auth, "Bearer "+wantToken)
		}
	}
}

func TestClient_OmitsBearerWhenTokenEmpty(t *testing.T) {
	var sawHeader string
	fb := newFakeBackend(t)
	fb.on("GET", "/health", func(w http.ResponseWriter, r *http.Request) {
		sawHeader = r.Header.Get("Authorization")
		jsonReply(w, http.StatusOK, HealthResponse{Status: "ok"})
	})
	if _, err := New(fb.URL, "", nil).Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if sawHeader != "" {
		t.Errorf("Authorization = %q, want empty (no header)", sawHeader)
	}
}

func TestClient_Health(t *testing.T) {
	fb := newFakeBackend(t)
	fb.on("GET", "/health", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, 200, HealthResponse{Status: "ok", Version: "test-1"})
	})
	got, err := New(fb.URL, "", nil).Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "ok" || got.Version != "test-1" {
		t.Errorf("got %+v", got)
	}
}

func TestClient_Health_NotOK_Errors(t *testing.T) {
	fb := newFakeBackend(t)
	fb.on("GET", "/health", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "database offline", http.StatusServiceUnavailable)
	})
	_, err := New(fb.URL, "", nil).Health(context.Background())
	if err == nil {
		t.Fatal("expected error on 503")
	}
	if !strings.Contains(err.Error(), "database offline") {
		t.Errorf("err should include body: %v", err)
	}
}

func TestClient_MintOnion(t *testing.T) {
	fb := newFakeBackend(t)
	fb.on("POST", "/onion/mint", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, 200, MintedOnion{
			Address:    "freshonion4567890123456789012345678901234567890123456789",
			PrivateKey: "ZmFrZS1iYXNlNjQta2V5",
		})
	})
	got, err := New(fb.URL, "", nil).MintOnion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Address == "" || got.PrivateKey == "" {
		t.Errorf("got %+v", got)
	}
}

func TestClient_Peers_HappyPath(t *testing.T) {
	fb := newFakeBackend(t)
	fb.on("GET", "/peers", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, 200, PeersResponse{Peers: []Peer{
			{ID: "p1", KnownAddresses: []string{"a1"}},
			{ID: "p2", KnownAddresses: []string{"b1", "b2"}, LastPassiveAt: 123},
		}})
	})
	got, err := New(fb.URL, "", nil).Peers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Peers) != 2 || got.Peers[0].ID != "p1" || got.Peers[1].LastPassiveAt != 123 {
		t.Errorf("got %+v", got)
	}
}

func TestClient_Peer_404_Errors(t *testing.T) {
	fb := newFakeBackend(t)
	fb.on("GET", "/peers/missing", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	_, err := New(fb.URL, "", nil).Peer(context.Background(), "missing")
	if err == nil {
		t.Error("expected error on 404")
	}
}

func TestClient_Inbox_QueryParams(t *testing.T) {
	fb := newFakeBackend(t)
	var capturedQuery string
	fb.on("GET", "/inbox", func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.RawQuery
		jsonReply(w, 200, InboxResponse{Entries: nil})
	})
	_, err := New(fb.URL, "", nil).Inbox(context.Background(), 1_700_000_000_000_000_000, 50)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(capturedQuery, "since=1700000000000000000") {
		t.Errorf("query missing since: %q", capturedQuery)
	}
	if !strings.Contains(capturedQuery, "limit=50") {
		t.Errorf("query missing limit: %q", capturedQuery)
	}
}

func TestClient_Inbox_NoArgsMeansNoQuery(t *testing.T) {
	fb := newFakeBackend(t)
	var capturedQuery string
	fb.on("GET", "/inbox", func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.RawQuery
		jsonReply(w, 200, InboxResponse{})
	})
	_, err := New(fb.URL, "", nil).Inbox(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if capturedQuery != "" {
		t.Errorf("expected empty query; got %q", capturedQuery)
	}
}

func TestClient_Inbox_DecodesEntries(t *testing.T) {
	fb := newFakeBackend(t)
	fb.on("GET", "/inbox", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, 200, InboxResponse{Entries: []InboxEntry{{
			ArrivalAt: 42,
			PeerID:    "p1",
			Envelope:  RawEnvelope{ID: "env-1", Kind: "text", Payload: []byte("hello")},
		}}})
	})
	got, err := New(fb.URL, "", nil).Inbox(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(got.Entries))
	}
	e := got.Entries[0]
	if e.Envelope.Kind != "text" || string(e.Envelope.Payload) != "hello" {
		t.Errorf("entry = %+v", e)
	}
}

func TestClient_DeleteInboxEntry_204(t *testing.T) {
	fb := newFakeBackend(t)
	fb.on("DELETE", "/inbox/abc", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	if err := New(fb.URL, "", nil).DeleteInboxEntry(context.Background(), "abc"); err != nil {
		t.Errorf("204 should not error: %v", err)
	}
}

func TestClient_DeleteInboxEntry_404_TreatedAsSuccess(t *testing.T) {
	fb := newFakeBackend(t)
	fb.on("DELETE", "/inbox/gone", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})

	if err := New(fb.URL, "", nil).DeleteInboxEntry(context.Background(), "gone"); err != nil {
		t.Errorf("404 should be treated as success: %v", err)
	}
}

func TestClient_DeleteInboxEntry_500_Errors(t *testing.T) {
	fb := newFakeBackend(t)
	fb.on("DELETE", "/inbox/broken", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "kaboom", http.StatusInternalServerError)
	})
	err := New(fb.URL, "", nil).DeleteInboxEntry(context.Background(), "broken")
	if err == nil {
		t.Error("expected error on 500")
	}
}

func TestClient_Send_RoundTrip(t *testing.T) {
	fb := newFakeBackend(t)
	var captured SendRequest
	fb.on("POST", "/send", func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			http.Error(w, "want application/json", http.StatusUnsupportedMediaType)
			return
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		jsonReply(w, http.StatusAccepted, SendResponse{EnvelopeID: "e1"})
	})
	got, err := New(fb.URL, "", nil).Send(context.Background(), SendRequest{PeerID: "p1", Payload: []byte("hi")})
	if err != nil {
		t.Fatal(err)
	}
	if got.EnvelopeID != "e1" {
		t.Errorf("resp = %+v", got)
	}
	if captured.PeerID != "p1" || string(captured.Payload) != "hi" {
		t.Errorf("server saw %+v", captured)
	}
}

func TestClient_IDSStats(t *testing.T) {
	fb := newFakeBackend(t)
	fb.on("GET", "/ids/stats", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, 200, IDSStats{
			EventCounts:  map[string]int64{"bad_mac": 5},
			ActionCounts: map[string]int64{"alert": 1},
		})
	})
	got, err := New(fb.URL, "", nil).IDSStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.EventCounts["bad_mac"] != 5 {
		t.Errorf("got %+v", got)
	}
}

func TestClient_ContextCancel_Aborts(t *testing.T) {
	fb := newFakeBackend(t)
	done := make(chan struct{})
	fb.on("GET", "/peers", func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		close(done)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := New(fb.URL, "", nil).Peers(ctx)
	if err == nil {
		t.Fatal("expected context error")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("handler never saw request cancel")
	}
}

func TestClient_ProxyServe_HappyPath(t *testing.T) {
	fb := newFakeBackend(t)
	var got ProxyServeRequest
	fb.on("POST", "/proxy/serve", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusCreated)
	})
	err := New(fb.URL, "", nil).ProxyServe(context.Background(), ProxyServeRequest{
		Token: "tok-1", Modality: "audio", LocalPort: 12345,
	})
	if err != nil {
		t.Fatalf("ProxyServe: %v", err)
	}
	if got.Token != "tok-1" || got.Modality != "audio" || got.LocalPort != 12345 {
		t.Errorf("body = %+v", got)
	}
}

func TestClient_ProxyServe_ConflictMaps(t *testing.T) {
	fb := newFakeBackend(t)
	fb.on("POST", "/proxy/serve", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "in use", http.StatusConflict)
	})
	err := New(fb.URL, "", nil).ProxyServe(context.Background(), ProxyServeRequest{
		Token: "x", Modality: "audio", LocalPort: 1,
	})
	if err != ErrProxyTokenInUse {
		t.Fatalf("err = %v, want ErrProxyTokenInUse", err)
	}
}

func TestClient_ProxyFetch_HappyPath(t *testing.T) {
	fb := newFakeBackend(t)
	var got ProxyFetchRequest
	fb.on("POST", "/proxy/fetch", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusCreated)
	})
	err := New(fb.URL, "", nil).ProxyFetch(context.Background(), ProxyFetchRequest{
		Token: "tok-2", Modality: "audio", PeerURL: "http://x.onion/audio/tok-2", LocalPort: 9999,
	})
	if err != nil {
		t.Fatalf("ProxyFetch: %v", err)
	}
	if got.Token != "tok-2" || got.PeerURL != "http://x.onion/audio/tok-2" {
		t.Errorf("body = %+v", got)
	}
}

func TestClient_ProxyFetch_ConflictMaps(t *testing.T) {
	fb := newFakeBackend(t)
	fb.on("POST", "/proxy/fetch", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "in use", http.StatusConflict)
	})
	err := New(fb.URL, "", nil).ProxyFetch(context.Background(), ProxyFetchRequest{
		Token: "x", Modality: "audio", PeerURL: "http://x.onion/audio/x", LocalPort: 1,
	})
	if err != ErrProxyTokenInUse {
		t.Fatalf("err = %v, want ErrProxyTokenInUse", err)
	}
}

func TestClient_ProxyCancel_HappyPath(t *testing.T) {
	fb := newFakeBackend(t)
	var sawPath string
	fb.on("DELETE", "/proxy/tok-3", func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})
	if err := New(fb.URL, "", nil).ProxyCancel(context.Background(), "tok-3"); err != nil {
		t.Fatalf("ProxyCancel: %v", err)
	}
	if sawPath != "/proxy/tok-3" {
		t.Errorf("path = %q, want /proxy/tok-3", sawPath)
	}
}

func TestClient_ProxyCancel_RejectsEmptyToken(t *testing.T) {
	if err := New("http://nowhere", "", nil).ProxyCancel(context.Background(), ""); err == nil {
		t.Fatal("expected error on empty token")
	}
}
