package files

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dgraph-io/badger/v4"

	"haoma-frontend/internal/chat"
)

const prefixWanted = "gap-wanted:"

const keyWantedFilled = "gap-wanted-filled"

const MaxGapAsks = 10

const FailIntegrity = "integrity"

type WantedFile struct {
	MsgID  string      `json:"msg_id"`
	ChatID chat.ChatID `json:"chat_id"`

	DisplayTs int64 `json:"display_ts"`

	AskedAt int64 `json:"asked_at,omitempty"`

	Asks int `json:"asks,omitempty"`
}

func (mgr *Manager) AddWantedFile(w WantedFile) (bool, error) {
	if w.MsgID == "" || w.ChatID == "" {
		return false, errors.New("files: wanted file: empty msg id or chat id")
	}
	raw, err := json.Marshal(w)
	if err != nil {
		return false, fmt.Errorf("files: marshal wanted file: %w", err)
	}
	var added bool
	for attempt := 0; attempt < updateMetaAttempts; attempt++ {
		added = false
		err = mgr.st.Update(func(txn *badger.Txn) error {
			for _, key := range [][]byte{metaKey(w.MsgID), wantedKey(w.MsgID)} {
				_, gErr := txn.Get(key)
				switch {
				case gErr == nil:
					return nil
				case !errors.Is(gErr, badger.ErrKeyNotFound):
					return gErr
				}
			}
			added = true
			return txn.Set(wantedKey(w.MsgID), raw)
		})
		if !errors.Is(err, badger.ErrConflict) {
			break
		}
	}
	if err != nil {
		return false, err
	}
	return added, nil
}

func (mgr *Manager) ListWantedFiles() ([]WantedFile, error) {
	var out []WantedFile
	err := mgr.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefixWanted)
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			var w WantedFile
			if err := it.Item().Value(func(v []byte) error {
				return json.Unmarshal(v, &w)
			}); err != nil {
				return err
			}
			out = append(out, w)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("files: list wanted files: %w", err)
	}
	return out, nil
}

func (mgr *Manager) MarkWantedAsked(msgIDs []string, at int64) error {
	return mgr.updateWanted(msgIDs, func(w *WantedFile) { w.AskedAt = at })
}

func (mgr *Manager) ResetWantedAsks() (int, error) {
	all, err := mgr.ListWantedFiles()
	if err != nil {
		return 0, err
	}
	ids := make([]string, 0, len(all))
	for _, w := range all {
		ids = append(ids, w.MsgID)
	}
	if err := mgr.updateWanted(ids, func(w *WantedFile) { w.Asks, w.AskedAt = 0, 0 }); err != nil {
		return 0, err
	}
	return len(ids), nil
}

func (mgr *Manager) updateWanted(msgIDs []string, fn func(w *WantedFile)) error {
	for _, id := range msgIDs {
		var err error
		for attempt := 0; attempt < updateMetaAttempts; attempt++ {
			err = mgr.st.Update(func(txn *badger.Txn) error {
				item, gErr := txn.Get(wantedKey(id))
				if errors.Is(gErr, badger.ErrKeyNotFound) {
					return nil
				}
				if gErr != nil {
					return gErr
				}
				var w WantedFile
				if vErr := item.Value(func(v []byte) error { return json.Unmarshal(v, &w) }); vErr != nil {
					return fmt.Errorf("files: decode wanted file: %w", vErr)
				}
				fn(&w)
				raw, mErr := json.Marshal(w)
				if mErr != nil {
					return fmt.Errorf("files: marshal wanted file: %w", mErr)
				}
				return txn.Set(wantedKey(id), raw)
			})
			if !errors.Is(err, badger.ErrConflict) {
				break
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (mgr *Manager) DeleteWantedFile(msgID string) error {
	if msgID == "" {
		return nil
	}
	return mgr.st.Update(func(txn *badger.Txn) error {
		return deleteWantedTxn(txn, msgID)
	})
}

func deleteWantedTxn(txn *badger.Txn, msgID string) error {
	err := txn.Delete(wantedKey(msgID))
	if errors.Is(err, badger.ErrKeyNotFound) {
		return nil
	}
	return err
}

func (mgr *Manager) deleteWantedByChat(chatID chat.ChatID) error {
	all, err := mgr.ListWantedFiles()
	if err != nil {
		return err
	}
	for _, w := range all {
		if w.ChatID != chatID {
			continue
		}
		if err := mgr.DeleteWantedFile(w.MsgID); err != nil {
			return err
		}
	}
	return nil
}

func (mgr *Manager) WantedFilled() (bool, error) {
	err := mgr.st.View(func(txn *badger.Txn) error {
		_, gErr := txn.Get([]byte(keyWantedFilled))
		return gErr
	})
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, badger.ErrKeyNotFound):
		return false, nil
	default:
		return false, fmt.Errorf("files: read wanted-set scan marker: %w", err)
	}
}

func (mgr *Manager) MarkWantedFilled() error {
	return mgr.st.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(keyWantedFilled), []byte{1})
	})
}

