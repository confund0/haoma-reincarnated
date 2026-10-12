package gapfill

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	badger "github.com/dgraph-io/badger/v4"

	"haoma-frontend/internal/store"
)

const (
	watermarkPrefix = "gapfill-wm:"
	ackPrefix       = "gapfill-ack:"
)

type Watermarks struct {
	st *store.Store
}

func NewWatermarks(st *store.Store) *Watermarks {
	return &Watermarks{st: st}
}

func (w *Watermarks) Get(siblingPeerID string) (uint64, error) {
	if w == nil || w.st == nil {
		return 0, errors.New("gapfill: watermark store not wired")
	}
	if siblingPeerID == "" {
		return 0, errors.New("gapfill: watermark needs a sibling peer id")
	}
	raw, err := w.st.Get(key(watermarkPrefix, siblingPeerID))
	if errors.Is(err, store.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("gapfill: read watermark: %w", err)
	}
	if len(raw) != 8 {

		return 0, nil
	}
	return binary.BigEndian.Uint64(raw), nil
}

func (w *Watermarks) Advance(siblingPeerID string, seq uint64) (bool, error) {
	if w == nil || w.st == nil {
		return false, errors.New("gapfill: watermark store not wired")
	}
	if siblingPeerID == "" {
		return false, errors.New("gapfill: watermark needs a sibling peer id")
	}
	cur, err := w.Get(siblingPeerID)
	if err != nil {
		return false, err
	}
	if seq <= cur {
		return false, nil
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], seq)
	if err := w.st.Put(key(watermarkPrefix, siblingPeerID), buf[:]); err != nil {
		return false, fmt.Errorf("gapfill: write watermark: %w", err)
	}
	return true, nil
}

func (w *Watermarks) Forget(siblingPeerID string) error {
	if w == nil || w.st == nil {
		return errors.New("gapfill: watermark store not wired")
	}
	for _, prefix := range []string{watermarkPrefix, ackPrefix} {
		if err := w.st.Delete(key(prefix, siblingPeerID)); err != nil && !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("gapfill: forget watermark: %w", err)
		}
	}
	return nil
}

func (w *Watermarks) AckedBy(siblingPeerID string) (uint64, error) {
	return w.read(ackPrefix, siblingPeerID)
}

func (w *Watermarks) RecordAck(siblingPeerID string, seq uint64) (bool, error) {
	if w == nil || w.st == nil {
		return false, errors.New("gapfill: watermark store not wired")
	}
	if siblingPeerID == "" {
		return false, errors.New("gapfill: watermark needs a sibling peer id")
	}
	cur, err := w.AckedBy(siblingPeerID)
	if err != nil {
		return false, err
	}
	if seq <= cur {
		return false, nil
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], seq)
	if err := w.st.Put(key(ackPrefix, siblingPeerID), buf[:]); err != nil {
		return false, fmt.Errorf("gapfill: write acknowledgement: %w", err)
	}
	return true, nil
}

func (w *Watermarks) read(prefix, siblingPeerID string) (uint64, error) {
	if w == nil || w.st == nil {
		return 0, errors.New("gapfill: watermark store not wired")
	}
	if siblingPeerID == "" {
		return 0, errors.New("gapfill: watermark needs a sibling peer id")
	}
	raw, err := w.st.Get(key(prefix, siblingPeerID))
	if errors.Is(err, store.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("gapfill: read cursor: %w", err)
	}
	if len(raw) != 8 {
		return 0, nil
	}
	return binary.BigEndian.Uint64(raw), nil
}

func (w *Watermarks) AllAcks() (map[string]uint64, error) {
	if w == nil || w.st == nil {
		return nil, errors.New("gapfill: watermark store not wired")
	}
	out := map[string]uint64{}
	err := w.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(ackPrefix)
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.ValidForPrefix(opts.Prefix); it.Next() {
			item := it.Item()
			peerID := strings.TrimPrefix(string(item.Key()), ackPrefix)
			raw, err := item.ValueCopy(nil)
			if err != nil {
				return err
			}
			if len(raw) != 8 || peerID == "" {
				continue
			}
			out[peerID] = binary.BigEndian.Uint64(raw)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("gapfill: scan acknowledgements: %w", err)
	}
	return out, nil
}

func key(prefix, siblingPeerID string) []byte {
	return []byte(prefix + siblingPeerID)
}
