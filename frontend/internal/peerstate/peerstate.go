package peerstate

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/dgraph-io/badger/v4"

	"haoma-frontend/internal/logid"
	"haoma-frontend/internal/store"
)

const (
	prefix        = "peer:"
	suffixSendSeq = ":send_seq"
	suffixMeta    = ":meta"

	suffixCountAt    = ":count_at"
	suffixUnlinkedAt = ":unlinked_at"
)

type Counters struct {
	st *store.Store
}

func New(st *store.Store) *Counters {
	return &Counters{st: st}
}

func (c *Counters) NextSendSeq(peerID string) (uint64, error) {
	if peerID == "" {
		return 0, errors.New("peerstate: empty peer id")
	}
	key := sendSeqKey(peerID)
	var next uint64
	for attempt := 0; attempt < maxConflictRetries; attempt++ {
		err := c.st.Update(func(txn *badger.Txn) error {
			current, err := loadUint64(txn, key)
			if err != nil {
				return err
			}
			next = current + 1
			var buf [8]byte
			binary.BigEndian.PutUint64(buf[:], next)
			return txn.Set(key, buf[:])
		})
		if err == nil {
			return next, nil
		}
		if !errors.Is(err, badger.ErrConflict) {
			return 0, fmt.Errorf("peerstate: next send seq for %s: %w", logid.Short(peerID), err)
		}

	}
	return 0, fmt.Errorf("peerstate: next send seq for %s: exceeded %d conflict retries", logid.Short(peerID), maxConflictRetries)
}

const maxConflictRetries = 256

func (c *Counters) PeekSendSeq(peerID string) (uint64, error) {
	if peerID == "" {
		return 0, errors.New("peerstate: empty peer id")
	}
	var v uint64
	err := c.st.View(func(txn *badger.Txn) error {
		var err error
		v, err = loadUint64(txn, sendSeqKey(peerID))
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("peerstate: peek send seq for %s: %w", logid.Short(peerID), err)
	}
	return v, nil
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
			return fmt.Errorf("peerstate: corrupt counter, length %d != 8", len(raw))
		}
		v = binary.BigEndian.Uint64(raw)
		return nil
	})
	return v, err
}

func sendSeqKey(peerID string) []byte {
	out := make([]byte, 0, len(prefix)+len(peerID)+len(suffixSendSeq))
	out = append(out, prefix...)
	out = append(out, peerID...)
	out = append(out, suffixSendSeq...)
	return out
}

type MetaRecord struct {
	Nick  string `json:"nick,omitempty"`
	Alias string `json:"alias,omitempty"`

	NickAt int64 `json:"nick_at,omitempty"`

	Fingerprint string `json:"fingerprint,omitempty"`

	AliasAt int64 `json:"alias_at,omitempty"`

	Verified   bool  `json:"verified,omitempty"`
	VerifiedAt int64 `json:"verified_at,omitempty"`
	Blocked    bool  `json:"blocked,omitempty"`
	BlockedAt  int64 `json:"blocked_at,omitempty"`

	PairedAt int64 `json:"paired_at,omitempty"`

	SendsUnlinkNoticeAt int64 `json:"sends_unlink_notice_at,omitempty"`
}

type Meta struct {
	st *store.Store
}

func NewMeta(st *store.Store) *Meta { return &Meta{st: st} }

func (m *Meta) Get(peerID string) (MetaRecord, error) {
	rec, err := m.peerRecord(peerID)
	if err != nil {
		return MetaRecord{}, err
	}
	if rec.Fingerprint == "" {
		slog.Debug("peer-meta: read a peer with no person to fold in",
			slog.String("peer_id", peerID),
		)
		return rec, nil
	}
	person, err := m.GetPerson(rec.Fingerprint)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {

			slog.Warn("peer-meta: reading the person failed; the alias is missing from this row",
				slog.String("peer_id", peerID),
				slog.String("fingerprint", rec.Fingerprint),
				slog.Any("err", err),
			)
			return rec, nil
		}
		slog.Debug("peer-meta: read a peer whose person is not stored here",
			slog.String("peer_id", peerID),
			slog.String("fingerprint", rec.Fingerprint),
		)
		return rec, nil
	}
	before := rec
	foldMeta(&rec, person)
	slog.Debug("peer-meta: read a peer and folded in the person",
		slog.String("peer_id", peerID),
		slog.String("fingerprint", rec.Fingerprint),
		slog.Bool("person_won", rec != before),
	)
	return rec, nil
}

