package rotation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"haoma-frontend/internal/msg"
)

type State string

const (
	StateProposed State = "proposed"

	StateRequested State = "requested"

	StateAccepted State = "accepted"

	StateAddressExchanged State = "address_exchanged"

	StateConfirmed State = "confirmed"

	StateFailed State = "failed"
)

type Role string

const (
	RoleInitiator Role = "initiator"
	RoleResponder Role = "responder"
)

type Snapshot struct {
	PeerID        string
	RotationID    string
	Role          Role
	State         State
	MyNewAddr     string
	TheirNewAddr  string
	IConfirmed    bool
	PeerConfirmed bool
	StartedAt     int64
	DeadlineAt    int64
	Reason        string
}

type Publisher interface {
	AddOnionNew(ctx context.Context) (addr, privKey string, err error)

	DelOnion(ctx context.Context, addr string) error
}

type SendFunc func(ctx context.Context, peerID string, w *msg.Wrapper) error

type SeqFunc func(peerID string) (uint64, error)

type Notifier interface {
	OnRotationLifecycle(snap Snapshot)
	OnRotationRequested(snap Snapshot)
}

type Registry interface {
	OverlayPeerAddress(ctx context.Context, peerID, address string) error
	CollapsePeerAddress(ctx context.Context, peerID, retain string) error
	RotateOwnOnion(ctx context.Context, peerID, address, privateKey string) (oldAddr string, err error)
}

type Config struct {
	Publisher Publisher
	Send      SendFunc
	Seq       SeqFunc
	Notifier  Notifier
	Registry  Registry

	Timeout time.Duration

	Now func() time.Time
}

var (
	ErrInflight      = errors.New("rotation: peer already has an in-flight rotation")
	ErrNotFound      = errors.New("rotation: rotation_id not found")
	ErrBadState      = errors.New("rotation: bad state transition")
	ErrPeerMismatch  = errors.New("rotation: peer mismatch")
	ErrNotConfigured = errors.New("rotation: manager not fully configured")
)

type record struct {
	Snapshot
	MyNewPrivKey string
}

type Manager struct {
	cfg Config

	mu       sync.Mutex
	inflight map[string]*record
	perPeer  map[string]string
}

func NewManager(cfg Config) *Manager {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 3 * time.Minute
	}
	return &Manager{
		cfg:      cfg,
		inflight: make(map[string]*record),
		perPeer:  make(map[string]string),
	}
}

func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Tick(ctx, m.cfg.Now())
		}
	}
}

func (m *Manager) Tick(ctx context.Context, now time.Time) {
	m.mu.Lock()
	expired := make([]string, 0)
	for rotID, rec := range m.inflight {
		if rec.State == StateConfirmed || rec.State == StateFailed {
			continue
		}
		if rec.DeadlineAt > 0 && now.Unix() >= rec.DeadlineAt {
			expired = append(expired, rotID)
		}
	}
	m.mu.Unlock()
	for _, rotID := range expired {
		_ = m.fail(ctx, rotID, msg.RotateCancelTimeout, "deadline elapsed")
	}
}

func (m *Manager) Get(rotID string) (Snapshot, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.inflight[rotID]
	if !ok {
		return Snapshot{}, false
	}
	return rec.Snapshot, true
}

func (m *Manager) ByPeer(peerID string) (Snapshot, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rotID, ok := m.perPeer[peerID]
	if !ok {
		return Snapshot{}, false
	}
	rec, ok := m.inflight[rotID]
	if !ok {
		return Snapshot{}, false
	}
	return rec.Snapshot, true
}

func (s State) IsTerminal() bool {
	return s == StateConfirmed || s == StateFailed
}

