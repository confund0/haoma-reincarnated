package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/dgraph-io/badger/v4"

	"haoma/internal/pair"
	"haoma/internal/store"
)

const dhtPairCachePrefix = "pair-invite:"

type dhtPairCacheEntry struct {
	GUID         string         `json:"guid"`
	InviteJSON   []byte         `json:"invite_json"`
	SecretHex    string         `json:"secret_hex"`
	Materials    pair.Materials `json:"materials"`
	CreatedAt    int64          `json:"created_at"`
	ExpiresAt    int64          `json:"expires_at"`
	ReturnInvite []byte         `json:"return_invite,omitempty"`
	ReturnAt     int64          `json:"return_at,omitempty"`
}

type dhtPairCache struct {
	st *store.Store
}

func newDHTPairCache(st *store.Store) *dhtPairCache { return &dhtPairCache{st: st} }

func (c *dhtPairCache) Put(e dhtPairCacheEntry) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return c.st.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(dhtPairCachePrefix+e.GUID), raw)
	})
}

func (c *dhtPairCache) Get(guid string) (dhtPairCacheEntry, error) {
	var e dhtPairCacheEntry
	err := c.st.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(dhtPairCachePrefix + guid))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return errDHTPairNotFound
		}
		if err != nil {
			return err
		}
		return item.Value(func(v []byte) error {
			return json.Unmarshal(v, &e)
		})
	})
	if err != nil {
		return dhtPairCacheEntry{}, err
	}
	if e.ExpiresAt > 0 && time.Now().Unix() > e.ExpiresAt {

		return dhtPairCacheEntry{}, errDHTPairExpired
	}
	return e, nil
}

func (c *dhtPairCache) Delete(guid string) error {
	return c.st.Update(func(txn *badger.Txn) error {
		return txn.Delete([]byte(dhtPairCachePrefix + guid))
	})
}

func (c *dhtPairCache) List() ([]dhtPairCacheEntry, error) {
	var out []dhtPairCacheEntry
	err := c.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(dhtPairCachePrefix)
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			var e dhtPairCacheEntry
			if err := it.Item().Value(func(v []byte) error {
				return json.Unmarshal(v, &e)
			}); err != nil {
				slog.Warn("pair cache: unmarshal entry failed", slog.Any("err", err))
				continue
			}
			out = append(out, e)
		}
		return nil
	})
	return out, err
}

func (c *dhtPairCache) SweepExpired(now time.Time) (int, error) {
	nowUnix := now.Unix()
	var toDelete [][]byte
	err := c.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(dhtPairCachePrefix)
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			var e dhtPairCacheEntry
			if err := item.Value(func(v []byte) error {
				return json.Unmarshal(v, &e)
			}); err != nil {
				continue
			}
			if e.ExpiresAt > 0 && e.ExpiresAt <= nowUnix {
				toDelete = append(toDelete, item.KeyCopy(nil))
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if len(toDelete) == 0 {
		return 0, nil
	}
	err = c.st.Update(func(txn *badger.Txn) error {
		for _, k := range toDelete {
			if err := txn.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
	return len(toDelete), err
}

var (
	errDHTPairNotFound = errors.New("dht pair cache: entry not found")
	errDHTPairExpired  = errors.New("dht pair cache: entry expired")
)
