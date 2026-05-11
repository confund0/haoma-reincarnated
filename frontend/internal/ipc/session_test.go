package ipc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func spinServerReturningServer(t *testing.T) (addr, certPEMPath, token string, srv *Server, cleanup func()) {
	t.Helper()
	dir := t.TempDir()
	tlsCfg, err := LoadOrCreateTLS(dir)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := LoadOrCreateToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv = NewServer(tok)
	httpSrv := &http.Server{Handler: srv.Handler(), TLSConfig: tlsCfg}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		tlsLn := tls.NewListener(ln, tlsCfg)
		_ = httpSrv.Serve(tlsLn)
	}()
	cleanup = func() {
		shutdownCtx, c := context.WithTimeout(context.Background(), time.Second)
		defer c()
		_ = httpSrv.Shutdown(shutdownCtx)
	}
	return ln.Addr().String(), filepath.Join(dir, certFilename), tok, srv, cleanup
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", timeout)
}

func spinServer(t *testing.T, onSession func(context.Context, *Session)) (addr, certPEMPath, token string, cleanup func()) {
	t.Helper()
	dir := t.TempDir()
	tlsCfg, err := LoadOrCreateTLS(dir)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := LoadOrCreateToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(tok)
	srv.OnSession = onSession
	httpSrv := &http.Server{Handler: srv.Handler(), TLSConfig: tlsCfg}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		tlsLn := tls.NewListener(ln, tlsCfg)
		_ = httpSrv.Serve(tlsLn)
	}()
	cleanup = func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}
	return ln.Addr().String(), filepath.Join(dir, certFilename), tok, cleanup
}

func dialClient(t *testing.T, ctx context.Context, addr, certPEMPath, token string) *websocket.Conn {
	t.Helper()
	certPEM, err := os.ReadFile(certPEMPath)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatal("parse cert pem")
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
	return conn
}

func TestSession_HelloWelcomeRoundTrip(t *testing.T) {
	addr, certPEM, token, cleanup := spinServer(t, nil)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := dialClient(t, ctx, addr, certPEM, token)
	defer conn.Close(websocket.StatusNormalClosure, "")

	hello, err := NewFrame(FrameHello, "h1", HelloPayload{ClientName: "test", ClientVersion: "0"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Encode(hello)
	if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("write hello: %v", err)
	}

	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read welcome: %v", err)
	}
	f, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if f.Type != FrameWelcome {
		t.Errorf("type = %q, want welcome", f.Type)
	}
	if f.ID != "h1" {
		t.Errorf("correlation id = %q, want h1", f.ID)
	}
	var p WelcomePayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.ProtocolVersion != ProtocolVersion {
		t.Errorf("protocol version = %d, want %d", p.ProtocolVersion, ProtocolVersion)
	}
}

func TestSession_RejectsMissingBearerToken(t *testing.T) {
	addr, certPEM, _, cleanup := spinServer(t, nil)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	certPEMBytes, _ := os.ReadFile(certPEM)
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEMBytes)
	tlsCfg := &tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS13}
	httpClient := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}}

	_, resp, err := websocket.Dial(ctx, "wss://"+addr+"/ws", &websocket.DialOptions{
		HTTPClient: httpClient,
	})
	if err == nil {
		t.Fatal("expected dial to fail on missing bearer")
	}

	if resp != nil && resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestSession_RejectsWrongBearerToken(t *testing.T) {
	addr, certPEM, _, cleanup := spinServer(t, nil)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	certPEMBytes, _ := os.ReadFile(certPEM)
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEMBytes)
	tlsCfg := &tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS13}
	httpClient := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}}
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer wrong-token")

	_, resp, err := websocket.Dial(ctx, "wss://"+addr+"/ws", &websocket.DialOptions{
		HTTPClient: httpClient,
		HTTPHeader: hdr,
	})
	if err == nil {
		t.Fatal("expected dial to fail on wrong bearer")
	}
	if resp != nil && resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestSession_PingPong(t *testing.T) {
	addr, certPEM, token, cleanup := spinServer(t, nil)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := dialClient(t, ctx, addr, certPEM, token)
	defer conn.Close(websocket.StatusNormalClosure, "")

	hello, _ := NewFrame(FrameHello, "h1", HelloPayload{ClientName: "test"})
	b, _ := Encode(hello)
	_ = conn.Write(ctx, websocket.MessageText, b)
	_, _, _ = conn.Read(ctx)

	ping, _ := NewFrame(FramePing, "p1", nil)
	b, _ = Encode(ping)
	if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if f.Type != FramePong {
		t.Errorf("type = %q, want pong", f.Type)
	}
	if f.ID != "p1" {
		t.Errorf("correlation id = %q, want p1", f.ID)
	}
}

