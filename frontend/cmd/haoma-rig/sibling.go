package main

import (
	"encoding/json"
	"fmt"
	"time"

	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
)

var siblingRigNames = []string{"dev-a", "dev-b", "peer"}

const siblingRigCount = 3

const mirrorTimeout = 60 * time.Second

func railText(origin, sibling, peer *rig, peerPeerID, text string) (d delivery, peerErr, mirrorErr, rungErr error) {
	sibSub, cancel := sibling.subscribe()
	defer cancel()

	d, peerErr = deliverText(origin, peer, peerPeerID, text)
	if peerErr != nil {
		notAsserted := fmt.Errorf("not asserted: the peer send did not land")
		return d, peerErr, notAsserted, notAsserted
	}
	f, err := awaitFrame(sibSub, mirrorTimeout, func(f ipc.Frame) bool {
		ev, ok := timelineEvent(f)
		return ok && ev.MsgID == d.msgID && ev.Kind == events.KindText && ev.Direction == events.DirOut
	})
	if err != nil {
		mirrorErr = fmt.Errorf("%s never showed the outbound row for msg_id=%s: %w", sibling.name, short(d.msgID), err)
		return d, nil, mirrorErr, fmt.Errorf("not asserted: the row never crossed")
	}

	ev, _ := timelineEvent(f)
	logf("[%s] mirrored outbound row msg_id=%s delivery=%s", sibling.name, short(d.msgID), ev.DeliveryState)
	return d, nil, nil, assertNotBottomRung(sibling.name, ev.MsgID, ev.DeliveryState)
}

func assertNotBottomRung(deviceName, msgID, state string) error {
	switch state {
	case events.DeliveryPending, events.DeliveryEnqueued, "":
		return fmt.Errorf("%s shows msg_id=%s at %q — it never sent that message and holds no envelope for it, "+
			"so it will read as never having left the outbox for as long as the row exists",
			deviceName, short(msgID), state)
	}
	return nil
}

func railRow(origin, sibling, peer *rig, peerPeerID, text string) (delivery, error) {
	d, peerErr, mirrorErr, _ := railText(origin, sibling, peer, peerPeerID, text)
	if peerErr != nil {
		return d, peerErr
	}

	return d, mirrorErr
}

func railEdit(origin, sibling, peer *rig, peerPeerID, msgID, newText string) (peerErr, mirrorErr error) {
	sibSub, cancel := sibling.subscribe()
	defer cancel()

	if peerErr := assertEdit(origin, peer, peerPeerID, msgID, newText); peerErr != nil {
		return peerErr, fmt.Errorf("not asserted: the peer edit did not land")
	}
	_, err := awaitFrame(sibSub, mirrorTimeout, func(f ipc.Frame) bool {
		ev, ok := timelineEvent(f)
		if !ok || ev.MsgID != msgID || ev.EditedAt <= 0 {
			return false
		}
		var b events.TextBody
		return json.Unmarshal(ev.Body, &b) == nil && b.Text == newText
	})
	if err != nil {
		return nil, fmt.Errorf("%s never reflected the edit of msg_id=%s: %w", sibling.name, short(msgID), err)
	}
	return nil, nil
}

func railDelete(origin, sibling, peer *rig, peerPeerID, msgID string) (peerErr, mirrorErr error) {
	sibSub, cancel := sibling.subscribe()
	defer cancel()

	if peerErr := assertDelete(origin, peer, peerPeerID, msgID); peerErr != nil {
		return peerErr, fmt.Errorf("not asserted: the peer delete did not land")
	}
	_, err := awaitFrame(sibSub, mirrorTimeout, func(f ipc.Frame) bool {
		ev, ok := timelineEvent(f)
		return ok && ev.MsgID == msgID && ev.DeletedAt > 0
	})
	if err != nil {
		return nil, fmt.Errorf("%s never reflected the tombstone of msg_id=%s: %w", sibling.name, short(msgID), err)
	}
	return nil, nil
}

