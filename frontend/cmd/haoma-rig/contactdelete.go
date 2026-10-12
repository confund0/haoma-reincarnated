package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"haoma-frontend/internal/ipc"
)

const (
	personDeletedLine = "contact delete: the person was deleted here"

	contactDeletePushedLine = "contact delete: pushed to a sibling"

	contactDeletePushDroppedLine = "seeded fault: the contact delete was not pushed to my other device"

	dropContactDeletePushOnLine = "seeded fault on: a contact deleted on this device is not pushed to my other device, so only the sweep can carry the delete"

	backfillContactsAppliedLine = "backfill: contacts applied"
)

const contactDeleteTimeout = 3 * time.Minute

type contactDeleteFault struct {
	devA     *rig
	relaunch relaunchFunc
	on       bool
}

func (s *contactDeleteFault) turnOn() error {
	s.on = true
	mark := haomaLogMark(s.devA)
	logf("[%s] starting haoma again with %s=1", s.devA.name, dropContactDeletePushEnv)
	if err := s.relaunch(s.devA, func(r *rig) { r.dropContactDeletePush = true }); err != nil {
		return err
	}
	return awaitStartLines(s.devA, mark, startLine{dropContactDeletePushOnLine, ""})
}

func (s *contactDeleteFault) restore() error {
	if !s.on {
		return nil
	}
	mark := haomaLogMark(s.devA)
	logf("[%s] starting haoma again without %s", s.devA.name, dropContactDeletePushEnv)
	if err := s.relaunch(s.devA, func(r *rig) { r.dropContactDeletePush = false }); err != nil {
		return err
	}
	if err := awaitStartedWithout(s.devA, mark, dropContactDeletePushOnLine); err != nil {
		return err
	}
	s.on = false
	return nil
}

type contactCase struct {
	name string
	err  error
}

