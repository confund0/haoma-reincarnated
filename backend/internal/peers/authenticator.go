package peers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"haoma/internal/ids"
	"haoma/internal/xport"
)

type HMACVerifier struct {
	Registry *Registry

	IDS *ids.IDS

	OnVerifySuccess func(ctx context.Context, peerID string)

	Now func() time.Time
}

var ErrUnknownSource = errors.New("contacts: envelope from unknown source address")

func (v *HMACVerifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

func (v *HMACVerifier) Verify(ctx context.Context, env xport.Envelope) error {

	slotIdx, _ := xport.SlotFromContext(ctx)

	peer, err := v.Registry.ByAddress(env.From)
	if errors.Is(err, ErrPeerNotFound) {
		v.observe(ctx, ids.Event{
			Kind:       ids.EventUnknownSource,
			At:         v.now(),
			SourceAddr: env.From,
			SlotIdx:    slotIdx,
		})
		return fmt.Errorf("%w: %s", ErrUnknownSource, env.From)
	}
	if err != nil {
		return err
	}
	if err := xport.Verify(env, peer.InboundSecret); err != nil {
		ev := ids.Event{
			Kind:       ids.EventBadMAC,
			At:         v.now(),
			PeerID:     peer.ID,
			SourceAddr: env.From,
			SlotIdx:    slotIdx,
		}
		if v.IDS != nil {
			v.observe(ctx, ev)
		} else {

			_ = v.Registry.RecordViolation(peer.ID, env.From)
		}
		return err
	}
	_ = v.Registry.TouchPresence(peer.ID, v.now(), env.PresenceSource)
	v.observe(ctx, ids.Event{
		Kind:       ids.EventConnectionAccepted,
		At:         v.now(),
		PeerID:     peer.ID,
		SourceAddr: env.From,
		SlotIdx:    slotIdx,
	})

	xport.StampPeerID(ctx, peer.ID)
	if v.OnVerifySuccess != nil {
		v.OnVerifySuccess(ctx, peer.ID)
	}
	return nil
}

func (v *HMACVerifier) observe(ctx context.Context, ev ids.Event) {
	if v.IDS == nil {
		return
	}
	actions := v.IDS.Observe(ctx, ev)
	for _, a := range actions {
		switch act := a.(type) {
		case ids.CounterBump:

			if act.PeerID != "" && act.SourceAddr != "" {
				_ = v.Registry.RecordViolation(act.PeerID, act.SourceAddr)
			}
		case ids.AlertEmit:

		case ids.PanicRotate:

		}
	}
}