func railReaction(origin, sibling, peer *rig, peerPeerID, msgID, emoji string) (peerErr, mirrorErr error) {
	sibSub, cancel := sibling.subscribe()
	defer cancel()

	if peerErr := assertReaction(origin, peer, peerPeerID, msgID, emoji); peerErr != nil {
		return peerErr, fmt.Errorf("not asserted: the peer reaction did not land")
	}
	_, err := awaitFrame(sibSub, mirrorTimeout, func(f ipc.Frame) bool {
		ev, ok := timelineEvent(f)
		if !ok || ev.Kind != events.KindReaction || ev.Direction != events.DirOut {
			return false
		}
		var b events.ReactionBody
		return json.Unmarshal(ev.Body, &b) == nil && b.TargetMsgID == msgID && b.Emoji == emoji
	})
	if err != nil {
		return nil, fmt.Errorf("%s never showed the reaction breadcrumb for msg_id=%s: %w", sibling.name, short(msgID), err)
	}
	return nil, nil
}

const fanoutSentTimeout = 15 * time.Second

func peerSendAwait(peer *rig, sub <-chan ipc.Frame, req ipc.FrameType, payload any, reply ipc.FrameType, stage string) (ipc.Frame, error) {
	id, err := peer.sendReq(req, payload)
	if err != nil {
		return ipc.Frame{}, fmt.Errorf("%s send: %w", stage, err)
	}
	f, err := awaitFrame(sub, fanoutSentTimeout, func(f ipc.Frame) bool {
		return f.ID == id && (f.Type == reply || f.Type == ipc.FrameError)
	})
	if err != nil {
		return ipc.Frame{}, fmt.Errorf("await %s reply: %w", stage, err)
	}
	if f.Type == ipc.FrameError {
		return ipc.Frame{}, frameErr(stage, f)
	}
	return f, nil
}

func peerRow(peer, devA, devB *rig, peerPeerID, text string) (msgID string, err error) {
	subA, cancelA := devA.subscribe()
	defer cancelA()
	subB, cancelB := devB.subscribe()
	defer cancelB()
	peerSub, peerCancel := peer.subscribe()
	defer peerCancel()

	sf, err := peerSendAwait(peer, peerSub, ipc.FrameSendText,
		ipc.SendTextRequest{PeerID: peerPeerID, Text: text}, ipc.FrameTextSent, "text")
	if err != nil {
		return "", err
	}
	var resp ipc.SendTextResponse
	if err := json.Unmarshal(sf.Payload, &resp); err != nil {
		return "", fmt.Errorf("decode text_sent: %w", err)
	}
	if resp.MsgID == "" {
		return "", fmt.Errorf("text_sent carried no msg_id")
	}

	matches := func(f ipc.Frame) bool {
		ev, ok := timelineEvent(f)
		return ok && ev.Direction == events.DirIn && ev.Kind == events.KindText && ev.MsgID == resp.MsgID
	}
	if _, err := awaitFrame(subA, inboundTimeout, matches); err != nil {
		return "", fmt.Errorf("%s never saw the peer's row (msg_id=%s): %w", devA.name, short(resp.MsgID), err)
	}
	if _, err := awaitFrame(subB, inboundTimeout, matches); err != nil {
		return "", fmt.Errorf("%s never saw the peer's row (msg_id=%s): %w", devB.name, short(resp.MsgID), err)
	}
	return resp.MsgID, nil
}

func peerEditFanout(peer, devA, devB *rig, peerPeerID, msgID, newText string) error {
	subA, cancelA := devA.subscribe()
	defer cancelA()
	subB, cancelB := devB.subscribe()
	defer cancelB()
	peerSub, peerCancel := peer.subscribe()
	defer peerCancel()

	if _, err := peerSendAwait(peer, peerSub, ipc.FrameSendEdit,
		ipc.SendEditRequest{PeerID: peerPeerID, TargetMsgID: msgID, Text: newText},
		ipc.FrameEditSent, "edit"); err != nil {
		return err
	}
	matches := func(f ipc.Frame) bool {
		ev, ok := timelineEvent(f)
		if !ok || ev.MsgID != msgID || ev.EditedAt <= 0 {
			return false
		}
		var b events.TextBody
		return json.Unmarshal(ev.Body, &b) == nil && b.Text == newText
	}
	if _, err := awaitFrame(subA, reflectTimeout, matches); err != nil {
		return fmt.Errorf("%s never reflected the peer's edit of msg_id=%s: %w", devA.name, short(msgID), err)
	}
	if _, err := awaitFrame(subB, reflectTimeout, matches); err != nil {
		return fmt.Errorf("%s never reflected the peer's edit of msg_id=%s: %w", devB.name, short(msgID), err)
	}
	return nil
}

