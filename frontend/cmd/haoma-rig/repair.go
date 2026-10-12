package main

import (
	"encoding/json"
	"fmt"
	"time"

	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
)

var repairRigNames = []string{"master", "slave", "peer"}

const repairRigCount = 3

const (
	offerTimeout = 4*time.Minute + 30*time.Second

	invitationTimeout = 210 * time.Second

	frameDrainGrace = 3 * time.Second

	markerTimeout = 30 * time.Second

	invitationAcceptTimeout = 240 * time.Second

	invitationCompleteTimeout = 120 * time.Second
)

const firstReachTimeout = 4 * time.Minute

func runRepairFleet(master, slave, peer *rig, restartBeforeAccept bool) *pairingResult {
	res := &pairingResult{a: slave.name, b: peer.name}

	logf("[%s<->%s] pairing — the master needs a contact the slave can inherit", master.name, peer.name)
	masterSeesPeer, peerSeesMaster, err := pairOnion(master, peer)
	res.add("pair master<->peer", err)
	if err != nil {
		logf("[%s<->%s] PAIR FAILED: %v", master.name, peer.name, err)
		return res
	}

	logf("[%s<->%s] linking my two devices …", master.name, slave.name)
	link, err := linkDevices(master, slave)
	res.add("link", err)
	if err != nil {
		logf("[%s<->%s] LINK FAILED: %v", master.name, slave.name, err)
		return res
	}
	res.add("link set master", assertLinkedSet(master, link.offeringSeesJoining))
	res.add("link set slave", assertLinkedSet(slave, link.joiningSeesOffering))
	time.Sleep(linkSettleDelay)

	masterChats, err := listChats(master, syncConvergeTimeout)
	if err != nil {
		res.add("slave inherits the contact", fmt.Errorf("could not read %s's chat list: %w", master.name, err))
		return res
	}
	var masterChatID string
	for _, c := range masterChats {
		if c.PeerID == masterSeesPeer {
			masterChatID = c.ChatID
		}
	}
	if masterChatID == "" {
		res.add("slave inherits the contact", fmt.Errorf("%s has no conversation with %s to inherit", master.name, peer.name))
		return res
	}

	inheritErr := assertBackfilled(slave, peer, masterChatID)
	res.add("slave inherits the contact", inheritErr)
	if inheritErr != nil {
		return res
	}
	fingerprint, err := inheritedFingerprint(slave, masterChatID)
	if err != nil {
		res.add("slave inherits the contact", err)
		return res
	}
	logf("[%s] holds %s as an inherited contact (fp=%s) and cannot reach them", slave.name, peer.name, short(fingerprint))

	peerChats, err := listChats(peer, syncConvergeTimeout)
	if err != nil {
		res.add("peer sees the invitation", fmt.Errorf("could not read %s's chat list: %w", peer.name, err))
		return res
	}
	var peerChatID string
	for _, c := range peerChats {
		if c.PeerID == peerSeesMaster {
			peerChatID = c.ChatID
		}
	}
	if peerChatID == "" {
		res.add("peer sees the invitation", fmt.Errorf("%s has no conversation with %s", peer.name, master.name))
		return res
	}

	peerSub, peerCancel := peer.subscribe()
	defer peerCancel()
	slaveSub, slaveCancel := slave.subscribe()
	defer slaveCancel()
	masterSub, masterCancel := master.subscribe()
	defer masterCancel()

	logf("[%s] offering a pairing invitation to %s", slave.name, peer.name)
	offered, slaveFrames, err := offerInvitation(slave, fingerprint)
	res.add("slave offers the invitation", err)
	if err != nil {
		logf("[%s] OFFER FAILED: %v", slave.name, err)
		return res
	}
	logf("[%s] invitation %s minted, expires %s", slave.name, short(offered.MsgID),
		time.Unix(offered.ExpiresAt, 0).Format(time.TimeOnly))

	row, peerFrames, rowErr := awaitInvitationRow(peerSub, invitationTimeout, offered.MsgID)
	res.add("peer sees the invitation", rowErr)

	if rowErr == nil {
		res.add("same conversation", func() error {
			if string(row.ChatID) != peerChatID {
				return fmt.Errorf("the invitation landed in %s, but %s's conversation with %s is %s — "+
					"one person is one conversation, and a second thread is a failure even when it arrives",
					short(string(row.ChatID)), peer.name, master.name, short(peerChatID))
			}
			if row.Direction != events.DirIn {
				return fmt.Errorf("the invitation row is %q, want inbound", row.Direction)
			}
			return nil
		}())
		res.add("deadline only", assertMarkerCarriesDeadlineOnly(row, offered.ExpiresAt))
	} else {
		res.add("same conversation", fmt.Errorf("not asserted: no row arrived"))
		res.add("deadline only", fmt.Errorf("not asserted: no row arrived"))
	}

	res.add("slave marked its own conversation", assertSlaveMarker(slaveSub, slave, masterChatID, offered.MsgID))

	var peerSeesSlave string
	var acceptFrames []ipc.Frame
	if rowErr == nil {

		res.add("the invitation is reachable",
			awaitSelfProbe(slave, "", rendezvousReachableTimeout))

		restart := repairRestart{}
		if restartBeforeAccept {
			restart = runRepairRestartStop(res, slave, offered.MsgID)
		}

		logf("[%s] accepting the invitation %s offered", peer.name, slave.name)
		accepted, frames, acceptErr := acceptInvitation(peer, offered.MsgID)
		acceptFrames = frames
		res.add("peer accepts the invitation", acceptErr)

		switch {
		case restart.stopped:
			res.add("slave's haoma starts again", slave.startHaoma())
		case restartBeforeAccept:
			res.add("slave's haoma starts again", fmt.Errorf("not asserted: the slave's haoma was never stopped"))
		}
		if acceptErr != nil {
			logf("[%s] ACCEPT FAILED: %v", peer.name, acceptErr)
			if restartBeforeAccept {
				repairRestartNotAsserted(res, "the peer's accept failed")
			}
		} else {
			res.add("accepted into the same conversation", func() error {
				if accepted.ChatID != peerChatID {
					return fmt.Errorf("the completed pairing landed in %s, but %s's conversation with that person is %s — "+
						"a re-pair refreshes the transport of a conversation, it does not start a second one",
						short(accepted.ChatID), peer.name, short(peerChatID))
				}
				if accepted.PeerID == "" {
					return fmt.Errorf("the accept named no pairing")
				}
				return nil
			}())
			peerSeesSlave = accepted.PeerID

			slaveSeesPeer, pairedErr := awaitPeerPaired(slaveSub, invitationCompleteTimeout)
			if pairedErr != nil && restart.stopped {

				slaveSeesPeer, pairedErr = confirmPairedAfterRestart(slave, offered.MsgID, pairedErr)
			}
			res.add("slave completes its half", pairedErr)
			if restartBeforeAccept {
				runRepairRestartVerdicts(res, restart, slave, peer, offered.MsgID,
					slaveSeesPeer, peerSeesMaster, accepted.PeerID, pairedErr)
			}
			if pairedErr == nil {
				logf("[%s] now reaches %s as %s", slave.name, peer.name, short(slaveSeesPeer))

				awaitFirstReach(slave, peer, slaveSeesPeer)
				_, err := deliverText(slave, peer, slaveSeesPeer, "the device that could not reach you")
				res.add("slave -> peer text", err)
				_, err = deliverText(peer, slave, accepted.PeerID, "and back the other way")
				res.add("peer -> slave text", err)
				res.add("both rows read accepted", assertInvitationSettled(
					map[*rig]string{peer: peerSeesMaster, slave: slaveSeesPeer},
					offered.MsgID, offered.ExpiresAt, events.InvitationAccepted))
			} else {
				res.add("slave -> peer text", fmt.Errorf("not asserted: the offering device never completed"))
				res.add("peer -> slave text", fmt.Errorf("not asserted: the offering device never completed"))
				res.add("both rows read accepted", fmt.Errorf("not asserted: the offering device never completed"))
			}
		}
	} else {
		res.add("peer accepts the invitation", fmt.Errorf("not asserted: no row arrived"))
		res.add("accepted into the same conversation", fmt.Errorf("not asserted: no row arrived"))
		res.add("slave completes its half", fmt.Errorf("not asserted: no row arrived"))
		res.add("slave -> peer text", fmt.Errorf("not asserted: no row arrived"))
		res.add("peer -> slave text", fmt.Errorf("not asserted: no row arrived"))
		res.add("both rows read accepted", fmt.Errorf("not asserted: no row arrived"))
		if restartBeforeAccept {
			res.add("slave's haoma stops", fmt.Errorf("not asserted: no row arrived"))
			res.add("slave's haoma starts again", fmt.Errorf("not asserted: no row arrived"))
			repairRestartNotAsserted(res, "no row arrived")
		}
	}

	seen := append(slaveFrames, peerFrames...)
	seen = append(seen, acceptFrames...)
	seen = append(seen, drainFrames(masterSub)...)
	seen = append(seen, drainFrames(peerSub)...)
	seen = append(seen, drainFrames(slaveSub)...)
	res.add("no secret in any frame", assertNoSecretInFrames(seen))
	res.add("master persisted nothing", assertNoInvitationRows(master, masterSeesPeer, offered.MsgID))

	if peerSeesSlave == "" {
		for _, name := range unlinkFanoutChecks {
			res.add(name, fmt.Errorf("not asserted: the peer never paired with %s", slave.name))
		}
	} else {
		runUnlinkFanout(res, slave, master, peer, peerSeesSlave, peerSeesMaster, link.joiningSeesOffering, peerChatID)
	}

	for _, c := range res.checks {
		if !c.ok {
			logf("[%s<->%s] %s FAILED: %s", slave.name, peer.name, c.name, c.err)
		}
	}
	return res
}

