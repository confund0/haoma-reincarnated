package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
)

var repairDevicesRigNames = []string{"master", "slave", "peer", "peerlink"}

const repairDevicesRigCount = 4

const repairDevicesConvergeTimeout = 6 * time.Minute

var (
	repairDevicesChecks = []string{
		"pair master<->peer", "link peer->peerlink", "peerlink inherits the contact",
		"peerlink pairs with master", "link master->slave", "slave inherits the contact",
		"slave sees two devices to pair with", "first invitation", "second invitation",
		"the two invitations reached two devices", "slave holds two pairings",
		"slave's row is gone", "slave -> both devices text",
	}
	repairDevicesOldChecks = []string{
		"pair master<->peer", "link master->slave", "slave inherits the contact",
		"link peer->peerlink", "peerlink inherits the contact", "pair slave<->peerlink",
		"every device sees one device missing", "only the lower identity offers",
		"first invitation", "second invitation", "every device holds two pairings",
		"no row is left",
	}
)

func notAsserted(res *pairingResult, names []string, why string) {
	done := map[string]bool{}
	for _, c := range res.checks {
		done[c.name] = true
	}
	for _, n := range names {
		if !done[n] {
			res.add(n, fmt.Errorf("not asserted: %s", why))
		}
	}
}

func runRepairDevicesFleet(master, slave, peer, peerlink *rig, acceptBetween bool) *pairingResult {
	res := &pairingResult{a: slave.name, b: peer.name}
	stop := func(why string) *pairingResult {
		notAsserted(res, repairDevicesChecks, why)
		return res
	}

	masterSeesPeer, peerSeesMaster, err := pairOnion(master, peer)
	res.add("pair master<->peer", err)
	if err != nil {
		return stop("the first pairing failed")
	}
	if _, err := linkDevices(peer, peerlink); err != nil {
		res.add("link peer->peerlink", err)
		return stop("the contact's link failed")
	}
	res.add("link peer->peerlink", nil)
	time.Sleep(linkSettleDelay)

	peerChatID, err := chatWith(peer, peerSeesMaster)
	if err == nil {
		err = assertBackfilled(peerlink, master, peerChatID)
	}
	var masterFP string
	if err == nil {
		masterFP, err = inheritedFingerprint(peerlink, peerChatID)
	}
	res.add("peerlink inherits the contact", err)
	if err != nil {
		return stop("the contact's second device inherited nothing")
	}

	if _, err := awaitInheritedRow(peerlink, masterFP, func(e ipc.InheritedContactEntry) bool { return e.Offers == 1 && !e.Waiting }); err != nil {
		res.add("peerlink pairs with master", err)
		return stop("the contact's second device had nothing to offer")
	}
	got, _, err := offerToOneDevice(peerlink, masterFP, []*rig{master})
	if err == nil && got != master {
		err = fmt.Errorf("the invitation reached %s", got.name)
	}
	res.add("peerlink pairs with master", err)
	if err != nil {
		return stop("the contact's second device did not pair")
	}

	if _, err := linkDevices(master, slave); err != nil {
		res.add("link master->slave", err)
		return stop("my link failed")
	}
	res.add("link master->slave", nil)
	time.Sleep(linkSettleDelay)
	masterChatID, err := chatWith(master, masterSeesPeer)
	if err == nil {
		err = assertBackfilled(slave, peer, masterChatID)
	}
	var peerFP string
	if err == nil {
		peerFP, err = inheritedFingerprint(slave, masterChatID)
	}
	res.add("slave inherits the contact", err)
	if err != nil {
		return stop("my new device inherited nothing")
	}
	logf("[%s] my identity is %s than the contact's", slave.name, map[bool]string{true: "lower", false: "higher"}[masterFP < peerFP])

	_, err = awaitInheritedRow(slave, peerFP, func(e ipc.InheritedContactEntry) bool {
		return e.Devices == 2 && e.Paired == 0 && e.Offers == 2 && !e.Waiting
	})
	res.add("slave sees two devices to pair with", err)
	if err != nil {
		return stop("my new device does not see the contact's two devices")
	}

	theirs := []*rig{peer, peerlink}
	reached := map[*rig]string{}
	land := func(name string) (*rig, string, error) {
		to, msgID, err := offerAndLand(slave, ipc.InvitationOfferRequest{Fingerprint: peerFP, Force: true}, theirs)
		if err == nil {
			if _, twice := reached[to]; twice {
				err = fmt.Errorf("it reached %s, which the other invitation reached too", to.name)
			} else {
				reached[to] = ""
				logf("[%s] %s reached %s", slave.name, name, to.name)
			}
		}
		return to, msgID, err
	}
	offSub, offCancel := slave.subscribe()
	defer offCancel()
	accept := func(to *rig, msgID string) error {
		sees, err := acceptAndComplete(offSub, to, msgID)
		if err == nil {
			reached[to] = sees
		}
		return err
	}

	if acceptBetween {
		for _, name := range []string{"first invitation", "second invitation"} {
			to, msgID, err := land(name)
			if err == nil {
				err = accept(to, msgID)
			}
			res.add(name, err)
			if err != nil {
				return stop("an invitation did not complete")
			}
		}
	} else {

		type out struct {
			to    *rig
			msgID string
		}
		var outs []out
		for _, name := range []string{"first invitation", "second invitation"} {
			to, msgID, err := land(name)
			if err != nil {
				res.add(name, err)
				return stop("an invitation did not land")
			}
			outs = append(outs, out{to, msgID})
		}
		for i, name := range []string{"first invitation", "second invitation"} {
			err := accept(outs[i].to, outs[i].msgID)
			res.add(name, err)
			if err != nil {
				return stop("an invitation did not complete")
			}
		}
	}
	res.add("the two invitations reached two devices", func() error {
		if reached[peer] == "" || reached[peerlink] == "" {
			return fmt.Errorf("reached %d device(s), want both", len(reached))
		}
		return nil
	}())
	res.add("slave holds two pairings", awaitPairingCount(slave, peerFP, 2))
	res.add("slave's row is gone", awaitInheritedGone(slave, peerFP))

	var textErr error
	for _, to := range theirs {
		awaitFirstReach(slave, to, reached[to])
		if _, err := deliverText(slave, to, reached[to], "from the device that now reaches all of yours"); err != nil && textErr == nil {
			textErr = fmt.Errorf("to %s: %w", to.name, err)
		}
	}
	res.add("slave -> both devices text", textErr)
	return res
}

