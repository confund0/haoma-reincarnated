package events

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dgraph-io/badger/v4"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/store"
)

const (
	prefix         = "evt:"
	envIDPrefix    = "evt-envid:"
	msgIDPrefix    = "evt-msgid:"
	keyNextRecvSeq = "events:next_recv_seq"
)

var ErrEventNotFound = errors.New("events: event not found")

var ErrEditorNotAuthor = errors.New("events: editor is not the original author")

var ErrEditUnsupportedKind = errors.New("events: target event is not editable")

var ErrDeleterNotAuthor = errors.New("events: deleter is not the original author")

var ErrDeleteUnsupportedKind = errors.New("events: target event is not deletable")

var ErrReactionUnsupportedKind = errors.New("events: target event is not reactable")

var ErrReaderNotPeer = errors.New("events: reader is not the target's recipient")

var ErrReadUnsupportedKind = errors.New("events: target event is not receipt-ackable")

const (
	SkewMaxPastSec   int64 = 3600
	SkewMaxFutureSec int64 = 60
)

const maxConflictRetries = 256

type Direction string

const (
	DirIn  Direction = "in"
	DirOut Direction = "out"
)

const MutationWindow = 24 * 60 * 60

type Kind string

const (
	KindText Kind = "text"

	KindTimerChange Kind = "timer_change"

	KindReaction Kind = "reaction"

	KindFile Kind = "file"

	KindCallSummary Kind = "call_summary"

	KindOnionRotation Kind = "onion_rotation"
)

type OnionRotationStage string

const (
	OnionRotationStageStarted   OnionRotationStage = "started"
	OnionRotationStageSucceeded OnionRotationStage = "succeeded"
	OnionRotationStageFailed    OnionRotationStage = "failed"
)

type OnionRotationBody struct {
	RotationID string             `json:"rotation_id"`
	Stage      OnionRotationStage `json:"stage"`
	Reason     string             `json:"reason,omitempty"`
}

type TimerChangeBody struct {
	From      uint32 `json:"from"`
	To        uint32 `json:"to"`
	ChangedBy string `json:"changed_by,omitempty"`
}

type ReactionBody struct {
	TargetMsgID string `json:"target_msg_id"`
	Emoji       string `json:"emoji"`
	At          int64  `json:"at,omitempty"`
}

type CallOutcome string

const (
	CallOutcomeCompleted CallOutcome = "completed"

	CallOutcomeMissed CallOutcome = "missed"

	CallOutcomeRejected CallOutcome = "rejected"

	CallOutcomeCancelled CallOutcome = "cancelled"

	CallOutcomeFailed CallOutcome = "failed"

	CallOutcomeDisrupted CallOutcome = "disrupted"
)

type CallSummaryBody struct {
	CallID          string      `json:"call_id"`
	Direction       string      `json:"direction"`
	Outcome         CallOutcome `json:"outcome"`
	DurationSeconds int64       `json:"duration_seconds,omitempty"`
	Modalities      []string    `json:"modalities,omitempty"`
	FailReason      string      `json:"fail_reason,omitempty"`
}

type DecryptStatus string

const (
	DecryptOK     DecryptStatus = "ok"
	DecryptFailed DecryptStatus = "failed"
)

type DecryptFailReason string

const (
	DecryptReasonVersionMismatch DecryptFailReason = "version_mismatch"
)

type Event struct {
	RecvSeq      uint64      `json:"recv_seq"`
	ChatID       chat.ChatID `json:"chat_id"`
	Direction    Direction   `json:"direction"`
	Kind         Kind        `json:"kind"`
	DisplayTs    int64       `json:"display_ts"`
	SenderTs     int64       `json:"sender_ts,omitempty"`
	RecvTs       int64       `json:"recv_ts"`
	SenderSeq    uint64      `json:"sender_seq,omitempty"`
	SenderPeerID string      `json:"sender_peer_id,omitempty"`
	EnvelopeID   string      `json:"envelope_id,omitempty"`

	EnvelopeIDs   []string          `json:"envelope_ids,omitempty"`
	MsgID         string            `json:"msg_id,omitempty"`
	DecryptStatus DecryptStatus     `json:"decrypt_status,omitempty"`
	FailReason    DecryptFailReason `json:"fail_reason,omitempty"`
	Body          json.RawMessage   `json:"body,omitempty"`
	RawBlob       []byte            `json:"raw_blob,omitempty"`

	DeliveryState string `json:"delivery_state,omitempty"`

	ExpireSeconds uint32 `json:"expire_seconds,omitempty"`

	ReadAt int64 `json:"read_at,omitempty"`

	EditedAt int64 `json:"edited_at,omitempty"`

	DeletedAt int64 `json:"deleted_at,omitempty"`

	ReadReceiptSentAt int64 `json:"read_receipt_sent_at,omitempty"`
}

func (e Event) Deletable(now int64) bool {
	if e.Direction != DirOut {
		return false
	}
	if e.Kind != KindText && e.Kind != KindFile {
		return false
	}
	if e.DeletedAt != 0 {
		return false
	}
	if now-e.DisplayTs > int64(MutationWindow) {
		return false
	}
	return true
}

func (e Event) fileReceiptReady() bool {
	if len(e.Body) == 0 {
		return false
	}
	var b struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(e.Body, &b); err != nil {
		return false
	}
	return b.State == "ready"
}

type ReplyToSnapshot struct {
	MsgID string `json:"msg_id"`
	Text  string `json:"text"`
}

type TextBody struct {
	Text    string           `json:"text"`
	ReplyTo *ReplyToSnapshot `json:"reply_to,omitempty"`
}

