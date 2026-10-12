package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/logid"
)

const (
	syncConvergeTimeout = 90 * time.Second

	restartConvergeTimeout = 240 * time.Second

	syncPollInterval = 5 * time.Second
)

func assertGapFill(devA, devB, peer *rig, peerSeesA, bSeesPeer string, since gapFillMark) map[string]error {
	names := []string{"sync inbound", "sync edit", "sync delete", "sync on reconnect", "healed by sync", "recovered row provenance", "one row per message"}
	out := map[string]error{}
	fail := func(err error) map[string]error {
		for _, n := range names {
			out[n] = err
		}
		return out
	}

	logf("[%s] going down — the peer's next messages must be unreachable by fan-out", devB.name)
	if err := devB.stop(); err != nil {
		return fail(fmt.Errorf("could not take %s down: %w", devB.name, err))
	}

	logf("[%s] sending while %s is down", peer.name, devB.name)
	editTarget, err := deliverText(peer, devA, peerSeesA, "sync edit target "+nextCorrID())
	if err != nil {
		return fail(fmt.Errorf("peer send to %s failed: %w", devA.name, err))
	}
	deleteTarget, err := deliverText(peer, devA, peerSeesA, "sync delete target "+nextCorrID())
	if err != nil {
		return fail(fmt.Errorf("peer send to %s failed: %w", devA.name, err))
	}

	if err := waitForOutboxGiveUp(peer, 2); err != nil {
		return fail(fmt.Errorf("could not close off the direct route before bringing %s back: %w", devB.name, err))
	}

	logf("[%s] coming back up", devB.name)
	if err := devB.start(); err != nil {
		return fail(fmt.Errorf("could not bring %s back: %w", devB.name, err))
	}

	if rows, rErr := timelineRows(devB, bSeesPeer, syncConvergeTimeout); rErr == nil {
		if _, held := rows[editTarget.msgID]; held {
			return fail(fmt.Errorf("%s holds the peer's message without syncing even though it was down — this gate no longer isolates gap-fill", devB.name))
		}
	}

	logf("[%s] syncing …", devB.name)
	if err := driveSync(devB, false); err != nil {
		return fail(fmt.Errorf("could not drive a sync on %s: %w", devB.name, err))
	}

	out["sync inbound"] = awaitRows(devB, bSeesPeer, restartConvergeTimeout, "sync inbound",
		"the peer's messages never crossed from the sibling",
		[]string{editTarget.msgID, deleteTarget.msgID},
		func(rows map[string]events.Event) bool {
			_, a := rows[editTarget.msgID]
			_, b := rows[deleteTarget.msgID]
			return a && b
		})
	if out["sync inbound"] != nil {

		for _, n := range names[1:] {
			out[n] = fmt.Errorf("not asserted: the inbound sync did not land")
		}
		return out
	}

	out["recovered row provenance"] = func() error {
		rows, err := timelineRows(devB, bSeesPeer, syncConvergeTimeout)
		if err != nil {
			return fmt.Errorf("could not read %s's timeline: %w", devB.name, err)
		}
		for _, msgID := range []string{editTarget.msgID, deleteTarget.msgID} {
			ev, ok := rows[msgID]
			if !ok {
				return fmt.Errorf("row %s vanished between assertions", short(msgID))
			}
			if !ev.MirroredFromSibling {
				return fmt.Errorf("row %s arrived by gap-fill but is not marked as recovered — "+
					"the renderer cannot tell it apart from a row this device witnessed", short(msgID))
			}
		}
		return nil
	}()

	logf("[%s] going down again — the peer's edit and delete must be unreachable by fan-out", devB.name)
	if err := devB.stop(); err != nil {
		return fail(fmt.Errorf("could not take %s down: %w", devB.name, err))
	}

	const editedText = "edited by the peer while the sibling was down"
	logf("[%s] mutating rows while %s is down", peer.name, devB.name)
	if err := assertEdit(peer, devA, peerSeesA, editTarget.msgID, editedText); err != nil {
		return fail(fmt.Errorf("the peer's edit did not reach %s: %w", devA.name, err))
	}
	if err := assertDelete(peer, devA, peerSeesA, deleteTarget.msgID); err != nil {
		return fail(fmt.Errorf("the peer's delete did not reach %s: %w", devA.name, err))
	}

	if err := waitForOutboxGiveUp(peer, 2); err != nil {
		return fail(fmt.Errorf("could not close off the direct route before bringing %s back: %w", devB.name, err))
	}

	logf("[%s] coming back up", devB.name)
	if err := devB.start(); err != nil {
		return fail(fmt.Errorf("could not bring %s back: %w", devB.name, err))
	}

	if rows, rErr := timelineRows(devB, bSeesPeer, syncConvergeTimeout); rErr == nil {
		if ev, held := rows[editTarget.msgID]; held && ev.EditedAt > 0 {
			return fail(fmt.Errorf("%s reflects the peer's edit without syncing even though it was down — this gate no longer isolates gap-fill", devB.name))
		}
		if ev, held := rows[deleteTarget.msgID]; held && ev.DeletedAt > 0 {
			return fail(fmt.Errorf("%s reflects the peer's delete without syncing even though it was down — this gate no longer isolates gap-fill", devB.name))
		}
	}

	logf("[%s] syncing again …", devB.name)
	if err := driveSync(devB, false); err != nil {
		return fail(fmt.Errorf("could not drive the second sync on %s: %w", devB.name, err))
	}
	out["sync edit"] = awaitRows(devB, bSeesPeer, restartConvergeTimeout, "sync edit",
		"the edit never crossed — this is the in-place rewrite a sweep of the event log cannot see",
		[]string{editTarget.msgID},
		func(rows map[string]events.Event) bool {
			ev, ok := rows[editTarget.msgID]
			if !ok || ev.EditedAt <= 0 {
				return false
			}
			var b events.TextBody
			return json.Unmarshal(ev.Body, &b) == nil && b.Text == editedText
		})
	out["sync delete"] = awaitRows(devB, bSeesPeer, restartConvergeTimeout, "sync delete",
		"the tombstone never crossed — deleted content is still readable on this device",
		[]string{deleteTarget.msgID},
		func(rows map[string]events.Event) bool {
			ev, ok := rows[deleteTarget.msgID]
			return ok && ev.DeletedAt > 0
		})

	logf("[%s] going down", devB.name)
	if err := devB.stop(); err != nil {
		out["sync on reconnect"] = fmt.Errorf("could not take %s down: %w", devB.name, err)
	} else {
		reconnectMsg, sErr := deliverText(peer, devA, peerSeesA, "sent while the sibling was down "+nextCorrID())
		if sErr != nil {
			out["sync on reconnect"] = fmt.Errorf("peer send during the downtime failed: %w", sErr)
		} else if err := waitForOutboxGiveUp(peer, 1); err != nil {
			out["sync on reconnect"] = fmt.Errorf("could not close off the direct route before bringing %s back: %w", devB.name, err)
		} else {
			logf("[%s] coming back up — nobody will ask it to sync", devB.name)
			if err := devB.start(); err != nil {
				out["sync on reconnect"] = fmt.Errorf("could not bring %s back up: %w", devB.name, err)
			} else {
				out["sync on reconnect"] = awaitRows(devB, bSeesPeer, restartConvergeTimeout, "sync on reconnect",
					"nothing converged after the reconnect — the backend-ready trigger did not fire",
					[]string{reconnectMsg.msgID},
					func(rows map[string]events.Event) bool {
						_, ok := rows[reconnectMsg.msgID]
						return ok
					})
			}
		}
	}

	out["healed by sync"] = assertGapFillDidTheWork(devB, since)

	out["one row per message"] = assertOneRowPerMessage(devB, bSeesPeer)
	return out
}

