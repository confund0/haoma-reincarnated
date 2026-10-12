package main

import (
	"encoding/json"
	"fmt"

	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
)

func sendTextNoWait(from *rig, toPeerID, text string) (msgID, envID string, err error) {
	fromSub, cancel := from.subscribe()
	defer cancel()
	if err := from.send(ipc.FrameSendText, ipc.SendTextRequest{PeerID: toPeerID, Text: text}); err != nil {
		return "", "", fmt.Errorf("send: %w", err)
	}
	sf, err := awaitFrame(fromSub, textSentTimeout, func(f ipc.Frame) bool {
		return f.Type == ipc.FrameTextSent || f.Type == ipc.FrameError
	})
	if err != nil {
		return "", "", fmt.Errorf("await text_sent: %w", err)
	}
	if sf.Type == ipc.FrameError {
		return "", "", frameErr("send_text", sf)
	}
	var resp ipc.SendTextResponse
	if err := json.Unmarshal(sf.Payload, &resp); err != nil {
		return "", "", fmt.Errorf("decode text_sent: %w", err)
	}
	if resp.MsgID == "" {
		return "", "", fmt.Errorf("text_sent carried no msg_id")
	}
	return resp.MsgID, resp.EnvelopeID, nil
}

func assertOutboundDeliveryStep(devA, devB, peer *rig, aSeesPeer, bSeesPeer string) error {
	res, err := sendFile(devA, peer, aSeesPeer, "delivery step "+nextCorrID())
	if err != nil {
		return fmt.Errorf("peer send to %s failed: %w", peer.name, err)
	}

	if err := awaitRows(devA, aSeesPeer, fileReadyTimeout, "outbound delivery step (origin)",
		"devA's own row never advanced to delivered — the peer's unconditional file-delivered receipt never landed",
		[]string{res.msgID},
		func(rows map[string]events.Event) bool {
			ev, ok := rows[res.msgID]
			return ok && ev.DeliveryState == events.DeliveryDelivered
		}); err != nil {
		return err
	}

	if rows, rErr := timelineRows(devB, bSeesPeer, syncConvergeTimeout); rErr == nil {
		if ev, held := rows[res.msgID]; held && ev.DeliveryState == events.DeliveryDelivered {
			logf("[%s] already holds msg_id=%s at delivered — the trigger's own sweep got there first; the merge count below still decides", devB.name, short(res.msgID))
		}
	}

	logf("[%s] driving a sync — devA's own advance to delivered has no live push of its own, only the mirror floor does", devB.name)
	if err := driveSync(devB, false); err != nil {
		return fmt.Errorf("could not drive a sync on %s: %w", devB.name, err)
	}

	if err := awaitRows(devB, bSeesPeer, syncConvergeTimeout, "outbound delivery step crossing",
		"the delivered step never crossed — devB is stuck at whatever the live mirror seeded",
		[]string{res.msgID},
		func(rows map[string]events.Event) bool {
			ev, ok := rows[res.msgID]

			return ok && ev.DeliveryState == events.DeliveryDelivered && ev.ReadAt == 0
		}); err != nil {
		return err
	}

	n, cErr := journalTouchCount(devB, events.MutatorApplyDeliverySnapshot, res.msgID)
	if cErr != nil {
		return cErr
	}
	if n == 0 {
		return fmt.Errorf("%s shows msg_id=%s at delivered but never journalled an ApplyDeliverySnapshot merge for it — something else produced this state", devB.name, short(res.msgID))
	}
	logf("[%s] outbound delivery step crossed: msg_id=%s delivered, not read (merge touches=%d)", devB.name, short(res.msgID), n)
	return nil
}

