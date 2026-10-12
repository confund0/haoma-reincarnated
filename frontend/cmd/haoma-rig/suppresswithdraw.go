package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
)

const peerAckTimeout = republishWait

var suppressWithdrawVerdicts = []string{
	"suppressed read crosses",
	"suppressed claim never crosses",
	"withdrawal beats an inbound claim",
}

func assertSuppressAndWithdraw(devA, devB, peer *rig, aSeesPeer, bSeesPeer, peerSeesA string) map[string]error {
	out := map[string]error{}
	failAll := func(err error) map[string]error {
		for _, n := range suppressWithdrawVerdicts {
			out[n] = err
		}
		return out
	}

	if err := devicesAtDebugLevel(devA, devB, peer); err != nil {
		return failAll(err)
	}

	aChatID, err := chatIDForPeer(devA, aSeesPeer, syncConvergeTimeout)
	if err != nil {
		return failAll(fmt.Errorf("could not resolve %s's own chat with %s: %w", devA.name, peer.name, err))
	}
	bChatID, err := chatIDForPeer(devB, bSeesPeer, syncConvergeTimeout)
	if err != nil {
		return failAll(fmt.Errorf("could not resolve %s's own chat with %s: %w", devB.name, peer.name, err))
	}

	sup := assertSuppressionNeverCrosses(devA, devB, peer, aChatID, bChatID, bSeesPeer, peerSeesA)
	for name, err := range sup {
		out[name] = err
	}

	if err := awaitChatSettings(devB, bChatID, func(p ipc.ChatSettingsPayload) bool {
		return !p.DisableReadReceipts
	}); err != nil {
		out["withdrawal beats an inbound claim"] = fmt.Errorf(
			"%s never saw %s's receipts restored, so it would suppress the row gate two needs it to claim: %w",
			devB.name, devA.name, err)
		return completeSuppressWithdrawVerdicts(out)
	}

	out["withdrawal beats an inbound claim"] = assertWithdrawalBeatsInboundClaim(
		devA, devB, peer, aChatID, bSeesPeer, peerSeesA)

	return completeSuppressWithdrawVerdicts(out)
}

