package files

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/dgraph-io/badger/v4"

	"haoma/internal/store"
)

const SubdirName = "files"

const (
	prefixToken = "file-token:"
	prefixIndex = "file-blob:"
)

const (
	dirMode  os.FileMode = 0o700
	fileMode os.FileMode = 0o600
)

const MaxBlobBytes = 11 * 1024 * 1024

const TokenByteLen = 32

var (
	ErrTokenNotFound     = errors.New("files: token not found")
	ErrTokenInvalidated  = errors.New("files: token invalidated")
	ErrBlobNotFound      = errors.New("files: blob not found")
	ErrMsgIDInUse        = errors.New("files: msg id already staged")
	ErrCiphertextTooLong = errors.New("files: ciphertext exceeds MaxBlobBytes")
)

type TokenRow struct {
	Token             string `json:"token"`
	RecipientPeerID   string `json:"recipient_peer_id"`
	MsgID             string `json:"msg_id"`
	ExpiresAt         int64  `json:"expires_at,omitempty"`
	ReceiptsRemaining int    `json:"receipts_remaining"`
}

type Manager struct {
	st      *store.Store
	rootDir string

	mu sync.Mutex
}

func NewManager(st *store.Store, dataDir string) (*Manager, error) {
	if st == nil {
		return nil, errors.New("files: nil store")
	}
	if dataDir == "" {
		return nil, errors.New("files: empty data dir")
	}
	root := filepath.Join(dataDir, SubdirName)
	if err := os.MkdirAll(root, dirMode); err != nil {
		return nil, fmt.Errorf("files: mkdir %s: %w", root, err)
	}
	if err := os.Chmod(root, dirMode); err != nil {
		return nil, fmt.Errorf("files: chmod %s: %w", root, err)
	}
	return &Manager{st: st, rootDir: root}, nil
}

func (m *Manager) RootDir() string { return m.rootDir }

func (m *Manager) blobPath(msgID string) string {
	return filepath.Join(m.rootDir, msgID)
}

