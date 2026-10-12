package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"

	"fmt"
	"log/slog"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/destroypin"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/linkeddevice"
	"haoma-frontend/internal/syncrail"
)

func (sd *sessionDispatcher) handleGetDestroyPIN(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("destroy pin: handle state read")
	d := sd.d
	if d.backendClient == nil {
		sendError(sess, f.ID, reasonNotReady, "frontend wiring incomplete")
		return
	}
	state, err := d.backendClient.DestroyPIN(ctx)
	if err != nil {
		slog.Warn("destroy pin: state read failed",
			slog.String("reason", reasonBackendDown),
			slog.Any("err", err),
		)
		sendError(sess, f.ID, reasonBackendDown, err.Error())
		return
	}
	out := ipc.DestroyPINListedResponse{
		Set:      state.Set,
		SetAt:    state.SetAt,
		Siblings: []ipc.DestroyPINSiblingEntry{},
	}
	for _, peerID := range d.siblings() {
		entry := ipc.DestroyPINSiblingEntry{PeerID: peerID}
		if d.linkedDevices != nil {
			rec, held, sErr := d.linkedDevices.SiblingSalt(peerID)
			if sErr != nil {
				slog.Warn("destroy pin: could not read a sibling's salt",
					slog.String("sibling_peer_id", peerID),
					slog.Any("err", sErr),
				)
			} else {
				entry.SaltHeld = held
				entry.ReceivedAt = rec.ReceivedAt
			}
		}
		out.Siblings = append(out.Siblings, entry)
	}

	if pending, pErr := d.backendClient.UnlinkPending(ctx); pErr != nil {
		slog.Warn("destroy pin: the destroy being waited on could not be read",
			slog.Any("err", pErr),
		)
	} else if pending.PeerID != "" {
		out.Waiting = &ipc.DestroyWaiting{
			PeerID:    pending.PeerID,
			Deadline:  pending.Deadline,
			StartedAt: pending.StartedAt,
		}
	}
	slog.Debug("destroy pin: state answered",
		slog.Bool("set", out.Set),
		slog.Int("sibling_count", len(out.Siblings)),
		slog.Bool("waiting_on_a_destroy", out.Waiting != nil),
	)
	resp, err := ipc.NewFrame(ipc.FrameDestroyPINListed, f.ID, out)
	if err != nil {
		sendError(sess, f.ID, reasonEncodeFrame, err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("destroy pin: send state frame failed", slog.Any("err", err))
	}
}

func (sd *sessionDispatcher) handleSetDestroyPIN(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("destroy pin: handle set")
	d := sd.d
	var req ipc.SetDestroyPINRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, reasonBadRequest, "decode payload")
		return
	}
	if d.backendClient == nil {
		sendError(sess, f.ID, reasonNotReady, "frontend wiring incomplete")
		return
	}

	out, err := setLocalDestroyPIN(ctx, d, req)
	if err != nil {
		slog.Warn("destroy pin: set failed",
			slog.String("reason", reasonBackendDown),
			slog.Any("err", err),
		)
		sendError(sess, f.ID, reasonBackendDown, err.Error())
		return
	}
	resp, nErr := ipc.NewFrame(ipc.FrameDestroyPINSet, f.ID, out)
	if nErr != nil {
		sendError(sess, f.ID, reasonEncodeFrame, nErr.Error())
		return
	}
	if sErr := sess.Send(resp); sErr != nil {
		slog.Warn("destroy pin: send set frame failed", slog.Any("err", sErr))
	}
}

