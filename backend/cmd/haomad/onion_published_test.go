package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"haoma/internal/peers"
	"haoma/internal/tor/control"
)

type fakeTor struct {
	mu         sync.Mutex
	cmds       []fakeCmd
	serving    map[string]int
	keys       map[string]string
	delReply   map[string]string
	minted     int
	circuits   []string
	closeReply map[string]string
	wires      map[int]*fakeWire

	socksConf    string
	socksAddr    string
	netOff       bool
	nextPort     int
	moveAlways   bool
	toggleEvents bool

	refuseOnPinned bool
	setconfReply   map[string]string
}

type fakeWire struct {
	mu sync.Mutex
	c  net.Conn
}

func (w *fakeWire) write(s string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, err := w.c.Write([]byte(s))
	return err
}

func (f *fakeTor) push(t *testing.T, conn int, line string) {
	t.Helper()
	f.mu.Lock()
	w := f.wires[conn]
	f.mu.Unlock()
	if w == nil {
		t.Fatalf("fake tor: no connection %d", conn)
	}
	if err := w.write(line + "\r\n"); err != nil {
		t.Fatalf("fake tor: push to connection %d: %v", conn, err)
	}
}

type fakeCmd struct {
	conn int
	line string
}

func sid(name string) string {
	return name + strings.Repeat("a", 56-len(name))
}

func newFakeTor() *fakeTor {
	return &fakeTor{
		serving:  make(map[string]int),
		keys:     make(map[string]string),
		delReply: make(map[string]string),
		wires:    make(map[int]*fakeWire),

		socksConf:    "auto",
		socksAddr:    "127.0.0.1:40001",
		nextPort:     40002,
		setconfReply: make(map[string]string),
	}
}

func (f *fakeTor) setconf(key, value string) string {
	if reply, ok := f.setconfReply[key+"="+value]; ok {
		return reply
	}
	if reply, ok := f.setconfReply[key]; ok {
		return reply
	}
	switch key {
	case "SocksPort":
		f.socksConf = value
		if addr, _, _ := strings.Cut(value, " "); addr != "auto" && !f.netOff {
			f.socksAddr = addr
		}
	case "DisableNetwork":
		switch value {
		case "1":
			f.netOff = true
			f.socksAddr = ""
			if f.toggleEvents {
				return "650 NETWORK_LIVENESS DOWN\r\n250 OK\r\n"
			}
		case "0":
			addr, _, _ := strings.Cut(f.socksConf, " ")
			if f.refuseOnPinned && addr != "auto" {
				return "553 Unable to set option: Failed to bind one of the listener ports.\r\n"
			}
			f.netOff = false
			if addr == "auto" || f.moveAlways {
				addr = fmt.Sprintf("127.0.0.1:%d", f.nextPort)
				f.nextPort++
			}
			f.socksAddr = addr
			if f.toggleEvents {
				return "250 OK\r\n650 NETWORK_LIVENESS UP\r\n"
			}
		}
	}
	return "250 OK\r\n"
}

var setconfPair = regexp.MustCompile(`(\w+)="([^"]*)"`)

func (f *fakeTor) setconfs() []string {
	var out []string
	for _, c := range f.sent() {
		if strings.HasPrefix(c.line, "SETCONF ") {
			out = append(out, strings.TrimPrefix(c.line, "SETCONF "))
		}
	}
	return out
}

func (f *fakeTor) seedKey(key, serviceID string, foreign bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys[key] = serviceID
	if foreign {
		f.serving[serviceID] = 0
	}
}

func (f *fakeTor) refuseDel(serviceID, reply string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delReply[serviceID] = reply
}

func (f *fakeTor) sent() []fakeCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeCmd(nil), f.cmds...)
}

func (f *fakeTor) addOnions() []string {
	var out []string
	for _, c := range f.sent() {
		if strings.HasPrefix(c.line, "ADD_ONION ") {
			out = append(out, c.line)
		}
	}
	return out
}

func (f *fakeTor) isServing(serviceID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.serving[serviceID]
	return ok
}

func (f *fakeTor) connClosed(conn int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, owner := range f.serving {
		if owner == conn {
			delete(f.serving, id)
		}
	}
}