func (m *Manager) Begin(ctx context.Context, peerID string) (string, error) {
	if err := m.checkConfig(); err != nil {
		return "", err
	}
	if peerID == "" {
		return "", fmt.Errorf("rotation: peer_id required")
	}

	rotID, err := newRotationID()
	if err != nil {
		return "", fmt.Errorf("rotation: id mint: %w", err)
	}
	now := m.cfg.Now()

	m.mu.Lock()
	if _, busy := m.perPeer[peerID]; busy {
		m.mu.Unlock()
		return "", ErrInflight
	}
	rec := &record{
		Snapshot: Snapshot{
			PeerID:     peerID,
			RotationID: rotID,
			Role:       RoleInitiator,
			State:      StateProposed,
			StartedAt:  now.Unix(),
			DeadlineAt: now.Add(m.cfg.Timeout).Unix(),
		},
	}
	m.inflight[rotID] = rec
	m.perPeer[peerID] = rotID
	snap := rec.Snapshot
	m.mu.Unlock()

	slog.Debug("rotation: begin",
		slog.String("peer_id", peerID),
		slog.String("rotation_id", rotID),
	)

	if err := m.sendRequest(ctx, peerID, rotID, now.Unix()); err != nil {

		_ = m.fail(ctx, rotID, msg.RotateCancelInternal, fmt.Sprintf("send request: %v", err))
		return "", fmt.Errorf("rotation: send request: %w", err)
	}
	m.notifyLifecycle(snap)
	return rotID, nil
}

func (m *Manager) OnRequest(ctx context.Context, peerID, rotID string, proposedAt int64) error {
	if err := m.checkConfig(); err != nil {
		return err
	}
	if peerID == "" || rotID == "" {
		return fmt.Errorf("rotation: peer_id + rotation_id required")
	}

	now := m.cfg.Now()
	m.mu.Lock()
	if _, busy := m.perPeer[peerID]; busy {
		m.mu.Unlock()
		return ErrInflight
	}
	if _, dup := m.inflight[rotID]; dup {
		m.mu.Unlock()
		return fmt.Errorf("rotation: rotation_id already known: %s", rotID)
	}
	rec := &record{
		Snapshot: Snapshot{
			PeerID:     peerID,
			RotationID: rotID,
			Role:       RoleResponder,
			State:      StateRequested,
			StartedAt:  now.Unix(),
			DeadlineAt: now.Add(m.cfg.Timeout).Unix(),
		},
	}
	m.inflight[rotID] = rec
	m.perPeer[peerID] = rotID
	snap := rec.Snapshot
	m.mu.Unlock()

	slog.Debug("rotation: request received",
		slog.String("peer_id", peerID),
		slog.String("rotation_id", rotID),
		slog.Int64("proposed_at", proposedAt),
	)
	m.notifyRequested(snap)
	return nil
}

func (m *Manager) UserAccept(ctx context.Context, rotID string) error {
	if err := m.checkConfig(); err != nil {
		return err
	}
	m.mu.Lock()
	rec, ok := m.inflight[rotID]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	if rec.Role != RoleResponder || rec.State != StateRequested {
		m.mu.Unlock()
		return fmt.Errorf("%w: state=%s role=%s", ErrBadState, rec.State, rec.Role)
	}
	rec.State = StateAccepted
	snap := rec.Snapshot
	peerID := rec.PeerID
	m.mu.Unlock()

	slog.Debug("rotation: user accepted",
		slog.String("peer_id", peerID),
		slog.String("rotation_id", rotID),
	)
	if err := m.sendAccept(ctx, peerID, rotID); err != nil {
		_ = m.fail(ctx, rotID, msg.RotateCancelInternal, fmt.Sprintf("send accept: %v", err))
		return fmt.Errorf("rotation: send accept: %w", err)
	}
	m.notifyLifecycle(snap)
	return nil
}

func (m *Manager) UserDecline(ctx context.Context, rotID, reason string) error {
	if reason == "" {
		reason = msg.RotateCancelUserDeclined
	}
	return m.fail(ctx, rotID, reason, "user declined")
}

func (m *Manager) Cancel(ctx context.Context, rotID, reason string) error {
	if reason == "" {
		reason = msg.RotateCancelUserDeclined
	}
	return m.fail(ctx, rotID, reason, "user cancelled")
}

func (m *Manager) SendCancel(ctx context.Context, peerID, rotID, reason string) error {
	if m.cfg.Send == nil || m.cfg.Seq == nil {
		return ErrNotConfigured
	}
	if reason == "" {
		reason = msg.RotateCancelUserDeclined
	}
	return m.sendCancel(ctx, peerID, rotID, reason)
}

