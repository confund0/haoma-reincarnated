package events

import (
	"encoding/json"
	"errors"

	"github.com/dgraph-io/badger/v4"

	"haoma-frontend/internal/chat"
)

const receiptEnvPrefix = "rcpt-envid:"

type ReceiptEnvelope struct {
	EnvelopeID string      `json:"envelope_id"`
	ChatID     chat.ChatID `json:"chat_id"`
	MsgIDs     []string    `json:"msg_ids"`

	SentAt int64 `json:"sent_at"`
}

func (l *Log) RecordReceiptEnvelope(envID string, chatID chat.ChatID, msgIDs []string, sentAt int64) error {
	if envID == "" {
		return errors.New("events: empty envelope id")
	}
	if len(msgIDs) == 0 {
		return nil
	}
	rec := ReceiptEnvelope{
		EnvelopeID: envID,
		ChatID:     chatID,
		MsgIDs:     append([]string(nil), msgIDs...),
		SentAt:     sentAt,
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return l.st.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(receiptEnvPrefix+envID), raw)
	})
}

func (l *Log) DropReceiptEnvelope(envID string) (bool, error) {
	if envID == "" {
		return false, nil
	}
	found := false
	err := l.st.Update(func(txn *badger.Txn) error {
		found = false
		key := []byte(receiptEnvPrefix + envID)
		if _, err := txn.Get(key); err != nil {
			if errors.Is(err, badger.ErrKeyNotFound) {
				return nil
			}
			return err
		}
		found = true
		return txn.Delete(key)
	})
	if err != nil {
		return false, err
	}
	return found, nil
}

func (l *Log) ClearReceiptEnvelope(envID string) (bool, int, error) {
	if envID == "" {
		return false, 0, nil
	}
	cleared := 0
	found := false

	err := l.updateRetry(func(m mutatingTxn) error {
		cleared, found = 0, false
		mapKey := []byte(receiptEnvPrefix + envID)
		item, err := m.get(mapKey)
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		var rec ReceiptEnvelope
		if err := item.Value(func(v []byte) error {
			return json.Unmarshal(v, &rec)
		}); err != nil {
			return err
		}
		found = true
		for _, msgID := range rec.MsgIDs {
			if msgID == "" {
				continue
			}
			idxItem, err := m.get([]byte(msgIDPrefix + msgID))
			if errors.Is(err, badger.ErrKeyNotFound) {
				continue
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
			rowItem, err := m.get(key)
			if errors.Is(err, badger.ErrKeyNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			var ev Event
			if err := rowItem.Value(func(v []byte) error {
				return json.Unmarshal(v, &ev)
			}); err != nil {
				return err
			}
			if ev.ReadReceiptSentAt != rec.SentAt {
				continue
			}
			ev.ReadReceiptSentAt = 0
			ev.ReceiptClaimOrigin = false

			ev.ReceiptClaimGeneration++
			raw, err := json.Marshal(ev)
			if err != nil {
				return err
			}

			if err := m.storeRow(MutatorClearReceiptEnvelope, ev.ChatID, ev.MsgID, key, raw); err != nil {
				return err
			}
			cleared++
		}
		return m.deleteAux(mapKey)
	})
	if err != nil {
		return false, 0, err
	}
	return found, cleared, nil
}

func (l *Log) ListReceiptEnvelopes() ([]ReceiptEnvelope, error) {
	var out []ReceiptEnvelope
	err := l.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(receiptEnvPrefix)
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			var rec ReceiptEnvelope
			if err := it.Item().Value(func(v []byte) error {
				return json.Unmarshal(v, &rec)
			}); err != nil {
				return err
			}
			out = append(out, rec)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
