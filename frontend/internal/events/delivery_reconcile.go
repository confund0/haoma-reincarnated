package events

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dgraph-io/badger/v4"
)

const keyOutboxReconcileAt = "events:outbox_reconcile_at"

func (l *Log) GetByEnvelopeID(envelopeID string) (Event, error) {
	if envelopeID == "" {
		return Event{}, ErrEventNotFound
	}
	var ev Event
	err := l.st.View(func(txn *badger.Txn) error {
		idxItem, err := txn.Get([]byte(envIDPrefix + envelopeID))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrEventNotFound
		}
		if err != nil {
			return err
		}
		var key []byte
		if err := idxItem.Value(func(v []byte) error {
			key = append([]byte(nil), v...)
			return nil
		}); err != nil {
			return err
		}
		rowItem, err := txn.Get(key)
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrEventNotFound
		}
		if err != nil {
			return err
		}
		return rowItem.Value(func(v []byte) error {
			return json.Unmarshal(v, &ev)
		})
	})
	if err != nil {
		return Event{}, err
	}
	return ev, nil
}

func (l *Log) OutboxReconcileWatermark() (int64, error) {
	var at uint64
	err := l.st.View(func(txn *badger.Txn) error {
		v, err := loadUint64(txn, []byte(keyOutboxReconcileAt))
		if err != nil {
			return err
		}
		at = v
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("events: read outbox reconcile watermark: %w", err)
	}
	return int64(at), nil
}

func (l *Log) SetOutboxReconcileWatermark(atNanos int64) error {
	if atNanos <= 0 {
		return nil
	}
	err := l.st.Update(func(txn *badger.Txn) error {
		cur, err := loadUint64(txn, []byte(keyOutboxReconcileAt))
		if err != nil {
			return err
		}
		if uint64(atNanos) <= cur {
			return nil
		}
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], uint64(atNanos))
		return txn.Set([]byte(keyOutboxReconcileAt), buf[:])
	})
	if err != nil {
		return fmt.Errorf("events: advance outbox reconcile watermark: %w", err)
	}
	return nil
}
