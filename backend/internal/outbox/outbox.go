package outbox

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/dgraph-io/badger/v4"

	"haoma/internal/store"
	"haoma/internal/xport"
)

const (
	StateEnqueued  = "enqueued"
	StateSent      = "sent"
	StateDelivered = "delivered"
	StateRead      = "read"
	StateFailed    = "failed"
)

const (
	rowPrefix   = "outbox:"
	duePrefix   = "outbox-due:"
	statePrefix = "outbox-state:"
)

type OutboxRow struct {
	EnvelopeID     string         `json:"envelope_id"`
	Dest           string         `json:"dest"`
	Envelope       xport.Envelope `json:"envelope"`
	State          string         `json:"state"`
	Attempts       int            `json:"attempts"`
	AckFailures    int            `json:"ack_failures,omitempty"`
	FirstAt        int64          `json:"first_at"`
	NextAttemptAt  int64          `json:"next_attempt_at"`
	StateChangedAt int64          `json:"state_changed_at"`
	LastError      string         `json:"last_error,omitempty"`
}

type DeliveryStatus struct {
	EnvelopeID string `json:"envelope_id"`
	State      string `json:"state"`
	At         int64  `json:"at"`
	Attempts   int    `json:"attempts"`
	LastError  string `json:"last_error,omitempty"`
}

var ErrDuplicate = errors.New("outbox: envelope already exists")

type Sender interface {
	Send(ctx context.Context, dest string, env xport.Envelope) ([]byte, error)
}

type AckVerifier interface {
	VerifyAck(ctx context.Context, ackBody []byte, dest string) error
}

type AckVerifierFunc func(ctx context.Context, ackBody []byte, dest string) error

func (f AckVerifierFunc) VerifyAck(ctx context.Context, ackBody []byte, dest string) error {
	return f(ctx, ackBody, dest)
}

type Bus struct {
	mu   sync.Mutex
	subs []chan DeliveryStatus
}

func (b *Bus) Subscribe(bufSize int) (<-chan DeliveryStatus, func()) {
	ch := make(chan DeliveryStatus, bufSize)
	b.mu.Lock()
	b.subs = append(b.subs, ch)
	b.mu.Unlock()
	cancel := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		for i, s := range b.subs {
			if s == ch {
				b.subs = append(b.subs[:i], b.subs[i+1:]...)
				close(ch)
				return
			}
		}
	}
	return ch, cancel
}

func (b *Bus) publish(ds DeliveryStatus) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- ds:
		default:
		}
	}
}

type Store struct {
	st *store.Store
}

func NewStore(st *store.Store) *Store { return &Store{st: st} }

func (s *Store) Enqueue(dest string, env xport.Envelope, now time.Time) (*OutboxRow, error) {
	if env.ID == "" {
		return nil, errors.New("outbox: enqueue: envelope missing ID")
	}
	row := &OutboxRow{
		EnvelopeID:     env.ID,
		Dest:           dest,
		Envelope:       env,
		State:          StateEnqueued,
		FirstAt:        now.UnixNano(),
		NextAttemptAt:  now.UnixNano(),
		StateChangedAt: now.UnixNano(),
	}
	raw, err := json.Marshal(row)
	if err != nil {
		return nil, err
	}
	mainKey := makeRowKey(env.ID)
	dueKey := makeDueKey(now, env.ID)
	stKey := makeStateKey(StateEnqueued, env.ID)

	err = s.st.Update(func(txn *badger.Txn) error {
		if _, err := txn.Get(mainKey); err == nil {
			return ErrDuplicate
		} else if !errors.Is(err, badger.ErrKeyNotFound) {
			return err
		}
		if err := txn.Set(mainKey, raw); err != nil {
			return err
		}
		if err := txn.Set(dueKey, []byte(env.ID)); err != nil {
			return err
		}
		return txn.Set(stKey, nil)
	})
	if err != nil {
		return nil, err
	}
	return row, nil
}

