package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"haoma-frontend/internal/vault"
)

var fastParams = vault.KDFParams{
	Time:    1,
	Memory:  8 * 1024,
	Threads: 1,
	KeyLen:  32,
}

var (
	haomaVaultBinOnce sync.Once
	haomaVaultBinPath string
	haomaVaultBinErr  error
)

func haomaVaultBin(t *testing.T) string {
	t.Helper()
	haomaVaultBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "haoma-vault-bin-*")
		if err != nil {
			haomaVaultBinErr = err
			return
		}
		out := filepath.Join(dir, "haoma-vault")
		cmd := exec.Command("go", "build", "-o", out, "../haoma-vault")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			haomaVaultBinErr = err
			return
		}
		haomaVaultBinPath = out
	})
	if haomaVaultBinErr != nil {
		t.Fatalf("build haoma-vault: %v", haomaVaultBinErr)
	}
	return haomaVaultBinPath
}

func newTestController(t *testing.T) *vaultController {
	t.Helper()
	bin := haomaVaultBin(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod %s: %v", dir, err)
	}
	path := filepath.Join(dir, "vault.enc")
	payload, err := vault.MintFreshPayload()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	const pw = "test-pass"
	if err := vault.Create(path, pw, payload, fastParams); err != nil {
		t.Fatalf("create: %v", err)
	}
	return newVaultController(path, pw, payload, fastParams, bin)
}

func TestApplyThreatPreset_Domestic(t *testing.T) {
	vc := newTestController(t)
	if err := vc.ApplyThreatPreset(vault.PresetDomestic); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got := vc.snapshot()
	want := vault.ThreatPresetBundles[vault.PresetDomestic]
	if got.ThreatProfile != vault.PresetDomestic {
		t.Errorf("ThreatProfile = %q, want %q", got.ThreatProfile, vault.PresetDomestic)
	}
	if got.IdleAction != want.IdleAction {
		t.Errorf("IdleAction = %q, want %q", got.IdleAction, want.IdleAction)
	}
	if got.IdleTimeoutSeconds != want.IdleTimeoutSeconds {
		t.Errorf("IdleTimeoutSeconds = %d, want %d", got.IdleTimeoutSeconds, want.IdleTimeoutSeconds)
	}
	if got.PinValiditySec != want.PinValiditySec {
		t.Errorf("PinValiditySec = %d, want %d", got.PinValiditySec, want.PinValiditySec)
	}
	if got.PanicAction != want.PanicAction {
		t.Errorf("PanicAction = %q, want %q", got.PanicAction, want.PanicAction)
	}
}

func TestApplyThreatPreset_Privacy(t *testing.T) {
	vc := newTestController(t)
	if err := vc.ApplyThreatPreset(vault.PresetPrivacy); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got := vc.snapshot()
	want := vault.ThreatPresetBundles[vault.PresetPrivacy]
	if got.ThreatProfile != vault.PresetPrivacy {
		t.Errorf("ThreatProfile = %q, want %q", got.ThreatProfile, vault.PresetPrivacy)
	}
	if got.IdleAction != want.IdleAction || got.IdleTimeoutSeconds != want.IdleTimeoutSeconds ||
		got.PinValiditySec != want.PinValiditySec || got.PanicAction != want.PanicAction {
		t.Errorf("bundle drift:\n got  %+v\n want %+v", got, want)
	}
}

func TestApplyThreatPreset_PersistsAcrossReopen(t *testing.T) {
	vc := newTestController(t)
	if err := vc.ApplyThreatPreset(vault.PresetPrivacy); err != nil {
		t.Fatalf("apply: %v", err)
	}
	reopened, _, err := vault.Open(vc.path, vc.passphrase)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.ThreatProfile != vault.PresetPrivacy {
		t.Errorf("disk ThreatProfile = %q, want %q", reopened.ThreatProfile, vault.PresetPrivacy)
	}
	want := vault.ThreatPresetBundles[vault.PresetPrivacy]
	if reopened.IdleTimeoutSeconds != want.IdleTimeoutSeconds {
		t.Errorf("disk IdleTimeoutSeconds = %d, want %d", reopened.IdleTimeoutSeconds, want.IdleTimeoutSeconds)
	}
	if reopened.PanicAction != want.PanicAction {
		t.Errorf("disk PanicAction = %q, want %q", reopened.PanicAction, want.PanicAction)
	}
}

func TestApplyThreatPreset_Activist_NotImplemented(t *testing.T) {
	vc := newTestController(t)
	before := vc.snapshot()
	err := vc.ApplyThreatPreset(vault.PresetActivist)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("error = %v, want substring 'not yet implemented'", err)
	}
	after := vc.snapshot()
	if after.ThreatProfile != before.ThreatProfile ||
		after.IdleAction != before.IdleAction ||
		after.IdleTimeoutSeconds != before.IdleTimeoutSeconds ||
		after.PinValiditySec != before.PinValiditySec ||
		after.PanicAction != before.PanicAction {
		t.Error("activist failure mutated payload — should be no-op")
	}
}