type Deletion struct {
	ChatID  chat.ChatID
	RecvSeq uint64
	MsgID   string
}

type Bus struct {
	mu      sync.RWMutex
	subs    map[chan Event]struct{}
	delSubs map[chan Deletion]struct{}
}

func NewBus() *Bus {
	return &Bus{
		subs:    map[chan Event]struct{}{},
		delSubs: map[chan Deletion]struct{}{},
	}
}

func (b *Bus) Subscribe(buffer int) (ch <-chan Event, cancel func()) {
	if buffer <= 0 {
		buffer = 16
	}
	c := make(chan Event, buffer)
	b.mu.Lock()
	b.subs[c] = struct{}{}
	b.mu.Unlock()
	var once sync.Once
	cancel = func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, c)
			b.mu.Unlock()
			close(c)
		})
	}
	return c, cancel
}

func (b *Bus) publish(ev Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for c := range b.subs {
		select {
		case c <- ev:
		default:

		}
	}
}

func (b *Bus) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

func (b *Bus) SubscribeDeletions(buffer int) (ch <-chan Deletion, cancel func()) {
	if buffer <= 0 {
		buffer = 16
	}
	c := make(chan Deletion, buffer)
	b.mu.Lock()
	b.delSubs[c] = struct{}{}
	b.mu.Unlock()
	var once sync.Once
	cancel = func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.delSubs, c)
			b.mu.Unlock()
			close(c)
		})
	}
	return c, cancel
}

func (b *Bus) publishDeletion(d Deletion) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for c := range b.delSubs {
		select {
		case c <- d:
		default:
		}
	}
}

func (b *Bus) DeletionSubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.delSubs)
}

type Log struct {
	st  *store.Store
	bus *Bus
	now func() time.Time
}

func New(st *store.Store, bus *Bus, now func() time.Time) *Log {
	if now == nil {
		now = time.Now
	}
	return &Log{st: st, bus: bus, now: now}
}

func (l *Log) AppendInbound(in InboundParams) (Event, error) {
	now := l.now()
	displayTs := clampDisplayTs(in.SenderTs, now.Unix())
	ev := Event{
		ChatID:        in.ChatID,
		Direction:     DirIn,
		Kind:          in.Kind,
		DisplayTs:     displayTs,
		SenderTs:      in.SenderTs,
		RecvTs:        now.Unix(),
		SenderSeq:     in.SenderSeq,
		SenderPeerID:  in.SenderPeerID,
		EnvelopeID:    in.EnvelopeID,
		MsgID:         in.MsgID,
		ExpireSeconds: in.ExpireSeconds,
		DecryptStatus: in.Status,
		FailReason:    in.FailReason,
		Body:          in.Body,
		RawBlob:       in.RawBlob,
	}
	return l.append(ev)
}

func (l *Log) AppendOutbound(out OutboundParams) (Event, error) {
	now := l.now()

	envIDs := out.EnvelopeIDs
	if len(envIDs) == 0 && out.EnvelopeID != "" {
		envIDs = []string{out.EnvelopeID}
	}
	var primaryEnvID string
	if len(envIDs) > 0 {
		primaryEnvID = envIDs[0]
	}
	ev := Event{
		ChatID:        out.ChatID,
		Direction:     DirOut,
		Kind:          out.Kind,
		DisplayTs:     now.Unix(),
		SenderTs:      now.Unix(),
		RecvTs:        now.Unix(),
		SenderSeq:     out.SenderSeq,
		EnvelopeID:    primaryEnvID,
		EnvelopeIDs:   envIDs,
		MsgID:         out.MsgID,
		ExpireSeconds: out.ExpireSeconds,
		Body:          out.Body,
		DeliveryState: "enqueued",
	}
	return l.append(ev)
}

type InboundParams struct {
	ChatID        chat.ChatID
	Kind          Kind
	SenderTs      int64
	SenderSeq     uint64
	SenderPeerID  string
	EnvelopeID    string
	MsgID         string
	ExpireSeconds uint32
	Status        DecryptStatus
	FailReason    DecryptFailReason
	Body          json.RawMessage
	RawBlob       []byte
}

type LocalParams struct {
	ChatID        chat.ChatID
	Kind          Kind
	Direction     Direction
	DisplayTs     int64
	SenderPeerID  string
	ExpireSeconds uint32
	Body          json.RawMessage
}

type OutboundParams struct {
	ChatID    chat.ChatID
	Kind      Kind
	SenderSeq uint64

	EnvelopeIDs   []string
	EnvelopeID    string
	MsgID         string
	ExpireSeconds uint32
	Body          json.RawMessage
}

func (l *Log) AppendLocal(p LocalParams) (Event, error) {
	now := l.now()
	displayTs := p.DisplayTs
	if displayTs == 0 {
		displayTs = now.Unix()
	}
	ev := Event{
		ChatID:        p.ChatID,
		Direction:     p.Direction,
		Kind:          p.Kind,
		DisplayTs:     displayTs,
		RecvTs:        now.Unix(),
		SenderPeerID:  p.SenderPeerID,
		ExpireSeconds: p.ExpireSeconds,
		Body:          p.Body,
	}
	return l.append(ev)
}

