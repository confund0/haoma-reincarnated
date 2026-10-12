package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
)

var readDriverVerdicts = []string{"read stamp", "read receipt", "read journal", "sibling read crossing", "sibling unread count", "sibling receipt sticky"}

func assertReadDriver(devA, devB, peer *rig, peerSeesA, bSeesPeer string) map[string]error {
	out := map[string]error{}
	failAll := func(err error) map[string]error {
		for _, n := range readDriverVerdicts {
			out[n] = err
		}
		return out
	}

	if err := devicesAtDebugLevel(devA, devB, peer); err != nil {
		return failAll(err)
	}

	bSub, bCancel := devB.subscribe()
	defer bCancel()
	peerSub, peerCancel := peer.subscribe()
	defer peerCancel()

	var fanoutPrimed bool
	if err := primePeerSessionToward(peer, devB, bSeesPeer); err != nil {
		out["sibling read crossing"] = err
		out["sibling unread count"] = err
		out["sibling receipt sticky"] = err
	} else {
		fanoutPrimed = true
	}

	text := "read-driver " + nextCorrID()
	d, err := deliverText(peer, devA, peerSeesA, text)
	if err != nil {
		return failAll(fmt.Errorf("peer send to %s: %w", devA.name, err))
	}
	msgID, aChatID := d.msgID, d.chatID
	logf("[%s] read-driver row msg_id=%s landed on %s — also fans out to %s", peer.name, short(msgID), devA.name, devB.name)

	if _, err := deviceReadsRow(devA, aChatID, readTimeout); err != nil {
		out["read stamp"] = fmt.Errorf("mark-read on %s: %w", devA.name, err)
	} else {

		n, cErr := journalTouchCount(devA, events.MutatorMarkRead, msgID)
		switch {
		case cErr != nil:
			out["read stamp"] = cErr
		case n != 1:
			out["read stamp"] = fmt.Errorf("%s journalled the read stamp %d time(s) for msg_id=%s, want exactly 1", devA.name, n, short(msgID))
		default:
			out["read stamp"] = nil
			logf("[%s] read stamp journalled (journal touches=%d)", devA.name, n)
		}
	}

	_, err = awaitFrame(peerSub, readTimeout, func(f ipc.Frame) bool {
		ev, ok := timelineEvent(f)
		return ok && ev.MsgID == msgID && ev.DeliveryState == "read"
	})
	if err != nil {
		out["read receipt"] = fmt.Errorf("%s never saw its outbound row reach read: %w", peer.name, err)
	} else {
		out["read receipt"] = nil
	}

	preDupN, jErr := journalTouchCount(peer, events.MutatorApplyReadReceipt, msgID)
	switch {
	case jErr != nil:
		out["read journal"] = jErr
	case preDupN != 1:
		out["read journal"] = fmt.Errorf("%s journalled the applied receipt %d time(s), want exactly 1", peer.name, preDupN)
	default:
		out["read journal"] = nil
		logf("[%s] read-receipt journal entry earned (journal touches=%d)", peer.name, preDupN)
	}

	if !fanoutPrimed {
		return completeVerdicts(out)
	}

	bChatID, bErr := awaitInboundText(bSub, msgID, mirrorTimeout)
	if bErr != nil {
		e := fmt.Errorf("%s never received the row the same send delivered to %s — nothing here has a copy to converge: %w", devB.name, devA.name, bErr)
		out["sibling read crossing"] = e
		out["sibling unread count"] = e
		out["sibling receipt sticky"] = e
		return completeVerdicts(out)
	}

	before, beforeErr := chatUnreadCount(devB, bChatID)

	logf("[%s] driving a sync — the read axis, the unread count and the receipt claim have no live push of their own", devB.name)
	if err := driveSync(devB, false); err != nil {
		e := fmt.Errorf("could not drive a sync on %s: %w", devB.name, err)
		out["sibling read crossing"] = e
		out["sibling unread count"] = e
		out["sibling receipt sticky"] = e
		return completeVerdicts(out)
	}

	out["sibling read crossing"] = func() error {
		if err := awaitRows(devB, bSeesPeer, syncConvergeTimeout, "sibling read crossing",
			"devB never showed the row read after devA's read crossed on the sync",
			[]string{msgID},
			func(rows map[string]events.Event) bool {
				ev, ok := rows[msgID]
				return ok && ev.ReadAt > 0
			}); err != nil {
			return err
		}

		n, cErr := journalTouchCount(devB, events.MutatorApplyInboundReadAt, msgID)
		if cErr != nil {
			return cErr
		}
		if n != 1 {
			return fmt.Errorf("%s journalled the inherited read time %d time(s) for msg_id=%s, want exactly 1", devB.name, n, short(msgID))
		}
		logf("[%s] sibling read crossing held: read time merged (journal touches=%d)", devB.name, n)
		return nil
	}()

	out["sibling unread count"] = func() error {
		if beforeErr != nil {
			return fmt.Errorf("could not read %s's unread count before the sync: %w", devB.name, beforeErr)
		}
		if before == 0 {
			return fmt.Errorf("%s already showed 0 unread on chat %s before the sync — zero would prove nothing there, so this gate cannot isolate the unread count crossing", devB.name, short(bChatID))
		}
		if err := awaitChatUnreadCount(bSub, bChatID, 0, syncConvergeTimeout); err != nil {
			return fmt.Errorf("%s's unread count on chat %s never reached 0 (was %d before the sync): %w", devB.name, short(bChatID), before, err)
		}
		logf("[%s] sibling unread count held: %d -> 0 on chat %s", devB.name, before, short(bChatID))
		return nil
	}()

	out["sibling receipt sticky"] = func() error {
		n, dErr := countApplyReceiptSentClaimChanged(devB, msgID)
		if dErr != nil {
			return dErr
		}
		if n != 1 {
			return fmt.Errorf("%s never merged devA's inherited receipt-sent claim (changed=true count=%d, want 1) — the sticky rule below would prove nothing", devB.name, n)
		}
		if _, err := deviceReadsRow(devB, bChatID, readTimeout); err != nil {
			return fmt.Errorf("mark-read on %s (post-convergence, must be a safe no-op): %w", devB.name, err)
		}
		n2, cErr := journalTouchCount(devB, events.MutatorMarkReadReceiptSent, msgID)
		if cErr != nil {
			return cErr
		}
		if n2 != 0 {
			return fmt.Errorf("%s shipped its own receipt-sent claim (journal touches=%d) even though it had already inherited devA's — the sticky rule did not hold", devB.name, n2)
		}
		n3, cErr := journalTouchCount(peer, events.MutatorApplyReadReceipt, msgID)
		if cErr != nil {
			return cErr
		}
		if n3 != preDupN {
			return fmt.Errorf("%s journalled the applied receipt %d time(s) after %s's own mark-read, want flat at %d — the peer received a second receipt for one reading", peer.name, n3, devB.name, preDupN)
		}
		logf("[%s] sibling receipt sticky held: %s shipped no claim of its own, %s's journal flat at %d", devB.name, devB.name, peer.name, n3)
		return nil
	}()

	return completeVerdicts(out)
}