func offerInvitation(r *rig, fingerprint string) (ipc.InvitationOfferedResponse, []ipc.Frame, error) {
	return offerInvitationAs(r, ipc.InvitationOfferRequest{Fingerprint: fingerprint})
}

func offerInvitationAs(r *rig, req ipc.InvitationOfferRequest) (ipc.InvitationOfferedResponse, []ipc.Frame, error) {
	var out ipc.InvitationOfferedResponse
	sub, cancel := r.subscribe()
	defer cancel()

	id, err := r.sendReq(ipc.FrameInvitationOffer, req)
	if err != nil {
		return out, nil, fmt.Errorf("invitation_offer send: %w", err)
	}
	var seen []ipc.Frame
	deadline := time.After(offerTimeout)
	for {
		select {
		case f := <-sub:
			seen = append(seen, f)
			if f.ID != id {
				continue
			}
			switch f.Type {
			case ipc.FrameInvitationOffered:
				if err := json.Unmarshal(f.Payload, &out); err != nil {
					return out, seen, fmt.Errorf("decode invitation_offered: %w", err)
				}
				if out.MsgID == "" || out.ExpiresAt == 0 || out.Siblings == 0 {
					return out, seen, fmt.Errorf("the offer replied without naming what it did: %+v", out)
				}
				return out, seen, nil
			case ipc.FrameError:
				return out, seen, frameErr("invitation_offer", f)
			}
		case <-deadline:
			return out, seen, fmt.Errorf("no reply to invitation_offer within %s", offerTimeout)
		}
	}
}