func (f *fakeTor) handle(conn int, line string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmds = append(f.cmds, fakeCmd{conn: conn, line: line})
	switch {
	case line == "PROTOCOLINFO 1":
		return "250-AUTH METHODS=NULL\r\n250-VERSION Tor=\"0.4.8.12\"\r\n250 OK\r\n"
	case strings.HasPrefix(line, "ADD_ONION NEW:ED25519-V3"):
		f.minted++
		key := fmt.Sprintf("mintedkey%d", f.minted)
		addr := sid(fmt.Sprintf("minted%d", f.minted))
		f.keys[key] = addr
		f.serving[addr] = conn
		return "250-ServiceID=" + addr + "\r\n250-PrivateKey=ED25519-V3:" + key + "\r\n250 OK\r\n"
	case strings.HasPrefix(line, "ADD_ONION ED25519-V3:"):
		rest := strings.TrimPrefix(line, "ADD_ONION ED25519-V3:")
		key, _, _ := strings.Cut(rest, " ")
		addr, known := f.keys[key]
		if !known {
			return "512 Unknown key in this fixture\r\n"
		}
		if _, busy := f.serving[addr]; busy {
			return "550 Onion address collision\r\n"
		}
		f.serving[addr] = conn
		return "250-ServiceID=" + addr + "\r\n250 OK\r\n"
	case line == "GETCONF SocksPort":
		return "250 SocksPort=" + f.socksConf + "\r\n"
	case line == "GETINFO net/listeners/socks":
		if f.socksAddr == "" {
			return "250-net/listeners/socks=\r\n250 OK\r\n"
		}
		return "250-net/listeners/socks=\"" + f.socksAddr + "\"\r\n250 OK\r\n"
	case strings.HasPrefix(line, "SETCONF "):

		pairs := setconfPair.FindAllStringSubmatch(strings.TrimPrefix(line, "SETCONF "), -1)
		if len(pairs) == 1 {
			return f.setconf(pairs[0][1], pairs[0][2])
		}
		for _, m := range pairs {
			if reply := f.setconf(m[1], m[2]); !strings.Contains(reply, "250 OK\r\n") {
				return reply
			}
		}
		return "250 OK\r\n"
	case line == "GETINFO circuit-status":
		body := ""
		for _, c := range f.circuits {
			body += c + "\r\n"
		}
		return "250+circuit-status=\r\n" + body + ".\r\n250 OK\r\n"
	case strings.HasPrefix(line, "CLOSECIRCUIT "):
		if reply, ok := f.closeReply[strings.TrimPrefix(line, "CLOSECIRCUIT ")]; ok {
			return reply
		}
		return "250 OK\r\n"
	case strings.HasPrefix(line, "DEL_ONION "):
		addr := strings.TrimSpace(strings.TrimPrefix(line, "DEL_ONION "))
		if reply, refused := f.delReply[addr]; refused {
			return reply
		}
		if _, ok := f.serving[addr]; !ok {
			return "552 Unknown onion service\r\n"
		}
		delete(f.serving, addr)
		return "250 OK\r\n"
	default:
		return "250 OK\r\n"
	}
}

