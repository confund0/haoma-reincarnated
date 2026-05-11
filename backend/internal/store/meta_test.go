package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func sampleMeta(t *testing.T) Meta {
	t.Helper()
	salt, err := newSalt(DefaultKDFParams)
	if err != nil {
		t.Fatalf("newSalt: %v", err)
	}
	return Meta{Version: metaFormatVersion, Salt: salt, KDF: DefaultKDFParams}
}

func TestSaveLoadMeta_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := sampleMeta(t)
	if err := saveMeta(dir, want); err != nil {
		t.Fatalf("saveMeta: %v", err)
	}
	got, found, err := loadMeta(dir)
	if err != nil || !found {
		t.Fatalf("loadMeta: found=%v err=%v", found, err)
	}
	if got.Version != want.Version {
		t.Errorf("Version = %d, want %d", got.Version, want.Version)
	}
	if !bytes.Equal(got.Salt, want.Salt) {
		t.Errorf("Salt mismatch")
	}
	if got.KDF != want.KDF {
		t.Errorf("KDF = %+v, want %+v", got.KDF, want.KDF)
	}
}

func TestLoadMeta_Missing(t *testing.T) {
	dir := t.TempDir()
	m, found, err := loadMeta(dir)
	if err != nil {
		t.Fatalf("unexpected error on missing meta: %v", err)
	}
	if found {
		t.Fatalf("found=true on empty dir")
	}
	if m.Version != 0 || len(m.Salt) != 0 {
		t.Fatalf("expected zero Meta, got %+v", m)
	}
}

func TestLoadMeta_CorruptJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, metaFileName), []byte("{not json"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, _, err := loadMeta(dir)
	if !errors.Is(err, ErrMetaCorrupt) {
		t.Fatalf("err = %v, want ErrMetaCorrupt", err)
	}
}

func TestLoadMeta_MissingVersionField(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, metaFileName), []byte(`{"salt":"AAAA","kdf":{}}`), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, _, err := loadMeta(dir)
	if !errors.Is(err, ErrMetaCorrupt) {
		t.Fatalf("err = %v, want ErrMetaCorrupt", err)
	}
}

func TestLoadMeta_UnsupportedNewerVersion(t *testing.T) {
	dir := t.TempDir()
	body := []byte(`{"version":9999,"salt":"AAAA","kdf":{"time":1,"memory_kib":1,"threads":1,"key_len":32,"salt_len":16}}`)
	if err := os.WriteFile(filepath.Join(dir, metaFileName), body, 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, _, err := loadMeta(dir)
	if !errors.Is(err, ErrMetaVersionUnsupported) {
		t.Fatalf("err = %v, want ErrMetaVersionUnsupported", err)
	}
}

func TestSaveMeta_FilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions not meaningful on windows")
	}
	dir := t.TempDir()
	if err := saveMeta(dir, sampleMeta(t)); err != nil {
		t.Fatalf("saveMeta: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, metaFileName))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode = %o, want 0600", mode)
	}
}

func TestSaveMeta_AtomicReplaceDoesNotLeaveTmp(t *testing.T) {
	dir := t.TempDir()
	if err := saveMeta(dir, sampleMeta(t)); err != nil {
		t.Fatalf("saveMeta: %v", err)
	}
	if err := saveMeta(dir, sampleMeta(t)); err != nil {
		t.Fatalf("saveMeta (second): %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("leftover tmp file: %s", e.Name())
		}
	}
}
