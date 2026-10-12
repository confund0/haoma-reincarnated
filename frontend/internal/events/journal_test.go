package events_test

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
)

func textBody(t *testing.T, s string) []byte {
	t.Helper()
	raw, err := json.Marshal(events.TextBody{Text: s})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func journalMsgIDs(t *testing.T, l *events.Log, afterSeq uint64, limit int) []string {
	t.Helper()
	entries, err := l.JournalSince(afterSeq, limit)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.MsgID)
	}
	return out
}

func TestJournal_AppendEarnsAnEntry(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))

	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText,
		MsgID: "m1", EnvelopeID: "e1", Body: textBody(t, "hi"),
	}); err != nil {
		t.Fatal(err)
	}

	entries, err := l.JournalSince(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 journal entry, got %d", len(entries))
	}
	if entries[0].MsgID != "m1" {
		t.Errorf("msg id = %q, want m1", entries[0].MsgID)
	}
	if entries[0].ChatID != chat.ChatID("alice") {
		t.Errorf("chat id = %q, want alice", entries[0].ChatID)
	}
	if entries[0].Seq == 0 {
		t.Error("seq is 0; the key was not decoded back onto the entry")
	}
}

func TestJournal_InboundEarnsAnEntry(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))

	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText,
		MsgID: "in-1", SenderPeerID: "peer-1", SenderTs: 1742643880,
		Status: events.DecryptOK, Body: textBody(t, "yo"),
	}); err != nil {
		t.Fatal(err)
	}

	if got := journalMsgIDs(t, l, 0, 0); len(got) != 1 || got[0] != "in-1" {
		t.Fatalf("journal = %v, want [in-1]", got)
	}
}

func TestJournal_EditUpsertsToAFreshSeq(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))

	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText,
		MsgID: "m1", EnvelopeID: "e1", Body: textBody(t, "first"),
	}); err != nil {
		t.Fatal(err)
	}
	before, err := l.JournalSince(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	appendSeq := before[0].Seq

	if _, err := l.ApplyEdit("m1", "second", 1742643900, ""); err != nil {
		t.Fatal(err)
	}

	after, err := l.JournalSince(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 {
		t.Fatalf("want exactly 1 entry after the edit (upsert, not append), got %d", len(after))
	}
	if after[0].Seq <= appendSeq {
		t.Fatalf("edit seq %d did not advance past the append seq %d — a sibling already past %d would never see the edit",
			after[0].Seq, appendSeq, appendSeq)
	}

	if got := journalMsgIDs(t, l, appendSeq, 0); len(got) != 1 || got[0] != "m1" {
		t.Fatalf("sweep from the append cursor = %v, want [m1]", got)
	}
}

func TestJournal_DeleteUpsertsToAFreshSeq(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))

	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText,
		MsgID: "m1", EnvelopeID: "e1", Body: textBody(t, "secret"),
	}); err != nil {
		t.Fatal(err)
	}
	before, _ := l.JournalSince(0, 0)

	if _, err := l.ApplyDelete("m1", 1742643900, ""); err != nil {
		t.Fatal(err)
	}

	after, err := l.JournalSince(before[0].Seq, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0].MsgID != "m1" {
		t.Fatalf("delete not visible above the append cursor: %v", after)
	}
}

func TestJournal_RepeatedEditsHoldOneEntry(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))

	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText,
		MsgID: "m1", EnvelopeID: "e1", Body: textBody(t, "v0"),
	}); err != nil {
		t.Fatal(err)
	}
	for i, text := range []string{"v1", "v2", "v3"} {
		if _, err := l.ApplyEdit("m1", text, int64(1742643900+i), ""); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := l.JournalSince(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry after 3 edits, got %d — the upsert is appending", len(entries))
	}
}