func TestServer_Broadcast_FansOutToAllSessions(t *testing.T) {

	addr, certPEM, token, srv, cleanup := spinServerReturningServer(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const clients = 3
	conns := make([]*websocket.Conn, clients)
	for i := 0; i < clients; i++ {
		conns[i] = dialClient(t, ctx, addr, certPEM, token)
		defer conns[i].Close(websocket.StatusNormalClosure, "")

		hello, _ := NewFrame(FrameHello, fmt.Sprintf("h-%d", i), HelloPayload{ClientName: "test"})
		b, _ := Encode(hello)
		_ = conns[i].Write(ctx, websocket.MessageText, b)
		_, _, _ = conns[i].Read(ctx)
	}

	waitFor(t, 2*time.Second, func() bool { return srv.SessionCount() == clients })

	pushF, _ := NewFrame(FrameStatusEvent, "", StatusEventPayload{
		Event: json.RawMessage(`{"kind":"bad_mac"}`),
	})
	srv.Broadcast(pushF)

	for i, conn := range conns {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Errorf("client %d read: %v", i, err)
			continue
		}
		f, err := Decode(data)
		if err != nil {
			t.Errorf("client %d decode: %v", i, err)
			continue
		}
		if f.Type != FrameStatusEvent {
			t.Errorf("client %d got %q, want status_event", i, f.Type)
		}
	}
}

