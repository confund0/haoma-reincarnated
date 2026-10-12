package events

import (
	"encoding/json"
	"testing"

	"haoma-frontend/internal/chat"
)

func withMutatorClass(t *testing.T, m Mutator, class MutatorClass) {
	t.Helper()
	orig, had := mutatorRegistry[m]
	mutatorRegistry[m] = class
	t.Cleanup(func() {
		if had {
			mutatorRegistry[m] = orig
		} else {
			delete(mutatorRegistry, m)
		}
	})
}

func routingTextBody(t *testing.T, s string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(TextBody{Text: s})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func requireJournalEntry(t *testing.T, l *Log, afterSeq uint64, chatID chat.ChatID, msgID string) {
	t.Helper()
	entries, err := l.JournalSince(afterSeq, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ChatID != chatID || entries[0].MsgID != msgID {
		t.Fatalf("journal entries after seq %d = %+v, want exactly one entry for chat %q msg %q",
			afterSeq, entries, chatID, msgID)
	}
}

func TestRouting_Append_ExemptStopsJournalling(t *testing.T) {
	st := newTestStore(t)
	l := New(st, NewBus(), nil)
	withMutatorClass(t, MutatorAppend, ClassExempt)

	if _, err := l.AppendOutbound(OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: KindText,
		MsgID: "m1", EnvelopeID: "e1", Body: routingTextBody(t, "hi"),
	}); err != nil {
		t.Fatal(err)
	}
	head, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}
	if head != 0 {
		t.Fatalf("journal head = %d, want 0 — Append ignored its ClassExempt override", head)
	}
}

func TestRouting_ApplyEdit_ExemptStopsJournalling(t *testing.T) {
	st := newTestStore(t)
	l := New(st, NewBus(), nil)

	if _, err := l.AppendOutbound(OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: KindText,
		MsgID: "m1", EnvelopeID: "e1", Body: routingTextBody(t, "v0"),
	}); err != nil {
		t.Fatal(err)
	}
	beforeHead, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}

	withMutatorClass(t, MutatorApplyEdit, ClassExempt)
	if _, err := l.ApplyEdit("m1", "v1", 1742643900, ""); err != nil {
		t.Fatal(err)
	}

	afterHead, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}
	if afterHead != beforeHead {
		t.Fatalf("journal head = %d, want unchanged from %d — ApplyEdit ignored its ClassExempt override", afterHead, beforeHead)
	}
}

func TestRouting_ApplyDelete_ExemptStopsJournalling(t *testing.T) {
	st := newTestStore(t)
	l := New(st, NewBus(), nil)

	if _, err := l.AppendOutbound(OutboundParams{
		ChatID: chat.ChatID("alice"), Kind: KindText,
		MsgID: "m1", EnvelopeID: "e1", Body: routingTextBody(t, "v0"),
	}); err != nil {
		t.Fatal(err)
	}
	beforeHead, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}

	withMutatorClass(t, MutatorApplyDelete, ClassExempt)
	if _, err := l.ApplyDelete("m1", 1742643900, ""); err != nil {
		t.Fatal(err)
	}

	afterHead, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}
	if afterHead != beforeHead {
		t.Fatalf("journal head = %d, want unchanged from %d — ApplyDelete ignored its ClassExempt override", afterHead, beforeHead)
	}
}

func TestRouting_UpdateDeliveryState_JournalClassJournals(t *testing.T) {
	st := newTestStore(t)
	l := New(st, NewBus(), nil)
	chatID := chat.ChatID("alice")

	if _, err := l.AppendOutbound(OutboundParams{
		ChatID: chatID, Kind: KindText,
		MsgID: "m1", EnvelopeID: "e1", Body: routingTextBody(t, "hi"),
	}); err != nil {
		t.Fatal(err)
	}
	beforeHead, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}

	withMutatorClass(t, MutatorUpdateDeliveryState, ClassJournal)
	if _, _, err := l.UpdateDeliveryState("e1", DeliverySent); err != nil {
		t.Fatal(err)
	}

	afterHead, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}
	if afterHead <= beforeHead {
		t.Fatalf("journal head = %d, did not advance past %d — UpdateDeliveryState ignored its ClassJournal override", afterHead, beforeHead)
	}
	requireJournalEntry(t, l, beforeHead, chatID, "m1")
}

func TestRouting_ApplyDeliveredReceipt_JournalClassJournals(t *testing.T) {
	st := newTestStore(t)
	l := New(st, NewBus(), nil)
	chatID := chat.ChatID("alice")

	if _, err := l.AppendOutbound(OutboundParams{
		ChatID: chatID, Kind: KindFile,
		MsgID: "m1", EnvelopeID: "e1", Body: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	beforeHead, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}

	withMutatorClass(t, MutatorApplyDeliveredReceipt, ClassJournal)
	if _, err := l.ApplyDeliveredReceipt("m1", chatID); err != nil {
		t.Fatal(err)
	}

	afterHead, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}
	if afterHead <= beforeHead {
		t.Fatalf("journal head = %d, did not advance past %d — ApplyDeliveredReceipt ignored its ClassJournal override", afterHead, beforeHead)
	}
	requireJournalEntry(t, l, beforeHead, chatID, "m1")
}

