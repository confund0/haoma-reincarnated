package outbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/dgraph-io/badger/v4"

	"haoma/internal/store"
	"haoma/internal/xport"
)

const xqueuePrefix = "xport-queue:"

type xqueueEntry struct {
	Dest     string         `json:"dest"`
	Envelope xport.Envelope `json:"envelope"`
	Attempts int            `json:"attempts"`
	FirstAt  int64          `json:"first_at"`
}

func Migrate(st *store.Store, now time.Time) (int, error) {
	type pending struct {
		oldKey []byte
		e      xqueueEntry
	}
	var rows []pending

	err := st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(xqueuePrefix)
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			var e xqueueEntry
			if err := item.Value(func(v []byte) error {
				return json.Unmarshal(v, &e)
			}); err != nil {
				return fmt.Errorf("outbox migrate: decode queue entry: %w", err)
			}
			rows = append(rows, pending{oldKey: item.KeyCopy(nil), e: e})
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}

	migrated := 0
	for _, p := range rows {
		if p.e.Envelope.ID == "" {
			continue
		}
		mainKey := makeRowKey(p.e.Envelope.ID)

		row := OutboxRow{
			EnvelopeID:     p.e.Envelope.ID,
			Dest:           p.e.Dest,
			Envelope:       p.e.Envelope,
			State:          StateEnqueued,
			Attempts:       p.e.Attempts,
			FirstAt:        p.e.FirstAt * int64(time.Second),
			NextAttemptAt:  now.UnixNano(),
			StateChangedAt: now.UnixNano(),
		}
		raw, err := json.Marshal(row)
		if err != nil {
			return migrated, err
		}
		dueKey := makeDueKey(now, row.EnvelopeID)
		stKey := makeStateKey(StateEnqueued, row.EnvelopeID)

		err = st.Update(func(txn *badger.Txn) error {

			if _, err := txn.Get(mainKey); err == nil {
				return nil
			} else if !errors.Is(err, badger.ErrKeyNotFound) {
				return err
			}
			if err := txn.Set(mainKey, raw); err != nil {
				return err
			}
			if err := txn.Set(dueKey, []byte(row.EnvelopeID)); err != nil {
				return err
			}
			if err := txn.Set(stKey, nil); err != nil {
				return err
			}
			return txn.Delete(p.oldKey)
		})
		if err != nil {
			return migrated, err
		}
		migrated++
	}
	return migrated, nil
}
