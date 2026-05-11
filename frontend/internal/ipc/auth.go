package ipc

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"haoma-frontend/internal/paths"
)

const tokenFilename = "token"

var ErrBadToken = errors.New("ipc: bad bearer token")

func LoadOrCreateToken(dataDir string) (string, error) {
	path := filepath.Join(dataDir, tokenFilename)
	info, err := os.Stat(path)
	if err == nil {
		if info.IsDir() {
			return "", fmt.Errorf("ipc: token path %s is a directory", path)
		}
		if perm := info.Mode().Perm(); perm != paths.SecureFileMode {
			return "", fmt.Errorf("ipc: token file %s has mode %o, want %o — refusing to use (run: chmod 600 %s)",
				path, perm, paths.SecureFileMode, path)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("ipc: read token: %w", err)
		}
		tok := strings.TrimSpace(string(b))
		if tok == "" {
			return "", fmt.Errorf("ipc: token file %s is empty", path)
		}
		return tok, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("ipc: stat token: %w", err)
	}

	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("ipc: generate token: %w", err)
	}
	tok := hex.EncodeToString(raw[:])
	if err := paths.WriteSensitiveFile(path, []byte(tok)); err != nil {
		return "", fmt.Errorf("ipc: write token: %w", err)
	}
	return tok, nil
}

func TokenPath(dataDir string) string {
	return filepath.Join(dataDir, tokenFilename)
}

func ReadToken(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("ipc: read token %s: %w", path, err)
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", fmt.Errorf("ipc: token file %s is empty", path)
	}
	return tok, nil
}

func ReadSensitive(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("ipc: stat %s: %w", path, err)
	}
	if perm := info.Mode().Perm(); perm != paths.SecureFileMode {
		return "", fmt.Errorf("ipc: %s has mode %o, want %o — refusing to use (run: chmod 600 %s)",
			path, perm, paths.SecureFileMode, path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("ipc: read %s: %w", path, err)
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", fmt.Errorf("ipc: %s is empty", path)
	}
	return s, nil
}

func CheckToken(expected, presented string) error {
	if subtle.ConstantTimeCompare([]byte(expected), []byte(presented)) != 1 {
		return ErrBadToken
	}
	return nil
}