func peerDeleteFanout(peer, devA, devB *rig, peerPeerID, msgID string) error {
	subA, cancelA := devA.subscribe()
	defer cancelA()
	subB, cancelB := devB.subscribe()
	defer cancelB()
	peerSub, peerCancel := peer.subscribe()
	defer peerCancel()

	if _, err := peerSendAwait(peer, peerSub, ipc.FrameSendDelete,
		ipc.SendDeleteRequest{PeerID: peerPeerID, TargetMsgID: msgID},
		ipc.FrameDeleteSent, "delete"); err != nil {
		return err
	}
	matches := func(f ipc.Frame) bool {
		ev, ok := timelineEvent(f)
		return ok && ev.MsgID == msgID && ev.DeletedAt > 0
	}
	if _, err := awaitFrame(subA, reflectTimeout, matches); err != nil {
		return fmt.Errorf("%s never reflected the peer's delete of msg_id=%s: %w", devA.name, short(msgID), err)
	}
	if _, err := awaitFrame(subB, reflectTimeout, matches); err != nil {
		return fmt.Errorf("%s never reflected the peer's delete of msg_id=%s: %w", devB.name, short(msgID), err)
	}
	return nil
}

func peerReactionFanout(peer, devA, devB *rig, peerPeerID, msgID, emoji string) error {
	subA, cancelA := devA.subscribe()
	defer cancelA()
	subB, cancelB := devB.subscribe()
	defer cancelB()
	peerSub, peerCancel := peer.subscribe()
	defer peerCancel()

	if _, err := peerSendAwait(peer, peerSub, ipc.FrameSendReaction,
		ipc.SendReactionRequest{PeerID: peerPeerID, TargetMsgID: msgID, Emoji: emoji},
		ipc.FrameReactionSent, "reaction"); err != nil {
		return err
	}
	matches := func(f ipc.Frame) bool {
		ev, ok := timelineEvent(f)
		if !ok || ev.Kind != events.KindReaction || ev.Direction != events.DirIn {
			return false
		}
		var b events.ReactionBody
		return json.Unmarshal(ev.Body, &b) == nil && b.TargetMsgID == msgID && b.Emoji == emoji
	}
	if _, err := awaitFrame(subA, reflectTimeout, matches); err != nil {
		return fmt.Errorf("%s never got the peer's reaction breadcrumb for msg_id=%s: %w", devA.name, short(msgID), err)
	}
	if _, err := awaitFrame(subB, reflectTimeout, matches); err != nil {
		return fmt.Errorf("%s never got the peer's reaction breadcrumb for msg_id=%s: %w", devB.name, short(msgID), err)
	}
	return nil
}

func assertPeerMutationFanout(devA, devB, peer *rig, peerSeesA, bSeesPeer string) map[string]error {
	out := map[string]error{}
	if err := primePeerSessionToward(peer, devB, bSeesPeer); err != nil {
		return map[string]error{
			"peer edit fanout":     err,
			"peer delete fanout":   err,
			"peer reaction fanout": err,
		}
	}
	mutations := []struct {
		name string
		run  func(msgID string) error
	}{
		{"peer edit fanout", func(msgID string) error {
			return peerEditFanout(peer, devA, devB, peerSeesA, msgID, "peer-edited "+msgID)
		}},
		{"peer delete fanout", func(msgID string) error {
			return peerDeleteFanout(peer, devA, devB, peerSeesA, msgID)
		}},
		{"peer reaction fanout", func(msgID string) error {
			return peerReactionFanout(peer, devA, devB, peerSeesA, msgID, "👍")
		}},
	}
	for _, m := range mutations {
		msgID, err := peerRow(peer, devA, devB, peerSeesA, fmt.Sprintf("peer fanout %s-target %s", m.name, nextCorrID()))
		if err != nil {
			out[m.name] = fmt.Errorf("setup row: %w", err)
			continue
		}
		out[m.name] = m.run(msgID)
	}
	return out
}

type siblingFleet struct {
	devA, devB, peer *rig

	aSeesPeer, peerSeesA string
	bSeesPeer, peerSeesB string
	link                 linkResult

	gapFillSince gapFillMark
}

type siblingNeed struct {
	group string
	why   string
	state string
}

type siblingGroup struct {
	name  string
	needs []siblingNeed
	run   func(f *siblingFleet, res *pairingResult)
}