func assertContactDelete(devA, devB, peer *rig, aSeesPeer, bSeesPeer string) []contactCase {
	var out []contactCase
	add := func(name string, err error) { out = append(out, contactCase{name, err}) }
	notReached := func(why error, names ...string) {
		for _, n := range names {
			add(n, fmt.Errorf("not reached: %w", why))
		}
	}
	if !strings.EqualFold(devA.logLevel, "debug") || !strings.EqualFold(devB.logLevel, "debug") {
		err := fmt.Errorf("the rigs run at -log-level=%q/%q; this group reads DEBUG lines", devA.logLevel, devB.logLevel)
		add("contact delete crosses by push", err)
		return out
	}
	fp, err := peerFingerprint(devA, aSeesPeer)
	if err != nil {
		add("contact delete crosses by push", err)
		return out
	}

	erased := map[string]bool{}
	if err := collectMsgIDs(erased, devA, aSeesPeer, devB, bSeesPeer); err != nil {
		add("contact delete crosses by push", err)
		return out
	}

	markA, markB := haomaLogMark(devA), haomaLogMark(devB)
	logf("[%s] deleting %s (%s)", devA.name, peer.name, short(aSeesPeer))
	if err := deletePeer(devA, aSeesPeer); err != nil {
		add("contact delete crosses by push", err)
		return out
	}
	add("the deleting device loses the contact", awaitContactGone(devA, aSeesPeer, fp))
	add("the delete is pushed", awaitMarkedLine(devA, markA, contactDeleteTimeout, lineWant{contactDeletePushedLine, nil}))
	add("contact delete crosses by push", firstErr(
		awaitMarkedLine(devB, markB, contactDeleteTimeout, lineWant{personDeletedLine, map[string]string{"origin": "sibling_push"}}),
		awaitContactGone(devB, bSeesPeer, fp),
	))

	a1, b1, err := rePairBoth(devA, devB, peer, fp)
	add("re-pair after a delete", err)
	if err != nil {
		notReached(err, "a sweep from the deleting device refuses the contact", "a sweep carries the delete")
		return out
	}

	fault := &contactDeleteFault{devA: devA, relaunch: relaunchHaoma}
	defer func() {
		if err := fault.restore(); err != nil {
			logf("[%s] COULD NOT TAKE %s OFF AGAIN: %v", devA.name, dropContactDeletePushEnv, err)
		}
	}()
	if err := fault.turnOn(); err != nil {
		notReached(err, "a sweep from the deleting device refuses the contact", "a sweep carries the delete")
		return out
	}
	if err := collectMsgIDs(erased, devA, a1, devB, b1); err != nil {
		notReached(err, "a sweep from the deleting device refuses the contact", "a sweep carries the delete")
		return out
	}
	markA, markB = haomaLogMark(devA), haomaLogMark(devB)
	logf("[%s] deleting %s again, with the push dropped", devA.name, peer.name)
	if err := deletePeer(devA, a1); err != nil {
		notReached(err, "a sweep from the deleting device refuses the contact", "a sweep carries the delete")
		return out
	}

	if err := firstErr(
		awaitMarkedLine(devA, markA, contactDeleteTimeout, lineWant{contactDeletePushDroppedLine, nil}),
		awaitContactGone(devA, a1, fp),
		pushedToB(devB, markB),
	); err != nil {
		notReached(fmt.Errorf("the dropped push did not hold: %w", err),
			"a sweep from the deleting device refuses the contact", "a sweep carries the delete")
		return out
	}

	add("a sweep from the deleting device refuses the contact", func() error {
		if err := driveSync(devA, false); err != nil {
			return err
		}
		if err := awaitVerdict(contactDeleteTimeout, func() (roundVerdict, string, error) {
			aLines, err := markA.lines()
			if err != nil {
				return verdictPending, "", err
			}
			bLines, err := markB.lines()
			if err != nil {
				return verdictPending, "", err
			}
			v, why := refusalVerdict(aLines, bLines)
			return v, why, nil
		}); err != nil {
			return err
		}
		return awaitContactGone(devA, a1, fp)
	}())

	add("a sweep carries the delete", func() error {
		if err := driveSync(devB, false); err != nil {
			return err
		}
		if err := awaitVerdict(contactDeleteTimeout, func() (roundVerdict, string, error) {
			bLines, err := markB.lines()
			if err != nil {
				return verdictPending, "", err
			}
			v, why := carryVerdict(bLines)
			return v, why, nil
		}); err != nil {
			return err
		}
		return awaitContactGone(devB, b1, fp)
	}())
	if err := fault.restore(); err != nil {
		why := fmt.Errorf("could not take %s off: %w", dropContactDeletePushEnv, err)
		add("re-pair after the sweep", why)
		notReached(why, "text after the re-pair, deleting device",
			"text after the re-pair, other device",
			"no sync brings the deleted history back")
		return out
	}

	a2, b2, err := rePairBoth(devA, devB, peer, fp)
	add("re-pair after the sweep", err)
	if err != nil {
		notReached(err, "text after the re-pair, deleting device",
			"text after the re-pair, other device",
			"no sync brings the deleted history back")
		return out
	}
	_, err = deliverText(devA, peer, a2, "after the delete, from "+devA.name)
	add("text after the re-pair, deleting device", err)
	_, err = deliverText(devB, peer, b2, "after the delete, from "+devB.name)
	add("text after the re-pair, other device", err)

	add("no sync brings the deleted history back", func() error {
		if len(erased) == 0 {
			return fmt.Errorf("no msg id of the deleted histories was read, so this check would test nothing")
		}
		for _, r := range []*rig{devA, devB} {
			if err := syncAndAwaitFinishedWith(r, contactDeleteTimeout, true); err != nil {
				return err
			}
		}
		if err := firstErr(stillHolds(devA, a2), stillHolds(devB, b2)); err != nil {
			return err
		}
		for _, c := range []struct {
			r  *rig
			id string
		}{{devA, a2}, {devB, b2}} {
			ids, err := timelineMsgIDs(c.r, c.id, syncConvergeTimeout)
			if err != nil {
				return err
			}
			for id := range ids {
				if erased[id] {
					return fmt.Errorf("%s's new chat holds msg_id=%s from a history that was deleted", c.r.name, short(id))
				}
			}
		}
		return nil
	}())
	return out
}

func collectMsgIDs(into map[string]bool, devA *rig, aSees string, devB *rig, bSees string) error {
	for _, c := range []struct {
		r  *rig
		id string
	}{{devA, aSees}, {devB, bSees}} {
		ids, err := timelineMsgIDs(c.r, c.id, syncConvergeTimeout)
		if err != nil {
			return fmt.Errorf("read %s's history before the delete: %w", c.r.name, err)
		}
		for id := range ids {
			into[id] = true
		}
	}
	return nil
}

