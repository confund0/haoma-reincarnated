package events

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
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

var ErrDuplicateMsgID = errors.New("events: a row with this msg id is already held")

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

	KindInvitation Kind = "invitation"
)

const (
	InvitationLive = "live"

	InvitationSuperseded = "superseded"

	InvitationAccepted = "accepted"

	InvitationFailed = "failed"
)

func InvitationTerminal(state string) bool {
	return state == InvitationAccepted || state == InvitationFailed
}

func InvitationAnswerable(state string, expiresAt, now int64) bool {
	return state == InvitationLive && expiresAt > 0 && now < expiresAt
}

type InvitationBody struct {
	ExpiresAt int64  `json:"expires_at"`
	State     string `json:"state"`
}

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

	CallOutcomeAnsweredElsewhere CallOutcome = "answered_elsewhere"
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
	RecvSeq   uint64      `json:"recv_seq"`
	ChatID    chat.ChatID `json:"chat_id"`
	Direction Direction   `json:"direction"`
	Kind      Kind        `json:"kind"`
	DisplayTs int64       `json:"display_ts"`
	SenderTs  int64       `json:"sender_ts,omitempty"`
	RecvTs    int64       `json:"recv_ts"`
	SenderSeq uint64      `json:"sender_seq,omitempty"`

	SenderPeerID string `json:"sender_peer_id,omitempty"`

	ArrivedFromPeerID string `json:"arrived_from_peer_id,omitempty"`
	EnvelopeID        string `json:"envelope_id,omitempty"`

	EnvelopeIDs   []string          `json:"envelope_ids,omitempty"`
	MsgID         string            `json:"msg_id,omitempty"`
	DecryptStatus DecryptStatus     `json:"decrypt_status,omitempty"`
	FailReason    DecryptFailReason `json:"fail_reason,omitempty"`
	Body          json.RawMessage   `json:"body,omitempty"`
	RawBlob       []byte            `json:"raw_blob,omitempty"`

	DeliveryState string `json:"delivery_state,omitempty"`

	FailedEnvelopeIDs []string `json:"failed_envelope_ids,omitempty"`

	MirroredFromSibling bool `json:"mirrored_from_sibling,omitempty"`

	ExpireSeconds uint32 `json:"expire_seconds,omitempty"`

	ReadAt int64 `json:"read_at,omitempty"`

	EditedAt int64 `json:"edited_at,omitempty"`

	DeletedAt int64 `json:"deleted_at,omitempty"`

	ReadReceiptSentAt int64 `json:"read_receipt_sent_at,omitempty"`

	ReceiptClaimOrigin bool `json:"receipt_claim_origin,omitempty"`

	ReceiptClaimGeneration uint64 `json:"receipt_claim_generation,omitempty"`
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

	retryTestHook func()

	journalGrowth atomic.Pointer[func()]
}

func (l *Log) SetJournalGrowthHook(fn func()) {
	if fn == nil {
		l.journalGrowth.Store(nil)
		return
	}
	l.journalGrowth.Store(&fn)
}

func New(st *store.Store, bus *Bus, now func() time.Time) *Log {
	if now == nil {
		now = time.Now
	}
	return &Log{st: st, bus: bus, now: now}
}

func (l *Log) AppendInbound(in InboundParams) (Event, error) {
	now := l.now()

	displayTs := in.DisplayTs
	if displayTs == 0 {
		displayTs = clampDisplayTs(in.SenderTs, now.Unix())
	}
	ev := Event{
		ChatID:              in.ChatID,
		Direction:           DirIn,
		Kind:                in.Kind,
		DisplayTs:           displayTs,
		SenderTs:            in.SenderTs,
		RecvTs:              now.Unix(),
		SenderSeq:           in.SenderSeq,
		SenderPeerID:        in.SenderPeerID,
		ArrivedFromPeerID:   in.ArrivedFromPeerID,
		EnvelopeID:          in.EnvelopeID,
		MsgID:               in.MsgID,
		ExpireSeconds:       in.ExpireSeconds,
		DecryptStatus:       in.Status,
		FailReason:          in.FailReason,
		Body:                in.Body,
		RawBlob:             in.RawBlob,
		MirroredFromSibling: in.MirroredFromSibling,
	}
	return l.append(ev, in.Attach)
}

