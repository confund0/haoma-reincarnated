package store

import (
	"errors"
	"testing"

	"github.com/dgraph-io/badger/v4"
)

func newTestBadger(t *testing.T) *badger.DB {
	t.Helper()
	opts := badger.DefaultOptions(t.TempDir()).WithLoggingLevel(badger.WARNING)
	db, err := badger.Open(opts)
	if err != nil {
		t.Fatalf("badger.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestRunMigrations_EmptyList_Noop(t *testing.T) {
	db := newTestBadger(t)
	if err := runMigrations(db, nil); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	v, err := readSchemaVersion(db)
	if err != nil {
		t.Fatalf("readSchemaVersion: %v", err)
	}
	if v != 0 {
		t.Errorf("version = %d, want 0", v)
	}
}

func TestRunMigrations_UpgradesFromZero(t *testing.T) {
	db := newTestBadger(t)
	list := []Migration{
		{To: 1, Up: func(txn *badger.Txn) error {
			return txn.Set([]byte("m1"), []byte("one"))
		}},
		{To: 2, Up: func(txn *badger.Txn) error {
			return txn.Set([]byte("m2"), []byte("two"))
		}},
	}
	if err := runMigrations(db, list); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	v, err := readSchemaVersion(db)
	if err != nil {
		t.Fatalf("readSchemaVersion: %v", err)
	}
	if v != 2 {
		t.Errorf("version = %d, want 2", v)
	}
	for _, k := range []string{"m1", "m2"} {
		err := db.View(func(txn *badger.Txn) error {
			_, err := txn.Get([]byte(k))
			return err
		})
		if err != nil {
			t.Errorf("key %q missing: %v", k, err)
		}
	}
}

func TestRunMigrations_ResumesFromPartialRun(t *testing.T) {
	db := newTestBadger(t)
	if err := db.Update(func(txn *badger.Txn) error {
		return writeSchemaVersion(txn, 1)
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ran1 := 0
	ran2 := 0
	list := []Migration{
		{To: 1, Up: func(txn *badger.Txn) error { ran1++; return nil }},
		{To: 2, Up: func(txn *badger.Txn) error { ran2++; return nil }},
	}
	if err := runMigrations(db, list); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	if ran1 != 0 {
		t.Errorf("migration 1 ran %d times, want 0 (already applied)", ran1)
	}
	if ran2 != 1 {
		t.Errorf("migration 2 ran %d times, want 1", ran2)
	}
}

func TestRunMigrations_RefusesStoredNewerThanTarget(t *testing.T) {
	db := newTestBadger(t)
	if err := db.Update(func(txn *badger.Txn) error {
		return writeSchemaVersion(txn, 5)
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := runMigrations(db, []Migration{
		{To: 1, Up: func(txn *badger.Txn) error { return nil }},
	})
	if !errors.Is(err, ErrSchemaNewer) {
		t.Fatalf("err = %v, want ErrSchemaNewer", err)
	}
}

func TestRunMigrations_FailedMigrationDoesNotBumpVersion(t *testing.T) {
	db := newTestBadger(t)
	boom := errors.New("boom")
	list := []Migration{
		{To: 1, Up: func(txn *badger.Txn) error { return boom }},
	}
	if err := runMigrations(db, list); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom wrapped", err)
	}
	v, err := readSchemaVersion(db)
	if err != nil {
		t.Fatalf("readSchemaVersion: %v", err)
	}
	if v != 0 {
		t.Errorf("version = %d, want 0 (migration failed)", v)
	}
}

func TestRunMigrations_MisnumberedListRejected(t *testing.T) {
	db := newTestBadger(t)
	list := []Migration{
		{To: 2, Up: func(txn *badger.Txn) error { return nil }},
	}
	if err := runMigrations(db, list); err == nil {
		t.Fatalf("expected misnumbered-list error, got nil")
	}
}

func TestRunMigrations_FailedMidRunLeavesPriorApplied(t *testing.T) {
	db := newTestBadger(t)
	boom := errors.New("boom")
	list := []Migration{
		{To: 1, Up: func(txn *badger.Txn) error {
			return txn.Set([]byte("k1"), []byte("v1"))
		}},
		{To: 2, Up: func(txn *badger.Txn) error { return boom }},
	}
	if err := runMigrations(db, list); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	v, err := readSchemaVersion(db)
	if err != nil {
		t.Fatalf("readSchemaVersion: %v", err)
	}
	if v != 1 {
		t.Errorf("version = %d, want 1 (M1 committed, M2 failed)", v)
	}
	err = db.View(func(txn *badger.Txn) error {
		_, err := txn.Get([]byte("k1"))
		return err
	})
	if err != nil {
		t.Errorf("M1's key missing after partial run: %v", err)
	}
}
