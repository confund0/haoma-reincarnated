package mirror

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/peerstate"
	"haoma-frontend/internal/syncrail"
)

type Sender interface {
	Send(ctx context.Context, req backendapi.SendRequest) (backendapi.SendResponse, error)
}

const (
	StagePrepare = "prepare"
	StageSend    = "send"
)

type SkipRecord struct {
	DeviceID string
	Stage    string
	Err      error
}

type PrimaryResult struct {
	DeviceID   string
	EnvelopeID string
	Seq        uint64
}

type Result struct {
	EnvelopeIDs []string
	Primary     PrimaryResult
	Skipped     []SkipRecord

	SiblingOffersSent []string
}

type DispatchError struct {
	Stage string
	Err   error
}

func (e *DispatchError) Error() string { return e.Err.Error() }
func (e *DispatchError) Unwrap() error { return e.Err }

type acceptedDevice struct {
	peerID     string
	wrapper    []byte
	envelopeID string
}

func Dispatch(ctx context.Context, s Sender, rail Rail, targets Targets, prepare PrepareFunc, op Op) (Result, error) {
	class, ok := Classify(op.kind)
	if !ok {
		return Result{}, fmt.Errorf("mirror: unclassified wire kind %q — add it to the registry", op.kind)
	}

	devices := targets.deviceList()
	if len(devices) == 0 {
		return Result{}, fmt.Errorf("mirror: no target devices supplied")
	}

	var (
		result   Result
		accepted []acceptedDevice
		lastFail *DispatchError
	)

	for _, deviceID := range devices {
		req, wrapper, err := prepare(ctx, deviceID)
		if err != nil {
			slog.Debug("mirror: device could not be prepared; skipped",
				slog.String("kind", string(op.kind)),
				slog.String("device_id", deviceID),
				slog.Any("err", err),
			)
			result.Skipped = append(result.Skipped, SkipRecord{DeviceID: deviceID, Stage: StagePrepare, Err: err})
			lastFail = &DispatchError{Stage: StagePrepare, Err: err}
			continue
		}
		resp, sErr := s.Send(ctx, req)
		if sErr != nil {
			slog.Warn("mirror: send to a device failed; skipped",
				slog.String("kind", string(op.kind)),
				slog.String("device_id", deviceID),
				slog.Any("err", sErr),
			)
			result.Skipped = append(result.Skipped, SkipRecord{DeviceID: deviceID, Stage: StageSend, Err: sErr})
			lastFail = &DispatchError{Stage: StageSend, Err: sErr}
			continue
		}
		slog.Debug("mirror: device accepted the send",
			slog.String("kind", string(op.kind)),
			slog.String("device_id", deviceID),
			slog.String("envelope_id", resp.EnvelopeID),
		)
		result.EnvelopeIDs = append(result.EnvelopeIDs, resp.EnvelopeID)
		accepted = append(accepted, acceptedDevice{peerID: deviceID, wrapper: wrapper, envelopeID: resp.EnvelopeID})
	}

	if len(accepted) == 0 {
		if lastFail == nil {
			lastFail = &DispatchError{Stage: StagePrepare, Err: fmt.Errorf("mirror: no target devices")}
		}
		slog.Warn("mirror: no device could be enqueued for; send failed",
			slog.String("kind", string(op.kind)),
			slog.Int("targets", len(devices)),
		)
		return Result{}, lastFail
	}

	first := accepted[0]
	primary := PrimaryResult{DeviceID: first.peerID, EnvelopeID: first.envelopeID}

	if len(first.wrapper) > 0 {
		if w, err := msg.Unmarshal(first.wrapper); err != nil {
			slog.Warn("mirror: primary device's wrapper did not parse; sender seq unavailable in the reply",
				slog.String("kind", string(op.kind)),
				slog.String("device_id", first.peerID),
				slog.Any("err", err),
			)
		} else {
			primary.Seq = w.Seq
		}
	}
	result.Primary = primary

	if class == ClassMirror {
		result.SiblingOffersSent = mirrorToSiblings(ctx, s, rail, accepted, op)
	}
	return result, nil
}