func completeVerdicts(out map[string]error) map[string]error {
	for _, n := range readDriverVerdicts {
		if _, ok := out[n]; !ok {
			out[n] = fmt.Errorf("assertReadDriver: verdict %q was never set — bug in the driver itself", n)
		}
	}
	return out
}

func chatUnreadCount(r *rig, chatID string) (uint32, error) {
	chats, err := listChats(r, syncConvergeTimeout)
	if err != nil {
		return 0, err
	}
	for _, c := range chats {
		if c.ChatID == chatID {
			return c.UnreadCount, nil
		}
	}
	return 0, fmt.Errorf("%s: chat %s not found in its own chat list", r.name, short(chatID))
}

func awaitChatUnreadCount(sub <-chan ipc.Frame, chatID string, want uint32, timeout time.Duration) error {
	_, err := awaitFrame(sub, timeout, func(f ipc.Frame) bool {
		if f.Type != ipc.FrameChatUnreadChanged {
			return false
		}
		var p ipc.ChatUnreadChangedPayload
		if json.Unmarshal(f.Payload, &p) != nil {
			return false
		}
		return p.ChatID == chatID && p.UnreadCount == want
	})
	return err
}

func countApplyReceiptSentClaimChanged(r *rig, msgID string) (int, error) {
	lines, err := rigLogLines(r)
	if err != nil {
		return 0, err
	}
	const marker = "events: ApplyReceiptSentClaim decision"
	needle := short(msgID)
	n := 0
	for _, line := range lines {
		if strings.Contains(line, marker) && strings.Contains(line, needle) && strings.Contains(line, "changed=true") {
			n++
		}
	}
	return n, nil
}

