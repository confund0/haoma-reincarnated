package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"haoma-frontend/internal/calls"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/syncrail"
)

func endedCall(t *testing.T, chatID chat.ChatID, peerID string, dir calls.Direction) calls.State {
	t.Helper()
	id, err := calls.NewCallID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	return calls.State{
		CallID:     id,
		ChatID:     chatID,
		PeerID:     peerID,
		Direction:  dir,
		Status:     calls.StatusEnded,
		Modalities: []string{"audio"},
		StartedAt:  now - 60,
		AcceptedAt: now - 50,
		EndedAt:    now,
	}
}

func callSummaryRows(t *testing.T, d *daemon, chatID chat.ChatID) []events.Event {
	t.Helper()
	rows, err := d.events.List(chatID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []events.Event
	for _, ev := range rows {
		if ev.Kind == events.KindCallSummary {
			out = append(out, ev)
		}
	}
	return out
}

func sealCallSummary(t *testing.T, d *daemon, body syncrail.CallSummaryBody) []byte {
	t.Helper()
	return sealGapEntry(t, d, railSiblingID, syncrail.KindCallSummary, body)
}

func outgoingSummary(t *testing.T, callID, direction string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(events.CallSummaryBody{
		CallID:          callID,
		Direction:       direction,
		Outcome:         events.CallOutcomeCompleted,
		DurationSeconds: 50,
		Modalities:      []string{"audio"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestCallSummary_OutgoingCrumbGoesToMySiblingOnly(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)

	state := endedCall(t, chatID, railPeerID, calls.DirOut)
	writeCallSummaryBreadcrumb(d, state)
	stub.WaitForSend(t, 1, 5*time.Second)

	rows := callSummaryRows(t, d, chatID)
	if len(rows) != 1 {
		t.Fatalf("%d call_summary rows here, want 1", len(rows))
	}
	local := rows[0]
	if local.MsgID == "" {
		t.Fatal("the crumb of a call I made has no msg id; my other device cannot name it")
	}
	if local.Direction != events.DirOut {
		t.Fatalf("direction = %q, want out", local.Direction)
	}

	time.Sleep(200 * time.Millisecond)
	stub.sendMu.Lock()
	peers := append([]string(nil), stub.SentPeerIDs...)
	payloads := append([][]byte(nil), stub.SentPayloads...)
	kinds := append([]string(nil), stub.SentKinds...)
	stub.sendMu.Unlock()
	if len(peers) != 1 || peers[0] != railSiblingID {
		t.Fatalf("sent to %v, want only my other device %s", peers, railSiblingID)
	}
	if kinds[0] != "" {
		t.Fatalf("wire-kind hint leaked to the router: %q", kinds[0])
	}

	env := openGapEnvelope(t, d, payloads[0])
	if env.Kind != syncrail.KindCallSummary {
		t.Fatalf("sent rail kind %q, want %q", env.Kind, syncrail.KindCallSummary)
	}
	body, err := syncrail.ParseCallSummary(env)
	if err != nil {
		t.Fatal(err)
	}
	if body.MsgID != local.MsgID {
		t.Errorf("sent msg id %q, the row here has %q", body.MsgID, local.MsgID)
	}
	if body.Fingerprint != railFingerprint {
		t.Errorf("fingerprint = %q, want the contact's %q", body.Fingerprint, railFingerprint)
	}
	if body.DisplayTs != local.DisplayTs || body.ExpireSeconds != local.ExpireSeconds {
		t.Errorf("display_ts %d / expire %d sent, row has %d / %d", body.DisplayTs, body.ExpireSeconds, local.DisplayTs, local.ExpireSeconds)
	}
	if string(body.Summary) != string(local.Body) {
		t.Errorf("summary sent\n%s\nrow body\n%s", body.Summary, local.Body)
	}
}

func TestCallSummary_IncomingCrumbIsNeverSent(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)

	writeCallSummaryBreadcrumb(d, endedCall(t, chatID, railPeerID, calls.DirIn))

	rows := callSummaryRows(t, d, chatID)
	if len(rows) != 1 {
		t.Fatalf("%d call_summary rows here, want 1", len(rows))
	}
	if rows[0].Direction != events.DirIn {
		t.Fatalf("direction = %q, want in", rows[0].Direction)
	}
	if rows[0].MsgID != "" {
		t.Errorf("the crumb of an incoming call has msg id %q; it must stay unnameable on other devices", rows[0].MsgID)
	}
	time.Sleep(300 * time.Millisecond)
	stub.sendMu.Lock()
	sent := stub.SendCalls
	stub.sendMu.Unlock()
	if sent != 0 {
		t.Fatalf("%d sends for the crumb of an incoming call, want none", sent)
	}
}

func TestRail_CallSummary_AppliesAsAnOutgoingRowWithTheSameMsgID(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)

	summary := outgoingSummary(t, "call-1", "out")
	recent := time.Now().Unix() - 30
	processInboxEntry(context.Background(), d, railEntry(railSiblingID, "env-cs-1", sealCallSummary(t, d, syncrail.CallSummaryBody{
		Fingerprint:   railFingerprint,
		MsgID:         "crumb-1",
		DisplayTs:     recent,
		ExpireSeconds: 3600,
		Summary:       summary,
	})))

	got, err := d.events.GetByMsgID("crumb-1")
	if err != nil {
		t.Fatalf("pushed crumb not written: %v", err)
	}
	if got.ChatID != chatID || got.Kind != events.KindCallSummary || got.Direction != events.DirOut {
		t.Fatalf("row = chat %s kind %s dir %s; want an outgoing call_summary in %s", got.ChatID, got.Kind, got.Direction, chatID)
	}
	if string(got.Body) != string(summary) {
		t.Errorf("body = %s, want %s", got.Body, summary)
	}
	if got.DisplayTs != recent {
		t.Errorf("display_ts = %d, want the origin's %d", got.DisplayTs, recent)
	}
	if got.ExpireSeconds != 3600 {
		t.Errorf("expire_seconds = %d, want 3600", got.ExpireSeconds)
	}
	if got.SenderPeerID != "" {
		t.Errorf("sender peer id = %q; a crumb of my own call has none", got.SenderPeerID)
	}

}

func TestCallSummary_PushAndGapFillKeepTheOriginPosition(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	seedKnownPeer(t, d, railPeerID, railFingerprint)

	old := time.Now().Unix() - 3*3600
	ahead := time.Now().Unix() + 2*3600
	for i, ts := range []int64{old, ahead} {
		pushID := []string{"crumb-old-push", "crumb-ahead-push"}[i]
		gapID := []string{"crumb-old-gap", "crumb-ahead-gap"}[i]
		processInboxEntry(context.Background(), d, railEntry(railSiblingID, "env-"+pushID, sealCallSummary(t, d, syncrail.CallSummaryBody{
			Fingerprint: railFingerprint,
			MsgID:       pushID,
			DisplayTs:   ts,
			Summary:     outgoingSummary(t, "call-"+pushID, "out"),
		})))
		handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{
			RangeEnd: uint64(i + 1),
			Rows: []syncrail.RowSnapshot{{
				Fingerprint: railFingerprint,
				MsgID:       gapID,
				Kind:        string(events.KindCallSummary),
				DisplayTs:   ts,
				Body:        outgoingSummary(t, "call-"+gapID, "out"),
			}},
		})
		pushed, err := d.events.GetByMsgID(pushID)
		if err != nil {
			t.Fatalf("pushed crumb %s not written: %v", pushID, err)
		}
		swept, err := d.events.GetByMsgID(gapID)
		if err != nil {
			t.Fatalf("gap-fill crumb %s not written: %v", gapID, err)
		}
		if pushed.DisplayTs != ts || swept.DisplayTs != ts {
			t.Errorf("display_ts: push %d, gap-fill %d; want both at the origin's %d", pushed.DisplayTs, swept.DisplayTs, ts)
		}
	}
}

func TestCallSummary_ExpiredCopyIsNotWritten(t *testing.T) {
	buf := captureRailLog(t)
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)
	now := time.Now().Unix()

	push := func(msgID string, displayTs int64) {
		processInboxEntry(context.Background(), d, railEntry(railSiblingID, "env-"+msgID, sealCallSummary(t, d, syncrail.CallSummaryBody{
			Fingerprint: railFingerprint, MsgID: msgID, DisplayTs: displayTs, ExpireSeconds: 60,
			Summary: outgoingSummary(t, "call-"+msgID, "out"),
		})))
	}
	push("cs-expired-push", now-120)
	push("cs-live-push", now-30)
	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{
		RangeEnd: 9,
		Rows: []syncrail.RowSnapshot{
			{Fingerprint: railFingerprint, MsgID: "cs-expired-gap", Kind: string(events.KindCallSummary),
				DisplayTs: now - 120, ExpireSeconds: 60, Body: outgoingSummary(t, "call-gap-1", "out")},
			{Fingerprint: railFingerprint, MsgID: "cs-live-gap", Kind: string(events.KindCallSummary),
				DisplayTs: now - 30, ExpireSeconds: 60, Body: outgoingSummary(t, "call-gap-2", "out")},
		},
	})

	for _, id := range []string{"cs-expired-push", "cs-expired-gap"} {
		if _, err := d.events.GetByMsgID(id); err == nil {
			t.Errorf("%s was written after its retention ran out", id)
		}
	}
	for _, id := range []string{"cs-live-push", "cs-live-gap"} {
		if _, err := d.events.GetByMsgID(id); err != nil {
			t.Errorf("%s, still inside its retention, was not written: %v", id, err)
		}
	}
	if rows := callSummaryRows(t, d, chatID); len(rows) != 2 {
		t.Errorf("%d call_summary rows, want the 2 live ones", len(rows))
	}
	waitForLog(t, buf, "rail: call summary already expired", "cs-expired-push", "noop_expired")
	waitForLog(t, buf, "gapfill: call summary already expired", "cs-expired-gap", "noop_expired")
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, "level=WARN") || strings.Contains(line, "level=ERROR") {
			t.Errorf("an expired copy logged above debug: %s", line)
		}
	}
	if wm, _ := d.watermarks.Get(railSiblingID); wm != 9 {
		t.Errorf("watermark = %d, want 9", wm)
	}
}

