package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"haoma/internal/peers"
	"haoma/internal/tor/control"
	"haoma/internal/xport"
)

const circuitRenewWindow = 3 * time.Minute

const circuitCutAfter = 30 * time.Second

const circuitWarmFor = 10 * time.Minute

var startCutTimer = func(d time.Duration, f func()) (stop func() bool) {
	return time.AfterFunc(d, f).Stop
}

const circuitRenewJitter = 90 * time.Second

const circuitRenewDelay = 30 * time.Second

var (
	renewDelay = func() time.Duration { return rand.N(circuitRenewDelay) }
	renewSleep = func(ctx context.Context, d time.Duration) bool {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
			return true
		case <-ctx.Done():
			return false
		}
	}
)

const circuitInboundProof = 10 * time.Minute

const routeFailTimeout = "timeout"

const routeFailNoAnswer = "no answer in 30 s"

const (
	routeProofInbound   = "inbound envelope"
	routeProofSelfProbe = "self-probe"
)

const (
	circuitRenewed          = "renewed"
	circuitNoPeer           = "peer not in the registry"
	circuitNotPeerAddress   = "not an address of the peer"
	circuitOldTag           = "the request was under an older tag"
	circuitAnsweredMeantime = "the peer answered meanwhile"
	circuitCallActive       = "a call is pulling from the peer"
	circuitInWindow         = "renewed recently"
	circuitBusy             = "another decision is running"
	circuitOwnSideRed       = "self-probe red and no recent inbound envelope"
	circuitStopped          = "the daemon is stopping"
	circuitMovedOn          = "moved to its own goroutine"
)

const circuitRenewedLine = "automatic new tor circuit for peer"

var goCircuitRenewal = func(f func()) { go f() }

type routeState struct {
	gen int

	answeredAt map[string]time.Time

	inboundAt time.Time

	firstAnsweredAt map[string]time.Time

	nextRenewAt time.Time

	decidedAt time.Time

	deciding bool

	failures int
	renewals int
}

type routeBook struct {
	mu    sync.Mutex
	boot  string
	peers map[string]*routeState

	gone map[string]bool

	manual map[string]bool
}

func (b *routeBook) state(peerID string) *routeState {
	if b.gone[peerID] {
		return &routeState{answeredAt: make(map[string]time.Time)}
	}
	if b.peers == nil {
		b.peers = make(map[string]*routeState)
	}
	s, ok := b.peers[peerID]
	if !ok {
		s = &routeState{answeredAt: make(map[string]time.Time)}
		b.peers[peerID] = s
	}
	return s
}

func (b *routeBook) bootTag() string {
	if b.boot == "" {
		var buf [4]byte
		if _, err := randRead(buf[:]); err != nil {

			b.boot = "0"
		} else {
			b.boot = hex.EncodeToString(buf[:])
		}
	}
	return b.boot
}

func (b *routeBook) tag(peerID string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	gen := 0
	if s, ok := b.peers[peerID]; ok {
		gen = s.gen
	}
	return fmt.Sprintf("haomad-peer:%s:%s:%d", peerID, b.bootTag(), gen)
}

func (b *routeBook) currentTag(peerID string, s *routeState) string {
	return fmt.Sprintf("haomad-peer:%s:%s:%d", peerID, b.bootTag(), s.gen)
}

func (b *routeBook) answered(peerID, tag, host string, addrs []string, at time.Time) {
	if !hasAddress(addrs, host) {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.state(peerID)
	s.answeredAt[tag] = at
	if s.firstAnsweredAt == nil {
		s.firstAnsweredAt = make(map[string]time.Time)
	}
	if _, ok := s.firstAnsweredAt[tag]; !ok {
		s.firstAnsweredAt[tag] = at
	}
	s.failures = 0
	s.renewals = 0
}

func (b *routeBook) inbound(peerID string, at time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state(peerID).inboundAt = at
}

func (b *routeBook) forget(peerID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.peers, peerID)
	if b.gone == nil {
		b.gone = make(map[string]bool)
	}
	b.gone[peerID] = true
}

