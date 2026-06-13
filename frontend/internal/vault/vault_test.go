package vault

import (
	"bytes"
	"encoding/json"
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

	if err := Create(path, []byte("correct horse battery staple"), want, fastParams); err != nil {
		t.Fatalf("create: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != fileMode {
		t.Errorf("vault perms = %o, want %o", perm, fileMode)
	}

	got, gotParams, err := Open(path, []byte("correct horse battery staple"))
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
	if err := Create(path, []byte("right"), mintFresh(t), fastParams); err != nil {
		t.Fatalf("create: %v", err)
	}
	_, _, err := Open(path, []byte("wrong"))
	if !errors.Is(err, ErrUnseal) {
		t.Fatalf("expected ErrUnseal, got %v", err)
	}
}

func TestOpen_TamperedCiphertextFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.enc")
	if err := Create(path, []byte("pw"), mintFresh(t), fastParams); err != nil {
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
	_, _, err = Open(path, []byte("pw"))
	if !errors.Is(err, ErrUnseal) {
		t.Fatalf("expected ErrUnseal on tampered ciphertext, got %v", err)
	}
}

func TestOpen_TamperedAADFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.enc")
	if err := Create(path, []byte("pw"), mintFresh(t), fastParams); err != nil {
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
	_, _, err = Open(path, []byte("pw"))

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
	_, _, err := Open(path, []byte("x"))
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
	_, _, err := Open(path, []byte("x"))
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
	_, _, err := Open(path, []byte("x"))
	if !errors.Is(err, ErrEmpty) {
		t.Fatalf("expected ErrEmpty, got %v", err)
	}
}

func TestCreate_RefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.enc")
	if err := Create(path, []byte("a"), mintFresh(t), fastParams); err != nil {
		t.Fatal(err)
	}
	err := Create(path, []byte("b"), mintFresh(t), fastParams)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected refuse-overwrite, got %v", err)
	}
}

func TestChangePassphrase_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.enc")
	want := mintFresh(t)
	if err := Create(path, []byte("old"), want, fastParams); err != nil {
		t.Fatal(err)
	}
	if err := ChangePassphrase(path, []byte("old"), []byte("new")); err != nil {
		t.Fatalf("change: %v", err)
	}
	if _, _, err := Open(path, []byte("old")); !errors.Is(err, ErrUnseal) {
		t.Errorf("old passphrase should fail post-change, got %v", err)
	}
	got, _, err := Open(path, []byte("new"))
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
	if err := Create(path, []byte("right"), mintFresh(t), fastParams); err != nil {
		t.Fatal(err)
	}
	err := ChangePassphrase(path, []byte("wrong"), []byte("new"))
	if !errors.Is(err, ErrUnseal) {
		t.Fatalf("expected ErrUnseal, got %v", err)
	}
}

func TestOpen_RefusesEmptyPassphrase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.enc")
	if err := Create(path, []byte("real"), mintFresh(t), fastParams); err != nil {
		t.Fatalf("create: %v", err)
	}
	_, _, err := Open(path, []byte(""))
	if !errors.Is(err, ErrEmptyPassphrase) {
		t.Fatalf("expected ErrEmptyPassphrase, got %v", err)
	}
}

func TestSave_RefusesEmptyPassphrase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.enc")
	if err := Create(path, []byte("real"), mintFresh(t), fastParams); err != nil {
		t.Fatalf("create: %v", err)
	}
	err := Save(path, []byte(""), mintFresh(t), fastParams)
	if !errors.Is(err, ErrEmptyPassphrase) {
		t.Fatalf("expected ErrEmptyPassphrase, got %v", err)
	}
}

func TestCreate_RefusesEmptyPassphrase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.enc")
	err := Create(path, []byte(""), mintFresh(t), fastParams)
	if !errors.Is(err, ErrEmptyPassphrase) {
		t.Fatalf("expected ErrEmptyPassphrase, got %v", err)
	}
}

func TestChangePassphrase_RefusesEmptyOld(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.enc")
	if err := Create(path, []byte("real"), mintFresh(t), fastParams); err != nil {
		t.Fatalf("create: %v", err)
	}
	err := ChangePassphrase(path, []byte(""), []byte("newpass"))
	if !errors.Is(err, ErrEmptyPassphrase) {
		t.Fatalf("expected ErrEmptyPassphrase, got %v", err)
	}
}

func TestChangePassphrase_RefusesEmptyNew(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.enc")
	if err := Create(path, []byte("real"), mintFresh(t), fastParams); err != nil {
		t.Fatalf("create: %v", err)
	}
	err := ChangePassphrase(path, []byte("real"), []byte(""))
	if !errors.Is(err, ErrEmptyPassphrase) {
		t.Fatalf("expected ErrEmptyPassphrase, got %v", err)
	}
}

func TestWriteSealedVault_RefusesNonMagic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.enc")

	if err := Create(path, []byte("real"), mintFresh(t), fastParams); err != nil {
		t.Fatalf("create: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pre: %v", err)
	}

	if err := writeSealedVault(path, []byte("this is plaintext JSON, not a sealed vault")); err == nil {
		t.Fatal("expected refusal, got nil")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read post: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("vault.enc was overwritten despite guard")
	}
}

func TestWriteSealedVault_RefusesShorterThanMagic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.enc")
	if err := writeSealedVault(path, []byte("short")); err == nil {
		t.Fatal("expected refusal for sub-magic-length bytes")
	}
}