func TestRail_CallSummary_DuplicateIsDroppedSilently(t *testing.T) {
	buf := captureRailLog(t)
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)

	body := syncrail.CallSummaryBody{
		Fingerprint: railFingerprint,
		MsgID:       "crumb-dup",
		DisplayTs:   time.Now().Unix(),
		Summary:     outgoingSummary(t, "call-dup", "out"),
	}
	processInboxEntry(context.Background(), d, railEntry(railSiblingID, "env-dup-1", sealCallSummary(t, d, body)))
	processInboxEntry(context.Background(), d, railEntry(railSiblingID, "env-dup-2", sealCallSummary(t, d, body)))

	if rows := callSummaryRows(t, d, chatID); len(rows) != 1 {
		t.Fatalf("%d call_summary rows after the same crumb twice, want 1", len(rows))
	}
	waitForLog(t, buf, "rail: call summary already held", "noop_duplicate")
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, "level=WARN") || strings.Contains(line, "level=ERROR") {
			t.Errorf("the duplicate logged above debug: %s", line)
		}
	}
}

func TestRail_CallSummary_IncomingCrumbIsRefused(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)

	processInboxEntry(context.Background(), d, railEntry(railSiblingID, "env-cs-in", sealCallSummary(t, d, syncrail.CallSummaryBody{
		Fingerprint: railFingerprint,
		MsgID:       "crumb-in",
		DisplayTs:   time.Now().Unix(),
		Summary:     outgoingSummary(t, "call-in", "in"),
	})))
	if rows := callSummaryRows(t, d, chatID); len(rows) != 0 {
		t.Fatalf("%d call_summary rows from an incoming crumb, want none", len(rows))
	}
}

