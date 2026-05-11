package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	metaFileName      = "meta.json"
	metaFormatVersion = 1
)

type Meta struct {
	Version int       `json:"version"`
	Salt    []byte    `json:"salt"`
	KDF     KDFParams `json:"kdf"`
}

var (
	ErrMetaCorrupt            = errors.New("store: meta corrupt")
	ErrMetaVersionUnsupported = errors.New("store: meta version unsupported")
)

func metaPath(dir string) string { return filepath.Join(dir, metaFileName) }

func loadMeta(dir string) (Meta, bool, error) {
	data, err := os.ReadFile(metaPath(dir))
	if errors.Is(err, fs.ErrNotExist) {
		return Meta{}, false, nil
	}
	if err != nil {
		return Meta{}, false, err
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return Meta{}, false, fmt.Errorf("%w: %v", ErrMetaCorrupt, err)
	}
	if m.Version == 0 {
		return Meta{}, false, fmt.Errorf("%w: missing version", ErrMetaCorrupt)
	}
	if m.Version > metaFormatVersion {
		return Meta{}, false, fmt.Errorf("%w: got %d, supports up to %d", ErrMetaVersionUnsupported, m.Version, metaFormatVersion)
	}
	return m, true, nil
}

func saveMeta(dir string, m Meta) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := metaPath(dir) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, metaPath(dir))
}
