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

var ErrNotFound = errors.New("store: not found")

type Store struct {
	dir string

	mu  sync.RWMutex
	db  *badger.DB
	key []byte
}

func Unlock(dataDir, passphrase string) (*Store, error) {
	meta, found, err := loadMeta(dataDir)
	if err != nil {
		return nil, err
	}
	if !found {
		salt, err := newSalt(DefaultKDFParams)
		if err != nil {
			return nil, fmt.Errorf("store: new salt: %w", err)
		}
		meta = Meta{Version: metaFormatVersion, Salt: salt, KDF: DefaultKDFParams}
		if err := saveMeta(dataDir, meta); err != nil {
			return nil, fmt.Errorf("store: save meta: %w", err)
		}
	}

	key := deriveKey(passphrase, meta.Salt, meta.KDF)

	opts := badger.DefaultOptions(filepath.Join(dataDir, badgerSubdir)).
		WithEncryptionKey(key).
		WithIndexCacheSize(16 << 20).
		WithLoggingLevel(badger.WARNING)

	db, err := badger.Open(opts)
	if err != nil {
		zero(key)
		return nil, fmt.Errorf("store: open badger: %w", err)
	}
	return &Store{dir: dataDir, db: db, key: key}, nil
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

func (s *Store) Put(key, value []byte) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db == nil {
		return ErrLocked
	}
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(key, value)
	})
}

func (s *Store) Get(key []byte) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db == nil {
		return nil, ErrLocked
	}
	var out []byte
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return item.Value(func(v []byte) error {
			out = append([]byte(nil), v...)
			return nil
		})
	})
	return out, err
}

func (s *Store) Delete(key []byte) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db == nil {
		return ErrLocked
	}
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Delete(key)
	})
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