func (m *Meta) peerRecord(peerID string) (MetaRecord, error) {
	if peerID == "" {
		return MetaRecord{}, errors.New("peerstate: empty peer id")
	}
	var rec MetaRecord
	err := m.st.View(func(txn *badger.Txn) error {
		var err error
		rec, err = loadMeta(txn, metaKey(peerID))
		return err
	})
	if err != nil {
		return MetaRecord{}, fmt.Errorf("peerstate: get meta for %s: %w", logid.Short(peerID), err)
	}
	return rec, nil
}

func (m *Meta) PairedAt(peerID string) (int64, error) {
	rec, err := m.peerRecord(peerID)
	if err != nil {
		return 0, err
	}
	return rec.PairedAt, nil
}

func (m *Meta) MarkSendsUnlinkNotice(peerID string, at int64) (changed bool, err error) {
	if peerID == "" {
		return false, errors.New("peerstate: empty peer id")
	}
	if at <= 0 {
		return false, errors.New("peerstate: no time for the unlink-notice marker")
	}
	return m.mutate(peerID, func(rec *MetaRecord) bool {
		if rec.SendsUnlinkNoticeAt != 0 {
			return false
		}
		rec.SendsUnlinkNoticeAt = at
		return true
	})
}

func (m *Meta) SendsUnlinkNotice(peerID string) (bool, error) {
	rec, err := m.peerRecord(peerID)
	if err != nil {
		return false, err
	}
	return rec.SendsUnlinkNoticeAt != 0, nil
}

func (m *Meta) SetAlias(peerID, alias string, ts int64) (changed bool, err error) {
	if peerID == "" {
		return false, errors.New("peerstate: empty peer id")
	}
	peer, err := m.peerRecord(peerID)
	if err != nil {
		return false, err
	}
	slog.Debug("peer-meta: alias write asked for",
		slog.String("peer_id", peerID),
		slog.String("fingerprint", peer.Fingerprint),
		slog.Bool("clears", alias == ""),
	)
	key, subject, home := personKey(peer.Fingerprint), peer.Fingerprint, "person"
	if peer.Fingerprint == "" {
		key, subject, home = metaKey(peerID), peerID, "peer"
	}
	changed, err = m.mutateKey(key, subject, func(rec *MetaRecord) bool {
		if rec.Fingerprint == "" {
			rec.Fingerprint = peer.Fingerprint
		}
		if rec.Alias == alias {

			if ts > rec.AliasAt {
				rec.AliasAt = ts
			}
			return false
		}
		rec.Alias, rec.AliasAt = alias, bumpPast(ts, rec.AliasAt)
		return true
	})
	if err != nil {
		return false, err
	}
	slog.Debug("peer-meta: alias written",
		slog.String("peer_id", peerID),
		slog.String("fingerprint", peer.Fingerprint),
		slog.String("stored_under", home),
		slog.Bool("changed", changed),
	)
	return changed, nil
}

func (m *Meta) SetVerified(peerID string, verified bool, ts int64) (changed bool, err error) {
	if peerID == "" {
		return false, errors.New("peerstate: empty peer id")
	}
	return m.mutate(peerID, func(rec *MetaRecord) bool {
		if ts != 0 && ts < rec.VerifiedAt {
			return false
		}
		if ts != 0 {
			rec.VerifiedAt = ts
		}
		if rec.Verified == verified {
			return false
		}
		rec.Verified = verified
		return true
	})
}

func (m *Meta) SetBlocked(peerID string, blocked bool, ts int64) (changed bool, err error) {
	if peerID == "" {
		return false, errors.New("peerstate: empty peer id")
	}
	return m.mutate(peerID, func(rec *MetaRecord) bool {
		if ts != 0 && ts < rec.BlockedAt {
			return false
		}
		if ts != 0 {
			rec.BlockedAt = ts
		}
		if rec.Blocked == blocked {
			return false
		}
		rec.Blocked = blocked
		return true
	})
}

func (m *Meta) SetNick(peerID, nick string, ts int64) (changed bool, err error) {
	if peerID == "" {
		return false, errors.New("peerstate: empty peer id")
	}
	return m.mutate(peerID, func(rec *MetaRecord) bool {
		if ts != 0 && ts <= rec.NickAt {
			return false
		}
		if rec.Nick == nick {

			if ts != 0 && ts > rec.NickAt {
				rec.NickAt = ts
			}
			return false
		}
		rec.Nick = nick
		if ts != 0 {
			rec.NickAt = ts
		}
		return true
	})
}

