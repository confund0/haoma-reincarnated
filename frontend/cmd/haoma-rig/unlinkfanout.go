package main

import (
	"encoding/json"
	"fmt"
	"time"

	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/logid"
)

const (
	fanoutRetireTimeout = 5 * time.Minute

	fanoutPollInterval = 3 * time.Second

	fanoutPushTimeout = 30 * time.Second

	fanoutLogTimeout = 30 * time.Second

	noticeRetiredLine    = "unlink notice: a pairing was retired and its address dropped"
	noticeRetiredOutcome = "retired_on_unlink_notice"

	chatMovedLine    = "chat routing set: retired pairings left it"
	chatMovedOutcome = "primary_moved_to_live_pairing"
	chatMovedTrigger = "retire"

	fanoutChatMoveTimeout = 30 * time.Second
)

func runUnlinkFanout(res *pairingResult, stays, goes, peer *rig, peerSeesStays, peerSeesGoes, staysSeesGoes, peerChatID string) {

	before, err := listPeers(peer, syncConvergeTimeout)
	if err != nil {
		res.add("contact holds two live pairings with one person",
			fmt.Errorf("could not read %s's peer list: %w", peer.name, err))
		return
	}
	if err := assertBothPairingsLive(peer, before, peerSeesStays, peerSeesGoes); err != nil {
		res.add("contact holds two live pairings with one person", err)
		return
	}
	res.add("contact holds two live pairings with one person", nil)

	chatsBefore, err := listChats(peer, syncConvergeTimeout)
	if err == nil {
		err = assertChatPrimary(peer, chatsBefore, peerChatID, peerSeesGoes)
	}
	res.add("the contact's chat is on the pairing that goes", err)
	if err != nil {
		return
	}

	peerSub, peerCancel := peer.subscribe()
	defer peerCancel()

	linesBefore, beforeErr := rigLogLines(peer)

	logf("[%s] unlinking %s — the contacts have to learn that device is gone", stays.name, goes.name)
	if _, err := unlinkDevice(stays, staysSeesGoes, ""); err != nil {
		res.add("the device is unlinked", err)
		return
	}
	res.add("the device is unlinked", nil)

	after, retireErr := awaitPairingRetired(peer, peerSeesGoes, peerSeesStays)
	res.add("the contact retires the unlinked device's pairing", retireErr)
	if retireErr != nil {
		for _, name := range unlinkFanoutAfterRetireChecks {
			res.add(name, fmt.Errorf("not asserted: the unlinked device's pairing was never retired"))
		}
		return
	}
	logf("[%s] retired its pairing with %s and kept the one with %s", peer.name, goes.name, stays.name)

	pushErr := awaitRetiredPush(peerSub, peerSeesGoes)
	res.add("the contact's client is told the pairing was retired", pushErr)

	if beforeErr != nil {
		res.add("the contact retired it on the unlink notice",
			fmt.Errorf("not asserted: %s's log could not be read before the unlink: %w", peer.name, beforeErr))
		res.add("the contact's log says the retire moved the chat",
			fmt.Errorf("not asserted: %s's log could not be read before the unlink: %w", peer.name, beforeErr))
	} else {
		res.add("the contact retired it on the unlink notice",
			awaitNoticeRetiredLine(peer, peerSeesGoes, noticeRetiredLines(linesBefore, peerSeesGoes)))
		res.add("the contact's log says the retire moved the chat",
			awaitChatMovedLine(peer, peerChatID, peerSeesStays, chatMovedLines(linesBefore, peerChatID, peerSeesStays)))
	}

	res.add("the contact's chat moved to the pairing that stays",
		awaitChatMoved(peer, peerChatID, peerSeesStays, peerSeesGoes))

	res.add("contact -> device that stays text lands in the same chat",
		assertTextInChat(peer, stays, peerSeesStays, peerChatID))

	final, err := listPeers(peer, syncConvergeTimeout)
	if err != nil {

		final = after
		logf("[%s] the closing peer list could not be read (%v); using the last one the wait took", peer.name, err)
	}
	res.add("the contact keeps the pairing with the device that stays",
		assertPairingLive(peer, final, peerSeesStays))
}

var unlinkFanoutAfterRetireChecks = []string{
	"the contact keeps the pairing with the device that stays",
	"the contact's client is told the pairing was retired",
	"the contact retired it on the unlink notice",
	"the contact's log says the retire moved the chat",
	"the contact's chat moved to the pairing that stays",
	"contact -> device that stays text lands in the same chat",
}