func driveSync(r *rig, full bool) error {
	sub, cancel := r.subscribe()
	defer cancel()
	if err := r.send(ipc.FrameDeviceSync, ipc.DeviceSyncRequest{Full: full}); err != nil {
		return fmt.Errorf("devicesync send: %w", err)
	}
	f, err := awaitFrame(sub, 30*time.Second, func(f ipc.Frame) bool {
		return f.Type == ipc.FrameDeviceSyncSent || f.Type == ipc.FrameError
	})
	if err != nil {
		return fmt.Errorf("await devicesync_sent: %w", err)
	}
	if f.Type == ipc.FrameError {
		return frameErr("devicesync", f)
	}
	var resp ipc.DeviceSyncSentResponse
	if err := json.Unmarshal(f.Payload, &resp); err != nil {
		return fmt.Errorf("decode devicesync_sent: %w", err)
	}
	if resp.Siblings == 0 {
		return fmt.Errorf("%s reports no linked devices to sync with", r.name)
	}
	return nil
}

func awaitRows(r *rig, peerID string, timeout time.Duration, label, why string, watch []string, want func(map[string]events.Event) bool) error {
	deadline := time.Now().Add(timeout)
	last := ""
	for time.Now().Before(deadline) {
		rows, err := timelineRows(r, peerID, 30*time.Second)
		if err == nil && want(rows) {
			logf("[%s] converged: %s", r.name, label)
			return nil
		}

		if obs := observeRows(rows, err, watch); obs != last {
			logf("[%s] %s: %s", r.name, label, obs)
			last = obs
		}
		rigSleep(syncPollInterval, "poll: sync converge")
	}
	return fmt.Errorf("%s after %s (last read: %s)", why, timeout, last)
}

