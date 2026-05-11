package files

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/dgraph-io/badger/v4"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/store"

	"crypto/sha256"
)

const SubdirName = "files"

const StagingSubdir = "staging"

const OpenSubdir = "open"

const HKDFInfo = "haoma-file-seal-v1"

const (
	keyMaster     = "files:master"
	prefixMeta    = "file:"
	prefixIndex   = "file-by-chat:"
	prefixByToken = "file-by-token:"
)

const (
	dirMode  os.FileMode = 0o700
	fileMode os.FileMode = 0o600
)

const MaxPlaintextBytes = 10 * 1024 * 1024

var ErrSealedNotFound = errors.New("files: sealed file not found")

type Manager struct {
	st      *store.Store
	rootDir string

	mu     sync.RWMutex
	master [32]byte
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

	m := &Manager{st: st, rootDir: root}
	if err := m.loadOrInitMaster(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) RootDir() string { return m.rootDir }

func (m *Manager) loadOrInitMaster() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	raw, err := m.st.Get([]byte(keyMaster))
	if err == nil {
		if len(raw) != 32 {
			return fmt.Errorf("files: stored master has wrong length %d", len(raw))
		}
		copy(m.master[:], raw)
		return nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("files: read master: %w", err)
	}

	var fresh [32]byte
	if _, err := io.ReadFull(rand.Reader, fresh[:]); err != nil {
		return fmt.Errorf("files: mint master: %w", err)
	}
	if err := m.st.Put([]byte(keyMaster), fresh[:]); err != nil {
		return fmt.Errorf("files: persist master: %w", err)
	}
	m.master = fresh
	return nil
}

func (m *Manager) derivePerFileKey(msgID string) ([]byte, error) {
	if msgID == "" {
		return nil, errors.New("files: empty msg id")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	r := hkdf.New(sha256.New, m.master[:], []byte(msgID), []byte(HKDFInfo))
	out := make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, fmt.Errorf("files: hkdf: %w", err)
	}
	return out, nil
}

func (m *Manager) sealedPath(chatID chat.ChatID, msgID string) (string, error) {
	if chatID == "" {
		return "", errors.New("files: empty chat id")
	}
	if msgID == "" {
		return "", errors.New("files: empty msg id")
	}
	if !isHex(string(chatID)) {
		return "", fmt.Errorf("files: chat id %q is not hex", chatID)
	}
	if !isHex(msgID) {
		return "", fmt.Errorf("files: msg id %q is not hex", msgID)
	}
	return filepath.Join(m.rootDir, string(chatID), msgID), nil
}

func (m *Manager) SealAtRest(chatID chat.ChatID, msgID string, plaintext []byte) (string, error) {
	if len(plaintext) > MaxPlaintextBytes {
		return "", fmt.Errorf("files: plaintext %d bytes exceeds cap %d", len(plaintext), MaxPlaintextBytes)
	}
	dst, err := m.sealedPath(chatID, msgID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dst), dirMode); err != nil {
		return "", fmt.Errorf("files: mkdir %s: %w", filepath.Dir(dst), err)
	}

	key, err := m.derivePerFileKey(msgID)
	if err != nil {
		return "", err
	}
	defer zero(key)

	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return "", fmt.Errorf("files: aead init: %w", err)
	}

	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("files: nonce: %w", err)
	}

	out := make([]byte, 0, len(nonce)+len(plaintext)+aead.Overhead())
	out = append(out, nonce...)
	out = aead.Seal(out, nonce, plaintext, []byte(msgID))

	if err := writeAtomic(dst, out); err != nil {
		return "", err
	}
	return dst, nil
}

func (m *Manager) UnsealAtRest(chatID chat.ChatID, msgID string) ([]byte, error) {
	src, err := m.sealedPath(chatID, msgID)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrSealedNotFound
		}
		return nil, fmt.Errorf("files: read sealed %s: %w", src, err)
	}
	if len(raw) < chacha20poly1305.NonceSizeX+chacha20poly1305.Overhead {
		return nil, fmt.Errorf("files: sealed file truncated (%d bytes)", len(raw))
	}
	nonce := raw[:chacha20poly1305.NonceSizeX]
	ct := raw[chacha20poly1305.NonceSizeX:]

	key, err := m.derivePerFileKey(msgID)
	if err != nil {
		return nil, err
	}
	defer zero(key)

	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("files: aead init: %w", err)
	}
	pt, err := aead.Open(nil, nonce, ct, []byte(msgID))
	if err != nil {
		return nil, fmt.Errorf("files: unseal: %w", err)
	}
	return pt, nil
}

func (m *Manager) DeleteSealed(chatID chat.ChatID, msgID string) error {
	dst, err := m.sealedPath(chatID, msgID)
	if err != nil {
		return err
	}
	if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("files: remove %s: %w", dst, err)
	}

	_ = os.Remove(filepath.Dir(dst))
	return nil
}