func runRepairDevicesOldFleet(master, slave, peer, peerlink *rig) *pairingResult {
	res := &pairingResult{a: master.name, b: peerlink.name}
	stop := func(why string) *pairingResult {
		notAsserted(res, repairDevicesOldChecks, why)
		return res
	}

	masterSeesPeer, peerSeesMaster, err := pairOnion(master, peer)
	res.add("pair master<->peer", err)
	if err != nil {
		return stop("the first pairing failed")
	}
	if _, err := linkDevices(master, slave); err != nil {
		res.add("link master->slave", err)
		return stop("my link failed")
	}
	res.add("link master->slave", nil)
	time.Sleep(linkSettleDelay)
	masterChatID, err := chatWith(master, masterSeesPeer)
	if err == nil {
		err = assertBackfilled(slave, peer, masterChatID)
	}
	var peerFP string
	if err == nil {
		peerFP, err = inheritedFingerprint(slave, masterChatID)
	}
	res.add("slave inherits the contact", err)
	if err != nil {
		return stop("my new device inherited nothing")
	}

	if _, err := linkDevices(peer, peerlink); err != nil {
		res.add("link peer->peerlink", err)
		return stop("the contact's link failed")
	}
	res.add("link peer->peerlink", nil)
	time.Sleep(linkSettleDelay)
	peerChatID, err := chatWith(peer, peerSeesMaster)
	if err == nil {
		err = assertBackfilled(peerlink, master, peerChatID)
	}
	var masterFP string
	if err == nil {
		masterFP, err = inheritedFingerprint(peerlink, peerChatID)
	}
	res.add("peerlink inherits the contact", err)
	if err != nil {
		return stop("the contact's second device inherited nothing")
	}

	_, _, err = pairOnion(slave, peerlink)
	res.add("pair slave<->peerlink", err)
	if err != nil {
		return stop("the direct pairing failed")
	}

	type side struct {
		r      *rig
		fp     string
		target *rig
		others []*rig
	}
	sides := []side{
		{master, peerFP, peerlink, []*rig{peer, peerlink}},
		{slave, peerFP, peer, []*rig{peer, peerlink}},
		{peer, masterFP, slave, []*rig{master, slave}},
		{peerlink, masterFP, master, []*rig{master, slave}},
	}
	rows := map[*rig]ipc.InheritedContactEntry{}
	var seenErr error
	for _, s := range sides {
		e, err := awaitInheritedRow(s.r, s.fp, func(e ipc.InheritedContactEntry) bool {
			return e.Devices == 2 && e.Paired == 1 && e.Offers == 1 && !e.Invited
		})
		if err != nil && seenErr == nil {
			seenErr = err
		}
		rows[s.r] = e
	}
	res.add("every device sees one device missing", seenErr)
	if seenErr != nil {
		return stop("a device does not see the device it is missing")
	}

	mineLower := masterFP < peerFP
	var offering []side
	res.add("only the lower identity offers", func() error {
		for _, s := range sides {
			myDevice := s.r == master || s.r == slave
			wantOffer := myDevice == mineLower
			if wantOffer {
				offering = append(offering, s)
			}
			if got := !rows[s.r].Waiting; got != wantOffer {
				return fmt.Errorf("%s waiting=%v, want offering=%v (my identity lower: %v)",
					s.r.name, rows[s.r].Waiting, wantOffer, mineLower)
			}
		}
		return nil
	}())
	if len(offering) != 2 {
		return stop("the offering side is not two devices")
	}
	sort.Slice(offering, func(i, j int) bool { return offering[i].r.name < offering[j].r.name })

	for i, name := range []string{"first invitation", "second invitation"} {
		s := offering[i]
		got, _, err := offerToOneDevice(s.r, s.fp, s.others)
		if err == nil && got != s.target {
			err = fmt.Errorf("%s's invitation reached %s, want %s — the route index did not pick the device it was for",
				s.r.name, got.name, s.target.name)
		}
		res.add(name, err)
		if err != nil {
			return stop("an invitation did not complete")
		}
	}

	var countErr, rowErr error
	for _, s := range sides {
		if err := awaitPairingCount(s.r, s.fp, 2); err != nil && countErr == nil {
			countErr = err
		}
		if err := awaitInheritedGone(s.r, s.fp); err != nil && rowErr == nil {
			rowErr = err
		}
	}
	res.add("every device holds two pairings", countErr)
	res.add("no row is left", rowErr)
	return res
}