func needPeerSessionTowardB(what string) siblingNeed {
	return siblingNeed{group: "peer-fanout", state: stateSessionTowardB, why: "the peer must already hold a session toward dev-b, because " + what}
}

var siblingGroups = []siblingGroup{
	{

		name: "text",
		run: func(f *siblingFleet, res *pairingResult) {
			devA, devB, peer, aSeesPeer := f.devA, f.devB, f.peer, f.aSeesPeer
			_, peerErr, mirrorErr, rungErr := railText(devA, devB, peer, aSeesPeer, fmt.Sprintf("rail ping %s->%s", devA.name, peer.name))
			res.add("text peer", peerErr)
			res.add("text mirror", mirrorErr)
			res.add("mirrored row rung", rungErr)

			mutations := []struct {
				name string
				run  func(d delivery) (peerErr, mirrorErr error)
			}{
				{"edit", func(d delivery) (error, error) {
					return railEdit(devA, devB, peer, aSeesPeer, d.msgID, "edited "+d.msgID)
				}},
				{"delete", func(d delivery) (error, error) {
					return railDelete(devA, devB, peer, aSeesPeer, d.msgID)
				}},
				{"reaction", func(d delivery) (error, error) {
					return railReaction(devA, devB, peer, aSeesPeer, d.msgID, "👍")
				}},
			}
			for _, m := range mutations {
				d, setupErr := railRow(devA, devB, peer, aSeesPeer, fmt.Sprintf("rail %s-target %s", m.name, nextCorrID()))
				if setupErr != nil {
					res.add(m.name+" peer", fmt.Errorf("setup row: %w", setupErr))
					res.add(m.name+" mirror", fmt.Errorf("setup row: %w", setupErr))
					continue
				}
				pErr, mErr := m.run(d)
				res.add(m.name+" peer", pErr)
				res.add(m.name+" mirror", mErr)
			}
		},
	},
	{

		name: "peer-fanout",
		run: func(f *siblingFleet, res *pairingResult) {
			logf("[%s<->%s] driving the peer's own edit/delete/reaction fan-out to both my devices …", f.devA.name, f.devB.name)
			for name, err := range assertPeerMutationFanout(f.devA, f.devB, f.peer, f.peerSeesA, f.bSeesPeer) {
				res.add(name, err)
			}
		},
	},
	{

		name: "read",
		run: func(f *siblingFleet, res *pairingResult) {
			logf("[%s<->%s] driving the read axis …", f.devA.name, f.devB.name)
			for name, err := range assertReadDriver(f.devA, f.devB, f.peer, f.peerSeesA, f.bSeesPeer) {
				res.add(name, err)
			}
		},
	},
	{

		name: "receipts",
		run: func(f *siblingFleet, res *pairingResult) {
			logf("[%s<->%s] driving the peer's read-receipt target choice over messages from both my devices …", f.devA.name, f.devB.name)
			for name, err := range assertReceiptTargets(f.devA, f.devB, f.peer, f.aSeesPeer, f.bSeesPeer, f.peerSeesA, f.peerSeesB) {
				res.add(name, err)
			}
		},
	},
	{

		name:  "file-fanout",
		needs: []siblingNeed{needPeerSessionTowardB("a prime of its own grows both journals just before the send, and the sweep that starts can seed dev-b with dev-a's row body")},
		run: func(f *siblingFleet, res *pairingResult) {
			logf("[%s<->%s] driving a file fan-out while both devices are live …", f.devA.name, f.devB.name)
			mCtrl, mFan := assertSiblingFileMirror(f.devA, f.devB, f.peer, f.aSeesPeer, f.peerSeesA, f.bSeesPeer)
			res.add("file fan-out control", mCtrl)
			res.add("file fan-out to my second device", mFan)
		},
	},
	{

		name:  "file-recovery",
		needs: []siblingNeed{needPeerSessionTowardB("a prime of its own, just before dev-b goes down, leaves its traffic in the outboxes while dev-b is away")},
		run: func(f *siblingFleet, res *pairingResult) {
			logf("[%s<->%s] driving a file recovery after an offline gap …", f.devA.name, f.devB.name)
			rCtrl, rSib := assertSiblingFileRecovery(f.devA, f.devB, f.peer, f.aSeesPeer, f.peerSeesA, f.peerSeesB, f.bSeesPeer)
			res.add("file recovery control", rCtrl)
			res.add("file recovery sibling", rSib)
		},
	},
	{

		name: "file-sent-gapfill",
		run: func(f *siblingFleet, res *pairingResult) {
			logf("[%s<->%s] driving a file I send while my other device is away …", f.devA.name, f.devB.name)
			gCtrl, gSib := assertSentFileOnSiblingByGapFill(f.devA, f.devB, f.peer, f.aSeesPeer, f.bSeesPeer)
			res.add("sent file by gap-fill control", gCtrl)
			res.add("sent file by gap-fill bytes on my other device", gSib)
		},
	},
	{

		name: "file-sent",
		run: func(f *siblingFleet, res *pairingResult) {
			logf("[%s<->%s] driving a file I send, on my other device …", f.devA.name, f.devB.name)
			sCtrl, sSib, sKey := assertSentFileOnSibling(f.devA, f.devB, f.peer, f.aSeesPeer, f.bSeesPeer)
			res.add("sent file control", sCtrl)
			res.add("sent file bytes on my other device", sSib)
			res.add("sent file: the contact's key came with the download", sKey)
		},
	},
	{

		name: "presence",
		run: func(f *siblingFleet, res *pairingResult) {
			logf("[%s<->%s] driving the peer's presence fan-out to both my devices …", f.devA.name, f.devB.name)
			for name, err := range assertPresenceFanout(f.devA, f.devB, f.peer, f.aSeesPeer, f.bSeesPeer, f.peerSeesA) {
				res.add(name, err)
			}
		},
	},
	{

		name:  "gapfill",
		needs: []siblingNeed{needPeerSessionTowardB("its hold counts every dead-letter in the peer's log, and a prime of its own could leave a presence echo to dev-b to be counted")},
		run: func(f *siblingFleet, res *pairingResult) {
			logf("[%s<->%s] driving gap-fill …", f.devA.name, f.devB.name)
			for name, err := range assertGapFill(f.devA, f.devB, f.peer, f.peerSeesA, f.bSeesPeer, f.gapFillSince) {
				res.add(name, err)
			}
		},
	},
	{

		name: "outbound",
		run: func(f *siblingFleet, res *pairingResult) {
			devA, devB, peer, aSeesPeer, bSeesPeer := f.devA, f.devB, f.peer, f.aSeesPeer, f.bSeesPeer
			logf("[%s<->%s] driving the outbound delivery step …", devA.name, devB.name)
			res.add("outbound delivery step crossing", assertOutboundDeliveryStep(devA, devB, peer, aSeesPeer, bSeesPeer))

			logf("[%s<->%s] driving an outbound row recovered by sweep …", devA.name, devB.name)
			res.add("outbound row recovered by sweep", assertOutboundRecoveredFloor(devA, devB, peer, aSeesPeer, bSeesPeer))

			logf("[%s<->%s] driving the outbound failure crossing …", devA.name, devB.name)
			res.add("outbound failure crossing", assertOutboundFailureCrossing(devA, devB, peer, aSeesPeer, bSeesPeer))
		},
	},
	{

		name: "file-contact-away",
		run: func(f *siblingFleet, res *pairingResult) {
			logf("[%s<->%s] driving a file I send while the contact is away, on my other device …", f.devA.name, f.devB.name)
			oCtrl, oSib := assertSentFileOnSiblingContactOffline(f.devA, f.devB, f.peer, f.aSeesPeer, f.bSeesPeer)
			res.add("sent file with the contact away control", oCtrl)
			res.add("sent file with the contact away bytes on my other device", oSib)
		},
	},
	{

		name:  "suppress",
		needs: []siblingNeed{needPeerSessionTowardB("in the full run it follows a restart of the peer, and a prime of its own would dial the peer inside deliverText's timeout instead of the wait allowed for a republishing descriptor")},
		run: func(f *siblingFleet, res *pairingResult) {
			logf("[%s<->%s] driving suppression and withdrawal …", f.devA.name, f.devB.name)
			for name, err := range assertSuppressAndWithdraw(f.devA, f.devB, f.peer, f.aSeesPeer, f.bSeesPeer, f.peerSeesA) {
				res.add(name, err)
			}
		},
	},
	{

		name:  "trigger",
		needs: []siblingNeed{needPeerSessionTowardB("in the full run it follows suppress's restart of the peer, and a prime of its own would dial the peer while its descriptor may still be republishing")},
		run: func(f *siblingFleet, res *pairingResult) {
			logf("[%s<->%s] driving the local trigger — no sync verb and no restart from here on …", f.devA.name, f.devB.name)
			for name, err := range assertLocalTrigger(f.devA, f.devB, f.peer, f.aSeesPeer, f.bSeesPeer, f.peerSeesA) {
				res.add(name, err)
			}
		},
	},
	{

		name: "chat-settings",
		run: func(f *siblingFleet, res *pairingResult) {
			devA, devB := f.devA, f.devB
			logf("[%s<->%s] driving per-chat settings convergence …", devA.name, devB.name)
			if aChatID, cErr := chatIDForPeer(devA, f.aSeesPeer, syncConvergeTimeout); cErr != nil {
				res.add("chat settings cross to a sibling", fmt.Errorf("could not resolve %s's chat id: %w", devA.name, cErr))
				res.add("a settings change converges with no sync verb", fmt.Errorf("not reached: %w", cErr))
			} else if bChatID, cErr := chatIDForPeer(devB, f.bSeesPeer, syncConvergeTimeout); cErr != nil {
				res.add("chat settings cross to a sibling", fmt.Errorf("could not resolve %s's chat id: %w", devB.name, cErr))
				res.add("a settings change converges with no sync verb", fmt.Errorf("not reached: %w", cErr))
			} else {
				for name, err := range assertChatSettingsConverge(devA, devB, aChatID, bChatID) {
					res.add(name, err)
				}
			}
		},
	},
	{

		name: "alias",
		run: func(f *siblingFleet, res *pairingResult) {
			logf("[%s<->%s] driving alias convergence …", f.devA.name, f.devB.name)
			for name, err := range assertAliasConverges(f.devA, f.devB, f.aSeesPeer, f.bSeesPeer) {
				res.add(name, err)
			}
		},
	},
	{

		name: "self-nick",
		run: func(f *siblingFleet, res *pairingResult) {
			logf("[%s<->%s] driving self-nick convergence …", f.devA.name, f.devB.name)
			for name, err := range assertSelfNickConverges(f.devA, f.devB) {
				res.add(name, err)
			}
		},
	},
	{

		name: "file-gapfill-expiry",
		run: func(f *siblingFleet, res *pairingResult) {
			logf("[%s<->%s] driving a gap-fill download that crosses the blob's hour …", f.devA.name, f.devB.name)
			for _, c := range assertFileAcrossTheHour(f.devA, f.devB, f.peer, f.aSeesPeer, f.bSeesPeer) {
				res.add(c.name, c.err)
			}
		},
	},
	{

		name: "fold-delivery",
		run: func(f *siblingFleet, res *pairingResult) {
			logf("[%s] driving a text to both my devices while the first of them is down …", f.peer.name)
			res.add("fold delivery: sent under the row's first envelope",
				assertFoldDelivery(f.devA, f.devB, f.peer, f.aSeesPeer, f.bSeesPeer, f.peerSeesA, f.peerSeesB))
		},
	},
	{

		name: "clock",
		run: func(f *siblingFleet, res *pairingResult) {
			logf("[%s] driving the clock reading and a seeded drift …", f.devB.name)
			for name, err := range assertClockWarning(f.devB) {
				res.add(name, err)
			}
		},
	},
	{

		name: "netchange",
		run: func(f *siblingFleet, res *pairingResult) {
			logf("[%s<->%s] driving a network change on %s …", f.devA.name, f.peer.name, f.devA.name)
			for _, c := range assertNetChange(f.devA, f.peer, f.aSeesPeer, f.peerSeesA) {
				res.add(c.name, c.err)
			}
		},
	},
	{

		name: "peer-circuit",
		run: func(f *siblingFleet, res *pairingResult) {
			logf("[%s<->%s] driving a stalled circuit and the automatic new circuit …", f.devA.name, f.peer.name)
			res.add("automatic new circuit after a stall", assertPeerCircuit(f.devA, f.peer, f.aSeesPeer, f.peerSeesA))
		},
	},
	{

		name: "destroy-pin",
		run: func(f *siblingFleet, res *pairingResult) {
			runDestroyPINGate(res, f.devA, f.devB, f.link.offeringSeesJoining, f.link.joiningSeesOffering, f.bSeesPeer)
		},
	},
	{

		name: "contact-delete",
		run: func(f *siblingFleet, res *pairingResult) {
			logf("[%s<->%s] driving a contact delete, its sweep and a re-pair …", f.devA.name, f.devB.name)
			for _, c := range assertContactDelete(f.devA, f.devB, f.peer, f.aSeesPeer, f.bSeesPeer) {
				res.add(c.name, c.err)
			}
		},
	},
}

