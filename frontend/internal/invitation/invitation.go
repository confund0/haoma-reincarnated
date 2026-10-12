package invitation

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"

	"github.com/dgraph-io/badger/v4"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/devicetag"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/logid"
	"haoma-frontend/internal/store"
)

const (
	prefixRecord = "invitation:"

	prefixIndex = "invitation-by-chat:"

	prefixNote = "invitation-note:"
)

var ErrNotFound = errors.New("invitation: not found")

var ErrSettled = errors.New("invitation: already settled")

var ErrNotLive = errors.New("invitation: not the live invitation")

type Record struct {
	MsgID string `json:"msg_id"`

	ChatID string `json:"chat_id"`

	Fingerprint string `json:"fingerprint"`

	OfferingDevice string `json:"offering_device"`

	Secret string `json:"secret"`

	MintedAt int64 `json:"minted_at"`

	ExpiresAt int64 `json:"expires_at"`

	State string `json:"state"`

	Dialling bool `json:"dialling,omitempty"`
}

func (r Record) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("invitation_id", logid.Short(r.MsgID)),
		slog.String("chat_id", logid.Short(r.ChatID)),
		slog.String("fingerprint", logid.Short(r.Fingerprint)),
		slog.String("offering_device", logid.Short(r.OfferingDevice)),
		slog.Int64("minted_at", r.MintedAt),
		slog.Int64("expires_at", r.ExpiresAt),
		slog.String("state", r.State),
	)
}

type Note struct {
	MsgID string `json:"msg_id"`

	HandleID string `json:"handle_id"`

	Fingerprint string `json:"fingerprint"`

	MintedAt int64 `json:"minted_at"`

	ExpiresAt int64 `json:"expires_at"`

	Device uint32 `json:"device,omitempty"`
}

func (n Note) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("invitation_id", logid.Short(n.MsgID)),
		slog.String("handle_id", logid.Short(n.HandleID)),
		slog.String("fingerprint", logid.Short(n.Fingerprint)),
		slog.Int64("minted_at", n.MintedAt),
		slog.Int64("expires_at", n.ExpiresAt),
		slog.String("device", devicetag.Tag(n.Device)),
	)
}

type Store struct {
	st *store.Store
}

func New(st *store.Store) *Store {
	if st == nil {
		return nil
	}
	return &Store{st: st}
}

func (s *Store) Put(rec Record) error {
	if s == nil || s.st == nil {
		return errors.New("invitation: no store")
	}
	return s.st.Update(func(txn *badger.Txn) error {
		return s.PutTxn(txn, rec)
	})
}

func (s *Store) PutTxn(txn *badger.Txn, rec Record) error {
	if txn == nil {
		return errors.New("invitation: no transaction")
	}
	if rec.MsgID == "" {
		return errors.New("invitation: record has no id")
	}
	if rec.ChatID == "" {
		return errors.New("invitation: record has no chat")
	}
	if rec.Fingerprint == "" {

		return errors.New("invitation: record has no fingerprint")
	}
	if rec.State == "" {

		return errors.New("invitation: record has no state")
	}

	if rec.State == events.InvitationLive && rec.Secret == "" {
		return errors.New("invitation: a live record has no secret")
	}
	if rec.State != events.InvitationLive && rec.Secret != "" {
		return errors.New("invitation: a record that is no longer live still holds a secret")
	}
	if rec.OfferingDevice == "" {

		return errors.New("invitation: record names no offering device")
	}
	if rec.MintedAt <= 0 {

		return errors.New("invitation: record has no mint time")
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("invitation: marshal record: %w", err)
	}
	if err := txn.Set(recordKey(rec.MsgID), raw); err != nil {
		return err
	}
	return txn.Set(indexKey(chat.ChatID(rec.ChatID), rec.MsgID), []byte{})
}

func (s *Store) Get(msgID string) (Record, error) {
	if s == nil || s.st == nil {
		return Record{}, errors.New("invitation: no store")
	}
	if msgID == "" {
		return Record{}, ErrNotFound
	}
	var rec Record
	err := s.st.View(func(txn *badger.Txn) error {
		var err error
		rec, err = getTxn(txn, msgID)
		return err
	})
	if err != nil {
		return Record{}, err
	}
	return rec, nil
}

func getTxn(txn *badger.Txn, msgID string) (Record, error) {
	item, err := txn.Get(recordKey(msgID))
	if errors.Is(err, badger.ErrKeyNotFound) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, err
	}
	var rec Record
	if err := item.Value(func(v []byte) error {
		return json.Unmarshal(v, &rec)
	}); err != nil {
		return Record{}, fmt.Errorf("invitation: decode record: %w", err)
	}
	return rec, nil
}

func (s *Store) SetState(msgID, state string) error {
	if s == nil || s.st == nil {
		return errors.New("invitation: no store")
	}
	return s.st.Update(func(txn *badger.Txn) error {
		return s.SetStateTxn(txn, msgID, state)
	})
}

func (s *Store) SetStateTxn(txn *badger.Txn, msgID, state string) error {
	if txn == nil {
		return errors.New("invitation: no transaction")
	}
	if state == "" {
		return errors.New("invitation: no state to stamp")
	}
	rec, err := getTxn(txn, msgID)
	if err != nil {
		return err
	}
	if rec.State == state {
		return nil
	}
	if events.InvitationTerminal(rec.State) {
		return ErrSettled
	}
	rec.State = state
	if state != events.InvitationLive {
		rec.Secret = ""

		rec.Dialling = false
	}
	return s.PutTxn(txn, rec)
}