func (l *Log) append(ev Event) (Event, error) {
	if ev.ChatID == "" {
		return Event{}, errors.New("events: empty chat id")
	}
	if ev.Kind == "" {
		return Event{}, errors.New("events: empty kind")
	}
	seq, err := l.nextRecvSeq()
	if err != nil {
		return Event{}, err
	}
	ev.RecvSeq = seq

	raw, err := json.Marshal(ev)
	if err != nil {
		return Event{}, fmt.Errorf("events: marshal event: %w", err)
	}
	key := storageKey(ev.ChatID, ev.DisplayTs, seq)
	if err := l.st.Update(func(txn *badger.Txn) error {
		if err := txn.Set(key, raw); err != nil {
			return err
		}

		if ev.Direction == DirOut {
			envIDs := ev.EnvelopeIDs
			if len(envIDs) == 0 && ev.EnvelopeID != "" {
				envIDs = []string{ev.EnvelopeID}
			}
			for _, eid := range envIDs {
				if eid == "" {
					continue
				}
				if err := txn.Set([]byte(envIDPrefix+eid), key); err != nil {
					return err
				}
			}
		}

		if ev.MsgID != "" {
			if err := txn.Set([]byte(msgIDPrefix+ev.MsgID), key); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return Event{}, fmt.Errorf("events: persist event for %s: %w", ev.ChatID, err)
	}
	if l.bus != nil {
		l.bus.publish(ev)
	}
	return ev, nil
}

func (l *Log) nextRecvSeq() (uint64, error) {
	key := []byte(keyNextRecvSeq)
	var next uint64
	for attempt := 0; attempt < maxConflictRetries; attempt++ {
		err := l.st.Update(func(txn *badger.Txn) error {
			cur, err := loadUint64(txn, key)
			if err != nil {
				return err
			}
			next = cur + 1
			var buf [8]byte
			binary.BigEndian.PutUint64(buf[:], next)
			return txn.Set(key, buf[:])
		})
		if err == nil {
			return next, nil
		}
		if !errors.Is(err, badger.ErrConflict) {
			return 0, fmt.Errorf("events: next recv_seq: %w", err)
		}
	}
	return 0, fmt.Errorf("events: next recv_seq: exceeded %d conflict retries", maxConflictRetries)
}

func (l *Log) PeekNextRecvSeq() (uint64, error) {
	var v uint64
	err := l.st.View(func(txn *badger.Txn) error {
		var err error
		v, err = loadUint64(txn, []byte(keyNextRecvSeq))
		return err
	})
	if err != nil {
		return 0, err
	}
	return v + 1, nil
}

func Less(a, b Event) bool {
	if a.DisplayTs != b.DisplayTs {
		return a.DisplayTs < b.DisplayTs
	}
	if a.MsgID != b.MsgID {
		return a.MsgID < b.MsgID
	}
	return a.RecvSeq < b.RecvSeq
}

func (l *Log) List(chatID chat.ChatID, sinceDisplayTs int64, limit int) ([]Event, error) {
	if chatID == "" {
		return nil, errors.New("events: empty chat id")
	}
	if limit <= 0 {
		limit = 100
	}
	chatPrefix := append([]byte(prefix), chatID...)
	chatPrefix = append(chatPrefix, ':')

	var out []Event
	err := l.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = chatPrefix
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid() && len(out) < limit; it.Next() {
			item := it.Item()
			key := item.Key()
			rest := key[len(chatPrefix):]
			if len(rest) < 8 {
				continue
			}
			displayTs := int64(binary.BigEndian.Uint64(rest[:8]))
			if displayTs <= sinceDisplayTs {
				continue
			}
			var ev Event
			if err := item.Value(func(v []byte) error {
				return json.Unmarshal(v, &ev)
			}); err != nil {
				return err
			}
			out = append(out, ev)
		}
		return nil
	})
	return out, err
}

func (l *Log) GetByMsgID(msgID string) (Event, error) {
	if msgID == "" {
		return Event{}, ErrEventNotFound
	}
	var ev Event
	err := l.st.Update(func(txn *badger.Txn) error {
		idxItem, err := txn.Get([]byte(msgIDPrefix + msgID))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrEventNotFound
		}
		if err != nil {
			return err
		}
		var key []byte
		if err := idxItem.Value(func(v []byte) error {
			key = append([]byte(nil), v...)
			return nil
		}); err != nil {
			return err
		}
		rowItem, err := txn.Get(key)
		if errors.Is(err, badger.ErrKeyNotFound) {
			_ = txn.Delete([]byte(msgIDPrefix + msgID))
			return ErrEventNotFound
		}
		if err != nil {
			return err
		}
		return rowItem.Value(func(v []byte) error {
			return json.Unmarshal(v, &ev)
		})
	})
	if err != nil {
		return Event{}, err
	}
	return ev, nil
}

func (l *Log) UpdateDeliveryState(envelopeID, state string) (Event, error) {
	if envelopeID == "" {
		return Event{}, errors.New("events: empty envelope id")
	}
	var updated Event
	err := l.st.Update(func(txn *badger.Txn) error {
		idxItem, err := txn.Get([]byte(envIDPrefix + envelopeID))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrEventNotFound
		}
		if err != nil {
			return err
		}
		var key []byte
		if err := idxItem.Value(func(v []byte) error {
			key = append([]byte(nil), v...)
			return nil
		}); err != nil {
			return err
		}
		rowItem, err := txn.Get(key)
		if errors.Is(err, badger.ErrKeyNotFound) {

			_ = txn.Delete([]byte(envIDPrefix + envelopeID))
			return ErrEventNotFound
		}
		if err != nil {
			return err
		}
		if err := rowItem.Value(func(v []byte) error {
			return json.Unmarshal(v, &updated)
		}); err != nil {
			return err
		}
		if updated.DeliveryState == state {

			return nil
		}

		if updated.DeliveryState == "read" {
			return nil
		}
		updated.DeliveryState = state
		newRaw, err := json.Marshal(updated)
		if err != nil {
			return err
		}
		return txn.Set(key, newRaw)
	})
	if err != nil {
		return Event{}, err
	}
	return updated, nil
}