func TestRouting_UpdateFileBody_JournalClassJournals(t *testing.T) {
	st := newTestStore(t)
	l := New(st, NewBus(), nil)
	chatID := chat.ChatID("alice")

	if _, err := l.AppendInbound(InboundParams{
		ChatID: chatID, Kind: KindFile,
		MsgID: "m1", SenderPeerID: "peer-1", SenderTs: 1742643880,
		Status: DecryptOK, Body: json.RawMessage(`{"state":"downloading"}`),
	}); err != nil {
		t.Fatal(err)
	}
	beforeHead, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}

	withMutatorClass(t, MutatorUpdateFileBody, ClassJournal)
	if _, err := l.UpdateFileBody("m1", json.RawMessage(`{"state":"ready"}`)); err != nil {
		t.Fatal(err)
	}

	afterHead, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}
	if afterHead <= beforeHead {
		t.Fatalf("journal head = %d, did not advance past %d — UpdateFileBody ignored its ClassJournal override", afterHead, beforeHead)
	}
	requireJournalEntry(t, l, beforeHead, chatID, "m1")
}

func TestRouting_UpdateInvitationBody_JournalClassJournals(t *testing.T) {
	st := newTestStore(t)
	l := New(st, NewBus(), nil)
	chatID := chat.ChatID("alice")

	live, err := json.Marshal(InvitationBody{ExpiresAt: 1742650000, State: InvitationLive})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.AppendInbound(InboundParams{
		ChatID: chatID, Kind: KindInvitation,
		MsgID:  "inv-1",
		Status: DecryptOK, Body: live,
	}); err != nil {
		t.Fatal(err)
	}
	beforeHead, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}

	retired, err := json.Marshal(InvitationBody{ExpiresAt: 1742650000, State: InvitationSuperseded})
	if err != nil {
		t.Fatal(err)
	}
	withMutatorClass(t, MutatorUpdateInvitationBody, ClassJournal)
	if _, err := l.UpdateInvitationBody("inv-1", retired, nil); err != nil {
		t.Fatal(err)
	}

	afterHead, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}
	if afterHead <= beforeHead {
		t.Fatalf("journal head = %d, did not advance past %d — UpdateInvitationBody ignored its ClassJournal override", afterHead, beforeHead)
	}
	requireJournalEntry(t, l, beforeHead, chatID, "inv-1")
}

func TestRouting_MarkReadReceiptSent_JournalClassJournals(t *testing.T) {
	st := newTestStore(t)
	l := New(st, NewBus(), nil)
	chatID := chat.ChatID("alice")

	if _, err := l.AppendInbound(InboundParams{
		ChatID: chatID, Kind: KindText,
		MsgID: "m1", SenderPeerID: "peer-1", SenderTs: 1742643880,
		Status: DecryptOK, Body: routingTextBody(t, "yo"),
	}); err != nil {
		t.Fatal(err)
	}
	beforeHead, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}

	withMutatorClass(t, MutatorMarkReadReceiptSent, ClassJournal)
	if err := l.MarkReadReceiptSent([]string{"m1"}, 1742643900); err != nil {
		t.Fatal(err)
	}

	afterHead, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}
	if afterHead <= beforeHead {
		t.Fatalf("journal head = %d, did not advance past %d — MarkReadReceiptSent ignored its ClassJournal override", afterHead, beforeHead)
	}
	requireJournalEntry(t, l, beforeHead, chatID, "m1")
}

func TestRouting_SuppressPendingReadReceipts_JournalClassJournals(t *testing.T) {
	st := newTestStore(t)
	l := New(st, NewBus(), nil)
	chatID := chat.ChatID("alice")

	if _, err := l.AppendInbound(InboundParams{
		ChatID: chatID, Kind: KindText,
		MsgID: "m1", SenderPeerID: "peer-1", SenderTs: 1742643880,
		Status: DecryptOK, Body: routingTextBody(t, "yo"),
	}); err != nil {
		t.Fatal(err)
	}
	beforeHead, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}

	withMutatorClass(t, MutatorSuppressPendingReadReceipts, ClassJournal)
	if _, err := l.SuppressPendingReadReceipts(chatID); err != nil {
		t.Fatal(err)
	}

	afterHead, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}
	if afterHead <= beforeHead {
		t.Fatalf("journal head = %d, did not advance past %d — SuppressPendingReadReceipts ignored its ClassJournal override", afterHead, beforeHead)
	}
	requireJournalEntry(t, l, beforeHead, chatID, "m1")
}

func TestRouting_ClearReceiptEnvelope_JournalClassJournals(t *testing.T) {
	st := newTestStore(t)
	l := New(st, NewBus(), nil)
	chatID := chat.ChatID("alice")

	if _, err := l.AppendInbound(InboundParams{
		ChatID: chatID, Kind: KindText,
		MsgID: "m1", SenderPeerID: "peer-1", SenderTs: 1742643880,
		Status: DecryptOK, Body: routingTextBody(t, "yo"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := l.MarkReadReceiptSent([]string{"m1"}, 1742643900); err != nil {
		t.Fatal(err)
	}
	if err := l.RecordReceiptEnvelope("env-1", chatID, []string{"m1"}, 1742643900); err != nil {
		t.Fatal(err)
	}
	beforeHead, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}

	withMutatorClass(t, MutatorClearReceiptEnvelope, ClassJournal)
	found, cleared, err := l.ClearReceiptEnvelope("env-1")
	if err != nil {
		t.Fatal(err)
	}
	if !found || cleared != 1 {
		t.Fatalf("found=%v cleared=%d, want found=true cleared=1", found, cleared)
	}

	afterHead, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}
	if afterHead <= beforeHead {
		t.Fatalf("journal head = %d, did not advance past %d — ClearReceiptEnvelope ignored its ClassJournal override", afterHead, beforeHead)
	}
	requireJournalEntry(t, l, beforeHead, chatID, "m1")
}