func (m *Manager) OnAccept(ctx context.Context, peerID, rotID string) error {
	if err := m.checkConfig(); err != nil {
		return err
	}
	m.mu.Lock()
	rec, ok := m.inflight[rotID]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	if rec.PeerID != peerID {
		m.mu.Unlock()
		return ErrPeerMismatch
	}
	if rec.Role != RoleInitiator || rec.State != StateProposed {
		m.mu.Unlock()
		return fmt.Errorf("%w: state=%s role=%s", ErrBadState, rec.State, rec.Role)
	}
	m.mu.Unlock()

	addr, priv, err := m.cfg.Publisher.AddOnionNew(ctx)
	if err != nil {
		_ = m.fail(ctx, rotID, msg.RotateCancelInternal, fmt.Sprintf("mint onion: %v", err))
		return fmt.Errorf("rotation: mint: %w", err)
	}

	m.mu.Lock()
	rec, ok = m.inflight[rotID]
	if !ok || rec.State != StateProposed {
		m.mu.Unlock()

		_ = m.cfg.Publisher.DelOnion(ctx, addr)
		return ErrBadState
	}
	rec.MyNewAddr = addr
	rec.MyNewPrivKey = priv
	rec.State = StateAddressExchanged
	snap := rec.Snapshot
	m.mu.Unlock()

	slog.Debug("rotation: accept observed; minted local onion + sending address+confirm",
		slog.String("peer_id", peerID),
		slog.String("rotation_id", rotID),
		slog.String("my_new_addr", addr),
	)
	if err := m.sendAddress(ctx, peerID, rotID, addr); err != nil {
		_ = m.fail(ctx, rotID, msg.RotateCancelInternal, fmt.Sprintf("send address: %v", err))
		return fmt.Errorf("rotation: send address: %w", err)
	}
	m.notifyLifecycle(snap)
	if err := m.shipConfirm(ctx, rotID); err != nil {
		_ = m.fail(ctx, rotID, msg.RotateCancelInternal, fmt.Sprintf("send confirm: %v", err))
		return err
	}
	return nil
}

func (m *Manager) OnAddress(ctx context.Context, peerID, rotID, theirAddr string) error {
	if err := m.checkConfig(); err != nil {
		return err
	}
	if theirAddr == "" {
		return fmt.Errorf("rotation: their_addr empty")
	}
	m.mu.Lock()
	rec, ok := m.inflight[rotID]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	if rec.PeerID != peerID {
		m.mu.Unlock()
		return ErrPeerMismatch
	}
	if rec.State != StateAccepted {
		m.mu.Unlock()
		return fmt.Errorf("%w: state=%s", ErrBadState, rec.State)
	}
	rec.TheirNewAddr = theirAddr
	rec.State = StateAddressExchanged
	snap := rec.Snapshot
	m.mu.Unlock()

	slog.Debug("rotation: peer address received; overlaying + sending Confirm",
		slog.String("peer_id", peerID),
		slog.String("rotation_id", rotID),
		slog.String("their_new_addr", theirAddr),
	)
	if m.cfg.Registry != nil {
		if err := m.cfg.Registry.OverlayPeerAddress(ctx, peerID, theirAddr); err != nil {
			_ = m.fail(ctx, rotID, msg.RotateCancelInternal, fmt.Sprintf("overlay address: %v", err))
			return fmt.Errorf("rotation: overlay address: %w", err)
		}
	}
	m.notifyLifecycle(snap)
	if err := m.shipConfirm(ctx, rotID); err != nil {
		_ = m.fail(ctx, rotID, msg.RotateCancelInternal, fmt.Sprintf("send confirm: %v", err))
		return err
	}
	return nil
}

