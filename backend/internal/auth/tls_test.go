package auth

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
	if !bytesEqualTLS(c1, c2) {
		t.Error("second LoadOrCreateTLS regenerated the cert; should be idempotent")
	}
}

func TestLoadOrCreateTLS_MismatchedPair_Errors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, certFilename), []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateTLS(dir); err == nil {
		t.Error("expected error on mismatched cert/key pair")
	}
}

func TestLoadOrCreateTLS_TLS13Min(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadOrCreateTLS(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = %v, want TLS 1.3", cfg.MinVersion)
	}
}

func TestPinnedClientTLSConfig_PinsServerCert(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadOrCreateTLS(dir); err != nil {
		t.Fatal(err)
	}
	cfg, err := PinnedClientTLSConfig(CertPath(dir))
	if err != nil {
		t.Fatalf("PinnedClientTLSConfig: %v", err)
	}
	if cfg.RootCAs == nil {
		t.Fatal("RootCAs nil — pinning didn't load the cert into the pool")
	}
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("client MinVersion = %v, want TLS 1.3", cfg.MinVersion)
	}
	if cfg.ServerName != "haoma-backend" {
		t.Errorf("ServerName = %q, want haoma-backend", cfg.ServerName)
	}
}

func TestPinnedClientTLSConfig_GarbagePEM_Errors(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "garbage.pem")
	if err := os.WriteFile(bad, []byte("not a pem block"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PinnedClientTLSConfig(bad); err == nil {
		t.Error("expected error on non-PEM file")
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

func bytesEqualTLS(a, b []byte) bool {
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