func TestRail_CallSummary_UnknownFingerprintCreatesNothing(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)

	processInboxEntry(context.Background(), d, railEntry(railSiblingID, "env-cs-unknown", sealCallSummary(t, d, syncrail.CallSummaryBody{
		Fingerprint: "ffffffffffffffffffffffffffffffff",
		MsgID:       "crumb-unknown",
		DisplayTs:   time.Now().Unix(),
		Summary:     outgoingSummary(t, "call-unknown", "out"),
	})))
	if _, err := d.events.GetByMsgID("crumb-unknown"); err == nil {
		t.Fatal("a crumb for an unknown person was written")
	}
	if chatExistsForPeer(d, railSiblingID) {
		t.Fatal("a chat was created for the sibling")
	}
}

func TestSnapshotOf_CallSummaryOnlyOutgoingWithAMsgID(t *testing.T) {
	summary := outgoingSummary(t, "call-snap", "out")
	out := events.Event{
		ChatID: "c", Kind: events.KindCallSummary, Direction: events.DirOut,
		MsgID: "crumb-snap", DisplayTs: 1742600000, ExpireSeconds: 600, Body: summary,
	}
	snap, ok := snapshotOf(out, "fp-alice")
	if !ok {
		t.Fatal("the crumb of a call I made is not shipped")
	}
	if snap.Kind != string(events.KindCallSummary) || snap.Inbound || snap.MsgID != "crumb-snap" ||
		snap.DisplayTs != 1742600000 || snap.ExpireSeconds != 600 || string(snap.Body) != string(summary) {
		t.Fatalf("snapshot = %+v", snap)
	}

	in := out
	in.Direction = events.DirIn
	in.MsgID = "crumb-in"
	if _, ok := snapshotOf(in, "fp-alice"); ok {
		t.Fatal("the crumb of an incoming call is shipped")
	}
	noID := out
	noID.MsgID = ""
	if _, ok := snapshotOf(noID, "fp-alice"); ok {
		t.Fatal("a crumb with no msg id is shipped")
	}
}

