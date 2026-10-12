package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"time"

	"haoma/internal/ids"
	"haoma/internal/store"
	"haoma/internal/xport"
)

const destroyAttemptLimit = 3

const destroyCooloffSeconds = 3600

type destroyVerifyBody struct {
	Proof string `json:"proof"`
}

type destroyVerifyAckBody struct {
	VerifyID string `json:"verify_id"`
	Verified bool   `json:"verified"`
}

func respondDestroyVerify(d *daemon, ctx context.Context, req xport.Envelope) (xport.Envelope, error) {

	peer, err := peerForVerified(d, ctx, req)
	if err != nil {
		return xport.Envelope{}, err
	}
	outcome, err := checkDestroyProofEnvelope(d, req, peer.ID)
	if err != nil {
		return xport.Envelope{}, err
	}
	if outcome == destroyProofDropped {
		return xport.Envelope{}, nil
	}
	if peer.MyOnionAddr == "" {
		return xport.Envelope{}, nil
	}
	body, err := json.Marshal(destroyVerifyAckBody{VerifyID: req.ID, Verified: outcome == destroyProofRight})
	if err != nil {
		return xport.Envelope{}, err
	}
	envID, err := newEnvelopeID()
	if err != nil {
		return xport.Envelope{}, err
	}
	ack := xport.Envelope{
		ID:        envID,
		Timestamp: time.Now().Unix(),
		From:      peer.MyOnionAddr,
		Kind:      xport.KindDestroyVerifyAck,
		Payload:   body,
	}
	if err := xport.RandomPadding(&ack); err != nil {
		return xport.Envelope{}, err
	}
	return xport.Sign(ack, peer.OutboundSecret), nil
}

type destroyProofOutcome int

const (
	destroyProofDropped destroyProofOutcome = iota

	destroyProofWrong

	destroyProofRight
)

const (
	proofSourceEnvelope   = "envelope"
	proofSourceRailAnswer = "rail answer"
)

func proofOrigin(originID, source string) []any {
	if originID == "" {
		return []any{slog.String("source", source)}
	}
	return []any{slog.String("envelope_id", originID), slog.String("source", source)}
}

func checkDestroyProofEnvelope(d *daemon, req xport.Envelope, senderPeerID string) (destroyProofOutcome, error) {
	var proof []byte
	var body destroyVerifyBody
	if jErr := json.Unmarshal(req.Payload, &body); jErr == nil {
		if raw, hErr := hex.DecodeString(body.Proof); hErr == nil {
			proof = raw
		}
	}
	return checkDestroyProof(d, req.ID, proofSourceEnvelope, proof, senderPeerID)
}

func checkDestroyProof(d *daemon, originID, source string, proof []byte, senderPeerID string) (destroyProofOutcome, error) {
	now := time.Now().Unix()
	outcome := destroyProofDropped
	refused := false

	err := d.store.UpdateDestroyAttempts(func(guard store.DestroyPIN, att store.DestroyAttempts) (store.DestroyAttempts, bool, error) {

		if !guard.Set {
			slog.Info("destroy verify: dropped, no guard is set on this device",
				proofOrigin(originID, source)...)
			return att, false, nil
		}

		if senderPeerID != guard.SiblingPeerID {
			slog.Info("destroy verify: dropped, not the peer this guard belongs to",
				append(proofOrigin(originID, source),
					slog.String("sender_peer_id", senderPeerID))...)
			return att, false, nil
		}

		if att.BlockedUntil > now {
			att.Unseen++
			att.LastAt = now
			refused = true
			outcome = destroyProofWrong
			slog.Info("destroy verify: refused during cool-off",
				append(proofOrigin(originID, source),
					slog.Int64("seconds_left", att.BlockedUntil-now))...)
			return att, true, nil
		}

		ok := false
		if len(proof) > 0 {

			sum := sha256.Sum256(proof)
			ok = subtle.ConstantTimeCompare(sum[:], guard.Verifier) == 1
		}

		if !ok {
			att.Failures++

			att.Unseen++
			att.LastAt = now
			refused = true
			outcome = destroyProofWrong
			if att.Failures >= destroyAttemptLimit {
				att.Failures = 0
				att.BlockedUntil = now + destroyCooloffSeconds
				slog.Info("destroy verify: attempts used up, stopping for now",
					append(proofOrigin(originID, source),
						slog.Int("limit", destroyAttemptLimit),
						slog.Int64("blocked_until", att.BlockedUntil))...)
			} else {
				slog.Info("destroy verify: wrong proof",
					append(proofOrigin(originID, source),
						slog.Int("failures", att.Failures))...)
			}
			return att, true, nil
		}

		outcome = destroyProofRight
		slog.Info("destroy verify: proof accepted",
			proofOrigin(originID, source)...)

		if att.Failures != 0 || att.BlockedUntil != 0 {
			return store.DestroyAttempts{Unseen: att.Unseen, LastAt: att.LastAt}, true, nil
		}
		return att, false, nil
	})
	if err != nil {
		return destroyProofDropped, err
	}

	if refused && d.ids != nil {
		d.ids.Observe(context.Background(), ids.Event{
			Kind:    ids.EventBadDestroyProof,
			At:      time.Now(),
			PeerID:  senderPeerID,
			SlotIdx: ids.SlotUnknown,
			Detail:  "wrong destroy proof",
		})
	}
	return outcome, nil
}
