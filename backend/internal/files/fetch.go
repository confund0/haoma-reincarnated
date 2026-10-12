package files

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/dgraph-io/badger/v4"
)

const StagingSubdir = "staging"

const StagingPartSuffix = ".part"

const prefixFetch = "fetch:"

type FetchState string

const (
	FetchStatePending         FetchState = "pending"
	FetchStateDownloading     FetchState = "downloading"
	FetchStateReady           FetchState = "ready"
	FetchStateFailedTransient FetchState = "failed_transient"
	FetchStateFailedPermanent FetchState = "failed_permanent"
)

type FetchFailReason string

const (
	FetchFailBlobGone FetchFailReason = "blob_gone"

	FetchFailIntegrity FetchFailReason = "integrity"

	FetchFailLimit FetchFailReason = "limit"
)

type Fetch struct {
	Token          string     `json:"token"`
	MsgID          string     `json:"msg_id"`
	PeerID         string     `json:"peer_id"`
	UrlPath        string     `json:"url_path"`
	ExpectedSize   int64      `json:"expected_size"`
	ExpectedSha256 string     `json:"expected_sha256"`
	BytesReceived  int64      `json:"bytes_received,omitempty"`
	State          FetchState `json:"state"`
	LastError      string     `json:"last_error,omitempty"`
	CreatedAt      int64      `json:"created_at"`
	UpdatedAt      int64      `json:"updated_at,omitempty"`

	RetryAttempts uint16 `json:"retry_attempts,omitempty"`

	Kicked bool `json:"kicked,omitempty"`

	FailReason FetchFailReason `json:"fail_reason,omitempty"`
}

var ErrFetchNotFound = errors.New("files: fetch not found")

var ErrStagingIDInvalid = errors.New("files: msg id cannot name a staging blob")

func validStagingID(msgID string) bool {
	return msgID != "" && !strings.HasSuffix(msgID, StagingPartSuffix)
}

var ErrFetchTokenInUse = errors.New("files: fetch token already enqueued")

var ErrFetchReady = errors.New("files: fetch already ready")

var ErrFetchInFlight = errors.New("files: fetch in progress")

func (m *Manager) PutFetch(f Fetch) error {
	if f.Token == "" {
		return errors.New("files: empty fetch token")
	}
	if f.MsgID == "" {
		return errors.New("files: empty fetch msg id")
	}
	if !validStagingID(f.MsgID) {
		return ErrStagingIDInvalid
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("files: marshal fetch: %w", err)
	}
	key := fetchKey(f.Token)
	unlock := m.fetchLocks.lock(f.MsgID)
	defer unlock()
	return m.st.Update(func(txn *badger.Txn) error {
		if item, gerr := txn.Get(key); gerr == nil {
			var existing Fetch
			if verr := item.Value(func(v []byte) error {
				return json.Unmarshal(v, &existing)
			}); verr == nil {
				switch existing.State {
				case FetchStatePending, FetchStateDownloading:
					return ErrFetchTokenInUse
				case FetchStateReady:
					if _, serr := os.Stat(m.StagingPath(existing.MsgID)); serr == nil {
						return ErrFetchReady
					}
				}
			}
		}
		return txn.Set(key, raw)
	})
}

func (m *Manager) GetFetch(token string) (Fetch, error) {
	if token == "" {
		return Fetch{}, ErrFetchNotFound
	}
	var raw []byte
	err := m.st.View(func(txn *badger.Txn) error {
		item, err := txn.Get(fetchKey(token))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrFetchNotFound
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
		return Fetch{}, err
	}
	var f Fetch
	if err := json.Unmarshal(raw, &f); err != nil {
		return Fetch{}, fmt.Errorf("files: decode fetch: %w", err)
	}
	return f, nil
}

func (m *Manager) UpdateFetch(f Fetch) error {
	if f.Token == "" {
		return errors.New("files: empty fetch token")
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("files: marshal fetch: %w", err)
	}
	return m.st.Update(func(txn *badger.Txn) error {
		return txn.Set(fetchKey(f.Token), raw)
	})
}

func (m *Manager) DeleteFetch(token string) error {
	if token == "" {
		return nil
	}
	return m.st.Update(func(txn *badger.Txn) error {
		return txn.Delete(fetchKey(token))
	})
}

func (m *Manager) ListPendingFetches() ([]Fetch, error) {
	var out []Fetch
	err := m.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefixFetch)
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			var raw []byte
			if err := it.Item().Value(func(v []byte) error {
				raw = append([]byte(nil), v...)
				return nil
			}); err != nil {
				return err
			}
			var f Fetch
			if err := json.Unmarshal(raw, &f); err != nil {
				return fmt.Errorf("files: decode fetch row: %w", err)
			}
			switch f.State {
			case FetchStatePending, FetchStateDownloading:
				out = append(out, f)
			}
		}
		return nil
	})
	return out, err
}