func TestApplyThreatPreset_Invalid(t *testing.T) {
	vc := newTestController(t)
	before := vc.snapshot()
	err := vc.ApplyThreatPreset("nonsense")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	after := vc.snapshot()
	if after.ThreatProfile != before.ThreatProfile ||
		after.IdleAction != before.IdleAction ||
		after.IdleTimeoutSeconds != before.IdleTimeoutSeconds ||
		after.PinValiditySec != before.PinValiditySec ||
		after.PanicAction != before.PanicAction {
		t.Error("invalid preset mutated payload — should be no-op")
	}
}

func TestChangePassphrase_RotatesAndPersists(t *testing.T) {
	vc := newTestController(t)
	const newPass = "new-strong-pass"
	if err := vc.ChangePassphrase("test-pass", newPass); err != nil {
		t.Fatalf("change: %v", err)
	}

	if vc.passphrase != newPass {
		t.Errorf("vc.passphrase = %q, want %q", vc.passphrase, newPass)
	}

	if _, _, err := vault.Open(vc.path, "test-pass"); err == nil {
		t.Error("vault should NOT open under old passphrase")
	}
	if _, _, err := vault.Open(vc.path, newPass); err != nil {
		t.Errorf("vault must open under new passphrase: %v", err)
	}
}

func TestChangePassphrase_RejectsWrongOld(t *testing.T) {
	vc := newTestController(t)
	err := vc.ChangePassphrase("not-the-current-pass", "anything")
	if err == nil {
		t.Fatal("expected error for wrong old passphrase")
	}

	if vc.passphrase != "test-pass" {
		t.Errorf("vc.passphrase = %q, want unchanged", vc.passphrase)
	}
}

func TestSetTorPassword_PersistsToDisk(t *testing.T) {
	vc := newTestController(t)
	if err := vc.SetTorPassword("hunter2"); err != nil {
		t.Fatalf("set tor pw: %v", err)
	}
	reopened, _, err := vault.Open(vc.path, vc.passphrase)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.TorPassword != "hunter2" {
		t.Errorf("disk TorPassword = %q, want hunter2", reopened.TorPassword)
	}

	if _, err := os.Stat(vc.path + ".1"); err != nil {
		t.Errorf("expected .1 backup after reseal: %v", err)
	}
}

func TestMutate_BatchedSaveOneReseal(t *testing.T) {
	vc := newTestController(t)

	err := vc.Mutate("lock", func(p *vault.Payload) error {
		p.IdleAction = "soft-lock"
		p.IdleTimeoutSeconds = 600
		p.PinValiditySec = 300
		p.PanicAction = "hard-lock"
		return nil
	})
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	got := vc.snapshot()
	if got.IdleAction != "soft-lock" || got.IdleTimeoutSeconds != 600 ||
		got.PinValiditySec != 300 || got.PanicAction != "hard-lock" {
		t.Errorf("after Mutate: %+v", got)
	}

	reopened, _, err := vault.Open(vc.path, vc.passphrase)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.IdleAction != "soft-lock" || reopened.IdleTimeoutSeconds != 600 {
		t.Errorf("disk after Mutate: %+v", reopened)
	}

	for _, n := range []int{1} {
		if _, err := os.Stat(vc.path + "." + itoa(n)); err != nil {
			t.Errorf("expected .%d backup: %v", n, err)
		}
	}
	if _, err := os.Stat(vc.path + ".2"); err == nil {
		t.Errorf("did NOT expect .2 backup after a single Mutate (means we re-sealed multiple times)")
	}
}

func TestMutate_TransformErrorRevertsState(t *testing.T) {
	vc := newTestController(t)
	before := vc.snapshot()
	want := errors.New("nope")
	err := vc.Mutate("test", func(p *vault.Payload) error {
		p.IdleAction = "soft-lock"
		return want
	})
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
	if vc.snapshot().IdleAction != before.IdleAction {
		t.Error("transform error must revert in-memory state")
	}
}

func TestMutate_RevertsOnValidationFailure(t *testing.T) {
	vc := newTestController(t)
	before := vc.snapshot()
	err := vc.Mutate("bad-enum", func(p *vault.Payload) error {
		p.IdleAction = "panic-lock"
		return nil
	})
	if err == nil {
		t.Fatal("expected error from invalid enum")
	}
	if vc.snapshot().IdleAction != before.IdleAction {
		t.Errorf("validation failure must revert state: got %q, want %q",
			vc.snapshot().IdleAction, before.IdleAction)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func TestSetThreatProfile_NewVocabulary(t *testing.T) {
	vc := newTestController(t)
	for _, ok := range []string{"", vault.PresetDomestic, vault.PresetPrivacy, vault.PresetActivist} {
		if err := vc.SetThreatProfile(ok); err != nil {
			t.Errorf("SetThreatProfile(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"casual", "investigator", "international", "custom", "DOMESTIC"} {
		if err := vc.SetThreatProfile(bad); err == nil {
			t.Errorf("SetThreatProfile(%q) = nil, want error (old vocab should be rejected)", bad)
		}
	}
}