func TestGapFill_IncomingCallSummaryRowIsRefused(t *testing.T) {
	buf := captureRailLog(t)
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)

	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{
		RangeEnd: 5,
		Rows: []syncrail.RowSnapshot{{
			Fingerprint: railFingerprint,
			MsgID:       "crumb-gap-in",
			Kind:        string(events.KindCallSummary),
			Inbound:     true,
			DisplayTs:   1742600000,
			Body:        outgoingSummary(t, "call-gap-in", "in"),
		}},
	})
	if rows := callSummaryRows(t, d, chatID); len(rows) != 0 {
		t.Fatalf("%d call_summary rows from an incoming gap-fill row, want none", len(rows))
	}
	waitForLog(t, buf, "gapfill: row not applied", "crumb-gap-in", errSyncedCallSummaryNotOutgoing.Error())
	if wm, _ := d.watermarks.Get(railSiblingID); wm != 5 {
		t.Fatalf("watermark = %d, want 5", wm)
	}
}

func TestGapFill_OutgoingCallSummaryConvergesAcrossAnOfflineGap(t *testing.T) {
	stubA := startHaomadStub(t, nil, http.StatusOK)
	devA, _, _, _ := newTestDaemon(t, stubA)
	stubB := startHaomadStub(t, nil, http.StatusOK)
	devB, _, _, _ := newTestDaemon(t, stubB)
	adoptIdentityFrom(t, devB, devA)

	const peerOnA = "0000000000000000000000000000aaaa"
	const peerOnB = "0000000000000000000000000000bbbb"
	const sibOnA = "0000000000000000000000000000b0b0"
	const sibOnB = "0000000000000000000000000000a0a0"
	chatA := seedKnownPeer(t, devA, peerOnA, railFingerprint)
	chatB := seedKnownPeer(t, devB, peerOnB, railFingerprint)

	outState := endedCall(t, chatA, peerOnA, calls.DirOut)
	writeCallSummaryBreadcrumb(devA, outState)
	writeCallSummaryBreadcrumb(devA, endedCall(t, chatA, peerOnA, calls.DirIn))
	var outRow events.Event
	for _, ev := range callSummaryRows(t, devA, chatA) {
		if ev.Direction == events.DirOut {
			outRow = ev
		}
	}
	if outRow.MsgID == "" {
		t.Fatal("precondition: A holds no outgoing crumb with a msg id")
	}
	linkSibling(t, devA, sibOnA)
	linkSibling(t, devB, sibOnB)

	ctx := context.Background()
	sweep := func(full bool) syncrail.GapResponseBody {
		askSibling(ctx, devB, sibOnB, false, full)
		stubB.WaitForSend(t, 1, 5*time.Second)
		processSiblingRailEnvelope(ctx, devA, railEntry(sibOnA, "req", stubB.LastSendReq.Payload))
		stubA.WaitForSend(t, 1, 5*time.Second)
		resp, err := syncrail.ParseGapResponse(openGapEnvelope(t, devB, stubA.LastSendReq.Payload))
		if err != nil {
			t.Fatal(err)
		}
		handleGapResponse(ctx, devB, sibOnB, resp)
		return resp
	}
	resp := sweep(false)
	var shipped []string
	for _, r := range resp.Rows {
		if r.Kind == string(events.KindCallSummary) {
			shipped = append(shipped, r.MsgID)
			if r.Inbound {
				t.Errorf("the answer ships an incoming crumb %s", r.MsgID)
			}
		}
	}
	if len(shipped) != 1 || shipped[0] != outRow.MsgID {
		t.Fatalf("answer ships call summaries %v, want only %s", shipped, outRow.MsgID)
	}

	rows := callSummaryRows(t, devB, chatB)
	if len(rows) != 1 {
		t.Fatalf("B holds %d call_summary rows, want the one outgoing crumb", len(rows))
	}
	got := rows[0]
	if got.MsgID != outRow.MsgID || got.Direction != events.DirOut {
		t.Fatalf("B's crumb = msg id %q dir %s, want %q out", got.MsgID, got.Direction, outRow.MsgID)
	}
	if got.DisplayTs != outRow.DisplayTs {
		t.Errorf("display_ts = %d, want the origin's %d", got.DisplayTs, outRow.DisplayTs)
	}
	if string(got.Body) != string(outRow.Body) {
		t.Errorf("body = %s, want %s", got.Body, outRow.Body)
	}

	if resp := sweep(true); len(resp.Rows) == 0 {
		t.Fatal("the full sweep shipped nothing; the second apply was not tested")
	}
	if rows := callSummaryRows(t, devB, chatB); len(rows) != 1 {
		t.Fatalf("B holds %d call_summary rows after a second sweep, want 1", len(rows))
	}
}
