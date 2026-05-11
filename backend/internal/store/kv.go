package store

import (
	"errors"

	"github.com/dgraph-io/badger/v4"
)

var ErrNotFound = errors.New("store: not found")

type ContactID []byte

const contactPrefix = "contact:"

func ContactKey(id ContactID) []byte {
	k := make([]byte, 0, len(contactPrefix)+len(id))
	k = append(k, contactPrefix...)
	k = append(k, id...)
	return k
}

func (s *Store) PutContact(id ContactID, value []byte) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db == nil {
		return ErrLocked
	}
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(ContactKey(id), value)
	})
}

func (s *Store) GetContact(id ContactID) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db == nil {
		return nil, ErrLocked
	}
	var out []byte
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(ContactKey(id))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			out = append([]byte(nil), val...)
			return nil
		})
	})
	return out, err
}

func (s *Store) DeleteContact(id ContactID) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db == nil {
		return ErrLocked
	}
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Delete(ContactKey(id))
	})
}

func (s *Store) ListContacts() ([]ContactID, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db == nil {
		return nil, ErrLocked
	}
	prefix := []byte(contactPrefix)
	var out []ContactID
	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			k := it.Item().KeyCopy(nil)
			out = append(out, ContactID(k[len(prefix):]))
		}
		return nil
	})
	return out, err
}

const addrPrefix = "addr:"

func AddrKey(serviceID string) []byte {
	k := make([]byte, 0, len(addrPrefix)+len(serviceID))
	k = append(k, addrPrefix...)
	k = append(k, serviceID...)
	return k
}

func (s *Store) PutAddrIndex(serviceID string, id ContactID) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db == nil {
		return ErrLocked
	}
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(AddrKey(serviceID), []byte(id))
	})
}

func (s *Store) GetAddrIndex(serviceID string) (ContactID, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db == nil {
		return nil, ErrLocked
	}
	var out ContactID
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(AddrKey(serviceID))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			out = append(ContactID(nil), val...)
			return nil
		})
	})
	return out, err
}

func (s *Store) DeleteAddrIndex(serviceID string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db == nil {
		return ErrLocked
	}
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Delete(AddrKey(serviceID))
	})
}

const statePrefix = "state:"

func StateKey(name string) []byte {
	return []byte(statePrefix + name)
}

func (s *Store) PutState(name string, value []byte) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db == nil {
		return ErrLocked
	}
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(StateKey(name), value)
	})
}

func (s *Store) GetState(name string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db == nil {
		return nil, ErrLocked
	}
	var out []byte
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(StateKey(name))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return item.Value(func(val []byte) error {
			out = append([]byte(nil), val...)
			return nil
		})
	})
	return out, err
}

func (s *Store) DeleteState(name string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db == nil {
		return ErrLocked
	}
	return s.db.Update(func(txn *badger.Txn) error {
		return txn.Delete(StateKey(name))
	})
}