func (b *routeBook) failed(peerID, tag string, started time.Time) (failures int, why string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.state(peerID)
	if tag != b.currentTag(peerID, s) {
		return s.failures, circuitOldTag
	}
	if at, ok := s.answeredAt[tag]; ok && at.After(started) {
		return s.failures, circuitAnsweredMeantime
	}
	s.failures++
	return s.failures, ""
}

func (b *routeBook) stillDue(peerID, tag string, started time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.state(peerID)
	if tag != b.currentTag(peerID, s) {
		return false
	}
	at, ok := s.answeredAt[tag]
	return !ok || !at.After(started)
}

func (b *routeBook) begin(peerID string, now time.Time) (ok bool, why string, burst bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.state(peerID)
	if s.deciding {
		return false, circuitBusy, false
	}
	if now.Before(s.nextRenewAt) {
		return false, circuitInWindow, false
	}
	for id, o := range b.peers {
		if id != peerID && !o.decidedAt.IsZero() && now.Sub(o.decidedAt) < circuitRenewDelay {
			burst = true
			break
		}
	}
	s.deciding = true
	s.decidedAt = now
	return true, "", burst
}

func renewWindow() time.Duration { return circuitRenewWindow }

func (b *routeBook) cutDue(peerID, tag string, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.state(peerID)
	if tag != b.currentTag(peerID, s) {
		return false
	}
	first, ok := s.firstAnsweredAt[tag]
	return ok && now.Sub(first) < circuitWarmFor
}

func (b *routeBook) renew(peerID string, now time.Time) (renewals, gen int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.state(peerID)
	s.deciding = false
	s.gen++
	s.renewals++
	s.nextRenewAt = now.Add(renewWindow() + rand.N(circuitRenewJitter))
	clear(s.answeredAt)
	clear(s.firstAnsweredAt)
	return s.renewals, s.gen
}

func (b *routeBook) release(peerID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state(peerID).deciding = false
}

func (b *routeBook) arm(peerID string, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.state(peerID)
	next := now.Add(renewWindow() + rand.N(circuitRenewJitter))
	if next.After(s.nextRenewAt) {
		s.nextRenewAt = next
	}
}

func (b *routeBook) startManual(peerID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.manual[peerID] {
		return false
	}
	if b.manual == nil {
		b.manual = make(map[string]bool)
	}
	b.manual[peerID] = true
	return true
}

func (b *routeBook) endManual(peerID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.manual, peerID)
}

func (b *routeBook) inboundSince(peerID string, since time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state(peerID).inboundAt.After(since)
}

func (d *daemon) onInboundVerified(ctx context.Context, peerID string) {
	go d.kickPeers([]string{peerID}, kickSignalInbound)
	d.routes.inbound(peerID, time.Now())

	d.noteInboundForMark(peerID)

	d.noteSelfReachFromEnvelope(peerID, xport.HostFromContext(ctx))
}

