package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/rotation"
)

type rotationBackendPublisher struct {
	d *daemon
}

func (p *rotationBackendPublisher) AddOnionNew(ctx context.Context) (string, string, error) {
	c := p.d.backendClient
	if c == nil {
		return "", "", errors.New("rotation publisher: backend client not ready")
	}
	o, err := c.MintOnion(ctx)
	if err != nil {
		return "", "", fmt.Errorf("rotation publisher: mint onion: %w", err)
	}
	if o.Address == "" || o.PrivateKey == "" {
		return "", "", errors.New("rotation publisher: mint returned empty address or key")
	}
	slog.Debug("rotation: minted persistent onion via /onion/mint",
		slog.String("addr", o.Address),
	)
	return o.Address, o.PrivateKey, nil
}

func (p *rotationBackendPublisher) DelOnion(ctx context.Context, addr string) error {
	c := p.d.backendClient
	if c == nil {
		return errors.New("rotation publisher: backend client not ready")
	}
	if err := c.DelOnion(ctx, addr); err != nil {
		return fmt.Errorf("rotation publisher: del onion: %w", err)
	}
	slog.Debug("rotation: deleted onion via /onion/del",
		slog.String("addr", addr),
	)
	return nil
}

type rotationBackendRegistry struct {
	d *daemon
}

func (r *rotationBackendRegistry) OverlayPeerAddress(ctx context.Context, peerID, address string) error {
	c := r.d.backendClient
	if c == nil {
		return errors.New("rotation registry: backend client not ready")
	}
	if err := c.OverlayPeerAddress(ctx, peerID, address); err != nil {
		return fmt.Errorf("rotation registry: overlay: %w", err)
	}
	return nil
}

func (r *rotationBackendRegistry) CollapsePeerAddress(ctx context.Context, peerID, retain string) error {
	c := r.d.backendClient
	if c == nil {
		return errors.New("rotation registry: backend client not ready")
	}
	if err := c.CollapsePeerAddress(ctx, peerID, retain); err != nil {
		return fmt.Errorf("rotation registry: collapse: %w", err)
	}
	return nil
}

func (r *rotationBackendRegistry) RotateOwnOnion(ctx context.Context, peerID, address, privateKey string) (string, error) {
	c := r.d.backendClient
	if c == nil {
		return "", errors.New("rotation registry: backend client not ready")
	}
	out, err := c.RotateOwnOnion(ctx, peerID, address, privateKey)
	if err != nil {
		return "", fmt.Errorf("rotation registry: rotate-own-onion: %w", err)
	}
	return out.OldAddress, nil
}

type rotationNotifier struct {
	ipcSrv *ipc.Server
}

func (n *rotationNotifier) OnRotationLifecycle(s rotation.Snapshot) {
	push(n.ipcSrv, ipc.FrameRotateLifecycle, "", ipc.RotateLifecyclePush{
		RotationID: s.RotationID,
		PeerID:     s.PeerID,
		Role:       string(s.Role),
		State:      string(s.State),
		Reason:     s.Reason,
	})
}

func (n *rotationNotifier) OnRotationRequested(s rotation.Snapshot) {
	push(n.ipcSrv, ipc.FrameRotateRequested, "", ipc.RotateRequestedPush{
		RotationID: s.RotationID,
		PeerID:     s.PeerID,
		StartedAt:  s.StartedAt,
		DeadlineAt: s.DeadlineAt,
	})
}

func newRotationManager(d *daemon) *rotation.Manager {
	send := func(ctx context.Context, peerID string, w *msg.Wrapper) error {
		if d.cipher == nil || d.backendClient == nil {
			return errors.New("rotation send: cipher/backend not ready")
		}
		plain, err := msg.Marshal(w)
		if err != nil {
			return fmt.Errorf("rotation send: marshal: %w", err)
		}
		blob, err := d.cipher.Encrypt(ctx, peerID, plain)
		if err != nil {
			return fmt.Errorf("rotation send: encrypt: %w", err)
		}
		if _, err := d.backendClient.Send(ctx, backendapi.SendRequest{
			PeerID:         peerID,
			Payload:        blob,
			PresenceSource: backendapi.PresenceSourceHaoma,
		}); err != nil {
			return fmt.Errorf("rotation send: backend: %w", err)
		}
		slog.Debug("rotation: wire kind shipped",
			slog.String("peer_id", peerID),
			slog.String("kind", string(w.Kind)),
		)
		return nil
	}
	seq := func(peerID string) (uint64, error) {
		if d.peerSeq == nil {
			return 0, errors.New("rotation seq: peerSeq not ready")
		}
		return d.peerSeq.NextSendSeq(peerID)
	}
	return rotation.NewManager(rotation.Config{
		Publisher: &rotationBackendPublisher{d: d},
		Send:      send,
		Seq:       seq,
		Notifier:  &rotationNotifier{ipcSrv: d.ipcSrv},
		Registry:  &rotationBackendRegistry{d: d},
	})
}

