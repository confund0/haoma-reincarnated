package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/destroypin"
	"haoma-frontend/internal/ipc"
)

func (sd *sessionDispatcher) handleUnlinkDevice(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("unlink: handle request")
	d := sd.d
	var req ipc.UnlinkDeviceRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, reasonBadRequest, "decode payload")
		return
	}
	if d.backendClient == nil || d.linkedDevices == nil {
		sendError(sess, f.ID, reasonNotReady, "frontend wiring incomplete")
		return
	}

	out, err := unlinkDevice(ctx, d, req)
	if err != nil {
		slog.Warn("unlink: failed", slog.String("reason", reasonBackendDown), slog.Any("err", err))
		sendError(sess, f.ID, reasonBackendDown, err.Error())
		return
	}
	resp, nErr := ipc.NewFrame(ipc.FrameDeviceUnlinked, f.ID, out)
	if nErr != nil {
		sendError(sess, f.ID, reasonEncodeFrame, nErr.Error())
		return
	}
	if sErr := sess.Send(resp); sErr != nil {
		slog.Warn("unlink: send frame failed", slog.Any("err", sErr))
	}
}

func unlinkDevice(ctx context.Context, d *daemon, req ipc.UnlinkDeviceRequest) (ipc.DeviceUnlinkedResponse, error) {
	mine := false
	for _, peerID := range d.siblings() {
		if peerID == req.PeerID {
			mine = true
			break
		}
	}
	if !mine {
		slog.Info("unlink: refused", slog.String("reason", ipc.DestroyPINVerifyUnknownSibling))
		return ipc.DeviceUnlinkedResponse{Reason: ipc.DestroyPINVerifyUnknownSibling}, nil
	}

	proofHex := ""
	if req.PIN != "" {
		var reason string
		var err error
		proofHex, reason, err = destroyProofFor(d, req.PeerID, req.PIN)
		if err != nil {
			return ipc.DeviceUnlinkedResponse{}, err
		}
		if reason != "" {

			slog.Info("unlink: refused", slog.String("reason", reason))
			return ipc.DeviceUnlinkedResponse{Reason: reason}, nil
		}
	}

	far, err := d.backendClient.Unlink(ctx, backendapi.UnlinkRequest{
		PeerID:     req.PeerID,
		NarrowOnly: proofHex != "",
	})
	if err != nil {
		if proofHex != "" {
			slog.Warn("unlink: the rail could not be narrowed, so the destroy was refused; nothing was forgotten",
				slog.String("peer_id", req.PeerID),
				slog.Any("err", err),
			)
			return ipc.DeviceUnlinkedResponse{Reason: ipc.ReasonDestroyNotNarrowed}, nil
		}
		slog.Warn("unlink: the rail could not be narrowed; forgetting the device anyway",
			slog.String("peer_id", req.PeerID),
			slog.Any("err", err),
		)
		far = backendapi.UnlinkResponse{}
	}

	if proofHex != "" {
		checked, vErr := d.backendClient.VerifyDestroyPIN(ctx, backendapi.VerifyDestroyPINRequest{
			PeerID: req.PeerID,
			Proof:  proofHex,
		})
		switch {
		case vErr != nil:

			slog.Info("unlink: the PIN could not be checked; commanding anyway so the answer is armed",
				slog.String("peer_id", req.PeerID),
				slog.Any("err", vErr),
			)
		case checked.Answered && !checked.Verified:

			slog.Info("unlink: that device did not accept the PIN; nothing was forgotten",
				slog.String("peer_id", req.PeerID),
			)
			return ipc.DeviceUnlinkedResponse{Reason: ipc.DestroyPINRefusedWrong}, nil
		case !checked.Answered:

			slog.Info("unlink: that device did not answer the check; commanding anyway",
				slog.String("peer_id", req.PeerID),
			)
		}
	}

	if proofHex != "" {
		sent, sErr := d.backendClient.Unlink(ctx, backendapi.UnlinkRequest{
			PeerID: req.PeerID,
			Proof:  proofHex,
		})
		if sErr != nil {
			slog.Warn("unlink: the destroy command did not go out; forgetting the device anyway",
				slog.String("peer_id", req.PeerID),
				slog.Any("err", sErr),
			)
		} else {
			far = sent
		}
	}

	unlinkedAt := forgetSibling(ctx, d, req.PeerID)

	pushInheritedContactsChanged(d, nil, "device_unlinked")

	pushUnlinkNotice(ctx, d, req.PeerID, unlinkedAt)

	slog.Info("unlink: this device has forgotten the other one",
		slog.String("peer_id", req.PeerID),
		slog.Bool("command_sent", far.Sent),
		slog.Bool("receipted", far.Receipted),
	)
	return ipc.DeviceUnlinkedResponse{
		Unlinked:  true,
		Sent:      far.Sent,
		Receipted: far.Receipted,
		Deadline:  far.Deadline,
	}, nil
}