func mirrorToSiblings(ctx context.Context, s Sender, rail Rail, accepted []acceptedDevice, op Op) []string {
	if !rail.wired() {
		slog.Error("mirror: rail not wired at this call site; mutation NOT mirrored",
			slog.String("kind", string(op.kind)),
		)
		return nil
	}

	sibs := rail.siblings()
	slog.Debug("mirror: considering sibling fan-out",
		slog.String("kind", string(op.kind)),
		slog.Int("sibling_count", len(sibs)),
		slog.Int("accepted_devices", len(accepted)),
	)
	if len(sibs) == 0 {

		return nil
	}

	origin := accepted[0]

	if len(origin.wrapper) == 0 {

		slog.Error("mirror: no plaintext wrapper on a mirror-class op; skipping fan-out",
			slog.String("kind", string(op.kind)),
		)
		return nil
	}

	var (
		rec   peerstate.MetaRecord
		found bool
	)
	for _, a := range accepted {
		r, err := rail.fps.Get(a.peerID)
		if err != nil {
			slog.Warn("mirror: peer meta read failed for a device; trying the next accepted device",
				slog.String("kind", string(op.kind)),
				slog.String("peer_id", a.peerID),
				slog.Any("err", err),
			)
			continue
		}
		if r.Fingerprint == "" {
			slog.Debug("mirror: device has no stored fingerprint yet; trying the next accepted device",
				slog.String("kind", string(op.kind)),
				slog.String("peer_id", a.peerID),
			)
			continue
		}
		rec, found = r, true
		break
	}
	if !found {
		slog.Debug("mirror: no accepted device had a usable fingerprint; skipping fan-out",
			slog.String("kind", string(op.kind)),
			slog.Int("accepted_devices", len(accepted)),
		)
		return nil
	}

	w, err := msg.Unmarshal(origin.wrapper)
	if err != nil {
		slog.Warn("mirror: plaintext wrapper did not parse; skipping fan-out",
			slog.String("kind", string(op.kind)),
			slog.String("peer_id", origin.peerID),
			slog.Any("err", err),
		)
		return nil
	}

	key, err := rail.key()
	if err != nil {
		slog.Warn("mirror: rail key unavailable; skipping fan-out",
			slog.String("kind", string(op.kind)),
			slog.String("msg_id", w.MsgID),
			slog.Any("err", err),
		)
		return nil
	}

	if op.kind == msg.KindFileOffer {
		return sendSiblingOffers(ctx, s, key, sibs, rec.Fingerprint, w, origin.peerID, op)
	}

	body, err := json.Marshal(syncrail.MirrorBody{
		Fingerprint:  rec.Fingerprint,
		Wrapper:      origin.wrapper,
		WireKind:     string(op.kind),
		MsgID:        w.MsgID,
		DisplayTs:    w.Ts,
		OriginChatID: op.chatID,
		OriginPeerID: origin.peerID,
	})
	if err != nil {
		slog.Warn("mirror: mirror body marshal failed; skipping fan-out",
			slog.String("kind", string(op.kind)),
			slog.String("msg_id", w.MsgID),
			slog.Any("err", err),
		)
		return nil
	}
	env := syncrail.Envelope{
		V:    syncrail.Version,
		Kind: syncrail.KindMirror,
		Ts:   time.Now().Unix(),
		Body: body,
	}

	sent := 0
	for _, sib := range sibs {
		envelopeID, sErr := SealAndSend(ctx, s, key, sib, env)
		if sErr != nil {
			slog.Warn("mirror: sibling send failed; that device stays behind",
				slog.String("kind", string(op.kind)),
				slog.String("sibling_peer_id", sib),
				slog.String("msg_id", w.MsgID),
				slog.Any("err", sErr),
			)
			continue
		}
		sent++
		slog.Debug("mirror: mutation mirrored to sibling",
			slog.String("kind", string(op.kind)),
			slog.String("sibling_peer_id", sib),
			slog.String("msg_id", w.MsgID),
			slog.String("envelope_id", envelopeID),
		)
	}
	slog.Debug("mirror: sibling fan-out complete",
		slog.String("kind", string(op.kind)),
		slog.String("msg_id", w.MsgID),
		slog.Int("sibling_count", len(sibs)),
		slog.Int("mirrored", sent),
	)
	return nil
}

func sendSiblingOffers(ctx context.Context, s Sender, key []byte, sibs []string, fingerprint string, primary *msg.Wrapper, originPeerID string, op Op) []string {
	if op.siblingOffer == nil {
		slog.Error("mirror: file offer has no sibling offer to send; no sibling gets it",
			slog.String("kind", string(op.kind)),
			slog.String("msg_id", primary.MsgID),
			slog.Int("sibling_count", len(sibs)),
		)
		return nil
	}

	var sent []string
	for _, sib := range sibs {
		wrapper, offer, err := op.siblingOffer(sib, primary)
		if err != nil {
			slog.Warn("mirror: sibling offer could not be built; that device stays behind",
				slog.String("sibling_peer_id", sib),
				slog.String("msg_id", primary.MsgID),
				slog.Any("err", err),
			)
			continue
		}
		slog.Debug("mirror: sibling offer built",
			slog.String("sibling_peer_id", sib),
			slog.String("msg_id", primary.MsgID),
			slog.Any("sibling_offer", offer),
		)
		body, err := json.Marshal(syncrail.MirrorBody{
			Fingerprint:  fingerprint,
			Wrapper:      wrapper,
			WireKind:     string(op.kind),
			MsgID:        primary.MsgID,
			DisplayTs:    primary.Ts,
			OriginChatID: op.chatID,
			OriginPeerID: originPeerID,
			SiblingOffer: &offer,
		})
		offer.Wipe()
		if err != nil {
			slog.Warn("mirror: sibling offer marshal failed; that device stays behind",
				slog.String("sibling_peer_id", sib),
				slog.String("msg_id", primary.MsgID),
				slog.Any("err", err),
			)
			continue
		}
		envelopeID, sErr := SealAndSend(ctx, s, key, sib, syncrail.Envelope{
			V:    syncrail.Version,
			Kind: syncrail.KindMirror,
			Ts:   time.Now().Unix(),
			Body: body,
		})
		for i := range body {
			body[i] = 0
		}
		if sErr != nil {
			slog.Warn("mirror: sibling offer send failed; that device stays behind",
				slog.String("sibling_peer_id", sib),
				slog.String("msg_id", primary.MsgID),
				slog.Any("err", sErr),
			)
			continue
		}
		sent = append(sent, sib)
		slog.Debug("mirror: sibling offer sent",
			slog.String("sibling_peer_id", sib),
			slog.String("msg_id", primary.MsgID),
			slog.String("envelope_id", envelopeID),
		)
	}
	slog.Debug("mirror: sibling offer fan-out complete",
		slog.String("msg_id", primary.MsgID),
		slog.Int("sibling_count", len(sibs)),
		slog.Int("sent", len(sent)),
	)
	return sent
}
