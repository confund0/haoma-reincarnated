package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dgraph-io/badger/v4"
)

var ErrNoMeta = errors.New("store: no meta.json in this directory")

var ErrStoreInUse = errors.New("store: another process holds this store")

var ErrNeedsCleanClose = errors.New("store: the last daemon did not close this store")

func UnlockReadOnly(dataDir, passphrase string) (*Store, error) {
	meta, found, err := loadMeta(dataDir)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("%w: %s", ErrNoMeta, dataDir)
	}

	badgerDir := filepath.Join(dataDir, badgerSubdir)
	if err := checkNotInUse(badgerDir); err != nil {
		return nil, err
	}

	key := deriveKey(passphrase, meta.Salt, meta.KDF)

	opts := badger.DefaultOptions(badgerDir).
		WithEncryptionKey(key).
		WithIndexCacheSize(16 << 20).
		WithLoggingLevel(badger.WARNING).
		WithReadOnly(true)

	db, err := badger.Open(opts)
	if err != nil {
		zero(key)
		return nil, readOnlyOpenError(badgerDir, err)
	}
	return &Store{dir: dataDir, db: db, key: key}, nil
}

func readOnlyOpenError(badgerDir string, err error) error {
	switch {
	case containsAny(err.Error(), "Cannot acquire directory lock", "resource temporarily unavailable"):
		return fmt.Errorf("%w: %s: %v", ErrStoreInUse, badgerDir, err)
	case containsAny(err.Error(), "Log truncate required", "while opening memtables"):
		return fmt.Errorf("%w: %s: %v", ErrNeedsCleanClose, badgerDir, err)
	}
	return fmt.Errorf("store: open badger read-only: %w", err)
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}