func TestServer_SessionCount_UnregistersOnClientClose(t *testing.T) {
	addr, certPEM, token, srv, cleanup := spinServerReturningServer(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := dialClient(t, ctx, addr, certPEM, token)
	hello, _ := NewFrame(FrameHello, "h", HelloPayload{ClientName: "test"})
	b, _ := Encode(hello)
	_ = conn.Write(ctx, websocket.MessageText, b)
	_, _, _ = conn.Read(ctx)

	waitFor(t, 2*time.Second, func() bool { return srv.SessionCount() == 1 })

	_ = conn.Close(websocket.StatusNormalClosure, "done")
	waitFor(t, 2*time.Second, func() bool { return srv.SessionCount() == 0 })
}

func TestSession_SetPushFilter_NormalizesAndStores(t *testing.T) {
	var s Session
	got := s.SetPushFilter([]string{"  msg.  ", "", "delivery."})
	if len(got) != 2 || got[0] != "msg." || got[1] != "delivery." {
		t.Errorf("normalized = %v, want [msg. delivery.]", got)
	}
}

func TestSession_AcceptsPush_EmptyFilterPassesAll(t *testing.T) {
	var s Session

	if !s.AcceptsPush(FrameTimelineEvent) {
		t.Errorf("empty filter should pass FrameTimelineEvent")
	}
	if !s.AcceptsPush(FrameStatusEvent) {
		t.Errorf("empty filter should pass FrameStatusEvent")
	}
}

func TestSession_AcceptsPush_AppliesPrefixGate(t *testing.T) {
	var s Session

	s.SetPushFilter([]string{"msg.", "delivery."})
	if !s.AcceptsPush(FrameTimelineEvent) {
		t.Errorf("FrameTimelineEvent (msg.timeline-event) should pass msg./delivery. filter")
	}
	if !s.AcceptsPush(FrameDeliveryStatus) {
		t.Errorf("FrameDeliveryStatus (delivery.state-changed) should pass msg./delivery. filter")
	}

	if s.AcceptsPush(FrameStatusEvent) {
		t.Errorf("FrameStatusEvent should NOT pass msg./delivery. filter")
	}

	if s.AcceptsPush(FramePresenceChanged) {
		t.Errorf("FramePresenceChanged should NOT pass msg./delivery. filter")
	}
}

func TestSession_SetPushFilter_EmptyResetsToAll(t *testing.T) {
	var s Session
	s.SetPushFilter([]string{"msg."})
	if s.AcceptsPush(FrameStatusEvent) {
		t.Fatalf("filter should suppress FrameStatusEvent")
	}

	s.SetPushFilter(nil)
	if !s.AcceptsPush(FrameStatusEvent) {
		t.Errorf("empty filter should pass FrameStatusEvent")
	}

	s.SetPushFilter([]string{"  "})
	if !s.AcceptsPush(FrameStatusEvent) {
		t.Errorf("whitespace-only filter should pass FrameStatusEvent")
	}
}

func TestServer_Broadcast_RespectsPerSessionFilter(t *testing.T) {
	addr, certPEM, token, srv, cleanup := spinServerReturningServer(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	connA := dialClient(t, ctx, addr, certPEM, token)
	defer connA.Close(websocket.StatusNormalClosure, "")
	connB := dialClient(t, ctx, addr, certPEM, token)
	defer connB.Close(websocket.StatusNormalClosure, "")

	for i, conn := range []*websocket.Conn{connA, connB} {
		hello, _ := NewFrame(FrameHello, fmt.Sprintf("h-%d", i), HelloPayload{ClientName: "test"})
		b, _ := Encode(hello)
		_ = conn.Write(ctx, websocket.MessageText, b)
		_, _, _ = conn.Read(ctx)
	}
	waitFor(t, 2*time.Second, func() bool { return srv.SessionCount() == 2 })

	srv.mu.RLock()
	var sessA *Session
	for s := range srv.sessions {
		if sessA == nil {
			sessA = s
			break
		}
	}
	srv.mu.RUnlock()
	if sessA == nil {
		t.Fatal("session A not found")
	}
	sessA.SetPushFilter([]string{"msg."})

	pushF, _ := NewFrame(FrameDeliveryStatus, "", DeliveryStatusPayload{EnvelopeID: "x", State: "sent"})
	srv.Broadcast(pushF)

	rdCtx, c := context.WithTimeout(ctx, 500*time.Millisecond)
	defer c()

	type res struct {
		idx int
		t   FrameType
		err error
	}
	rChan := make(chan res, 2)
	for i, conn := range []*websocket.Conn{connA, connB} {
		go func(i int, conn *websocket.Conn) {
			_, data, err := conn.Read(rdCtx)
			if err != nil {
				rChan <- res{i, "", err}
				return
			}
			f, _ := Decode(data)
			rChan <- res{i, f.Type, nil}
		}(i, conn)
	}

	got := []res{<-rChan, <-rChan}

	delivered := 0
	for _, r := range got {
		if r.err == nil && r.t == FrameDeliveryStatus {
			delivered++
		}
	}
	if delivered != 1 {
		t.Errorf("delivered count = %d, want 1 (one client filtered, one not). results: %+v", delivered, got)
	}
}

func TestSession_UnsupportedFrameReturnsError(t *testing.T) {
	addr, certPEM, token, cleanup := spinServer(t, nil)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := dialClient(t, ctx, addr, certPEM, token)
	defer conn.Close(websocket.StatusNormalClosure, "")

	hello, _ := NewFrame(FrameHello, "h1", HelloPayload{ClientName: "test"})
	b, _ := Encode(hello)
	_ = conn.Write(ctx, websocket.MessageText, b)
	_, _, _ = conn.Read(ctx)

	bogus, _ := NewFrame("not_a_real_kind", "x1", nil)
	b, _ = Encode(bogus)
	if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if f.Type != FrameError {
		t.Errorf("type = %q, want error", f.Type)
	}
	if f.ID != "x1" {
		t.Errorf("correlation id = %q, want x1", f.ID)
	}
}