func acceptInvitation(r *rig, msgID string) (ipc.InvitationAcceptedResponse, []ipc.Frame, error) {
	var out ipc.InvitationAcceptedResponse
	sub, cancel := r.subscribe()
	defer cancel()

	id, err := r.sendReq(ipc.FrameInvitationAccept, ipc.InvitationAcceptRequest{MsgID: msgID})
	if err != nil {
		return out, nil, fmt.Errorf("invitation_accept send: %w", err)
	}
	var seen []ipc.Frame
	deadline := time.After(invitationAcceptTimeout)
	for {
		select {
		case f := <-sub:
			seen = append(seen, f)
			if f.ID != id {
				continue
			}
			switch f.Type {
			case ipc.FrameInvitationAccepted:
				if err := json.Unmarshal(f.Payload, &out); err != nil {
					return out, seen, fmt.Errorf("decode invitation_accepted: %w", err)
				}
				return out, seen, nil
			case ipc.FrameError:
				return out, seen, frameErr("invitation_accept", f)
			}
		case <-deadline:
			return out, seen, fmt.Errorf("no reply to invitation_accept within %s", invitationAcceptTimeout)
		}
	}
}

const invitationOfferSource = "invitation-offer"

func awaitPeerPaired(sub <-chan ipc.Frame, timeout time.Duration) (string, error) {
	fromTheOffer := func(f ipc.Frame) (ipc.PeerPairedPush, bool) {
		var p ipc.PeerPairedPush
		if f.Type != ipc.FramePeerPaired {
			return p, false
		}
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			return p, false
		}
		return p, p.PeerID != "" && p.Source == invitationOfferSource
	}
	f, err := awaitFrame(sub, timeout, func(f ipc.Frame) bool {
		_, ok := fromTheOffer(f)
		return ok
	})
	if err != nil {
		return "", fmt.Errorf("the offering device never completed its half: %w", err)
	}
	p, ok := fromTheOffer(f)
	if !ok {
		return "", fmt.Errorf("decode peer_paired")
	}
	return p.PeerID, nil
}