func assertSuppressionNeverCrosses(devA, devB, peer *rig, aChatID, bChatID, bSeesPeer, peerSeesA string) map[string]error {
	out := map[string]error{}
	fail := func(err error) map[string]error {
		out["suppressed read crosses"] = err
		out["suppressed claim never crosses"] = err
		return out
	}

	logf("[%s] turning read receipts OFF for its conversation with %s", devA.name, peer.name)
	restore, err := disableReadReceipts(devA, aChatID)
	if err != nil {
		return fail(fmt.Errorf("could not turn %s's read receipts off: %w", devA.name, err))
	}

	defer restore()

	preSuppressed, err := countSuppressionLines(devA, aChatID)
	if err != nil {
		return fail(err)
	}

	bSub, bCancel := devB.subscribe()
	defer bCancel()

	d, err := deliverText(peer, devA, peerSeesA, "slice4 suppression "+nextCorrID())
	if err != nil {
		return fail(fmt.Errorf("peer send to %s: %w", devA.name, err))
	}
	msgID := d.msgID
	logf("[%s] suppression row msg_id=%s landed on %s — the same send fans out to %s", peer.name, short(msgID), devA.name, devB.name)

	if _, err := awaitInboundText(bSub, msgID, mirrorTimeout); err != nil {
		return fail(fmt.Errorf("%s never received the row the same send delivered to %s — nothing here has a copy to converge: %w", devB.name, devA.name, err))
	}

	if rows, rErr := timelineRows(devB, bSeesPeer, syncConvergeTimeout); rErr == nil {
		if ev, held := rows[msgID]; held && ev.ReadAt > 0 {
			return fail(fmt.Errorf("%s already shows msg_id=%s read before any sync was driven — this gate no longer isolates the crossing", devB.name, short(msgID)))
		}
	}

	preObserved, err := totalInboundClaimObserved(devB, msgID)
	if err != nil {
		return fail(err)
	}

	if _, err := deviceReadsRow(devA, aChatID, readTimeout); err != nil {
		return fail(fmt.Errorf("mark-read on %s: %w", devA.name, err))
	}

	postSuppressed, err := countSuppressionLines(devA, aChatID)
	if err != nil {
		return fail(err)
	}
	if postSuppressed <= preSuppressed {
		return fail(fmt.Errorf("%s recorded no suppressed receipt for chat %s (%d -> %d) — the toggle did not take, so nothing below is about suppression",
			devA.name, short(aChatID), preSuppressed, postSuppressed))
	}
	nHonest, err := journalTouchCount(devA, events.MutatorMarkReadReceiptSent, msgID)
	if err != nil {
		return fail(err)
	}
	if nHonest != 0 {
		return fail(fmt.Errorf("%s journalled %d honest receipt claim(s) for msg_id=%s with receipts turned off — it acknowledged the peer anyway",
			devA.name, nHonest, short(msgID)))
	}
	logf("[%s] read the row with receipts off: suppression lines %d -> %d, honest claims journalled %d", devA.name, preSuppressed, postSuppressed, nHonest)

	logf("[%s] driving a sync — the read axis has no live push of its own", devB.name)
	if err := driveSync(devB, false); err != nil {
		return fail(fmt.Errorf("could not drive a sync on %s: %w", devB.name, err))
	}

	out["suppressed read crosses"] = func() error {
		if err := awaitRows(devB, bSeesPeer, syncConvergeTimeout, "suppressed read crosses",
			"devB never showed the row read after devA read it with receipts off",
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
		logf("[%s] suppressed read crossed: read time merged (journal touches=%d)", devB.name, n)
		return nil
	}()

	out["suppressed claim never crosses"] = func() error {

		if _, cErr := awaitCountGrew("inbound claim observed on "+devB.name,
			preObserved, syncConvergeTimeout, func() (int, error) {
				return totalInboundClaimObserved(devB, msgID)
			}); cErr != nil {
			return fmt.Errorf("%s never observed msg_id=%s arriving in %s's answer — the sweep did not carry the row, so nothing here is a statement about the sentinel: %w",
				devB.name, short(msgID), devA.name, cErr)
		}
		claimed, unclaimed, cErr := countInboundClaimObserved(devB, msgID)
		if cErr != nil {
			return cErr
		}
		if claimed != 0 {
			return fmt.Errorf("%s observed %s's answer asserting a receipt claim for msg_id=%s %d time(s) — the suppression sentinel left the device that wrote it",
				devB.name, devA.name, short(msgID), claimed)
		}
		logf("[%s] %s's answer carried msg_id=%s with no claim on it (%d observed, none claiming)",
			devB.name, devA.name, short(msgID), unclaimed)

		peerSub, peerCancel := peer.subscribe()
		defer peerCancel()

		if err := awaitChatSettings(devB, bChatID, func(p ipc.ChatSettingsPayload) bool {
			return p.DisableReadReceipts
		}); err != nil {
			return fmt.Errorf("%s never inherited %s's receipts-off toggle, so its silence below would be somebody else's: %w",
				devB.name, devA.name, err)
		}

		preB, cErr := countSuppressionLines(devB, bChatID)
		if cErr != nil {
			return cErr
		}
		if _, err := deviceReadsRow(devB, bChatID, readTimeout); err != nil {
			return fmt.Errorf("mark-read on %s: %w", devB.name, err)
		}
		n, cErr := journalTouchCount(devB, events.MutatorMarkReadReceiptSent, msgID)
		if cErr != nil {
			return cErr
		}
		if n != 0 {
			return fmt.Errorf("%s journalled %d honest receipt claim(s) for msg_id=%s after inheriting %s's receipts-off toggle — one device's decision to stay silent did not reach the other",
				devB.name, n, short(msgID), devA.name)
		}
		postB, cErr := countSuppressionLines(devB, bChatID)
		if cErr != nil {
			return cErr
		}
		if postB <= preB {
			return fmt.Errorf("%s wrote no suppression line of its own for chat %s (%d -> %d) — it is silent, but nothing shows the silence is its own decision rather than an inherited stamp",
				devB.name, short(bChatID), preB, postB)
		}

		if _, err := awaitFrame(peerSub, peerAckTimeout, func(f ipc.Frame) bool {
			ev, ok := timelineEvent(f)
			return ok && ev.MsgID == msgID && ev.DeliveryState == "read"
		}); err == nil {
			return fmt.Errorf("%s saw msg_id=%s reach read though both of my devices have receipts off — somebody acknowledged it",
				peer.name, short(msgID))
		}
		logf("[%s] inherited the toggle and stayed silent: honest claims %d, own suppression lines %d -> %d, and %s never saw the row read",
			devB.name, n, preB, postB, peer.name)
		return nil
	}()

	return out
}

func claimMergeWait(r *rig) (time.Duration, error) {
	if r.giveUp == "" {

		return syncConvergeTimeout, nil
	}
	d, err := time.ParseDuration(r.giveUp)
	if err != nil {
		return 0, fmt.Errorf("%s: -outbox-giveup %q does not parse: %w", r.name, r.giveUp, err)
	}
	wait := d - claimMergeMargin
	if wait < claimMergeFloor {
		return 0, fmt.Errorf("%s: -outbox-giveup %s leaves %s for a sibling to merge a claim, under the %s this gate needs — raise it",
			r.name, d, wait, claimMergeFloor)
	}
	if wait > syncConvergeTimeout {
		return syncConvergeTimeout, nil
	}
	return wait, nil
}

const (
	claimMergeMargin = 20 * time.Second
	claimMergeFloor  = 30 * time.Second
)

func assertWithdrawalBeatsInboundClaim(devA, devB, peer *rig, aChatID, bSeesPeer, peerSeesA string) error {
	bSub, bCancel := devB.subscribe()
	defer bCancel()

	d, err := deliverText(peer, devA, peerSeesA, "slice4 withdrawal "+nextCorrID())
	if err != nil {
		return fmt.Errorf("peer send to %s: %w", devA.name, err)
	}
	msgID := d.msgID
	logf("[%s] withdrawal row msg_id=%s landed on %s — the same send fans out to %s", peer.name, short(msgID), devA.name, devB.name)

	if _, err := awaitInboundText(bSub, msgID, mirrorTimeout); err != nil {
		return fmt.Errorf("%s never received the row the same send delivered to %s — it has no copy to inherit a claim into: %w", devB.name, devA.name, err)
	}
	if rows, rErr := timelineRows(devB, bSeesPeer, syncConvergeTimeout); rErr == nil {
		if ev, held := rows[msgID]; held && ev.ReadAt > 0 {
			return fmt.Errorf("%s already shows msg_id=%s read before anything was driven — this gate no longer isolates the crossing", devB.name, short(msgID))
		}
	}

	logf("[%s] going down — %s's receipt must have somewhere real to fail", peer.name, devA.name)
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

	preMerged, err := countApplyReceiptSentClaimChanged(devB, msgID)
	if err != nil {
		return err
	}
	if preMerged != 0 {
		return fmt.Errorf("%s had already merged a receipt claim for msg_id=%s before %s had even read it (changed=true count=%d) — this gate no longer isolates the crossing",
			devB.name, short(msgID), devA.name, preMerged)
	}
	preObservedA, err := totalInboundClaimObserved(devA, msgID)
	if err != nil {
		return err
	}

	envID, err := deviceReadsRowShipping(devA, aChatID, readTimeout)
	if err != nil {
		return fmt.Errorf("mark-read on %s (doomed receipt): %w", devA.name, err)
	}
	logf("[%s] receipt shipped into a dead end (envelope %s)", devA.name, short(envID))

	first, err := journalTouchCount(devA, events.MutatorMarkReadReceiptSent, msgID)
	if err != nil {
		return err
	}
	if first != 1 {
		return fmt.Errorf("%s journalled its receipt claim %d time(s) for msg_id=%s, want exactly 1 — there is no single claim here for a sibling to inherit", devA.name, first, short(msgID))
	}

	logf("[%s] driving a sync — %s's claim has to land before it can be asked to re-export it", devB.name, devA.name)
	if err := driveSync(devB, false); err != nil {
		return fmt.Errorf("could not drive a sync on %s: %w", devB.name, err)
	}

	mergeWait, err := claimMergeWait(devA)
	if err != nil {
		return err
	}
	inherited, err := awaitCountGrew("receipt claim merged on "+devB.name, preMerged, mergeWait,
		func() (int, error) { return countApplyReceiptSentClaimChanged(devB, msgID) })
	if err != nil {
		return fmt.Errorf("%s did not merge %s's receipt claim for msg_id=%s within %s. "+
			"Either the claim never crossed, or %s withdrew it first — it lives only until its receipt envelope gives up, and this gate makes that happen. "+
			"Check %s's log for the withdrawal before reading this as a merge fault: %w",
			devB.name, devA.name, short(msgID), mergeWait, devA.name, devA.name, err)
	}
	if inherited != 1 {
		return fmt.Errorf("%s merged %s's receipt claim for msg_id=%s %d time(s), want exactly 1 — a claim that can be applied twice is not the sticky latch decision 17 relies on",
			devB.name, devA.name, short(msgID), inherited)
	}
	logf("[%s] inherited %s's receipt claim for msg_id=%s", devB.name, devA.name, short(msgID))

	if _, err := awaitCountGrew("inbound claim observed on "+devA.name,
		preObservedA, syncConvergeTimeout,
		func() (int, error) { return totalInboundClaimObserved(devA, msgID) }); err != nil {
		return fmt.Errorf("%s never observed msg_id=%s arriving in %s's answer — %s cannot be said to have re-exported anything or not: %w",
			devA.name, short(msgID), devB.name, devB.name, err)
	}
	claimedOnA, unclaimedOnA, err := countInboundClaimObserved(devA, msgID)
	if err != nil {
		return err
	}
	if claimedOnA != 0 {
		return fmt.Errorf("%s observed %s asserting a receipt claim for msg_id=%s %d time(s) — %s re-exported a claim it had only inherited, which is the reflection loop",
			devA.name, devB.name, short(msgID), claimedOnA, devB.name)
	}
	logf("[%s] %s's answer carried msg_id=%s with no claim on it (%d observed, none claiming)",
		devA.name, devB.name, short(msgID), unclaimedOnA)

	cleared, err := awaitClearedRows(devA, envID, deadLetterWait)
	if err != nil {
		return err
	}
	if cleared < 1 {
		return fmt.Errorf("%s's give-up was observed but cleared %d row(s) — the claim was never withdrawn, so there is nothing for a sibling's echo to undo", devA.name, cleared)
	}
	logf("[%s] give-up seen, %d row(s) handed back to pending", devA.name, cleared)

	afterWithdraw, err := journalTouchCount(devA, events.MutatorMarkReadReceiptSent, msgID)
	if err != nil {
		return err
	}
	if afterWithdraw != first {
		return fmt.Errorf("%s journalled %d receipt claim(s) for msg_id=%s between shipping and withdrawing, was %d — something re-shipped on its own and this gate can no longer attribute the send below",
			devA.name, afterWithdraw, short(msgID), first)
	}

	if _, err := deviceReadsRow(devA, aChatID, readTimeout); err != nil {
		return fmt.Errorf("mark-read on %s (the retry the withdrawal unblocked): %w", devA.name, err)
	}
	again, err := journalTouchCount(devA, events.MutatorMarkReadReceiptSent, msgID)
	if err != nil {
		return err
	}
	if again != first+1 {
		return fmt.Errorf("%s journalled its receipt claim %d time(s) for msg_id=%s, want %d — the withdrawn row never went back to pending, so the receipt retry can never fire for it again",
			devA.name, again, short(msgID), first+1)
	}
	logf("[%s] withdrawal held: msg_id=%s re-entered the pending set and shipped again (journal touches=%d)", devA.name, short(msgID), again)

	endClaimed, _, err := countInboundClaimObserved(devA, msgID)
	if err != nil {
		return err
	}
	if endClaimed != 0 {
		return fmt.Errorf("%s observed %s asserting a receipt claim for msg_id=%s %d time(s) by the end of this gate — an inherited claim circulated back, even though the retry above still fired",
			devA.name, devB.name, short(msgID), endClaimed)
	}
	return nil
}

func completeSuppressWithdrawVerdicts(out map[string]error) map[string]error {
	for _, n := range suppressWithdrawVerdicts {
		if _, ok := out[n]; !ok {
			out[n] = fmt.Errorf("assertSuppressAndWithdraw: verdict %q was never set — bug in the driver itself", n)
		}
	}
	return out
}

func chatSettings(r *rig, chatID string) (ipc.ChatSettingsPayload, error) {
	sub, cancel := r.subscribe()
	defer cancel()
	id, err := r.sendReq(ipc.FrameGetChatSettings, ipc.GetChatSettingsRequest{ChatID: chatID})
	if err != nil {
		return ipc.ChatSettingsPayload{}, fmt.Errorf("get_chat_settings send: %w", err)
	}
	f, err := awaitFrame(sub, syncConvergeTimeout, func(f ipc.Frame) bool {
		return f.ID == id && (f.Type == ipc.FrameChatSettings || f.Type == ipc.FrameError)
	})
	if err != nil {
		return ipc.ChatSettingsPayload{}, fmt.Errorf("await chat_settings: %w", err)
	}
	if f.Type == ipc.FrameError {
		return ipc.ChatSettingsPayload{}, frameErr("get_chat_settings", f)
	}
	var p ipc.ChatSettingsPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		return ipc.ChatSettingsPayload{}, fmt.Errorf("decode chat_settings: %w", err)
	}
	return p, nil
}

