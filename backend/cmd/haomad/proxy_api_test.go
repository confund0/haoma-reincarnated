package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"haoma/internal/proxy"
)

func newProxyTestDaemon(t *testing.T) *daemon {
	t.Helper()
	d := newTestDaemon(t)
	d.proxy = proxy.NewManager(nil)
	return d
}

func startProxyTestStreamer(t *testing.T) (int, <-chan net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	conns := make(chan net.Conn, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns <- c
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, conns
}

func TestAPI_ProxyServe_Success(t *testing.T) {
	d := newProxyTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	port, _ := startProxyTestStreamer(t)
	resp := postJSON(t, srv, "/proxy/serve", proxyServeRequest{
		Token:     "tok-serve",
		Modality:  "audio",
		LocalPort: port,
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d, want 201; body=%s", resp.StatusCode, body)
	}
	if !d.proxy.Has("tok-serve") {
		t.Fatalf("manager has no record of tok-serve after success")
	}
}

func TestAPI_ProxyServe_IdempotentSameParams(t *testing.T) {
	d := newProxyTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	body := proxyServeRequest{Token: "tok-idem", Modality: "audio", LocalPort: 12345}
	resp1 := postJSON(t, srv, "/proxy/serve", body)
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusCreated {
		t.Fatalf("first POST: status=%d, want 201", resp1.StatusCode)
	}
	resp2 := postJSON(t, srv, "/proxy/serve", body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusCreated {
		t.Fatalf("second POST (idempotent): status=%d, want 201", resp2.StatusCode)
	}
}

func TestAPI_ProxyServe_ConflictOnDifferentParams(t *testing.T) {
	d := newProxyTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	resp1 := postJSON(t, srv, "/proxy/serve", proxyServeRequest{
		Token: "tok-conf", Modality: "audio", LocalPort: 12345,
	})
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusCreated {
		t.Fatalf("first POST: status=%d, want 201", resp1.StatusCode)
	}
	resp2 := postJSON(t, srv, "/proxy/serve", proxyServeRequest{
		Token: "tok-conf", Modality: "audio", LocalPort: 12346,
	})
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("conflict POST: status=%d, want 409", resp2.StatusCode)
	}
}

func TestAPI_ProxyServe_BadModality(t *testing.T) {
	d := newProxyTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	resp := postJSON(t, srv, "/proxy/serve", proxyServeRequest{
		Token: "tok-bad", Modality: "voice", LocalPort: 12345,
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", resp.StatusCode)
	}
}

func TestAPI_ProxyServe_MissingToken(t *testing.T) {
	d := newProxyTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	resp := postJSON(t, srv, "/proxy/serve", proxyServeRequest{
		Modality: "audio", LocalPort: 12345,
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", resp.StatusCode)
	}
}

func TestAPI_ProxyServe_BadPort(t *testing.T) {
	d := newProxyTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	resp := postJSON(t, srv, "/proxy/serve", proxyServeRequest{
		Token: "tok-x", Modality: "audio", LocalPort: 0,
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", resp.StatusCode)
	}
}

func TestAPI_ProxyCancel_Success(t *testing.T) {
	d := newProxyTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	port, _ := startProxyTestStreamer(t)
	postJSON(t, srv, "/proxy/serve", proxyServeRequest{
		Token: "tok-del", Modality: "audio", LocalPort: port,
	}).Body.Close()
	if !d.proxy.Has("tok-del") {
		t.Fatalf("setup: token not registered")
	}
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/proxy/tok-del", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status=%d, want 204", resp.StatusCode)
	}
	if d.proxy.Has("tok-del") {
		t.Fatalf("token still registered after DELETE")
	}
}

func TestAPI_ProxyCancel_IdempotentMissing(t *testing.T) {
	d := newProxyTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/proxy/never-existed", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status=%d, want 204 even on missing token", resp.StatusCode)
	}
}

func TestPeerOnion_AudioStreamGet_HappyPath(t *testing.T) {
	d := newProxyTestDaemon(t)
	port, conns := startProxyTestStreamer(t)
	if err := d.proxy.RegisterServe("tok-aud", proxy.ModalityAudio, port); err != nil {
		t.Fatalf("RegisterServe: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /audio/{token}", d.handleProxyStreamGet)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bodyCh := make(chan []byte, 1)
	go func() {
		resp, err := http.Get(srv.URL + "/audio/tok-aud")
		if err != nil {
			t.Errorf("GET: %v", err)
			bodyCh <- nil
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET status=%d, want 200", resp.StatusCode)
			bodyCh <- nil
			return
		}
		b, _ := io.ReadAll(resp.Body)
		bodyCh <- b
	}()

	var conn net.Conn
	select {
	case conn = <-conns:
	case <-time.After(2 * time.Second):
		t.Fatalf("streamer did not see haomad dial")
	}
	want := []byte("audio-frames-1234")
	conn.Write(want)
	conn.Close()

	select {
	case got := <-bodyCh:
		if string(got) != string(want) {
			t.Fatalf("body mismatch: got %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("GET did not return")
	}
}

func TestPeerOnion_AudioStreamGet_UnknownToken(t *testing.T) {
	d := newProxyTestDaemon(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /audio/{token}", d.handleProxyStreamGet)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/audio/never-registered")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
}

func TestPeerOnion_VideoAndScreenRoutesAreLive(t *testing.T) {

	d := newProxyTestDaemon(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /video/{token}", d.handleProxyStreamGet)
	mux.HandleFunc("GET /screen/{token}", d.handleProxyStreamGet)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	for _, path := range []string{"/video/x", "/screen/x"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s status=%d, want 404", path, resp.StatusCode)
		}
	}
}

func TestAPI_ProxyFetch_BadModality(t *testing.T) {
	d := newProxyTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	resp := postJSON(t, srv, "/proxy/fetch", proxyFetchRequest{
		Token: "tok-x", Modality: "voice", PeerURL: "http://example.onion/audio/x", LocalPort: 12345,
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", resp.StatusCode)
	}
}

func TestAPI_ProxyFetch_MissingPeerURL(t *testing.T) {
	d := newProxyTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	resp := postJSON(t, srv, "/proxy/fetch", proxyFetchRequest{
		Token: "tok-x", Modality: "audio", LocalPort: 12345,
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", resp.StatusCode)
	}
}

func TestAPI_ProxyFetch_MissingToken(t *testing.T) {
	d := newProxyTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	resp := postJSON(t, srv, "/proxy/fetch", proxyFetchRequest{
		Modality: "audio", PeerURL: "http://example.onion/audio/x", LocalPort: 12345,
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", resp.StatusCode)
	}
}

func TestAPI_ProxyFetch_AcceptsWhenTorAbsent(t *testing.T) {
	d := newProxyTestDaemon(t)
	d.cfg.torSocks = "127.0.0.1:1"
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	port, _ := startProxyTestStreamer(t)
	resp := postJSON(t, srv, "/proxy/fetch", proxyFetchRequest{
		Token:     "tok-fetch-api",
		Modality:  "audio",
		PeerURL:   "http://example.onion/audio/x",
		LocalPort: port,
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d, want 201; body=%s", resp.StatusCode, body)
	}

	d.proxy.Cancel("tok-fetch-api")
}

func TestAPI_ProxyServe_RejectsExtraJSONFields(t *testing.T) {
	d := newProxyTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/proxy/serve", "application/json", strings.NewReader(`{"token":"x","modality":"audio","local_port":1,"surprise":true}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d, want 400 (DisallowUnknownFields); body=%s", resp.StatusCode, body)
	}
}

func TestAPI_ProxyServe_RequiresJSONContentType(t *testing.T) {
	d := newProxyTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/proxy/serve", "text/plain", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("status=%d, want 415", resp.StatusCode)
	}
}