func (l *Log) RecordArrivedFrom(msgID, peerID string) (bool, error) {
	if msgID == "" || peerID == "" {
		return false, nil
	}
	slog.Debug("events: RecordArrivedFrom observed",
		slog.String("msg_id", msgID),
		slog.String("peer_id", peerID),
	)
	changed := false
	reason := ""

	err := l.updateRetry(func(m mutatingTxn) error {

		changed, reason = false, ""
		idxItem, err := m.get([]byte(msgIDPrefix + msgID))
		if errors.Is(err, badger.ErrKeyNotFound) {
			reason = "row_not_held"
			return nil
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
		rowItem, err := m.get(key)
		if errors.Is(err, badger.ErrKeyNotFound) {
			_ = m.deleteAux([]byte(msgIDPrefix + msgID))
			reason = "row_swept"
			return nil
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
		switch {
		case ev.Direction != DirIn:
			reason = "row_is_outbound"
			return nil
		case ev.DeletedAt != 0:
			reason = "row_is_deleted"
			return nil
		case ev.ArrivedFromPeerID == peerID:
			reason = "already_recorded"
			return nil
		case ev.ArrivedFromPeerID != "":
			reason = "another_device_already_recorded"
			return nil
		}
		ev.ArrivedFromPeerID = peerID
		raw, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		if err := m.storeRow(MutatorRecordArrivedFrom, ev.ChatID, ev.MsgID, key, raw); err != nil {
			return err
		}
		changed = true
		reason = "recorded"
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("events: RecordArrivedFrom msg=%s: %w", msgID, err)
	}

	slog.Debug("events: RecordArrivedFrom decision",
		slog.String("msg_id", msgID),
		slog.String("peer_id", peerID),
		slog.Bool("changed", changed),
		slog.String("outcome", reason),
	)
	return changed, nil
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

	displayTs := out.DisplayTs
	if displayTs == 0 {
		displayTs = now.Unix()
	}
	delivery := out.DeliveryState
	if delivery == "" {
		delivery = DeliveryEnqueued
	}
	if out.MirroredFromSibling {
		delivery = DeliverySeedFromSibling(delivery)
	}
	ev := Event{
		ChatID:              out.ChatID,
		Direction:           DirOut,
		Kind:                out.Kind,
		DisplayTs:           displayTs,
		SenderTs:            displayTs,
		RecvTs:              now.Unix(),
		SenderSeq:           out.SenderSeq,
		EnvelopeID:          primaryEnvID,
		EnvelopeIDs:         envIDs,
		MsgID:               out.MsgID,
		ExpireSeconds:       out.ExpireSeconds,
		Body:                out.Body,
		DeliveryState:       delivery,
		MirroredFromSibling: out.MirroredFromSibling,
	}
	return l.append(ev, nil)
}

type TxnWrite func(txn *badger.Txn) error

type InboundParams struct {
	ChatID chat.ChatID
	Kind   Kind

	DisplayTs    int64
	SenderTs     int64
	SenderSeq    uint64
	SenderPeerID string

	ArrivedFromPeerID string
	EnvelopeID        string
	MsgID             string
	ExpireSeconds     uint32
	Status            DecryptStatus
	FailReason        DecryptFailReason
	Body              json.RawMessage
	RawBlob           []byte

	MirroredFromSibling bool

	Attach TxnWrite
}

type LocalParams struct {
	ChatID    chat.ChatID
	Kind      Kind
	Direction Direction
	DisplayTs int64

	MsgID         string
	SenderPeerID  string
	ExpireSeconds uint32
	Body          json.RawMessage
}

type OutboundParams struct {
	ChatID chat.ChatID
	Kind   Kind

	DisplayTs int64
	SenderSeq uint64

	EnvelopeIDs   []string
	EnvelopeID    string
	MsgID         string
	ExpireSeconds uint32
	Body          json.RawMessage

	DeliveryState string

	MirroredFromSibling bool
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
		MsgID:         p.MsgID,
		ExpireSeconds: p.ExpireSeconds,
		Body:          p.Body,
	}
	return l.append(ev, nil)
}

func (l *Log) append(ev Event, attach TxnWrite) (Event, error) {
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
	var held Event
	duplicate := false
	if err := l.updateRetry(func(m mutatingTxn) error {

		held, duplicate = Event{}, false
		if ev.MsgID != "" {
			prior, lookupErr := eventByMsgIDTxn(m, ev.MsgID)
			switch {
			case lookupErr == nil:
				held, duplicate = prior, true
				return nil
			case errors.Is(lookupErr, ErrEventNotFound):

			default:
				return lookupErr
			}
		}

		if err := m.storeRow(MutatorAppend, ev.ChatID, ev.MsgID, key, raw); err != nil {
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
				if err := m.setAux([]byte(envIDPrefix+eid), key); err != nil {
					return err
				}
			}
		}

		if ev.MsgID != "" {
			if err := m.setAux([]byte(msgIDPrefix+ev.MsgID), key); err != nil {
				return err
			}
		}
		return m.runAttach(attach)
	}); err != nil {
		return Event{}, fmt.Errorf("events: persist event for %s: %w", ev.ChatID, err)
	}
	if duplicate {
		if held.ChatID != ev.ChatID {
			slog.Warn("events: msg id is held by a different conversation; the arriving row is refused",
				slog.String("msg_id", ev.MsgID),
				slog.String("holding_chat_id", string(held.ChatID)),
				slog.String("refused_chat_id", string(ev.ChatID)),
				slog.String("kind", string(ev.Kind)),
			)
		}
		return held, ErrDuplicateMsgID
	}
	if l.bus != nil {
		l.bus.publish(ev)
	}
	return ev, nil
}

func eventByMsgIDTxn(m mutatingTxn, msgID string) (Event, error) {
	idxItem, err := m.get([]byte(msgIDPrefix + msgID))
	if errors.Is(err, badger.ErrKeyNotFound) {
		return Event{}, ErrEventNotFound
	}
	if err != nil {
		return Event{}, err
	}
	var key []byte
	if err := idxItem.Value(func(v []byte) error {
		key = append([]byte(nil), v...)
		return nil
	}); err != nil {
		return Event{}, err
	}
	rowItem, err := m.get(key)
	if errors.Is(err, badger.ErrKeyNotFound) {
		return Event{}, ErrEventNotFound
	}
	if err != nil {
		return Event{}, err
	}
	var ev Event
	if err := rowItem.Value(func(v []byte) error {
		return json.Unmarshal(v, &ev)
	}); err != nil {
		return Event{}, err
	}
	return ev, nil
}

func (l *Log) updateRetry(fn func(m mutatingTxn) error) error {
	var err error
	for attempt := 0; attempt < maxConflictRetries; attempt++ {
		err = l.attemptUpdate(fn)
		if !errors.Is(err, badger.ErrConflict) {
			return err
		}
	}
	return fmt.Errorf("events: exceeded %d conflict retries: %w", maxConflictRetries, err)
}

func (l *Log) attemptUpdate(fn func(m mutatingTxn) error) (err error) {
	var txnUsed *badger.Txn
	committed := false

	journalled := false
	defer func() {
		if txnUsed == nil {
			return
		}
		if committed {
			logJournalTouches(txnUsed)
		} else {
			discardJournalTouches(txnUsed)
		}
	}()
	err = l.st.Update(func(txn *badger.Txn) error {
		txnUsed = txn
		return fn(mutatingTxn{txn: txn, journalled: &journalled})
	})
	committed = err == nil
	if committed && journalled {

		if hook := l.journalGrowth.Load(); hook != nil {
			(*hook)()
		}
	}
	return err
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

func (l *Log) EachFileRow(fn func(Event) error) error {
	return l.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefix)
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			var ev Event
			if err := it.Item().Value(func(v []byte) error {
				return json.Unmarshal(v, &ev)
			}); err != nil {
				return err
			}
			if ev.Kind != KindFile {
				continue
			}
			if err := fn(ev); err != nil {
				return err
			}
		}
		return nil
	})
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