func (m *Manager) ListFailedTransientFetches() ([]Fetch, error) {
	var out []Fetch
	err := m.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefixFetch)
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			var raw []byte
			if err := it.Item().Value(func(v []byte) error {
				raw = append([]byte(nil), v...)
				return nil
			}); err != nil {
				return err
			}
			var f Fetch
			if err := json.Unmarshal(raw, &f); err != nil {
				return fmt.Errorf("files: decode fetch row: %w", err)
			}
			if f.State == FetchStateFailedTransient {
				out = append(out, f)
			}
		}
		return nil
	})
	return out, err
}

func (m *Manager) StagingDir() string {
	return filepath.Join(m.rootDir, StagingSubdir)
}

func (m *Manager) StagingPath(msgID string) string {
	return filepath.Join(m.StagingDir(), msgID)
}

func (m *Manager) StagingPartPath(msgID string) string {
	return filepath.Join(m.StagingDir(), msgID+StagingPartSuffix)
}

func (m *Manager) EnsureStagingDir() error {
	dir := m.StagingDir()
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("files: mkdir staging %s: %w", dir, err)
	}
	if err := os.Chmod(dir, dirMode); err != nil {
		return fmt.Errorf("files: chmod staging %s: %w", dir, err)
	}
	return nil
}

func (m *Manager) OpenStaging(msgID string) (*os.File, int64, error) {
	if !validStagingID(msgID) {
		return nil, 0, ErrBlobNotFound
	}
	f, err := os.Open(m.StagingPath(msgID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, ErrBlobNotFound
		}
		return nil, 0, fmt.Errorf("files: open staging %s: %w", msgID, err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, fmt.Errorf("files: stat staging %s: %w", msgID, err)
	}
	return f, st.Size(), nil
}

func (m *Manager) DeleteStaging(msgID string) error {
	if !validStagingID(msgID) {
		return ErrStagingIDInvalid
	}
	for _, path := range []string{m.StagingPath(msgID), m.StagingPartPath(msgID)} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("files: remove staging %s: %w", filepath.Base(path), err)
		}
	}
	return nil
}