func rePairBoth(devA, devB, peer *rig, fp string) (aSees, bSees string, err error) {
	logf("[%s<->%s] pairing again after the delete", devA.name, peer.name)
	aSees, _, err = pairOnion(devA, peer)
	if err != nil {
		return "", "", fmt.Errorf("pair %s<->%s: %w", devA.name, peer.name, err)
	}
	awaitFirstReach(devA, peer, aSees)

	early, err := deliverText(devA, peer, aSees, "the first text after a re-pair "+nextCorrID())
	if err != nil {
		return aSees, "", fmt.Errorf("%s -> %s text after the re-pair: %w", devA.name, peer.name, err)
	}
	if err := awaitInheritedContact(devB, fp, contactDeleteTimeout); err != nil {
		return aSees, "", err
	}

	peerSub, peerCancel := peer.subscribe()
	defer peerCancel()
	bSub, bCancel := devB.subscribe()
	defer bCancel()
	probeMark := markLog(filepath.Join(devB.root, "haomad.log"))
	offered, _, err := offerInvitation(devB, fp)
	if err != nil {
		return aSees, "", fmt.Errorf("%s offers an invitation: %w", devB.name, err)
	}
	if _, _, err := awaitInvitationRow(peerSub, invitationTimeout, offered.MsgID); err != nil {
		return aSees, "", fmt.Errorf("%s never got %s's invitation: %w", peer.name, devB.name, err)
	}
	if err := awaitSelfProbeSince(devB, probeMark, rendezvousReachableTimeout); err != nil {
		return aSees, "", err
	}
	if _, _, err := acceptInvitation(peer, offered.MsgID); err != nil {
		return aSees, "", fmt.Errorf("%s accepts the invitation: %w", peer.name, err)
	}
	bSees, err = awaitPeerPaired(bSub, invitationCompleteTimeout)
	if err != nil {
		return aSees, "", fmt.Errorf("%s completes its half: %w", devB.name, err)
	}
	awaitFirstReach(devB, peer, bSees)
	logf("[%s] reaches %s again as %s, %s as %s", devA.name, peer.name, short(aSees), devB.name, short(bSees))
	if err := awaitRowOn(devB, bSees, early.msgID, contactDeleteTimeout); err != nil {
		return aSees, bSees, fmt.Errorf("the first text after the re-pair never reached %s: %w", devB.name, err)
	}
	return aSees, bSees, nil
}

func deletePeer(r *rig, peerID string) error {
	sub, cancel := r.subscribe()
	defer cancel()
	id, err := r.sendReq(ipc.FramePeerAction, ipc.PeerActionRequest{PeerID: peerID, Action: ipc.PeerActionDelete})
	if err != nil {
		return fmt.Errorf("peer_action send: %w", err)
	}
	f, err := awaitFrame(sub, syncConvergeTimeout, func(f ipc.Frame) bool {
		return f.ID == id && (f.Type == ipc.FramePeerActionApplied || f.Type == ipc.FrameError)
	})
	if err != nil {
		return fmt.Errorf("await peer_action_applied: %w", err)
	}
	if f.Type == ipc.FrameError {
		return frameErr("peer_action delete", f)
	}
	return nil
}

func peerFingerprint(r *rig, peerID string) (string, error) {
	peers, err := listPeers(r, syncConvergeTimeout)
	if err != nil {
		return "", err
	}
	for _, p := range peers {
		if p.ID == peerID {
			if p.Fingerprint == "" {
				return "", fmt.Errorf("%s's row for %s names no person", r.name, short(peerID))
			}
			return p.Fingerprint, nil
		}
	}
	return "", fmt.Errorf("%s has no peer %s", r.name, short(peerID))
}

func awaitContactGone(r *rig, peerID, fp string) error {
	deadline := time.Now().Add(contactDeleteTimeout)
	var last string
	for {
		last = contactLeft(r, peerID, fp)
		if last == "" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s still holds the deleted contact after %s: %s", r.name, contactDeleteTimeout, last)
		}
		time.Sleep(syncPollInterval)
	}
}

func contactLeft(r *rig, peerID, fp string) string {
	peers, err := listPeers(r, syncConvergeTimeout)
	if err != nil {
		return "the peer list could not be read: " + err.Error()
	}
	for _, p := range peers {
		if p.ID == peerID || (p.Fingerprint == fp && !p.Sibling) {
			return "peer " + short(p.ID)
		}
	}
	chats, err := listChats(r, syncConvergeTimeout)
	if err != nil {
		return "the chat list could not be read: " + err.Error()
	}
	for _, c := range chats {
		if c.PeerID == peerID || (fp != "" && c.Fingerprint == fp) {
			return "chat " + short(c.ChatID)
		}
	}
	inherited, err := listInheritedContacts(r)
	if err != nil {
		return "the inherited contacts could not be read: " + err.Error()
	}
	for _, c := range inherited {
		if c.Fingerprint == fp {
			return "inherited contact " + short(fp)
		}
	}
	return ""
}