func dispatchRotateRequest(ctx context.Context, d *daemon, peerID string, w *msg.Wrapper) {
	body, err := w.RotateRequest()
	if err != nil {
		slog.Warn("inbound rotate_request: body parse failed",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return
	}
	if d.rotation == nil {
		slog.Warn("inbound rotate_request: rotation manager not wired",
			slog.String("peer_id", peerID),
		)
		return
	}

	if d.backendClient != nil {
		if peer, perr := d.backendClient.Peer(ctx, peerID); perr == nil {
			if peer.PrevMyOnionExpiresAt > time.Now().Unix() {
				slog.Info("inbound rotate_request: refused (cooldown)",
					slog.String("peer_id", peerID),
					slog.String("rotation_id", body.RotationID),
					slog.Int64("cooldown_until", peer.PrevMyOnionExpiresAt),
				)
				_ = d.rotation.SendCancel(ctx, peerID, body.RotationID, msg.RotateCancelCooldown)
				return
			}
		}
	}

	if d.calls != nil {
		if chatID, cerr := resolveChatForPeer(ctx, d, peerID); cerr == nil {
			if states, lerr := d.calls.ListByChat(chatID); lerr == nil {
				for _, s := range states {
					if !s.IsTerminal() {
						slog.Info("inbound rotate_request: refused (in_call)",
							slog.String("peer_id", peerID),
							slog.String("rotation_id", body.RotationID),
							slog.String("call_id", s.CallID),
						)
						_ = d.rotation.SendCancel(ctx, peerID, body.RotationID, msg.RotateCancelInCall)
						return
					}
				}
			}
		}
	}

	if err := d.rotation.OnRequest(ctx, peerID, body.RotationID, body.ProposedAt); err != nil {
		slog.Info("inbound rotate_request: manager rejected",
			slog.String("peer_id", peerID),
			slog.String("rotation_id", body.RotationID),
			slog.Any("err", err),
		)
		if errors.Is(err, rotation.ErrInflight) {
			_ = d.rotation.SendCancel(ctx, peerID, body.RotationID, msg.RotateCancelConflict)
		} else {
			_ = d.rotation.SendCancel(ctx, peerID, body.RotationID, msg.RotateCancelInternal)
		}
		return
	}
	if err := d.rotation.UserAccept(ctx, body.RotationID); err != nil {
		slog.Warn("inbound rotate_request: auto-accept failed",
			slog.String("peer_id", peerID),
			slog.String("rotation_id", body.RotationID),
			slog.Any("err", err),
		)
	}
}

func dispatchRotateAccept(ctx context.Context, d *daemon, peerID string, w *msg.Wrapper) {
	body, err := w.RotateAccept()
	if err != nil {
		slog.Warn("inbound rotate_accept: body parse failed",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return
	}
	if d.rotation == nil {
		return
	}
	if err := d.rotation.OnAccept(ctx, peerID, body.RotationID); err != nil {
		slog.Warn("inbound rotate_accept: manager rejected",
			slog.String("peer_id", peerID),
			slog.String("rotation_id", body.RotationID),
			slog.Any("err", err),
		)
	}
}

func dispatchRotateAddress(ctx context.Context, d *daemon, peerID string, w *msg.Wrapper) {
	body, err := w.RotateAddress()
	if err != nil {
		slog.Warn("inbound rotate_address: body parse failed",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return
	}
	if d.rotation == nil {
		return
	}
	if err := d.rotation.OnAddress(ctx, peerID, body.RotationID, body.NewAddress); err != nil {
		slog.Warn("inbound rotate_address: manager rejected",
			slog.String("peer_id", peerID),
			slog.String("rotation_id", body.RotationID),
			slog.Any("err", err),
		)
	}
}

func dispatchRotateConfirm(ctx context.Context, d *daemon, peerID string, w *msg.Wrapper) {
	body, err := w.RotateConfirm()
	if err != nil {
		slog.Warn("inbound rotate_confirm: body parse failed",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return
	}
	if d.rotation == nil {
		return
	}
	if err := d.rotation.OnConfirm(ctx, peerID, body.RotationID); err != nil {
		slog.Warn("inbound rotate_confirm: manager rejected",
			slog.String("peer_id", peerID),
			slog.String("rotation_id", body.RotationID),
			slog.Any("err", err),
		)
	}
}

func dispatchRotateCancel(ctx context.Context, d *daemon, peerID string, w *msg.Wrapper) {
	body, err := w.RotateCancel()
	if err != nil {
		slog.Warn("inbound rotate_cancel: body parse failed",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return
	}
	if d.rotation == nil {
		return
	}
	if err := d.rotation.OnCancel(ctx, peerID, body.RotationID, body.Reason); err != nil {

		level := slog.LevelWarn
		if errors.Is(err, rotation.ErrNotFound) {
			level = slog.LevelDebug
		}
		slog.Log(ctx, level, "inbound rotate_cancel: manager rejected",
			slog.String("peer_id", peerID),
			slog.String("rotation_id", body.RotationID),
			slog.Any("err", err),
		)
	}
}