func (m *Manager) StagingPartSize(msgID string) (int64, error) {
	if !validStagingID(msgID) {
		return 0, ErrStagingIDInvalid
	}
	st, err := os.Stat(m.StagingPartPath(msgID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("files: stat staging part %s: %w", msgID, err)
	}
	return st.Size(), nil
}

func (m *Manager) DropStaging(msgID, token string) error {
	if !validStagingID(msgID) {
		return ErrStagingIDInvalid
	}
	unlock := m.fetchLocks.lock(msgID)
	defer unlock()
	inFlight, err := m.fetchInFlightForMsg(msgID)
	if err != nil {
		return err
	}
	if inFlight {
		return ErrFetchInFlight
	}
	if err := m.DeleteStaging(msgID); err != nil {
		return err
	}
	if err := m.DeleteFetch(token); err != nil {
		return fmt.Errorf("files: drop fetch row: %w", err)
	}
	return nil
}

func (m *Manager) fetchInFlightForMsg(msgID string) (bool, error) {
	rows, err := m.allFetches()
	if err != nil {
		return false, err
	}
	for _, f := range rows {
		if f.MsgID != msgID {
			continue
		}
		switch f.State {
		case FetchStatePending, FetchStateDownloading:
			return true, nil
		}
	}
	return false, nil
}

type StagingScan struct {
	MovedToPart int

	RestoredFinal int

	Removed int
}

func (m *Manager) ReconcileStaging() (StagingScan, error) {
	var scan StagingScan
	entries, err := os.ReadDir(m.StagingDir())
	if errors.Is(err, os.ErrNotExist) {
		return scan, nil
	}
	if err != nil {
		return scan, fmt.Errorf("files: read staging dir: %w", err)
	}
	rows, err := m.allFetches()
	if err != nil {
		return scan, err
	}
	type msgState struct {
		ready, resumable, any bool
		readyRow              Fetch
	}
	states := map[string]msgState{}
	for _, f := range rows {
		st := states[f.MsgID]
		st.any = true
		switch f.State {
		case FetchStateReady:
			st.ready = true
			st.readyRow = f
		case FetchStatePending, FetchStateDownloading, FetchStateFailedTransient:
			st.resumable = true
		}
		states[f.MsgID] = st
	}
	names := map[string]bool{}
	for _, e := range entries {
		if e.Type().IsRegular() {
			names[e.Name()] = true
		}
	}

	var finals []string
	for name := range names {
		if !strings.HasSuffix(name, StagingPartSuffix) {
			finals = append(finals, name)
		}
	}
	for _, id := range finals {
		st := states[id]
		if !st.any || st.ready {
			continue
		}
		if !st.resumable {
			if err := os.Remove(m.StagingPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return scan, fmt.Errorf("files: remove staging %s: %w", id, err)
			}
			scan.Removed++
			continue
		}
		if names[id+StagingPartSuffix] {
			continue
		}
		if err := os.Rename(m.StagingPath(id), m.StagingPartPath(id)); err != nil {
			return scan, fmt.Errorf("files: move staging %s to part: %w", id, err)
		}
		delete(names, id)
		names[id+StagingPartSuffix] = true
		scan.MovedToPart++
	}

	for name := range names {
		id, isPart := strings.CutSuffix(name, StagingPartSuffix)
		if !isPart || states[id].resumable {
			continue
		}
		if st := states[id]; st.ready && !names[id] && partIsWhole(m.StagingPartPath(id), st.readyRow) {
			if err := os.Rename(m.StagingPartPath(id), m.StagingPath(id)); err != nil {
				return scan, fmt.Errorf("files: move staging part %s to final: %w", id, err)
			}
			scan.RestoredFinal++
			continue
		}
		if err := os.Remove(filepath.Join(m.StagingDir(), name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return scan, fmt.Errorf("files: remove staging part %s: %w", id, err)
		}
		scan.Removed++
	}
	if scan.MovedToPart+scan.RestoredFinal+scan.Removed > 0 {
		if err := syncDir(m.StagingDir()); err != nil {
			return scan, fmt.Errorf("files: fsync staging dir: %w", err)
		}
	}
	return scan, nil
}

func partIsWhole(path string, row Fetch) bool {
	if row.ExpectedSize <= 0 || row.ExpectedSha256 == "" {
		return false
	}
	sum, size, err := fileSha256(path)
	return err == nil && size == row.ExpectedSize && sum == row.ExpectedSha256
}

func fileSha256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func (m *Manager) allFetches() ([]Fetch, error) {
	var out []Fetch
	err := m.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefixFetch)
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			var f Fetch
			if err := it.Item().Value(func(v []byte) error {
				return json.Unmarshal(v, &f)
			}); err != nil {
				return fmt.Errorf("files: decode fetch row: %w", err)
			}
			out = append(out, f)
		}
		return nil
	})
	return out, err
}

type keyedLocks struct {
	mu sync.Mutex
	m  map[string]*keyedLock
}

type keyedLock struct {
	mu   sync.Mutex
	refs int
}

func (k *keyedLocks) lock(key string) func() {
	k.mu.Lock()
	if k.m == nil {
		k.m = map[string]*keyedLock{}
	}
	l := k.m[key]
	if l == nil {
		l = &keyedLock{}
		k.m[key] = l
	}
	l.refs++
	k.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		k.mu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(k.m, key)
		}
		k.mu.Unlock()
	}
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func fetchKey(token string) []byte {
	out := make([]byte, 0, len(prefixFetch)+len(token))
	out = append(out, prefixFetch...)
	out = append(out, token...)
	return out
}
