package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"haoma/internal/peers"
	"haoma/internal/proxy"
	"haoma/internal/xport"
)

type fakeSocks struct {
	mu        sync.Mutex
	stall     func(tag, host string) bool
	onConnect func(tag, host string)
	connects  []string

	stallHTTP func(tag string) bool

	bodies []string
}

func (s *fakeSocks) seenBodies() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.bodies...)
}

func (s *fakeSocks) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.connects...)
}

func startFakeSocks(t *testing.T, s *fakeSocks) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return ln.Addr().String()
}

func (s *fakeSocks) serve(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	buf := make([]byte, 2)
	if _, err := io.ReadFull(br, buf); err != nil || buf[0] != 5 {
		return
	}
	methods := make([]byte, buf[1])
	if _, err := io.ReadFull(br, methods); err != nil {
		return
	}
	_, _ = c.Write([]byte{5, 2})

	if _, err := io.ReadFull(br, buf[:2]); err != nil {
		return
	}
	user := make([]byte, buf[1])
	if _, err := io.ReadFull(br, user); err != nil {
		return
	}
	if _, err := io.ReadFull(br, buf[:1]); err != nil {
		return
	}
	pass := make([]byte, buf[0])
	if _, err := io.ReadFull(br, pass); err != nil {
		return
	}
	_, _ = c.Write([]byte{1, 0})
	head := make([]byte, 4)
	if _, err := io.ReadFull(br, head); err != nil || head[1] != 1 || head[3] != 3 {
		return
	}
	if _, err := io.ReadFull(br, buf[:1]); err != nil {
		return
	}
	name := make([]byte, buf[0])
	if _, err := io.ReadFull(br, name); err != nil {
		return
	}
	port := make([]byte, 2)
	if _, err := io.ReadFull(br, port); err != nil {
		return
	}
	_ = binary.BigEndian.Uint16(port)
	tag, host := string(user), strings.TrimSuffix(string(name), ".onion")
	s.mu.Lock()
	s.connects = append(s.connects, tag+" "+host)
	stall, hook := s.stall, s.onConnect
	s.mu.Unlock()
	if hook != nil {
		hook(tag, host)
	}
	if stall != nil && stall(tag, host) {

		_, _ = io.Copy(io.Discard, br)
		return
	}
	_, _ = c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		b, _ := io.ReadAll(req.Body)
		_ = req.Body.Close()
		s.mu.Lock()
		s.bodies = append(s.bodies, tag+" "+string(b))
		stallHTTP := s.stallHTTP
		s.mu.Unlock()
		if stallHTTP != nil && stallHTTP(tag) {
			_, _ = io.Copy(io.Discard, br)
			return
		}
		if _, err := c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\n{}")); err != nil {
			return
		}
	}
}

type renewFixture struct {
	d     *daemon
	f     *fakeTor
	socks *fakeSocks
	peer  *peers.Peer
	addr  string
	tag0  string

	cutMu sync.Mutex
	cuts  []*fakeCut
}

type fakeCut struct {
	f       func()
	stopped atomic.Bool
}

func newRenewFixture(t *testing.T) *renewFixture {
	t.Helper()
	d, f := newTorDaemon(t)
	inv := newTestBackendInvite(t, []string{sid("peerroute")})
	if _, err := d.registry.Import(inv); err != nil {
		t.Fatal(err)
	}
	p, err := d.registry.Get(inv.PeerID)
	if err != nil {
		t.Fatal(err)
	}
	addr := p.KnownAddresses[0]
	f.mu.Lock()
	f.circuits = []string{
		"7 BUILT $AAAA~a,$BBBB~b,$CCCC~c PURPOSE=HS_CLIENT_REND HS_STATE=HSCR_JOINED REND_QUERY=" + addr,
		"8 BUILT $DDDD~d,$EEEE~e PURPOSE=GENERAL",
		"9 BUILT $FFFF~f,$GGGG~g PURPOSE=HS_CLIENT_REND REND_QUERY=" + sid("someoneelse"),
		"10 BUILT $HHHH~h,$IIII~i PURPOSE=HS_CLIENT_REND REND_QUERY=" + addr,
	}
	f.closeReply = map[string]string{"10": "552 Unknown circuit \"10\"\r\n"}
	f.mu.Unlock()
	socks := &fakeSocks{}
	d.cfg.torSocks = startFakeSocks(t, socks)
	d.selfProbe = newPeerSelfProbe()
	setSelfProbe(d, p, true)

	old := goCircuitRenewal
	goCircuitRenewal = func(fn func()) { fn() }
	oldDelay, oldSleep := renewDelay, renewSleep
	renewDelay = func() time.Duration { return 0 }
	renewSleep = func(context.Context, time.Duration) bool { return true }
	t.Cleanup(func() { goCircuitRenewal, renewDelay, renewSleep = old, oldDelay, oldSleep })
	fx := &renewFixture{d: d, f: f, socks: socks, peer: p, addr: addr, tag0: d.peerIsoTag(p.ID)}
	oldCut := startCutTimer
	startCutTimer = func(_ time.Duration, fn func()) func() bool {
		c := &fakeCut{f: fn}
		fx.cutMu.Lock()
		fx.cuts = append(fx.cuts, c)
		fx.cutMu.Unlock()
		return func() bool { return !c.stopped.Swap(true) }
	}
	t.Cleanup(func() { startCutTimer = oldCut })
	return fx
}

func setSelfProbe(d *daemon, p *peers.Peer, ok bool) {
	d.selfProbe.store(peerSelfReachState{PeerID: p.ID, Onion: p.MyOnionAddr, Ok: ok, At: time.Now()})
}

