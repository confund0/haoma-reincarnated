package linkeddevice

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dgraph-io/badger/v4"
)

const siblingSaltPrefix = prefix + "destroy-salt:"

type SiblingSalt struct {
	Salt []byte `json:"salt"`

	SetAt int64 `json:"set_at,omitempty"`

	ReceivedAt int64 `json:"received_at,omitempty"`
}

func siblingSaltKey(peerID string) []byte {
	return []byte(siblingSaltPrefix + peerID)
}

func (s *Store) PutSiblingSalt(peerID string, rec SiblingSalt) (replaced bool, err error) {
	if peerID == "" {
		return false, errors.New("linkeddevice: empty peer id")
	}
	if len(rec.Salt) == 0 {
		return false, errors.New("linkeddevice: empty destroy salt")
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return false, fmt.Errorf("linkeddevice: encode destroy salt: %w", err)
	}
	key := siblingSaltKey(peerID)
	err = s.st.Update(func(txn *badger.Txn) error {
		_, getErr := txn.Get(key)
		switch {
		case getErr == nil:
			replaced = true
		case errors.Is(getErr, badger.ErrKeyNotFound):
			replaced = false
		default:
			return getErr
		}
		return txn.Set(key, raw)
	})
	if err != nil {
		return false, fmt.Errorf("linkeddevice: put destroy salt: %w", err)
	}
	return replaced, nil
}

func (s *Store) SiblingSalt(peerID string) (rec SiblingSalt, held bool, err error) {
	if peerID == "" {
		return SiblingSalt{}, false, errors.New("linkeddevice: empty peer id")
	}
	err = s.st.View(func(txn *badger.Txn) error {
		item, getErr := txn.Get(siblingSaltKey(peerID))
		if errors.Is(getErr, badger.ErrKeyNotFound) {
			return nil
		}
		if getErr != nil {
			return getErr
		}
		return item.Value(func(raw []byte) error {
			if len(raw) == 0 {
				return nil
			}
			if unErr := json.Unmarshal(raw, &rec); unErr != nil {
				return unErr
			}
			held = len(rec.Salt) > 0
			return nil
		})
	})
	if err != nil {
		return SiblingSalt{}, false, fmt.Errorf("linkeddevice: get destroy salt: %w", err)
	}
	return rec, held, nil
}

func (s *Store) ForgetSiblingSalt(peerID string) error {
	if peerID == "" {
		return errors.New("linkeddevice: no peer named")
	}
	err := s.st.Update(func(txn *badger.Txn) error {
		return txn.Delete(siblingSaltKey(peerID))
	})
	if err != nil {
		return fmt.Errorf("linkeddevice: forget sibling salt: %w", err)
	}
	return nil
}
