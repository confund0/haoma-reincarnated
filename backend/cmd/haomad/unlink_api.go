package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"haoma/internal/peers"
	"haoma/internal/xport"
)

type unlinkRequest struct {
	PeerID string `json:"peer_id"`
	Proof  string `json:"proof"`

	NarrowOnly bool `json:"narrow_only,omitempty"`
}

type unlinkResponse struct {
	Narrowed  bool  `json:"narrowed"`
	Sent      bool  `json:"sent"`
	Receipted bool  `json:"receipted"`
	Deadline  int64 `json:"deadline"`
}

func (d *daemon) handleUnlink(w http.ResponseWriter, r *http.Request) {
	var body unlinkRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("decode body: %w", err))
		return
	}
	if body.PeerID == "" {
		writeErr(w, http.StatusBadRequest, errors.New("peer_id is required"))
		return
	}
	peer, err := d.registry.Get(body.PeerID)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}

	pending, err := d.beginUnlink(peer.ID)
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	out := unlinkResponse{Narrowed: true, Deadline: pending.Deadline}

	if body.NarrowOnly {

		slog.Info("unlink: narrowed and holding, the destroy has not been commanded yet",
			slog.String("peer_id", peer.ID),
		)
		writeJSON(w, http.StatusOK, out)
		return
	}

	if body.Proof == "" {

		d.finishUnlink("unlink, nothing was commanded")
		slog.Info("unlink: finished at once, nothing was commanded",
			slog.String("peer_id", peer.ID),
		)
		out.Deadline = 0
		writeJSON(w, http.StatusOK, out)
		return
	}

	if len(peer.KnownAddresses) == 0 || peer.MyOnionAddr == "" {

		d.armSelfErase(peer.ID, body.Proof)
		slog.Info("unlink: narrowed, but that device has no address to command",
			slog.String("peer_id", peer.ID),
		)
		writeJSON(w, http.StatusOK, out)
		return
	}

	envID, receipted, err := d.sendDestroyCommand(r, peer, body.Proof)
	if err != nil {

		d.armSelfErase(peer.ID, body.Proof)
		slog.Info("unlink: the command did not reach that device",
			slog.String("peer_id", peer.ID),
			slog.Any("err", err),
		)
		writeJSON(w, http.StatusOK, out)
		return
	}
	out.Sent = true
	out.Receipted = receipted

	slog.Info("unlink: that device answered",
		slog.String("peer_id", peer.ID),
		slog.String("envelope_id", envID),
		slog.Bool("receipted", receipted),
	)
	if receipted {
		d.finishUnlink("the receipt arrived")
	}
	writeJSON(w, http.StatusOK, out)
}

func (d *daemon) sendDestroyCommand(r *http.Request, peer *peers.Peer, proof string) (envelopeID string, receipted bool, err error) {
	payload, err := json.Marshal(destroyVerifyBody{Proof: proof})
	if err != nil {
		return "", false, err
	}
	envID, err := newEnvelopeID()
	if err != nil {
		return "", false, err
	}
	env := xport.Envelope{
		ID:        envID,
		Timestamp: time.Now().Unix(),
		From:      peer.MyOnionAddr,
		Kind:      xport.KindDestroy,
		Payload:   payload,
	}
	if err := xport.RandomPadding(&env); err != nil {
		return "", false, err
	}
	env = xport.Sign(env, peer.OutboundSecret)

	dest := "http://" + peer.KnownAddresses[0] + ".onion"
	sender := &perPeerSender{d: d}
	respBody, err := sender.Send(r.Context(), dest, env)
	if errors.Is(err, xport.ErrCircuitRenewed) {

		respBody, err = sender.Send(r.Context(), dest, env)
	}
	if err != nil {
		return envID, false, err
	}

	if err := readDestroyReceipt(peer, respBody, envID); err != nil {

		slog.Info("unlink: no receipt in the answer",
			slog.String("peer_id", peer.ID),
			slog.Any("err", err),
		)
		return envID, false, nil
	}
	return envID, true, nil
}

func readDestroyReceipt(peer *peers.Peer, raw []byte, sentID string) error {
	if len(raw) == 0 {
		return errors.New("destroy_receipt: the answer was empty")
	}
	var receipt xport.Envelope
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return fmt.Errorf("destroy_receipt parse: %w", err)
	}
	if receipt.EffectiveKind() != xport.KindDestroyReceipt {
		return fmt.Errorf("destroy_receipt: unexpected kind %q", receipt.EffectiveKind())
	}
	if err := xport.Verify(receipt, peer.InboundSecret); err != nil {
		return err
	}
	var body destroyReceiptBody
	if err := json.Unmarshal(receipt.Payload, &body); err != nil {
		return fmt.Errorf("destroy_receipt body: %w", err)
	}
	if body.DestroyID != sentID {
		return fmt.Errorf("destroy_receipt answers %q, sent %q", body.DestroyID, sentID)
	}
	return nil
}

type unlinkPendingResponse struct {
	PeerID    string `json:"peer_id"`
	Deadline  int64  `json:"deadline"`
	StartedAt int64  `json:"started_at"`
}

func (d *daemon) handleUnlinkPending(w http.ResponseWriter, _ *http.Request) {
	rec, err := d.store.GetUnlinkPending()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, unlinkPendingResponse{
		PeerID:    rec.PeerID,
		Deadline:  rec.Deadline,
		StartedAt: rec.StartedAt,
	})
}

func (d *daemon) handleUnlinkStopWaiting(w http.ResponseWriter, _ *http.Request) {
	rec, err := d.store.GetUnlinkPending()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if !rec.InFlight() {

		writeJSON(w, http.StatusOK, unlinkPendingResponse{})
		return
	}
	d.finishUnlink("the operator stopped waiting for the receipt")
	writeJSON(w, http.StatusOK, unlinkPendingResponse{})
}
