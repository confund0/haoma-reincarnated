package vault

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var fastParams = KDFParams{
	Time:    1,
	Memory:  8 * 1024,
	Threads: 1,
	KeyLen:  32,
}

func mintFresh(t *testing.T) Payload {
	t.Helper()
	p, err := MintFreshPayload()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	return p
}

func TestCreate_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.enc")
	want := mintFresh(t)

	if err := Create(path, "correct horse battery staple", want, fastParams); err != nil {
		t.Fatalf("create: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != fileMode {
		t.Errorf("vault perms = %o, want %o", perm, fileMode)
	}

	got, gotParams, err := Open(path, "correct horse battery staple")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("payload drift:\n got  %+v\n want %+v", got, want)
	}
	if gotParams != fastParams {
		t.Errorf("params drift:\n got  %+v\n want %+v", gotParams, fastParams)
	}
}

func TestOpen_WrongPassphraseFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.enc")
	if err := Create(path, "right", mintFresh(t), fastParams); err != nil {
		t.Fatalf("create: %v", err)
	}
	_, _, err := Open(path, "wrong")
	if !errors.Is(err, ErrUnseal) {
		t.Fatalf("expected ErrUnseal, got %v", err)
	}
}

func TestOpen_TamperedCiphertextFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.enc")
	if err := Create(path, "pw", mintFresh(t), fastParams); err != nil {
		t.Fatalf("create: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	raw[headerLen+4] ^= 0x01
	if err := os.WriteFile(path, raw, fileMode); err != nil {
		t.Fatal(err)
	}
	_, _, err = Open(path, "pw")
	if !errors.Is(err, ErrUnseal) {
		t.Fatalf("expected ErrUnseal on tampered ciphertext, got %v", err)
	}
}

func TestOpen_TamperedAADFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.enc")
	if err := Create(path, "pw", mintFresh(t), fastParams); err != nil {
		t.Fatalf("create: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	raw[8] = 0x99
	if err := os.WriteFile(path, raw, fileMode); err != nil {
		t.Fatal(err)
	}
	_, _, err = Open(path, "pw")

	if err == nil {
		t.Fatal("expected error on AAD/version tamper")
	}
}

func TestOpen_BadMagic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.enc")
	bad := make([]byte, headerLen+32)
	copy(bad, "NOPE")
	if err := os.WriteFile(path, bad, fileMode); err != nil {
		t.Fatal(err)
	}
	_, _, err := Open(path, "x")
	if err == nil || !strings.Contains(err.Error(), "magic") {
		t.Fatalf("expected magic error, got %v", err)
	}
}

func TestOpen_Truncated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.enc")
	if err := os.WriteFile(path, []byte("HAOMAVLT\x01"), fileMode); err != nil {
		t.Fatal(err)
	}
	_, _, err := Open(path, "x")
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("expected ErrTruncated, got %v", err)
	}
}

func TestOpen_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.enc")
	if err := os.WriteFile(path, nil, fileMode); err != nil {
		t.Fatal(err)
	}
	_, _, err := Open(path, "x")
	if !errors.Is(err, ErrEmpty) {
		t.Fatalf("expected ErrEmpty, got %v", err)
	}
}

func TestCreate_RefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.enc")
	if err := Create(path, "a", mintFresh(t), fastParams); err != nil {
		t.Fatal(err)
	}
	err := Create(path, "b", mintFresh(t), fastParams)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected refuse-overwrite, got %v", err)
	}
}

func TestChangePassphrase_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.enc")
	want := mintFresh(t)
	if err := Create(path, "old", want, fastParams); err != nil {
		t.Fatal(err)
	}
	if err := ChangePassphrase(path, "old", "new"); err != nil {
		t.Fatalf("change: %v", err)
	}
	if _, _, err := Open(path, "old"); !errors.Is(err, ErrUnseal) {
		t.Errorf("old passphrase should fail post-change, got %v", err)
	}
	got, _, err := Open(path, "new")
	if err != nil {
		t.Fatalf("open with new: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("payload changed across rotation; got %+v want %+v", got, want)
	}
}

func TestChangePassphrase_WrongOldFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.enc")
	if err := Create(path, "right", mintFresh(t), fastParams); err != nil {
		t.Fatal(err)
	}
	err := ChangePassphrase(path, "wrong", "new")
	if !errors.Is(err, ErrUnseal) {
		t.Fatalf("expected ErrUnseal, got %v", err)
	}
}

func TestCreateInsecure_OpensWithDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.enc")
	want := mintFresh(t)
	if err := CreateInsecure(path, want, fastParams); err != nil {
		t.Fatalf("create insecure: %v", err)
	}
	got, _, err := Open(path, InsecureDefaultPassphrase)
	if err != nil {
		t.Fatalf("open with default: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("payload drift")
	}
}

func TestIsInsecureDefaultPassphrase(t *testing.T) {
	if !IsInsecureDefaultPassphrase(InsecureDefaultPassphrase) {
		t.Error("constant should match itself")
	}
	if IsInsecureDefaultPassphrase("anything-else") {
		t.Error("non-default flagged as insecure")
	}
	if IsInsecureDefaultPassphrase("") {
		t.Error("empty flagged as insecure default")
	}
}

