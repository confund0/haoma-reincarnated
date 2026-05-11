package calls

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/dgraph-io/badger/v4"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/store"
)

type Direction string

const (
	DirIn  Direction = "in"
	DirOut Direction = "out"
)

type Status string

const (
	StatusOffered Status = "offered"

	StatusRinging Status = "ringing"

	StatusAccepted Status = "accepted"

	StatusRejected Status = "rejected"

	StatusEnded Status = "ended"

	StatusFailed Status = "failed"
)

type State struct {
	CallID     string      `json:"call_id"`
	ChatID     chat.ChatID `json:"chat_id"`
	PeerID     string      `json:"peer_id"`
	Direction  Direction   `json:"direction"`
	Status     Status      `json:"status"`
	Modalities []string    `json:"modalities"`
	StartedAt  int64       `json:"started_at"`
	UpdatedAt  int64       `json:"updated_at,omitempty"`
	EndedAt    int64       `json:"ended_at,omitempty"`
	FailReason string      `json:"fail_reason,omitempty"`

	RemoteOutboundKey []byte            `json:"remote_outbound_key,omitempty"`
	RemoteTokens      map[string]string `json:"remote_tokens,omitempty"`

	LocalTokens map[string]string `json:"local_tokens,omitempty"`
}

func (s State) IsTerminal() bool {
	switch s.Status {
	case StatusRejected, StatusEnded, StatusFailed:
		return true
	default:
		return false
	}
}

var ErrCallNotFound = errors.New("calls: state not found")

var ErrIllegalTransition = errors.New("calls: illegal state transition")

const (
	FailReasonNoAnswer      = "no_answer"
	FailReasonSendFail      = "send_failed"
	FailReasonDaemonRestart = "daemon_restart"
	FailReasonStreamerSpawn = "streamer_spawn_failed"
	FailReasonProxyRegister = "proxy_register_failed"
	FailReasonPeerUnreach   = "peer_unreachable"
	FailReasonInvalidOffer  = "invalid_call_offer"
)

const (
	prefixState = "call:"
	prefixIndex = "call-by-chat:"
)

type Manager struct {
	st *store.Store
}

func NewManager(st *store.Store) (*Manager, error) {
	if st == nil {
		return nil, errors.New("calls: nil store")
	}
	return &Manager{st: st}, nil
}

