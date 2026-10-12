package events

import (
	"testing"

	"haoma-frontend/internal/chat"
)

func TestMutatingTxnSetAuxRefusesRowKey(t *testing.T) {
	st := newTestStore(t)
	l := New(st, NewBus(), nil)
	rowKey := storageKey(chat.ChatID("alice"), 1000, 1)

	err := l.updateRetry(func(m mutatingTxn) error {
		return m.setAux(rowKey, []byte("payload"))
	})
	if err == nil {
		t.Fatal("expected setAux to refuse an event row key")
	}
}

func TestMutatingTxnDeleteAuxRefusesRowKey(t *testing.T) {
	st := newTestStore(t)
	l := New(st, NewBus(), nil)
	rowKey := storageKey(chat.ChatID("alice"), 1000, 1)

	err := l.updateRetry(func(m mutatingTxn) error {
		return m.deleteAux(rowKey)
	})
	if err == nil {
		t.Fatal("expected deleteAux to refuse an event row key")
	}
}

func TestMutatingTxnSetAuxAcceptsIndexKey(t *testing.T) {
	st := newTestStore(t)
	l := New(st, NewBus(), nil)

	err := l.updateRetry(func(m mutatingTxn) error {
		return m.setAux([]byte(msgIDPrefix+"m1"), []byte("some-row-key"))
	})
	if err != nil {
		t.Fatalf("setAux refused a legitimate index key: %v", err)
	}
}

func TestMutatingTxnDeleteAuxRefusesJournalKey(t *testing.T) {
	st := newTestStore(t)
	l := New(st, NewBus(), nil)

	err := l.updateRetry(func(m mutatingTxn) error {
		return m.deleteAux([]byte(journalPrefix + "whatever"))
	})
	if err == nil {
		t.Fatal("expected deleteAux to refuse a journal-keyspace key")
	}
}

func TestIsRowKeyDisjointFromAuxPrefixes(t *testing.T) {
	if !isRowKey([]byte(prefix + "chat1:000:1")) {
		t.Fatalf("isRowKey(%q) = false, want true", prefix)
	}
	for _, p := range auxPrefixes {
		if isRowKey([]byte(p + "x")) {
			t.Fatalf("isRowKey(%q) = true, want false — %q collides with the row prefix", p, p)
		}
	}
}

func TestMutatingTxnStoreRowWritesRowAndJournalsTogether(t *testing.T) {
	st := newTestStore(t)
	l := New(st, NewBus(), nil)
	rowKey := storageKey(chat.ChatID("alice"), 1000, 1)
	raw := []byte(`{"chat_id":"alice","msg_id":"m1"}`)

	err := l.updateRetry(func(m mutatingTxn) error {
		return m.storeRow(MutatorApplyEdit, chat.ChatID("alice"), "m1", rowKey, raw)
	})
	if err != nil {
		t.Fatalf("storeRow: %v", err)
	}

	entries, err := l.JournalSince(0, 0)
	if err != nil {
		t.Fatalf("JournalSince: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("journal entries = %d, want 1", len(entries))
	}
	if entries[0].ChatID != chat.ChatID("alice") || entries[0].MsgID != "m1" {
		t.Fatalf("journal entry = %+v, want chat_id=alice msg_id=m1", entries[0])
	}
}
