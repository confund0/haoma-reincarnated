package files

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dgraph-io/badger/v4"

	"haoma-frontend/internal/chat"
)

type State string

const (
	StatePending State = "pending"

	StateDownloading State = "downloading"

	StateAwaitingKey State = "awaiting_key"

	StateReady State = "ready"

	StateFailedTransient State = "failed_transient"

	StateFailedPermanent State = "failed_permanent"

	StateExpired State = "expired"
)

type Direction string

const (
	DirIn  Direction = "in"
	DirOut Direction = "out"
)

type Metadata struct {
	MsgID            string            `json:"msg_id"`
	ChatID           chat.ChatID       `json:"chat_id"`
	Direction        Direction         `json:"direction"`
	Token            string            `json:"token"`
	RecipientTokens  map[string]string `json:"recipient_tokens,omitempty"`
	OfferPeerID      string            `json:"offer_peer_id,omitempty"`
	OriginalName     string            `json:"original_name"`
	Mime             string            `json:"mime,omitempty"`
	Size             uint64            `json:"size"`
	Sha256Ciphertext string            `json:"sha256_ciphertext"`
	BlobPath         string            `json:"blob_path,omitempty"`
	SealedPath       string            `json:"sealed_path,omitempty"`
	KeyBytes         []byte            `json:"key_bytes,omitempty"`
	Nonce            []byte            `json:"nonce,omitempty"`
	State            State             `json:"state"`

	KeyEnvelopes map[string]string `json:"key_envelopes,omitempty"`

	SiblingStagingIDs []string `json:"sibling_staging_ids,omitempty"`

	FetchedFromSibling bool `json:"fetched_from_sibling,omitempty"`

	StagingID string `json:"staging_id,omitempty"`

	GapFill bool `json:"gap_fill,omitempty"`

	GapAsks int `json:"gap_asks,omitempty"`

	FailReason string `json:"fail_reason,omitempty"`

	CreatedAt int64 `json:"created_at"`
	UpdatedAt int64 `json:"updated_at,omitempty"`
}

var ErrMetaNotFound = errors.New("files: metadata not found")

func (mgr *Manager) PutMeta(m Metadata) error {
	write, err := mgr.metaWrites(m)
	if err != nil {
		return err
	}
	return mgr.st.Update(write)
}

func (mgr *Manager) metaWrites(m Metadata) (func(*badger.Txn) error, error) {
	if m.MsgID == "" {
		return nil, errors.New("files: PutMeta: empty msg id")
	}
	if m.ChatID == "" {
		return nil, errors.New("files: PutMeta: empty chat id")
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("files: marshal metadata: %w", err)
	}
	primary := metaKey(m.MsgID)
	idx := indexKey(m.ChatID, m.MsgID)
	tokenSet := tokensFor(m)
	return func(txn *badger.Txn) error {
		if err := txn.Set(primary, raw); err != nil {
			return err
		}
		if err := txn.Set(idx, []byte{}); err != nil {
			return err
		}

		if err := deleteWantedTxn(txn, m.MsgID); err != nil {
			return err
		}
		for tok := range tokenSet {
			if err := txn.Set(tokenKey(tok), []byte(m.MsgID)); err != nil {
				return err
			}
		}
		return nil
	}, nil
}

var ErrMetaHeld = errors.New("files: metadata already held for this msg id")

func (mgr *Manager) PutMetaIfAbsent(m Metadata) error {
	write, err := mgr.metaWrites(m)
	if err != nil {
		return err
	}
	return mgr.putIfAbsent(m.MsgID, write)
}

func (mgr *Manager) putIfAbsent(msgID string, write func(*badger.Txn) error) error {
	var err error
	for attempt := 0; attempt < updateMetaAttempts; attempt++ {
		err = mgr.st.Update(func(txn *badger.Txn) error {
			_, gErr := txn.Get(metaKey(msgID))
			switch {
			case gErr == nil:
				return ErrMetaHeld
			case !errors.Is(gErr, badger.ErrKeyNotFound):
				return gErr
			}
			putIfAbsentCheckpoint(msgID)
			return write(txn)
		})
		if !errors.Is(err, badger.ErrConflict) {
			return err
		}
	}
	return err
}

