package files

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/dgraph-io/badger/v4"

	"haoma-frontend/internal/logid"
)

const prefixGapOffer = "gap-offer:"

var ErrGapOfferNotFound = errors.New("files: kept gap-fill offer not found")

type GapOffer struct {
	MsgID string `json:"msg_id"`

	RequesterPeerID string `json:"requester_peer_id"`

	StagingID string `json:"staging_id"`

	Token   string `json:"token"`
	UrlPath string `json:"url_path"`

	Key   []byte `json:"key"`
	Nonce []byte `json:"nonce"`

	BlobSize         uint64 `json:"blob_size"`
	PlaintextSize    uint64 `json:"plaintext_size"`
	Sha256Ciphertext string `json:"sha256_ciphertext"`

	StagedAt int64 `json:"staged_at"`
}

func (o *GapOffer) Wipe() {
	zero(o.Key)
	zero(o.Nonce)
}

func (o GapOffer) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("msg_id", logid.Short(o.MsgID)),
		slog.String("requester_peer_id", logid.Short(o.RequesterPeerID)),
		slog.String("staging_id", logid.Short(o.StagingID)),
		slog.Bool("token_present", o.Token != ""),
		slog.Int("key_bytes", len(o.Key)),
		slog.Int("nonce_bytes", len(o.Nonce)),
		slog.Int64("staged_at", o.StagedAt),
	)
}

func (o GapOffer) String() string {
	return fmt.Sprintf("GapOffer{msg_id=%s requester_peer_id=%s staging_id=%s token_present=%t key_bytes=%d nonce_bytes=%d staged_at=%d}",
		logid.Short(o.MsgID), logid.Short(o.RequesterPeerID), logid.Short(o.StagingID), o.Token != "", len(o.Key), len(o.Nonce), o.StagedAt)
}

func (o GapOffer) GoString() string { return o.String() }

func (o GapOffer) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, o.String()) }

func gapOfferBytes(o GapOffer) ([]byte, error) {
	switch {
	case o.MsgID == "" || strings.Contains(o.MsgID, ":"):
		return nil, errors.New("files: kept gap-fill offer: bad msg id")
	case o.RequesterPeerID == "" || strings.Contains(o.RequesterPeerID, ":"):
		return nil, errors.New("files: kept gap-fill offer: bad requester peer id")
	case o.StagingID == "" || o.StagingID == o.MsgID:
		return nil, errors.New("files: kept gap-fill offer: staging id empty or equal to the msg id")
	case o.Token == "" || o.UrlPath == "":
		return nil, errors.New("files: kept gap-fill offer: no token or url path")
	case len(o.Key) == 0 || len(o.Nonce) == 0:
		return nil, errors.New("files: kept gap-fill offer: no key or nonce")
	case o.Sha256Ciphertext == "":
		return nil, errors.New("files: kept gap-fill offer: no blob hash")
	case o.BlobSize == 0 || o.PlaintextSize == 0 || o.PlaintextSize >= o.BlobSize:
		return nil, errors.New("files: kept gap-fill offer: blob and plaintext sizes do not fit a padded blob")
	case o.StagedAt <= 0:
		return nil, errors.New("files: kept gap-fill offer: no staging time")
	}
	raw, err := json.Marshal(o)
	if err != nil {
		return nil, errors.New("files: kept gap-fill offer will not marshal")
	}
	return raw, nil
}

func (mgr *Manager) RecordGapOffer(o GapOffer) error {
	raw, err := gapOfferBytes(o)
	if err != nil {
		return err
	}
	defer zero(raw)
	for attempt := 0; attempt < updateMetaAttempts; attempt++ {
		err = mgr.st.Update(func(txn *badger.Txn) error {
			m, err := metaTxn(txn, o.MsgID)
			if err != nil {
				return err
			}
			if !containsString(m.SiblingStagingIDs, o.StagingID) {
				m.SiblingStagingIDs = append(m.SiblingStagingIDs, o.StagingID)
			}
			write, err := mgr.metaWrites(m)
			if err != nil {
				return err
			}
			if err := write(txn); err != nil {
				return err
			}
			return txn.Set(gapOfferKey(o.MsgID, o.RequesterPeerID), raw)
		})
		if !errors.Is(err, badger.ErrConflict) {
			return err
		}
	}
	return err
}