func TestJournal_ReactionToAnOldMessageIsVisibleToASweep(t *testing.T) {
	const oldTs = 1700000000
	const nowTs = 1742643890

	clock, setClock := movableClock(oldTs)
	l, _ := newLog(t, clock)
	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText,
		MsgID: "old", EnvelopeID: "e1", Body: textBody(t, "months ago"),
	}); err != nil {
		t.Fatal(err)
	}

	setClock(nowTs)
	l2 := l
	if _, err := l2.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText,
		MsgID: "recent", EnvelopeID: "e2", Body: textBody(t, "today"),
	}); err != nil {
		t.Fatal(err)
	}
	cursor, err := l2.JournalHead()
	if err != nil {
		t.Fatal(err)
	}

	react, err := l2.AppendReactionBreadcrumb("old", "👍", "", "react-old", nowTs, 0)
	if err != nil {
		t.Fatal(err)
	}

	if react.DisplayTs != oldTs {
		t.Fatalf("breadcrumb display_ts = %d, want the target's %d — the test's premise changed",
			react.DisplayTs, oldTs)
	}

	got := journalMsgIDs(t, l2, cursor, 0)
	if len(got) != 1 || got[0] != react.MsgID {
		t.Fatalf("sweep above the cursor = %v, want the breadcrumb %q; a display-time sweep would have missed it",
			got, react.MsgID)
	}
}

func TestJournal_ReactionCarriesItsOwnMsgIDNotTheTargets(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))

	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText,
		MsgID: "target-1", EnvelopeID: "e1", Body: textBody(t, "hi"),
	}); err != nil {
		t.Fatal(err)
	}
	react, err := l.AppendReactionBreadcrumb("target-1", "👍", "", "react-1", 1742643900, 0)
	if err != nil {
		t.Fatal(err)
	}
	if react.MsgID != "react-1" {
		t.Fatalf("breadcrumb msg_id = %q, want react-1 (arguments swapped?)", react.MsgID)
	}
	var body events.ReactionBody
	if err := json.Unmarshal(react.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.TargetMsgID != "target-1" {
		t.Fatalf("breadcrumb target = %q, want target-1", body.TargetMsgID)
	}

	got := journalMsgIDs(t, l, 0, 0)
	if len(got) != 2 {
		t.Fatalf("journal = %v, want both the target and the breadcrumb", got)
	}
}

func TestJournal_LateDeliveredMessageIsVisibleToASweep(t *testing.T) {
	const nowTs = 1742643890

	l, _ := newLog(t, fixedClock(nowTs))
	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText,
		MsgID: "recent", EnvelopeID: "e1", Body: textBody(t, "just now"),
	}); err != nil {
		t.Fatal(err)
	}
	cursor, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}

	late, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText,
		MsgID: "late", SenderPeerID: "peer-1", SenderTs: nowTs - 7*24*3600,
		Status: events.DecryptOK, Body: textBody(t, "monday"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if late.DisplayTs >= nowTs {
		t.Fatalf("late row display_ts = %d, expected it clamped below now (%d) — premise changed",
			late.DisplayTs, nowTs)
	}

	if got := journalMsgIDs(t, l, cursor, 0); len(got) != 1 || got[0] != "late" {
		t.Fatalf("sweep above the cursor = %v, want [late]; it sorted below in display order", got)
	}
}

func TestJournal_SinceIsExclusiveAndOrdered(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))

	for i := 0; i < 5; i++ {
		if _, err := l.AppendOutbound(events.OutboundParams{
			ChatID: chat.ChatID("alice"), Kind: events.KindText,
			MsgID: fmt.Sprintf("m%d", i), EnvelopeID: fmt.Sprintf("e%d", i),
			Body: textBody(t, "x"),
		}); err != nil {
			t.Fatal(err)
		}
	}

	all, err := l.JournalSince(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 5 {
		t.Fatalf("want 5, got %d", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i].Seq <= all[i-1].Seq {
			t.Fatalf("entries not ascending: %d after %d", all[i].Seq, all[i-1].Seq)
		}
	}

	tail, err := l.JournalSince(all[2].Seq, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(tail) != 2 || tail[0].MsgID != "m3" {
		t.Fatalf("since is not exclusive: %v", tail)
	}
}

func TestJournal_BatchLimitBounds(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))

	for i := 0; i < 10; i++ {
		if _, err := l.AppendOutbound(events.OutboundParams{
			ChatID: chat.ChatID("alice"), Kind: events.KindText,
			MsgID: fmt.Sprintf("m%d", i), EnvelopeID: fmt.Sprintf("e%d", i),
			Body: textBody(t, "x"),
		}); err != nil {
			t.Fatal(err)
		}
	}

	batch, err := l.JournalSince(0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 3 {
		t.Fatalf("want a batch of 3, got %d", len(batch))
	}

	next, err := l.JournalSince(batch[2].Seq, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 3 || next[0].MsgID != "m3" {
		t.Fatalf("resume from a batch boundary = %v", next)
	}
}

func TestJournal_RowWithoutMsgIDIsSkipped(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))

	if _, err := l.AppendLocal(events.LocalParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindOnionRotation,
		Body: textBody(t, "x"),
	}); err != nil {
		t.Fatal(err)
	}

	entries, err := l.JournalSince(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("want no entries for a msg_id-less row, got %d", len(entries))
	}
}