func devicesAtDebugLevel(rigs ...*rig) error {
	for _, r := range rigs {
		if !strings.EqualFold(r.logLevel, "debug") {
			return fmt.Errorf("%s is running at -log-level=%q — this driver counts DEBUG-only log lines and would otherwise fail as an unexplained timeout; rerun with -log-level debug (the scenario's default)", r.name, r.logLevel)
		}
	}
	return nil
}

func awaitInboundText(sub <-chan ipc.Frame, msgID string, timeout time.Duration) (string, error) {
	f, err := awaitFrame(sub, timeout, func(f ipc.Frame) bool {
		ev, ok := timelineEvent(f)
		return ok && ev.Direction == events.DirIn && ev.Kind == events.KindText && ev.MsgID == msgID
	})
	if err != nil {
		return "", err
	}
	ev, _ := timelineEvent(f)
	return string(ev.ChatID), nil
}

func deviceReadsRow(r *rig, chatID string, timeout time.Duration) (int, error) {
	sub, cancel := r.subscribe()
	defer cancel()
	id, err := r.sendReq(ipc.FrameMarkRead, ipc.MarkReadRequest{ChatID: chatID})
	if err != nil {
		return 0, fmt.Errorf("mark_read send: %w", err)
	}
	f, err := awaitFrame(sub, timeout, func(f ipc.Frame) bool {
		return f.ID == id && (f.Type == ipc.FrameMarkedRead || f.Type == ipc.FrameError)
	})
	if err != nil {
		return 0, fmt.Errorf("await marked_read: %w", err)
	}
	if f.Type == ipc.FrameError {
		return 0, frameErr("mark_read", f)
	}
	var resp ipc.MarkedReadResponse
	if err := json.Unmarshal(f.Payload, &resp); err != nil {
		return 0, fmt.Errorf("decode marked_read: %w", err)
	}
	return resp.MarkedCount, nil
}

func rigLogLines(r *rig) ([]string, error) {
	var lines []string
	opened := false
	for _, name := range []string{"haoma.log.prev", "haoma.log"} {
		f, err := os.Open(filepath.Join(r.root, name))
		if err != nil {
			continue
		}
		opened = true
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			lines = append(lines, sc.Text())
		}
		f.Close()
	}
	if !opened {
		return nil, fmt.Errorf("cannot read %s's log (haoma.log and haoma.log.prev both missing)", r.name)
	}
	return lines, nil
}

func logFieldValue(line, key string) (string, bool) {
	idx := strings.Index(line, key+"=")
	if idx < 0 {
		return "", false
	}
	rest := line[idx+len(key)+1:]
	if sp := strings.IndexByte(rest, ' '); sp >= 0 {
		rest = rest[:sp]
	}
	return rest, true
}

func journalTouchCount(r *rig, mutator events.Mutator, msgID string) (int, error) {
	lines, err := rigLogLines(r)
	if err != nil {
		return 0, err
	}
	const marker = "events: journal touch"
	needleMsg, wantMutator := short(msgID), string(mutator)
	n := 0
	for _, line := range lines {
		if !strings.Contains(line, marker) || !strings.Contains(line, needleMsg) {
			continue
		}
		if v, ok := logFieldValue(line, "mutator"); ok && v == wantMutator {
			n++
		}
	}
	return n, nil
}

func primePeerSessionToward(peer, devB *rig, bSeesPeer string) error {
	if _, err := deliverText(devB, peer, bSeesPeer, "fan-out warm-up "+nextCorrID()); err != nil {
		return fmt.Errorf("%s never received a message from %s — %s cannot hold a session to encrypt toward %s without one, so any fan-out to both of my devices never had a real chance: %w",
			peer.name, devB.name, peer.name, devB.name, err)
	}
	logf("[%s] can encrypt toward %s — a message from it arrived and decrypted", peer.name, devB.name)
	return nil
}
