package rotation_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/rotation"
)

type fakePublisher struct {
	mu       sync.Mutex
	mintN    int
	addOnion []string
	delOnion []string
	failNext error
}

func (p *fakePublisher) AddOnionNew(ctx context.Context) (string, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failNext != nil {
		err := p.failNext
		p.failNext = nil
		return "", "", err
	}
	p.mintN++
	addr := strings.Repeat("a", 56-2) + map[int]string{1: "01", 2: "02", 3: "03", 4: "04"}[p.mintN]
	privKey := "priv-" + addr
	p.addOnion = append(p.addOnion, addr)
	return addr, privKey, nil
}

func (p *fakePublisher) DelOnion(ctx context.Context, addr string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.delOnion = append(p.delOnion, addr)
	return nil
}

type fakeSeq struct {
	mu sync.Mutex
	n  map[string]uint64
}

func newFakeSeq() *fakeSeq { return &fakeSeq{n: make(map[string]uint64)} }

func (s *fakeSeq) Next(peerID string) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n[peerID]++
	return s.n[peerID], nil
}

type sendCapture struct {
	PeerID  string
	Wrapper *msg.Wrapper
}

type fakeSink struct {
	mu      sync.Mutex
	sends   []sendCapture
	failOne error
}

func (s *fakeSink) Send(ctx context.Context, peerID string, w *msg.Wrapper) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failOne != nil {
		err := s.failOne
		s.failOne = nil
		return err
	}
	s.sends = append(s.sends, sendCapture{PeerID: peerID, Wrapper: w})
	return nil
}

func (s *fakeSink) Snapshot() []sendCapture {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]sendCapture, len(s.sends))
	copy(out, s.sends)
	return out
}

func (s *fakeSink) lastKind() msg.Kind {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sends) == 0 {
		return ""
	}
	return s.sends[len(s.sends)-1].Wrapper.Kind
}

type fakeRegistry struct {
	mu             sync.Mutex
	currentMyOnion string
	overlay        string
	collapseRetain string
	rotatedAddr    string
	rotatedPriv    string
}

func (r *fakeRegistry) OverlayPeerAddress(ctx context.Context, peerID, address string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.overlay = address
	return nil
}

func (r *fakeRegistry) CollapsePeerAddress(ctx context.Context, peerID, retain string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.collapseRetain = retain
	return nil
}

func (r *fakeRegistry) RotateOwnOnion(ctx context.Context, peerID, address, privateKey string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.currentMyOnion
	r.rotatedAddr = address
	r.rotatedPriv = privateKey
	r.currentMyOnion = address
	return old, nil
}

type fakeNotifier struct {
	mu         sync.Mutex
	lifecycles []rotation.Snapshot
	requested  []rotation.Snapshot
}

func (n *fakeNotifier) OnRotationLifecycle(s rotation.Snapshot) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.lifecycles = append(n.lifecycles, s)
}

func (n *fakeNotifier) OnRotationRequested(s rotation.Snapshot) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.requested = append(n.requested, s)
}

type fakeClock struct {
	t atomic.Int64
}

func (c *fakeClock) set(unix int64)  { c.t.Store(unix) }
func (c *fakeClock) advance(d int64) { c.t.Add(d) }
func (c *fakeClock) Now() time.Time  { return time.Unix(c.t.Load(), 0) }

type rig struct {
	mgr   *rotation.Manager
	pub   *fakePublisher
	send  *fakeSink
	notif *fakeNotifier
	reg   *fakeRegistry
	clock *fakeClock
}

func newRig(t *testing.T) *rig {
	t.Helper()
	return newRigWithRegistry(t, nil)
}

func newRigWithRegistry(t *testing.T, reg *fakeRegistry) *rig {
	t.Helper()
	clock := &fakeClock{}
	clock.set(1_700_000_000)
	pub := &fakePublisher{}
	send := &fakeSink{}
	seq := newFakeSeq()
	notif := &fakeNotifier{}
	cfg := rotation.Config{
		Publisher: pub,
		Send:      send.Send,
		Seq:       seq.Next,
		Notifier:  notif,
		Timeout:   3 * time.Minute,
		Now:       clock.Now,
	}
	if reg != nil {
		cfg.Registry = reg
	}
	mgr := rotation.NewManager(cfg)
	return &rig{mgr: mgr, pub: pub, send: send, notif: notif, reg: reg, clock: clock}
}

const peerBob = "bob-peer-id"
const peerAlice = "alice-peer-id"

