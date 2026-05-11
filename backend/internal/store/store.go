package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/dgraph-io/badger/v4"
)

const badgerSubdir = "badger"

var ErrLocked = errors.New("store: locked")

type Store struct {
	dir string

	mu  sync.RWMutex
	db  *badger.DB
	key []byte
}

func Unlock(dir, passphrase string) (*Store, error) {
	meta, found, err := loadMeta(dir)
	if err != nil {
		return nil, err
	}
	if !found {
		salt, err := newSalt(DefaultKDFParams)
		if err != nil {
			return nil, err
		}
		meta = Meta{Version: metaFormatVersion, Salt: salt, KDF: DefaultKDFParams}
		if err := saveMeta(dir, meta); err != nil {
			return nil, err
		}
	}

	key := deriveKey(passphrase, meta.Salt, meta.KDF)

	opts := badger.DefaultOptions(filepath.Join(dir, badgerSubdir)).
		WithEncryptionKey(key).
		WithIndexCacheSize(16 << 20).
		WithLoggingLevel(badger.WARNING)

	db, err := badger.Open(opts)
	if err != nil {
		zero(key)
		return nil, fmt.Errorf("store: open: %w", err)
	}

	if err := runMigrations(db, migrations); err != nil {
		db.Close()
		zero(key)
		return nil, err
	}

	return &Store{dir: dir, db: db, key: key}, nil
}

func (s *Store) Lock() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	zero(s.key)
	s.key = nil
	return err
}

func (s *Store) View(fn func(txn *badger.Txn) error) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db == nil {
		return ErrLocked
	}
	return s.db.View(fn)
}

func (s *Store) Update(fn func(txn *badger.Txn) error) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db == nil {
		return ErrLocked
	}
	return s.db.Update(fn)
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