func (m *Manager) OnConfirm(ctx context.Context, peerID, rotID string) error {
	if err := m.checkConfig(); err != nil {
		return err
	}
	m.mu.Lock()
	rec, ok := m.inflight[rotID]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	if rec.PeerID != peerID {
		m.mu.Unlock()
		return ErrPeerMismatch
	}
	if rec.State != StateAddressExchanged {
		m.mu.Unlock()
		return fmt.Errorf("%w: state=%s", ErrBadState, rec.State)
	}
	rec.PeerConfirmed = true
	confirmed := rec.PeerConfirmed && rec.IConfirmed
	myNewAddr := rec.MyNewAddr
	myNewPriv := rec.MyNewPrivKey
	theirNewAddr := rec.TheirNewAddr
	if confirmed {
		rec.State = StateConfirmed
		delete(m.perPeer, rec.PeerID)
	}
	snap := rec.Snapshot
	m.mu.Unlock()

	slog.Debug("rotation: peer confirm observed",
		slog.String("peer_id", peerID),
		slog.String("rotation_id", rotID),
		slog.Bool("both_confirmed", confirmed),
	)
	if confirmed {
		m.applyConfirmedSideEffects(ctx, peerID, myNewAddr, myNewPriv, theirNewAddr)
	}
	m.notifyLifecycle(snap)
	return nil
}

