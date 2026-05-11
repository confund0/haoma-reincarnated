package ipcclient

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"testing"
	"time"

	"haoma-frontend/internal/ipc"
)

func spinDaemon(t *testing.T) (dataDir, addr string, cancel func()) {
	t.Helper()
	dir := t.TempDir()
	tlsCfg, err := ipc.LoadOrCreateTLS(dir)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := ipc.LoadOrCreateToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := ipc.NewServer(tok)
	httpSrv := &http.Server{Handler: srv.Handler(), TLSConfig: tlsCfg}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		tlsLn := tls.NewListener(ln, tlsCfg)
		_ = httpSrv.Serve(tlsLn)
	}()
	cancel = func() {
		shutdownCtx, c := context.WithTimeout(context.Background(), time.Second)
		defer c()
		_ = httpSrv.Shutdown(shutdownCtx)
	}
	return dir, ln.Addr().String(), cancel
}

func TestClient_HelloWelcomeRoundTrip(t *testing.T) {
	dir, addr, cancel := spinDaemon(t)
	defer cancel()

	client, err := New(Config{
		FrontendDir: dir,
		Addr:        addr,
		ClientName:  "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	go func() { _ = client.Run() }()

	select {
	case f := <-client.Incoming():
		if f.Type != ipc.FrameWelcome {
			t.Errorf("first frame = %q, want welcome", f.Type)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for welcome")
	}
}

func TestClient_New_ErrorsOnMissingCert(t *testing.T) {
	dir := t.TempDir()

	_, err := New(Config{FrontendDir: dir, Addr: "127.0.0.1:1", ClientName: "x"})
	if err == nil {
		t.Fatal("expected error on missing cert")
	}
}

func TestClient_New_ErrorsOnMissingToken(t *testing.T) {
	dir := t.TempDir()

	if _, err := ipc.LoadOrCreateTLS(dir); err != nil {
		t.Fatal(err)
	}
	_, err := New(Config{FrontendDir: dir, Addr: "127.0.0.1:1", ClientName: "x"})
	if err == nil {
		t.Fatal("expected error on missing token")
	}
}

func TestClient_ConnectionChannel_EmitsTrueOnConnect(t *testing.T) {
	dir, addr, cancel := spinDaemon(t)
	defer cancel()

	client, err := New(Config{FrontendDir: dir, Addr: addr, ClientName: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	go func() { _ = client.Run() }()

	go func() {
		for range client.Incoming() {
		}
	}()

	select {
	case up := <-client.Connection():
		if !up {
			t.Errorf("first Connection event = false, want true")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for connected event")
	}

	if !client.IsConnected() {
		t.Errorf("IsConnected() = false after connect event, want true")
	}
}

func TestClient_ConnectionChannel_EmitsFalseOnClose(t *testing.T) {
	dir, addr, cancel := spinDaemon(t)
	defer cancel()

	client, err := New(Config{FrontendDir: dir, Addr: addr, ClientName: "test"})
	if err != nil {
		t.Fatal(err)
	}

	go func() { _ = client.Run() }()
	go func() {
		for range client.Incoming() {
		}
	}()

	select {
	case <-client.Connection():
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for connected")
	}

	client.Close()

	deadline := time.After(5 * time.Second)
	for {
		select {
		case up, ok := <-client.Connection():
			if !ok {
				t.Fatal("connection channel closed without a false event")
			}
			if !up {
				if client.IsConnected() {
					t.Errorf("IsConnected() = true after false event, want false")
				}
				return
			}
		case <-deadline:
			t.Fatal("timeout waiting for disconnected event")
		}
	}
}
