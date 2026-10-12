package events

import (
	"io"
	"log/slog"
	"testing"

	badger "github.com/dgraph-io/badger/v4"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Unlock(t.TempDir(), "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Lock() })
	return st
}

func TestTouch_UnclassifiedMutatorFailsClosed(t *testing.T) {
	st := newTestStore(t)

	wrote := true
	err := st.Update(func(txn *badger.Txn) error {
		var tErr error
		wrote, tErr = touch(txn, Mutator("never_registered"), chat.ChatID("alice"), "m1")
		return tErr
	})
	if err == nil {
		t.Fatal("expected an error for an unclassified mutator")
	}
	if wrote {
		t.Fatal("a refused mutator reported a journal write — the growth signal would fire for a transaction that failed")
	}
}

func TestTouch_ExemptMutatorSkipsJournal(t *testing.T) {
	st := newTestStore(t)
	l := New(st, NewBus(), nil)

	wrote := true
	if err := st.Update(func(txn *badger.Txn) error {
		var tErr error
		wrote, tErr = touch(txn, MutatorSuppressPendingReadReceipts, chat.ChatID("alice"), "m1")
		return tErr
	}); err != nil {
		t.Fatal(err)
	}
	if wrote {
		t.Fatal("a ClassExempt mutator reported a journal write — the growth signal would buy a Tor exchange for a fact no sibling ever sees")
	}
	head, err := l.JournalHead()
	if err != nil {
		t.Fatal(err)
	}
	if head != 0 {
		t.Fatalf("journal head = %d, want 0 — a ClassExempt mutator must not journal", head)
	}
}

func TestTouch_EmptyMsgIDRecordsNoJournalTouch(t *testing.T) {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	st := newTestStore(t)

	var txnUsed *badger.Txn
	wrote := true
	if err := st.Update(func(txn *badger.Txn) error {
		txnUsed = txn
		var tErr error
		wrote, tErr = touch(txn, MutatorAppend, chat.ChatID("alice"), "")
		return tErr
	}); err != nil {
		t.Fatal(err)
	}
	if wrote {
		t.Fatal("a row with no msg id reported a journal write — there is no entry, so the growth signal would name a mutation no sweep can ship")
	}

	pendingJournalTouchesMu.Lock()
	got := len(pendingJournalTouches[txnUsed])
	pendingJournalTouchesMu.Unlock()
	if got != 0 {
		t.Fatalf("pending journal touches queued for an empty msg_id = %d, want 0 — the journal wrote nothing, so there is nothing here to log", got)
	}
}

func TestTouch_NonEmptyMsgIDRecordsOneJournalTouch(t *testing.T) {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	st := newTestStore(t)

	var txnUsed *badger.Txn
	wrote := false
	if err := st.Update(func(txn *badger.Txn) error {
		txnUsed = txn
		var tErr error
		wrote, tErr = touch(txn, MutatorAppend, chat.ChatID("alice"), "m1")
		return tErr
	}); err != nil {
		t.Fatal(err)
	}
	if !wrote {
		t.Fatal("a journalled write reported no journal entry — the local sync trigger would never learn the journal grew")
	}

	pendingJournalTouchesMu.Lock()
	entries := append([]journalTouchLogEntry(nil), pendingJournalTouches[txnUsed]...)
	pendingJournalTouchesMu.Unlock()
	if len(entries) != 1 {
		t.Fatalf("pending journal touches queued for msg_id=m1 = %d, want 1", len(entries))
	}
	if entries[0].mutator != MutatorAppend || entries[0].msgID != "m1" {
		t.Fatalf("queued entry = %+v, want {mutator:%q msgID:m1}", entries[0], MutatorAppend)
	}
}