func destroyProofFor(d *daemon, peerID, pin string) (proofHex, reason string, err error) {
	if vErr := destroypin.ValidatePIN(pin); vErr != nil {
		return "", ipc.DestroyPINRefusedShape, nil
	}
	rec, held, sErr := d.linkedDevices.SiblingSalt(peerID)
	if sErr != nil {
		return "", "", fmt.Errorf("read the other device's salt: %w", sErr)
	}
	if !held || len(rec.Salt) == 0 {
		return "", ipc.DestroyPINVerifyNoSalt, nil
	}
	proof, dErr := destroypin.Derive(pin, rec.Salt)
	if dErr != nil {
		return "", "", fmt.Errorf("derive the proof: %w", dErr)
	}
	out := hex.EncodeToString(proof)
	destroypin.Zero(proof)
	return out, "", nil
}

const clearGuardTimeout = 30 * time.Second

func forgetSibling(ctx context.Context, d *daemon, peerID string) int64 {
	unlinkedAt := time.Now().Unix()
	if _, err := d.linkedDevices.Unlink(peerID, unlinkedAt); err != nil {
		slog.Warn("unlink: could not drop the linked-device entry",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
	}
	if err := d.linkedDevices.ForgetSiblingSalt(peerID); err != nil {
		slog.Warn("unlink: could not drop the other device's salt",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
	}

	forgetSaltPushes(d, peerID)

	clearCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), clearGuardTimeout)
	defer cancel()
	if err := d.backendClient.ClearDestroyPIN(clearCtx); err != nil {
		slog.Warn("unlink: could not clear this device's guard", slog.Any("err", err))
	}
	return unlinkedAt
}

func stopForDestroy(d *daemon, reason, eraseID string) {
	slog.Info("destroy: this device is being erased; stopping",
		slog.String("reason", reason),
		slog.String("erase_id", eraseID),
	)
	if d.ipcSrv != nil {
		push(d.ipcSrv, ipc.FrameDestroyDevice, "",
			ipc.DestroyDevicePush{Reason: reason, EraseID: eraseID})
	}

	streamers := beginStreamerShutdown(d)

	time.Sleep(destroyClientGrace)
	awaitStreamerShutdown(streamers)
	exitForDestroy(0)
}

const destroyClientGrace = 1200 * time.Millisecond

const destroyStreamerGrace = 3 * time.Second

var destroyStreamerGraceForTest = destroyStreamerGrace

var shutdownStreamers = func(d *daemon) {
	if d.streamers == nil {
		return
	}
	d.streamers.Shutdown()
}

func beginStreamerShutdown(d *daemon) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		shutdownStreamers(d)
	}()
	return done
}

func awaitStreamerShutdown(done <-chan struct{}) {
	select {
	case <-done:
		slog.Info("destroy: the native streamers are stopped")
	case <-time.After(destroyStreamerGraceForTest):
		slog.Warn("destroy: the native streamers did not stop in time; ending this process anyway",
			slog.Duration("waited", destroyStreamerGraceForTest),
		)
	}
}

var exitForDestroy = func(code int) { os.Exit(code) }