func staleSelfProbe(d *daemon, p *peers.Peer) {
	d.selfProbe.store(peerSelfReachState{PeerID: p.ID, Ok: false, At: time.Now().Add(-time.Hour)})
}

func (fx *renewFixture) plainTag() string { return fx.tag0 }

func (fx *renewFixture) cutCount() int {
	fx.cutMu.Lock()
	defer fx.cutMu.Unlock()
	return len(fx.cuts)
}

func (fx *renewFixture) fireCut(t *testing.T, i int) {
	t.Helper()
	fx.cutMu.Lock()
	c := fx.cuts[i]
	fx.cutMu.Unlock()
	if !c.stopped.Load() {
		c.f()
	}
}

func (fx *renewFixture) waitCuts(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for fx.cutCount() < n {
		if time.Now().After(deadline) {
			t.Fatalf("%d cut timers started, want %d", fx.cutCount(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (fx *renewFixture) tagAt(n int) string {
	return strings.TrimSuffix(fx.tag0, ":0") + ":" + fmt.Sprint(n)
}

func (fx *renewFixture) decide(started time.Time) string {
	return fx.d.considerCircuitRenewal(context.Background(), fx.peer.ID, fx.tag(), fx.addr, routeFailTimeout, started, false, nil)
}

func (fx *renewFixture) decideMoved(started time.Time) string {
	return fx.d.considerCircuitRenewal(context.Background(), fx.peer.ID, fx.tag(), fx.addr, routeFailTimeout, started, true, nil)
}

func mustStayOnTheRequest(t *testing.T) {
	t.Helper()
	goCircuitRenewal = func(func()) { t.Error("the decision moved off the request's goroutine") }
}

func (fx *renewFixture) tag() string { return fx.d.peerIsoTag(fx.peer.ID) }

func (fx *renewFixture) closes() []string {
	var out []string
	for _, c := range fx.f.sent() {
		if strings.HasPrefix(c.line, "CLOSECIRCUIT ") {
			out = append(out, c.line)
		}
	}
	return out
}

func (fx *renewFixture) post(t *testing.T, host string) bool {
	t.Helper()
	hc, err := fx.d.httpClientForPeer(fx.peer.ID)
	if err != nil {
		t.Fatal(err)
	}
	hc.Timeout = 300 * time.Millisecond
	resp, err := hc.Post("http://"+host+".onion/message", "application/json", strings.NewReader("{}"))
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return true
}

func (fx *renewFixture) stallTag(tag string) {
	fx.socks.mu.Lock()
	fx.socks.stall = func(got, _ string) bool { return got == tag }
	fx.socks.mu.Unlock()
}

type statusRT int

func (s statusRT) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: int(s),
		Body:       io.NopCloser(strings.NewReader("some body")),
		Header:     make(http.Header),
		Request:    r,
	}, nil
}

func TestCircuitRenewal_TimeoutMovesTheTag(t *testing.T) {
	fx := newRenewFixture(t)
	fx.stallTag(fx.plainTag())
	if fx.post(t, fx.addr) {
		t.Fatal("the request under the stalled tag was answered")
	}
	if got, want := fx.tag(), fx.tagAt(1); got != want {
		t.Fatalf("tag after one timeout = %q, want %q", got, want)
	}
	if !fx.post(t, fx.addr) {
		t.Fatalf("the request after the renewal was not answered; connects %q", fx.socks.seen())
	}
	seen := fx.socks.seen()
	if last := seen[len(seen)-1]; last != fx.tagAt(1)+" "+fx.addr {
		t.Fatalf("last CONNECT = %q, want the new tag to the peer", last)
	}
	if got := fx.closes(); len(got) != 0 {
		t.Fatalf("the automatic renewal closed circuits: %q", got)
	}
}

func TestCircuitRenewal_NextQueuedRowGoesOutOnTheNewTag(t *testing.T) {
	fx := newRenewFixture(t)
	fx.d.onInboundVerified(context.Background(), fx.peer.ID)
	mustStayOnTheRequest(t)
	fx.stallTag(fx.plainTag())
	if fx.post(t, fx.addr) {
		t.Fatal("the first row, under the stalled tag, was answered")
	}
	if !fx.post(t, fx.addr) {
		t.Fatalf("the second row was not answered; connects %q", fx.socks.seen())
	}
	seen := fx.socks.seen()
	if len(seen) != 2 || seen[0] != fx.plainTag()+" "+fx.addr || seen[1] != fx.tagAt(1)+" "+fx.addr {
		t.Fatalf("connects = %q, want the start tag and then the new tag", seen)
	}
}

func TestRouteBook_TagHasAPartPerStart(t *testing.T) {
	var a, b routeBook
	ta, tb := a.tag("p1"), b.tag("p1")
	if ta == tb {
		t.Fatalf("two starts gave the same tag %q", ta)
	}
	parts := strings.Split(ta, ":")
	if len(parts) != 4 || parts[0] != "haomad-peer" || parts[1] != "p1" || len(parts[2]) != 8 || parts[3] != "0" {
		t.Fatalf("tag %q, want haomad-peer:p1:<8 hex>:0", ta)
	}
	if again := a.tag("p1"); again != ta {
		t.Fatalf("one start gave two tags %q and %q", ta, again)
	}
	if other := a.tag("p2"); !strings.HasPrefix(other, "haomad-peer:p2:"+parts[2]+":") {
		t.Fatalf("tag of another peer %q does not share the start's part", other)
	}
	a.renew("p1", time.Now())
	if got := a.tag("p1"); got != "haomad-peer:p1:"+parts[2]+":1" {
		t.Fatalf("tag after a renewal = %q", got)
	}
}

func TestCircuitRenewal_SecondTimeoutInWindowDoesNotRenew(t *testing.T) {
	fx := newRenewFixture(t)
	fx.d.onInboundVerified(context.Background(), fx.peer.ID)
	if why := fx.decide(time.Now()); why != circuitRenewed {
		t.Fatalf("first timeout: outcome %q, want %q", why, circuitRenewed)
	}
	if why := fx.decide(time.Now()); why != circuitInWindow {
		t.Fatalf("second timeout: outcome %q, want %q", why, circuitInWindow)
	}
	if got, want := fx.tag(), fx.tagAt(1); got != want {
		t.Fatalf("tag after two timeouts in one window = %q, want %q", got, want)
	}
}

func TestCircuitRenewal_FailureUnderAnOlderTagIsNotCounted(t *testing.T) {
	fx := newRenewFixture(t)
	fx.d.onInboundVerified(context.Background(), fx.peer.ID)
	old := fx.tag()
	fx.decide(time.Now())
	why := fx.d.considerCircuitRenewal(context.Background(), fx.peer.ID, old, fx.addr, routeFailTimeout, time.Now(), false, nil)
	if why != circuitOldTag {
		t.Fatalf("outcome %q, want %q", why, circuitOldTag)
	}
}

func TestCircuitRenewal_WindowIsFixed(t *testing.T) {
	fx := newRenewFixture(t)
	fx.d.onInboundVerified(context.Background(), fx.peer.ID)
	next := func() time.Duration {
		fx.d.routes.mu.Lock()
		defer fx.d.routes.mu.Unlock()
		return time.Until(fx.d.routes.peers[fx.peer.ID].nextRenewAt)
	}
	pass := func() {
		fx.d.routes.mu.Lock()
		fx.d.routes.peers[fx.peer.ID].nextRenewAt = time.Now().Add(-time.Second)
		fx.d.routes.mu.Unlock()
	}
	for i := 1; i <= 4; i++ {
		if why := fx.decide(time.Now()); why != circuitRenewed {
			t.Fatalf("renewal %d: outcome %q, want %q", i, why, circuitRenewed)
		}
		got := next()
		if got < circuitRenewWindow-time.Second || got > circuitRenewWindow+circuitRenewJitter {
			t.Fatalf("after renewal %d: next renewal in %v, want between %v and %v", i, got, circuitRenewWindow, circuitRenewWindow+circuitRenewJitter)
		}
		pass()
	}
}

func TestCircuitRenewal_HTTPAnswerDoesNotRenew(t *testing.T) {
	for _, code := range []int{http.StatusGone, http.StatusNotFound, http.StatusBadRequest, http.StatusUnauthorized, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			fx := newRenewFixture(t)
			hc := fx.d.watchRoute(fx.peer.ID, fx.tag(), &http.Client{Transport: statusRT(code)})
			for range 2 {
				resp, err := hc.Get("http://" + fx.addr + ".onion/message")
				if err != nil {
					t.Fatal(err)
				}
				_ = resp.Body.Close()
			}
			if got := fx.tag(); got != fx.plainTag() {
				t.Fatalf("tag after a %d answer = %q, want the start tag: the circuit carried the answer", code, got)
			}
		})
	}
}

func TestCircuitRenewal_BodyBytesCountAsAnswer(t *testing.T) {
	fx := newRenewFixture(t)
	fx.d.onInboundVerified(context.Background(), fx.peer.ID)
	hc := fx.d.watchRoute(fx.peer.ID, fx.tag(), &http.Client{Transport: statusRT(http.StatusOK)})
	resp, err := hc.Get("http://" + fx.addr + ".onion/files/x")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	started := time.Now()
	time.Sleep(10 * time.Millisecond)
	buf := make([]byte, 4)
	if n, _ := resp.Body.Read(buf); n == 0 {
		t.Fatal("no bytes read")
	}
	if why := fx.decide(started); why != circuitAnsweredMeantime {
		t.Fatalf("outcome %q, want %q: the body was flowing", why, circuitAnsweredMeantime)
	}
}

func TestCircuitRenewal_AnswerUnderAnotherTagDoesNotHold(t *testing.T) {
	fx := newRenewFixture(t)
	fx.d.onInboundVerified(context.Background(), fx.peer.ID)
	started := time.Now().Add(-time.Minute)
	fx.d.routes.answered(fx.peer.ID, "haomad-peer:other:tag:0", fx.addr, fx.peer.KnownAddresses, time.Now())
	if why := fx.decide(started); why != circuitRenewed {
		t.Fatalf("outcome %q, want %q", why, circuitRenewed)
	}
}

func TestCircuitRenewal_SelfProbeRedDoesNotRenew(t *testing.T) {
	fx := newRenewFixture(t)
	setSelfProbe(fx.d, fx.peer, false)
	fx.decide(time.Now())
	if got := fx.tag(); got != fx.plainTag() {
		t.Fatalf("tag while the self-probe is red = %q, want the start tag", got)
	}
	if why := fx.decideMoved(time.Now()); why != circuitOwnSideRed {
		t.Fatalf("outcome %q, want %q", why, circuitOwnSideRed)
	}

	setSelfProbe(fx.d, fx.peer, true)
	if why := fx.decideMoved(time.Now()); why != circuitRenewed {
		t.Fatalf("after the self-probe went green: outcome %q, want %q", why, circuitRenewed)
	}
}

func TestCircuitRenewal_SelfProbeProofMovesOff(t *testing.T) {
	fx := newRenewFixture(t)
	moved := false
	goCircuitRenewal = func(fn func()) { moved = true; fn() }
	if why := fx.decide(time.Now()); why != circuitMovedOn {
		t.Fatalf("outcome %q, want %q", why, circuitMovedOn)
	}
	if !moved || fx.tag() != fx.tagAt(1) {
		t.Fatalf("moved=%v tag=%q, want the moved part to renew", moved, fx.tag())
	}
}

func TestCircuitRenewal_InboundEnvelopeStandsInForSelfProbe(t *testing.T) {
	fx := newRenewFixture(t)
	setSelfProbe(fx.d, fx.peer, false)
	fx.d.onInboundVerified(context.Background(), fx.peer.ID)
	mustStayOnTheRequest(t)
	if why := fx.decide(time.Now()); why != circuitRenewed {
		t.Fatalf("outcome %q with a fresh inbound envelope, want %q", why, circuitRenewed)
	}
}

func TestCircuitRenewal_OldInboundEnvelopeIsNoProof(t *testing.T) {
	fx := newRenewFixture(t)
	setSelfProbe(fx.d, fx.peer, false)
	fx.d.routes.inbound(fx.peer.ID, time.Now().Add(-circuitInboundProof-time.Minute))
	if why := fx.decideMoved(time.Now()); why != circuitOwnSideRed {
		t.Fatalf("outcome %q with only an old inbound envelope, want %q", why, circuitOwnSideRed)
	}
}

func TestCircuitRenewal_ActiveCallDoesNotRenew(t *testing.T) {
	fx := newRenewFixture(t)
	fx.d.onInboundVerified(context.Background(), fx.peer.ID)
	fx.d.proxy = proxy.NewManager(nil)
	const token = "calltokencalltokencalltoken"
	startCall(t, fx.d, fx.addr, token)
	if why := fx.decide(time.Now()); why != circuitCallActive {
		t.Fatalf("outcome %q, want %q", why, circuitCallActive)
	}
	fx.d.proxy.Cancel(token)
	if why := fx.decide(time.Now()); why != circuitRenewed {
		t.Fatalf("after the call ended: outcome %q, want %q", why, circuitRenewed)
	}
}

func TestCircuitRenewal_CallWithAnotherPeerDoesNotHold(t *testing.T) {
	fx := newRenewFixture(t)
	fx.d.onInboundVerified(context.Background(), fx.peer.ID)
	fx.d.proxy = proxy.NewManager(nil)
	startCall(t, fx.d, sid("someoneelse"), "othertokenothertokenothertoken")
	if why := fx.decide(time.Now()); why != circuitRenewed {
		t.Fatalf("outcome %q with a call to another onion, want %q", why, circuitRenewed)
	}
}

func TestCircuitRenewal_AnswerDuringFailedRequestDoesNotRenew(t *testing.T) {
	fx := newRenewFixture(t)
	fx.d.onInboundVerified(context.Background(), fx.peer.ID)
	started := time.Now().Add(-time.Minute)
	fx.d.routes.answered(fx.peer.ID, fx.tag(), fx.addr, fx.peer.KnownAddresses, time.Now())
	if why := fx.decide(started); why != circuitAnsweredMeantime {
		t.Fatalf("outcome %q, want %q", why, circuitAnsweredMeantime)
	}
}

func TestCircuitRenewal_RecheckedAfterTheSelfProbe(t *testing.T) {
	t.Run("answer", func(t *testing.T) {
		fx := newRenewFixture(t)
		staleSelfProbe(fx.d, fx.peer)
		tag := fx.tag()
		fx.socks.onConnect = func(_, host string) {
			if host == fx.peer.MyOnionAddr {
				fx.d.routes.answered(fx.peer.ID, tag, fx.addr, fx.peer.KnownAddresses, time.Now())
			}
		}
		if why := fx.decideMoved(time.Now()); why != circuitAnsweredMeantime {
			t.Fatalf("outcome %q, want %q", why, circuitAnsweredMeantime)
		}
	})
	t.Run("call", func(t *testing.T) {
		fx := newRenewFixture(t)
		fx.d.proxy = proxy.NewManager(nil)
		staleSelfProbe(fx.d, fx.peer)
		fx.socks.onConnect = func(_, host string) {
			if host == fx.peer.MyOnionAddr {
				startCall(t, fx.d, fx.addr, "latecalltokenlatecalltoken")
			}
		}
		if why := fx.decideMoved(time.Now()); why != circuitCallActive {
			t.Fatalf("outcome %q, want %q", why, circuitCallActive)
		}
	})
	t.Run("nothing changed", func(t *testing.T) {
		fx := newRenewFixture(t)
		staleSelfProbe(fx.d, fx.peer)
		if why := fx.decideMoved(time.Now()); why != circuitRenewed {
			t.Fatalf("outcome %q after a green self-probe dial, want %q", why, circuitRenewed)
		}
	})
}

func TestCircuitRenewal_SelfProbeHostIsNotThePeersRoute(t *testing.T) {
	fx := newRenewFixture(t)
	fx.stallTag(fx.plainTag())
	fx.post(t, fx.peer.MyOnionAddr)
	if got := fx.tag(); got != fx.plainTag() {
		t.Fatalf("tag after a self-probe timeout = %q, want the start tag", got)
	}

	fx.d.routes.failed(fx.peer.ID, fx.tag(), time.Now())
	fx.d.routes.answered(fx.peer.ID, fx.tag(), fx.peer.MyOnionAddr, fx.peer.KnownAddresses, time.Now())
	fx.d.routes.mu.Lock()
	s := fx.d.routes.peers[fx.peer.ID]
	failures, held := s.failures, len(s.answeredAt)
	fx.d.routes.mu.Unlock()
	if failures != 1 {
		t.Fatalf("failures after an answer from this device's own onion = %d, want 1", failures)
	}
	if held != 0 {
		t.Fatalf("answeredAt holds %d entries, want none: only the peer's addresses count", held)
	}
}

func TestCircuitRenewal_NeedsNoControlPort(t *testing.T) {
	fx := newRenewFixture(t)
	fx.d.onInboundVerified(context.Background(), fx.peer.ID)
	fx.d.ctrlConn = nil
	if why := fx.decide(time.Now()); why != circuitRenewed {
		t.Fatalf("outcome %q without tor control, want %q", why, circuitRenewed)
	}
}

func TestCircuitRenewal_DeletedPeerIsForgotten(t *testing.T) {
	fx := newRenewFixture(t)
	fx.decide(time.Now())
	req := httptest.NewRequest(http.MethodDelete, "/peers/"+fx.peer.ID, nil)
	req.SetPathValue("id", fx.peer.ID)
	rec := httptest.NewRecorder()
	fx.d.handleDeletePeer(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: status %d", rec.Code)
	}
	held := func() bool {
		fx.d.routes.mu.Lock()
		defer fx.d.routes.mu.Unlock()
		_, ok := fx.d.routes.peers[fx.peer.ID]
		return ok
	}
	if held() {
		t.Fatal("the route state of a deleted peer is still held")
	}

	fx.d.routes.failed(fx.peer.ID, fx.tag(), time.Now())
	fx.d.routes.renew(fx.peer.ID, time.Now())
	fx.d.routes.answered(fx.peer.ID, fx.tag(), fx.addr, fx.peer.KnownAddresses, time.Now())
	fx.d.routes.inbound(fx.peer.ID, time.Now())
	if held() {
		t.Fatal("a decision after the delete brought the entry back")
	}
}

func TestHTTPClientForPeer_CarriesRouteWatch(t *testing.T) {
	d := newTestDaemon(t)
	d.cfg.torSocks = "127.0.0.1:9"
	hc, err := d.httpClientForPeer("somepeer")
	if err != nil {
		t.Fatal(err)
	}
	rw, ok := hc.Transport.(*routeWatch)
	if !ok {
		t.Fatalf("transport is %T, want *routeWatch", hc.Transport)
	}
	if _, ok := rw.next.(*removalWatch); !ok {
		t.Fatalf("route watch wraps %T, want the removal watch under it", rw.next)
	}
	if rw.tag != d.peerIsoTag("somepeer") {
		t.Fatalf("route watch tag %q, want %q", rw.tag, d.peerIsoTag("somepeer"))
	}
}

func TestNewCircuitForPeer_ClosesAndStartsTheWindow(t *testing.T) {
	fx := newRenewFixture(t)
	fx.d.onInboundVerified(context.Background(), fx.peer.ID)
	req := httptest.NewRequest(http.MethodPost, "/peers/"+fx.peer.ID+"/new-circuit", nil)
	req.SetPathValue("id", fx.peer.ID)
	rec := httptest.NewRecorder()
	fx.d.handleNewCircuitForPeer(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"closed":1`) {
		t.Fatalf("status %d body %s, want 200 closed 1 (circuit 10 was unknown to tor)", rec.Code, rec.Body.String())
	}
	if got := fx.closes(); len(got) != 2 || got[0] != "CLOSECIRCUIT 7" || got[1] != "CLOSECIRCUIT 10" {
		t.Fatalf("closes = %q, want [CLOSECIRCUIT 7 CLOSECIRCUIT 10]", got)
	}
	if why := fx.decide(time.Now()); why != circuitInWindow {
		t.Fatalf("automatic renewal right after the person's: outcome %q, want %q", why, circuitInWindow)
	}
}

func TestNewCircuitForPeer_ControlPortThatDoesNotAnswer(t *testing.T) {
	fx := newRenewFixture(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(io.Discard, c) }()
		}
	}()
	fx.d.ctrlConn = dialFakeTor(t, ln.Addr().String())
	old := newCircuitControlTimeout
	newCircuitControlTimeout = 100 * time.Millisecond
	t.Cleanup(func() { newCircuitControlTimeout = old })

	press := func() int {
		req := httptest.NewRequest(http.MethodPost, "/peers/"+fx.peer.ID+"/new-circuit", nil)
		req.SetPathValue("id", fx.peer.ID)
		rec := httptest.NewRecorder()
		done := make(chan struct{})
		go func() { fx.d.handleNewCircuitForPeer(rec, req); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the handler waited on a control port that never answers")
		}
		return rec.Code
	}
	if code := press(); code != http.StatusGatewayTimeout {
		t.Fatalf("first press: status %d, want 504", code)
	}
	if code := press(); code != http.StatusConflict {
		t.Fatalf("second press while the first still waits on tor: status %d, want 409", code)
	}
}

func TestCircuitRenewal_BurstWaitsARandomDelay(t *testing.T) {
	fx := newRenewFixture(t)
	fx.d.onInboundVerified(context.Background(), fx.peer.ID)
	fx.d.routes.mu.Lock()
	fx.d.routes.state("anotherpeer").decidedAt = time.Now()
	fx.d.routes.mu.Unlock()
	renewDelay = func() time.Duration { return 17 * time.Second }
	var waited []time.Duration
	renewSleep = func(_ context.Context, d time.Duration) bool {
		waited = append(waited, d)
		if got := fx.tag(); got != fx.plainTag() {
			t.Errorf("the tag moved before the wait: %q", got)
		}
		return true
	}
	if why := fx.decide(time.Now()); why != circuitMovedOn {
		t.Fatalf("outcome %q, want %q", why, circuitMovedOn)
	}
	if len(waited) != 1 || waited[0] != 17*time.Second {
		t.Fatalf("waited %v, want one wait of the drawn 17s", waited)
	}
	if got := fx.tag(); got != fx.tagAt(1) {
		t.Fatalf("tag after the wait = %q, want %q", got, fx.tagAt(1))
	}
}

func TestCircuitRenewal_NoBurstNoWait(t *testing.T) {
	fx := newRenewFixture(t)
	fx.d.onInboundVerified(context.Background(), fx.peer.ID)
	fx.d.routes.mu.Lock()
	fx.d.routes.state("anotherpeer").decidedAt = time.Now().Add(-circuitRenewDelay - time.Second)
	fx.d.routes.mu.Unlock()
	renewSleep = func(context.Context, time.Duration) bool { t.Error("waited outside a burst"); return true }
	mustStayOnTheRequest(t)
	if why := fx.decide(time.Now()); why != circuitRenewed {
		t.Fatalf("outcome %q, want %q", why, circuitRenewed)
	}
}

func TestCircuitRenewal_RecheckedAfterTheDelay(t *testing.T) {
	burst := func(fx *renewFixture) {
		fx.d.onInboundVerified(context.Background(), fx.peer.ID)
		fx.d.routes.mu.Lock()
		fx.d.routes.state("anotherpeer").decidedAt = time.Now()
		fx.d.routes.mu.Unlock()
	}
	t.Run("answer", func(t *testing.T) {
		fx := newRenewFixture(t)
		burst(fx)
		tag := fx.tag()
		renewSleep = func(context.Context, time.Duration) bool {
			fx.d.routes.answered(fx.peer.ID, tag, fx.addr, fx.peer.KnownAddresses, time.Now())
			return true
		}
		if why := fx.decideMoved(time.Now().Add(-time.Second)); why != circuitAnsweredMeantime {
			t.Fatalf("outcome %q, want %q", why, circuitAnsweredMeantime)
		}
	})
	t.Run("call", func(t *testing.T) {
		fx := newRenewFixture(t)
		burst(fx)
		fx.d.proxy = proxy.NewManager(nil)
		renewSleep = func(context.Context, time.Duration) bool {
			startCall(t, fx.d, fx.addr, "delaycalltokendelaycalltoken")
			return true
		}
		if why := fx.decideMoved(time.Now()); why != circuitCallActive {
			t.Fatalf("outcome %q, want %q", why, circuitCallActive)
		}
	})
	t.Run("stopping", func(t *testing.T) {
		fx := newRenewFixture(t)
		burst(fx)
		renewSleep = func(context.Context, time.Duration) bool { return false }
		if why := fx.decideMoved(time.Now()); why != circuitStopped {
			t.Fatalf("outcome %q, want %q", why, circuitStopped)
		}
		renewSleep = func(context.Context, time.Duration) bool { return true }
		if why := fx.decideMoved(time.Now()); why != circuitRenewed {
			t.Fatalf("after a stopped wait: outcome %q, want %q (the right to decide was not given back)", why, circuitRenewed)
		}
	})
}
func TestRenewWindow(t *testing.T) {
	if got := renewWindow(); got != 3*time.Minute {
		t.Fatalf("renewWindow() = %v, want 3m", got)
	}
}

func TestOnInboundVerified_RecordsTheEnvelope(t *testing.T) {
	d := newTestDaemon(t)
	if d.routes.inboundSince("p1", time.Now().Add(-time.Minute)) {
		t.Fatal("inbound recorded before any envelope")
	}
	d.onInboundVerified(context.Background(), "p1")
	if !d.routes.inboundSince("p1", time.Now().Add(-time.Minute)) {
		t.Fatal("onInboundVerified did not record the envelope")
	}
}

func startCall(t *testing.T, d *daemon, host, token string) {
	t.Helper()
	media := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	if err := d.proxy.StartFetch(context.Background(), token, proxy.Modality("audio"), "http://"+host+".onion/audio/"+token, 40000, media); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.proxy.Cancel(token) })
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "net/http: timeout awaiting response headers" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestRouteFailReason(t *testing.T) {
	socks := func(inner error) error {
		return fmt.Errorf("xport: post: %w", &net.OpError{Op: "socks connect", Net: "tcp", Err: inner})
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancel2 := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel2()
	cases := []struct {
		name string
		ctx  context.Context
		err  error
		want string
	}{
		{"nil", context.Background(), nil, ""},
		{"request deadline passed", expired, errors.New("net/http: request canceled"), routeFailTimeout},
		{"caller canceled", canceled, context.Canceled, ""},
		{"deadline exceeded", context.Background(), fmt.Errorf("xport: post: %w", context.DeadlineExceeded), routeFailTimeout},
		{"response header timeout", context.Background(), timeoutErr{}, routeFailTimeout},
		{"socks general failure", context.Background(), socks(errors.New("unknown error general SOCKS server failure")), ""},
		{"socks TTL expired", context.Background(), socks(errors.New("unknown error TTL expired")), ""},
		{"socks host unreachable", context.Background(), socks(errors.New("unknown error host unreachable")), ""},
		{"socks connection refused", context.Background(), socks(errors.New("unknown error connection refused")), ""},
		{"local tor socks port refused", context.Background(), socks(&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}), ""},
		{"local tor socks port timed out", expired, socks(&net.OpError{Op: "dial", Net: "tcp", Err: timeoutErr{}}), ""},
		{"EOF", context.Background(), fmt.Errorf("xport: post: %w", io.EOF), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := routeFailReason(c.ctx, c.err); got != c.want {
				t.Fatalf("routeFailReason = %q, want %q", got, c.want)
			}
		})
	}
}

func TestPeerSelfReachPayload_HoldsNoOnion(t *testing.T) {
	ty := reflect.TypeOf(peerSelfReachPayload{})
	for i := range ty.NumField() {
		f := ty.Field(i)
		if strings.Contains(strings.ToLower(f.Name+" "+f.Tag.Get("json")), "onion") {
			t.Errorf("peerSelfReachPayload has field %s %q", f.Name, f.Tag.Get("json"))
		}
	}

	fx := newRenewFixture(t)
	req := httptest.NewRequest(http.MethodPost, "/peers/"+fx.peer.ID+"/self-probe", nil)
	req.SetPathValue("id", fx.peer.ID)
	rec := httptest.NewRecorder()
	fx.d.handlePeerSelfProbe(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, fx.peer.MyOnionAddr) || strings.Contains(body, "onion") {
		t.Fatalf("self-probe reply holds an onion: %s", body)
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
}

func TestRenewDelay_WithinBounds(t *testing.T) {
	seen := map[time.Duration]bool{}
	for range 1000 {
		d := renewDelay()
		if d < 0 || d >= circuitRenewDelay {
			t.Fatalf("renewDelay() = %v, want in [0, %v)", d, circuitRenewDelay)
		}
		seen[d/time.Second] = true
	}
	if len(seen) < 10 {
		t.Fatalf("renewDelay drew only %d distinct seconds in 1000 draws", len(seen))
	}
}

func TestRenewSleep_EndsWithTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if renewSleep(ctx, time.Hour) {
		t.Fatal("renewSleep reported a full wait on an ended context")
	}
	if time.Since(start) > time.Second {
		t.Fatal("renewSleep did not return at once")
	}
}

func (fx *renewFixture) sendOnce(timeout time.Duration) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	start := time.Now()
	_, err := (&perPeerSender{d: fx.d}).Send(ctx, "http://"+fx.addr+".onion", xport.Envelope{ID: "e1", Timestamp: 1})
	return time.Since(start), err
}

func (fx *renewFixture) warm(t *testing.T) {
	t.Helper()
	if !fx.post(t, fx.addr) {
		t.Fatal("the warming request was not answered")
	}
	if fx.cutCount() != 0 {
		t.Fatal("a request on a tag that never answered got a cut timer")
	}
}

func TestCircuitRenewal_CutAt30sThenSendAgainOnTheNewTag(t *testing.T) {
	fx := newRenewFixture(t)
	fx.d.onInboundVerified(context.Background(), fx.peer.ID)
	fx.warm(t)
	fx.stallTag(fx.plainTag())

	type result struct {
		took time.Duration
		err  error
	}
	done := make(chan result, 1)
	go func() {
		took, err := fx.sendOnce(60 * time.Second)
		done <- result{took, err}
	}()
	fx.waitCuts(t, 1)
	fx.fireCut(t, 0)
	var r result
	select {
	case r = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the send was not cut when the renewal ran")
	}
	if !errors.Is(r.err, xport.ErrCircuitRenewed) {
		t.Fatalf("send error = %v, want ErrCircuitRenewed", r.err)
	}
	if got := fx.tag(); got != fx.tagAt(1) {
		t.Fatalf("tag after the cut = %q, want %q", got, fx.tagAt(1))
	}
	if _, err := fx.sendOnce(60 * time.Second); err != nil {
		t.Fatalf("the send again on the new tag: %v", err)
	}
	seen := fx.socks.seen()
	if last := seen[len(seen)-1]; last != fx.tagAt(1)+" "+fx.addr {
		t.Fatalf("last CONNECT = %q, want the new tag", last)
	}
}

func TestCircuitRenewal_CutOnlyOnAWarmTag(t *testing.T) {
	fx := newRenewFixture(t)
	fx.d.onInboundVerified(context.Background(), fx.peer.ID)
	now := time.Now()
	if fx.d.routes.cutDue(fx.peer.ID, fx.plainTag(), now) {
		t.Fatal("cutDue on a start tag that never answered")
	}
	fx.d.routes.answered(fx.peer.ID, fx.plainTag(), fx.addr, fx.peer.KnownAddresses, now)
	if !fx.d.routes.cutDue(fx.peer.ID, fx.plainTag(), now.Add(time.Minute)) {
		t.Fatal("no cut on a tag that answered a minute ago")
	}

	fx.d.routes.answered(fx.peer.ID, fx.plainTag(), fx.addr, fx.peer.KnownAddresses, now.Add(9*time.Minute))
	if fx.d.routes.cutDue(fx.peer.ID, fx.plainTag(), now.Add(circuitWarmFor+time.Second)) {
		t.Fatal("cutDue on a tag whose first answer is more than ten minutes old")
	}
	if why := fx.decide(now.Add(10 * time.Minute)); why != circuitRenewed {
		t.Fatalf("outcome %q, want %q", why, circuitRenewed)
	}
	if fx.d.routes.cutDue(fx.peer.ID, fx.tag(), time.Now()) {
		t.Fatal("cutDue on the new tag before it answered")
	}
	if fx.d.routes.cutDue(fx.peer.ID, fx.plainTag(), time.Now()) {
		t.Fatal("cutDue on the older tag")
	}
	before := fx.cutCount()
	if !fx.post(t, fx.addr) {
		t.Fatal("the request on the new tag was not answered")
	}
	if fx.cutCount() != before {
		t.Fatal("the first request on the new tag got a cut timer")
	}
}

func TestCircuitRenewal_GatesHoldAtTheCut(t *testing.T) {
	for name, set := range map[string]func(fx *renewFixture){
		"self-probe red, no inbound envelope": func(fx *renewFixture) { setSelfProbe(fx.d, fx.peer, false) },
		"a call is pulling": func(fx *renewFixture) {
			fx.d.onInboundVerified(context.Background(), fx.peer.ID)
			fx.d.proxy = proxy.NewManager(nil)
			startCall(t, fx.d, fx.addr, "cutcalltokencutcalltoken")
		},
		"window not passed": func(fx *renewFixture) {
			fx.d.onInboundVerified(context.Background(), fx.peer.ID)
			fx.d.routes.mu.Lock()
			fx.d.routes.state(fx.peer.ID).nextRenewAt = time.Now().Add(time.Hour)
			fx.d.routes.mu.Unlock()
		},
	} {
		t.Run(name, func(t *testing.T) {
			fx := newRenewFixture(t)
			fx.warm(t)
			set(fx)
			fx.stallTag(fx.plainTag())
			done := make(chan error, 1)
			go func() {
				_, err := fx.sendOnce(time.Second)
				done <- err
			}()
			fx.waitCuts(t, 1)
			fx.fireCut(t, 0)
			err := <-done
			if errors.Is(err, xport.ErrCircuitRenewed) {
				t.Fatalf("the request was cut: %v", err)
			}
			if got := fx.tag(); got != fx.plainTag() {
				t.Fatalf("tag = %q, want the start tag", got)
			}
		})
	}
}

func TestCircuitRenewal_NoCutAfterTheConnection(t *testing.T) {
	fx := newRenewFixture(t)
	fx.d.onInboundVerified(context.Background(), fx.peer.ID)
	fx.warm(t)
	tag := fx.plainTag()
	fx.socks.mu.Lock()
	fx.socks.stallHTTP = func(got string) bool { return got == tag }
	fx.socks.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, err := fx.sendOnce(2 * time.Second)
		done <- err
	}()
	fx.waitCuts(t, 1)

	deadline := time.Now().Add(5 * time.Second)
	for len(fx.socks.seenBodies()) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("the request never reached the connection")
		}
		time.Sleep(5 * time.Millisecond)
	}
	fx.fireCut(t, 0)
	err := <-done
	if errors.Is(err, xport.ErrCircuitRenewed) {
		t.Fatalf("a connected request was cut: %v", err)
	}
	if err == nil {
		t.Fatal("the stalled answer came back")
	}
}

func TestRouteFailNoAnswer_NamesTheCutTime(t *testing.T) {
	if want := fmt.Sprintf("no answer in %d s", int(circuitCutAfter/time.Second)); routeFailNoAnswer != want {
		t.Fatalf("routeFailNoAnswer = %q, want %q", routeFailNoAnswer, want)
	}
}

func cutAndCount(t *testing.T, fx *renewFixture, run func()) []string {
	t.Helper()
	fx.d.onInboundVerified(context.Background(), fx.peer.ID)
	fx.warm(t)
	fx.stallTag(fx.plainTag())
	done := make(chan struct{})
	go func() { run(); close(done) }()
	fx.waitCuts(t, 1)
	fx.fireCut(t, 0)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the destroy request did not finish after the cut")
	}
	return fx.socks.seenBodies()[1:]
}

func envelopeIDIn(t *testing.T, rec string) (tag, id string) {
	t.Helper()
	tag, body, _ := strings.Cut(rec, " ")
	var env xport.Envelope
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("body %q: %v", body, err)
	}
	return tag, env.ID
}

func TestDestroyVerify_CutSendsTheSameCheckAgainOnTheNewTag(t *testing.T) {
	fx := newRenewFixture(t)
	var rec *httptest.ResponseRecorder
	bodies := cutAndCount(t, fx, func() {
		body := `{"peer_id":"` + fx.peer.ID + `","proof":"p"}`
		req := httptest.NewRequest(http.MethodPost, "/destroy-pin/verify", strings.NewReader(body))
		rec = httptest.NewRecorder()
		fx.d.handleDestroyVerifySend(rec, req)
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if len(bodies) != 1 {
		t.Fatalf("the peer got %d checks, want 1: the cut one wrote nothing", len(bodies))
	}
	tag, id := envelopeIDIn(t, bodies[0])
	if tag != fx.tagAt(1) || id == "" {
		t.Fatalf("the check came under %q with id %q, want the new tag", tag, id)
	}
}

func TestDestroyCommand_CutSendsTheSameCommandAgainOnTheNewTag(t *testing.T) {
	fx := newRenewFixture(t)
	var envID string
	var err error
	bodies := cutAndCount(t, fx, func() {
		req := httptest.NewRequest(http.MethodPost, "/unlink", nil)
		envID, _, err = fx.d.sendDestroyCommand(req, fx.peer, "p")
	})
	if err != nil {
		t.Fatalf("sendDestroyCommand: %v", err)
	}
	if len(bodies) != 1 {
		t.Fatalf("the peer got %d commands, want 1: the cut one wrote nothing", len(bodies))
	}
	tag, id := envelopeIDIn(t, bodies[0])
	if tag != fx.tagAt(1) || id != envID {
		t.Fatalf("the command came under %q with id %q, want the new tag and id %q", tag, id, envID)
	}
}