func (mgr *Manager) GetGapOffer(msgID, requester string) (GapOffer, error) {
	if msgID == "" || requester == "" {
		return GapOffer{}, ErrGapOfferNotFound
	}
	var o GapOffer
	err := mgr.st.View(func(txn *badger.Txn) error {
		item, err := txn.Get(gapOfferKey(msgID, requester))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrGapOfferNotFound
		}
		if err != nil {
			return err
		}
		return item.Value(func(v []byte) error {
			if err := json.Unmarshal(v, &o); err != nil {
				return errors.New("files: kept gap-fill offer will not decode")
			}
			return nil
		})
	})
	if err != nil {
		o.Wipe()
		return GapOffer{}, err
	}
	return o, nil
}

type GapOfferTime struct {
	MsgID           string `json:"msg_id"`
	RequesterPeerID string `json:"requester_peer_id"`
	StagingID       string `json:"staging_id"`
	StagedAt        int64  `json:"staged_at"`
}

func (mgr *Manager) ListGapOfferTimes() ([]GapOfferTime, error) {
	var out []GapOfferTime
	err := mgr.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefixGapOffer)
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			var o GapOfferTime
			if err := it.Item().Value(func(v []byte) error {
				if err := json.Unmarshal(v, &o); err != nil {
					return errors.New("files: kept gap-fill offer will not decode")
				}
				return nil
			}); err != nil {
				return err
			}
			out = append(out, o)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("files: list kept gap-fill offers: %w", err)
	}
	return out, nil
}

func (mgr *Manager) DeleteGapOffer(msgID, requester, stagingID string) error {
	if msgID == "" || requester == "" || stagingID == "" {
		return nil
	}
	var err error
	for attempt := 0; attempt < updateMetaAttempts; attempt++ {
		err = mgr.st.Update(func(txn *badger.Txn) error {
			key := gapOfferKey(msgID, requester)
			item, err := txn.Get(key)
			if errors.Is(err, badger.ErrKeyNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			var held struct {
				StagingID string `json:"staging_id"`
			}
			if err := item.Value(func(v []byte) error { return json.Unmarshal(v, &held) }); err != nil {
				return errors.New("files: kept gap-fill offer will not decode")
			}
			if held.StagingID != stagingID {
				return nil
			}
			return txn.Delete(key)
		})
		if !errors.Is(err, badger.ErrConflict) {
			return err
		}
	}
	return err
}

func (mgr *Manager) RemoveSiblingStagingID(msgID, stagingID string) error {
	if msgID == "" || stagingID == "" {
		return nil
	}
	err := mgr.UpdateMeta(msgID, func(m *Metadata) error {
		kept := make([]string, 0, len(m.SiblingStagingIDs))
		for _, id := range m.SiblingStagingIDs {
			if id != stagingID {
				kept = append(kept, id)
			}
		}
		m.SiblingStagingIDs = kept
		return nil
	})
	if errors.Is(err, ErrMetaNotFound) {
		return nil
	}
	return err
}

func deleteGapOffersTxn(txn *badger.Txn, msgID string) error {
	opts := badger.DefaultIteratorOptions
	opts.PrefetchValues = false
	opts.Prefix = []byte(prefixGapOffer + msgID + ":")
	it := txn.NewIterator(opts)
	var keys [][]byte
	for it.Rewind(); it.Valid(); it.Next() {
		keys = append(keys, it.Item().KeyCopy(nil))
	}
	it.Close()
	for _, k := range keys {
		if err := txn.Delete(k); err != nil {
			return err
		}
	}
	return nil
}

func metaTxn(txn *badger.Txn, msgID string) (Metadata, error) {
	item, err := txn.Get(metaKey(msgID))
	if errors.Is(err, badger.ErrKeyNotFound) {
		return Metadata{}, ErrMetaNotFound
	}
	if err != nil {
		return Metadata{}, err
	}
	var m Metadata
	if err := item.Value(func(v []byte) error { return json.Unmarshal(v, &m) }); err != nil {
		return Metadata{}, fmt.Errorf("files: decode metadata: %w", err)
	}
	return m, nil
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func gapOfferKey(msgID, requester string) []byte {
	return []byte(prefixGapOffer + msgID + ":" + requester)
}
