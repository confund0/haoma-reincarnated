package peerstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/dgraph-io/badger/v4"

	"haoma-frontend/internal/logid"
	"haoma-frontend/internal/store"
)

const suffixDeleted = ":deleted"

func deletedKey(fingerprint string) []byte {
	return []byte(personPrefix + fingerprint + suffixDeleted)
}

type Tombstone struct {
	DeletedAt int64 `json:"deleted_at"`

	AppliedAt int64 `json:"applied_at"`

	Done bool `json:"done,omitempty"`
}

func (t Tombstone) Covers(pairedAt int64) bool {
	return pairedAt <= t.Bound()
}

func (t Tombstone) Bound() int64 {
	bound := t.DeletedAt
	if t.AppliedAt > 0 && t.AppliedAt < bound {
		bound = t.AppliedAt
	}
	return bound
}

func (m *Meta) Tombstone(fingerprint string) (t Tombstone, found bool, err error) {
	if fingerprint == "" {
		return Tombstone{}, false, errors.New("peerstate: empty fingerprint")
	}
	raw, err := m.st.Get(deletedKey(fingerprint))
	if errors.Is(err, store.ErrNotFound) {
		return Tombstone{}, false, nil
	}
	if err != nil {
		return Tombstone{}, false, fmt.Errorf("peerstate: read tombstone: %w", err)
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		return Tombstone{}, false, fmt.Errorf("peerstate: decode tombstone: %w", err)
	}
	return t, true, nil
}

func (m *Meta) MarkDeleted(fingerprint string, deletedAt, now int64) (held Tombstone, moved bool, err error) {
	if fingerprint == "" {
		return Tombstone{}, false, errors.New("peerstate: empty fingerprint")
	}
	if deletedAt <= 0 {
		return Tombstone{}, false, errors.New("peerstate: a delete with no time")
	}
	key := deletedKey(fingerprint)
	err = m.st.Update(func(txn *badger.Txn) error {
		cur, found, lErr := loadTombstone(txn, key)
		if lErr != nil {
			return lErr
		}
		if found && cur.DeletedAt >= deletedAt {
			held, moved = cur, false
			return nil
		}
		held = Tombstone{DeletedAt: deletedAt, AppliedAt: now}
		moved = true
		raw, mErr := json.Marshal(held)
		if mErr != nil {
			return mErr
		}
		return txn.Set(key, raw)
	})
	if err != nil {
		return Tombstone{}, false, fmt.Errorf("peerstate: mark %s deleted: %w", logid.Short(fingerprint), err)
	}
	return held, moved, nil
}

func (m *Meta) MarkDeleteDone(fingerprint string, deletedAt int64) error {
	if fingerprint == "" {
		return errors.New("peerstate: empty fingerprint")
	}
	key := deletedKey(fingerprint)
	err := m.st.Update(func(txn *badger.Txn) error {
		cur, found, lErr := loadTombstone(txn, key)
		if lErr != nil {
			return lErr
		}
		if !found || cur.DeletedAt != deletedAt || cur.Done {
			return nil
		}
		cur.Done = true
		raw, mErr := json.Marshal(cur)
		if mErr != nil {
			return mErr
		}
		return txn.Set(key, raw)
	})
	if err != nil {
		return fmt.Errorf("peerstate: mark delete of %s done: %w", logid.Short(fingerprint), err)
	}
	return nil
}

type DeletedPerson struct {
	Fingerprint string
	Tombstone
}

func (m *Meta) Tombstones() ([]DeletedPerson, error) {
	var out []DeletedPerson
	err := m.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(personPrefix)
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.ValidForPrefix([]byte(personPrefix)); it.Next() {
			key := string(it.Item().Key())
			if !strings.HasSuffix(key, suffixDeleted) {
				continue
			}
			fp := key[len(personPrefix) : len(key)-len(suffixDeleted)]
			if fp == "" {
				continue
			}
			var t Tombstone
			if err := it.Item().Value(func(raw []byte) error {
				return json.Unmarshal(raw, &t)
			}); err != nil {
				return err
			}
			out = append(out, DeletedPerson{Fingerprint: fp, Tombstone: t})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("peerstate: tombstones: %w", err)
	}

	return out, nil
}

func loadTombstone(txn *badger.Txn, key []byte) (Tombstone, bool, error) {
	item, err := txn.Get(key)
	if errors.Is(err, badger.ErrKeyNotFound) {
		return Tombstone{}, false, nil
	}
	if err != nil {
		return Tombstone{}, false, err
	}
	var t Tombstone
	err = item.Value(func(raw []byte) error {
		return json.Unmarshal(raw, &t)
	})
	return t, err == nil, err
}

func (m *Meta) ForgetPeer(peerID string) error {
	if peerID == "" {
		return errors.New("peerstate: empty peer id")
	}
	if err := m.st.Delete(metaKey(peerID)); err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("peerstate: forget peer %s: %w", logid.Short(peerID), err)
	}
	return nil
}

func (m *Meta) ForgetPerson(fingerprint string) error {
	if fingerprint == "" {
		return errors.New("peerstate: empty fingerprint")
	}
	if err := m.st.Delete(personKey(fingerprint)); err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("peerstate: forget person %s: %w", logid.Short(fingerprint), err)
	}
	return nil
}

func (m *Meta) HasPerson(fingerprint string) (bool, error) {
	_, err := m.GetPerson(fingerprint)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (m *Meta) PeerRecords() (map[string]MetaRecord, error) {
	out := map[string]MetaRecord{}
	err := m.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefix)
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.ValidForPrefix([]byte(prefix)); it.Next() {
			peerID, ok := peerIDFromMetaKey(string(it.Item().Key()))
			if !ok {
				continue
			}
			var rec MetaRecord
			if err := it.Item().Value(func(raw []byte) error {
				if len(raw) == 0 {
					return nil
				}
				return json.Unmarshal(raw, &rec)
			}); err != nil {
				return err
			}
			if rec.Fingerprint == "" {
				continue
			}
			out[peerID] = rec
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("peerstate: peer records: %w", err)
	}
	return out, nil
}