func TestBegin_HappyPath(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()

	rotID, err := r.mgr.Begin(ctx, peerBob)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if !strings.HasPrefix(rotID, "rot-") || len(rotID) != 36 {
		t.Errorf("rotID format = %q", rotID)
	}

	sends := r.send.Snapshot()
	if len(sends) != 1 {
		t.Fatalf("send count = %d, want 1", len(sends))
	}
	if sends[0].Wrapper.Kind != msg.KindRotateRequest {
		t.Errorf("kind = %q, want %q", sends[0].Wrapper.Kind, msg.KindRotateRequest)
	}
	body, err := sends[0].Wrapper.RotateRequest()
	if err != nil {
		t.Fatalf("RotateRequest: %v", err)
	}
	if body.RotationID != rotID {
		t.Errorf("rotation_id = %q, want %q", body.RotationID, rotID)
	}

	snap, ok := r.mgr.Get(rotID)
	if !ok || snap.State != rotation.StateProposed || snap.Role != rotation.RoleInitiator {
		t.Errorf("snapshot drift: %+v", snap)
	}

	if len(r.notif.lifecycles) != 1 {
		t.Errorf("lifecycle count = %d, want 1", len(r.notif.lifecycles))
	}
}

func TestBegin_RejectsInflight(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.mgr.Begin(ctx, peerBob); err != nil {
		t.Fatalf("first Begin: %v", err)
	}
	if _, err := r.mgr.Begin(ctx, peerBob); !errors.Is(err, rotation.ErrInflight) {
		t.Errorf("second Begin err = %v, want ErrInflight", err)
	}
}

func TestOnRequest_InsertsAndNotifies(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.mgr.OnRequest(ctx, peerAlice, "rot-test-001", 1_700_000_000); err != nil {
		t.Fatalf("OnRequest: %v", err)
	}
	snap, ok := r.mgr.Get("rot-test-001")
	if !ok || snap.Role != rotation.RoleResponder || snap.State != rotation.StateRequested {
		t.Errorf("snapshot drift: %+v", snap)
	}
	if len(r.notif.requested) != 1 {
		t.Errorf("requested count = %d, want 1", len(r.notif.requested))
	}
}

func TestOnRequest_RejectsConcurrent(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.mgr.OnRequest(ctx, peerAlice, "rot-test-001", 1); err != nil {
		t.Fatalf("first OnRequest: %v", err)
	}
	err := r.mgr.OnRequest(ctx, peerAlice, "rot-test-002", 1)
	if !errors.Is(err, rotation.ErrInflight) {
		t.Errorf("err = %v, want ErrInflight", err)
	}
}

func TestUserAccept_TransitionsAndSendsAccept(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.mgr.OnRequest(ctx, peerAlice, "rot-test-A", 1_700_000_000); err != nil {
		t.Fatalf("OnRequest: %v", err)
	}
	if err := r.mgr.UserAccept(ctx, "rot-test-A"); err != nil {
		t.Fatalf("UserAccept: %v", err)
	}
	snap, _ := r.mgr.Get("rot-test-A")
	if snap.State != rotation.StateAccepted {
		t.Errorf("state = %q, want %q", snap.State, rotation.StateAccepted)
	}
	if r.send.lastKind() != msg.KindRotateAccept {
		t.Errorf("last kind = %q, want %q", r.send.lastKind(), msg.KindRotateAccept)
	}
}

