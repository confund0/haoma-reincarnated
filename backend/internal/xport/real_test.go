package xport

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	"haoma/internal/tor/control"
)

const (
	realTorAddrEnv  = "HAOMA_REAL_TOR_CTRL"
	realTorPassEnv  = "HAOMA_REAL_TOR_PASS"
	realTorSocksEnv = "HAOMA_REAL_TOR_SOCKS"
)

func requireRealTor(t *testing.T) (ctrlAddr, pass, socks string) {
	t.Helper()
	ctrlAddr = os.Getenv(realTorAddrEnv)
	if ctrlAddr == "" {
		t.Skipf("%s not set — skipping live tor test", realTorAddrEnv)
	}
	pass = os.Getenv(realTorPassEnv)
	socks = os.Getenv(realTorSocksEnv)
	if socks == "" {
		socks = "127.0.0.1:9050"
	}
	return
}

func TestRealTor_Xport_RoundTrip(t *testing.T) {
	ctrlAddr, pass, socks := requireRealTor(t)

	received := make(chan Envelope, 2)
	recv := ReceiverFunc(func(ctx context.Context, e Envelope) error {
		received <- e
		return nil
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	srv := &http.Server{Handler: NewServer(-1, recv, nil, nil)}
	go srv.Serve(ln)
	defer srv.Close()
	t.Logf("local HTTP server on 127.0.0.1:%d", port)

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer dialCancel()
	c, err := control.Dial(dialCtx, ctrlAddr)
	if err != nil {
		t.Fatalf("control.Dial: %v", err)
	}
	defer c.Close()
	if _, err := c.Authenticate(pass); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	ports := []control.OnionPort{{
		VirtPort: 80,
		Target:   fmt.Sprintf("127.0.0.1:%d", port),
	}}
	o, err := c.AddOnionNew(ports)
	if err != nil {
		t.Fatalf("AddOnionNew: %v", err)
	}
	defer func() { _ = c.DelOnion(o.ServiceID) }()
	onion := o.ServiceID
	onionURL := "http://" + onion + ".onion"
	t.Logf("onion published: %s.onion", onion)

	time.Sleep(8 * time.Second)

	hc, err := NewTorHTTPClient(socks, "xport-roundtrip-test")
	if err != nil {
		t.Fatalf("NewTorHTTPClient: %v", err)
	}
	hc.Timeout = 90 * time.Second
	client := &Client{HTTP: hc}

	want := Envelope{
		ID:        "rt-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		Timestamp: time.Now().Unix(),
		From:      "sender-" + onion,
		Payload:   []byte("hello over tor — end-to-end"),
	}
	sendCtx, sendCancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer sendCancel()
	if _, err := client.Send(sendCtx, onionURL, want); err != nil {
		t.Fatalf("Send: %v", err)
	}
	t.Logf("sent envelope %s", want.ID)

	select {
	case got := <-received:
		if got.ID != want.ID {
			t.Errorf("ID = %q, want %q", got.ID, want.ID)
		}
		if got.Timestamp != want.Timestamp {
			t.Errorf("Timestamp = %d, want %d", got.Timestamp, want.Timestamp)
		}
		if !bytes.Equal(got.Payload, want.Payload) {
			t.Errorf("Payload = %q, want %q", got.Payload, want.Payload)
		}
		t.Logf("received envelope %s, %d bytes payload — end-to-end round-trip OK", got.ID, len(got.Payload))
	case <-time.After(30 * time.Second):
		t.Fatal("receiver did not get envelope within 30s of Send returning")
	}
}