func setLocalDestroyPIN(ctx context.Context, d *daemon, req ipc.SetDestroyPINRequest) (ipc.DestroyPINSetResponse, error) {
	if err := destroypin.ValidatePIN(req.New); err != nil {
		slog.Info("destroy pin: set refused",
			slog.String("reason", ipc.DestroyPINRefusedShape),
		)
		return ipc.DestroyPINSetResponse{Reason: ipc.DestroyPINRefusedShape}, nil
	}

	current, err := d.backendClient.DestroyPIN(ctx)
	if err != nil {
		return ipc.DestroyPINSetResponse{}, fmt.Errorf("read the current guard: %w", err)
	}
	slog.Debug("destroy pin: set observed",
		slog.Bool("guard_already_set", current.Set),
	)

	if current.Set {
		slog.Info("destroy pin: set refused",
			slog.String("reason", ipc.DestroyPINRefusedAlreadySet),
		)
		return ipc.DestroyPINSetResponse{Reason: ipc.DestroyPINRefusedAlreadySet}, nil
	}

	sibs := d.siblings()
	if len(sibs) != 1 {
		slog.Info("destroy pin: set refused",
			slog.String("reason", ipc.DestroyPINRefusedNoSibling),
			slog.Int("siblings", len(sibs)),
		)
		return ipc.DestroyPINSetResponse{Reason: ipc.DestroyPINRefusedNoSibling}, nil
	}

	backendReq := backendapi.SetDestroyPINRequest{SiblingPeerID: sibs[0]}
	salt, err := destroypin.NewSalt()
	if err != nil {
		return ipc.DestroyPINSetResponse{}, fmt.Errorf("generate a salt: %w", err)
	}

	proof, err := destroypin.Derive(req.New, salt)
	if err != nil {
		return ipc.DestroyPINSetResponse{}, fmt.Errorf("derive the proof: %w", err)
	}
	verifier := destroypin.Verifier(proof)
	destroypin.Zero(proof)
	backendReq.Salt = hex.EncodeToString(salt)
	backendReq.Verifier = hex.EncodeToString(verifier)

	stored, err := d.backendClient.SetDestroyPIN(ctx, backendReq)
	if err != nil {
		return ipc.DestroyPINSetResponse{}, fmt.Errorf("store the guard: %w", err)
	}
	if !stored.Accepted {

		slog.Info("destroy pin: set refused",
			slog.String("reason", ipc.DestroyPINRefusedAlreadySet),
		)
		return ipc.DestroyPINSetResponse{Reason: ipc.DestroyPINRefusedAlreadySet}, nil
	}

	setAt := time.Now().Unix()

	slog.Info("destroy pin: guard set on this device")

	pushed := pushDestroySalt(ctx, d, sibs, salt, setAt)
	return ipc.DestroyPINSetResponse{
		Accepted:   true,
		SaltPushed: pushed,
		Siblings:   len(sibs),
	}, nil
}

func pushDestroySalt(ctx context.Context, d *daemon, siblings []string, salt []byte, setAt int64) int {
	if len(siblings) == 0 {

		slog.Debug("destroy pin: no siblings to send the salt to")
		return 0
	}
	pushed, held := 0, 0
	for _, sib := range siblings {
		if ctx.Err() != nil {
			return pushed
		}
		if holdSaltBehindBundle(d, sib, salt, setAt) {
			held++
			continue
		}
		if sendSaltTo(ctx, d, sib, salt, setAt) {
			pushed++
		}
	}
	slog.Info("destroy pin: salt push finished",
		slog.Int("siblings", len(siblings)),
		slog.Int("pushed", pushed),
		slog.Int("waiting_for_identity_bundle", held),
	)
	return pushed
}

func applyDestroySalt(_ context.Context, d *daemon, siblingPeerID string, body syncrail.DestroySaltBody, salt []byte) {
	logger := slog.With(
		slog.String("sibling_peer_id", siblingPeerID),
		slog.Int("salt_bytes", len(salt)),
	)
	logger.Debug("destroy pin: a sibling's salt received",
		slog.Int64("sibling_set_at", body.SetAt),
	)
	if d == nil || d.linkedDevices == nil {
		logger.Warn("destroy pin: drop — no linked-device store",
			slog.String("reason", "no_linked_device_store"),
		)
		return
	}
	if destroySaltDropped(logger) {
		return
	}

	if rec, held, gErr := d.linkedDevices.SiblingSalt(siblingPeerID); gErr == nil && held && bytes.Equal(rec.Salt, salt) {
		logger.Info("destroy pin: a sibling's salt received again; already held, nothing changed")
		return
	}
	replaced, err := d.linkedDevices.PutSiblingSalt(siblingPeerID, linkeddevice.SiblingSalt{
		Salt:       salt,
		SetAt:      body.SetAt,
		ReceivedAt: time.Now().Unix(),
	})
	if err != nil {
		logger.Warn("destroy pin: drop — a sibling's salt would not store",
			slog.String("reason", "salt_store_failed"),
			slog.Any("err", err),
		)
		return
	}
	if replaced {
		logger.Info("destroy pin: a sibling's salt stored, replacing an older one")
		return
	}
	logger.Info("destroy pin: a sibling's salt stored, the first from that device")
}

func (sd *sessionDispatcher) handleVerifyDestroyPIN(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("destroy pin: handle verify")
	d := sd.d
	var req ipc.VerifyDestroyPINRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, reasonBadRequest, "decode payload")
		return
	}
	if d.backendClient == nil || d.linkedDevices == nil {
		sendError(sess, f.ID, reasonNotReady, "frontend wiring incomplete")
		return
	}

	out, err := verifySiblingDestroyPIN(ctx, d, req)
	if err != nil {
		slog.Warn("destroy pin: verify failed",
			slog.String("reason", reasonBackendDown),
			slog.Any("err", err),
		)
		sendError(sess, f.ID, reasonBackendDown, err.Error())
		return
	}
	resp, nErr := ipc.NewFrame(ipc.FrameDestroyPINVerified, f.ID, out)
	if nErr != nil {
		sendError(sess, f.ID, reasonEncodeFrame, nErr.Error())
		return
	}
	if sErr := sess.Send(resp); sErr != nil {
		slog.Warn("destroy pin: send verify frame failed", slog.Any("err", sErr))
	}
}