func assertOutboundFailureCrossing(devA, devB, peer *rig, aSeesPeer, bSeesPeer string) error {
	logf("[%s] going down — the next send from %s must genuinely dead-letter", peer.name, devA.name)
	if err := peer.stop(); err != nil {
		return fmt.Errorf("could not take %s down: %w", peer.name, err)
	}
	restored := false
	restorePeer := func() {
		if restored {
			return
		}
		restored = true
		logf("[%s] coming back up", peer.name)
		if err := peer.start(); err != nil {
			logf("[%s] could not bring %s back up: %v", devA.name, peer.name, err)
		}
	}
	defer restorePeer()

	msgID, _, err := sendTextNoWait(devA, aSeesPeer, "doomed to fail "+nextCorrID())
	if err != nil {
		return fmt.Errorf("send from %s to the (now down) peer failed locally: %w", devA.name, err)
	}

	if err := awaitRows(devB, bSeesPeer, mirrorTimeout, "outbound failure crossing (stale copy)",
		"devB never showed the mirrored row at all — nothing to grow stale, and the crossing below would prove nothing",
		[]string{msgID},
		func(rows map[string]events.Event) bool {
			ev, ok := rows[msgID]
			return ok && ev.DeliveryState == events.DeliverySent
		}); err != nil {
		return err
	}
	logf("[%s] holds msg_id=%s at %q — the stale copy the failure below must overwrite", devB.name, short(msgID), events.DeliverySent)

	if err := waitForOutboxGiveUp(devA, 1); err != nil {
		return fmt.Errorf("%s's send never genuinely dead-lettered: %w", devA.name, err)
	}

	logf("[%s] restarting — the backend-ready edge is what stamps the failure onto the row itself", devA.name)
	if err := devA.restart(); err != nil {
		return fmt.Errorf("could not restart %s: %w", devA.name, err)
	}

	if err := awaitRows(devA, aSeesPeer, restartConvergeTimeout, "outbound failure crossing (origin)",
		"devA never stamped its own row as failed after the reconcile sweep",
		[]string{msgID},
		func(rows map[string]events.Event) bool {
			ev, ok := rows[msgID]
			return ok && ev.DeliveryState == events.DeliveryFailed
		}); err != nil {
		return err
	}

	logf("[%s] driving a sync — the failure has no live push of its own, same as any other delivery-axis fact", devB.name)
	if err := driveSync(devB, false); err != nil {
		return fmt.Errorf("could not drive a sync on %s: %w", devB.name, err)
	}

	if err := awaitRows(devB, bSeesPeer, syncConvergeTimeout, "outbound failure crossing",
		"the failure never crossed — devB's stale \"sent\" copy was never overwritten",
		[]string{msgID},
		func(rows map[string]events.Event) bool {
			ev, ok := rows[msgID]
			return ok && ev.DeliveryState == events.DeliveryFailed
		}); err != nil {
		return err
	}
	logf("[%s] outbound failure crossed over its own stale copy: msg_id=%s failed", devB.name, short(msgID))

	restorePeer()
	return nil
}

func assertOutboundRecoveredFloor(devA, devB, peer *rig, aSeesPeer, bSeesPeer string) error {
	logf("[%s] going down — devA's next mirror to it must be unreachable by anything but gap-fill", devB.name)
	if err := devB.stop(); err != nil {
		return fmt.Errorf("could not take %s down: %w", devB.name, err)
	}

	d, err := deliverText(devA, peer, aSeesPeer, "recovered outbound floor "+nextCorrID())
	if err != nil {
		if sErr := devB.start(); sErr != nil {
			logf("[%s] could not bring %s back up after a failed send: %v", devA.name, devB.name, sErr)
		}
		return fmt.Errorf("send from %s to %s failed: %w", devA.name, peer.name, err)
	}

	if err := waitForOutboxGiveUp(devA, 1); err != nil {
		if sErr := devB.start(); sErr != nil {
			logf("[%s] could not bring %s back up: %v", devA.name, devB.name, sErr)
		}
		return fmt.Errorf("could not close off the live mirror before bringing %s back: %w", devB.name, err)
	}

	logf("[%s] coming back up", devB.name)
	if err := devB.start(); err != nil {
		return fmt.Errorf("could not bring %s back: %w", devB.name, err)
	}

	if rows, rErr := timelineRows(devB, bSeesPeer, syncConvergeTimeout); rErr == nil {
		if _, held := rows[d.msgID]; held {
			logf("[%s] already holds msg_id=%s — the sweep its own restart fired got there first", devB.name, short(d.msgID))
		}
	}

	logf("[%s] syncing …", devB.name)
	if err := driveSync(devB, false); err != nil {
		return fmt.Errorf("could not drive a sync on %s: %w", devB.name, err)
	}

	if err := awaitRows(devB, bSeesPeer, restartConvergeTimeout, "outbound row recovered by sweep",
		"the outbound row never crossed by the sweep",
		[]string{d.msgID},
		func(rows map[string]events.Event) bool {
			ev, ok := rows[d.msgID]
			return ok && ev.MirroredFromSibling && ev.DeliveryState == events.DeliverySent
		}); err != nil {
		return err
	}
	logf("[%s] recovered outbound row msg_id=%s at the honest floor (%s), not the bottom of the ladder", devB.name, short(d.msgID), events.DeliverySent)
	return nil
}