func assertInvitationSettled(rows map[*rig]string, msgID string, wantExpiresAt int64, wantState string) error {
	for r, peerID := range rows {
		held, err := timelineRows(r, peerID, syncConvergeTimeout)
		if err != nil {
			return fmt.Errorf("could not read %s's timeline: %w", r.name, err)
		}
		ev, found := held[msgID]
		if !found {
			return fmt.Errorf("%s holds no row for invitation %s", r.name, short(msgID))
		}
		if ev.Kind != events.KindInvitation {
			return fmt.Errorf("%s's row for %s is a %q", r.name, short(msgID), ev.Kind)
		}
		if err := assertMarkerBody(ev.Body, wantExpiresAt, wantState); err != nil {
			return fmt.Errorf("%s: %w", r.name, err)
		}
	}
	return nil
}

func awaitInvitationRow(sub <-chan ipc.Frame, timeout time.Duration, msgID string) (events.Event, []ipc.Frame, error) {
	var seen []ipc.Frame
	var row events.Event
	found := false
	deadline := time.After(timeout)
	for !found {
		select {
		case f := <-sub:
			seen = append(seen, f)
			ev, ok := timelineEvent(f)
			if ok && ev.MsgID == msgID && ev.Kind == events.KindInvitation {
				row, found = ev, true
			}
		case <-deadline:
			return row, seen, fmt.Errorf("no invitation row for %s within %s — "+
				"the offer was accepted here, so it was lost between my two devices or on the way to the person",
				short(msgID), timeout)
		}
	}
	grace := time.After(frameDrainGrace)
	for {
		select {
		case f := <-sub:
			seen = append(seen, f)
		case <-grace:
			return row, seen, nil
		}
	}
}

func drainFrames(sub <-chan ipc.Frame) []ipc.Frame {
	var seen []ipc.Frame
	for {
		select {
		case f := <-sub:
			seen = append(seen, f)
		default:
			return seen
		}
	}
}

func assertMarkerCarriesDeadlineOnly(row events.Event, wantExpiresAt int64) error {
	return assertMarkerBody(row.Body, wantExpiresAt, events.InvitationLive)
}

func assertMarkerBody(raw json.RawMessage, wantExpiresAt int64, wantState string) error {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return fmt.Errorf("the invitation row's body will not decode: %w", err)
	}
	for key := range keys {
		if key != "expires_at" && key != "state" {
			return fmt.Errorf("the invitation row carries %q — a marker row says an invitation exists, when it lapses and what became of it, and nothing else", key)
		}
	}
	var body events.InvitationBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return fmt.Errorf("decode invitation body: %w", err)
	}
	if body.ExpiresAt != wantExpiresAt {
		return fmt.Errorf("the row lapses at %d, the invitation at %d", body.ExpiresAt, wantExpiresAt)
	}
	if body.State != wantState {
		return fmt.Errorf("the invitation row is in state %q, want %q", body.State, wantState)
	}
	return nil
}

