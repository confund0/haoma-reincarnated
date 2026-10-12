package main

import (
	"context"
	"log/slog"

	"haoma-frontend/internal/ipc"
)

func (sd *sessionDispatcher) handleWipeDevice(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	d := sd.d
	if d.backendClient == nil {
		sendError(sess, f.ID, reasonNotReady, "frontend wiring incomplete")
		return
	}

	slog.Info("wipe: the operator asked for this device to be erased")
	out, err := d.backendClient.WipeDevice(ctx)
	if err != nil {

		slog.Warn("wipe: the erasure was not started",
			slog.String("reason", reasonBackendDown),
			slog.Any("err", err),
		)
		sendError(sess, f.ID, reasonBackendDown, err.Error())
		return
	}

	resp, nErr := ipc.NewFrame(ipc.FrameDeviceWiping, f.ID, ipc.DeviceWipingResponse{Started: out.Started})
	if nErr != nil {
		sendError(sess, f.ID, reasonEncodeFrame, nErr.Error())
		return
	}
	if sErr := sess.Send(resp); sErr != nil {

		slog.Warn("wipe: could not answer the request", slog.Any("err", sErr))
	}
}
