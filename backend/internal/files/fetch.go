package files

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dgraph-io/badger/v4"
)

const StagingSubdir = "staging"

const prefixFetch = "fetch:"

type FetchState string

const (
	FetchStatePending         FetchState = "pending"
	FetchStateDownloading     FetchState = "downloading"
	FetchStateReady           FetchState = "ready"
	FetchStateFailedTransient FetchState = "failed_transient"
	FetchStateFailedPermanent FetchState = "failed_permanent"
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
}

var ErrFetchNotFound = errors.New("files: fetch not found")

var ErrFetchTokenInUse = errors.New("files: fetch token already enqueued")

func (m *Manager) PutFetch(f Fetch) error {
	if f.Token == "" {
		return errors.New("files: empty fetch token")
	}
	if f.MsgID == "" {
		return errors.New("files: empty fetch msg id")
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("files: marshal fetch: %w", err)
	}
	key := fetchKey(f.Token)
	return m.st.Update(func(txn *badger.Txn) error {
		if item, gerr := txn.Get(key); gerr == nil {
			var existing Fetch
			if verr := item.Value(func(v []byte) error {
				return json.Unmarshal(v, &existing)
			}); verr == nil {
				switch existing.State {
				case FetchStatePending, FetchStateDownloading:
					return ErrFetchTokenInUse
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
	if msgID == "" {
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
	if msgID == "" {
		return errors.New("files: empty msg id")
	}
	if err := os.Remove(m.StagingPath(msgID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("files: remove staging %s: %w", msgID, err)
	}
	return nil
}

func (m *Manager) StagingSize(msgID string) (int64, error) {
	if msgID == "" {
		return 0, errors.New("files: empty msg id")
	}
	st, err := os.Stat(m.StagingPath(msgID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("files: stat staging %s: %w", msgID, err)
	}
	return st.Size(), nil
}

func fetchKey(token string) []byte {
	out := make([]byte, 0, len(prefixFetch)+len(token))
	out = append(out, prefixFetch...)
	out = append(out, token...)
	return out
}