func (l *Log) UpdateDeliveryState(envelopeID, state string) (row Event, moved bool, err error) {
	if envelopeID == "" {
		return Event{}, false, errors.New("events: empty envelope id")
	}
	var updated Event

	err = l.updateRetry(func(m mutatingTxn) error {

		moved = false
		idxItem, err := m.get([]byte(envIDPrefix + envelopeID))
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
		rowItem, err := m.get(key)
		if errors.Is(err, badger.ErrKeyNotFound) {

			_ = m.deleteAux([]byte(envIDPrefix + envelopeID))
			return ErrEventNotFound
		}
		if err != nil {
			return err
		}

		updated = Event{}
		if err := rowItem.Value(func(v []byte) error {
			return json.Unmarshal(v, &updated)
		}); err != nil {
			return err
		}
		if l.retryTestHook != nil {
			l.retryTestHook()
		}

		listChanged := updated.noteEnvelopeOutcome(envelopeID, state)
		if !deliveryFoldAdvances(updated, state) {
			if !listChanged {
				return nil
			}
			raw, err := json.Marshal(updated)
			if err != nil {
				return err
			}
			return m.storeRow(MutatorRecordEnvelopeOutcome, updated.ChatID, updated.MsgID, key, raw)
		}
		updated.DeliveryState = state
		newRaw, err := json.Marshal(updated)
		if err != nil {
			return err
		}

		if err := m.storeRow(MutatorUpdateDeliveryState, updated.ChatID, updated.MsgID, key, newRaw); err != nil {
			return err
		}
		moved = true
		return nil
	})
	if err != nil {
		return Event{}, false, err
	}
	return updated, moved, nil
}

