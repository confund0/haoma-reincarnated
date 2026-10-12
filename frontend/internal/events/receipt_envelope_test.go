package events_test

import (
	"encoding/json"
	"testing"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
)

func seedReadRow(t *testing.T, l *events.Log, chatID chat.ChatID, msgIDs ...string) []string {
	t.Helper()
	body, _ := json.Marshal(events.TextBody{Text: "hi"})
	for _, id := range msgIDs {
		if _, err := l.AppendInbound(events.InboundParams{
			ChatID: chatID, Kind: events.KindText, SenderTs: 1742643885,
			MsgID: id, Status: events.DecryptOK, Body: body,
		}); err != nil {
			t.Fatal(err)
		}
	}
	_, pending, err := l.MarkRead(chatID)
	if err != nil {
		t.Fatal(err)
	}
	return events.ReceiptMsgIDs(pending)
}

func TestClearReceiptEnvelope_ReturnsRowsToPending(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	const chatID = chat.ChatID("alice")

	pending := seedReadRow(t, l, chatID, "mid-a", "mid-b")
	if len(pending) != 2 {
		t.Fatalf("pending = %v, want 2", pending)
	}
	if err := l.MarkReadReceiptSent(pending, 1742643999); err != nil {
		t.Fatal(err)
	}
	if err := l.RecordReceiptEnvelope("env-rcpt-1", chatID, pending, 1742643999); err != nil {
		t.Fatal(err)
	}

	if _, still, _ := l.MarkRead(chatID); len(still) != 0 {
		t.Fatalf("pending after stamping = %v, want empty", still)
	}

	found, cleared, err := l.ClearReceiptEnvelope("env-rcpt-1")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("mapping not found")
	}
	if cleared != 2 {
		t.Errorf("cleared = %d, want 2", cleared)
	}
	_, back, err := l.MarkRead(chatID)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 2 {
		t.Errorf("pending after clear = %v, want both rows back", back)
	}

	found2, cleared2, err := l.ClearReceiptEnvelope("env-rcpt-1")
	if err != nil {
		t.Fatal(err)
	}
	if found2 || cleared2 != 0 {
		t.Errorf("second clear: found=%v cleared=%d, want false/0", found2, cleared2)
	}
}

func TestClearReceiptEnvelope_LeavesSuppressedRows(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	const chatID = chat.ChatID("alice")

	pending := seedReadRow(t, l, chatID, "mid-a")
	if err := l.MarkReadReceiptSent(pending, events.ReadReceiptSuppressedSentinel); err != nil {
		t.Fatal(err)
	}

	if err := l.RecordReceiptEnvelope("env-stale", chatID, pending, 1742643999); err != nil {
		t.Fatal(err)
	}

	found, cleared, err := l.ClearReceiptEnvelope("env-stale")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("mapping not found")
	}
	if cleared != 0 {
		t.Errorf("cleared = %d, want 0 — a suppressed row must stay suppressed", cleared)
	}
	got, err := l.GetByMsgID("mid-a")
	if err != nil {
		t.Fatal(err)
	}
	if got.ReadReceiptSentAt != events.ReadReceiptSuppressedSentinel {
		t.Errorf("ReadReceiptSentAt = %d, want the suppression sentinel", got.ReadReceiptSentAt)
	}
}

func TestClearReceiptEnvelope_LeavesRestampedRows(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	const chatID = chat.ChatID("alice")

	pending := seedReadRow(t, l, chatID, "mid-a")
	if err := l.MarkReadReceiptSent(pending, 1000); err != nil {
		t.Fatal(err)
	}
	if err := l.RecordReceiptEnvelope("env-old", chatID, pending, 1000); err != nil {
		t.Fatal(err)
	}

	if _, _, err := l.ClearReceiptEnvelope("env-old"); err != nil {
		t.Fatal(err)
	}
	if err := l.MarkReadReceiptSent([]string{"mid-a"}, 2000); err != nil {
		t.Fatal(err)
	}
	if err := l.RecordReceiptEnvelope("env-old", chatID, pending, 1000); err != nil {
		t.Fatal(err)
	}

	_, cleared, err := l.ClearReceiptEnvelope("env-old")
	if err != nil {
		t.Fatal(err)
	}
	if cleared != 0 {
		t.Errorf("cleared = %d, want 0 — the row belongs to a newer receipt", cleared)
	}
	got, _ := l.GetByMsgID("mid-a")
	if got.ReadReceiptSentAt != 2000 {
		t.Errorf("ReadReceiptSentAt = %d, want 2000 (newer stamp preserved)", got.ReadReceiptSentAt)
	}
}

func TestDropAndListReceiptEnvelopes(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	const chatID = chat.ChatID("alice")

	pending := seedReadRow(t, l, chatID, "mid-a")
	if err := l.MarkReadReceiptSent(pending, 1742643999); err != nil {
		t.Fatal(err)
	}
	if err := l.RecordReceiptEnvelope("env-ok", chatID, pending, 1742643999); err != nil {
		t.Fatal(err)
	}

	list, err := l.ListReceiptEnvelopes()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].EnvelopeID != "env-ok" || len(list[0].MsgIDs) != 1 {
		t.Fatalf("list = %+v, want one mapping covering one row", list)
	}

	found, err := l.DropReceiptEnvelope("env-ok")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Error("drop reported not-found for an existing mapping")
	}

	found2, err := l.DropReceiptEnvelope("env-some-text")
	if err != nil {
		t.Fatal(err)
	}
	if found2 {
		t.Error("drop reported found for an envelope that was never a receipt")
	}
	list2, _ := l.ListReceiptEnvelopes()
	if len(list2) != 0 {
		t.Errorf("list after drop = %+v, want empty", list2)
	}
}

func TestOutboxReconcileWatermark_NeverMovesBackwards(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))

	at, err := l.OutboxReconcileWatermark()
	if err != nil || at != 0 {
		t.Fatalf("fresh watermark = %d, err = %v, want 0/nil", at, err)
	}
	if err := l.SetOutboxReconcileWatermark(5000); err != nil {
		t.Fatal(err)
	}
	if err := l.SetOutboxReconcileWatermark(3000); err != nil {
		t.Fatal(err)
	}
	at, err = l.OutboxReconcileWatermark()
	if err != nil {
		t.Fatal(err)
	}
	if at != 5000 {
		t.Errorf("watermark = %d, want 5000 — an emptier pass must not rewind it", at)
	}
}
