package events

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	badger "github.com/dgraph-io/badger/v4"

	"haoma-frontend/internal/chat"
)

const (
	journalPrefix = "evt-jrn:"

	journalIDPrefix = "evt-jrnid:"

	keyNextJournalSeq = "events:next_journal_seq"
)

type JournalEntry struct {
	Seq uint64 `json:"-"`

	MsgID  string      `json:"msg_id"`
	ChatID chat.ChatID `json:"chat_id"`
}

func journalTouch(txn *badger.Txn, chatID chat.ChatID, msgID string) (wrote bool, err error) {
	if msgID == "" {
		return false, nil
	}

	idKey := []byte(journalIDPrefix + msgID)
	switch prevItem, err := txn.Get(idKey); {
	case err == nil:
		prevSeqBytes, cErr := prevItem.ValueCopy(nil)
		if cErr != nil {
			return false, fmt.Errorf("events: journal read previous seq: %w", cErr)
		}
		if len(prevSeqBytes) == 8 {
			if dErr := txn.Delete(journalKey(binary.BigEndian.Uint64(prevSeqBytes))); dErr != nil {
				return false, fmt.Errorf("events: journal drop previous entry: %w", dErr)
			}
		}
	case errors.Is(err, badger.ErrKeyNotFound):

	default:
		return false, fmt.Errorf("events: journal reverse-index read: %w", err)
	}

	seq, err := nextJournalSeq(txn)
	if err != nil {
		return false, err
	}

	raw, err := json.Marshal(JournalEntry{MsgID: msgID, ChatID: chatID})
	if err != nil {
		return false, fmt.Errorf("events: journal marshal entry: %w", err)
	}
	if err := txn.Set(journalKey(seq), raw); err != nil {
		return false, fmt.Errorf("events: journal write entry: %w", err)
	}
	var seqBuf [8]byte
	binary.BigEndian.PutUint64(seqBuf[:], seq)
	if err := txn.Set(idKey, seqBuf[:]); err != nil {
		return false, fmt.Errorf("events: journal write reverse index: %w", err)
	}
	return true, nil
}

var (
	pendingJournalTouchesMu sync.Mutex
	pendingJournalTouches   = map[*badger.Txn][]journalTouchLogEntry{}
)

type journalTouchLogEntry struct {
	mutator Mutator
	msgID   string
}

func recordJournalTouch(txn *badger.Txn, m Mutator, msgID string) {
	if !slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		return
	}
	pendingJournalTouchesMu.Lock()
	pendingJournalTouches[txn] = append(pendingJournalTouches[txn], journalTouchLogEntry{mutator: m, msgID: msgID})
	pendingJournalTouchesMu.Unlock()
}

func logJournalTouches(txn *badger.Txn) {
	pendingJournalTouchesMu.Lock()
	entries := pendingJournalTouches[txn]
	delete(pendingJournalTouches, txn)
	pendingJournalTouchesMu.Unlock()
	for _, e := range entries {
		slog.Debug("events: journal touch",
			slog.String("mutator", string(e.mutator)),
			slog.String("msg_id", e.msgID),
		)
	}
}

func discardJournalTouches(txn *badger.Txn) {
	pendingJournalTouchesMu.Lock()
	delete(pendingJournalTouches, txn)
	pendingJournalTouchesMu.Unlock()
}

func nextJournalSeq(txn *badger.Txn) (uint64, error) {
	key := []byte(keyNextJournalSeq)
	cur, err := loadUint64(txn, key)
	if err != nil {
		return 0, fmt.Errorf("events: journal counter read: %w", err)
	}
	next := cur + 1
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], next)
	if err := txn.Set(key, buf[:]); err != nil {
		return 0, fmt.Errorf("events: journal counter write: %w", err)
	}
	return next, nil
}

func journalKey(seq uint64) []byte {
	key := make([]byte, 0, len(journalPrefix)+8)
	key = append(key, journalPrefix...)
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], seq)
	return append(key, buf[:]...)
}

func (l *Log) JournalSince(afterSeq uint64, limit int) ([]JournalEntry, error) {
	if limit <= 0 {
		limit = defaultJournalBatch
	}
	var out []JournalEntry
	err := l.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(journalPrefix)
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek(journalKey(afterSeq + 1)); it.ValidForPrefix(opts.Prefix); it.Next() {
			if len(out) >= limit {
				break
			}
			item := it.Item()
			key := item.Key()
			if len(key) != len(journalPrefix)+8 {
				continue
			}
			raw, err := item.ValueCopy(nil)
			if err != nil {
				return err
			}
			var entry JournalEntry
			if err := json.Unmarshal(raw, &entry); err != nil {

				continue
			}
			entry.Seq = binary.BigEndian.Uint64(key[len(journalPrefix):])
			out = append(out, entry)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("events: journal scan: %w", err)
	}
	return out, nil
}

const defaultJournalBatch = 100

func (l *Log) JournalHead() (uint64, error) {
	var v uint64
	err := l.st.View(func(txn *badger.Txn) error {
		var err error
		v, err = loadUint64(txn, []byte(keyNextJournalSeq))
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("events: journal head: %w", err)
	}
	return v, nil
}

func (l *Log) JournalPrune(throughSeq uint64) (int, error) {
	if throughSeq == 0 {
		return 0, nil
	}
	pruned := 0
	for {
		var batch []JournalEntry
		if err := l.st.View(func(txn *badger.Txn) error {
			opts := badger.DefaultIteratorOptions
			opts.Prefix = []byte(journalPrefix)
			it := txn.NewIterator(opts)
			defer it.Close()

			for it.Seek(journalKey(1)); it.ValidForPrefix(opts.Prefix); it.Next() {
				if len(batch) >= deleteBatchSize {
					break
				}
				item := it.Item()
				key := item.Key()
				if len(key) != len(journalPrefix)+8 {
					continue
				}
				seq := binary.BigEndian.Uint64(key[len(journalPrefix):])
				if seq > throughSeq {
					break
				}
				raw, err := item.ValueCopy(nil)
				if err != nil {
					return err
				}
				entry := JournalEntry{Seq: seq}

				_ = json.Unmarshal(raw, &entry)
				entry.Seq = seq
				batch = append(batch, entry)
			}
			return nil
		}); err != nil {
			return pruned, fmt.Errorf("events: journal prune scan: %w", err)
		}
		if len(batch) == 0 {
			return pruned, nil
		}
		if err := l.st.Update(func(txn *badger.Txn) error {
			for _, entry := range batch {
				if err := txn.Delete(journalKey(entry.Seq)); err != nil {
					return err
				}
				if entry.MsgID == "" {
					continue
				}

				idKey := []byte(journalIDPrefix + entry.MsgID)
				item, err := txn.Get(idKey)
				if errors.Is(err, badger.ErrKeyNotFound) {
					continue
				}
				if err != nil {
					return err
				}
				cur, err := item.ValueCopy(nil)
				if err != nil {
					return err
				}
				if len(cur) == 8 && binary.BigEndian.Uint64(cur) == entry.Seq {
					if err := txn.Delete(idKey); err != nil {
						return err
					}
				}
			}
			return nil
		}); err != nil {
			return pruned, fmt.Errorf("events: journal prune delete: %w", err)
		}
		pruned += len(batch)
		if len(batch) < deleteBatchSize {
			return pruned, nil
		}
	}
}