func TestIsInsecureDefaultPIN(t *testing.T) {
	if !IsInsecureDefaultPIN(InsecureDefaultPIN) {
		t.Error("constant should match itself")
	}
	if IsInsecureDefaultPIN("1234") {
		t.Error("non-default PIN flagged as insecure")
	}
	if IsInsecureDefaultPIN("") {
		t.Error("empty PIN flagged as insecure default")
	}
}

func TestInsecureDefaults_AreDistinct(t *testing.T) {

	if IsInsecureDefaultPassphrase(InsecureDefaultPIN) {
		t.Error("PIN constant flagged as default passphrase")
	}
	if IsInsecureDefaultPIN(InsecureDefaultPassphrase) {
		t.Error("passphrase constant flagged as default PIN")
	}
}

func TestMintFreshSecrets_PopulatesRequiredFields(t *testing.T) {
	s, err := MintFreshSecrets()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if err := s.Validate(); err != nil {
		t.Errorf("freshly minted Secrets should validate: %v", err)
	}
	if s.HaomadStorePassphrase == s.FrontendStorePassphrase {
		t.Error("the two store passphrases collided — entropy bug")
	}
}

func TestMintFreshPayload_AppliesDefaults(t *testing.T) {
	p, err := MintFreshPayload()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if err := p.Validate(); err != nil {
		t.Errorf("freshly minted Payload should validate: %v", err)
	}
	if p.IdleTimeoutSeconds != DefaultIdleTimeoutSec {
		t.Errorf("IdleTimeoutSeconds = %d, want %d", p.IdleTimeoutSeconds, DefaultIdleTimeoutSec)
	}
	if p.IdleAction != DefaultIdleAction {
		t.Errorf("IdleAction = %q, want %q", p.IdleAction, DefaultIdleAction)
	}
	if !p.NotificationsOnLock {
		t.Error("NotificationsOnLock should default true")
	}
	if p.PIN != InsecureDefaultPIN {
		t.Errorf("PIN should default to InsecureDefaultPIN %q; got %q", InsecureDefaultPIN, p.PIN)
	}
	if p.ThreatProfile != "" {
		t.Errorf("ThreatProfile should be empty pre-wizard; got %q", p.ThreatProfile)
	}
	if p.PanicAction != "" {
		t.Errorf("PanicAction should be empty (disabled); got %q", p.PanicAction)
	}
	if p.PinValiditySec != 0 {
		t.Errorf("PinValiditySec should be 0; got %d", p.PinValiditySec)
	}
	if p.RotationIntervalSec != 0 {
		t.Errorf("RotationIntervalSec should be 0 (protocol default); got %d", p.RotationIntervalSec)
	}
	if len(p.SecurityWarnings) != 0 {
		t.Errorf("SecurityWarnings should be empty; got %v", p.SecurityWarnings)
	}

	if p.DefaultRetentionSec != 0 {
		t.Errorf("DefaultRetentionSec should be 0 (no expiry); got %d", p.DefaultRetentionSec)
	}
	if !p.DefaultSendReceipts {
		t.Error("DefaultSendReceipts should default true (Slice 5 alignment)")
	}
	if !p.NotifyShellEnabled {
		t.Error("NotifyShellEnabled should default true (notify on; banner stays anonymous)")
	}
	if p.NotifyShowSender {
		t.Error("NotifyShowSender should default false (privacy-first)")
	}
	if p.NotifyShowBody {
		t.Error("NotifyShowBody should default false (privacy-first)")
	}

	if p.DefaultSaveDir == "" {
		t.Error("DefaultSaveDir should be seeded from paths.DefaultDownloadsDir on mint")
	}
	if p.DefaultAttachStartDir == "" {
		t.Error("DefaultAttachStartDir should be seeded from paths.DefaultAttachStartDir on mint")
	}
}

func TestPayload_RoundTripsAllFields(t *testing.T) {

	dir := t.TempDir()
	path := filepath.Join(dir, "vault.enc")
	want := mintFresh(t)
	want.ThreatProfile = "privacy"
	want.PIN = "1357"
	want.IdleAction = "soft-lock"
	want.PinValiditySec = 300
	want.PanicAction = "hard-lock"
	want.NotificationsOnLock = false
	want.RotationIntervalSec = 600
	want.SecurityWarnings = []string{"pin_validity_exceeds_recommended"}
	want.HaomadURL = "http://127.0.0.1:9999"
	want.TorPassword = "torpw"

	want.DefaultRetentionSec = 86400
	want.DefaultSendReceipts = false
	want.NotifyShellEnabled = true
	want.NotifyShowSender = true
	want.NotifyShowBody = true

	want.DefaultSaveDir = "/tmp/haoma-test-saves"
	want.DefaultAttachStartDir = "/tmp/haoma-test-attach"

	if err := Create(path, "pw", want, fastParams); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, _, err := Open(path, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("payload drift:\n got  %+v\n want %+v", got, want)
	}
}

func TestSave_OverwritesAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.enc")
	first := mintFresh(t)
	if err := Create(path, "pw", first, fastParams); err != nil {
		t.Fatal(err)
	}
	second := mintFresh(t)
	second.HaomadURL = "http://127.0.0.1:9999"
	if err := Save(path, "pw", second, fastParams); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, _, err := Open(path, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, second) {
		t.Errorf("save did not persist; got %+v want %+v", got, second)
	}

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file leaked: %s", e.Name())
		}
	}
}