func routeFailReason(ctx context.Context, err error) string {
	if err == nil {
		return ""
	}

	var op *net.OpError
	if errors.As(err, &op) && op.Op == "socks connect" {
		var inner *net.OpError
		if errors.As(op.Err, &inner) {
			return ""
		}
	}
	if ctx != nil {
		if dl, ok := ctx.Deadline(); ok && !time.Now().Before(dl) {
			return routeFailTimeout
		}
		if errors.Is(ctx.Err(), context.Canceled) {

			return ""
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return routeFailTimeout
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {

		return routeFailTimeout
	}
	return ""
}

func (d *daemon) watchRoute(peerID, tag string, hc *http.Client) *http.Client {
	if hc == nil {
		return nil
	}
	next := hc.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	hc.Transport = &routeWatch{d: d, peerID: peerID, tag: tag, next: next}
	return hc
}

type routeWatch struct {
	d      *daemon
	peerID string
	tag    string
	next   http.RoundTripper

	addrsOnce sync.Once
	addrs     []string
}

func (t *routeWatch) peerAddrs() []string {
	t.addrsOnce.Do(func() {
		if t.d.registry == nil {
			return
		}
		if p, err := t.d.registry.Get(t.peerID); err == nil {
			t.addrs = p.KnownAddresses
		}
	})
	return t.addrs
}

func (t *routeWatch) RoundTrip(req *http.Request) (*http.Response, error) {
	started := time.Now()
	host := strings.TrimSuffix(req.URL.Hostname(), ".onion")
	addrs := t.peerAddrs()
	peerHost := hasAddress(addrs, host)

	callerCtx := req.Context()

	var (
		state  atomic.Int32
		cancel context.CancelCauseFunc = func(error) {}
		stop                           = func() bool { return false }
	)
	if peerHost && t.d.routes.cutDue(t.peerID, t.tag, started) {
		var cctx context.Context
		cctx, cancel = context.WithCancelCause(callerCtx)
		cctx = httptrace.WithClientTrace(cctx, &httptrace.ClientTrace{
			GotConn: func(httptrace.GotConnInfo) { state.CompareAndSwap(cutWaiting, cutConnected) },
		})
		req = req.WithContext(cctx)
		stop = startCutTimer(circuitCutAfter, func() {
			if state.Load() != cutWaiting {
				return
			}
			t.d.considerCircuitRenewal(t.d.renewCtx(), t.peerID, t.tag, host, routeFailNoAnswer, started, false, func() {
				if state.CompareAndSwap(cutWaiting, cutTaken) {
					cancel(xport.ErrCircuitRenewed)
				}
			})
		})
	}

	resp, err := t.next.RoundTrip(req)
	stop()
	if state.Load() == cutTaken {

		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		cancel(nil)
		return nil, fmt.Errorf("%w; the request had no connection after %s", xport.ErrCircuitRenewed, circuitCutAfter)
	}
	if err == nil {
		if peerHost {
			t.d.routes.answered(t.peerID, t.tag, host, addrs, time.Now())
			if resp != nil && resp.Body != nil {
				resp.Body = &answerBody{ReadCloser: resp.Body, w: t, host: host, release: cancel}
			} else {
				cancel(nil)
			}
		} else {
			cancel(nil)
		}
		return resp, nil
	}
	reason := routeFailReason(callerCtx, err)
	cancel(nil)
	if reason != "" {

		t.d.considerCircuitRenewal(t.d.renewCtx(), t.peerID, t.tag, host, reason, started, false, nil)
	}
	return resp, err
}

const (
	cutWaiting int32 = iota
	cutConnected
	cutTaken
)

type answerBody struct {
	io.ReadCloser
	w       *routeWatch
	host    string
	last    time.Time
	release context.CancelCauseFunc
}

func (b *answerBody) Close() error {
	err := b.ReadCloser.Close()
	b.release(nil)
	return err
}

func (b *answerBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		if now := time.Now(); now.Sub(b.last) >= time.Second {
			b.last = now
			b.w.d.routes.answered(b.w.peerID, b.w.tag, b.host, b.w.peerAddrs(), now)
		}
	}
	return n, err
}

func (d *daemon) renewCtx() context.Context {
	if d.bgCtx != nil {
		return d.bgCtx
	}
	return context.Background()
}

func (d *daemon) callActiveWith(p *peers.Peer) bool {
	if d.proxy == nil {
		return false
	}
	return d.proxy.FetchingFrom(func(host string) bool {
		return hasAddress(p.KnownAddresses, strings.TrimSuffix(host, ".onion"))
	})
}

func hasAddress(addrs []string, a string) bool {
	if a == "" {
		return false
	}
	for _, x := range addrs {
		if x == a {
			return true
		}
	}
	return false
}

func (d *daemon) considerCircuitRenewal(ctx context.Context, peerID, tag, host, reason string, started time.Time, moved bool, onRenew func()) string {
	why := d.decideCircuitRenewal(ctx, peerID, tag, host, reason, started, moved, onRenew)
	if why != circuitRenewed {
		slog.Debug("no automatic new tor circuit",
			slog.String("peer_id", peerID),
			slog.String("reason", reason),
			slog.String("why", why),
		)
	}
	return why
}

func (d *daemon) decideCircuitRenewal(ctx context.Context, peerID, tag, host, reason string, started time.Time, moved bool, onRenew func()) string {
	if d.registry == nil {
		return circuitNoPeer
	}
	p, err := d.registry.Get(peerID)
	if err != nil {
		return circuitNoPeer
	}

	if !hasAddress(p.KnownAddresses, host) {
		return circuitNotPeerAddress
	}
	var failures int
	if !moved {
		n, why := d.routes.failed(peerID, tag, started)
		if why != "" {
			return why
		}
		failures = n
	} else if !d.routes.stillDue(peerID, tag, started) {
		return circuitAnsweredMeantime
	}
	if d.callActiveWith(p) {
		return circuitCallActive
	}
	ok, why, burst := d.routes.begin(peerID, time.Now())
	if !ok {
		return why
	}
	inbound := d.routes.inboundSince(peerID, time.Now().Add(-circuitInboundProof))
	if !moved && (burst || !inbound) {

		d.routes.release(peerID)
		goCircuitRenewal(func() {
			d.considerCircuitRenewal(d.renewCtx(), peerID, tag, host, reason, started, true, onRenew)
		})
		return circuitMovedOn
	}
	if burst {
		if !renewSleep(ctx, renewDelay()) {
			d.routes.release(peerID)
			return circuitStopped
		}
	}
	proof := routeProofInbound
	if !d.routes.inboundSince(peerID, time.Now().Add(-circuitInboundProof)) {

		proof = routeProofSelfProbe
		if s := d.probePeerSelf(ctx, p.ID, p.MyOnionAddr, false); !s.Ok {
			d.routes.release(peerID)
			return circuitOwnSideRed
		}
	}
	if moved {

		if !d.routes.stillDue(peerID, tag, started) {
			d.routes.release(peerID)
			return circuitAnsweredMeantime
		}
		if d.callActiveWith(p) {
			d.routes.release(peerID)
			return circuitCallActive
		}
	}
	renewals, gen := d.routes.renew(peerID, time.Now())
	if onRenew != nil {

		onRenew()
	}

	slog.Info(circuitRenewedLine,
		slog.String("peer_id", peerID),
		slog.String("reason", reason),
		slog.String("proof", proof),
		slog.Int("failures", failures),
		slog.Int("renewals", renewals),
		slog.Int("tag", gen),
	)
	return circuitRenewed
}

func closePeerRendCircuits(conn *control.Conn, peerID string, addrs []string) (int, error) {
	targets := make(map[string]struct{}, len(addrs))
	for _, a := range addrs {
		if a != "" {
			targets[a] = struct{}{}
		}
	}
	if len(targets) == 0 {
		return 0, nil
	}
	circuits, err := conn.CircuitStatus()
	if err != nil {
		return 0, fmt.Errorf("circuit-status: %w", err)
	}
	closed := 0
	for _, ci := range circuits {
		if ci.Purpose != "HS_CLIENT_REND" {
			continue
		}
		if _, ok := targets[ci.HSAddress]; !ok {
			continue
		}
		if err := conn.CloseCircuit(ci.ID); err != nil {
			if errors.Is(err, control.ErrUnknownCircuit) {

				continue
			}
			slog.Warn("CloseCircuit failed",
				slog.String("peer_id", peerID),
				slog.String("circ_id", ci.ID),
				slog.Any("err", err),
			)
			continue
		}
		closed++
	}
	return closed, nil
}
