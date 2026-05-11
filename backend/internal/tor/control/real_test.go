package control

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	realTorAddrEnv = "HAOMA_REAL_TOR_CTRL"
	realTorPassEnv = "HAOMA_REAL_TOR_PASS"
)

func requireRealTor(t *testing.T) string {
	t.Helper()
	addr := os.Getenv(realTorAddrEnv)
	if addr == "" {
		t.Skipf("%s not set — skipping live tor test", realTorAddrEnv)
	}
	return addr
}

func TestRealTor_Protocolinfo(t *testing.T) {
	addr := requireRealTor(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := Dial(ctx, addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	reply, err := c.cmd("PROTOCOLINFO 1")
	if err != nil {
		t.Fatalf("cmd: %v", err)
	}
	if reply.Code != 250 {
		t.Fatalf("code = %d, lines = %q", reply.Code, reply.Lines)
	}
	if !strings.HasPrefix(reply.Lines[0], "PROTOCOLINFO") {
		t.Errorf("first line = %q, want PROTOCOLINFO prefix", reply.Lines[0])
	}
	var foundAuth, foundVersion bool
	for _, l := range reply.Lines {
		switch {
		case strings.HasPrefix(l, "AUTH "):
			foundAuth = true
		case strings.HasPrefix(l, "VERSION "):
			foundVersion = true
		}
	}
	if !foundAuth {
		t.Errorf("no AUTH line in reply: %q", reply.Lines)
	}
	if !foundVersion {
		t.Errorf("no VERSION line in reply: %q", reply.Lines)
	}
	t.Logf("PROTOCOLINFO reply:\n  %s", strings.Join(reply.Lines, "\n  "))
}

func TestRealTor_AuthSafeCookie(t *testing.T) {
	addr := requireRealTor(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c, err := Dial(ctx, addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	pi, err := c.ProtocolInfo()
	if err != nil {
		t.Fatalf("ProtocolInfo: %v", err)
	}
	if !pi.Has("SAFECOOKIE") {
		t.Skip("tor does not advertise SAFECOOKIE")
	}
	if pi.CookieFile == "" {
		t.Skip("tor did not disclose COOKIEFILE")
	}
	if _, err := os.Stat(pi.CookieFile); err != nil {
		t.Skipf("cookie file %q not accessible by this user: %v", pi.CookieFile, err)
	}

	if err := c.AuthSafeCookie(pi.CookieFile); err != nil {
		t.Fatalf("AuthSafeCookie: %v", err)
	}

	reply, err := c.cmd("GETINFO version")
	if err != nil {
		t.Fatalf("GETINFO: %v", err)
	}
	if reply.Code != 250 {
		t.Errorf("GETINFO code = %d", reply.Code)
	}
	t.Logf("authenticated; GETINFO version → %v", reply.Lines)
}

func TestRealTor_Authenticate(t *testing.T) {
	addr := requireRealTor(t)
	pass := os.Getenv(realTorPassEnv)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Dial(ctx, addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()
	method, err := c.Authenticate(pass)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	t.Logf("authenticated via %s", method)
	v, err := c.GetInfo("version")
	if err != nil {
		t.Fatalf("GETINFO: %v", err)
	}
	t.Logf("tor version: %s", v)
}

func TestRealTor_AuthPassword(t *testing.T) {
	addr := requireRealTor(t)
	pass := os.Getenv(realTorPassEnv)
	if pass == "" {
		t.Skipf("%s not set — skipping HASHEDPASSWORD test", realTorPassEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c, err := Dial(ctx, addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	if err := c.AuthPassword(pass); err != nil {
		t.Fatalf("AuthPassword: %v", err)
	}
	reply, err := c.cmd("GETINFO version")
	if err != nil {
		t.Fatalf("GETINFO: %v", err)
	}
	if reply.Code != 250 {
		t.Errorf("GETINFO code = %d", reply.Code)
	}
	t.Logf("authenticated via HASHEDPASSWORD; GETINFO version → %v", reply.Lines)
}

func TestRealTor_OnionRoundTrip(t *testing.T) {
	addr := requireRealTor(t)
	pass := os.Getenv(realTorPassEnv)
	if pass == "" {
		t.Skipf("%s not set — skipping onion round-trip", realTorPassEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c, err := Dial(ctx, addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()
	if err := c.AuthPassword(pass); err != nil {
		t.Fatalf("AuthPassword: %v", err)
	}

	o, err := c.AddOnionNew([]OnionPort{{VirtPort: 80, Target: "127.0.0.1:12345"}})
	if err != nil {
		t.Fatalf("AddOnionNew: %v", err)
	}
	t.Logf("created %s.onion (pk len=%d)", o.ServiceID, len(o.PrivateKey))
	if len(o.ServiceID) != 56 {
		t.Errorf("ServiceID length = %d, want 56", len(o.ServiceID))
	}
	if o.PrivateKey == "" {
		t.Error("PrivateKey not returned")
	}

	if err := c.DelOnion(o.ServiceID); err != nil {
		t.Fatalf("DelOnion: %v", err)
	}

	o2, err := c.AddOnion(o.PrivateKey, []OnionPort{{VirtPort: 80, Target: "127.0.0.1:12345"}})
	if err != nil {
		t.Fatalf("AddOnion (republish): %v", err)
	}
	if o2.ServiceID != o.ServiceID {
		t.Errorf("republish ServiceID = %s, want %s", o2.ServiceID, o.ServiceID)
	}
	if err := c.DelOnion(o2.ServiceID); err != nil {
		t.Logf("cleanup DelOnion: %v", err)
	}
}

func TestRealTor_GetInfoVersion(t *testing.T) {
	addr := requireRealTor(t)
	pass := os.Getenv(realTorPassEnv)
	if pass == "" {
		t.Skipf("%s not set", realTorPassEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Dial(ctx, addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()
	if err := c.AuthPassword(pass); err != nil {
		t.Fatal(err)
	}
	v, err := c.GetInfo("version")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("tor version via GetInfo: %s", v)
}

func TestRealTor_SetEvents_HSDescLifecycle(t *testing.T) {
	addr := requireRealTor(t)
	pass := os.Getenv(realTorPassEnv)
	if pass == "" {
		t.Skipf("%s not set", realTorPassEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := Dial(ctx, addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()
	if err := c.AuthPassword(pass); err != nil {
		t.Fatal(err)
	}
	if err := c.SetEvents("HS_DESC"); err != nil {
		t.Fatalf("SetEvents: %v", err)
	}

	o, err := c.AddOnionNew([]OnionPort{{VirtPort: 80, Target: "127.0.0.1:12345"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.DelOnion(o.ServiceID)

	var seenCreated, seenUploaded bool
	deadline := time.After(10 * time.Second)
LOOP:
	for {
		select {
		case ev := <-c.Events():
			if ev.Type != "HS_DESC" || len(ev.Lines) == 0 {
				continue
			}
			fields := strings.Fields(ev.Lines[0])
			if len(fields) < 3 || fields[2] != o.ServiceID {
				continue
			}
			switch fields[1] {
			case "CREATED":
				seenCreated = true
			case "UPLOADED":
				seenUploaded = true
				break LOOP
			}
		case <-deadline:
			break LOOP
		}
		if seenCreated && seenUploaded {
			break
		}
	}
	if !seenCreated {
		t.Fatalf("no HS_DESC CREATED event for %s within 10s", o.ServiceID)
	}
	t.Logf("HS_DESC CREATED observed for %s.onion (UPLOADED=%v)", o.ServiceID, seenUploaded)
}