func (l *Log) ApplyEdit(targetMsgID, newText string, editedAt int64, expectedSenderPeerID string) (Event, error) {
	if targetMsgID == "" {
		return Event{}, ErrEventNotFound
	}
	if newText == "" {
		return Event{}, fmt.Errorf("events: empty edit text")
	}
	var updated Event
	err := l.st.Update(func(txn *badger.Txn) error {
		idxItem, err := txn.Get([]byte(msgIDPrefix + targetMsgID))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrEventNotFound
		}
		if err != nil {
			return err
		}
		var key []byte
		if err := idxItem.Value(func(v []byte) error {
			key = append([]byte(nil), v...)
			return nil
		}); err != nil {
			return err
		}
		rowItem, err := txn.Get(key)
		if errors.Is(err, badger.ErrKeyNotFound) {
			_ = txn.Delete([]byte(msgIDPrefix + targetMsgID))
			return ErrEventNotFound
		}
		if err != nil {
			return err
		}
		if err := rowItem.Value(func(v []byte) error {
			return json.Unmarshal(v, &updated)
		}); err != nil {
			return err
		}
		if updated.Kind != KindText {
			return ErrEditUnsupportedKind
		}

		if expectedSenderPeerID == "" {
			if updated.Direction != DirOut {
				return ErrEditorNotAuthor
			}
		} else {
			if updated.Direction != DirIn {
				return ErrEditorNotAuthor
			}
			if updated.SenderPeerID != "" && updated.SenderPeerID != expectedSenderPeerID {
				return ErrEditorNotAuthor
			}
		}
		newBody, err := json.Marshal(TextBody{Text: newText})
		if err != nil {
			return err
		}
		updated.Body = newBody
		updated.EditedAt = editedAt
		raw, err := json.Marshal(updated)
		if err != nil {
			return err
		}
		return txn.Set(key, raw)
	})
	if err != nil {
		return Event{}, err
	}
	if l.bus != nil {
		l.bus.publish(updated)
	}
	return updated, nil
}

func (l *Log) UpdateFileBody(targetMsgID string, body json.RawMessage) (Event, error) {
	if targetMsgID == "" {
		return Event{}, ErrEventNotFound
	}
	var updated Event
	err := l.st.Update(func(txn *badger.Txn) error {
		idxItem, err := txn.Get([]byte(msgIDPrefix + targetMsgID))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrEventNotFound
		}
		if err != nil {
			return err
		}
		var key []byte
		if err := idxItem.Value(func(v []byte) error {
			key = append([]byte(nil), v...)
			return nil
		}); err != nil {
			return err
		}
		rowItem, err := txn.Get(key)
		if errors.Is(err, badger.ErrKeyNotFound) {
			_ = txn.Delete([]byte(msgIDPrefix + targetMsgID))
			return ErrEventNotFound
		}
		if err != nil {
			return err
		}
		if err := rowItem.Value(func(v []byte) error {
			return json.Unmarshal(v, &updated)
		}); err != nil {
			return err
		}
		if updated.Kind != KindFile {
			return ErrEditUnsupportedKind
		}
		updated.Body = body
		raw, err := json.Marshal(updated)
		if err != nil {
			return err
		}
		return txn.Set(key, raw)
	})
	if err != nil {
		return Event{}, err
	}
	if l.bus != nil {
		l.bus.publish(updated)
	}
	return updated, nil
}

func (l *Log) ApplyDelete(targetMsgID string, deletedAt int64, expectedSenderPeerID string) (Event, error) {
	if targetMsgID == "" {
		return Event{}, ErrEventNotFound
	}
	var updated Event
	err := l.st.Update(func(txn *badger.Txn) error {
		idxItem, err := txn.Get([]byte(msgIDPrefix + targetMsgID))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrEventNotFound
		}
		if err != nil {
			return err
		}
		var key []byte
		if err := idxItem.Value(func(v []byte) error {
			key = append([]byte(nil), v...)
			return nil
		}); err != nil {
			return err
		}
		rowItem, err := txn.Get(key)
		if errors.Is(err, badger.ErrKeyNotFound) {
			_ = txn.Delete([]byte(msgIDPrefix + targetMsgID))
			return ErrEventNotFound
		}
		if err != nil {
			return err
		}
		if err := rowItem.Value(func(v []byte) error {
			return json.Unmarshal(v, &updated)
		}); err != nil {
			return err
		}

		if updated.Kind != KindText && updated.Kind != KindFile {
			return ErrDeleteUnsupportedKind
		}
		if expectedSenderPeerID == "" {
			if updated.Direction != DirOut {
				return ErrDeleterNotAuthor
			}
		} else {
			if updated.Direction != DirIn {
				return ErrDeleterNotAuthor
			}
			if updated.SenderPeerID != "" && updated.SenderPeerID != expectedSenderPeerID {
				return ErrDeleterNotAuthor
			}
		}
		updated.Body = nil
		updated.DeletedAt = deletedAt
		raw, err := json.Marshal(updated)
		if err != nil {
			return err
		}
		return txn.Set(key, raw)
	})
	if err != nil {
		return Event{}, err
	}
	if l.bus != nil {
		l.bus.publish(updated)
	}
	return updated, nil
}