func verifySiblingDestroyPIN(ctx context.Context, d *daemon, req ipc.VerifyDestroyPINRequest) (ipc.DestroyPINVerifiedResponse, error) {

	if err := destroypin.ValidatePIN(req.PIN); err != nil {
		slog.Info("destroy pin: verify refused", slog.String("reason", ipc.DestroyPINRefusedShape))
		return ipc.DestroyPINVerifiedResponse{Reason: ipc.DestroyPINRefusedShape}, nil
	}
	mine := false
	for _, peerID := range d.siblings() {
		if peerID == req.PeerID {
			mine = true
			break
		}
	}
	if !mine {
		slog.Info("destroy pin: verify refused",
			slog.String("reason", ipc.DestroyPINVerifyUnknownSibling),
		)
		return ipc.DestroyPINVerifiedResponse{Reason: ipc.DestroyPINVerifyUnknownSibling}, nil
	}
	rec, held, err := d.linkedDevices.SiblingSalt(req.PeerID)
	if err != nil {
		return ipc.DestroyPINVerifiedResponse{}, fmt.Errorf("read the sibling's salt: %w", err)
	}
	if !held || len(rec.Salt) == 0 {

		slog.Info("destroy pin: verify refused",
			slog.String("reason", ipc.DestroyPINVerifyNoSalt),
			slog.String("sibling_peer_id", req.PeerID),
		)
		return ipc.DestroyPINVerifiedResponse{Reason: ipc.DestroyPINVerifyNoSalt}, nil
	}

	proof, err := destroypin.Derive(req.PIN, rec.Salt)
	if err != nil {
		return ipc.DestroyPINVerifiedResponse{}, fmt.Errorf("derive the proof: %w", err)
	}
	proofHex := hex.EncodeToString(proof)
	destroypin.Zero(proof)

	slog.Debug("destroy pin: asking a sibling to check a proof",
		slog.String("sibling_peer_id", req.PeerID),
	)
	answer, err := d.backendClient.VerifyDestroyPIN(ctx, backendapi.VerifyDestroyPINRequest{
		PeerID: req.PeerID,
		Proof:  proofHex,
	})
	if err != nil {
		return ipc.DestroyPINVerifiedResponse{}, fmt.Errorf("ask the sibling: %w", err)
	}
	slog.Info("destroy pin: verify answered",
		slog.String("sibling_peer_id", req.PeerID),
		slog.Bool("answered", answer.Answered),
		slog.Bool("verified", answer.Verified),
		slog.String("reason", answer.Reason),
	)

	return ipc.DestroyPINVerifiedResponse{
		Answered: answer.Answered,
		Verified: answer.Verified,
		Reason:   answer.Reason,
	}, nil
}

func reportDestroyAttempts(ctx context.Context, d *daemon) {

	readDestroySaltDropFault()
	if d == nil || d.backendClient == nil {
		return
	}
	snap := d.settingsSnapshot.Load()
	if snap == nil || !snap.ShowFailedDestroyAttempts {
		slog.Debug("destroy pin: not reporting attempts; the operator has not asked to be told")
		return
	}
	out, err := d.backendClient.DrainDestroyAttempts(ctx)
	if err != nil {
		slog.Warn("destroy pin: could not read the attempt notices", slog.Any("err", err))
		return
	}
	if out.Count == 0 {
		slog.Debug("destroy pin: no failed attempts to report")
		return
	}
	slog.Info("destroy pin: failed attempts reported to the operator",
		slog.Int("count", out.Count),
		slog.Int64("last_at", out.LastAt),
	)
	if d.ipcSrv != nil {
		push(d.ipcSrv, ipc.FrameDestroyAttemptsSeen, "", ipc.DestroyAttemptsSeenPush{
			Count:  out.Count,
			LastAt: out.LastAt,
		})
	}
}

func (sd *sessionDispatcher) handleStopDestroyWaiting(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	d := sd.d
	if err := d.backendClient.StopUnlinkWaiting(ctx); err != nil {
		slog.Warn("destroy pin: the wait could not be stopped",
			slog.String("reason", reasonBackendDown),
			slog.Any("err", err),
		)
		sendError(sess, f.ID, reasonBackendDown, err.Error())
		return
	}
	slog.Info("destroy pin: the operator stopped waiting for a destroy receipt")
	resp, err := ipc.NewFrame(ipc.FrameDestroyWaitingStopped, f.ID, struct{}{})
	if err != nil {
		sendError(sess, f.ID, reasonEncodeFrame, err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("destroy pin: could not answer the stop-waiting request", slog.Any("err", err))
	}
}