func (m *Manager) OnCancel(ctx context.Context, peerID, rotID, reason string) error {
	if err := m.checkConfig(); err != nil {
		return err
	}
	m.mu.Lock()
	rec, ok := m.inflight[rotID]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	if rec.PeerID != peerID {
		m.mu.Unlock()
		return ErrPeerMismatch
	}
	if rec.State.IsTerminal() {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	return m.terminateAsFailed(ctx, rotID, reason, "peer cancelled", false)
}

func (m *Manager) fail(ctx context.Context, rotID, reason, note string) error {
	return m.terminateAsFailed(ctx, rotID, reason, note, true)
}

func (m *Manager) terminateAsFailed(ctx context.Context, rotID, reason, note string, sendCancel bool) error {
	m.mu.Lock()
	rec, ok := m.inflight[rotID]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	if rec.State.IsTerminal() {
		m.mu.Unlock()
		return nil
	}
	rec.State = StateFailed
	rec.Reason = reason
	mintedAddr := rec.MyNewAddr
	peerID := rec.PeerID
	delete(m.perPeer, peerID)
	snap := rec.Snapshot
	m.mu.Unlock()

	slog.Info("rotation: failed",
		slog.String("peer_id", peerID),
		slog.String("rotation_id", rotID),
		slog.String("reason", reason),
		slog.String("note", note),
	)

	if mintedAddr != "" && m.cfg.Publisher != nil {
		if err := m.cfg.Publisher.DelOnion(ctx, mintedAddr); err != nil {
			slog.Warn("rotation: del onion (cleanup) failed",
				slog.String("addr", mintedAddr),
				slog.Any("err", err),
			)
		}
	}

	if sendCancel && m.cfg.Send != nil {
		if err := m.sendCancel(ctx, peerID, rotID, reason); err != nil {
			slog.Warn("rotation: send cancel failed",
				slog.String("peer_id", peerID),
				slog.String("rotation_id", rotID),
				slog.Any("err", err),
			)
		}
	}
	m.notifyLifecycle(snap)
	return nil
}

func (m *Manager) shipConfirm(ctx context.Context, rotID string) error {
	m.mu.Lock()
	rec, ok := m.inflight[rotID]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	peerID := rec.PeerID
	m.mu.Unlock()

	if err := m.sendConfirm(ctx, peerID, rotID); err != nil {
		return fmt.Errorf("rotation: send confirm: %w", err)
	}

	m.mu.Lock()
	rec, ok = m.inflight[rotID]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	rec.IConfirmed = true
	confirmed := rec.IConfirmed && rec.PeerConfirmed
	myNewAddr := rec.MyNewAddr
	myNewPriv := rec.MyNewPrivKey
	theirNewAddr := rec.TheirNewAddr
	if confirmed {
		rec.State = StateConfirmed
		delete(m.perPeer, rec.PeerID)
	}
	snap := rec.Snapshot
	m.mu.Unlock()

	if confirmed {
		slog.Info("rotation: confirmed",
			slog.String("peer_id", peerID),
			slog.String("rotation_id", rotID),
		)
		m.applyConfirmedSideEffects(ctx, peerID, myNewAddr, myNewPriv, theirNewAddr)
	}

	m.notifyLifecycle(snap)
	return nil
}

func (m *Manager) applyConfirmedSideEffects(ctx context.Context, peerID, myNewAddr, myNewPriv, theirNewAddr string) {
	if m.cfg.Registry == nil {
		return
	}
	if myNewAddr != "" {
		oldAddr, err := m.cfg.Registry.RotateOwnOnion(ctx, peerID, myNewAddr, myNewPriv)
		if err != nil {
			slog.Warn("rotation: registry RotateOwnOnion failed",
				slog.String("peer_id", peerID),
				slog.Any("err", err),
			)
		} else if oldAddr != "" && oldAddr != myNewAddr && m.cfg.Publisher != nil {
			if delErr := m.cfg.Publisher.DelOnion(ctx, oldAddr); delErr != nil {
				slog.Warn("rotation: del old onion failed",
					slog.String("addr", oldAddr),
					slog.Any("err", delErr),
				)
			}
		}
	}
	if theirNewAddr != "" {
		if err := m.cfg.Registry.CollapsePeerAddress(ctx, peerID, theirNewAddr); err != nil {
			slog.Warn("rotation: registry CollapsePeerAddress failed",
				slog.String("peer_id", peerID),
				slog.Any("err", err),
			)
		}
	}
}

func (m *Manager) sendRequest(ctx context.Context, peerID, rotID string, proposedAt int64) error {
	seq, err := m.cfg.Seq(peerID)
	if err != nil {
		return fmt.Errorf("seq: %w", err)
	}
	mid, err := msg.NewID()
	if err != nil {
		return fmt.Errorf("msg id: %w", err)
	}
	w, err := msg.BuildRotateRequest(seq, m.cfg.Now().Unix(), mid, rotID, proposedAt, 0)
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	return m.cfg.Send(ctx, peerID, w)
}

func (m *Manager) sendAccept(ctx context.Context, peerID, rotID string) error {
	seq, err := m.cfg.Seq(peerID)
	if err != nil {
		return fmt.Errorf("seq: %w", err)
	}
	mid, err := msg.NewID()
	if err != nil {
		return fmt.Errorf("msg id: %w", err)
	}
	w, err := msg.BuildRotateAccept(seq, m.cfg.Now().Unix(), mid, rotID, 0)
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	return m.cfg.Send(ctx, peerID, w)
}

func (m *Manager) sendAddress(ctx context.Context, peerID, rotID, newAddr string) error {
	seq, err := m.cfg.Seq(peerID)
	if err != nil {
		return fmt.Errorf("seq: %w", err)
	}
	mid, err := msg.NewID()
	if err != nil {
		return fmt.Errorf("msg id: %w", err)
	}
	w, err := msg.BuildRotateAddress(seq, m.cfg.Now().Unix(), mid, rotID, newAddr, 0)
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	return m.cfg.Send(ctx, peerID, w)
}

func (m *Manager) sendConfirm(ctx context.Context, peerID, rotID string) error {
	seq, err := m.cfg.Seq(peerID)
	if err != nil {
		return fmt.Errorf("seq: %w", err)
	}
	mid, err := msg.NewID()
	if err != nil {
		return fmt.Errorf("msg id: %w", err)
	}
	w, err := msg.BuildRotateConfirm(seq, m.cfg.Now().Unix(), mid, rotID, 0)
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	return m.cfg.Send(ctx, peerID, w)
}

func (m *Manager) sendCancel(ctx context.Context, peerID, rotID, reason string) error {
	seq, err := m.cfg.Seq(peerID)
	if err != nil {
		return fmt.Errorf("seq: %w", err)
	}
	mid, err := msg.NewID()
	if err != nil {
		return fmt.Errorf("msg id: %w", err)
	}
	w, err := msg.BuildRotateCancel(seq, m.cfg.Now().Unix(), mid, rotID, reason, 0)
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	return m.cfg.Send(ctx, peerID, w)
}

func (m *Manager) checkConfig() error {
	if m.cfg.Publisher == nil || m.cfg.Send == nil || m.cfg.Seq == nil {
		return ErrNotConfigured
	}
	return nil
}

func (m *Manager) notifyLifecycle(snap Snapshot) {
	if m.cfg.Notifier != nil {
		m.cfg.Notifier.OnRotationLifecycle(snap)
	}
}

func (m *Manager) notifyRequested(snap Snapshot) {
	if m.cfg.Notifier != nil {
		m.cfg.Notifier.OnRotationRequested(snap)
	}
}

func newRotationID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "rot-" + hex.EncodeToString(b[:]), nil
}
