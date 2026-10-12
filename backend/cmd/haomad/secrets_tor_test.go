package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSecretsSystemTorNeedsThePassword(t *testing.T) {
	t.Setenv("HAOMA_PASSPHRASE", "store-pass")
	t.Setenv("HAOMA_TOR_PASSWORD", "")
	_, err := loadSecrets(false, false, "", "", "")
	if err == nil || !strings.Contains(err.Error(), "tor password") {
		t.Fatalf("system tor with no password: err = %v, want a tor password error", err)
	}

	t.Setenv("HAOMA_TOR_PASSWORD", "tor-pw")
	s, err := loadSecrets(false, false, "", "", "")
	if err != nil {
		t.Fatalf("system tor with a password: %v", err)
	}
	if s.TorPassword != "tor-pw" || s.HaomadStorePassphrase != "store-pass" {
		t.Fatalf("got %+v", s)
	}
}

func TestLoadSecretsOwnTorNeedsNoPassword(t *testing.T) {
	t.Setenv("HAOMA_PASSPHRASE", "store-pass")
	t.Setenv("HAOMA_TOR_PASSWORD", "")
	s, err := loadSecrets(false, true, "", "", "")
	if err != nil {
		t.Fatalf("own tor with no password: %v", err)
	}
	if s.HaomadStorePassphrase != "store-pass" || s.TorPassword != "" {
		t.Fatalf("got %+v", s)
	}

	t.Setenv("HAOMA_TOR_PASSWORD", "from-the-shell")
	s, err = loadSecrets(false, true, "", "", "")
	if err != nil {
		t.Fatalf("own tor, password in the shell: %v", err)
	}
	if s.TorPassword != "" {
		t.Fatalf("own tor took the shell's tor password: %q", s.TorPassword)
	}
}

func TestLoadSecretsOwnTorRefusesAPasswordFile(t *testing.T) {
	t.Setenv("HAOMA_PASSPHRASE", "store-pass")
	f := filepath.Join(t.TempDir(), "tor-pw")
	if err := os.WriteFile(f, []byte("pw"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadSecrets(false, true, "", f, "")
	if err == nil || !strings.Contains(err.Error(), "--tor-password-file") {
		t.Fatalf("own tor with --tor-password-file: err = %v, want a refusal", err)
	}
}

func TestLoadSecretsOwnTorStillNeedsThePassphrase(t *testing.T) {
	t.Setenv("HAOMA_PASSPHRASE", "")
	if _, err := loadSecrets(false, true, "", "", ""); err == nil {
		t.Fatal("own tor with no store passphrase: no error")
	}
}