func (l *Log) AppendReactionBreadcrumb(targetMsgID, emoji, reactorPeerID string, at int64, expireSeconds uint32) (Event, error) {
	if targetMsgID == "" {
		return Event{}, ErrEventNotFound
	}
	target, err := l.GetByMsgID(targetMsgID)
	if err != nil {
		return Event{}, err
	}
	if !isReactable(target) {
		return Event{}, ErrReactionUnsupportedKind
	}
	if at == 0 {
		at = l.now().Unix()
	}
	body, err := json.Marshal(ReactionBody{TargetMsgID: targetMsgID, Emoji: emoji, At: at})
	if err != nil {
		return Event{}, fmt.Errorf("events: marshal reaction body: %w", err)
	}
	dir := DirOut
	if reactorPeerID != "" {
		dir = DirIn
	}
	return l.AppendLocal(LocalParams{
		ChatID:        target.ChatID,
		Kind:          KindReaction,
		Direction:     dir,
		DisplayTs:     target.DisplayTs,
		SenderPeerID:  reactorPeerID,
		ExpireSeconds: expireSeconds,
		Body:          body,
	})
}

func isReactable(ev Event) bool {
	if ev.DeletedAt > 0 {
		return false
	}
	if ev.DecryptStatus == DecryptFailed {
		return false
	}
	switch ev.Kind {
	case KindText, KindFile:
		return true
	default:
		return false
	}
}

func (l *Log) ListBefore(chatID chat.ChatID, beforeDisplayTs int64, limit int) ([]Event, error) {
	if chatID == "" {
		return nil, errors.New("events: empty chat id")
	}
	if limit <= 0 {
		limit = 50
	}
	chatPrefix := append([]byte(prefix), chatID...)
	chatPrefix = append(chatPrefix, ':')

	var out []Event
	err := l.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = chatPrefix
		opts.Reverse = true
		it := txn.NewIterator(opts)
		defer it.Close()

		if beforeDisplayTs > 0 {

			seek := make([]byte, len(chatPrefix)+8)
			copy(seek, chatPrefix)
			binary.BigEndian.PutUint64(seek[len(chatPrefix):], uint64(beforeDisplayTs))
			it.Seek(seek)
		} else {

			seekEnd := append(append([]byte(nil), chatPrefix...), 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff)
			it.Seek(seekEnd)
		}

		for ; it.Valid() && len(out) < limit; it.Next() {
			item := it.Item()
			key := item.Key()
			rest := key[len(chatPrefix):]
			if len(rest) < 8 {
				continue
			}
			displayTs := int64(binary.BigEndian.Uint64(rest[:8]))
			if beforeDisplayTs > 0 && displayTs >= beforeDisplayTs {
				continue
			}
			var ev Event
			if err := item.Value(func(v []byte) error {
				return json.Unmarshal(v, &ev)
			}); err != nil {
				return err
			}
			out = append(out, ev)
		}
		return nil
	})
	return out, err
}

const DefaultSearchCap = 500

type SearchHit struct {
	MsgID      string
	DisplayTs  int64
	RecvSeq    uint64
	BodyOffset int
}

func (l *Log) SearchInChat(chatID chat.ChatID, query string, maxMatches int) (matches []SearchHit, truncated bool, err error) {
	if chatID == "" {
		return nil, false, errors.New("events: empty chat id")
	}
	if query == "" {
		return nil, false, errors.New("events: empty query")
	}
	if maxMatches <= 0 {
		maxMatches = DefaultSearchCap
	}
	qLower := strings.ToLower(query)

	chatPrefix := append([]byte(prefix), chatID...)
	chatPrefix = append(chatPrefix, ':')

	err = l.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = chatPrefix
		opts.Reverse = true
		it := txn.NewIterator(opts)
		defer it.Close()

		seekEnd := append(append([]byte(nil), chatPrefix...), 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff)
		it.Seek(seekEnd)

		for ; it.Valid(); it.Next() {
			item := it.Item()
			var ev Event
			if vErr := item.Value(func(v []byte) error {
				return json.Unmarshal(v, &ev)
			}); vErr != nil {
				return vErr
			}
			if ev.Kind != KindText {
				continue
			}
			if ev.DeletedAt != 0 {
				continue
			}
			if ev.DecryptStatus == DecryptFailed {
				continue
			}
			if ev.MsgID == "" {
				continue
			}
			var tb TextBody
			if uErr := json.Unmarshal(ev.Body, &tb); uErr != nil {
				continue
			}
			if tb.Text == "" {
				continue
			}
			idx := strings.Index(strings.ToLower(tb.Text), qLower)
			if idx < 0 {
				continue
			}
			if len(matches) >= maxMatches {
				truncated = true
				return nil
			}
			matches = append(matches, SearchHit{
				MsgID:      ev.MsgID,
				DisplayTs:  ev.DisplayTs,
				RecvSeq:    ev.RecvSeq,
				BodyOffset: idx,
			})
		}
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("events: search in chat %s: %w", chatID, err)
	}
	return matches, truncated, nil
}

