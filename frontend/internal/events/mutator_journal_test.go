package events_test

import (
	"encoding/json"
	"sync"
	"testing"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
)

func TestMarkRead_StampingAnUnreadRowJournalsIt(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	chatID := chat.ChatID("alice")

	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chatID, Kind: events.KindText,
		MsgID: "in-1", SenderPeerID: "peer-1", SenderTs: 1742643880,
		Status: events.DecryptOK, Body: textBody(t, "yo"),
	}); err != nil {
		t.Fatal(err)
	}
	beforeHead, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}
	if beforeHead != 1 {
		t.Fatalf("head after append = %d, want 1", beforeHead)
	}

	mutated, pending, err := l.MarkRead(chatID)
	if err != nil {
		t.Fatal(err)
	}
	if mutated != 1 {
		t.Fatalf("mutated = %d, want 1", mutated)
	}
	if len(pending) != 1 || pending[0].MsgID != "in-1" {
		t.Fatalf("pendingReceipt = %v, want [in-1]", pending)
	}

	afterHead, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}
	if afterHead != 2 {
		t.Fatalf("head after MarkRead = %d, want 2 (upsert to a fresh seq) — the mutator did not journal", afterHead)
	}
	entries, err := l.JournalSince(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].MsgID != "in-1" || entries[0].Seq != afterHead {
		t.Fatalf("journal = %+v, want exactly one entry for in-1 at seq %d", entries, afterHead)
	}
}

func TestMarkRead_AlreadyReadRowWithPendingReceiptDoesNotReJournal(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	chatID := chat.ChatID("alice")

	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chatID, Kind: events.KindText,
		MsgID: "in-1", SenderPeerID: "peer-1", SenderTs: 1742643880,
		Status: events.DecryptOK, Body: textBody(t, "yo"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.MarkRead(chatID); err != nil {
		t.Fatal(err)
	}
	midHead, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}

	mutated, pending, err := l.MarkRead(chatID)
	if err != nil {
		t.Fatal(err)
	}
	if mutated != 0 {
		t.Fatalf("second call mutated = %d, want 0 (already read)", mutated)
	}
	if len(pending) != 1 {
		t.Fatalf("second call pendingReceipt = %v, want the row still owing a receipt", pending)
	}

	afterHead, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}
	if afterHead != midHead {
		t.Fatalf("head after a no-op MarkRead = %d, want unchanged from %d — an idempotent call must not journal",
			afterHead, midHead)
	}
}

func TestMarkRead_ConcurrentEditIsNeverClobbered(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	chatID := chat.ChatID("alice")

	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chatID, Kind: events.KindText,
		MsgID: "m1", SenderPeerID: "peer-1", SenderTs: 1742643880,
		Status: events.DecryptOK, Body: textBody(t, "v0"),
	}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if _, _, err := l.MarkRead(chatID); err != nil {
			t.Error(err)
		}
	}()
	go func() {
		defer wg.Done()

		if _, err := l.ApplyEdit("m1", "v1", 1742643900, "peer-1"); err != nil {
			t.Error(err)
		}
	}()
	wg.Wait()

	got, err := l.GetByMsgID("m1")
	if err != nil {
		t.Fatal(err)
	}
	var body events.TextBody
	if err := json.Unmarshal(got.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Text != "v1" || got.EditedAt != 1742643900 {
		t.Fatalf("row = {text:%q edited_at:%d}, want {v1 1742643900} — a concurrent MarkRead clobbered the edit",
			body.Text, got.EditedAt)
	}
	if got.ReadAt == 0 {
		t.Fatal("read_at = 0, want stamped — MarkRead's own write was lost")
	}
}

func TestApplyReadReceipt_JournalsTheOutboundRow(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	chatID := chat.ChatID("alice")

	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chatID, Kind: events.KindText,
		MsgID: "m1", EnvelopeID: "e1", Body: textBody(t, "hi"),
	}); err != nil {
		t.Fatal(err)
	}
	beforeHead, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := l.ApplyReadReceipt("m1", 1742643900, chatID); err != nil {
		t.Fatal(err)
	}

	afterHead, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}
	if afterHead <= beforeHead {
		t.Fatalf("head after ApplyReadReceipt = %d, did not advance past %d — the receipt was not journalled",
			afterHead, beforeHead)
	}
	entries, err := l.JournalSince(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].MsgID != "m1" || entries[0].Seq != afterHead {
		t.Fatalf("journal = %+v, want exactly one entry for m1 at seq %d", entries, afterHead)
	}
}

