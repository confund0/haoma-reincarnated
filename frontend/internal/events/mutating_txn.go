package events

import (
	"fmt"

	badger "github.com/dgraph-io/badger/v4"

	"haoma-frontend/internal/chat"
)

type mutatingTxn struct {
	txn *badger.Txn

	journalled *bool
}

func (m mutatingTxn) get(key []byte) (*badger.Item, error) {
	return m.txn.Get(key)
}

var auxPrefixes = []string{msgIDPrefix, envIDPrefix, receiptEnvPrefix}

func (m mutatingTxn) setAux(key, value []byte) error {
	if !isAuxKey(key) {
		return fmt.Errorf("events: setAux: %q is not one of the three recognised aux keys", key)
	}
	return m.txn.Set(key, value)
}

func (m mutatingTxn) deleteAux(key []byte) error {
	if !isAuxKey(key) {
		return fmt.Errorf("events: deleteAux: %q is not one of the three recognised aux keys", key)
	}
	return m.txn.Delete(key)
}

func isAuxKey(key []byte) bool {
	for _, p := range auxPrefixes {
		if len(key) >= len(p) && string(key[:len(p)]) == p {
			return true
		}
	}
	return false
}

func isRowKey(key []byte) bool {
	return len(key) >= len(prefix) && string(key[:len(prefix)]) == prefix
}

func (m mutatingTxn) storeRow(kind Mutator, chatID chat.ChatID, msgID string, key, raw []byte) error {
	if err := m.txn.Set(key, raw); err != nil {
		return fmt.Errorf("events: store row: %w", err)
	}
	wrote, err := touch(m.txn, kind, chatID, msgID)
	if err != nil {
		return err
	}
	if wrote && m.journalled != nil {
		*m.journalled = true
	}
	return nil
}

func (m mutatingTxn) runAttach(attach TxnWrite) error {
	if attach == nil {
		return nil
	}
	return attach(m.txn)
}