func observeRows(rows map[string]events.Event, err error, watch []string) string {
	if err != nil {
		return "read failed: " + err.Error()
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d row(s)", len(rows))
	for _, msgID := range watch {
		ev, ok := rows[msgID]
		if !ok {
			fmt.Fprintf(&sb, "; %s absent", short(msgID))
			continue
		}
		var b events.TextBody
		_ = json.Unmarshal(ev.Body, &b)
		fmt.Fprintf(&sb, "; %s chat=%s edited_at=%d deleted_at=%d mirrored=%t text=%q",
			short(msgID), short(string(ev.ChatID)), ev.EditedAt, ev.DeletedAt,
			ev.MirroredFromSibling, truncate(b.Text, 40))
	}
	return sb.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

const outboxGiveUpPollInterval = 10 * time.Second

const (
	outboxGiveUpBaseSlack        = 210 * time.Second
	outboxGiveUpPerEnvelopeSlack = 90 * time.Second
)

func waitForOutboxGiveUp(peer *rig, want int) error {
	if peer.giveUp == "" {
		return fmt.Errorf("%s has no -outbox-giveup configured (the shipped default is 48h + 72 attempts) — this gate cannot close off the direct route inside any timeout a test can wait out; rerun with -outbox-giveup set", peer.name)
	}
	d, err := time.ParseDuration(peer.giveUp)
	if err != nil {
		return fmt.Errorf("%s: -outbox-giveup %q does not parse: %w", peer.name, peer.giveUp, err)
	}

	before, err := countOutboxFailed(peer)
	if err != nil {
		return fmt.Errorf("could not read %s's log before the hold: %w", peer.name, err)
	}

	timeout := d + outboxGiveUpBaseSlack + outboxGiveUpPerEnvelopeSlack*time.Duration(want)
	deadline := time.Now().Add(timeout)
	logf("[%s] holding up to %s so its outbox exhausts retries before the target is reachable again", peer.name, timeout)

	var got int
	for {
		time.Sleep(outboxGiveUpPollInterval)
		after, cErr := countOutboxFailed(peer)
		if cErr != nil {
			return fmt.Errorf("could not read %s's log during the hold: %w", peer.name, cErr)
		}
		got = after - before
		if got >= want {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s's outbox dead-lettered %d envelope(s) after %s, want at least %d — the give-up window may not be wired (HAOMA_OUTBOX_GIVEUP), so this gate cannot claim the direct route is closed", peer.name, got, timeout, want)
		}
	}
	logf("[%s] outbox dead-lettered %d envelope(s) — the direct route is closed", peer.name, got)
	return nil
}

func countOutboxFailed(r *rig) (int, error) {
	lines, err := daemonLogLines(r)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, line := range lines {
		if strings.Contains(line, "outbox: transition") && strings.Contains(line, "to=failed") {
			n++
		}
	}
	return n, nil
}

func waitForEnvelopeGiveUp(r *rig, envelopeID string) error {
	if envelopeID == "" {
		return fmt.Errorf("%s: no envelope id to wait on — this gate cannot claim the direct route is closed", r.name)
	}
	return waitForLoggedEnvelopeGiveUp(r, logid.Short(envelopeID))
}

func waitForLoggedEnvelopeGiveUp(r *rig, onDiskID string) error {
	if onDiskID == "" {
		return fmt.Errorf("%s: no envelope id to wait on — this gate cannot claim the direct route is closed", r.name)
	}
	if r.giveUp == "" {
		return fmt.Errorf("%s has no -outbox-giveup configured (the shipped default is 48h + 72 attempts) — this gate cannot close off the direct route inside any timeout a test can wait out; rerun with -outbox-giveup set", r.name)
	}
	d, err := time.ParseDuration(r.giveUp)
	if err != nil {
		return fmt.Errorf("%s: -outbox-giveup %q does not parse: %w", r.name, r.giveUp, err)
	}

	timeout := d + outboxGiveUpBaseSlack + outboxGiveUpPerEnvelopeSlack
	deadline := time.Now().Add(timeout)
	logf("[%s] holding up to %s so envelope %s exhausts its retries", r.name, timeout, onDiskID)

	for {
		dead, cErr := loggedEnvelopeDeadLettered(r, onDiskID)
		if cErr != nil {
			return fmt.Errorf("could not read %s's log during the hold: %w", r.name, cErr)
		}
		if dead {
			logf("[%s] envelope %s dead-lettered — the direct route is closed", r.name, onDiskID)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s never dead-lettered envelope %s after %s — the give-up window may not be wired (HAOMA_OUTBOX_GIVEUP), so this gate cannot claim the direct route is closed", r.name, onDiskID, timeout)
		}
		time.Sleep(outboxGiveUpPollInterval)
	}
}

func logMatchID(key, id string) string {
	return key + "=" + logid.Short(id)
}

func envelopeDeadLettered(r *rig, envelopeID string) (bool, error) {
	if envelopeID == "" {
		return false, nil
	}
	return loggedEnvelopeDeadLettered(r, logid.Short(envelopeID))
}

func loggedEnvelopeDeadLettered(r *rig, onDiskID string) (bool, error) {
	if onDiskID == "" {
		return false, nil
	}
	lines, err := daemonLogLines(r)
	if err != nil {
		return false, err
	}
	for _, line := range lines {
		if !strings.Contains(line, "outbox: transition") || !strings.Contains(line, "to=failed") {
			continue
		}
		if got, _ := logFieldValue(line, "envelope_id"); got == onDiskID {
			return true, nil
		}
	}
	return false, nil
}

func enqueuedEnvelopeFor(r *rig, envelopeIDs []string, peerID string) (string, error) {
	if peerID == "" {
		return "", fmt.Errorf("%s: no peer id to look up an envelope for", r.name)
	}
	lines, err := daemonLogLines(r)
	if err != nil {
		return "", err
	}

	wantPeer := logMatchID("peer_id", peerID)
	enqueueLines := map[string]int{}
	addressedTo := map[string]bool{}
	var forThisPeer []string
	for _, line := range lines {
		if !strings.Contains(line, "/send enqueuing envelope") {
			continue
		}
		for _, id := range envelopeIDs {
			if id == "" || !strings.Contains(line, logMatchID("envelope_id", id)) {
				continue
			}
			enqueueLines[id]++
			if strings.Contains(line, wantPeer) && !addressedTo[id] {
				addressedTo[id] = true
				forThisPeer = append(forThisPeer, id)
			}
		}
	}

	if len(enqueueLines) == 0 {
		return "", fmt.Errorf("%s's log holds no enqueue line for any of this send's %d envelope(s) — the harness cannot see what the daemon did here (log level, a rotation, or the id form on disk), so this says nothing about which devices the send addressed",
			r.name, len(envelopeIDs))
	}
	if len(forThisPeer) == 0 {
		return "", fmt.Errorf("%s's log shows this send's envelope(s) enqueued, and none of them for %s — the send did not address that device, so there is no direct route to close and nothing downstream isolates anything",
			r.name, short(peerID))
	}
	if len(forThisPeer) > 1 {
		return "", fmt.Errorf("%s enqueued %d of this send's envelopes for %s — one send addresses a device once, so the harness cannot say which envelope to wait on",
			r.name, len(forThisPeer), short(peerID))
	}
	got := forThisPeer[0]
	if enqueueLines[got] > 1 {
		return "", fmt.Errorf("%s's log carries %d enqueue lines for envelope id %s — ids are shortened on disk and another envelope in this run shares that prefix, so waiting on it could answer for the wrong send",
			r.name, enqueueLines[got], logid.Short(got))
	}
	return got, nil
}

func daemonLogLines(r *rig) ([]string, error) {
	var lines []string
	opened := false
	for _, name := range []string{"haomad.log.prev", "haomad.log"} {
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
		return nil, fmt.Errorf("cannot read %s's daemon log (haomad.log and haomad.log.prev both missing)", r.name)
	}
	return lines, nil
}

func assertGapFillDidTheWork(r *rig, since gapFillMark) error {
	now, err := gapFillMarkOf(r)
	if err != nil {
		return fmt.Errorf("cannot read %s's log to confirm gap-fill ran: %w", r.name, err)
	}
	batches, total := now.batches-since.batches, now.applied-since.applied
	if batches <= 0 {
		return fmt.Errorf("%s never merged a gap-fill batch%s — the sweep did not run at all", r.name, since.suffix())
	}
	if total <= 0 {
		return fmt.Errorf("%s merged %d gap-fill batch(es)%s but applied 0 rows — something else healed it and this gate proves nothing", r.name, batches, since.suffix())
	}
	logf("[%s] gap-fill applied %d row(s) across %d batch(es)%s", r.name, total, batches, since.suffix())
	return nil
}

type gapFillMark struct {
	batches, applied int
}

func (m gapFillMark) suffix() string {
	if m == (gapFillMark{}) {
		return ""
	}
	return fmt.Sprintf(" since the warm-up (which ended at %d batch(es), %d row(s))", m.batches, m.applied)
}

func gapFillMarkOf(r *rig) (gapFillMark, error) {
	f, err := os.Open(filepath.Join(r.root, "haoma.log"))
	if err != nil {
		return gapFillMark{}, err
	}
	defer func() { _ = f.Close() }()

	var m gapFillMark
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.Contains(line, "gapfill: batch merged") {
			continue
		}
		m.batches++
		if n, ok := intField(line, "applied="); ok {
			m.applied += n
		}
	}

	return m, nil
}

func intField(line, key string) (int, bool) {
	i := strings.Index(line, key)
	if i < 0 {
		return 0, false
	}
	rest := line[i+len(key):]
	end := strings.IndexFunc(rest, func(r rune) bool { return r < '0' || r > '9' })
	if end == 0 {
		return 0, false
	}
	if end < 0 {
		end = len(rest)
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0, false
	}
	return n, true
}

func timelineRows(r *rig, peerID string, timeout time.Duration) (map[string]events.Event, error) {
	rows, _, err := timelinePage(r, peerID, timeout)
	return rows, err
}

func timelinePage(r *rig, peerID string, timeout time.Duration) (map[string]events.Event, map[string]int, error) {
	sub, cancel := r.subscribe()
	defer cancel()
	id, err := r.sendReq(ipc.FrameListTimeline, ipc.ListTimelineRequest{PeerID: peerID, Limit: 200})
	if err != nil {
		return nil, nil, fmt.Errorf("list_timeline send: %w", err)
	}

	f, err := awaitFrame(sub, timeout, func(f ipc.Frame) bool {
		return f.ID == id && (f.Type == ipc.FrameTimelinePage || f.Type == ipc.FrameError)
	})
	if err != nil {
		return nil, nil, fmt.Errorf("await timeline_page: %w", err)
	}
	if f.Type == ipc.FrameError {
		return nil, nil, frameErr("list_timeline", f)
	}
	var page ipc.TimelinePageResponse
	if err := json.Unmarshal(f.Payload, &page); err != nil {
		return nil, nil, fmt.Errorf("decode timeline_page: %w", err)
	}
	rows := make(map[string]events.Event, len(page.Events))
	seen := make(map[string]int, len(page.Events))
	for _, raw := range page.Events {
		var ev events.Event
		if json.Unmarshal(raw, &ev) == nil && ev.MsgID != "" {
			seen[ev.MsgID]++
			rows[ev.MsgID] = ev
		}
	}
	return rows, seen, nil
}

func assertOneRowPerMessage(r *rig, peerID string) error {
	_, seen, err := timelinePage(r, peerID, syncConvergeTimeout)
	if err != nil {
		return fmt.Errorf("could not read %s's timeline: %w", r.name, err)
	}
	dupes := []string{}
	for msgID, n := range seen {
		if n > 1 {
			dupes = append(dupes, fmt.Sprintf("%s x%d", short(msgID), n))
		}
	}
	if len(dupes) > 0 {
		sort.Strings(dupes)
		return fmt.Errorf("%s holds %d message(s) more than once (%s) — a mutation resolves one copy and the reader draws the other",
			r.name, len(dupes), strings.Join(dupes, ", "))
	}
	logf("[%s] one row per message across the whole timeline", r.name)
	return nil
}

const backfillTimeout = 180 * time.Second

func assertBackfilled(fresh, peer *rig, wantChatID string) error {
	deadline := time.Now().Add(backfillTimeout)
	var lastSeen int
	for time.Now().Before(deadline) {
		chats, err := listChats(fresh, syncConvergeTimeout)
		if err == nil {
			lastSeen = len(chats)
			for _, c := range chats {
				if c.Fingerprint == "" {
					continue
				}
				if c.PeerID != "" {
					return fmt.Errorf("%s holds a conversation for %s with an edge of its own (%s) — it did not come from the sibling",
						fresh.name, peer.name, short(c.PeerID))
				}
				if c.ChatID != wantChatID {

					return fmt.Errorf("%s adopted chat id %s, but its sibling calls the same conversation %s",
						fresh.name, short(c.ChatID), short(wantChatID))
				}

				if c.Label == "" || c.Label == short(c.Fingerprint) {
					return fmt.Errorf("%s inherited the conversation but not the contact — it renders as %q",
						fresh.name, c.Label)
				}
				return nil
			}
		}
		rigSleep(syncPollInterval, "poll: sync converge")
	}
	return fmt.Errorf("%s never inherited a conversation from its sibling after %s (%d chats present) — "+
		"linking copied an identity and nothing else", fresh.name, backfillTimeout, lastSeen)
}