func TestJournal_HeadAdvancesAndSurvivesPrune(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))

	if head, err := l.JournalHead(); err != nil || head != 0 {
		t.Fatalf("fresh head = %d, %v; want 0, nil", head, err)
	}
	for i := 0; i < 4; i++ {
		if _, err := l.AppendOutbound(events.OutboundParams{
			ChatID: chat.ChatID("alice"), Kind: events.KindText,
			MsgID: fmt.Sprintf("m%d", i), EnvelopeID: fmt.Sprintf("e%d", i),
			Body: textBody(t, "x"),
		}); err != nil {
			t.Fatal(err)
		}
	}
	head, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}
	if head != 4 {
		t.Fatalf("head = %d, want 4", head)
	}

	pruned, err := l.JournalPrune(2)
	if err != nil {
		t.Fatal(err)
	}
	if pruned != 2 {
		t.Fatalf("pruned %d, want 2", pruned)
	}

	if got, err := l.JournalHead(); err != nil || got != 4 {
		t.Fatalf("head after prune = %d, %v; want 4", got, err)
	}
	if got := journalMsgIDs(t, l, 0, 0); len(got) != 2 || got[0] != "m2" {
		t.Fatalf("survivors = %v, want [m2 m3]", got)
	}
}

func TestJournal_PruneLeavesTheReverseIndexForARetouchedRow(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))

	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText,
		MsgID: "m1", EnvelopeID: "e1", Body: textBody(t, "v0"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.ApplyEdit("m1", "v1", 1742643900, ""); err != nil {
		t.Fatal(err)
	}
	head, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := l.JournalPrune(head - 1); err != nil {
		t.Fatal(err)
	}

	if _, err := l.ApplyEdit("m1", "v2", 1742643910, ""); err != nil {
		t.Fatal(err)
	}
	entries, err := l.JournalSince(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 live entry, got %d — prune orphaned the reverse index", len(entries))
	}
}

func TestJournal_ConcurrentWritesGetDistinctAscendingSeqs(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))

	const writers = 8
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := l.AppendOutbound(events.OutboundParams{
				ChatID: chat.ChatID("alice"), Kind: events.KindText,
				MsgID: fmt.Sprintf("m%d", i), EnvelopeID: fmt.Sprintf("e%d", i),
				Body: textBody(t, "x"),
			})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent append failed: %v", err)
	}

	entries, err := l.JournalSince(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != writers {
		t.Fatalf("want %d entries, got %d — a conflict retry dropped a write", writers, len(entries))
	}
	seen := map[uint64]bool{}
	for _, e := range entries {
		if seen[e.Seq] {
			t.Fatalf("duplicate seq %d — two writes shared a sequence", e.Seq)
		}
		seen[e.Seq] = true
	}
}

func movableClock(unix int64) (now func() time.Time, set func(int64)) {
	var mu sync.Mutex
	cur := unix
	return func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return time.Unix(cur, 0)
		}, func(v int64) {
			mu.Lock()
			defer mu.Unlock()
			cur = v
		}
}