func (m *Manager) StageBlob(msgID string, ciphertext []byte, recipientPeerIDs []string, expiresAt int64) ([]string, error) {
	if msgID == "" {
		return nil, errors.New("files: empty msg id")
	}
	if len(ciphertext) == 0 {
		return nil, errors.New("files: empty ciphertext")
	}
	if len(ciphertext) > MaxBlobBytes {
		return nil, ErrCiphertextTooLong
	}
	if len(recipientPeerIDs) == 0 {
		return nil, errors.New("files: no recipient peer ids")
	}
	for i, p := range recipientPeerIDs {
		if p == "" {
			return nil, fmt.Errorf("files: empty recipient peer id at index %d", i)
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	already, err := m.hasTokensForMsg(msgID)
	if err != nil {
		return nil, err
	}
	if already {
		return nil, ErrMsgIDInUse
	}

	tokens := make([]string, len(recipientPeerIDs))
	for i := range recipientPeerIDs {
		tok, err := mintToken()
		if err != nil {
			return nil, fmt.Errorf("files: mint token: %w", err)
		}
		tokens[i] = tok
	}

	dst := m.blobPath(msgID)
	if err := writeAtomic(dst, ciphertext); err != nil {
		return nil, err
	}

	if err := m.st.Update(func(txn *badger.Txn) error {
		for i, tok := range tokens {
			row := TokenRow{
				Token:             tok,
				RecipientPeerID:   recipientPeerIDs[i],
				MsgID:             msgID,
				ExpiresAt:         expiresAt,
				ReceiptsRemaining: 1,
			}
			raw, err := json.Marshal(row)
			if err != nil {
				return fmt.Errorf("files: marshal token row: %w", err)
			}
			if err := txn.Set(tokenKey(tok), raw); err != nil {
				return err
			}
			if err := txn.Set(indexKey(msgID, tok), []byte{}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		_ = os.Remove(dst)
		return nil, fmt.Errorf("files: stage txn: %w", err)
	}

	return tokens, nil
}

func (m *Manager) LookupToken(token string) (TokenRow, error) {
	if token == "" {
		return TokenRow{}, ErrTokenNotFound
	}
	var raw []byte
	err := m.st.View(func(txn *badger.Txn) error {
		item, err := txn.Get(tokenKey(token))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrTokenNotFound
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
		return TokenRow{}, err
	}
	var row TokenRow
	if err := json.Unmarshal(raw, &row); err != nil {
		return TokenRow{}, fmt.Errorf("files: decode token row: %w", err)
	}
	if row.ReceiptsRemaining <= 0 {
		return row, ErrTokenInvalidated
	}
	return row, nil
}

func (m *Manager) OpenBlob(msgID string) (*os.File, int64, error) {
	if msgID == "" {
		return nil, 0, ErrBlobNotFound
	}
	f, err := os.Open(m.blobPath(msgID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, ErrBlobNotFound
		}
		return nil, 0, fmt.Errorf("files: open blob %s: %w", msgID, err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, fmt.Errorf("files: stat blob %s: %w", msgID, err)
	}
	return f, st.Size(), nil
}

func (m *Manager) DecrementReceipts(token string) error {
	if token == "" {
		return ErrTokenNotFound
	}
	return m.st.Update(func(txn *badger.Txn) error {
		item, err := txn.Get(tokenKey(token))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrTokenNotFound
		}
		if err != nil {
			return err
		}
		var row TokenRow
		if err := item.Value(func(v []byte) error {
			return json.Unmarshal(v, &row)
		}); err != nil {
			return fmt.Errorf("files: decode token row: %w", err)
		}
		if row.ReceiptsRemaining > 0 {
			row.ReceiptsRemaining--
		}
		raw, err := json.Marshal(row)
		if err != nil {
			return fmt.Errorf("files: marshal token row: %w", err)
		}
		return txn.Set(tokenKey(token), raw)
	})
}

func (m *Manager) DropByMsgID(msgID string) error {
	if msgID == "" {
		return errors.New("files: empty msg id")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	tokens, err := m.tokensForMsg(msgID)
	if err != nil {
		return err
	}
	if err := m.st.Update(func(txn *badger.Txn) error {
		for _, tok := range tokens {
			if err := txn.Delete(tokenKey(tok)); err != nil {
				return err
			}
			if err := txn.Delete(indexKey(msgID, tok)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("files: drop txn: %w", err)
	}

	if err := os.Remove(m.blobPath(msgID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("files: remove blob %s: %w", msgID, err)
	}
	return nil
}

func (m *Manager) hasTokensForMsg(msgID string) (bool, error) {
	prefix := append([]byte(prefixIndex), msgID...)
	prefix = append(prefix, ':')
	var found bool
	err := m.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()
		it.Rewind()
		found = it.Valid()
		return nil
	})
	return found, err
}

func (m *Manager) tokensForMsg(msgID string) ([]string, error) {
	prefix := append([]byte(prefixIndex), msgID...)
	prefix = append(prefix, ':')
	var out []string
	err := m.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			key := it.Item().KeyCopy(nil)
			out = append(out, string(key[len(prefix):]))
		}
		return nil
	})
	return out, err
}

func mintToken() (string, error) {
	var buf [TokenByteLen]byte
	if _, err := io.ReadFull(rand.Reader, buf[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf[:]), nil
}

func tokenKey(token string) []byte {
	out := make([]byte, 0, len(prefixToken)+len(token))
	out = append(out, prefixToken...)
	out = append(out, token...)
	return out
}

func indexKey(msgID, token string) []byte {
	out := make([]byte, 0, len(prefixIndex)+len(msgID)+1+len(token))
	out = append(out, prefixIndex...)
	out = append(out, msgID...)
	out = append(out, ':')
	out = append(out, token...)
	return out
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("files: mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("files: create temp in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(fileMode); err != nil {
		tmp.Close()
		return fmt.Errorf("files: chmod temp: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("files: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("files: fsync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("files: close temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("files: rename %s: %w", path, err)
	}
	return nil
}