func setChatSettings(r *rig, want ipc.ChatSettingsPayload) error {
	sub, cancel := r.subscribe()
	defer cancel()
	id, err := r.sendReq(ipc.FrameSetChatSettings, ipc.SetChatSettingsRequest(want))
	if err != nil {
		return fmt.Errorf("set_chat_settings send: %w", err)
	}
	f, err := awaitFrame(sub, syncConvergeTimeout, func(f ipc.Frame) bool {
		return f.ID == id && (f.Type == ipc.FrameChatSettings || f.Type == ipc.FrameError)
	})
	if err != nil {
		return fmt.Errorf("await chat_settings (set): %w", err)
	}
	if f.Type == ipc.FrameError {
		return frameErr("set_chat_settings", f)
	}
	return nil
}

func disableReadReceipts(r *rig, chatID string) (restore func(), err error) {
	before, err := chatSettings(r, chatID)
	if err != nil {
		return nil, err
	}
	if before.DisableReadReceipts {
		return nil, fmt.Errorf("%s already has read receipts off for chat %s — something earlier in the run left them off, and this gate cannot tell its own suppression apart from that one", r.name, short(chatID))
	}
	want := before
	want.DisableReadReceipts = true
	if err := setChatSettings(r, want); err != nil {
		return nil, err
	}
	return func() {
		if err := setChatSettings(r, before); err != nil {
			logf("[%s] could not restore read receipts for chat %s: %v", r.name, short(chatID), err)
			return
		}
		logf("[%s] read receipts restored for chat %s", r.name, short(chatID))
	}, nil
}