func (m *Manager) DeleteSealedByChat(chatID chat.ChatID) error {
	if chatID == "" {
		return errors.New("files: empty chat id")
	}
	if !isHex(string(chatID)) {
		return fmt.Errorf("files: chat id %q is not hex", chatID)
	}
	dir := filepath.Join(m.rootDir, string(chatID))
	if err := os.RemoveAll(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("files: remove dir %s: %w", dir, err)
	}
	return nil
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
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

func isHex(s string) bool {
	if s == "" {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

func (m *Manager) StagingDir() string {
	return filepath.Join(m.rootDir, StagingSubdir)
}

func (m *Manager) StagingPath(msgID string) (string, error) {
	if msgID == "" {
		return "", errors.New("files: empty msg id")
	}
	if !isHex(msgID) {
		return "", fmt.Errorf("files: msg id %q is not hex", msgID)
	}
	return filepath.Join(m.StagingDir(), msgID), nil
}

func (m *Manager) WriteStaging(msgID string, ciphertext []byte) (string, error) {
	dst, err := m.StagingPath(msgID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dst), dirMode); err != nil {
		return "", fmt.Errorf("files: mkdir %s: %w", filepath.Dir(dst), err)
	}
	if err := writeAtomic(dst, ciphertext); err != nil {
		return "", err
	}
	return dst, nil
}

func (m *Manager) OpenDir() string {
	return filepath.Join(m.rootDir, OpenSubdir)
}

func (m *Manager) OpenPath(msgID string) (string, error) {
	if msgID == "" {
		return "", errors.New("files: empty msg id")
	}
	if !isHex(msgID) {
		return "", fmt.Errorf("files: msg id %q is not hex", msgID)
	}
	return filepath.Join(m.OpenDir(), msgID), nil
}

func (m *Manager) WriteOpenTransient(chatID chat.ChatID, msgID string) (string, error) {
	plaintext, err := m.UnsealAtRest(chatID, msgID)
	if err != nil {
		return "", err
	}
	dst, err := m.OpenPath(msgID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dst), dirMode); err != nil {
		return "", fmt.Errorf("files: mkdir %s: %w", filepath.Dir(dst), err)
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fileMode)
	if err != nil {
		return "", fmt.Errorf("files: open transient %s: %w", dst, err)
	}
	if _, werr := f.Write(plaintext); werr != nil {
		_ = f.Close()
		_ = os.Remove(dst)
		return "", fmt.Errorf("files: write transient %s: %w", dst, werr)
	}
	if cerr := f.Close(); cerr != nil {
		_ = os.Remove(dst)
		return "", fmt.Errorf("files: close transient %s: %w", dst, cerr)
	}
	return dst, nil
}

func (m *Manager) WipeOpenTransient() error {
	if err := os.RemoveAll(m.OpenDir()); err != nil {
		return fmt.Errorf("files: wipe open transient: %w", err)
	}
	return nil
}

func (m *Manager) DeleteStaging(msgID string) error {
	dst, err := m.StagingPath(msgID)
	if err != nil {
		return err
	}
	if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("files: remove staging %s: %w", msgID, err)
	}
	return nil
}

var ErrAEADOpen = errors.New("files: aead open failed")

func (m *Manager) DecryptSealMove(chatID chat.ChatID, msgID string, key, nonce []byte) (string, error) {
	if msgID == "" {
		return "", errors.New("files: empty msg id")
	}
	if len(key) != chacha20poly1305.KeySize {
		return "", fmt.Errorf("files: key must be %d bytes, got %d", chacha20poly1305.KeySize, len(key))
	}
	if len(nonce) != chacha20poly1305.NonceSizeX {
		return "", fmt.Errorf("files: nonce must be %d bytes, got %d", chacha20poly1305.NonceSizeX, len(nonce))
	}

	stagingPath, err := m.StagingPath(msgID)
	if err != nil {
		return "", err
	}
	ciphertext, err := os.ReadFile(stagingPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%w (staging): %s", ErrSealedNotFound, msgID)
		}
		return "", fmt.Errorf("files: read staging %s: %w", msgID, err)
	}

	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return "", fmt.Errorf("files: aead init: %w", err)
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrAEADOpen, err)
	}
	if len(plaintext) > MaxPlaintextBytes {

		zero(plaintext)
		return "", fmt.Errorf("files: plaintext %d bytes exceeds cap %d", len(plaintext), MaxPlaintextBytes)
	}

	sealedPath, err := m.SealAtRest(chatID, msgID, plaintext)
	zero(plaintext)
	if err != nil {
		return "", err
	}

	if err := m.DeleteStaging(msgID); err != nil {
		return sealedPath, fmt.Errorf("files: drop staging post-seal: %w", err)
	}
	return sealedPath, nil
}

func (m *Manager) scanChatMeta(chatID chat.ChatID, fn func(msgID string) error) error {
	if chatID == "" {
		return errors.New("files: empty chat id")
	}
	scanPrefix := append([]byte(prefixIndex), chatID...)
	scanPrefix = append(scanPrefix, ':')
	return m.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = scanPrefix
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			key := it.Item().Key()
			msgID := string(key[len(scanPrefix):])
			if err := fn(msgID); err != nil {
				return err
			}
		}
		return nil
	})
}