func (l *Log) DeleteByChat(chatID chat.ChatID) (int, int, error) {
	if chatID == "" {
		return 0, 0, errors.New("events: empty chat id")
	}
	chatPrefix := append([]byte(prefix), chatID...)
	chatPrefix = append(chatPrefix, ':')

	deleted := 0
	cascadeDeleted := 0
	for {

		type rowRef struct {
			key     []byte
			envelID string
			msgID   string
			kind    Kind
			cascade [][]byte
		}
		var batch []rowRef
		err := l.st.View(func(txn *badger.Txn) error {
			opts := badger.DefaultIteratorOptions
			opts.Prefix = chatPrefix
			it := txn.NewIterator(opts)
			defer it.Close()
			for it.Rewind(); it.Valid() && len(batch) < deleteBatchSize; it.Next() {
				item := it.Item()
				keyCopy := append([]byte(nil), item.Key()...)
				var ev Event
				if err := item.Value(func(v []byte) error {
					return json.Unmarshal(v, &ev)
				}); err != nil {
					return err
				}
				ref := rowRef{key: keyCopy, msgID: ev.MsgID, kind: ev.Kind, cascade: cascadeKeysFor(ev)}
				if ev.Direction == DirOut && ev.EnvelopeID != "" {
					ref.envelID = ev.EnvelopeID
				}
				batch = append(batch, ref)
			}
			return nil
		})
		if err != nil {
			return deleted, cascadeDeleted, fmt.Errorf("events: scan for delete chat=%s: %w", chatID, err)
		}
		if len(batch) == 0 {
			return deleted, cascadeDeleted, nil
		}
		err = l.st.Update(func(txn *badger.Txn) error {
			for _, r := range batch {
				if err := txn.Delete(r.key); err != nil {
					return err
				}
				if r.envelID != "" {
					if err := txn.Delete([]byte(envIDPrefix + r.envelID)); err != nil {
						return err
					}
				}
				if r.msgID != "" {
					if err := txn.Delete([]byte(msgIDPrefix + r.msgID)); err != nil {
						return err
					}
				}
				for _, k := range r.cascade {
					if err := txn.Delete(k); err != nil {
						return err
					}
				}
			}
			return nil
		})
		if err != nil {
			return deleted, cascadeDeleted, fmt.Errorf("events: delete batch chat=%s: %w", chatID, err)
		}

		if l.bus != nil {
			for _, r := range batch {
				if r.kind != KindFile || r.msgID == "" {
					continue
				}
				l.bus.publishDeletion(Deletion{ChatID: chatID, MsgID: r.msgID})
			}
		}
		deleted += len(batch)
		for _, r := range batch {
			cascadeDeleted += len(r.cascade)
		}
	}
}

const deleteBatchSize = 1000

func (l *Log) MarkRead(chatID chat.ChatID) (mutated int, pendingReceipt []string, err error) {
	if chatID == "" {
		return 0, nil, errors.New("events: empty chat id")
	}
	chatPrefix := append([]byte(prefix), chatID...)
	chatPrefix = append(chatPrefix, ':')

	nowSec := l.now().Unix()
	for {
		type rowRef struct {
			key   []byte
			ev    Event
			stamp bool
		}
		var batch []rowRef
		viewErr := l.st.View(func(txn *badger.Txn) error {
			opts := badger.DefaultIteratorOptions
			opts.Prefix = chatPrefix
			it := txn.NewIterator(opts)
			defer it.Close()
			for it.Rewind(); it.Valid() && len(batch) < deleteBatchSize; it.Next() {
				item := it.Item()
				var ev Event
				if err := item.Value(func(v []byte) error {
					return json.Unmarshal(v, &ev)
				}); err != nil {
					return err
				}
				if ev.Direction != DirIn {
					continue
				}
				if ev.DecryptStatus != DecryptOK {
					continue
				}
				stampReadAt := ev.ReadAt == 0

				receiptPending := ev.MsgID != "" && ev.ReadReceiptSentAt == 0 &&
					(ev.Kind == KindText || (ev.Kind == KindFile && ev.fileReceiptReady()))
				if !stampReadAt && !receiptPending {
					continue
				}
				batch = append(batch, rowRef{
					key:   append([]byte(nil), item.Key()...),
					ev:    ev,
					stamp: stampReadAt,
				})
			}
			return nil
		})
		if viewErr != nil {
			return mutated, pendingReceipt, fmt.Errorf("events: MarkRead scan chat=%s: %w", chatID, viewErr)
		}
		if len(batch) == 0 {
			return mutated, pendingReceipt, nil
		}
		writeErr := l.st.Update(func(txn *badger.Txn) error {
			for i := range batch {
				if !batch[i].stamp {
					continue
				}
				batch[i].ev.ReadAt = nowSec
				raw, err := json.Marshal(batch[i].ev)
				if err != nil {
					return err
				}
				if err := txn.Set(batch[i].key, raw); err != nil {
					return err
				}
			}
			return nil
		})
		if writeErr != nil {
			return mutated, pendingReceipt, fmt.Errorf("events: MarkRead write chat=%s: %w", chatID, writeErr)
		}
		for i := range batch {
			if batch[i].stamp {
				mutated++
			}
			if batch[i].ev.MsgID != "" && batch[i].ev.ReadReceiptSentAt == 0 &&
				(batch[i].ev.Kind == KindText || (batch[i].ev.Kind == KindFile && batch[i].ev.fileReceiptReady())) {
				pendingReceipt = append(pendingReceipt, batch[i].ev.MsgID)
			}
		}
		if len(batch) < deleteBatchSize {
			return mutated, pendingReceipt, nil
		}
	}
}

const ReadReceiptSuppressedSentinel int64 = 69