func stillHolds(r *rig, peerID string) error {
	peers, err := listPeers(r, syncConvergeTimeout)
	if err != nil {
		return err
	}
	if !peersNameID(peers, peerID) {
		return fmt.Errorf("%s no longer lists %s", r.name, short(peerID))
	}
	return nil
}

func awaitInheritedContact(r *rig, fp string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		inherited, err := listInheritedContacts(r)
		if err == nil {
			for _, c := range inherited {
				if c.Fingerprint == fp {
					return nil
				}
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s never offered the re-paired contact as inherited within %s (last read error: %v)", r.name, timeout, err)
		}
		time.Sleep(syncPollInterval)
	}
}

func listInheritedContacts(r *rig) ([]ipc.InheritedContactEntry, error) {
	sub, cancel := r.subscribe()
	defer cancel()
	id, err := r.sendReq(ipc.FrameListInheritedContacts, nil)
	if err != nil {
		return nil, fmt.Errorf("list_inherited_contacts send: %w", err)
	}
	f, err := awaitFrame(sub, syncConvergeTimeout, func(f ipc.Frame) bool {
		return f.ID == id && (f.Type == ipc.FrameInheritedContactsListed || f.Type == ipc.FrameError)
	})
	if err != nil {
		return nil, fmt.Errorf("await inherited_contacts_listed: %w", err)
	}
	if f.Type == ipc.FrameError {
		return nil, frameErr("list_inherited_contacts", f)
	}
	var listed ipc.InheritedContactsListedResponse
	if err := json.Unmarshal(f.Payload, &listed); err != nil {
		return nil, fmt.Errorf("decode inherited_contacts_listed: %w", err)
	}
	return listed.Contacts, nil
}

func awaitSelfProbeSince(r *rig, mark logMark, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		lines, err := mark.lines()
		if err != nil {
			return err
		}
		for _, line := range lines {
			if strings.Contains(line, "pair: self-probe attempt") && strings.Contains(line, "ready=true") {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s never reached its new rendezvous within %s", r.name, timeout)
		}
		time.Sleep(5 * time.Second)
	}
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

type roundVerdict int

const (
	verdictPending roundVerdict = iota
	verdictPass

	verdictNotReached
	verdictFail
)

func refusalVerdict(aLines, bLines []string) (roundVerdict, string) {
	for _, line := range aLines {
		if !hasLogMsg(line, backfillContactsAppliedLine) {
			continue
		}
		v, _ := logFieldValue(line, " refused_deleted")
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			return verdictPass, ""
		}
	}
	for _, line := range bLines {
		if hasLogMsg(line, personDeletedLine) {
			origin, _ := logFieldValue(line, " origin")
			return verdictNotReached, fmt.Sprintf("dev-b took the delete (origin=%s) before it answered dev-a's sweep, so it no longer described the person", origin)
		}
	}
	return verdictPending, "dev-a has logged no contacts batch that refused the deleted person"
}

func carryVerdict(bLines []string) (roundVerdict, string) {
	for _, line := range bLines {
		if !hasLogMsg(line, personDeletedLine) {
			continue
		}
		switch origin, _ := logFieldValue(line, " origin"); origin {
		case "sibling_push":
			return verdictFail, "dev-b took the delete by the push, which the fault should have dropped"
		case "sibling_sweep":
			return verdictPass, ""
		}
	}
	return verdictPending, "dev-b has not taken the delete"
}

func pushedToB(devB *rig, mark logMark) error {
	lines, err := mark.lines()
	if err != nil {
		return err
	}
	if v, why := carryVerdict(lines); v == verdictFail {
		return fmt.Errorf("%s: %s", devB.name, why)
	}
	return nil
}

func awaitVerdict(timeout time.Duration, read func() (roundVerdict, string, error)) error {
	deadline := time.Now().Add(timeout)
	for {
		v, why, err := read()
		if err != nil {
			return err
		}
		switch v {
		case verdictPass:
			return nil
		case verdictNotReached:
			return fmt.Errorf("not reached: %s", why)
		case verdictFail:
			return errors.New(why)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("after %s: %s", timeout, why)
		}
		time.Sleep(syncPollInterval)
	}
}