func (l *Log) ApplyEdit(targetMsgID, newText string, editedAt int64, expectedSenderPeerID string) (Event, error) {
	if targetMsgID == "" {
		return Event{}, ErrEventNotFound
	}
	if newText == "" {
		return Event{}, fmt.Errorf("events: empty edit text")
	}
	var updated Event
	err := l.updateRetry(func(m mutatingTxn) error {
		idxItem, err := m.get([]byte(msgIDPrefix + targetMsgID))
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
		rowItem, err := m.get(key)
		if errors.Is(err, badger.ErrKeyNotFound) {
			_ = m.deleteAux([]byte(msgIDPrefix + targetMsgID))
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

		return m.storeRow(MutatorApplyEdit, updated.ChatID, updated.MsgID, key, raw)
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

	err := l.updateRetry(func(m mutatingTxn) error {
		idxItem, err := m.get([]byte(msgIDPrefix + targetMsgID))
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
		rowItem, err := m.get(key)
		if errors.Is(err, badger.ErrKeyNotFound) {
			_ = m.deleteAux([]byte(msgIDPrefix + targetMsgID))
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

		return m.storeRow(MutatorUpdateFileBody, updated.ChatID, updated.MsgID, key, raw)
	})
	if err != nil {
		return Event{}, err
	}
	if l.bus != nil {
		l.bus.publish(updated)
	}
	return updated, nil
}

func (l *Log) UpdateInvitationBody(targetMsgID string, body json.RawMessage, attach TxnWrite) (Event, error) {
	if targetMsgID == "" {
		return Event{}, ErrEventNotFound
	}
	var updated Event

	err := l.updateRetry(func(m mutatingTxn) error {
		idxItem, err := m.get([]byte(msgIDPrefix + targetMsgID))
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
		rowItem, err := m.get(key)
		if errors.Is(err, badger.ErrKeyNotFound) {
			_ = m.deleteAux([]byte(msgIDPrefix + targetMsgID))
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
		if updated.Kind != KindInvitation {
			return ErrEditUnsupportedKind
		}
		updated.Body = body
		raw, err := json.Marshal(updated)
		if err != nil {
			return err
		}

		if err := m.storeRow(MutatorUpdateInvitationBody, updated.ChatID, updated.MsgID, key, raw); err != nil {
			return err
		}
		return m.runAttach(attach)
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
	err := l.updateRetry(func(m mutatingTxn) error {
		idxItem, err := m.get([]byte(msgIDPrefix + targetMsgID))
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
		rowItem, err := m.get(key)
		if errors.Is(err, badger.ErrKeyNotFound) {
			_ = m.deleteAux([]byte(msgIDPrefix + targetMsgID))
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

		return m.storeRow(MutatorApplyDelete, updated.ChatID, updated.MsgID, key, raw)
	})
	if err != nil {
		return Event{}, err
	}
	if l.bus != nil {
		l.bus.publish(updated)
	}
	return updated, nil
}

func (l *Log) AppendReactionBreadcrumb(targetMsgID, emoji, reactorPeerID, msgID string, at int64, expireSeconds uint32) (Event, error) {
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
		MsgID:         msgID,
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
	return l.deleteByChat(chatID, nil)
}

func (l *Log) DeleteByChatThrough(chatID chat.ChatID, through int64) (int, int, error) {
	return l.deleteByChat(chatID, func(ev Event) bool { return ev.DisplayTs <= through })
}

func (l *Log) deleteByChat(chatID chat.ChatID, match func(Event) bool) (int, int, error) {
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
				if match != nil && !match(ev) {
					continue
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

type ReceiptTarget struct {
	MsgID             string
	ArrivedFromPeerID string
}

func ReceiptMsgIDs(targets []ReceiptTarget) []string {
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		out = append(out, t.MsgID)
	}
	return out
}

func (l *Log) MarkRead(chatID chat.ChatID) (mutated int, pendingReceipt []ReceiptTarget, err error) {
	if chatID == "" {
		return 0, nil, errors.New("events: empty chat id")
	}
	slog.Debug("events: MarkRead observed", slog.String("chat_id", string(chatID)))
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

		freshRows := make([]Event, len(batch))
		stampedNow := make([]bool, len(batch))

		writeErr := l.updateRetry(func(m mutatingTxn) error {
			for i := range batch {
				if !batch[i].stamp {
					continue
				}

				item, err := m.get(batch[i].key)
				if errors.Is(err, badger.ErrKeyNotFound) {

					stampedNow[i] = false
					continue
				}
				if err != nil {
					return err
				}
				var fresh Event
				if err := item.Value(func(v []byte) error {
					return json.Unmarshal(v, &fresh)
				}); err != nil {
					return err
				}
				if fresh.ReadAt != 0 {

					freshRows[i] = fresh
					stampedNow[i] = false
					continue
				}
				fresh.ReadAt = nowSec
				raw, err := json.Marshal(fresh)
				if err != nil {
					return err
				}

				if err := m.storeRow(MutatorMarkRead, fresh.ChatID, fresh.MsgID, batch[i].key, raw); err != nil {
					return err
				}
				freshRows[i] = fresh
				stampedNow[i] = true
			}
			return nil
		})
		if writeErr != nil {
			return mutated, pendingReceipt, fmt.Errorf("events: MarkRead write chat=%s: %w", chatID, writeErr)
		}
		batchStamped, batchPending := 0, 0
		for i := range batch {
			ev := batch[i].ev
			if batch[i].stamp {
				ev = freshRows[i]
				if stampedNow[i] {
					mutated++
					batchStamped++
				}
			}
			if ev.MsgID != "" && ev.ReadReceiptSentAt == 0 &&
				(ev.Kind == KindText || (ev.Kind == KindFile && ev.fileReceiptReady())) {

				pendingReceipt = append(pendingReceipt, ReceiptTarget{
					MsgID:             ev.MsgID,
					ArrivedFromPeerID: ev.ArrivedFromPeerID,
				})
				batchPending++
			}
		}

		slog.Debug("events: MarkRead batch applied",
			slog.String("chat_id", string(chatID)),
			slog.Int("batch_size", len(batch)),
			slog.Int("stamped", batchStamped),
			slog.Int("pending_receipt", batchPending),
		)
		if len(batch) < deleteBatchSize {
			return mutated, pendingReceipt, nil
		}
	}
}

func (l *Log) CountUnread(chatID chat.ChatID) (int, error) {
	if chatID == "" {
		return 0, errors.New("events: empty chat id")
	}
	chatPrefix := append([]byte(prefix), chatID...)
	chatPrefix = append(chatPrefix, ':')

	count := 0
	err := l.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = chatPrefix
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.ValidForPrefix(opts.Prefix); it.Next() {
			var ev Event
			if err := it.Item().Value(func(v []byte) error {
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
			if ev.Kind != KindText && ev.Kind != KindFile {
				continue
			}
			if ev.ReadAt != 0 {
				continue
			}
			count++
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("events: count unread chat=%s: %w", chatID, err)
	}
	return count, nil
}

const ReadReceiptSuppressedSentinel int64 = 69

func (l *Log) MarkReadReceiptSent(msgIDs []string, at int64) error {
	if len(msgIDs) == 0 {
		return nil
	}

	return l.updateRetry(func(m mutatingTxn) error {
		for _, msgID := range msgIDs {
			if msgID == "" {
				continue
			}
			idxItem, err := m.get([]byte(msgIDPrefix + msgID))
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
			rowItem, err := m.get(key)
			if errors.Is(err, badger.ErrKeyNotFound) {
				_ = m.deleteAux([]byte(msgIDPrefix + msgID))
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

			honest := at != ReadReceiptSuppressedSentinel
			ev.ReceiptClaimOrigin = honest
			raw, err := json.Marshal(ev)
			if err != nil {
				return err
			}

			mutator := MutatorMarkReadReceiptSent
			if !honest {
				mutator = MutatorMarkReadReceiptSentSuppressed
			}
			if err := m.storeRow(mutator, ev.ChatID, ev.MsgID, key, raw); err != nil {
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
		batchSuppressed := 0

		writeErr := l.updateRetry(func(m mutatingTxn) error {
			batchSuppressed = 0
			for i := range batch {

				item, err := m.get(batch[i].key)
				if errors.Is(err, badger.ErrKeyNotFound) {

					continue
				}
				if err != nil {
					return err
				}
				var fresh Event
				if err := item.Value(func(v []byte) error {
					return json.Unmarshal(v, &fresh)
				}); err != nil {
					return err
				}
				if fresh.ReadReceiptSentAt != 0 {

					continue
				}
				fresh.ReadReceiptSentAt = ReadReceiptSuppressedSentinel
				raw, err := json.Marshal(fresh)
				if err != nil {
					return err
				}

				if err := m.storeRow(MutatorSuppressPendingReadReceipts, fresh.ChatID, fresh.MsgID, batch[i].key, raw); err != nil {
					return err
				}
				batchSuppressed++
			}
			return nil
		})
		if writeErr != nil {
			return suppressed, fmt.Errorf("events: SuppressPendingReadReceipts write chat=%s: %w", chatID, writeErr)
		}
		suppressed += batchSuppressed
		if len(batch) < deleteBatchSize {
			return suppressed, nil
		}
	}
}

func (l *Log) ApplyReadReceipt(targetMsgID string, readAt int64, expectedChatID chat.ChatID) (Event, bool, error) {
	if targetMsgID == "" {
		return Event{}, false, ErrEventNotFound
	}
	if readAt <= 0 {
		return Event{}, false, fmt.Errorf("events: read_at must be > 0")
	}
	slog.Debug("events: ApplyReadReceipt observed",
		slog.String("msg_id", targetMsgID),
		slog.Int64("read_at", readAt),
	)
	var updated Event
	var changed bool

	err := l.updateRetry(func(m mutatingTxn) error {

		updated = Event{}
		idxItem, err := m.get([]byte(msgIDPrefix + targetMsgID))
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
		rowItem, err := m.get(key)
		if errors.Is(err, badger.ErrKeyNotFound) {
			_ = m.deleteAux([]byte(msgIDPrefix + targetMsgID))
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
		if l.retryTestHook != nil {
			l.retryTestHook()
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

		priorReadAt, priorState := updated.ReadAt, updated.DeliveryState
		if updated.ReadAt == 0 || readAt < updated.ReadAt {
			updated.ReadAt = readAt
		}
		updated.DeliveryState = "read"
		changed = updated.ReadAt != priorReadAt || updated.DeliveryState != priorState
		if !changed {

			return nil
		}
		raw, err := json.Marshal(updated)
		if err != nil {
			return err
		}

		return m.storeRow(MutatorApplyReadReceipt, updated.ChatID, updated.MsgID, key, raw)
	})
	if err != nil {
		return Event{}, false, err
	}

	slog.Debug("events: ApplyReadReceipt decision",
		slog.String("msg_id", updated.MsgID),
		slog.String("chat_id", string(updated.ChatID)),
		slog.Bool("changed", changed),
	)
	if l.bus != nil {
		l.bus.publish(updated)
	}
	return updated, changed, nil
}

func (l *Log) ApplyDeliveredReceipt(targetMsgID string, expectedChatID chat.ChatID) (Event, error) {
	if targetMsgID == "" {
		return Event{}, ErrEventNotFound
	}
	var updated Event
	changed := false

	err := l.updateRetry(func(m mutatingTxn) error {
		idxItem, err := m.get([]byte(msgIDPrefix + targetMsgID))
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
		rowItem, err := m.get(key)
		if errors.Is(err, badger.ErrKeyNotFound) {
			_ = m.deleteAux([]byte(msgIDPrefix + targetMsgID))
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

		if !DeliveryOutranks(updated.DeliveryState, DeliveryDelivered) {
			return nil
		}
		updated.DeliveryState = DeliveryDelivered
		changed = true
		raw, err := json.Marshal(updated)
		if err != nil {
			return err
		}

		return m.storeRow(MutatorApplyDeliveredReceipt, updated.ChatID, updated.MsgID, key, raw)
	})
	if err != nil {
		return Event{}, err
	}
	if changed && l.bus != nil {
		l.bus.publish(updated)
	}
	return updated, nil
}

func (l *Log) ApplyInboundReadAt(targetMsgID string, readAt int64) (Event, bool, error) {
	if targetMsgID == "" {
		return Event{}, false, ErrEventNotFound
	}
	if readAt <= 0 {
		return Event{}, false, fmt.Errorf("events: read_at must be > 0")
	}
	slog.Debug("events: ApplyInboundReadAt observed",
		slog.String("msg_id", targetMsgID),
		slog.Int64("read_at", readAt),
	)
	var updated Event
	changed := false

	err := l.updateRetry(func(m mutatingTxn) error {

		changed = false
		updated = Event{}
		idxItem, err := m.get([]byte(msgIDPrefix + targetMsgID))
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
		rowItem, err := m.get(key)
		if errors.Is(err, badger.ErrKeyNotFound) {
			_ = m.deleteAux([]byte(msgIDPrefix + targetMsgID))
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
		if l.retryTestHook != nil {
			l.retryTestHook()
		}
		if updated.Direction != DirIn {
			return nil
		}
		if updated.Kind != KindText && updated.Kind != KindFile {
			return nil
		}
		if updated.ReadAt != 0 && readAt >= updated.ReadAt {

			return nil
		}
		updated.ReadAt = readAt
		changed = true
		raw, err := json.Marshal(updated)
		if err != nil {
			return err
		}
		return m.storeRow(MutatorApplyInboundReadAt, updated.ChatID, updated.MsgID, key, raw)
	})
	if err != nil {
		return Event{}, false, err
	}

	slog.Debug("events: ApplyInboundReadAt decision",
		slog.String("msg_id", updated.MsgID),
		slog.String("chat_id", string(updated.ChatID)),
		slog.Bool("changed", changed),
	)
	if changed && l.bus != nil {
		l.bus.publish(updated)
	}
	return updated, changed, nil
}

func (l *Log) ApplyDeliverySnapshot(targetMsgID, rank string, failed bool) (Event, bool, error) {
	if targetMsgID == "" {
		return Event{}, false, ErrEventNotFound
	}
	slog.Debug("events: ApplyDeliverySnapshot observed",
		slog.String("msg_id", targetMsgID),
		slog.String("rank", rank),
		slog.Bool("failed", failed),
	)
	if rank == "" && !failed {

		return Event{}, false, nil
	}
	var updated Event
	changed := false
	err := l.updateRetry(func(m mutatingTxn) error {

		changed = false
		updated = Event{}
		idxItem, err := m.get([]byte(msgIDPrefix + targetMsgID))
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
		rowItem, err := m.get(key)
		if errors.Is(err, badger.ErrKeyNotFound) {
			_ = m.deleteAux([]byte(msgIDPrefix + targetMsgID))
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
		if l.retryTestHook != nil {
			l.retryTestHook()
		}
		if updated.Direction != DirOut {
			return nil
		}
		if updated.Kind != KindText && updated.Kind != KindFile {
			return nil
		}
		if rank != "" && DeliveryOutranks(updated.DeliveryState, rank) {
			updated.DeliveryState = rank
			changed = true
		}
		if failed && updated.mayFail() {
			updated.DeliveryState = DeliveryFailed
			changed = true
		}
		if !changed {
			return nil
		}
		raw, err := json.Marshal(updated)
		if err != nil {
			return err
		}
		return m.storeRow(MutatorApplyDeliverySnapshot, updated.ChatID, updated.MsgID, key, raw)
	})
	if err != nil {
		return Event{}, false, err
	}

	slog.Debug("events: ApplyDeliverySnapshot decision",
		slog.String("msg_id", updated.MsgID),
		slog.String("chat_id", string(updated.ChatID)),
		slog.Bool("changed", changed),
	)
	if changed && l.bus != nil {
		l.bus.publish(updated)
	}
	return updated, changed, nil
}

func (l *Log) ApplyReceiptSentClaim(targetMsgID string, sentAt int64, generation uint64) (Event, bool, error) {
	if targetMsgID == "" {
		return Event{}, false, ErrEventNotFound
	}
	if sentAt <= 0 {
		return Event{}, false, nil
	}
	slog.Debug("events: ApplyReceiptSentClaim observed",
		slog.String("msg_id", targetMsgID),
		slog.Int64("sent_at", sentAt),
		slog.Uint64("generation", generation),
	)
	var updated Event
	changed := false
	err := l.updateRetry(func(m mutatingTxn) error {

		changed = false
		updated = Event{}
		idxItem, err := m.get([]byte(msgIDPrefix + targetMsgID))
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
		rowItem, err := m.get(key)
		if errors.Is(err, badger.ErrKeyNotFound) {
			_ = m.deleteAux([]byte(msgIDPrefix + targetMsgID))
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
		if l.retryTestHook != nil {
			l.retryTestHook()
		}
		if updated.Direction != DirIn {
			return nil
		}
		if updated.Kind != KindText && updated.Kind != KindFile {
			return nil
		}
		if updated.ReadReceiptSentAt != 0 {

			return nil
		}
		if generation < updated.ReceiptClaimGeneration {

			return nil
		}
		updated.ReadReceiptSentAt = sentAt
		updated.ReceiptClaimOrigin = false
		updated.ReceiptClaimGeneration = generation
		changed = true
		raw, err := json.Marshal(updated)
		if err != nil {
			return err
		}
		return m.storeRow(MutatorApplyReceiptSentClaim, updated.ChatID, updated.MsgID, key, raw)
	})
	if err != nil {
		return Event{}, false, err
	}

	slog.Debug("events: ApplyReceiptSentClaim decision",
		slog.String("msg_id", updated.MsgID),
		slog.String("chat_id", string(updated.ChatID)),
		slog.Bool("changed", changed),
	)
	if changed && l.bus != nil {
		l.bus.publish(updated)
	}
	return updated, changed, nil
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

	if ev.Kind == KindCallSummary && ev.Direction == DirOut {
		return ev.DisplayTs+int64(ev.ExpireSeconds) <= nowSec
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