func (l *Log) MarkReadReceiptSent(msgIDs []string, at int64) error {
	if len(msgIDs) == 0 {
		return nil
	}
	return l.st.Update(func(txn *badger.Txn) error {
		for _, msgID := range msgIDs {
			if msgID == "" {
				continue
			}
			idxItem, err := txn.Get([]byte(msgIDPrefix + msgID))
			if errors.Is(err, badger.ErrKeyNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			var key []byte
			if err := idxItem.Value(func(v []byte) error {
				key = append([]byte(nil), v...)
				return nil
			}); err != nil {
				return err
			}
			rowItem, err := txn.Get(key)
			if errors.Is(err, badger.ErrKeyNotFound) {
				_ = txn.Delete([]byte(msgIDPrefix + msgID))
				continue
			}
			if err != nil {
				return err
			}
			var ev Event
			if err := rowItem.Value(func(v []byte) error {
				return json.Unmarshal(v, &ev)
			}); err != nil {
				return err
			}
			if ev.ReadReceiptSentAt != 0 {
				continue
			}
			ev.ReadReceiptSentAt = at
			raw, err := json.Marshal(ev)
			if err != nil {
				return err
			}
			if err := txn.Set(key, raw); err != nil {
				return err
			}
		}
		return nil
	})
}

func (l *Log) SuppressPendingReadReceipts(chatID chat.ChatID) (int, error) {
	if chatID == "" {
		return 0, errors.New("events: empty chat id")
	}
	chatPrefix := append([]byte(prefix), chatID...)
	chatPrefix = append(chatPrefix, ':')
	suppressed := 0
	for {
		type rowRef struct {
			key []byte
			ev  Event
		}
		var batch []rowRef
		viewErr := l.st.View(func(txn *badger.Txn) error {
			opts := badger.DefaultIteratorOptions
			opts.Prefix = chatPrefix
			it := txn.NewIterator(opts)
			defer it.Close()
			for it.Rewind(); it.Valid() && len(batch) < deleteBatchSize; it.Next() {
				item := it.Item()
				var ev Event
				if err := item.Value(func(v []byte) error {
					return json.Unmarshal(v, &ev)
				}); err != nil {
					return err
				}
				if ev.Direction != DirIn {
					continue
				}
				if ev.DecryptStatus != DecryptOK {
					continue
				}
				if ev.Kind != KindText {
					continue
				}
				if ev.MsgID == "" {
					continue
				}
				if ev.ReadReceiptSentAt != 0 {
					continue
				}
				batch = append(batch, rowRef{
					key: append([]byte(nil), item.Key()...),
					ev:  ev,
				})
			}
			return nil
		})
		if viewErr != nil {
			return suppressed, fmt.Errorf("events: SuppressPendingReadReceipts scan chat=%s: %w", chatID, viewErr)
		}
		if len(batch) == 0 {
			return suppressed, nil
		}
		writeErr := l.st.Update(func(txn *badger.Txn) error {
			for i := range batch {
				batch[i].ev.ReadReceiptSentAt = ReadReceiptSuppressedSentinel
				raw, err := json.Marshal(batch[i].ev)
				if err != nil {
					return err
				}
				if err := txn.Set(batch[i].key, raw); err != nil {
					return err
				}
			}
			return nil
		})
		if writeErr != nil {
			return suppressed, fmt.Errorf("events: SuppressPendingReadReceipts write chat=%s: %w", chatID, writeErr)
		}
		suppressed += len(batch)
		if len(batch) < deleteBatchSize {
			return suppressed, nil
		}
	}
}

func (l *Log) ApplyReadReceipt(targetMsgID string, readAt int64, expectedChatID chat.ChatID) (Event, error) {
	if targetMsgID == "" {
		return Event{}, ErrEventNotFound
	}
	if readAt <= 0 {
		return Event{}, fmt.Errorf("events: read_at must be > 0")
	}
	var updated Event
	err := l.st.Update(func(txn *badger.Txn) error {
		idxItem, err := txn.Get([]byte(msgIDPrefix + targetMsgID))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrEventNotFound
		}
		if err != nil {
			return err
		}
		var key []byte
		if err := idxItem.Value(func(v []byte) error {
			key = append([]byte(nil), v...)
			return nil
		}); err != nil {
			return err
		}
		rowItem, err := txn.Get(key)
		if errors.Is(err, badger.ErrKeyNotFound) {
			_ = txn.Delete([]byte(msgIDPrefix + targetMsgID))
			return ErrEventNotFound
		}
		if err != nil {
			return err
		}
		if err := rowItem.Value(func(v []byte) error {
			return json.Unmarshal(v, &updated)
		}); err != nil {
			return err
		}
		if updated.ChatID != expectedChatID {
			return ErrReaderNotPeer
		}
		if updated.Direction != DirOut {
			return ErrReaderNotPeer
		}

		if updated.Kind != KindText && updated.Kind != KindFile {
			return ErrReadUnsupportedKind
		}

		if updated.ReadAt == 0 || readAt < updated.ReadAt {
			updated.ReadAt = readAt
		}
		updated.DeliveryState = "read"
		raw, err := json.Marshal(updated)
		if err != nil {
			return err
		}
		return txn.Set(key, raw)
	})
	if err != nil {
		return Event{}, err
	}
	if l.bus != nil {
		l.bus.publish(updated)
	}
	return updated, nil
}

func (l *Log) ApplyDeliveredReceipt(targetMsgID string, expectedChatID chat.ChatID) (Event, error) {
	if targetMsgID == "" {
		return Event{}, ErrEventNotFound
	}
	var updated Event
	changed := false
	err := l.st.Update(func(txn *badger.Txn) error {
		idxItem, err := txn.Get([]byte(msgIDPrefix + targetMsgID))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrEventNotFound
		}
		if err != nil {
			return err
		}
		var key []byte
		if err := idxItem.Value(func(v []byte) error {
			key = append([]byte(nil), v...)
			return nil
		}); err != nil {
			return err
		}
		rowItem, err := txn.Get(key)
		if errors.Is(err, badger.ErrKeyNotFound) {
			_ = txn.Delete([]byte(msgIDPrefix + targetMsgID))
			return ErrEventNotFound
		}
		if err != nil {
			return err
		}
		if err := rowItem.Value(func(v []byte) error {
			return json.Unmarshal(v, &updated)
		}); err != nil {
			return err
		}
		if updated.ChatID != expectedChatID {
			return ErrReaderNotPeer
		}
		if updated.Direction != DirOut {
			return ErrReaderNotPeer
		}
		if updated.Kind != KindFile {
			return ErrReadUnsupportedKind
		}

		if updated.DeliveryState == "read" || updated.DeliveryState == "delivered" {
			return nil
		}
		updated.DeliveryState = "delivered"
		changed = true
		raw, err := json.Marshal(updated)
		if err != nil {
			return err
		}
		return txn.Set(key, raw)
	})
	if err != nil {
		return Event{}, err
	}
	if changed && l.bus != nil {
		l.bus.publish(updated)
	}
	return updated, nil
}