func NewCallID() (string, error) {
	var b [32]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return "", fmt.Errorf("calls: mint id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func (m *Manager) PutState(s State) error {
	if s.CallID == "" {
		return errors.New("calls: PutState: empty call id")
	}
	if s.ChatID == "" {
		return errors.New("calls: PutState: empty chat id")
	}
	if s.PeerID == "" {
		return errors.New("calls: PutState: empty peer id")
	}
	if s.Direction != DirIn && s.Direction != DirOut {
		return fmt.Errorf("calls: PutState: invalid direction %q", s.Direction)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("calls: marshal state: %w", err)
	}
	return m.st.Update(func(txn *badger.Txn) error {
		if err := txn.Set(stateKey(s.CallID), raw); err != nil {
			return err
		}
		return txn.Set(indexKey(s.ChatID, s.CallID), []byte{})
	})
}

func (m *Manager) GetState(callID string) (State, error) {
	if callID == "" {
		return State{}, ErrCallNotFound
	}
	var raw []byte
	err := m.st.View(func(txn *badger.Txn) error {
		item, err := txn.Get(stateKey(callID))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrCallNotFound
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
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return State{}, fmt.Errorf("calls: decode state: %w", err)
	}
	return s, nil
}

func (m *Manager) DeleteState(callID string) error {
	if callID == "" {
		return nil
	}
	return m.st.Update(func(txn *badger.Txn) error {
		primary := stateKey(callID)
		var s State
		item, err := txn.Get(primary)
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := item.Value(func(v []byte) error {
			return json.Unmarshal(v, &s)
		}); err != nil {
			return err
		}
		if err := txn.Delete(primary); err != nil {
			return err
		}
		if s.ChatID != "" {
			if err := txn.Delete(indexKey(s.ChatID, callID)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (m *Manager) Transition(callID string, next Status, reason string, nowUnix int64) (State, error) {
	if callID == "" {
		return State{}, ErrCallNotFound
	}
	var out State
	err := m.st.Update(func(txn *badger.Txn) error {
		item, err := txn.Get(stateKey(callID))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrCallNotFound
		}
		if err != nil {
			return err
		}
		var cur State
		if err := item.Value(func(v []byte) error {
			return json.Unmarshal(v, &cur)
		}); err != nil {
			return fmt.Errorf("calls: decode state for transition: %w", err)
		}
		if !legalTransition(cur.Status, next) {
			return fmt.Errorf("%w: %s → %s", ErrIllegalTransition, cur.Status, next)
		}
		cur.Status = next
		cur.UpdatedAt = nowUnix
		if next == StatusRejected || next == StatusFailed {
			cur.FailReason = reason
		}
		if next == StatusRejected || next == StatusEnded || next == StatusFailed {
			cur.EndedAt = nowUnix
			for i := range cur.RemoteOutboundKey {
				cur.RemoteOutboundKey[i] = 0
			}
			cur.RemoteOutboundKey = nil
		}
		raw, err := json.Marshal(cur)
		if err != nil {
			return fmt.Errorf("calls: marshal state for transition: %w", err)
		}
		if err := txn.Set(stateKey(callID), raw); err != nil {
			return err
		}
		out = cur
		return nil
	})
	return out, err
}

func (m *Manager) SetRemoteMaterial(callID string, outboundKey []byte, tokens map[string]string) error {
	if callID == "" {
		return ErrCallNotFound
	}
	return m.st.Update(func(txn *badger.Txn) error {
		item, err := txn.Get(stateKey(callID))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrCallNotFound
		}
		if err != nil {
			return err
		}
		var cur State
		if err := item.Value(func(v []byte) error {
			return json.Unmarshal(v, &cur)
		}); err != nil {
			return fmt.Errorf("calls: decode state for set-remote: %w", err)
		}
		if cur.IsTerminal() {
			return fmt.Errorf("calls: SetRemoteMaterial on terminal row (%s)", cur.Status)
		}
		if outboundKey != nil {
			cur.RemoteOutboundKey = append([]byte(nil), outboundKey...)
		}
		if tokens != nil {
			cp := make(map[string]string, len(tokens))
			for k, v := range tokens {
				cp[k] = v
			}
			cur.RemoteTokens = cp
		}
		raw, err := json.Marshal(cur)
		if err != nil {
			return fmt.Errorf("calls: marshal state for set-remote: %w", err)
		}
		return txn.Set(stateKey(callID), raw)
	})
}

func (m *Manager) SweepNonTerminal(reason string, nowUnix int64) (int, error) {
	if reason == "" {
		reason = "daemon_restart"
	}
	var ids []string
	if err := m.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefixState)
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			var s State
			if err := item.Value(func(v []byte) error {
				return json.Unmarshal(v, &s)
			}); err != nil {
				continue
			}
			if !s.IsTerminal() {
				ids = append(ids, s.CallID)
			}
		}
		return nil
	}); err != nil {
		return 0, err
	}
	count := 0
	for _, id := range ids {
		if _, err := m.Transition(id, StatusFailed, reason, nowUnix); err != nil {
			if errors.Is(err, ErrIllegalTransition) || errors.Is(err, ErrCallNotFound) {
				continue
			}
			return count, err
		}
		count++
	}
	return count, nil
}

func (m *Manager) ListByChat(chatID chat.ChatID) ([]State, error) {
	if chatID == "" {
		return nil, errors.New("calls: ListByChat: empty chat id")
	}
	var ids []string
	scanPrefix := append([]byte(prefixIndex), chatID...)
	scanPrefix = append(scanPrefix, ':')
	if err := m.st.View(func(txn *badger.Txn) error {
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
	out := make([]State, 0, len(ids))
	for _, id := range ids {
		s, err := m.GetState(id)
		if err != nil {
			if errors.Is(err, ErrCallNotFound) {
				continue
			}
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func legalTransition(from, to Status) bool {
	switch from {
	case StatusOffered, StatusRinging:
		switch to {
		case StatusAccepted, StatusRejected, StatusEnded, StatusFailed:
			return true
		}
	case StatusAccepted:
		switch to {
		case StatusEnded, StatusFailed:
			return true
		}
	}
	return false
}

func stateKey(callID string) []byte {
	return append([]byte(prefixState), callID...)
}

func indexKey(chatID chat.ChatID, callID string) []byte {
	out := append([]byte(prefixIndex), chatID...)
	out = append(out, ':')
	out = append(out, callID...)
	return out
}