func TestWriteSealedVault_AcceptsValidHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.enc")

	bogus := append(magic[:], make([]byte, 64)...)
	if err := writeSealedVault(path, bogus); err != nil {
		t.Fatalf("magic-prefixed bytes should pass guard, got %v", err)
	}
}

func TestCreateInsecure_OpensWithDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.enc")
	want := mintFresh(t)
	if err := CreateInsecure(path, want, fastParams); err != nil {
		t.Fatalf("create insecure: %v", err)
	}
	got, _, err := Open(path, []byte(InsecureDefaultPassphrase))
	if err != nil {
		t.Fatalf("open with default: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("payload drift")
	}
}

func TestIsInsecureDefaultPassphrase(t *testing.T) {
	if !IsInsecureDefaultPassphrase([]byte(InsecureDefaultPassphrase)) {
		t.Error("constant should match itself")
	}
	if IsInsecureDefaultPassphrase([]byte("anything-else")) {
		t.Error("non-default flagged as insecure")
	}
	if IsInsecureDefaultPassphrase([]byte("")) {
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

	if IsInsecureDefaultPassphrase([]byte(InsecureDefaultPIN)) {
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
	if !p.URLForceChooser {
		t.Error("URLForceChooser should default true (paranoid posture — force the app picker on URL taps)")
	}
}

func TestPayload_DefaultTrueBoolsSurviveOldVaults(t *testing.T) {

	oldVaultJSON := []byte(`{
		"haomad_store_passphrase":   "a",
		"frontend_store_passphrase": "b",
		"haomad_token":              "c"
	}`)
	p := defaultSeededPayload()
	if err := json.Unmarshal(oldVaultJSON, &p); err != nil {
		t.Fatalf("decode old vault: %v", err)
	}
	if !p.NotificationsOnLock {
		t.Error("NotificationsOnLock should remain true on old-vault upgrade " +
			"(absent key) — defaultSeededPayload seeds it before decode")
	}
	if !p.DefaultSendReceipts {
		t.Error("DefaultSendReceipts should remain true on old-vault upgrade " +
			"(absent key)")
	}
	if !p.NotifyShellEnabled {
		t.Error("NotifyShellEnabled should remain true on old-vault upgrade " +
			"(absent key) — also requires the tag to drop omitempty so " +
			"user-set-false survives save")
	}
	if !p.URLForceChooser {
		t.Error("URLForceChooser should remain true on old-vault upgrade " +
			"(absent key)")
	}

	userOptOuts := []byte(`{
		"haomad_store_passphrase":   "a",
		"frontend_store_passphrase": "b",
		"haomad_token":              "c",
		"notifications_on_lock":     false,
		"default_send_receipts":     false,
		"notify_shell_enabled":      false,
		"url_force_chooser":         false
	}`)
	p2 := defaultSeededPayload()
	if err := json.Unmarshal(userOptOuts, &p2); err != nil {
		t.Fatalf("decode opt-outs: %v", err)
	}
	if p2.NotificationsOnLock {
		t.Error("notifications_on_lock=false in JSON must overwrite seed")
	}
	if p2.DefaultSendReceipts {
		t.Error("default_send_receipts=false in JSON must overwrite seed")
	}
	if p2.NotifyShellEnabled {
		t.Error("notify_shell_enabled=false in JSON must overwrite seed")
	}
	if p2.URLForceChooser {
		t.Error("url_force_chooser=false in JSON must overwrite seed")
	}
}

func TestPayload_StrictDecodeAcceptsAllMobileKeys(t *testing.T) {
	mobilePayload := `{
		"haomad_store_passphrase":   "p1",
		"frontend_store_passphrase": "p2",
		"haomad_token":              "t1",
		"notify_shell_enabled":      true,
		"notify_show_sender":        false,
		"notify_show_body":          false,
		"notifications_on_lock":     true,
		"notify_disguise_enabled":   false,
		"notify_noisy":              false,
		"notify_persist_until_open": false,
		"notify_deep_link":          false,
		"default_retention_sec":     0,
		"default_send_receipts":     true,
		"idle_action":               "safe-lock",
		"idle_timeout_seconds":      900,
		"pin_validity_sec":          0,
		"panic_action":              "hard-lock",
		"threat_profile":            "privacy",
		"url_force_chooser":         true,
		"mobile_notice_snooze":      {"passphrase_is_default": {"until": 1733600000000, "step": 2}}
	}`

	dec := json.NewDecoder(bytes.NewReader([]byte(mobilePayload)))
	dec.DisallowUnknownFields()
	var p Payload
	if err := dec.Decode(&p); err != nil {
		t.Fatalf("strict decode rejected a mobile-written key — "+
			"the Kotlin side probably added a JSON key without a matching "+
			"vault.Payload struct field: %v", err)
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
	want.NotifyPersistUntilOpen = true
	want.NotifyDeepLink = true

	want.DefaultSaveDir = "/tmp/haoma-test-saves"
	want.DefaultAttachStartDir = "/tmp/haoma-test-attach"

	want.URLForceChooser = false

	want.MobileNoticeSnooze = map[string]NoticeSnoozeState{
		"passphrase_is_default": {Until: 1733600000000, Step: 2},
	}

	if err := Create(path, []byte("pw"), want, fastParams); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, _, err := Open(path, []byte("pw"))
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
	if err := Create(path, []byte("pw"), first, fastParams); err != nil {
		t.Fatal(err)
	}
	second := mintFresh(t)
	second.HaomadURL = "http://127.0.0.1:9999"
	if err := Save(path, []byte("pw"), second, fastParams); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, _, err := Open(path, []byte("pw"))
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
