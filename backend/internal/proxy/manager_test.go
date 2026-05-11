package proxy_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"haoma/internal/proxy"
)

func TestParseModality(t *testing.T) {
	for _, c := range []struct {
		in   string
		want proxy.Modality
		ok   bool
	}{
		{"audio", proxy.ModalityAudio, true},
		{"video", proxy.ModalityVideo, true},
		{"screen", proxy.ModalityScreen, true},
		{"", "", false},
		{"voice", "", false},
		{"AUDIO", "", false},
	} {
		got, err := proxy.ParseModality(c.in)
		if (err == nil) != c.ok {
			t.Errorf("ParseModality(%q) err=%v, want ok=%v", c.in, err, c.ok)
		}
		if got != c.want {
			t.Errorf("ParseModality(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRegisterServe_RejectsBadInputs(t *testing.T) {
	m := proxy.NewManager(nil)
	if err := m.RegisterServe("", proxy.ModalityAudio, 12345); err == nil {
		t.Fatalf("expected error on empty token")
	}
	if err := m.RegisterServe("tok", proxy.ModalityAudio, 0); !errors.Is(err, proxy.ErrInvalidLocalPort) {
		t.Fatalf("expected ErrInvalidLocalPort on port=0, got %v", err)
	}
	if err := m.RegisterServe("tok", proxy.ModalityAudio, 70000); !errors.Is(err, proxy.ErrInvalidLocalPort) {
		t.Fatalf("expected ErrInvalidLocalPort on port=70000, got %v", err)
	}
}

func TestRegisterServe_IdempotentSameParams(t *testing.T) {
	m := proxy.NewManager(nil)
	if err := m.RegisterServe("tok-1", proxy.ModalityAudio, 12345); err != nil {
		t.Fatalf("RegisterServe: %v", err)
	}
	if err := m.RegisterServe("tok-1", proxy.ModalityAudio, 12345); err != nil {
		t.Fatalf("re-RegisterServe with same params: %v", err)
	}
}

func TestRegisterServe_ConflictOnDifferentParams(t *testing.T) {
	m := proxy.NewManager(nil)
	if err := m.RegisterServe("tok-1", proxy.ModalityAudio, 12345); err != nil {
		t.Fatalf("RegisterServe: %v", err)
	}
	if err := m.RegisterServe("tok-1", proxy.ModalityAudio, 12346); !errors.Is(err, proxy.ErrTokenInUse) {
		t.Fatalf("expected ErrTokenInUse on port collision, got %v", err)
	}
	if err := m.RegisterServe("tok-1", proxy.ModalityVideo, 12345); !errors.Is(err, proxy.ErrTokenInUse) {
		t.Fatalf("expected ErrTokenInUse on modality collision, got %v", err)
	}
}

func TestHandleServe_UnknownTokenReturnsFalse(t *testing.T) {
	m := proxy.NewManager(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/audio/never-registered", nil)
	if handled := m.HandleServe(rec, req, "never-registered"); handled {
		t.Fatalf("HandleServe returned true for unknown token")
	}
}

type fakeStreamer struct {
	port    int
	ln      net.Listener
	conn    chan net.Conn
	closeMu sync.Once
}

func startFakeStreamer(t *testing.T) *fakeStreamer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	h := &fakeStreamer{
		port: ln.Addr().(*net.TCPAddr).Port,
		ln:   ln,
		conn: make(chan net.Conn, 1),
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			h.conn <- c
		}
	}()
	t.Cleanup(h.Close)
	return h
}

func (h *fakeStreamer) Close() {
	h.closeMu.Do(func() {
		_ = h.ln.Close()
	})
}

func TestHandleServe_HappyPath(t *testing.T) {
	streamer := startFakeStreamer(t)
	m := proxy.NewManager(nil)
	if err := m.RegisterServe("tok-good", proxy.ModalityAudio, streamer.port); err != nil {
		t.Fatalf("RegisterServe: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !m.HandleServe(w, r, "tok-good") {
			t.Errorf("HandleServe returned false on registered token")
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	bodyCh := make(chan []byte, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := http.Get(srv.URL + "/audio/tok-good")
		if err != nil {
			errCh <- err
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			errCh <- fmt.Errorf("status %d", resp.StatusCode)
			return
		}
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			errCh <- err
			return
		}
		bodyCh <- b
	}()

	var conn net.Conn
	select {
	case conn = <-streamer.conn:
	case <-time.After(2 * time.Second):
		t.Fatalf("streamer did not see haomad dial")
	}
	want := []byte("frame-A|frame-B|frame-C")
	if _, err := conn.Write(want); err != nil {
		t.Fatalf("streamer write: %v", err)
	}
	conn.Close()

	select {
	case got := <-bodyCh:
		if string(got) != string(want) {
			t.Fatalf("body mismatch: got %q, want %q", got, want)
		}
	case err := <-errCh:
		t.Fatalf("GET failed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatalf("GET never completed")
	}
}

func TestHandleServe_StreamerUnavailableReturns502(t *testing.T) {
	m := proxy.NewManager(nil)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	deadPort := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	if err := m.RegisterServe("tok-dead", proxy.ModalityAudio, deadPort); err != nil {
		t.Fatalf("RegisterServe: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.HandleServe(w, r, "tok-dead")
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/audio/tok-dead")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status: got %d, want 502", resp.StatusCode)
	}
}

func TestCancel_DropsRegistrationAndClosesActiveServe(t *testing.T) {
	streamer := startFakeStreamer(t)
	m := proxy.NewManager(nil)
	if err := m.RegisterServe("tok-cancel", proxy.ModalityAudio, streamer.port); err != nil {
		t.Fatalf("RegisterServe: %v", err)
	}

	getDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !m.HandleServe(w, r, "tok-cancel") {
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	go func() {
		resp, err := http.Get(srv.URL + "/audio/tok-cancel")
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		close(getDone)
	}()

	select {
	case <-streamer.conn:
	case <-time.After(2 * time.Second):
		t.Fatalf("streamer did not see dial")
	}

	if !m.Cancel("tok-cancel") {
		t.Fatalf("Cancel returned false on registered token")
	}
	if m.Cancel("tok-cancel") {
		t.Fatalf("second Cancel returned true (not idempotent)")
	}

	select {
	case <-getDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("GET did not unblock after Cancel")
	}

	if m.Has("tok-cancel") {
		t.Fatalf("token still registered after Cancel")
	}

	resp, err := http.Get(srv.URL + "/audio/tok-cancel")
	if err != nil {
		t.Fatalf("GET2: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("post-cancel GET: status %d, want 404", resp.StatusCode)
	}
}

func TestStartFetch_HappyPath(t *testing.T) {
	want := []byte("audio-frame-1|audio-frame-2|audio-frame-3")

	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		w.Write(want)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer peer.Close()

	streamer := startFakeStreamer(t)

	m := proxy.NewManager(nil)
	if err := m.StartFetch(context.Background(), "tok-fetch", proxy.ModalityAudio, peer.URL+"/audio/x", streamer.port, peer.Client()); err != nil {
		t.Fatalf("StartFetch: %v", err)
	}

	var conn net.Conn
	select {
	case conn = <-streamer.conn:
	case <-time.After(2 * time.Second):
		t.Fatalf("streamer did not see haomad dial")
	}
	defer conn.Close()

	got := make([]byte, len(want))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("streamer read: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("payload mismatch: got %q, want %q", got, want)
	}
}

func TestStartFetch_IdempotentSameParams(t *testing.T) {
	streamer := startFakeStreamer(t)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		<-r.Context().Done()
	}))
	defer peer.Close()

	m := proxy.NewManager(nil)
	hc := peer.Client()
	if err := m.StartFetch(context.Background(), "tok-idem", proxy.ModalityAudio, peer.URL+"/x", streamer.port, hc); err != nil {
		t.Fatalf("StartFetch: %v", err)
	}
	if err := m.StartFetch(context.Background(), "tok-idem", proxy.ModalityAudio, peer.URL+"/x", streamer.port, hc); err != nil {
		t.Fatalf("re-StartFetch with same params: %v", err)
	}

	if err := m.StartFetch(context.Background(), "tok-idem", proxy.ModalityAudio, peer.URL+"/x", streamer.port+1, hc); !errors.Is(err, proxy.ErrTokenInUse) {
		t.Fatalf("expected ErrTokenInUse on port collision, got %v", err)
	}
	m.Cancel("tok-idem")
}

func TestStartFetch_CancelTerminatesGoroutine(t *testing.T) {
	streamer := startFakeStreamer(t)

	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		w.Write([]byte("hello"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer peer.Close()

	m := proxy.NewManager(nil)
	if err := m.StartFetch(context.Background(), "tok-cancel-fetch", proxy.ModalityAudio, peer.URL+"/x", streamer.port, peer.Client()); err != nil {
		t.Fatalf("StartFetch: %v", err)
	}

	var conn net.Conn
	select {
	case conn = <-streamer.conn:
	case <-time.After(2 * time.Second):
		t.Fatalf("streamer did not see dial")
	}
	defer conn.Close()
	buf := make([]byte, 5)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("streamer read: %v", err)
	}

	if !m.Cancel("tok-cancel-fetch") {
		t.Fatalf("Cancel returned false")
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(buf); err == nil {
		t.Fatalf("streamer read succeeded post-cancel; want EOF / closed")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !m.Has("tok-cancel-fetch") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("entry did not clear from registry after fetch goroutine exit")
}

func TestStartFetch_ParentCancelTerminatesGoroutine(t *testing.T) {
	streamer := startFakeStreamer(t)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer peer.Close()

	m := proxy.NewManager(nil)
	ctx, cancel := context.WithCancel(context.Background())
	if err := m.StartFetch(ctx, "tok-parent", proxy.ModalityAudio, peer.URL+"/x", streamer.port, peer.Client()); err != nil {
		t.Fatalf("StartFetch: %v", err)
	}
	select {
	case <-streamer.conn:
	case <-time.After(2 * time.Second):
		t.Fatalf("streamer did not see dial")
	}

	cancel()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !m.Has("tok-parent") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("entry did not clear after parent cancel")
}

func TestStartFetch_RejectsBadInputs(t *testing.T) {
	m := proxy.NewManager(nil)
	hc := http.DefaultClient
	if err := m.StartFetch(context.Background(), "", proxy.ModalityAudio, "http://x", 1234, hc); err == nil {
		t.Fatalf("expected error on empty token")
	}
	if err := m.StartFetch(context.Background(), "t", proxy.ModalityAudio, "http://x", 0, hc); !errors.Is(err, proxy.ErrInvalidLocalPort) {
		t.Fatalf("expected ErrInvalidLocalPort, got %v", err)
	}
	if err := m.StartFetch(context.Background(), "t", proxy.ModalityAudio, "", 1234, hc); err == nil {
		t.Fatalf("expected error on empty peer_url")
	}
	if err := m.StartFetch(context.Background(), "t", proxy.ModalityAudio, "http://x", 1234, nil); err == nil {
		t.Fatalf("expected error on nil http client")
	}
}

func TestHandleServe_TokenLocked(t *testing.T) {
	streamer := startFakeStreamer(t)
	m := proxy.NewManager(nil)
	if err := m.RegisterServe("tok-locked", proxy.ModalityAudio, streamer.port); err != nil {
		t.Fatalf("RegisterServe: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !m.HandleServe(w, r, "tok-locked") {
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	first := make(chan *http.Response, 1)
	go func() {
		resp, err := http.Get(srv.URL + "/audio/tok-locked")
		if err != nil {
			t.Errorf("first GET: %v", err)
			first <- nil
			return
		}
		first <- resp
	}()
	var connA net.Conn
	select {
	case connA = <-streamer.conn:
	case <-time.After(2 * time.Second):
		t.Fatalf("streamer did not see first dial")
	}
	defer connA.Close()

	var firstResp *http.Response
	select {
	case firstResp = <-first:
		if firstResp == nil {
			return
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("first GET did not return headers")
	}
	defer firstResp.Body.Close()
	if firstResp.StatusCode != http.StatusOK {
		t.Fatalf("first GET status = %d, want 200", firstResp.StatusCode)
	}

	streamerDialCountBefore := len(streamer.conn)
	resp2, err := http.Get(srv.URL + "/audio/tok-locked")
	if err != nil {
		t.Fatalf("second GET: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("second GET status = %d, want 409", resp2.StatusCode)
	}

	if len(streamer.conn) != streamerDialCountBefore {
		t.Fatalf("streamer saw a second dial despite lock; channel size %d", len(streamer.conn))
	}

	connA.Close()
	if _, err := io.Copy(io.Discard, firstResp.Body); err != nil {
		t.Fatalf("drain first body: %v", err)
	}
	firstResp.Body.Close()

	resp3Done := make(chan *http.Response, 1)
	go func() {
		r, err := http.Get(srv.URL + "/audio/tok-locked")
		if err != nil {
			t.Errorf("reconnect GET: %v", err)
			resp3Done <- nil
			return
		}
		resp3Done <- r
	}()
	select {
	case c := <-streamer.conn:
		c.Close()
	case <-time.After(2 * time.Second):
		t.Fatalf("streamer did not see reconnect dial")
	}
	if r := <-resp3Done; r != nil {
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		if r.StatusCode != http.StatusOK {
			t.Fatalf("reconnect GET status = %d, want 200", r.StatusCode)
		}
	}
}

func TestEndToEnd_ServeOnSideA_FetchOnSideB(t *testing.T) {

	streamerA := startFakeStreamer(t)
	managerA := proxy.NewManager(nil)
	const token = "shared-capability-token-1"
	if err := managerA.RegisterServe(token, proxy.ModalityAudio, streamerA.port); err != nil {
		t.Fatalf("RegisterServe: %v", err)
	}
	peerOnion := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		tok := strings.TrimPrefix(r.URL.Path, "/audio/")
		if !managerA.HandleServe(w, r, tok) {
			http.NotFound(w, r)
		}
	}))
	defer peerOnion.Close()

	streamerB := startFakeStreamer(t)
	managerB := proxy.NewManager(nil)
	if err := managerB.StartFetch(context.Background(), token, proxy.ModalityAudio, peerOnion.URL+"/audio/"+token, streamerB.port, peerOnion.Client()); err != nil {
		t.Fatalf("StartFetch: %v", err)
	}

	var connA net.Conn
	select {
	case connA = <-streamerA.conn:
	case <-time.After(2 * time.Second):
		t.Fatalf("streamerA did not see dial")
	}
	want := []byte("opaque-AEAD-frame-stream-bytes")
	if _, err := connA.Write(want); err != nil {
		t.Fatalf("streamerA write: %v", err)
	}
	connA.Close()

	var connB net.Conn
	select {
	case connB = <-streamerB.conn:
	case <-time.After(2 * time.Second):
		t.Fatalf("streamerB did not see dial")
	}
	defer connB.Close()
	got := make([]byte, len(want))
	if _, err := io.ReadFull(connB, got); err != nil {
		t.Fatalf("streamerB read: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("end-to-end mismatch: got %q, want %q", got, want)
	}
}
