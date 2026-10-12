package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"haoma/internal/xport"
)

type destroyReceiptBody struct {
	DestroyID string `json:"destroy_id"`
}

func respondDestroy(d *daemon, ctx context.Context, req xport.Envelope) (xport.Envelope, error) {
	peer, err := peerForVerified(d, ctx, req)
	if err != nil {
		return xport.Envelope{}, err
	}

	outcome, err := checkDestroyProofEnvelope(d, req, peer.ID)
	if err != nil {
		return xport.Envelope{}, err
	}
	if outcome != destroyProofRight {

		slog.Info("destroy: refused", slog.String("envelope_id", req.ID))
		return xport.Envelope{}, nil
	}

	if d.cfg.dataRoot == "" || peer.MyOnionAddr == "" {
		return xport.Envelope{}, nil
	}
	body, err := json.Marshal(destroyReceiptBody{DestroyID: req.ID})
	if err != nil {
		return xport.Envelope{}, err
	}
	envID, err := newEnvelopeID()
	if err != nil {
		return xport.Envelope{}, err
	}
	receipt := xport.Envelope{
		ID:        envID,
		Timestamp: time.Now().Unix(),
		From:      peer.MyOnionAddr,
		Kind:      xport.KindDestroyReceipt,
		Payload:   body,
	}
	if err := xport.RandomPadding(&receipt); err != nil {
		return xport.Envelope{}, err
	}
	signed := xport.Sign(receipt, peer.OutboundSecret)

	eraseID := newEraseID()
	slog.Info("destroy: proof accepted, erasing this device",
		slog.String("envelope_id", req.ID),
		slog.String("erase_id", eraseID),
	)

	go d.eraseAndExit("the other device commanded it", eraseID)

	return signed, nil
}