func assertNoSecretInFrames(frames []ipc.Frame) error {
	banned := map[string]bool{"secret": true, "words": true, "phrase": true, "invite_secret": true}
	for _, f := range frames {
		if len(f.Payload) == 0 {
			continue
		}
		var decoded any
		if err := json.Unmarshal(f.Payload, &decoded); err != nil {
			continue
		}
		if key, found := findKey(decoded, banned); found {
			return fmt.Errorf("a %s frame carries a %q field — the secret that addresses a rendezvous reaches no client, ever", f.Type, key)
		}
	}
	return nil
}

func findKey(value any, names map[string]bool) (string, bool) {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if names[key] {
				return key, true
			}
			if found, ok := findKey(child, names); ok {
				return found, true
			}
		}
	case []any:
		for _, child := range v {
			if found, ok := findKey(child, names); ok {
				return found, true
			}
		}
	}
	return "", false
}

func assertNoInvitationRows(r *rig, peerID, msgID string) error {
	rows, err := timelineRows(r, peerID, syncConvergeTimeout)
	if err != nil {
		return fmt.Errorf("could not read %s's timeline: %w", r.name, err)
	}
	if _, held := rows[msgID]; held {
		return fmt.Errorf("%s kept a row for invitation %s — it is carrying a message, not filing one", r.name, short(msgID))
	}
	for id, ev := range rows {
		if ev.Kind == events.KindInvitation {
			return fmt.Errorf("%s holds an invitation row (%s) it never offered", r.name, short(id))
		}
	}
	return nil
}

func assertSlaveMarker(sub <-chan ipc.Frame, r *rig, chatID, msgID string) error {
	f, err := awaitFrame(sub, markerTimeout, func(f ipc.Frame) bool {
		ev, ok := timelineEvent(f)
		return ok && ev.MsgID == msgID && ev.Kind == events.KindInvitation
	})
	if err != nil {
		return fmt.Errorf("%s never marked its own conversation for invitation %s: %w", r.name, short(msgID), err)
	}
	ev, _ := timelineEvent(f)
	if ev.Direction != events.DirOut {
		return fmt.Errorf("%s's invitation row is %q, want outbound — it offered it", r.name, ev.Direction)
	}
	if string(ev.ChatID) != chatID {
		return fmt.Errorf("%s marked %s, but the person it named lives in %s",
			r.name, short(string(ev.ChatID)), short(chatID))
	}
	return nil
}

func inheritedFingerprint(r *rig, chatID string) (string, error) {
	chats, err := listChats(r, syncConvergeTimeout)
	if err != nil {
		return "", fmt.Errorf("could not read %s's chat list: %w", r.name, err)
	}
	for _, c := range chats {
		if c.ChatID == chatID {
			if c.Fingerprint == "" {
				return "", fmt.Errorf("%s's inherited conversation names no person", r.name)
			}
			return c.Fingerprint, nil
		}
	}
	return "", fmt.Errorf("%s does not hold conversation %s", r.name, short(chatID))
}

func awaitFirstReach(from, to *rig, toPeerID string) {
	deadline := time.Now().Add(firstReachTimeout)
	for attempt := 1; time.Now().Before(deadline); attempt++ {
		if _, err := deliverText(from, to, toPeerID, "reachability probe "+nextCorrID()); err == nil {
			if attempt > 1 {
				logf("[%s->%s] the new pairing answered on attempt %d", from.name, to.name, attempt)
			}
			return
		}
		logf("[%s->%s] the new pairing is not answering yet", from.name, to.name)
	}

	logf("[%s->%s] giving up waiting for the new pairing; the assertions below will say so", from.name, to.name)
}