func chatWith(r *rig, peerID string) (string, error) {
	chats, err := listChats(r, syncConvergeTimeout)
	if err != nil {
		return "", fmt.Errorf("could not read %s's chat list: %w", r.name, err)
	}
	for _, c := range chats {
		if c.PeerID == peerID {
			return c.ChatID, nil
		}
	}
	return "", fmt.Errorf("%s has no conversation over %s", r.name, short(peerID))
}

func awaitInheritedRow(r *rig, fp string, want func(ipc.InheritedContactEntry) bool) (ipc.InheritedContactEntry, error) {
	deadline := time.Now().Add(repairDevicesConvergeTimeout)
	var last ipc.InheritedContactEntry
	var lastErr error
	held := false
	for time.Now().Before(deadline) {
		rows, err := listInheritedContacts(r)
		lastErr = err
		held = false
		for _, e := range rows {
			if e.Fingerprint != fp {
				continue
			}
			held, last = true, e
			if want(e) {
				return e, nil
			}
		}
		rigSleep(syncPollInterval, "poll: inherited contacts")
	}
	if !held {
		return last, fmt.Errorf("%s never listed the person (fp=%s) within %s (last read error: %v)", r.name, short(fp), repairDevicesConvergeTimeout, lastErr)
	}
	return last, fmt.Errorf("%s's row for the person stayed at devices=%d paired=%d offers=%d waiting=%v",
		r.name, last.Devices, last.Paired, last.Offers, last.Waiting)
}