var putIfAbsentCheckpoint = func(msgID string) {}

func (mgr *Manager) GetMeta(msgID string) (Metadata, error) {
	if msgID == "" {
		return Metadata{}, ErrMetaNotFound
	}
	var raw []byte
	err := mgr.st.View(func(txn *badger.Txn) error {
		item, err := txn.Get(metaKey(msgID))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrMetaNotFound
		}
		if err != nil {
			return err
		}
		return item.Value(func(v []byte) error {
			raw = append([]byte(nil), v...)
			return nil
		})
	})
	if err != nil {
		return Metadata{}, err
	}
	var m Metadata
	if err := json.Unmarshal(raw, &m); err != nil {
		return Metadata{}, fmt.Errorf("files: decode metadata: %w", err)
	}
	return m, nil
}

func (mgr *Manager) DeleteMeta(msgID string) error {
	_, _, err := mgr.DeleteMetaReturning(msgID)
	return err
}

func (mgr *Manager) DeleteMetaReturning(msgID string) (Metadata, bool, error) {
	if msgID == "" {
		return Metadata{}, false, nil
	}
	var (
		meta  Metadata
		found bool
		err   error
	)
	for attempt := 0; attempt < updateMetaAttempts; attempt++ {
		meta, found = Metadata{}, false
		err = mgr.st.Update(func(txn *badger.Txn) error {
			var tErr error
			meta, found, tErr = deleteMetaTxn(txn, msgID)
			return tErr
		})
		if !errors.Is(err, badger.ErrConflict) {
			break
		}
	}
	if err != nil {
		return Metadata{}, false, err
	}
	return meta, found, nil
}

func deleteMetaTxn(txn *badger.Txn, msgID string) (Metadata, bool, error) {
	primary := metaKey(msgID)
	var meta Metadata

	if err := deleteWantedTxn(txn, msgID); err != nil {
		return Metadata{}, false, err
	}
	item, err := txn.Get(primary)
	if errors.Is(err, badger.ErrKeyNotFound) {

		return Metadata{}, false, nil
	}
	if err != nil {
		return Metadata{}, false, err
	}
	if err := item.Value(func(v []byte) error {
		if jsonErr := json.Unmarshal(v, &meta); jsonErr != nil {
			return fmt.Errorf("files: decode metadata for delete: %w", jsonErr)
		}
		return nil
	}); err != nil {
		return Metadata{}, false, err
	}
	deleteMetaCheckpoint(msgID)
	if err := txn.Delete(primary); err != nil {
		return Metadata{}, false, err
	}
	if meta.ChatID != "" {
		if err := txn.Delete(indexKey(meta.ChatID, msgID)); err != nil {
			return Metadata{}, false, err
		}
	}
	for tok := range tokensFor(meta) {
		if err := txn.Delete(tokenKey(tok)); err != nil {
			return Metadata{}, false, err
		}
	}

	if meta.StagingID != "" {
		if err := txn.Delete(siblingOfferByStagingKey(meta.StagingID)); err != nil {
			return Metadata{}, false, err
		}
	}

	if err := deleteSiblingOfferTxn(txn, msgID); err != nil {
		return Metadata{}, false, err
	}

	if err := deleteGapOffersTxn(txn, msgID); err != nil {
		return Metadata{}, false, err
	}
	return meta, true, nil
}

var deleteMetaCheckpoint = func(msgID string) {}