func deviceReadsRowShipping(r *rig, chatID string, timeout time.Duration) (string, error) {
	sub, cancel := r.subscribe()
	defer cancel()
	id, err := r.sendReq(ipc.FrameMarkRead, ipc.MarkReadRequest{ChatID: chatID})
	if err != nil {
		return "", fmt.Errorf("mark_read send: %w", err)
	}
	shipped, err := awaitFrame(sub, timeout, func(f ipc.Frame) bool {
		return f.Type == ipc.FrameReadSent || (f.ID == id && f.Type == ipc.FrameError)
	})
	if err != nil {
		return "", fmt.Errorf("await read_sent: %w", err)
	}
	if shipped.Type == ipc.FrameError {
		return "", frameErr("mark_read", shipped)
	}
	var p ipc.ReadSentPayload
	if err := json.Unmarshal(shipped.Payload, &p); err != nil {
		return "", fmt.Errorf("decode read_sent: %w", err)
	}
	if p.EnvelopeID == "" {
		return "", fmt.Errorf("%s reported a shipped receipt with no envelope id", r.name)
	}
	if _, err := awaitFrame(sub, timeout, func(f ipc.Frame) bool {
		return f.ID == id && (f.Type == ipc.FrameMarkedRead || f.Type == ipc.FrameError)
	}); err != nil {
		return "", fmt.Errorf("await marked_read: %w", err)
	}
	return p.EnvelopeID, nil
}

