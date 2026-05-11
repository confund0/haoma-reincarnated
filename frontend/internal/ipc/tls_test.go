package ipc

import (
	"crypto/tls"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateTLS_GeneratesOnFirstRun(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadOrCreateTLS(dir)
	if err != nil {
		t.Fatalf("LoadOrCreateTLS: %v", err)
	}
	if cfg == nil || len(cfg.Certificates) != 1 {
		t.Fatalf("cfg = %+v", cfg)
	}

	assertPerm(t, filepath.Join(dir, certFilename), 0o644)
	assertPerm(t, filepath.Join(dir, keyFilename), 0o600)
}

func TestLoadOrCreateTLS_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfg1, err := LoadOrCreateTLS(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg2, err := LoadOrCreateTLS(dir)
	if err != nil {
		t.Fatal(err)
	}

	c1 := cfg1.Certificates[0].Certificate[0]
	c2 := cfg2.Certificates[0].Certificate[0]
	if !bytesEqual(c1, c2) {
		t.Error("second LoadOrCreateTLS regenerated the cert; should be idempotent")
	}
}

func TestLoadOrCreateTLS_MismatchedPair_Errors(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, certFilename), []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadOrCreateTLS(dir)
	if err == nil {
		t.Error("expected error on mismatched cert/key pair")
	}
}

func TestLoadOrCreateTLS_ConfigUsableForTLS(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadOrCreateTLS(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = %v, want TLS 1.3", cfg.MinVersion)
	}

}

func assertPerm(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != want {
		t.Errorf("%s mode = %o, want %o", path, perm, want)
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
