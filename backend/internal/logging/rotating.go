package logging

import (
	"fmt"
	"os"
	"sync"
)

const defaultMaxBytes int64 = 4 << 20

type rotatingWriter struct {
	path     string
	maxBytes int64
	mode     os.FileMode
	mu       sync.Mutex
	f        *os.File
	size     int64
}

func newRotatingWriter(path string, maxBytes int64, mode os.FileMode) (*rotatingWriter, error) {
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}
	if mode == 0 {
		mode = DefaultFileMode
	}
	f, err := openLogFile(path, mode)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &rotatingWriter{path: path, maxBytes: maxBytes, mode: mode, f: f, size: st.Size()}, nil
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.size > 0 && w.size+int64(len(p)) > w.maxBytes {
		if err := w.rotateLocked(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *rotatingWriter) rotateLocked() error {
	if err := w.f.Close(); err != nil {
		return fmt.Errorf("logging: rotate close: %w", err)
	}
	prev := w.path + ".prev"
	_ = os.Remove(prev)
	if err := os.Rename(w.path, prev); err != nil {
		return fmt.Errorf("logging: rotate rename: %w", err)
	}
	f, err := openLogFile(w.path, w.mode)
	if err != nil {
		return fmt.Errorf("logging: rotate reopen: %w", err)
	}
	w.f = f
	w.size = 0
	return nil
}

const DefaultFileMode os.FileMode = 0o600

func openLogFile(path string, mode os.FileMode) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, mode)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("logging: set mode %o: %w", mode, err)
	}
	return f, nil
}

func ParseFileMode(s string) (os.FileMode, error) {
	switch s {
	case "":
		return DefaultFileMode, nil
	case "0600", "600":
		return 0o600, nil
	case "0640", "640":
		return 0o640, nil
	case "0660", "660":
		return 0o660, nil
	}
	return 0, fmt.Errorf("logging: file mode %q refused (want 0600, 0640 or 0660)", s)
}

func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}
