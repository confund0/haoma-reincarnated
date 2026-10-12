package events_test

import (
	"sync/atomic"
	"testing"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
)

func countGrowth(l *events.Log) func() int {
	var n atomic.Int64
	l.SetJournalGrowthHook(func() { n.Add(1) })
	return func() int { return int(n.Load()) }
}

func TestJournalGrowthFiresOnAJournalledWrite(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	grown := countGrowth(l)

	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText,
		MsgID: "in-1", SenderPeerID: "peer-1", SenderTs: 1742643880,
		Status: events.DecryptOK, Body: textBody(t, "yo"),
	}); err != nil {
		t.Fatal(err)
	}
	if got := grown(); got != 1 {
		t.Fatalf("the growth signal fired %d time(s) for one journalled append, want 1 — the trigger would never start a sweep", got)
	}
}

func TestJournalGrowthIsOnePerTransactionNotOnePerRow(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	chatID := chat.ChatID("alice")

	for _, id := range []string{"in-1", "in-2", "in-3"} {
		if _, err := l.AppendInbound(events.InboundParams{
			ChatID: chatID, Kind: events.KindText,
			MsgID: id, SenderPeerID: "peer-1", SenderTs: 1742643880,
			Status: events.DecryptOK, Body: textBody(t, "yo"),
		}); err != nil {
			t.Fatal(err)
		}
	}

	grown := countGrowth(l)

	mutated, _, err := l.MarkRead(chatID)
	if err != nil {
		t.Fatal(err)
	}
	if mutated != 3 {
		t.Fatalf("MarkRead stamped %d rows, want 3 — this test is no longer about a batch", mutated)
	}
	if got := grown(); got != 1 {
		t.Fatalf("the growth signal fired %d time(s) for one MarkRead over 3 rows, want 1 — a conversation opened with fifty unread would buy fifty exchanges", got)
	}
}

func TestJournalGrowthStaysSilentForAnExemptMutator(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	chatID := chat.ChatID("alice")

	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chatID, Kind: events.KindText,
		MsgID: "in-1", SenderPeerID: "peer-1", SenderTs: 1742643880,
		Status: events.DecryptOK, Body: textBody(t, "yo"),
	}); err != nil {
		t.Fatal(err)
	}

	grown := countGrowth(l)

	if _, err := l.SuppressPendingReadReceipts(chatID); err != nil {
		t.Fatal(err)
	}
	head, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}
	if head != 1 {
		t.Fatalf("journal head = %d, want 1 — the exempt write journalled and this test no longer isolates the signal", head)
	}
	if got := grown(); got != 0 {
		t.Fatalf("the growth signal fired %d time(s) for a write that earns no journal entry, want 0 — a suppression the sibling never sees would buy a Tor exchange", got)
	}
}

func TestJournalGrowthHookCanBeRemoved(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	grown := countGrowth(l)
	l.SetJournalGrowthHook(nil)

	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText,
		MsgID: "in-1", SenderPeerID: "peer-1", SenderTs: 1742643880,
		Status: events.DecryptOK, Body: textBody(t, "yo"),
	}); err != nil {
		t.Fatal(err)
	}
	if got := grown(); got != 0 {
		t.Fatalf("a removed hook fired %d time(s), want 0", got)
	}
}

func TestJournalWritesWithNoHookInstalled(t *testing.T) {
	l, _ := newLog(t, fixedClock(1742643890))
	if _, err := l.AppendInbound(events.InboundParams{
		ChatID: chat.ChatID("alice"), Kind: events.KindText,
		MsgID: "in-1", SenderPeerID: "peer-1", SenderTs: 1742643880,
		Status: events.DecryptOK, Body: textBody(t, "yo"),
	}); err != nil {
		t.Fatal(err)
	}
	head, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}
	if head != 1 {
		t.Fatalf("journal head = %d, want 1", head)
	}
}