func (mgr *Manager) DeleteMetaByChat(chatID chat.ChatID) (int, error) {
	if chatID == "" {
		return 0, errors.New("files: DeleteMetaByChat: empty chat id")
	}
	var msgIDs []string
	if err := mgr.scanChatMeta(chatID, func(msgID string) error {
		msgIDs = append(msgIDs, msgID)
		return nil
	}); err != nil {
		return 0, fmt.Errorf("files: scan chat %s: %w", chatID, err)
	}
	deleted := 0
	for _, msgID := range msgIDs {

		if err := mgr.DeleteMeta(msgID); err != nil {
			return deleted, fmt.Errorf("files: delete meta for %s: %w", msgID, err)
		}
		deleted++
	}

	if err := mgr.deleteWantedByChat(chatID); err != nil {
		return deleted, fmt.Errorf("files: drop wanted files of chat %s: %w", chatID, err)
	}
	return deleted, nil
}

func (mgr *Manager) ListByChat(chatID chat.ChatID) ([]Metadata, error) {
	if chatID == "" {
		return nil, errors.New("files: ListByChat: empty chat id")
	}
	var out []Metadata
	if err := mgr.scanChatMeta(chatID, func(msgID string) error {
		m, err := mgr.GetMeta(msgID)
		if errors.Is(err, ErrMetaNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("get meta %s: %w", msgID, err)
		}
		out = append(out, m)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("files: scan chat %s: %w", chatID, err)
	}
	return out, nil
}

func (mgr *Manager) ListInboundInFlight() ([]Metadata, error) {
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

			if m.Direction == DirIn && !m.FetchedFromSibling && (m.State == StatePending || m.State == StateDownloading) {
				out = append(out, m)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("files: list inbound in-flight: %w", err)
	}
	return out, nil
}

func (mgr *Manager) GetMetaByToken(token string) (Metadata, error) {
	if token == "" {
		return Metadata{}, ErrMetaNotFound
	}
	var msgID string
	err := mgr.st.View(func(txn *badger.Txn) error {
		item, err := txn.Get(tokenKey(token))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrMetaNotFound
		}
		if err != nil {
			return err
		}
		return item.Value(func(v []byte) error {
			msgID = string(v)
			return nil
		})
	})
	if err != nil {
		return Metadata{}, err
	}
	return mgr.GetMeta(msgID)
}

func tokensFor(m Metadata) map[string]struct{} {
	out := make(map[string]struct{}, 1+len(m.RecipientTokens))
	if m.Token != "" {
		out[m.Token] = struct{}{}
	}
	for _, t := range m.RecipientTokens {
		if t != "" {
			out[t] = struct{}{}
		}
	}
	return out
}

func metaKey(msgID string) []byte {
	out := make([]byte, 0, len(prefixMeta)+len(msgID))
	out = append(out, prefixMeta...)
	out = append(out, msgID...)
	return out
}

func indexKey(chatID chat.ChatID, msgID string) []byte {
	out := make([]byte, 0, len(prefixIndex)+len(chatID)+1+len(msgID))
	out = append(out, prefixIndex...)
	out = append(out, chatID...)
	out = append(out, ':')
	out = append(out, msgID...)
	return out
}

func tokenKey(token string) []byte {
	out := make([]byte, 0, len(prefixByToken)+len(token))
	out = append(out, prefixByToken...)
	out = append(out, token...)
	return out
}

func (mgr *Manager) UpdateMeta(msgID string, fn func(m *Metadata) error) error {
	if msgID == "" {
		return ErrMetaNotFound
	}
	var err error
	for attempt := 0; attempt < updateMetaAttempts; attempt++ {
		err = mgr.st.Update(func(txn *badger.Txn) error {
			item, gErr := txn.Get(metaKey(msgID))
			if errors.Is(gErr, badger.ErrKeyNotFound) {
				return ErrMetaNotFound
			}
			if gErr != nil {
				return gErr
			}
			var m Metadata
			if vErr := item.Value(func(v []byte) error { return json.Unmarshal(v, &m) }); vErr != nil {
				return fmt.Errorf("files: decode metadata for update: %w", vErr)
			}
			if fErr := fn(&m); fErr != nil {
				return fErr
			}
			write, wErr := mgr.metaWrites(m)
			if wErr != nil {
				return wErr
			}
			return write(txn)
		})
		if !errors.Is(err, badger.ErrConflict) {
			return err
		}
	}
	return err
}

const updateMetaAttempts = 5