func startFakeTor(t *testing.T) (*fakeTor, string) {
	t.Helper()
	f := newFakeTor()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn := 0
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conn++
			w := &fakeWire{c: c}
			f.mu.Lock()
			f.wires[conn] = w
			f.mu.Unlock()
			go func(id int) {
				defer c.Close()
				defer f.connClosed(id)
				br := bufio.NewReader(c)
				for {
					line, err := br.ReadString('\n')
					if err != nil {
						return
					}
					if err := w.write(f.handle(id, strings.TrimRight(line, "\r\n"))); err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	return f, ln.Addr().String()
}

func dialFakeTor(t *testing.T, addr string) *control.Conn {
	t.Helper()
	conn, err := control.Dial(context.Background(), addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func newTorDaemon(t *testing.T) (*daemon, *fakeTor) {
	t.Helper()
	d := newTestDaemon(t)
	f, addr := startFakeTor(t)
	d.cfg.torControl = addr
	d.ctrlConn = dialFakeTor(t, addr)
	d.cfg.onionVirtPort = 80
	target := "127.0.0.1:9999"
	d.xportTarget.Store(&target)
	return d, f
}

func importPeer(t *testing.T, d *daemon, inv *peers.BackendInvite) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(inv)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/peers", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	d.handleImportPeer(rec, req)
	return rec
}

func importedPeer(t *testing.T, d *daemon, addr, key string) (*peers.Peer, *httptest.ResponseRecorder) {
	t.Helper()
	inv := newTestBackendInvite(t, []string{sid("peeraddr")})
	inv.MyOnionAddr = addr
	inv.MyOnionPrivateKey = key
	rec := importPeer(t, d, inv)
	if rec.Code != http.StatusCreated {
		t.Fatalf("import: status %d, body %s", rec.Code, rec.Body.String())
	}
	p, err := d.registry.Get(inv.PeerID)
	if err != nil {
		t.Fatal(err)
	}
	return p, rec
}

func closeAndSettle(t *testing.T, d *daemon, f *fakeTor, addrs ...string) {
	t.Helper()
	d.ctrlMu.Lock()
	conn := d.ctrlConn
	d.ctrlMu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	for _, addr := range addrs {
		freed := false
		for i := 0; i < 200 && !freed; i++ {
			if !f.isServing(addr) {
				freed = true
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if !freed {
			t.Fatalf("fake tor still serves %s after its control connection closed", addr)
		}
	}
}

func captureLog(t *testing.T) *syncBuffer {
	t.Helper()
	var buf syncBuffer
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(restore) })
	return &buf
}

func logLines(buf *syncBuffer, want ...string) []string {
	var out []string
line:
	for _, line := range strings.Split(buf.String(), "\n") {
		for _, w := range want {
			if !strings.Contains(line, w) {
				continue line
			}
		}
		out = append(out, line)
	}
	return out
}

func TestMintedOnionIsNotPublishedAgainOnImport(t *testing.T) {
	d, f := newTorDaemon(t)

	rec := httptest.NewRecorder()
	d.handleOnionMint(rec, httptest.NewRequest(http.MethodPost, "/onion/mint", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("mint: status %d, body %s", rec.Code, rec.Body.String())
	}
	var minted struct {
		Address    string `json:"address"`
		PrivateKey string `json:"private_key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &minted); err != nil {
		t.Fatal(err)
	}

	importedPeer(t, d, minted.Address, minted.PrivateKey)

	adds := f.addOnions()
	if len(adds) != 1 {
		t.Errorf("tor saw %d ADD_ONION commands, want 1 (the mint):\n%s", len(adds), strings.Join(adds, "\n"))
	}
	if !strings.HasPrefix(adds[0], "ADD_ONION NEW:") {
		t.Errorf("first ADD_ONION = %q, want the mint", adds[0])
	}
	if !d.published.has(d.ctrlConn, minted.Address) {
		t.Error("the minted address is not recorded as published on this control connection")
	}
}

func TestImportedKeyFromAnEarlierRunIsPublished(t *testing.T) {
	d, f := newTorDaemon(t)

	addr := sid("earlier")
	key := "earlier-run-key"
	f.seedKey(key, addr, false)
	importedPeer(t, d, addr, key)

	adds := f.addOnions()
	if len(adds) != 1 {
		t.Fatalf("tor saw %d ADD_ONION commands, want 1:\n%s", len(adds), strings.Join(adds, "\n"))
	}
	if !strings.Contains(adds[0], "ED25519-V3:"+key) {
		t.Errorf("ADD_ONION = %q, want the stored key", adds[0])
	}
	if !f.isServing(addr) {
		t.Error("tor is not serving the imported peer's address")
	}
	if !d.published.has(d.ctrlConn, addr) {
		t.Error("the published address is not recorded on this control connection")
	}
}

func TestCollisionThisDaemonDidNotCauseIsLoud(t *testing.T) {
	d, f := newTorDaemon(t)
	buf := captureLog(t)

	addr := sid("taken")
	key := "taken-key"
	f.seedKey(key, addr, true)

	p, rec := importedPeer(t, d, addr, key)

	if got := logLines(buf, addr, "level=ERROR"); len(got) == 0 {
		t.Errorf("the collision was not reported at ERROR; log was:\n%s", buf.String())
	}
	if d.published.has(d.ctrlConn, addr) {
		t.Error("a refused address was recorded as published")
	}

	var body struct {
		OnionPublished *bool  `json:"onion_published"`
		OnionError     string `json:"onion_error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.OnionPublished == nil || *body.OnionPublished {
		t.Errorf("import response onion_published = %v, want false", body.OnionPublished)
	}
	if body.OnionError != "address_taken" {
		t.Errorf("import response onion_error = %q, want address_taken", body.OnionError)
	}

	perr := d.publishPeerOnion(*p)
	if !errors.Is(perr, control.ErrOnionAddressCollision) {
		t.Errorf("publishPeerOnion err = %v, want it to wrap control.ErrOnionAddressCollision", perr)
	}
}

func TestPublishFailureThatIsNotACollisionIsNotReportedAsTaken(t *testing.T) {
	d, _ := newTorDaemon(t)
	buf := captureLog(t)

	addr := sid("unknownkey")
	_, rec := importedPeer(t, d, addr, "a-key-this-tor-does-not-know")

	if got := logLines(buf, addr, "level=ERROR"); len(got) != 0 {
		t.Errorf("a 512 was reported as an address served by something else:\n%s", strings.Join(got, "\n"))
	}
	var body struct {
		OnionError string `json:"onion_error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.OnionError != "publish_failed" {
		t.Errorf("import response onion_error = %q, want publish_failed", body.OnionError)
	}
}

func TestPublishHappensAgainOnAFreshControlConnection(t *testing.T) {
	d, f := newTorDaemon(t)

	addr := sid("survivor")
	key := "survivor-key"
	f.seedKey(key, addr, false)
	p, _ := importedPeer(t, d, addr, key)

	if err := d.publishPeerOnion(*p); err != nil {
		t.Fatal(err)
	}
	if n := len(f.addOnions()); n != 1 {
		t.Errorf("%d ADD_ONION commands on the first connection, want 1", n)
	}

	closeAndSettle(t, d, f, addr)
	before := len(f.addOnions())
	if err := d.adoptCtrlConn(dialFakeTor(t, d.cfg.torControl)); err != nil {
		t.Fatalf("adoptCtrlConn: %v", err)
	}
	if n := len(f.addOnions()) - before; n != 1 {
		t.Errorf("%d ADD_ONION commands on the adopted connection, want 1", n)
	}
	if !f.isServing(addr) {
		t.Error("the peer's address was not put on the adopted control connection")
	}
	if err := d.publishPeerOnion(*p); err != nil {
		t.Fatal(err)
	}
	if n := len(f.addOnions()) - before; n != 1 {
		t.Errorf("%d ADD_ONION commands after a repeat publish, want the adopted connection's 1", n)
	}

	closeAndSettle(t, d, f, addr)
	before = len(f.addOnions())
	if !watchdogReconnect(context.Background(), d, "test", true) {
		t.Fatal("watchdogReconnect reported failure")
	}
	t.Cleanup(func() { _ = d.ctrlConn.Close() })
	if n := len(f.addOnions()) - before; n != 1 {
		t.Errorf("%d ADD_ONION commands on the reconnected connection, want 1", n)
	}
	if !f.isServing(addr) {
		t.Error("the peer's address was not put back after the reconnect")
	}
	if err := d.publishPeerOnion(*p); err != nil {
		t.Fatal(err)
	}
	if n := len(f.addOnions()) - before; n != 1 {
		t.Errorf("%d ADD_ONION commands after a repeat publish, want the reconnected connection's 1", n)
	}
}

func TestFailedBringUpLeavesNoConnectionToSkipAgainst(t *testing.T) {
	d, f := newTorDaemon(t)

	addr := sid("bringup")
	key := "bringup-key"
	f.seedKey(key, addr, false)
	p, _ := importedPeer(t, d, addr, key)

	bad := newTestBackendInvite(t, []string{sid("otheraddr")})
	bad.MyOnionAddr = sid("badkeypeer")
	bad.MyOnionPrivateKey = "a-key-this-tor-does-not-know"
	if rec := importPeer(t, d, bad); rec.Code != http.StatusCreated {
		t.Fatalf("import: status %d", rec.Code)
	}

	closeAndSettle(t, d, f, addr)
	fresh := dialFakeTor(t, d.cfg.torControl)
	if err := d.adoptCtrlConn(fresh); err == nil {
		t.Fatal("adoptCtrlConn succeeded with a peer tor refuses")
	}
	d.ctrlMu.Lock()
	held := d.ctrlConn
	d.ctrlMu.Unlock()
	if held != nil {
		t.Error("the daemon still points at the connection whose bring-up failed")
	}
	if err := d.publishPeerOnion(*p); err == nil {
		t.Error("publishPeerOnion reported success with no control connection")
	}
}

func TestRefusedUnpublishKeepsTheAddress(t *testing.T) {
	d, f := newTorDaemon(t)

	addr := sid("refused")
	key := "refused-key"
	f.seedKey(key, addr, false)
	p, _ := importedPeer(t, d, addr, key)
	f.refuseDel(addr, "550 Unspecified Tor error\r\n")

	d.unpublishPeerOnion(addr)
	if !d.published.has(d.ctrlConn, addr) {
		t.Fatal("the address was dropped although tor refused to stop serving it")
	}
	if err := d.publishPeerOnion(*p); err != nil {
		t.Fatalf("publish after a refused DEL_ONION: %v", err)
	}
	if n := len(f.addOnions()); n != 1 {
		t.Errorf("%d ADD_ONION commands, want 1 — the address never left tor", n)
	}
}

func TestAcceptedUnpublishReleasesTheAddress(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(f *fakeTor, addr string)
	}{
		{"accepted", func(*fakeTor, string) {}},
		{"unknown to tor", func(f *fakeTor, addr string) {
			f.refuseDel(addr, "552 Unknown onion service\r\n")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, f := newTorDaemon(t)

			addr := sid("released")
			key := "released-key"
			f.seedKey(key, addr, false)
			p, _ := importedPeer(t, d, addr, key)
			tc.setup(f, addr)

			d.unpublishPeerOnion(addr)
			if d.published.has(d.ctrlConn, addr) {
				t.Error("the address is still recorded as published after DEL_ONION")
			}

			f.mu.Lock()
			delete(f.serving, addr)
			delete(f.delReply, addr)
			f.mu.Unlock()

			if err := d.publishPeerOnion(*p); err != nil {
				t.Fatalf("republish after DEL_ONION: %v", err)
			}
			if n := len(f.addOnions()); n != 2 {
				t.Errorf("%d ADD_ONION commands, want 2 (the import and the republish)", n)
			}
		})
	}
}

func TestRepublishSkipsWhatThisConnectionAlreadyServes(t *testing.T) {
	d, f := newTorDaemon(t)
	buf := captureLog(t)

	addr := sid("standing")
	key := "standing-key"
	f.seedKey(key, addr, false)
	importedPeer(t, d, addr, key)
	before := len(f.addOnions())

	if err := d.republishAllPeerOnions(d.ctrlConn, true); err != nil {
		t.Fatalf("republishAllPeerOnions: %v", err)
	}
	if n := len(f.addOnions()) - before; n != 0 {
		t.Errorf("the walk sent %d ADD_ONION commands for an address tor already serves, want 0", n)
	}
	got := logLines(buf, "republishAllPeerOnions complete")
	if len(got) == 0 {
		t.Fatalf("no completion line; log was:\n%s", buf.String())
	}
	last := got[len(got)-1]
	if !strings.Contains(last, "published=0") || !strings.Contains(last, "already_published=1") {
		t.Errorf("completion line = %q, want published=0 already_published=1", last)
	}
}

func TestRepublishReportsATakenAddressAndKeepsGoing(t *testing.T) {
	d, f := newTorDaemon(t)

	taken := sid("occupied")
	f.seedKey("occupied-key", taken, true)
	reachable := sid("reachable")
	f.seedKey("reachable-key", reachable, false)

	for _, spec := range []struct{ addr, key string }{
		{taken, "occupied-key"},
		{reachable, "reachable-key"},
	} {
		inv := newTestBackendInvite(t, []string{sid("peer" + spec.addr[:4])})
		inv.MyOnionAddr = spec.addr
		inv.MyOnionPrivateKey = spec.key
		if rec := importPeer(t, d, inv); rec.Code != http.StatusCreated {
			t.Fatalf("import %s: status %d", spec.addr, rec.Code)
		}
	}

	closeAndSettle(t, d, f, reachable)
	buf := captureLog(t)
	fresh := dialFakeTor(t, d.cfg.torControl)
	d.ctrlMu.Lock()
	d.ctrlConn = fresh
	d.ctrlMu.Unlock()

	if err := d.republishAllPeerOnions(fresh, true); err != nil {
		t.Fatalf("republishAllPeerOnions failed the whole walk over one taken address: %v", err)
	}
	if !d.published.has(fresh, reachable) {
		t.Error("the reachable peer was not published")
	}
	if d.published.has(fresh, taken) {
		t.Error("the taken address was recorded as published")
	}
	if got := logLines(buf, taken, "level=ERROR"); len(got) == 0 {
		t.Errorf("the taken address was not reported at ERROR; log was:\n%s", buf.String())
	}
	got := logLines(buf, "republishAllPeerOnions complete")
	if len(got) == 0 {
		t.Fatalf("no completion line; log was:\n%s", buf.String())
	}
	last := got[len(got)-1]
	if !strings.Contains(last, "published=1") || !strings.Contains(last, "address_taken=1") {
		t.Errorf("completion line = %q, want published=1 address_taken=1", last)
	}
}
