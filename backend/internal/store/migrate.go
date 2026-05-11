package store

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/dgraph-io/badger/v4"
)

var ErrSchemaNewer = errors.New("store: data written by a newer binary")

type Migration struct {
	To int
	Up func(txn *badger.Txn) error
}

var migrations = []Migration{}

const schemaVersionKey = "sys:schema_version"

func runMigrations(db *badger.DB, list []Migration) error {
	current, err := readSchemaVersion(db)
	if err != nil {
		return err
	}
	target := len(list)
	if current > target {
		return fmt.Errorf("%w: store at schema %d, binary supports up to %d", ErrSchemaNewer, current, target)
	}
	for v := current + 1; v <= target; v++ {
		mig := list[v-1]
		if mig.To != v {
			return fmt.Errorf("store: migration list misnumbered: index %d has To=%d, want %d", v-1, mig.To, v)
		}
		if err := db.Update(func(txn *badger.Txn) error {
			if err := mig.Up(txn); err != nil {
				return err
			}
			return writeSchemaVersion(txn, v)
		}); err != nil {
			return fmt.Errorf("store: migration %d: %w", v, err)
		}
	}
	return nil
}

func readSchemaVersion(db *badger.DB) (int, error) {
	var v int
	err := db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(schemaVersionKey))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			if len(val) != 8 {
				return fmt.Errorf("store: schema version value length %d, want 8", len(val))
			}
			v = int(binary.BigEndian.Uint64(val))
			return nil
		})
	})
	return v, err
}

func writeSchemaVersion(txn *badger.Txn, v int) error {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(v))
	return txn.Set([]byte(schemaVersionKey), buf[:])
}
