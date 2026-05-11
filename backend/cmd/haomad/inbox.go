package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/dgraph-io/badger/v4"

	"haoma/internal/eventbus"
	"haoma/internal/store"
	"haoma/internal/xport"
)

const inboxPrefix = "inbox:"

const inboxEnvIDPrefix = "inbox-envid:"

type inboxEntry struct {
	ArrivalAt int64          `json:"arrival_at"`
	PeerID    string         `json:"peer_id"`
	Envelope  xport.Envelope `json:"envelope"`
}

type inbox struct {
	st  *store.Store
	bus *eventbus.Bus
}

func newInbox(st *store.Store, bus *eventbus.Bus) *inbox {
	return &inbox{st: st, bus: bus}
}

func inboxKey(arrivalNanos int64, envID string) []byte {
	buf := make([]byte, 0, len(inboxPrefix)+8+1+len(envID))
	buf = append(buf, inboxPrefix...)
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(arrivalNanos))
	buf = append(buf, ts[:]...)
	buf = append(buf, ':')
	buf = append(buf, envID...)
	return buf
}

func (i *inbox) Put(entry inboxEntry) error {
	raw, err := json.Marshal(entry)
	if err != nil {
		slog.Warn("inbox: marshal entry failed",
			slog.String("envelope_id", entry.Envelope.ID),
			slog.Any("err", err),
		)
		return err
	}
	idxKey := []byte(inboxEnvIDPrefix + entry.Envelope.ID)
	key := inboxKey(entry.ArrivalAt, entry.Envelope.ID)
	var stored bool
	if err := i.st.Update(func(txn *badger.Txn) error {
		if _, err := txn.Get(idxKey); err == nil {
			slog.Debug("inbox: duplicate envelope; skipping", slog.String("envelope_id", entry.Envelope.ID))
			return nil
		}
		if err := txn.Set(idxKey, nil); err != nil {
			slog.Warn("inbox: set dedup index failed",
				slog.String("envelope_id", entry.Envelope.ID),
				slog.Any("err", err),
			)
			return err
		}
		if err := txn.Set(key, raw); err != nil {
			slog.Warn("inbox: set entry failed",
				slog.String("envelope_id", entry.Envelope.ID),
				slog.Any("err", err),
			)
			return err
		}
		stored = true
		slog.Debug("inbox: entry stored",
			slog.String("envelope_id", entry.Envelope.ID),
			slog.String("peer_id", entry.PeerID),
		)
		return nil
	}); err != nil {
		return err
	}

	if stored && i.bus != nil {
		i.bus.Publish(eventbus.TopicInboxReceived, entry)
	}
	return nil
}

func (i *inbox) List(sinceNanos int64, limit int) ([]inboxEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	var out []inboxEntry
	err := i.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(inboxPrefix)
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid() && len(out) < limit; it.Next() {
			item := it.Item()
			key := item.Key()
			rest := key[len(inboxPrefix):]
			if len(rest) < 8 {
				continue
			}
			nanos := int64(binary.BigEndian.Uint64(rest[:8]))
			if nanos <= sinceNanos {
				continue
			}
			var e inboxEntry
			if err := item.Value(func(v []byte) error {
				return json.Unmarshal(v, &e)
			}); err != nil {
				slog.Warn("inbox: unmarshal entry failed", slog.Any("err", err))
				return err
			}
			out = append(out, e)
		}
		return nil
	})
	if err == nil {
		slog.Debug("inbox: list", slog.Int("count", len(out)), slog.Int64("since_ns", sinceNanos))
	}
	return out, err
}

func (i *inbox) Delete(envID string) error {
	var key []byte
	err := i.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(inboxPrefix)
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			k := it.Item().KeyCopy(nil)
			rest := k[len(inboxPrefix):]
			if len(rest) < 9 {
				continue
			}
			tail := string(rest[9:])
			if tail == envID {
				key = k
				return nil
			}
		}
		return nil
	})
	if err != nil {
		slog.Warn("inbox: delete scan failed", slog.String("envelope_id", envID), slog.Any("err", err))
		return err
	}
	if key == nil {
		slog.Debug("inbox: delete not found", slog.String("envelope_id", envID))
		return errInboxNotFound
	}
	err = i.st.Update(func(txn *badger.Txn) error {
		if err := txn.Delete(key); err != nil {
			slog.Warn("inbox: delete entry failed", slog.String("envelope_id", envID), slog.Any("err", err))
			return err
		}

		idxKey := []byte(inboxEnvIDPrefix + envID)
		if err := txn.Delete(idxKey); err != nil {
			slog.Warn("inbox: dedup index delete failed",
				slog.String("envelope_id", envID),
				slog.Any("err", err),
			)
			return err
		}
		return nil
	})
	if err == nil {
		slog.Debug("inbox: entry deleted", slog.String("envelope_id", envID))
	}
	return err
}

func (i *inbox) Count() (int, error) {
	var n int
	err := i.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(inboxPrefix)
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			n++
		}
		return nil
	})
	return n, err
}

var errInboxNotFound = errors.New("inbox: entry not found")

func newEnvelopeID() (string, error) {
	var b [16]byte
	if _, err := randRead(b[:]); err != nil {
		return "", fmt.Errorf("envelope id: %w", err)
	}
	const hexDigits = "0123456789abcdef"
	out := make([]byte, 32)
	for i, x := range b {
		out[2*i] = hexDigits[x>>4]
		out[2*i+1] = hexDigits[x&0x0f]
	}
	return string(out), nil
}