func (mgr *Manager) TakeGapSiblingOffer(m Metadata, o SiblingOffer) error {
	if o.MsgID != m.MsgID {
		return errors.New("files: TakeGapSiblingOffer: msg ids differ")
	}
	if !m.GapFill || !m.FetchedFromSibling {
		return errors.New("files: TakeGapSiblingOffer: not a gap-fill take")
	}
	offerRaw, err := siblingOfferBytes(o)
	if err != nil {
		return err
	}
	defer zero(offerRaw)
	return mgr.putIfAbsent(m.MsgID, func(txn *badger.Txn) error {
		taken := m
		taken.GapAsks = 1
		item, gErr := txn.Get(wantedKey(m.MsgID))
		switch {
		case gErr == nil:
			var w WantedFile
			if vErr := item.Value(func(v []byte) error { return json.Unmarshal(v, &w) }); vErr != nil {
				return fmt.Errorf("files: decode wanted file: %w", vErr)
			}
			taken.GapAsks = w.Asks + 1
		case !errors.Is(gErr, badger.ErrKeyNotFound):
			return gErr
		}
		metaWrite, wErr := mgr.metaWrites(taken)
		if wErr != nil {
			return wErr
		}
		if err := metaWrite(txn); err != nil {
			return err
		}
		if err := txn.Set(siblingOfferKey(o.MsgID), offerRaw); err != nil {
			return err
		}
		return txn.Set(siblingOfferByStagingKey(o.StagingID), []byte(o.MsgID))
	})
}

var ErrNotGapFill = errors.New("files: not a gap-fill download")

var ErrGapFileSettled = errors.New("files: gap-fill download already settled")

func (mgr *Manager) RequeueGapFile(msgID string, displayTs int64, asks func(Metadata) int) (Metadata, error) {
	if msgID == "" {
		return Metadata{}, ErrMetaNotFound
	}
	var (
		dropped Metadata
		err     error
	)
	for attempt := 0; attempt < updateMetaAttempts; attempt++ {
		err = mgr.st.Update(func(txn *badger.Txn) error {
			m, gErr := metaTxn(txn, msgID)
			if gErr != nil {
				return gErr
			}
			if !m.GapFill || !m.FetchedFromSibling {
				return ErrNotGapFill
			}
			if m.State == StateReady || m.State == StateExpired {
				return ErrGapFileSettled
			}
			deleted, found, dErr := deleteMetaTxn(txn, msgID)
			if dErr != nil {
				return dErr
			}
			if !found {
				return ErrMetaNotFound
			}
			raw, mErr := json.Marshal(WantedFile{
				MsgID:     msgID,
				ChatID:    deleted.ChatID,
				DisplayTs: displayTs,
				Asks:      asks(deleted),
			})
			if mErr != nil {
				return fmt.Errorf("files: marshal wanted file: %w", mErr)
			}
			dropped = deleted
			return txn.Set(wantedKey(msgID), raw)
		})
		if !errors.Is(err, badger.ErrConflict) {
			break
		}
	}
	if err != nil {
		return Metadata{}, err
	}
	return dropped, nil
}

func (mgr *Manager) ListGapFillFailed() ([]Metadata, error) {
	var out []Metadata
	err := mgr.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefixMeta)
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			var m Metadata
			if err := it.Item().Value(func(v []byte) error {
				return json.Unmarshal(v, &m)
			}); err != nil {
				return err
			}
			if m.GapFill && m.FetchedFromSibling && m.State == StateFailedPermanent {
				out = append(out, m)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("files: list failed gap-fill downloads: %w", err)
	}
	return out, nil
}

func wantedKey(msgID string) []byte {
	return []byte(prefixWanted + msgID)
}
