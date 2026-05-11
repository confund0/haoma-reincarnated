package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const SecureFileMode os.FileMode = 0o600

var ErrBadToken = errors.New("auth: bad bearer token")

func LoadOrCreateToken(path string) (string, error) {
	info, err := os.Stat(path)
	if err == nil {
		if info.IsDir() {
			return "", fmt.Errorf("auth: token path %s is a directory", path)
		}
		if perm := info.Mode().Perm(); perm != SecureFileMode {
			return "", fmt.Errorf("auth: token file %s has mode %o, want %o — refusing to run (run: chmod 600 %s)",
				path, perm, SecureFileMode, path)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("auth: read token: %w", err)
		}
		tok := strings.TrimSpace(string(b))
		if tok == "" {
			return "", fmt.Errorf("auth: token file %s is empty", path)
		}
		return tok, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("auth: stat token: %w", err)
	}

	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("auth: generate token: %w", err)
	}
	tok := hex.EncodeToString(raw[:])
	if err := writeSensitiveFile(path, []byte(tok)); err != nil {
		return "", fmt.Errorf("auth: write token: %w", err)
	}
	return tok, nil
}

func ReadSensitive(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("auth: stat %s: %w", path, err)
	}
	if perm := info.Mode().Perm(); perm != SecureFileMode {
		return "", fmt.Errorf("auth: %s has mode %o, want %o — refusing to use (run: chmod 600 %s)",
			path, perm, SecureFileMode, path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("auth: read %s: %w", path, err)
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", fmt.Errorf("auth: %s is empty", path)
	}
	return s, nil
}

func CheckToken(expected, presented string) error {
	if subtle.ConstantTimeCompare([]byte(expected), []byte(presented)) != 1 {
		return ErrBadToken
	}
	return nil
}

func Middleware(expected string, skipPaths []string, next http.Handler) http.Handler {
	skip := make(map[string]struct{}, len(skipPaths))
	for _, p := range skipPaths {
		skip[p] = struct{}{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, exempt := skip[r.URL.Path]; exempt {
			next.ServeHTTP(w, r)
			return
		}
		h := r.Header.Get("Authorization")
		if h == "" {
			http.Error(w, "unauthorized: missing Authorization header", http.StatusUnauthorized)
			return
		}
		if !strings.HasPrefix(h, "Bearer ") {
			http.Error(w, "unauthorized: expected Bearer scheme", http.StatusUnauthorized)
			return
		}
		if err := CheckToken(expected, strings.TrimPrefix(h, "Bearer ")); err != nil {
			http.Error(w, "unauthorized: bad token", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeSensitiveFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("auth: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(SecureFileMode); err != nil {
		tmp.Close()
		return fmt.Errorf("auth: chmod temp: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("auth: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("auth: close temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("auth: rename %s: %w", path, err)
	}
	return nil
}