func TestApplyReadReceipt_DuplicateReceiptDoesNotReJournal(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	chatID := chat.ChatID("alice")

	if _, err := l.AppendOutbound(events.OutboundParams{
		ChatID: chatID, Kind: events.KindText,
		MsgID: "m1", EnvelopeID: "e1", Body: textBody(t, "hi"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.ApplyReadReceipt("m1", 1742643900, chatID); err != nil {
		t.Fatal(err)
	}
	midHead, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := l.ApplyReadReceipt("m1", 1742643900, chatID); err != nil {
		t.Fatal(err)
	}

	afterHead, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}
	if afterHead != midHead {
		t.Fatalf("head after a duplicate receipt = %d, want unchanged from %d — a no-op apply must not journal",
			afterHead, midHead)
	}
}

func TestMarkReadReceiptSent_HonestSendJournals(t *testing.T) {
	if c, ok := events.Classify(events.MutatorMarkReadReceiptSent); !ok || c != events.ClassJournal {
		t.Fatalf("Classify(MutatorMarkReadReceiptSent) = (%v, %v), want (ClassJournal, true)", c, ok)
	}

	l, _ := newLog(t, fixedClock(1742643890))
	chatID := chat.ChatID("alice")

	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chatID, Kind: events.KindText,
		MsgID: "in-1", SenderPeerID: "peer-1", SenderTs: 1742643880,
		Status: events.DecryptOK, Body: textBody(t, "yo"),
	}); err != nil {
		t.Fatal(err)
	}
	beforeHead, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}

	if err := l.MarkReadReceiptSent([]string{"in-1"}, 1742643900); err != nil {
		t.Fatal(err)
	}

	afterHead, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}
	if afterHead != beforeHead+1 {
		t.Fatalf("head after an honest MarkReadReceiptSent = %d, want %d — the claim must reach a sibling's sweep",
			afterHead, beforeHead+1)
	}

	got, err := l.GetByMsgID("in-1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.ReceiptClaimOrigin {
		t.Error("an honest send did not stamp ReceiptClaimOrigin — snapshotOf would refuse to ship it")
	}
}

func TestMarkReadReceiptSent_SuppressionSentinelDoesNotClaimOrigin(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	chatID := chat.ChatID("alice")

	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chatID, Kind: events.KindText,
		MsgID: "in-1", SenderPeerID: "peer-1", SenderTs: 1742643880,
		Status: events.DecryptOK, Body: textBody(t, "yo"),
	}); err != nil {
		t.Fatal(err)
	}
	beforeHead, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}

	if err := l.MarkReadReceiptSent([]string{"in-1"}, events.ReadReceiptSuppressedSentinel); err != nil {
		t.Fatal(err)
	}

	got, err := l.GetByMsgID("in-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ReceiptClaimOrigin {
		t.Error("the suppression sentinel claimed origin — a disabled chat's choice would ship as a real claim")
	}

	afterHead, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}
	if afterHead != beforeHead {
		t.Fatalf("head after a suppression-sentinel MarkReadReceiptSent = %d, want unchanged from %d — "+
			"it is classified ClassExempt", afterHead, beforeHead)
	}
}

func TestSuppressPendingReadReceipts_DoesNotJournal(t *testing.T) {
	if c, ok := events.Classify(events.MutatorSuppressPendingReadReceipts); !ok || c != events.ClassExempt {
		t.Fatalf("Classify(MutatorSuppressPendingReadReceipts) = (%v, %v), want (ClassExempt, true)", c, ok)
	}

	l, _ := newLog(t, fixedClock(1742643890))
	chatID := chat.ChatID("alice")

	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chatID, Kind: events.KindText,
		MsgID: "in-1", SenderPeerID: "peer-1", SenderTs: 1742643880,
		Status: events.DecryptOK, Body: textBody(t, "yo"),
	}); err != nil {
		t.Fatal(err)
	}
	beforeHead, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := l.SuppressPendingReadReceipts(chatID); err != nil {
		t.Fatal(err)
	}

	afterHead, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}
	if afterHead != beforeHead {
		t.Fatalf("head after SuppressPendingReadReceipts = %d, want unchanged from %d — it is classified ClassExempt",
			afterHead, beforeHead)
	}
}
