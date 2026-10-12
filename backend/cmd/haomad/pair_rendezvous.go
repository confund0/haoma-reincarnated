package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/dgraph-io/badger/v4"

	"haoma/internal/pair"
	"haoma/internal/store"
)

const rendezvousPrefix = "pair-rendezvous:"

type storedRendezvous struct {
	HandleID  string `json:"handle_id"`
	Secret    string `json:"secret"`
	Payload   []byte `json:"payload"`
	ExpiresAt int64  `json:"expires_at"`
	CreatedAt int64  `json:"created_at"`
}

type rendezvousStore struct {
	st *store.Store
}

func newRendezvousStore(st *store.Store) *rendezvousStore { return &rendezvousStore{st: st} }

func (s *rendezvousStore) Put(r storedRendezvous) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("pair: encode stored rendezvous: %w", err)
	}
	return s.st.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(rendezvousPrefix+r.HandleID), raw)
	})
}

func (s *rendezvousStore) Delete(handleID string) error {
	return s.st.Update(func(txn *badger.Txn) error {
		return txn.Delete([]byte(rendezvousPrefix + handleID))
	})
}

func (s *rendezvousStore) LoadLive(now time.Time) ([]storedRendezvous, error) {
	nowUnix := now.Unix()
	var (
		live      []storedRendezvous
		toDelete  [][]byte
		undecoded int
	)
	err := s.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(rendezvousPrefix)
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			var r storedRendezvous
			if err := item.Value(func(v []byte) error {
				return json.Unmarshal(v, &r)
			}); err != nil {
				undecoded++
				toDelete = append(toDelete, item.KeyCopy(nil))
				continue
			}
			if r.ExpiresAt <= nowUnix {
				toDelete = append(toDelete, item.KeyCopy(nil))
				continue
			}
			live = append(live, r)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("pair: read stored rendezvous: %w", err)
	}
	if undecoded > 0 {
		slog.Warn("pair: dropped stored rendezvous that no longer decode",
			slog.Int("n", undecoded),
		)
	}
	if len(toDelete) == 0 {
		return live, nil
	}
	err = s.st.Update(func(txn *badger.Txn) error {
		for _, k := range toDelete {
			if err := txn.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return live, fmt.Errorf("pair: drop lapsed rendezvous: %w", err)
	}
	slog.Info("pair: dropped lapsed long-lived rendezvous on startup",
		slog.Int("n", len(toDelete)),
		slog.Int("live", len(live)),
	)
	return live, nil
}

func (d *daemon) persistRendezvous(handleID, secret string, payload []byte, expiresAt int64) error {
	if d.rendezvous == nil {
		return errors.New("pair: rendezvous store not initialised")
	}
	if err := d.rendezvous.Put(storedRendezvous{
		HandleID:  handleID,
		Secret:    secret,
		Payload:   payload,
		ExpiresAt: expiresAt,
		CreatedAt: time.Now().Unix(),
	}); err != nil {
		return err
	}
	slog.Debug("pair: long-lived rendezvous stored",
		slog.String("handle_id", handleID),
		slog.Int64("expires_at", expiresAt),
		slog.Int("payload_bytes", len(payload)),
	)
	return nil
}

func (d *daemon) dropRendezvousWhenDone(ctx context.Context, handleID string, pi pair.PendingInvite) {
	_, err := pi.Wait(ctx)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		slog.Debug("pair: long-lived rendezvous kept for the next start",
			slog.String("handle_id", handleID),
		)
		return
	}
	reason := "answered"
	switch {
	case errors.Is(err, pair.ErrCancelled):
		reason = "cancelled"
	case errors.Is(err, pair.ErrTimedOut):
		reason = "expired"
	case err != nil:
		reason = "failed"
	}
	if d.rendezvous == nil {
		return
	}
	if derr := d.rendezvous.Delete(handleID); derr != nil {
		slog.Warn("pair: dropping a finished long-lived rendezvous failed; it may be republished once more",
			slog.String("handle_id", handleID),
			slog.String("reason", reason),
			slog.Any("err", derr),
		)
		return
	}
	slog.Info("pair: long-lived rendezvous dropped",
		slog.String("handle_id", handleID),
		slog.String("reason", reason),
	)
}

func (d *daemon) republishStoredRendezvous() error {
	if d.rendezvous == nil {
		return nil
	}
	rows, err := d.rendezvous.LoadLive(time.Now())
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		slog.Debug("pair: no long-lived rendezvous to republish")
		return nil
	}
	driver, err := d.ensureOnionDriver()
	if err != nil {
		return err
	}

	republished := make([]*onionInviteEntry, 0, len(rows))
	for _, r := range rows {
		if _, live := d.onionInvites.get(r.HandleID); live {

			continue
		}
		remaining := time.Until(time.Unix(r.ExpiresAt, 0))
		if remaining <= 0 {
			continue
		}
		pi, cerr := driver.CreateInvite(d.bgCtx, pair.CreateRequest{
			Payload: r.Payload,
			Secret:  r.Secret,
			Timeout: remaining,
		})
		if cerr != nil {
			slog.Warn("pair: republishing a long-lived rendezvous failed; it stays stored for the next start",
				slog.String("handle_id", r.HandleID),
				slog.Any("err", cerr),
			)
			continue
		}
		entry := &onionInviteEntry{
			HandleID:  r.HandleID,
			Secret:    r.Secret,
			ExpiresAt: pi.ExpiresAt(),
			Pending:   pi,
			CreatedAt: time.Unix(r.CreatedAt, 0),
		}
		d.onionInvites.put(entry)
		go d.dropRendezvousWhenDone(d.bgCtx, r.HandleID, pi)
		republished = append(republished, entry)
		slog.Info("pair: long-lived rendezvous republished",
			slog.String("handle_id", r.HandleID),
			slog.Int64("expires_at", entry.ExpiresAt),
		)
	}

	if len(republished) > 0 {
		go func() {
			for _, e := range republished {
				probeOnionInvite(d.bgCtx, d, e.HandleID, nil, e.Secret, e.Pending)
			}
		}()
	}
	return nil
}