var unlinkFanoutChecks = append([]string{
	"contact holds two live pairings with one person",
	"the contact's chat is on the pairing that goes",
	"the device is unlinked",
	"the contact retires the unlinked device's pairing",
}, unlinkFanoutAfterRetireChecks...)

func assertChatPrimary(r *rig, chats []ipc.ChatEntry, chatID, peerID string) error {
	for _, c := range chats {
		if c.ChatID != chatID {
			continue
		}
		if c.PeerID != peerID {
			return fmt.Errorf("%s's chat %s is on pairing %s, want %s", r.name, short(chatID), short(c.PeerID), short(peerID))
		}
		return nil
	}
	return fmt.Errorf("%s holds no chat %s", r.name, short(chatID))
}

func chatMovedTo(chats []ipc.ChatEntry, peers []ipc.PeerEntry, chatID, stays, goes string) error {
	var chat *ipc.ChatEntry
	for i := range chats {
		if chats[i].ChatID == chatID {
			chat = &chats[i]
		}
	}
	if chat == nil {
		return fmt.Errorf("the chat %s is gone", short(chatID))
	}
	if chat.PeerID != stays {
		return fmt.Errorf("the chat %s is still on pairing %s, want %s — a client draws it from that pairing's row", short(chatID), short(chat.PeerID), short(stays))
	}
	for _, m := range chat.Members {
		if m == goes {
			return fmt.Errorf("the chat %s still routes to the retired pairing %s (members %v)", short(chatID), short(goes), chat.Members)
		}
	}
	for _, p := range peers {
		if p.ID == stays {
			if p.RetiredAt != 0 {
				return fmt.Errorf("the chat %s is on pairing %s, and that pairing is retired", short(chatID), short(stays))
			}
			return nil
		}
	}
	return fmt.Errorf("the chat %s is on pairing %s, which the peer list does not hold", short(chatID), short(stays))
}

func awaitChatMoved(r *rig, chatID, stays, goes string) error {
	deadline := time.Now().Add(fanoutChatMoveTimeout)
	var last error
	for {
		chats, cErr := listChats(r, syncConvergeTimeout)
		peers, pErr := listPeers(r, syncConvergeTimeout)
		switch {
		case cErr != nil:
			last = fmt.Errorf("could not read %s's chat list: %w", r.name, cErr)
		case pErr != nil:
			last = fmt.Errorf("could not read %s's peer list: %w", r.name, pErr)
		default:
			last = chatMovedTo(chats, peers, chatID, stays, goes)
			if last == nil {
				return nil
			}
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("%s: %w", r.name, last)
		}
		time.Sleep(fanoutPollInterval)
	}
}

func assertTextInChat(r, to *rig, toPeerID, chatID string) error {
	sub, cancel := r.subscribe()
	defer cancel()
	d, err := deliverText(r, to, toPeerID, "still the same chat after the unlink")
	if err != nil {
		return err
	}
	f, err := awaitFrame(sub, fanoutPushTimeout, func(f ipc.Frame) bool {
		ev, ok := timelineEvent(f)
		return ok && ev.Direction == events.DirOut && ev.MsgID == d.msgID
	})
	if err != nil {
		return fmt.Errorf("the text arrived, but %s's own copy of it was never pushed: %w", r.name, err)
	}
	ev, _ := timelineEvent(f)
	if string(ev.ChatID) != chatID {
		return fmt.Errorf("%s filed the text in chat %s, want the chat it held before the unlink, %s", r.name, short(string(ev.ChatID)), short(chatID))
	}
	return nil
}

func assertBothPairingsLive(r *rig, peers []ipc.PeerEntry, ids ...string) error {
	for _, id := range ids {
		if err := assertPairingLive(r, peers, id); err != nil {
			return err
		}
	}
	return nil
}

func assertPairingLive(r *rig, peers []ipc.PeerEntry, peerID string) error {
	for _, p := range peers {
		if p.ID != peerID {
			continue
		}
		if p.RetiredAt != 0 {
			return fmt.Errorf("%s retired its pairing %s at %d — that is the device that stays, and retiring it costs %s the whole person",
				r.name, short(peerID), p.RetiredAt, r.name)
		}
		return nil
	}
	return fmt.Errorf("%s holds no pairing %s at all", r.name, short(peerID))
}

