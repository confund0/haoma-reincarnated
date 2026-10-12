package linkeddevice

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/dgraph-io/badger/v4"

	"haoma-frontend/internal/crdt"
	"haoma-frontend/internal/store"
)

const (
	prefix     = "linkeddevice:"
	singleton  = prefix + "self"
	maxRetries = 256
)

type Store struct {
	st *store.Store

	beforeGrow func() error
}

func (s *Store) SetBeforeGrow(fn func() error) { s.beforeGrow = fn }

func (s *Store) runBeforeGrow() error {
	if s.beforeGrow == nil {
		return nil
	}
	return s.beforeGrow()
}

func New(st *store.Store) *Store { return &Store{st: st} }

func (s *Store) Get() (crdt.TwoPSet, error) {
	var set crdt.TwoPSet
	err := s.st.View(func(txn *badger.Txn) error {
		var err error
		set, err = load(txn)
		return err
	})
	if err != nil {
		return crdt.TwoPSet{}, fmt.Errorf("linkeddevice: get: %w", err)
	}
	return set, nil
}

func (s *Store) Members() ([]string, error) {
	set, err := s.Get()
	if err != nil {
		return nil, err
	}
	return set.Members(), nil
}

func (s *Store) Link(peerID string, ts int64) (changed bool, err error) {
	if peerID == "" {
		return false, errors.New("linkeddevice: empty peer id")
	}
	if err := s.runBeforeGrow(); err != nil {
		return false, fmt.Errorf("linkeddevice: link: %w", err)
	}
	changed, err = s.mutate(func(set *crdt.TwoPSet) {
		set.Add(peerID, ts)
	})
	if err != nil {
		return false, err
	}
	slog.Debug("linkeddevice: link",
		slog.String("peer_id", peerID),
		slog.Int64("ts", ts),
		slog.Bool("changed", changed),
	)
	return changed, nil
}

func (s *Store) Unlink(peerID string, ts int64) (changed bool, err error) {
	if peerID == "" {
		return false, errors.New("linkeddevice: empty peer id")
	}
	changed, err = s.mutate(func(set *crdt.TwoPSet) {
		set.Remove(peerID, ts)
	})
	if err != nil {
		return false, err
	}
	slog.Debug("linkeddevice: unlink",
		slog.String("peer_id", peerID),
		slog.Int64("ts", ts),
		slog.Bool("changed", changed),
	)
	return changed, nil
}

func (s *Store) Merge(other crdt.TwoPSet) (changed bool, err error) {
	if err := s.runBeforeGrow(); err != nil {
		return false, fmt.Errorf("linkeddevice: merge: %w", err)
	}
	changed, err = s.mutate(func(set *crdt.TwoPSet) {
		set.Merge(other)
	})
	if err != nil {
		return false, err
	}
	slog.Debug("linkeddevice: merge",
		slog.Int("incoming_added", len(other.Added)),
		slog.Int("incoming_removed", len(other.Removed)),
		slog.Bool("changed", changed),
	)
	return changed, nil
}

func (s *Store) mutate(fn func(*crdt.TwoPSet)) (bool, error) {
	var changed bool
	for attempt := 0; attempt < maxRetries; attempt++ {
		err := s.st.Update(func(txn *badger.Txn) error {
			set, err := load(txn)
			if err != nil {
				return err
			}
			before := membershipKey(set)
			fn(&set)
			changed = membershipKey(set) != before
			raw, err := json.Marshal(set)
			if err != nil {
				return err
			}
			return txn.Set([]byte(singleton), raw)
		})
		if err == nil {
			return changed, nil
		}
		if !errors.Is(err, badger.ErrConflict) {
			return false, fmt.Errorf("linkeddevice: mutate: %w", err)
		}
	}
	return false, fmt.Errorf("linkeddevice: mutate: exceeded %d conflict retries", maxRetries)
}

func load(txn *badger.Txn) (crdt.TwoPSet, error) {
	item, err := txn.Get([]byte(singleton))
	if errors.Is(err, badger.ErrKeyNotFound) {
		return crdt.NewTwoPSet(), nil
	}
	if err != nil {
		return crdt.TwoPSet{}, err
	}
	var set crdt.TwoPSet
	err = item.Value(func(raw []byte) error {
		if len(raw) == 0 {
			return nil
		}
		return json.Unmarshal(raw, &set)
	})
	return set, err
}

func membershipKey(set crdt.TwoPSet) string {
	members := set.Members()
	out := make([]byte, 0, 16*len(members))
	for _, m := range members {
		out = append(out, m...)
		out = append(out, 0)
	}
	return string(out)
}