func (ev *Event) expired(nowSec int64) bool {
	isContent := ev.Kind == KindText || ev.Kind == KindFile
	if isContent && ev.DecryptStatus != DecryptFailed {
		switch ev.Direction {
		case DirOut:
			return ev.DisplayTs+int64(ev.ExpireSeconds) <= nowSec
		case DirIn:
			if ev.DecryptStatus != DecryptOK {
				return false
			}
			return ev.ReadAt > 0 && ev.ReadAt+int64(ev.ExpireSeconds) <= nowSec
		default:
			return false
		}
	}
	return ev.RecvTs+int64(ev.ExpireSeconds) <= nowSec
}

func (l *Log) SweepExpired(nowSec int64) (int, int, error) {
	deleted := 0
	cascadeDeleted := 0
	for {
		type rowRef struct {
			key     []byte
			chatID  chat.ChatID
			recvSeq uint64
			envelID string
			msgID   string
			cascade [][]byte
		}
		var batch []rowRef
		err := l.st.View(func(txn *badger.Txn) error {
			opts := badger.DefaultIteratorOptions
			opts.Prefix = []byte(prefix)
			it := txn.NewIterator(opts)
			defer it.Close()
			for it.Rewind(); it.Valid() && len(batch) < deleteBatchSize; it.Next() {
				item := it.Item()
				var ev Event
				if err := item.Value(func(v []byte) error {
					return json.Unmarshal(v, &ev)
				}); err != nil {
					return err
				}
				if ev.ExpireSeconds == 0 {
					continue
				}
				if !ev.expired(nowSec) {
					continue
				}
				ref := rowRef{
					key:     append([]byte(nil), item.Key()...),
					chatID:  ev.ChatID,
					recvSeq: ev.RecvSeq,
					msgID:   ev.MsgID,
					cascade: cascadeKeysFor(ev),
				}
				if ev.Direction == DirOut && ev.EnvelopeID != "" {
					ref.envelID = ev.EnvelopeID
				}
				batch = append(batch, ref)
			}
			return nil
		})
		if err != nil {
			return deleted, cascadeDeleted, fmt.Errorf("events: SweepExpired scan: %w", err)
		}
		if len(batch) == 0 {
			return deleted, cascadeDeleted, nil
		}
		err = l.st.Update(func(txn *badger.Txn) error {
			for _, r := range batch {
				if err := txn.Delete(r.key); err != nil {
					return err
				}
				if r.envelID != "" {
					if err := txn.Delete([]byte(envIDPrefix + r.envelID)); err != nil {
						return err
					}
				}
				if r.msgID != "" {
					if err := txn.Delete([]byte(msgIDPrefix + r.msgID)); err != nil {
						return err
					}
				}
				for _, k := range r.cascade {
					if err := txn.Delete(k); err != nil {
						return err
					}
				}
			}
			return nil
		})
		if err != nil {
			return deleted, cascadeDeleted, fmt.Errorf("events: SweepExpired write: %w", err)
		}
		if l.bus != nil {
			for _, r := range batch {
				l.bus.publishDeletion(Deletion{ChatID: r.chatID, RecvSeq: r.recvSeq, MsgID: r.msgID})
			}
		}
		deleted += len(batch)
		for _, r := range batch {
			cascadeDeleted += len(r.cascade)
		}
		if len(batch) < deleteBatchSize {
			return deleted, cascadeDeleted, nil
		}
	}
}

func ClampSenderTs(senderTs, nowSec int64) int64 {
	return clampDisplayTs(senderTs, nowSec)
}

func clampDisplayTs(senderTs, nowSec int64) int64 {
	if senderTs <= 0 {

		return nowSec
	}
	minOK := nowSec - SkewMaxPastSec
	maxOK := nowSec + SkewMaxFutureSec
	if senderTs < minOK {
		return minOK
	}
	if senderTs > maxOK {
		return maxOK
	}
	return senderTs
}

func storageKey(chatID chat.ChatID, displayTs int64, recvSeq uint64) []byte {
	out := make([]byte, 0, len(prefix)+len(chatID)+1+8+1+8)
	out = append(out, prefix...)
	out = append(out, chatID...)
	out = append(out, ':')
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(displayTs))
	out = append(out, ts[:]...)
	out = append(out, ':')
	var seq [8]byte
	binary.BigEndian.PutUint64(seq[:], recvSeq)
	out = append(out, seq[:]...)
	return out
}

func loadUint64(txn *badger.Txn, key []byte) (uint64, error) {
	item, err := txn.Get(key)
	if errors.Is(err, badger.ErrKeyNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var v uint64
	err = item.Value(func(raw []byte) error {
		if len(raw) != 8 {
			return fmt.Errorf("events: counter wrong length %d", len(raw))
		}
		v = binary.BigEndian.Uint64(raw)
		return nil
	})
	return v, err
}