func (s *Store) Claim(msgID string) (Record, error) {
	if s == nil || s.st == nil {
		return Record{}, errors.New("invitation: no store")
	}
	if msgID == "" {
		return Record{}, ErrNotFound
	}
	var rec Record
	err := s.st.Update(func(txn *badger.Txn) error {
		var err error
		rec, err = getTxn(txn, msgID)
		if err != nil {
			return err
		}
		if rec.State != events.InvitationLive || rec.Dialling {
			return ErrNotLive
		}
		rec.Dialling = true
		return s.PutTxn(txn, rec)
	})
	if err != nil {
		return Record{}, err
	}
	return rec, nil
}

func (s *Store) Release(msgID string) error {
	if s == nil || s.st == nil {
		return errors.New("invitation: no store")
	}
	if msgID == "" {
		return nil
	}
	return s.st.Update(func(txn *badger.Txn) error {
		rec, err := getTxn(txn, msgID)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if rec.State != events.InvitationLive || !rec.Dialling {
			return nil
		}
		rec.Dialling = false
		return s.PutTxn(txn, rec)
	})
}

func (s *Store) ListByChat(chatID chat.ChatID) ([]Record, error) {
	if s == nil || s.st == nil {
		return nil, errors.New("invitation: no store")
	}
	if chatID == "" {
		return nil, errors.New("invitation: empty chat id")
	}
	scanPrefix := append([]byte(prefixIndex), chatID...)
	scanPrefix = append(scanPrefix, ':')
	var ids []string
	if err := s.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = scanPrefix
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			key := it.Item().Key()
			ids = append(ids, string(key[len(scanPrefix):]))
		}
		return nil
	}); err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(ids))
	for _, id := range ids {
		rec, err := s.Get(id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

func (s *Store) List() ([]Record, error) {
	if s == nil || s.st == nil {
		return nil, errors.New("invitation: no store")
	}
	var out []Record
	if err := s.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefixRecord)
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			var rec Record
			if err := it.Item().Value(func(v []byte) error {
				return json.Unmarshal(v, &rec)
			}); err != nil {
				return err
			}
			out = append(out, rec)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) Delete(msgID string) error {
	if s == nil || s.st == nil {
		return errors.New("invitation: no store")
	}
	if msgID == "" {
		return nil
	}
	return s.st.Update(func(txn *badger.Txn) error {
		item, err := txn.Get(recordKey(msgID))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		var rec Record
		if err := item.Value(func(v []byte) error {
			return json.Unmarshal(v, &rec)
		}); err != nil {

			return txn.Delete(recordKey(msgID))
		}
		if err := txn.Delete(recordKey(msgID)); err != nil {
			return err
		}
		if rec.ChatID == "" {
			return nil
		}
		return txn.Delete(indexKey(chat.ChatID(rec.ChatID), msgID))
	})
}

func (s *Store) PutNote(n Note) error {
	if s == nil || s.st == nil {
		return errors.New("invitation: no store")
	}
	if n.Fingerprint == "" {
		return errors.New("invitation: note has no fingerprint")
	}
	if n.HandleID == "" {
		return errors.New("invitation: note has no handle")
	}
	if n.MsgID == "" {

		return errors.New("invitation: note has no row id")
	}
	raw, err := json.Marshal(n)
	if err != nil {
		return fmt.Errorf("invitation: marshal note: %w", err)
	}
	return s.st.Put(noteKey(n.Fingerprint, n.Device), raw)
}

func (s *Store) GetNote(fingerprint string, device uint32) (Note, error) {
	if s == nil || s.st == nil {
		return Note{}, errors.New("invitation: no store")
	}
	if fingerprint == "" {
		return Note{}, ErrNotFound
	}
	raw, err := s.st.Get(noteKey(fingerprint, device))
	if errors.Is(err, store.ErrNotFound) {
		return Note{}, ErrNotFound
	}
	if err != nil {
		return Note{}, err
	}
	var n Note
	if err := json.Unmarshal(raw, &n); err != nil {
		return Note{}, fmt.Errorf("invitation: decode note: %w", err)
	}
	return n, nil
}

func (s *Store) NotesFor(fingerprint string) ([]Note, error) {
	if fingerprint == "" {
		return nil, nil
	}
	all, err := s.ListNotes()
	if err != nil {
		return nil, err
	}
	var out []Note
	for _, n := range all {
		if n.Fingerprint == fingerprint {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Device < out[j].Device })
	return out, nil
}

func (s *Store) ListNotes() ([]Note, error) {
	if s == nil || s.st == nil {
		return nil, errors.New("invitation: no store")
	}
	var out []Note
	if err := s.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefixNote)
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			var n Note
			if err := it.Item().Value(func(v []byte) error {
				return json.Unmarshal(v, &n)
			}); err != nil {
				return err
			}
			out = append(out, n)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) DeleteNote(fingerprint string, device uint32) error {
	if s == nil || s.st == nil {
		return errors.New("invitation: no store")
	}
	if fingerprint == "" {
		return nil
	}
	return s.st.Delete(noteKey(fingerprint, device))
}

func recordKey(msgID string) []byte {
	return append([]byte(prefixRecord), msgID...)
}

func indexKey(chatID chat.ChatID, msgID string) []byte {
	out := append([]byte(prefixIndex), chatID...)
	out = append(out, ':')
	out = append(out, msgID...)
	return out
}

func noteKey(fingerprint string, device uint32) []byte {
	out := append([]byte(prefixNote), fingerprint...)
	if device != 0 {
		out = append(out, '#')
		out = strconv.AppendUint(out, uint64(device), 10)
	}
	return out
}