func TestUserAccept_RejectsBadState(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	err := r.mgr.UserAccept(ctx, "rot-unknown")
	if !errors.Is(err, rotation.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestOnAccept_InitiatorMintsAddrAndShipsConfirm(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	rotID, err := r.mgr.Begin(ctx, peerBob)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := r.mgr.OnAccept(ctx, peerBob, rotID); err != nil {
		t.Fatalf("OnAccept: %v", err)
	}
	if len(r.pub.addOnion) != 1 {
		t.Errorf("AddOnionNew calls = %d, want 1", len(r.pub.addOnion))
	}
	snap, _ := r.mgr.Get(rotID)
	if snap.State != rotation.StateAddressExchanged {
		t.Errorf("state = %q, want %q", snap.State, rotation.StateAddressExchanged)
	}
	if snap.MyNewAddr != r.pub.addOnion[0] {
		t.Errorf("MyNewAddr = %q, want %q", snap.MyNewAddr, r.pub.addOnion[0])
	}
	if !snap.IConfirmed {
		t.Errorf("IConfirmed = false, want true (single-side ships Confirm right after Address)")
	}

	sends := r.send.Snapshot()
	if len(sends) != 3 {
		t.Fatalf("send count = %d, want 3 (Request+Address+Confirm)", len(sends))
	}
	if sends[1].Wrapper.Kind != msg.KindRotateAddress {
		t.Errorf("send[1] kind = %q, want %q", sends[1].Wrapper.Kind, msg.KindRotateAddress)
	}
	if sends[2].Wrapper.Kind != msg.KindRotateConfirm {
		t.Errorf("send[2] kind = %q, want %q", sends[2].Wrapper.Kind, msg.KindRotateConfirm)
	}
}

func TestHappyPathRoundTrip(t *testing.T) {

	a := newRigWithRegistry(t, &fakeRegistry{currentMyOnion: "alice-old-onion-addr-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	b := newRigWithRegistry(t, &fakeRegistry{currentMyOnion: "bob-old-onion-addr-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"})
	ctx := context.Background()

	rotID, err := a.mgr.Begin(ctx, peerBob)
	if err != nil {
		t.Fatalf("A.Begin: %v", err)
	}
	requestFrame := a.send.Snapshot()[0]
	if requestFrame.Wrapper.Kind != msg.KindRotateRequest {
		t.Fatalf("expected request, got %s", requestFrame.Wrapper.Kind)
	}

	reqBody, _ := requestFrame.Wrapper.RotateRequest()
	if err := b.mgr.OnRequest(ctx, peerAlice, reqBody.RotationID, reqBody.ProposedAt); err != nil {
		t.Fatalf("B.OnRequest: %v", err)
	}

	if err := b.mgr.UserAccept(ctx, rotID); err != nil {
		t.Fatalf("B.UserAccept: %v", err)
	}

	if err := a.mgr.OnAccept(ctx, peerBob, rotID); err != nil {
		t.Fatalf("A.OnAccept: %v", err)
	}
	aSends := a.send.Snapshot()
	if got, want := len(aSends), 3; got != want {
		t.Fatalf("A send count = %d, want %d (Request+Address+Confirm)", got, want)
	}
	addrFromA := aSends[1].Wrapper
	if addrFromA.Kind != msg.KindRotateAddress {
		t.Fatalf("A's address frame kind = %s", addrFromA.Kind)
	}
	if aSends[2].Wrapper.Kind != msg.KindRotateConfirm {
		t.Fatalf("A's confirm frame kind = %s", aSends[2].Wrapper.Kind)
	}
	addrBodyA, _ := addrFromA.RotateAddress()
	aSnap, _ := a.mgr.Get(rotID)
	if aSnap.State != rotation.StateAddressExchanged || !aSnap.IConfirmed {
		t.Errorf("A snapshot post-OnAccept: %+v", aSnap)
	}

	if err := b.mgr.OnAddress(ctx, peerAlice, rotID, addrBodyA.NewAddress); err != nil {
		t.Fatalf("B.OnAddress: %v", err)
	}
	if len(b.pub.addOnion) != 0 {
		t.Errorf("B minted onion = %v, want none (single-side)", b.pub.addOnion)
	}
	if b.reg.overlay != addrBodyA.NewAddress {
		t.Errorf("B overlay arg = %q, want %q", b.reg.overlay, addrBodyA.NewAddress)
	}
	bSends := b.send.Snapshot()

	if got, want := len(bSends), 2; got != want {
		t.Fatalf("B send count = %d, want %d (Accept+Confirm)", got, want)
	}
	if bSends[1].Wrapper.Kind != msg.KindRotateConfirm {
		t.Fatalf("B last frame kind = %s, want Confirm", bSends[1].Wrapper.Kind)
	}
	bSnap, _ := b.mgr.Get(rotID)
	if bSnap.State != rotation.StateAddressExchanged || !bSnap.IConfirmed {
		t.Errorf("B snapshot post-OnAddress: %+v", bSnap)
	}

	if err := a.mgr.OnConfirm(ctx, peerBob, rotID); err != nil {
		t.Fatalf("A.OnConfirm: %v", err)
	}
	aSnap, _ = a.mgr.Get(rotID)
	if aSnap.State != rotation.StateConfirmed {
		t.Errorf("A final state = %q, want %q", aSnap.State, rotation.StateConfirmed)
	}
	if a.reg.rotatedAddr != addrBodyA.NewAddress {
		t.Errorf("A RotateOwnOnion arg = %q, want %q", a.reg.rotatedAddr, addrBodyA.NewAddress)
	}
	if len(a.pub.delOnion) != 1 || a.pub.delOnion[0] != "alice-old-onion-addr-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("A DelOnion calls = %v, want [old-alice]", a.pub.delOnion)
	}
	if a.reg.collapseRetain != "" {
		t.Errorf("A CollapsePeerAddress unexpected (single-side initiator has TheirNewAddr=\"\"): %q", a.reg.collapseRetain)
	}

	if err := b.mgr.OnConfirm(ctx, peerAlice, rotID); err != nil {
		t.Fatalf("B.OnConfirm: %v", err)
	}
	bSnap, _ = b.mgr.Get(rotID)
	if bSnap.State != rotation.StateConfirmed {
		t.Errorf("B final state = %q, want %q", bSnap.State, rotation.StateConfirmed)
	}
	if b.reg.collapseRetain != addrBodyA.NewAddress {
		t.Errorf("B CollapsePeerAddress retain arg = %q, want %q", b.reg.collapseRetain, addrBodyA.NewAddress)
	}
	if b.reg.rotatedAddr != "" {
		t.Errorf("B RotateOwnOnion unexpected (single-side responder): %q", b.reg.rotatedAddr)
	}
	if len(b.pub.delOnion) != 0 {
		t.Errorf("B DelOnion unexpected: %v", b.pub.delOnion)
	}

	if _, ok := a.mgr.ByPeer(peerBob); ok {
		t.Error("A's perPeer not cleared after Confirmed")
	}
	if _, ok := b.mgr.ByPeer(peerAlice); ok {
		t.Error("B's perPeer not cleared after Confirmed")
	}
}

func TestCancel_FailsAndShipsCancel(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	rotID, err := r.mgr.Begin(ctx, peerBob)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	if err := r.mgr.OnAccept(ctx, peerBob, rotID); err != nil {
		t.Fatalf("OnAccept: %v", err)
	}
	mintedAddr := r.pub.addOnion[0]

	if err := r.mgr.Cancel(ctx, rotID, msg.RotateCancelInternal); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	snap, _ := r.mgr.Get(rotID)
	if snap.State != rotation.StateFailed {
		t.Errorf("state = %q, want Failed", snap.State)
	}
	if snap.Reason != msg.RotateCancelInternal {
		t.Errorf("reason = %q, want %q", snap.Reason, msg.RotateCancelInternal)
	}

	if len(r.pub.delOnion) != 1 || r.pub.delOnion[0] != mintedAddr {
		t.Errorf("DelOnion calls = %v, want [%s]", r.pub.delOnion, mintedAddr)
	}

	if r.send.lastKind() != msg.KindRotateCancel {
		t.Errorf("last sent kind = %q, want %q", r.send.lastKind(), msg.KindRotateCancel)
	}

	if _, ok := r.mgr.ByPeer(peerBob); ok {
		t.Error("perPeer not cleared after Cancel")
	}
}

func TestOnCancel_FailsWithoutResendingCancel(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.mgr.OnRequest(ctx, peerAlice, "rot-cancel-test", 1_700_000_000); err != nil {
		t.Fatalf("OnRequest: %v", err)
	}

	if got := len(r.send.Snapshot()); got != 0 {
		t.Fatalf("pre-cancel send count = %d, want 0", got)
	}
	if err := r.mgr.OnCancel(ctx, peerAlice, "rot-cancel-test", msg.RotateCancelUserDeclined); err != nil {
		t.Fatalf("OnCancel: %v", err)
	}

	if got := len(r.send.Snapshot()); got != 0 {
		t.Errorf("post-cancel send count = %d, want 0", got)
	}
	snap, _ := r.mgr.Get("rot-cancel-test")
	if snap.State != rotation.StateFailed {
		t.Errorf("state = %q, want Failed", snap.State)
	}
}

func TestTick_TimesOutPastDeadline(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	rotID, err := r.mgr.Begin(ctx, peerBob)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	r.clock.advance(int64(4 * 60))
	r.mgr.Tick(ctx, r.clock.Now())
	snap, _ := r.mgr.Get(rotID)
	if snap.State != rotation.StateFailed {
		t.Errorf("state after tick = %q, want Failed", snap.State)
	}
	if snap.Reason != msg.RotateCancelTimeout {
		t.Errorf("reason = %q, want %q", snap.Reason, msg.RotateCancelTimeout)
	}
}

func TestOnAccept_RejectsPeerMismatch(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	rotID, err := r.mgr.Begin(ctx, peerBob)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	err = r.mgr.OnAccept(ctx, "wrong-peer", rotID)
	if !errors.Is(err, rotation.ErrPeerMismatch) {
		t.Errorf("err = %v, want ErrPeerMismatch", err)
	}
}

func TestNotConfigured_ReturnsErr(t *testing.T) {
	mgr := rotation.NewManager(rotation.Config{})
	_, err := mgr.Begin(context.Background(), peerBob)
	if !errors.Is(err, rotation.ErrNotConfigured) {
		t.Errorf("err = %v, want ErrNotConfigured", err)
	}
}
