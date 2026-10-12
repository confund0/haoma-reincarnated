package files

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/dgraph-io/badger/v4"

	"haoma-frontend/internal/logid"
)

const prefixSiblingOffer = "sibling-offer:"

const prefixSiblingOfferByStaging = "sibling-offer-by-staging:"

var ErrSiblingOfferNotFound = errors.New("files: sibling offer not found")

type SiblingOffer struct {
	MsgID string `json:"msg_id"`

	ServingPeerID string `json:"serving_peer_id"`

	StagingID string `json:"staging_id"`

	Token   string `json:"token"`
	UrlPath string `json:"url_path"`

	Key   []byte `json:"key"`
	Nonce []byte `json:"nonce"`

	BlobSize      uint64 `json:"blob_size"`
	PlaintextSize uint64 `json:"plaintext_size"`

	Sha256Ciphertext string `json:"sha256_ciphertext"`
}

func (o SiblingOffer) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("msg_id", logid.Short(o.MsgID)),
		slog.String("serving_peer_id", logid.Short(o.ServingPeerID)),
		slog.String("staging_id", logid.Short(o.StagingID)),
		slog.Bool("token_present", o.Token != ""),
		slog.Int("key_bytes", len(o.Key)),
		slog.Int("nonce_bytes", len(o.Nonce)),
	)
}

func (o SiblingOffer) String() string {
	return fmt.Sprintf("SiblingOffer{msg_id=%s serving_peer_id=%s token_present=%t key_bytes=%d nonce_bytes=%d}",
		logid.Short(o.MsgID), logid.Short(o.ServingPeerID), o.Token != "", len(o.Key), len(o.Nonce))
}

func (o SiblingOffer) GoString() string { return o.String() }

func (o SiblingOffer) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, o.String()) }

func (mgr *Manager) PutMetaAndSiblingOffer(m Metadata, o SiblingOffer) error {
	if o.MsgID != m.MsgID {
		return errors.New("files: PutMetaAndSiblingOffer: msg ids differ")
	}
	metaWrite, err := mgr.metaWrites(m)
	if err != nil {
		return err
	}
	offerRaw, err := siblingOfferBytes(o)
	if err != nil {
		return err
	}
	defer zero(offerRaw)
	return mgr.putIfAbsent(m.MsgID, func(txn *badger.Txn) error {
		if err := metaWrite(txn); err != nil {
			return err
		}
		if err := txn.Set(siblingOfferKey(o.MsgID), offerRaw); err != nil {
			return err
		}
		return txn.Set(siblingOfferByStagingKey(o.StagingID), []byte(o.MsgID))
	})
}

func siblingOfferBytes(o SiblingOffer) ([]byte, error) {
	switch {
	case o.MsgID == "":
		return nil, errors.New("files: sibling offer: empty msg id")
	case o.ServingPeerID == "":
		return nil, errors.New("files: sibling offer: empty serving peer id")
	case o.StagingID == "" || o.StagingID == o.MsgID:
		return nil, errors.New("files: sibling offer: staging id empty or equal to the msg id")
	case o.Token == "" || o.UrlPath == "":
		return nil, errors.New("files: sibling offer: no token or url path")
	case len(o.Key) == 0 || len(o.Nonce) == 0:
		return nil, errors.New("files: sibling offer: no key or nonce")
	case o.Sha256Ciphertext == "":
		return nil, errors.New("files: sibling offer: no blob hash")
	case o.BlobSize == 0 || o.PlaintextSize == 0 || o.PlaintextSize >= o.BlobSize:
		return nil, errors.New("files: sibling offer: blob and plaintext sizes do not fit a padded blob")
	}
	raw, err := json.Marshal(o)
	if err != nil {
		return nil, fmt.Errorf("files: marshal sibling offer: %w", err)
	}
	return raw, nil
}

var ErrSiblingTakeReplaced = errors.New("files: a newer offer was taken while the file was being sealed")

var ErrSiblingFileGone = errors.New("files: the file was deleted while it was being sealed")

func (mgr *Manager) MarkSiblingFileSealed(m Metadata) error {
	write, err := mgr.metaWrites(m)
	if err != nil {
		return err
	}
	return mgr.st.Update(func(txn *badger.Txn) error {
		for _, key := range [][]byte{metaKey(m.MsgID), siblingOfferKey(m.MsgID)} {
			if _, err := txn.Get(key); errors.Is(err, badger.ErrKeyNotFound) {
				return ErrSiblingFileGone
			} else if err != nil {
				return err
			}
		}
		held, err := metaTxn(txn, m.MsgID)
		if err != nil {
			return err
		}
		if held.StagingID != m.StagingID {
			return ErrSiblingTakeReplaced
		}
		return write(txn)
	})
}

func (mgr *Manager) SiblingOfferMsgID(stagingID string) (string, error) {
	if stagingID == "" {
		return "", ErrSiblingOfferNotFound
	}
	var msgID string
	err := mgr.st.View(func(txn *badger.Txn) error {
		item, err := txn.Get(siblingOfferByStagingKey(stagingID))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrSiblingOfferNotFound
		}
		if err != nil {
			return err
		}
		return item.Value(func(v []byte) error {
			msgID = string(v)
			return nil
		})
	})
	return msgID, err
}

func (mgr *Manager) GetSiblingOffer(msgID string) (SiblingOffer, error) {
	if msgID == "" {
		return SiblingOffer{}, ErrSiblingOfferNotFound
	}
	var o SiblingOffer
	err := mgr.st.View(func(txn *badger.Txn) error {
		item, err := txn.Get(siblingOfferKey(msgID))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrSiblingOfferNotFound
		}
		if err != nil {
			return err
		}
		return item.Value(func(v []byte) error {
			return json.Unmarshal(v, &o)
		})
	})
	if err != nil {
		return SiblingOffer{}, err
	}
	return o, nil
}

func (mgr *Manager) DeleteSiblingOffer(msgID string) error {
	if msgID == "" {
		return nil
	}
	return mgr.st.Update(func(txn *badger.Txn) error {
		return deleteSiblingOfferTxn(txn, msgID)
	})
}

func deleteSiblingOfferTxn(txn *badger.Txn, msgID string) error {
	err := txn.Delete(siblingOfferKey(msgID))
	if errors.Is(err, badger.ErrKeyNotFound) {
		return nil
	}
	return err
}

func (mgr *Manager) ListSiblingOffers() ([]SiblingOffer, error) {
	var out []SiblingOffer
	err := mgr.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefixSiblingOffer)
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			var o SiblingOffer
			if err := it.Item().Value(func(v []byte) error {
				return json.Unmarshal(v, &o)
			}); err != nil {
				return err
			}
			out = append(out, o)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("files: list sibling offers: %w", err)
	}
	return out, nil
}

func siblingOfferKey(msgID string) []byte {
	return []byte(prefixSiblingOffer + msgID)
}

func siblingOfferByStagingKey(stagingID string) []byte {
	return []byte(prefixSiblingOfferByStaging + stagingID)
}
