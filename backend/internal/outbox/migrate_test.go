package outbox

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/dgraph-io/badger/v4"

	"haoma/internal/store"
	"haoma/internal/xport"
)

func writeXQueueEntry(t *testing.T, st *store.Store, key string, e xqueueEntry) {
	t.Helper()
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(xqueuePrefix+key), raw)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMigrate_Empty(t *testing.T) {
	s, err := store.Unlock(t.TempDir(), "pw")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Lock() }()

	n, err := Migrate(s, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("migrated = %d, want 0 (nothing to migrate)", n)
	}
}

func TestMigrate_CopiesAndDeletes(t *testing.T) {
	s, err := store.Unlock(t.TempDir(), "pw")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Lock() }()

	now := time.Unix(1_700_000_000, 0)
	e := xqueueEntry{
		Dest:     "http://peer.onion",
		Envelope: xport.Envelope{ID: "mig1", Timestamp: 1, Payload: []byte("p")},
		Attempts: 2,
		FirstAt:  now.Unix(),
	}
	writeXQueueEntry(t, s, "key1", e)

	n, err := Migrate(s, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("migrated = %d, want 1", n)
	}

	if err := s.View(func(txn *badger.Txn) error {
		_, err := txn.Get([]byte(xqueuePrefix + "key1"))
		return err
	}); err != badger.ErrKeyNotFound {
		t.Errorf("old xqueue key still present after migration: %v", err)
	}

	st := NewStore(s)
	row, err := st.Load("mig1")
	if err != nil {
		t.Fatalf("Load after migrate: %v", err)
	}
	if row.State != StateEnqueued {
		t.Errorf("state = %q, want enqueued", row.State)
	}
	if row.Dest != "http://peer.onion" {
		t.Errorf("dest = %q, want http://peer.onion", row.Dest)
	}
	if row.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", row.Attempts)
	}

	wantFirstAt := now.Unix() * int64(time.Second)
	if row.FirstAt != wantFirstAt {
		t.Errorf("FirstAt = %d, want %d", row.FirstAt, wantFirstAt)
	}

	if row.NextAttemptAt != now.UnixNano() {
		t.Errorf("NextAttemptAt = %d, want %d (reset to now)", row.NextAttemptAt, now.UnixNano())
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	s, err := store.Unlock(t.TempDir(), "pw")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Lock() }()

	now := time.Now()
	e := xqueueEntry{
		Dest:     "http://peer.onion",
		Envelope: xport.Envelope{ID: "idem1", Timestamp: 1, Payload: []byte("p")},
	}
	writeXQueueEntry(t, s, "k", e)

	n, err := Migrate(s, now)
	if err != nil || n != 1 {
		t.Fatalf("first migrate: n=%d err=%v", n, err)
	}

	n, err = Migrate(s, now)
	if err != nil {
		t.Fatalf("second migrate err: %v", err)
	}
	if n != 0 {
		t.Errorf("second migrate = %d, want 0 (idempotent)", n)
	}
}

func TestMigrate_SkipsMalformed(t *testing.T) {
	s, err := store.Unlock(t.TempDir(), "pw")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Lock() }()

	e := xqueueEntry{Dest: "http://peer.onion", Envelope: xport.Envelope{ID: ""}}
	writeXQueueEntry(t, s, "bad", e)

	n, err := Migrate(s, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("malformed entry should be skipped, got n=%d", n)
	}
}
