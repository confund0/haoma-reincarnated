package mirror

import (
	"context"
	"fmt"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/peerstate"
	"haoma-frontend/internal/syncrail"
)

type Fingerprints interface {
	Get(peerID string) (peerstate.MetaRecord, error)
}

type Rail struct {
	key      func() ([]byte, error)
	siblings func() []string
	fps      Fingerprints
}

func NewRail(key func() ([]byte, error), siblings func() []string, fps Fingerprints) Rail {
	return Rail{key: key, siblings: siblings, fps: fps}
}

func (r Rail) wired() bool {
	return r.key != nil && r.siblings != nil && r.fps != nil
}

func (r Rail) Key() ([]byte, error) {
	if r.key == nil {
		return nil, fmt.Errorf("mirror: rail has no key accessor")
	}
	return r.key()
}

func (r Rail) Siblings() []string {
	if r.siblings == nil {
		return nil
	}
	return r.siblings()
}

func SealAndSend(ctx context.Context, s Sender, key []byte, siblingPeerID string, env syncrail.Envelope) (envelopeID string, err error) {
	sealed, err := syncrail.SealEnvelope(key, env)
	if err != nil {
		return "", fmt.Errorf("mirror: seal for sibling: %w", err)
	}
	resp, err := s.Send(ctx, backendapi.SendRequest{
		PeerID:  siblingPeerID,
		Payload: sealed,
	})
	if err != nil {
		return "", err
	}
	return resp.EnvelopeID, nil
}

type Op struct {
	kind         msg.Kind
	chatID       string
	siblingOffer SiblingOfferFunc
}

func NewOp(kind msg.Kind, chatID string) Op {
	return Op{kind: kind, chatID: chatID}
}

type SiblingOfferFunc func(siblingPeerID string, primary *msg.Wrapper) (wrapper []byte, offer syncrail.SiblingOffer, err error)

func NewFileOfferOp(chatID string, siblingOffer SiblingOfferFunc) Op {
	return Op{kind: msg.KindFileOffer, chatID: chatID, siblingOffer: siblingOffer}
}

type PrepareFunc func(ctx context.Context, deviceID string) (req backendapi.SendRequest, plaintextWrapper []byte, err error)

type Targets struct {
	deviceIDs []string
}

func FanoutTargets(routingSet []string) Targets {
	return Targets{deviceIDs: routingSet}
}

func SingleTarget(deviceID string) Targets {
	return Targets{deviceIDs: []string{deviceID}}
}

func (t Targets) deviceList() []string {
	return t.deviceIDs
}