func runSiblingFleet(devA, devB, peer *rig, plan siblingPlan) *pairingResult {
	res := &pairingResult{a: devA.name, b: devB.name}
	if err := checkSiblingPlan(plan); err != nil {
		logf("[%s<->%s] REFUSING TO RUN: %v", devA.name, devB.name, err)
		res.add("sibling plan", err)
		return res
	}

	var ran []string
	defer func() { res.scope = plan.ranLine(ran) }()

	logf("[%s<->%s] pairing before any linking — %s has to have something worth inheriting", devA.name, peer.name, devA.name)
	var aSeesPeer, peerSeesA string
	err := timePhase("pair over an onion", func() (pErr error) {
		aSeesPeer, peerSeesA, pErr = pairOnion(devA, peer)
		return pErr
	})
	res.add("pair a<->peer", err)
	if err != nil {
		logf("[%s<->%s] PAIR FAILED: %v", devA.name, peer.name, err)
		return res
	}

	logf("[%s<->%s] linking my two devices …", devA.name, devB.name)
	var link linkResult
	err = timePhase("link two devices", func() (lErr error) {
		link, lErr = linkDevices(devA, devB)
		return lErr
	})
	res.add("link", err)
	if err != nil {
		logf("[%s<->%s] LINK FAILED: %v", devA.name, devB.name, err)
		return res
	}
	logf("[%s<->%s] linked (%s reaches %s on %s; %s reaches %s on %s)",
		devA.name, devB.name,
		devA.name, devB.name, short(link.offeringSeesJoining),
		devB.name, devA.name, short(link.joiningSeesOffering))

	res.add("link set a", assertLinkedSet(devA, link.offeringSeesJoining))
	res.add("link set b", assertLinkedSet(devB, link.joiningSeesOffering))

	rigSleep(linkSettleDelay, "settle after linking")

	aChats, aErr := listChats(devA, syncConvergeTimeout)
	if aErr != nil {
		res.add("backfill inherits contact", fmt.Errorf("could not read %s's chat list: %w", devA.name, aErr))
	} else {
		var wantChatID string
		for _, c := range aChats {
			if c.PeerID == aSeesPeer {
				wantChatID = c.ChatID
			}
		}
		res.add("backfill inherits contact", assertBackfilled(devB, peer, wantChatID))
	}

	logf("[%s<->%s] pairing …", devB.name, peer.name)
	var bSeesPeer, peerSeesB string
	err = timePhase("pair over an onion", func() (pErr error) {
		bSeesPeer, peerSeesB, pErr = pairOnion(devB, peer)
		return pErr
	})
	res.add("pair b<->peer", err)
	if err != nil {
		logf("[%s<->%s] PAIR FAILED: %v", devB.name, peer.name, err)
		return res
	}

	var twoIDsErr error
	if peerSeesA == peerSeesB {
		twoIDsErr = fmt.Errorf("%s minted one peerID for both of my devices (%s) — the second pairing did not mint its own", peer.name, short(peerSeesA))
	}
	res.add("peer holds 2 ids", twoIDsErr)

	rigSleep(settleDelay, "settle presence before traffic")

	fleet := &siblingFleet{
		devA: devA, devB: devB, peer: peer,
		aSeesPeer: aSeesPeer, peerSeesA: peerSeesA,
		bSeesPeer: bSeesPeer, peerSeesB: peerSeesB,
		link: link,
	}

	if plan.warmUp {
		err = timePhase("warm-up", func() error { return siblingWarmUp(fleet) })
		res.add("warm-up", err)
		if err != nil {
			logf("[%s<->%s] WARM-UP FAILED: %v", devA.name, devB.name, err)
			return res
		}
	}

	for _, g := range plan.run {
		g.run(fleet, res)
		ran = append(ran, g.name)
	}

	for _, c := range res.checks {
		if !c.ok {
			logf("[%s<->%s] %s FAILED: %s", devA.name, devB.name, c.name, c.err)
		}
	}
	return res
}