func awaitInheritedGone(r *rig, fp string) error {
	deadline := time.Now().Add(repairDevicesConvergeTimeout)
	var last ipc.InheritedContactEntry
	for time.Now().Before(deadline) {
		rows, err := listInheritedContacts(r)
		if err == nil {
			found := false
			for _, e := range rows {
				if e.Fingerprint == fp {
					found, last = true, e
				}
			}
			if !found {
				return nil
			}
		}
		rigSleep(syncPollInterval, "poll: inherited contacts")
	}
	return fmt.Errorf("%s still lists the person: devices=%d paired=%d offers=%d waiting=%v",
		r.name, last.Devices, last.Paired, last.Offers, last.Waiting)
}

func awaitPairingCount(r *rig, fp string, want int) error {
	deadline := time.Now().Add(repairDevicesConvergeTimeout)
	got := 0
	for time.Now().Before(deadline) {
		peers, err := listPeers(r, syncConvergeTimeout)
		if err == nil {
			got = 0
			for _, p := range peers {
				if p.Fingerprint == fp && !p.Sibling && p.RetiredAt == 0 {
					got++
				}
			}
			if got == want {
				return nil
			}
		}
		rigSleep(syncPollInterval, "poll: pairings")
	}
	return fmt.Errorf("%s holds %d pairing(s) with the person, want %d", r.name, got, want)
}

func offerToOneDevice(offerer *rig, fp string, theirs []*rig) (*rig, string, error) {
	offSub, offCancel := offerer.subscribe()
	defer offCancel()
	to, msgID, err := offerAndLand(offerer, ipc.InvitationOfferRequest{Fingerprint: fp}, theirs)
	if err != nil {
		return to, "", err
	}
	sees, err := acceptAndComplete(offSub, to, msgID)
	return to, sees, err
}

func offerAndLand(offerer *rig, req ipc.InvitationOfferRequest, theirs []*rig) (*rig, string, error) {
	subs := make([]<-chan ipc.Frame, len(theirs))
	for i, r := range theirs {
		sub, cancel := r.subscribe()
		defer cancel()
		subs[i] = sub
	}
	probeMark := markLog(filepath.Join(offerer.root, "haomad.log"))
	offered, _, err := offerInvitationAs(offerer, req)
	if err != nil {
		return nil, "", fmt.Errorf("%s offers: %w", offerer.name, err)
	}
	got, err := awaitRowOnAny(subs, offered.MsgID, invitationTimeout)
	if err != nil {
		return nil, offered.MsgID, err
	}
	to := theirs[got]
	if err := awaitSelfProbeSince(offerer, probeMark, rendezvousReachableTimeout); err != nil {
		return to, offered.MsgID, err
	}
	return to, offered.MsgID, nil
}

func acceptAndComplete(offSub <-chan ipc.Frame, to *rig, msgID string) (string, error) {
	if _, _, err := acceptInvitation(to, msgID); err != nil {
		return "", fmt.Errorf("%s accepts: %w", to.name, err)
	}
	return awaitPeerPaired(offSub, invitationCompleteTimeout)
}

func awaitRowOnAny(subs []<-chan ipc.Frame, msgID string, timeout time.Duration) (int, error) {
	type hit struct{ i int }
	found := make(chan hit, len(subs))
	done := make(chan struct{})
	defer close(done)
	for i, sub := range subs {
		go func(i int, sub <-chan ipc.Frame) {
			for {
				select {
				case f := <-sub:
					if ev, ok := timelineEvent(f); ok && ev.MsgID == msgID && ev.Kind == events.KindInvitation {
						found <- hit{i}
						return
					}
				case <-done:
					return
				}
			}
		}(i, sub)
	}
	select {
	case h := <-found:
		return h.i, nil
	case <-time.After(timeout):
		return -1, fmt.Errorf("no device of the person got invitation %s within %s", short(msgID), timeout)
	}
}