func (m *Meta) SetFingerprint(peerID, fingerprint string) (changed bool, prev string, err error) {
	return m.SetFingerprintAt(peerID, fingerprint, time.Now().Unix())
}

func (m *Meta) SetFingerprintAt(peerID, fingerprint string, pairedAt int64) (changed bool, prev string, err error) {
	if peerID == "" {
		return false, "", errors.New("peerstate: empty peer id")
	}
	if fingerprint == "" {
		return false, "", nil
	}
	var strandedAlias string
	var strandedAt int64
	changed, err = m.mutate(peerID, func(rec *MetaRecord) bool {
		prev = rec.Fingerprint
		strandedAlias, strandedAt = rec.Alias, rec.AliasAt
		if rec.Fingerprint == fingerprint {
			return false
		}
		rec.Fingerprint = fingerprint
		if rec.PairedAt == 0 {
			rec.PairedAt = pairedAt
		}
		return true
	})
	if err != nil || !changed || strandedAlias == "" {
		return changed, prev, err
	}
	if mErr := m.adoptStrandedAlias(peerID, fingerprint, strandedAlias, strandedAt); mErr != nil {
		return changed, prev, mErr
	}
	return changed, prev, nil
}

func (m *Meta) adoptStrandedAlias(peerID, fingerprint, alias string, at int64) error {
	if _, err := m.MergePerson(fingerprint, MetaRecord{
		Fingerprint: fingerprint,
		Alias:       alias,
		AliasAt:     at,
	}); err != nil {
		return fmt.Errorf("peerstate: adopt stranded alias for %s: %w", logid.Short(peerID), err)
	}
	if _, err := m.mutate(peerID, func(rec *MetaRecord) bool {
		if rec.Alias == "" {
			return false
		}
		rec.Alias, rec.AliasAt = "", 0
		return true
	}); err != nil {
		return fmt.Errorf("peerstate: clear stranded alias for %s: %w", logid.Short(peerID), err)
	}
	slog.Debug("peer-meta: an alias written before the fingerprint was known now lives on the person",
		slog.String("peer_id", peerID),
		slog.String("fingerprint", fingerprint),
	)
	return nil
}

func (m *Meta) mutate(peerID string, fn func(*MetaRecord) bool) (bool, error) {
	return m.mutateKey(metaKey(peerID), peerID, fn)
}

func (m *Meta) mutateKey(key []byte, subject string, fn func(*MetaRecord) bool) (bool, error) {
	var changed bool
	for attempt := 0; attempt < maxConflictRetries; attempt++ {
		err := m.st.Update(func(txn *badger.Txn) error {
			rec, err := loadMeta(txn, key)
			if err != nil {
				return err
			}
			changed = fn(&rec)
			raw, err := json.Marshal(rec)
			if err != nil {
				return err
			}
			return txn.Set(key, raw)
		})
		if err == nil {
			return changed, nil
		}
		if !errors.Is(err, badger.ErrConflict) {
			return false, fmt.Errorf("peerstate: mutate meta for %s: %w", logid.Short(subject), err)
		}
	}
	return false, fmt.Errorf("peerstate: mutate meta for %s: exceeded %d conflict retries", logid.Short(subject), maxConflictRetries)
}

func loadMeta(txn *badger.Txn, key []byte) (MetaRecord, error) {
	item, err := txn.Get(key)
	if errors.Is(err, badger.ErrKeyNotFound) {
		return MetaRecord{}, nil
	}
	if err != nil {
		return MetaRecord{}, err
	}
	var rec MetaRecord
	err = item.Value(func(raw []byte) error {
		if len(raw) == 0 {
			return nil
		}
		return json.Unmarshal(raw, &rec)
	})
	return rec, err
}

func metaKey(peerID string) []byte {
	out := make([]byte, 0, len(prefix)+len(peerID)+len(suffixMeta))
	out = append(out, prefix...)
	out = append(out, peerID...)
	out = append(out, suffixMeta...)
	return out
}

func Resolve(rec MetaRecord, peerID string) string {
	if rec.Alias != "" {
		return rec.Alias
	}
	if rec.Nick != "" {
		return rec.Nick
	}
	return ShortID(peerID)
}

func ShortID(peerID string) string {
	const n = 8
	if len(peerID) <= n {
		return peerID
	}
	return peerID[:n]
}
