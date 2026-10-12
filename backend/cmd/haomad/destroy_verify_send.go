package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"haoma/internal/peers"
	"haoma/internal/xport"
)

const destroyVerifyDialTimeout = 120 * time.Second

type destroyVerifySendRequest struct {
	PeerID string `json:"peer_id"`
	Proof  string `json:"proof"`
}

type destroyVerifySendResponse struct {
	Answered bool   `json:"answered"`
	Verified bool   `json:"verified"`
	Reason   string `json:"reason,omitempty"`
}

const destroyVerifyAnswerBad = "answer_did_not_check_out"

func (d *daemon) handleDestroyVerifySend(w http.ResponseWriter, r *http.Request) {
	var body destroyVerifySendRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("decode body: %w", err))
		return
	}
	if body.PeerID == "" || body.Proof == "" {
		writeErr(w, http.StatusBadRequest, errors.New("peer_id and proof are required"))
		return
	}
	peer, err := d.registry.Get(body.PeerID)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if len(peer.KnownAddresses) == 0 {
		writeErr(w, http.StatusServiceUnavailable, errors.New("peer has no address"))
		return
	}
	if peer.MyOnionAddr == "" {
		writeErr(w, http.StatusServiceUnavailable, errors.New("peer has no my-onion address"))
		return
	}
	dest := "http://" + peer.KnownAddresses[0] + ".onion"

	slog.Debug("destroy verify: asking a sibling",
		slog.String("sibling_peer_id", peer.ID),
	)

	payload, err := json.Marshal(destroyVerifyBody{Proof: body.Proof})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	envID, err := newEnvelopeID()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	env := xport.Envelope{
		ID:        envID,
		Timestamp: time.Now().Unix(),
		From:      peer.MyOnionAddr,
		Kind:      xport.KindDestroyVerify,
		Payload:   payload,
	}
	if err := xport.RandomPadding(&env); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	env = xport.Sign(env, peer.OutboundSecret)

	var respBody []byte
	hc, err := d.httpClientForPeer(peer.ID)
	if err == nil {
		respBody, err = sendDestroyVerify(r.Context(), hc, dest, env)
	}
	if errors.Is(err, xport.ErrCircuitRenewed) {

		if hc, err = d.httpClientForPeer(peer.ID); err == nil {
			respBody, err = sendDestroyVerify(r.Context(), hc, dest, env)
		}
	}
	if err != nil {

		slog.Info("destroy verify: no answer from the sibling",
			slog.String("sibling_peer_id", peer.ID),
			slog.Any("err", err),
		)
		writeJSON(w, http.StatusOK, destroyVerifySendResponse{})
		return
	}

	writeJSON(w, http.StatusOK, destroyVerifyAnswer(peer, respBody, envID))
}

func destroyVerifyAnswer(peer *peers.Peer, respBody []byte, envID string) destroyVerifySendResponse {
	if len(bytes.TrimSpace(respBody)) == 0 {
		slog.Info("destroy verify: the sibling sent nothing back",
			slog.String("sibling_peer_id", peer.ID),
		)
		return destroyVerifySendResponse{}
	}
	verified, err := readDestroyVerifyAck(peer, respBody, envID)
	if err != nil {
		slog.Warn("destroy verify: the answer did not check out",
			slog.String("sibling_peer_id", peer.ID),
			slog.Any("err", err),
		)
		return destroyVerifySendResponse{Reason: destroyVerifyAnswerBad}
	}

	slog.Info("destroy verify: the sibling answered",
		slog.String("sibling_peer_id", peer.ID),
		slog.Bool("verified", verified),
	)
	return destroyVerifySendResponse{Answered: true, Verified: verified}
}

func sendDestroyVerify(ctx context.Context, hc *http.Client, dest string, env xport.Envelope) ([]byte, error) {
	dial := *hc
	dial.Timeout = 0
	ctx, cancel := context.WithTimeout(ctx, destroyVerifyDialTimeout)
	defer cancel()
	return (&xport.Client{HTTP: &dial}).Send(ctx, dest, env)
}

func readDestroyVerifyAck(peer *peers.Peer, raw []byte, askedID string) (bool, error) {
	var ack xport.Envelope
	if err := json.Unmarshal(raw, &ack); err != nil {
		return false, fmt.Errorf("destroy_verify_ack parse: %w", err)
	}
	if ack.EffectiveKind() != xport.KindDestroyVerifyAck {
		return false, fmt.Errorf("destroy_verify_ack: unexpected kind %q", ack.EffectiveKind())
	}
	if err := xport.Verify(ack, peer.InboundSecret); err != nil {
		return false, err
	}
	var body destroyVerifyAckBody
	if err := json.Unmarshal(ack.Payload, &body); err != nil {
		return false, fmt.Errorf("destroy_verify_ack body: %w", err)
	}
	if body.VerifyID != askedID {
		return false, fmt.Errorf("destroy_verify_ack answers %q, asked %q", body.VerifyID, askedID)
	}
	return body.Verified, nil
}
