package peerstate

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dgraph-io/badger/v4"

	"haoma-frontend/internal/store"
)

const (
	prefix        = "peer:"
	suffixSendSeq = ":send_seq"
	suffixMeta    = ":meta"
)

type Counters struct {
	st *store.Store
}

func New(st *store.Store) *Counters {
	return &Counters{st: st}
}

func (c *Counters) NextSendSeq(peerID string) (uint64, error) {
	if peerID == "" {
		return 0, errors.New("peerstate: empty peer id")
	}
	key := sendSeqKey(peerID)
	var next uint64
	for attempt := 0; attempt < maxConflictRetries; attempt++ {
		err := c.st.Update(func(txn *badger.Txn) error {
			current, err := loadUint64(txn, key)
			if err != nil {
				return err
			}
			next = current + 1
			var buf [8]byte
			binary.BigEndian.PutUint64(buf[:], next)
			return txn.Set(key, buf[:])
		})
		if err == nil {
			return next, nil
		}
		if !errors.Is(err, badger.ErrConflict) {
			return 0, fmt.Errorf("peerstate: next send seq for %s: %w", peerID, err)
		}

	}
	return 0, fmt.Errorf("peerstate: next send seq for %s: exceeded %d conflict retries", peerID, maxConflictRetries)
}

const maxConflictRetries = 256

func (c *Counters) PeekSendSeq(peerID string) (uint64, error) {
	if peerID == "" {
		return 0, errors.New("peerstate: empty peer id")
	}
	var v uint64
	err := c.st.View(func(txn *badger.Txn) error {
		var err error
		v, err = loadUint64(txn, sendSeqKey(peerID))
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("peerstate: peek send seq for %s: %w", peerID, err)
	}
	return v, nil
}

func loadUint64(txn *badger.Txn, key []byte) (uint64, error) {
	item, err := txn.Get(key)
	if errors.Is(err, badger.ErrKeyNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var v uint64
	err = item.Value(func(raw []byte) error {
		if len(raw) != 8 {
			return fmt.Errorf("peerstate: corrupt counter, length %d != 8", len(raw))
		}
		v = binary.BigEndian.Uint64(raw)
		return nil
	})
	return v, err
}

func sendSeqKey(peerID string) []byte {
	out := make([]byte, 0, len(prefix)+len(peerID)+len(suffixSendSeq))
	out = append(out, prefix...)
	out = append(out, peerID...)
	out = append(out, suffixSendSeq...)
	return out
}

type MetaRecord struct {
	Nick  string `json:"nick,omitempty"`
	Alias string `json:"alias,omitempty"`

	NickAt int64 `json:"nick_at,omitempty"`
}

type Meta struct {
	st *store.Store
}

func NewMeta(st *store.Store) *Meta { return &Meta{st: st} }

func (m *Meta) Get(peerID string) (MetaRecord, error) {
	if peerID == "" {
		return MetaRecord{}, errors.New("peerstate: empty peer id")
	}
	var rec MetaRecord
	err := m.st.View(func(txn *badger.Txn) error {
		var err error
		rec, err = loadMeta(txn, metaKey(peerID))
		return err
	})
	if err != nil {
		return MetaRecord{}, fmt.Errorf("peerstate: get meta for %s: %w", peerID, err)
	}
	return rec, nil
}

func (m *Meta) SetAlias(peerID, alias string) (changed bool, err error) {
	if peerID == "" {
		return false, errors.New("peerstate: empty peer id")
	}
	return m.mutate(peerID, func(rec *MetaRecord) bool {
		if rec.Alias == alias {
			return false
		}
		rec.Alias = alias
		return true
	})
}

func (m *Meta) SetNick(peerID, nick string, ts int64) (changed bool, err error) {
	if peerID == "" {
		return false, errors.New("peerstate: empty peer id")
	}
	return m.mutate(peerID, func(rec *MetaRecord) bool {
		if ts != 0 && ts <= rec.NickAt {
			return false
		}
		if rec.Nick == nick {

			if ts != 0 && ts > rec.NickAt {
				rec.NickAt = ts
			}
			return false
		}
		rec.Nick = nick
		if ts != 0 {
			rec.NickAt = ts
		}
		return true
	})
}

func (m *Meta) mutate(peerID string, fn func(*MetaRecord) bool) (bool, error) {
	key := metaKey(peerID)
	var changed bool
	for attempt := 0; attempt < maxConflictRetries; attempt++ {
		err := m.st.Update(func(txn *badger.Txn) error {
			rec, err := loadMeta(txn, key)
			if err != nil {
				return err
			}
			changed = fn(&rec)
			raw, err := json.Marshal(rec)
			if err != nil {
				return err
			}
			return txn.Set(key, raw)
		})
		if err == nil {
			return changed, nil
		}
		if !errors.Is(err, badger.ErrConflict) {
			return false, fmt.Errorf("peerstate: mutate meta for %s: %w", peerID, err)
		}
	}
	return false, fmt.Errorf("peerstate: mutate meta for %s: exceeded %d conflict retries", peerID, maxConflictRetries)
}

func loadMeta(txn *badger.Txn, key []byte) (MetaRecord, error) {
	item, err := txn.Get(key)
	if errors.Is(err, badger.ErrKeyNotFound) {
		return MetaRecord{}, nil
	}
	if err != nil {
		return MetaRecord{}, err
	}
	var rec MetaRecord
	err = item.Value(func(raw []byte) error {
		if len(raw) == 0 {
			return nil
		}
		return json.Unmarshal(raw, &rec)
	})
	return rec, err
}

func metaKey(peerID string) []byte {
	out := make([]byte, 0, len(prefix)+len(peerID)+len(suffixMeta))
	out = append(out, prefix...)
	out = append(out, peerID...)
	out = append(out, suffixMeta...)
	return out
}

func Resolve(rec MetaRecord, peerID string) string {
	if rec.Alias != "" {
		return rec.Alias
	}
	if rec.Nick != "" {
		return rec.Nick
	}
	return ShortID(peerID)
}

func ShortID(peerID string) string {
	const n = 8
	if len(peerID) <= n {
		return peerID
	}
	return peerID[:n]
}