func countSuppressionLines(r *rig, chatID string) (int, error) {
	lines, err := rigLogLines(r)
	if err != nil {
		return 0, err
	}
	marker := suppressionMarker
	needle := short(chatID)
	n := 0
	for _, line := range lines {
		if strings.Contains(line, marker) && strings.Contains(line, needle) {
			n++
		}
	}
	return n, nil
}

const (
	inboundClaimMarker = "gapfill: inbound receipt claim observed"
	inboundClaimField  = "claimed"

	suppressionMarker = "read receipts disabled for this chat"
)

func countInboundClaimObserved(r *rig, msgID string) (claimed, unclaimed int, err error) {
	lines, err := rigLogLines(r)
	if err != nil {
		return 0, 0, err
	}
	marker := inboundClaimMarker
	needle := short(msgID)
	for _, line := range lines {
		if !strings.Contains(line, marker) || !strings.Contains(line, needle) {
			continue
		}

		v, ok := logFieldValue(line, inboundClaimField)
		if !ok {
			continue
		}
		if v == "true" {
			claimed++
			continue
		}
		unclaimed++
	}
	return claimed, unclaimed, nil
}

func totalInboundClaimObserved(r *rig, msgID string) (int, error) {
	claimed, unclaimed, err := countInboundClaimObserved(r, msgID)
	if err != nil {
		return 0, err
	}
	return claimed + unclaimed, nil
}

func awaitCountGrew(label string, base int, timeout time.Duration, count func() (int, error)) (int, error) {
	deadline := time.Now().Add(timeout)
	last := base
	for {
		n, err := count()
		if err != nil {
			return 0, err
		}
		last = n
		if n > base {
			return n, nil
		}
		if time.Now().After(deadline) {
			return last, fmt.Errorf("%s did not move within %s (still %d, was %d before the sweep) — the sweep's answer never landed, so nothing it would have carried can be concluded", label, timeout, last, base)
		}
		time.Sleep(syncPollInterval)
	}
}