func (s *Store) Load(envID string) (*OutboxRow, error) {
	var row OutboxRow
	err := s.st.View(func(txn *badger.Txn) error {
		return loadInTxn(txn, envID, &row)
	})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (s *Store) Advance(row *OutboxRow, newState string, nextAt time.Time, attempts, ackFailures int, lastErr string, now time.Time) error {
	terminal := newState == StateSent || newState == StateFailed

	oldDueKey := makeDueKey(time.Unix(0, row.NextAttemptAt), row.EnvelopeID)
	oldStKey := makeStateKey(row.State, row.EnvelopeID)
	mainKey := makeRowKey(row.EnvelopeID)
	newStKey := makeStateKey(newState, row.EnvelopeID)

	updated := *row
	updated.State = newState
	updated.Attempts = attempts
	updated.AckFailures = ackFailures
	updated.LastError = lastErr
	updated.StateChangedAt = now.UnixNano()
	if terminal {
		updated.NextAttemptAt = 0
	} else {
		updated.NextAttemptAt = nextAt.UnixNano()
	}

	raw, err := json.Marshal(updated)
	if err != nil {
		return err
	}

	return s.st.Update(func(txn *badger.Txn) error {
		if row.NextAttemptAt != 0 {

			if err := txn.Delete(oldDueKey); err != nil && !errors.Is(err, badger.ErrKeyNotFound) {
				return err
			}
		}
		if err := txn.Delete(oldStKey); err != nil && !errors.Is(err, badger.ErrKeyNotFound) {
			return err
		}
		if err := txn.Set(mainKey, raw); err != nil {
			return err
		}
		if err := txn.Set(newStKey, nil); err != nil {
			return err
		}
		if !terminal {
			return txn.Set(makeDueKey(nextAt, row.EnvelopeID), []byte(row.EnvelopeID))
		}
		return nil
	})
}

func (s *Store) ListDue(now time.Time) ([]*OutboxRow, error) {
	nowNanos := now.UnixNano()
	var ids []string
	err := s.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(duePrefix)
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			key := it.Item().Key()
			rest := key[len(duePrefix):]
			if len(rest) < 8 {
				return fmt.Errorf("outbox: malformed due key: %q", key)
			}
			nanos := int64(binary.BigEndian.Uint64(rest[:8]))
			if nanos > nowNanos {
				break
			}
			if err := it.Item().Value(func(v []byte) error {
				ids = append(ids, string(v))
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	rows := make([]*OutboxRow, 0, len(ids))
	for _, id := range ids {
		row, err := s.Load(id)
		if err != nil {
			if errors.Is(err, badger.ErrKeyNotFound) {
				continue
			}
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (s *Store) ListByState(state string, since int64, limit int) ([]*OutboxRow, error) {
	if limit <= 0 {
		limit = 10_000
	}
	prefix := statePrefix + state + ":"
	var ids []string
	err := s.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefix)
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid() && len(ids) < limit; it.Next() {
			ids = append(ids, string(it.Item().Key()[len(prefix):]))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var rows []*OutboxRow
	for _, id := range ids {
		row, err := s.Load(id)
		if err != nil {
			if errors.Is(err, badger.ErrKeyNotFound) {
				continue
			}
			return nil, err
		}
		if row.StateChangedAt < since {
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (s *Store) KickByDests(dests []string, now time.Time) (int, error) {
	if len(dests) == 0 {
		return 0, nil
	}
	want := make(map[string]struct{}, len(dests))
	for _, d := range dests {
		want[d] = struct{}{}
	}
	nowNanos := now.UnixNano()
	prefix := statePrefix + StateEnqueued + ":"

	var toKick []*OutboxRow
	err := s.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefix)
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			envID := string(it.Item().Key()[len(prefix):])
			var row OutboxRow
			if err := loadInTxn(txn, envID, &row); err != nil {
				return err
			}
			if _, ok := want[row.Dest]; !ok {
				continue
			}
			if row.NextAttemptAt <= nowNanos {
				continue
			}
			toKick = append(toKick, &row)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if len(toKick) == 0 {
		return 0, nil
	}

	err = s.st.Update(func(txn *badger.Txn) error {
		for _, row := range toKick {
			oldDueKey := makeDueKey(time.Unix(0, row.NextAttemptAt), row.EnvelopeID)
			newDueKey := makeDueKey(now, row.EnvelopeID)
			updated := *row
			updated.NextAttemptAt = nowNanos
			raw, err := json.Marshal(updated)
			if err != nil {
				return err
			}
			if err := txn.Delete(oldDueKey); err != nil && !errors.Is(err, badger.ErrKeyNotFound) {
				return err
			}
			if err := txn.Set(makeRowKey(row.EnvelopeID), raw); err != nil {
				return err
			}
			if err := txn.Set(newDueKey, []byte(row.EnvelopeID)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(toKick), nil
}

const (
	gcSentAge   = 7 * 24 * time.Hour
	gcFailedAge = 30 * 24 * time.Hour
)

func (s *Store) GC(now time.Time) (int, error) {
	nowNanos := now.UnixNano()
	var victims []*OutboxRow
	err := s.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(rowPrefix)
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			var row OutboxRow
			if err := it.Item().Value(func(v []byte) error {
				return json.Unmarshal(v, &row)
			}); err != nil {
				return err
			}
			var maxAge time.Duration
			switch row.State {
			case StateSent:
				maxAge = gcSentAge
			case StateFailed:
				maxAge = gcFailedAge
			default:
				continue
			}
			if nowNanos-row.StateChangedAt >= int64(maxAge) {
				victims = append(victims, &row)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if len(victims) == 0 {
		return 0, nil
	}
	err = s.st.Update(func(txn *badger.Txn) error {
		for _, row := range victims {
			for _, k := range [][]byte{makeRowKey(row.EnvelopeID), makeStateKey(row.State, row.EnvelopeID)} {
				if err := txn.Delete(k); err != nil && !errors.Is(err, badger.ErrKeyNotFound) {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(victims), nil
}

type Worker struct {
	store  *Store
	sender Sender
	ackV   AckVerifier
	bus    *Bus

	Gate func() bool

	Tick              time.Duration
	GCInterval        time.Duration
	Backoff           func(attempts int) time.Duration
	MaxAttempts       int
	DeadLetterAge     time.Duration
	AckVerifyMaxTries int
	SendTimeout       time.Duration
	now               func() time.Time

	destsMu sync.Mutex
	dests   map[string]*destState
	running bool
	runCtx  context.Context
	destsWG sync.WaitGroup
}

type destState struct {
	kick chan struct{}
}

func NewWorker(st *Store, sender Sender, ackVerifier AckVerifier, bus *Bus) *Worker {
	return &Worker{
		store:             st,
		sender:            sender,
		ackV:              ackVerifier,
		bus:               bus,
		dests:             map[string]*destState{},
		Tick:              30 * time.Second,
		GCInterval:        1 * time.Hour,
		Backoff:           doublingBackoffWithJitter,
		MaxAttempts:       12,
		DeadLetterAge:     7 * 24 * time.Hour,
		AckVerifyMaxTries: 5,
		SendTimeout:       60 * time.Second,
		now:               time.Now,
	}
}

func (w *Worker) Enqueue(dest string, env xport.Envelope) error {
	if _, err := w.store.Enqueue(dest, env, w.now()); err != nil {
		return err
	}
	w.ensureDest(dest)
	w.signalDest(dest)
	return nil
}

func (w *Worker) Load(envID string) (*OutboxRow, error) { return w.store.Load(envID) }

func (w *Worker) ListByState(state string, sinceNanos int64, limit int) ([]*OutboxRow, error) {
	return w.store.ListByState(state, sinceNanos, limit)
}

func (w *Worker) Bus() *Bus { return w.bus }

func (w *Worker) KickByDests(dests []string) (int, error) {
	n, err := w.store.KickByDests(dests, w.now())
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	for _, d := range dests {
		w.ensureDest(d)
		w.signalDest(d)
	}
	return n, nil
}

func (w *Worker) Run(ctx context.Context) error {
	w.destsMu.Lock()
	w.runCtx = ctx
	w.running = true
	w.destsMu.Unlock()

	seedRows, err := w.store.ListByState(StateEnqueued, 0, 0)
	if err != nil {
		slog.Warn("outbox: seed list failed", slog.Any("err", err))
	}
	seen := map[string]bool{}
	for _, row := range seedRows {
		if seen[row.Dest] {
			continue
		}
		seen[row.Dest] = true
		w.ensureDest(row.Dest)
		w.signalDest(row.Dest)
	}

	gcTick := time.NewTicker(w.GCInterval)
	defer gcTick.Stop()

	for {
		select {
		case <-ctx.Done():

			w.destsMu.Lock()
			w.running = false
			w.destsMu.Unlock()
			w.destsWG.Wait()
			return ctx.Err()
		case <-gcTick.C:
			if _, err := w.store.GC(w.now()); err != nil {
				slog.Warn("outbox: GC failed", slog.Any("err", err))
			}
		}
	}
}

func (w *Worker) ensureDest(dest string) {
	w.destsMu.Lock()
	defer w.destsMu.Unlock()
	if !w.running {
		return
	}
	if _, ok := w.dests[dest]; ok {
		return
	}
	ds := &destState{kick: make(chan struct{}, 1)}
	w.dests[dest] = ds
	w.destsWG.Add(1)
	go w.runDest(dest, ds)
}

func (w *Worker) signalDest(dest string) {
	w.destsMu.Lock()
	ds := w.dests[dest]
	w.destsMu.Unlock()
	if ds == nil {
		return
	}
	select {
	case ds.kick <- struct{}{}:
	default:
	}
}

func (w *Worker) runDest(dest string, ds *destState) {
	defer w.destsWG.Done()
	tick := time.NewTicker(w.Tick)
	defer tick.Stop()

	w.drainDest(w.runCtx, dest)
	for {
		select {
		case <-w.runCtx.Done():
			return
		case <-ds.kick:
			w.drainDest(w.runCtx, dest)
		case <-tick.C:
			w.drainDest(w.runCtx, dest)
		}
	}
}

func (w *Worker) drainDest(ctx context.Context, dest string) {
	if w.Gate != nil && !w.Gate() {
		slog.Debug("outbox: drain skipped (gate closed)", slog.String("dest", dest))
		return
	}
	rows, err := w.store.ListDue(w.now())
	if err != nil {
		slog.Warn("outbox: list due failed", slog.String("dest", dest), slog.Any("err", err))
		return
	}
	for _, row := range rows {
		if row.Dest != dest {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		w.processRow(ctx, row)
	}
}

func (w *Worker) processRow(ctx context.Context, row *OutboxRow) {
	now := w.now()
	slog.Debug("outbox: attempting send",
		slog.String("envelope_id", row.EnvelopeID),
		slog.String("dest", row.Dest),
		slog.Int("attempt", row.Attempts+1),
	)
	sendCtx, cancel := context.WithTimeout(ctx, w.SendTimeout)
	body, sendErr := w.sender.Send(sendCtx, row.Dest, row.Envelope)
	cancel()

	if sendErr != nil {
		w.handleSendErr(row, sendErr, now)
		return
	}
	w.handleSendOK(ctx, row, body, now)
}

func (w *Worker) handleSendErr(row *OutboxRow, sendErr error, now time.Time) {
	newAttempts := row.Attempts + 1

	var peerErr *xport.PeerHTTPError
	if errors.As(sendErr, &peerErr) && peerErr.StatusCode == http.StatusUnauthorized {
		slog.Debug("outbox: 401 terminal",
			slog.String("envelope_id", row.EnvelopeID),
			slog.String("dest", row.Dest),
			slog.String("err", peerErr.Error()),
		)
		w.transition(row, StateFailed, time.Time{}, newAttempts, row.AckFailures, peerErr.Error(), now)
		return
	}

	if newAttempts >= w.MaxAttempts || now.UnixNano()-row.FirstAt >= int64(w.DeadLetterAge) {
		w.transition(row, StateFailed, time.Time{}, newAttempts, row.AckFailures, sendErr.Error(), now)
		return
	}

	nextAt := now.Add(w.Backoff(newAttempts))
	slog.Debug("outbox: rescheduling",
		slog.String("envelope_id", row.EnvelopeID),
		slog.Int("attempts", newAttempts),
		slog.Time("next_at", nextAt),
		slog.Any("err", sendErr),
	)
	if err := w.store.Advance(row, StateEnqueued, nextAt, newAttempts, row.AckFailures, sendErr.Error(), now); err != nil {
		slog.Warn("outbox: advance (reschedule) failed",
			slog.String("envelope_id", row.EnvelopeID),
			slog.Any("err", err),
		)
	}
}

func (w *Worker) handleSendOK(ctx context.Context, row *OutboxRow, body []byte, now time.Time) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {

		slog.Warn("outbox: peer returned empty ack body; treating as sent (back-compat)",
			slog.String("envelope_id", row.EnvelopeID),
			slog.String("dest", row.Dest),
		)
		w.transition(row, StateSent, time.Time{}, row.Attempts+1, 0, "", now)
		return
	}
	if w.ackV == nil {

		slog.Warn("outbox: no AckVerifier; treating 200+body as sent (back-compat)",
			slog.String("envelope_id", row.EnvelopeID),
		)
		w.transition(row, StateSent, time.Time{}, row.Attempts+1, 0, "", now)
		return
	}
	if err := w.ackV.VerifyAck(ctx, trimmed, row.Dest); err != nil {
		newAckFails := row.AckFailures + 1
		if newAckFails >= w.AckVerifyMaxTries {
			w.transition(row, StateFailed, time.Time{}, row.Attempts+1, newAckFails, "ack verify: "+err.Error(), now)
		} else {
			nextAt := now.Add(w.Backoff(row.Attempts + 1))
			if err2 := w.store.Advance(row, StateEnqueued, nextAt, row.Attempts+1, newAckFails, "ack verify: "+err.Error(), now); err2 != nil {
				slog.Warn("outbox: advance (bad ack reschedule) failed",
					slog.String("envelope_id", row.EnvelopeID),
					slog.Any("err", err2),
				)
			}
		}
		return
	}
	w.transition(row, StateSent, time.Time{}, row.Attempts+1, 0, "", now)
}

func (w *Worker) transition(row *OutboxRow, newState string, nextAt time.Time, attempts, ackFailures int, lastErr string, now time.Time) {
	slog.Debug("outbox: transition",
		slog.String("envelope_id", row.EnvelopeID),
		slog.String("from", row.State),
		slog.String("to", newState),
	)
	if err := w.store.Advance(row, newState, nextAt, attempts, ackFailures, lastErr, now); err != nil {
		slog.Warn("outbox: advance failed",
			slog.String("envelope_id", row.EnvelopeID),
			slog.String("new_state", newState),
			slog.Any("err", err),
		)
		return
	}
	w.bus.publish(DeliveryStatus{
		EnvelopeID: row.EnvelopeID,
		State:      newState,
		At:         now.UnixNano(),
		Attempts:   attempts,
		LastError:  lastErr,
	})
}

func makeRowKey(envID string) []byte {
	return []byte(rowPrefix + envID)
}

func makeDueKey(nextAt time.Time, envID string) []byte {
	buf := make([]byte, 0, len(duePrefix)+8+1+len(envID))
	buf = append(buf, duePrefix...)
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(nextAt.UnixNano()))
	buf = append(buf, ts[:]...)
	buf = append(buf, ':')
	buf = append(buf, envID...)
	return buf
}

func makeStateKey(state, envID string) []byte {
	return []byte(statePrefix + state + ":" + envID)
}

func loadInTxn(txn *badger.Txn, envID string, row *OutboxRow) error {
	item, err := txn.Get(makeRowKey(envID))
	if err != nil {
		return err
	}
	return item.Value(func(v []byte) error {
		return json.Unmarshal(v, row)
	})
}

func doublingBackoffWithJitter(attempts int) time.Duration {
	base := 30 * time.Second
	for i := 1; i < attempts; i++ {
		base *= 2
		if base >= time.Hour {
			base = time.Hour
			break
		}
	}
	return jitter(base, 0.2)
}

func jitter(d time.Duration, frac float64) time.Duration {
	if frac <= 0 {
		return d
	}
	var buf [8]byte
	if _, err := cryptorand.Read(buf[:]); err != nil {
		return d
	}
	u := binary.BigEndian.Uint64(buf[:])
	r := float64(u) / float64(^uint64(0))
	factor := 1.0 + frac*(2*r-1)
	return time.Duration(float64(d) * factor)
}