func awaitPairingRetired(r *rig, retiredID, keepID string) ([]ipc.PeerEntry, error) {
	deadline := time.Now().Add(fanoutRetireTimeout)
	var last []ipc.PeerEntry
	for {
		peers, err := listPeers(r, syncConvergeTimeout)
		if err == nil {
			last = peers
			if kErr := assertPairingLive(r, peers, keepID); kErr != nil {
				return peers, fmt.Errorf("while waiting for the retirement: %w", kErr)
			}
			if pairingRetired(peers, retiredID) {
				return peers, nil
			}
		}
		if !time.Now().Before(deadline) {
			return last, fmt.Errorf("%s still holds pairing %s as live %s after the device was unlinked — "+
				"the unlink notice did not arrive, or it arrived and retired nothing",
				r.name, short(retiredID), fanoutRetireTimeout)
		}
		time.Sleep(fanoutPollInterval)
	}
}

func pairingRetired(peers []ipc.PeerEntry, peerID string) bool {
	for _, p := range peers {
		if p.ID == peerID {
			return p.RetiredAt != 0
		}
	}
	return false
}

func awaitRetiredPush(sub <-chan ipc.Frame, retiredID string) error {
	_, err := awaitFrame(sub, fanoutPushTimeout, func(f ipc.Frame) bool {
		if f.Type != ipc.FramePeerUpdated {
			return false
		}
		var p ipc.PeerUpdatedPayload
		if json.Unmarshal(f.Payload, &p) != nil {
			return false
		}
		return p.Peer.ID == retiredID && p.Peer.RetiredAt != 0
	})
	if err != nil {
		return fmt.Errorf("no peer.updated naming %s as retired: %w", short(retiredID), err)
	}
	return nil
}

func countNoticeRetiredLines(r *rig, peerID string) (int, error) {
	lines, err := rigLogLines(r)
	if err != nil {
		return 0, err
	}
	return noticeRetiredLines(lines, peerID), nil
}

func noticeRetiredLines(lines []string, peerID string) int {
	onDisk := logid.Short(peerID)
	n := 0
	for _, line := range lines {
		if !hasLogMsg(line, noticeRetiredLine) {
			continue
		}
		if v, ok := logFieldValue(line, "peer_id"); !ok || v != onDisk {
			continue
		}
		if v, ok := logFieldValue(line, "outcome"); !ok || v != noticeRetiredOutcome {
			continue
		}
		n++
	}
	return n
}

func awaitNoticeRetiredLine(r *rig, peerID string, before int) error {
	deadline := time.Now().Add(fanoutLogTimeout)
	for {
		n, err := countNoticeRetiredLines(r, peerID)
		if err == nil && n > before {
			logf("[%s] log: the unlink notice retired %s", r.name, short(peerID))
			return nil
		}
		if !time.Now().Before(deadline) {
			if err != nil {
				return fmt.Errorf("could not read %s's log: %w", r.name, err)
			}
			return fmt.Errorf("%s retired pairing %s, but its log has no line %q with outcome=%s for it — "+
				"something other than the unlink notice retired it",
				r.name, short(peerID), noticeRetiredLine, noticeRetiredOutcome)
		}
		time.Sleep(2 * time.Second)
	}
}

func chatMovedLines(lines []string, chatID, primary string) int {
	wantChat, wantPrimary := logid.Short(chatID), logid.Short(primary)
	n := 0
	for _, line := range lines {
		if !hasLogMsg(line, chatMovedLine) {
			continue
		}
		if v, ok := logFieldValue(line, "chat_id"); !ok || v != wantChat {
			continue
		}
		if v, ok := logFieldValue(line, "primary_peer_id"); !ok || v != wantPrimary {
			continue
		}
		if v, ok := logFieldValue(line, "outcome"); !ok || v != chatMovedOutcome {
			continue
		}
		if v, ok := logFieldValue(line, "trigger"); !ok || v != chatMovedTrigger {
			continue
		}
		n++
	}
	return n
}

func awaitChatMovedLine(r *rig, chatID, primary string, before int) error {
	deadline := time.Now().Add(fanoutLogTimeout)
	for {
		lines, err := rigLogLines(r)
		if err == nil && chatMovedLines(lines, chatID, primary) > before {
			logf("[%s] log: the retire moved chat %s to %s", r.name, short(chatID), short(primary))
			return nil
		}
		if !time.Now().Before(deadline) {
			if err != nil {
				return fmt.Errorf("could not read %s's log: %w", r.name, err)
			}
			return fmt.Errorf("%s has no line %q for chat %s with primary_peer_id=%s outcome=%s trigger=%s — "+
				"the retire did not move the chat",
				r.name, chatMovedLine, short(chatID), short(primary), chatMovedOutcome, chatMovedTrigger)
		}
		time.Sleep(2 * time.Second)
	}
}
