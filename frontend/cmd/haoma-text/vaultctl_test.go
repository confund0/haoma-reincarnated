package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
	if err := vault.Create(path, []byte(pw), payload, fastParams); err != nil {
		t.Fatalf("create: %v", err)
	}
	return newVaultController(path, []byte(pw), payload, fastParams, bin)
}

func presetVaultCells(p vault.Payload) vault.ThreatPresetBundle {
	return vault.ThreatPresetBundle{
		ShowLoginForm:          p.ShowLoginForm,
		SayPassphraseIsDefault: p.SayPassphraseIsDefault,

		IdleAction:         p.IdleAction,
		IdleTimeoutSeconds: p.IdleTimeoutSeconds,
		PinValiditySec:     p.PinValiditySec,
		PanicAction:        p.PanicAction,

		NotifyShellEnabled:     p.NotifyShellEnabled,
		NotifyShowSender:       p.NotifyShowSender,
		NotifyShowBody:         p.NotifyShowBody,
		NotifyDisguiseEnabled:  p.NotifyDisguiseEnabled,
		NotifyNoisy:            p.NotifyNoisy,
		NotifyPersistUntilOpen: p.NotifyPersistUntilOpen,
		NotifyDeepLink:         p.NotifyDeepLink,
		NotificationsOnLock:    p.NotificationsOnLock,

		ShareTargetEnabled:        p.ShareTargetEnabled,
		ShowFailedDestroyAttempts: p.ShowFailedDestroyAttempts,
	}
}

func wantVaultCells(b vault.ThreatPresetBundle) vault.ThreatPresetBundle {
	b.DefaultRetentionSec = 0
	b.BypassSlotEnabled = false
	return b
}

func TestApplyThreatPreset_EveryTierWritesEveryVaultCell(t *testing.T) {
	for _, preset := range []string{
		vault.PresetNobody,
		vault.PresetDomestic,
		vault.PresetPower,
		vault.PresetForce,
	} {
		t.Run(preset, func(t *testing.T) {
			vc := newTestController(t)
			if err := vc.ApplyThreatPreset(preset); err != nil {
				t.Fatalf("apply: %v", err)
			}
			got := vc.snapshot()
			if got.ThreatProfile != preset {
				t.Errorf("ThreatProfile = %q, want %q", got.ThreatProfile, preset)
			}
			want := wantVaultCells(vault.ThreatPresetBundles[preset])
			if diff := presetVaultCells(got); diff != want {
				t.Errorf("vault cells differ from the bundle:\n got  %+v\n want %+v", diff, want)
			}
		})
	}
}

func TestThreatPresetBundles_TiersDiffer(t *testing.T) {
	seen := map[vault.ThreatPresetBundle]string{}
	for _, preset := range []string{
		vault.PresetNobody,
		vault.PresetDomestic,
		vault.PresetPower,
		vault.PresetForce,
	} {
		b, ok := vault.ThreatPresetBundles[preset]
		if !ok {
			t.Fatalf("%s has no bundle", preset)
		}
		if other, dup := seen[b]; dup {
			t.Errorf("%s and %s hold the same bundle", preset, other)
		}
		seen[b] = preset
		if b.DefaultRetentionSec == 0 {
			t.Errorf("%s keeps messages for ever — no tier may", preset)
		}
		if b.NotifyShowBody {
			t.Errorf("%s shows the message body — no tier may", preset)
		}
	}
}

func TestThreatPresetBundles_MatchTheMatrix(t *testing.T) {
	want := map[string]vault.ThreatPresetBundle{
		vault.PresetNobody: {
			ShowLoginForm: false, SayPassphraseIsDefault: false, BypassSlotEnabled: true,
			IdleAction: "soft-lock", IdleTimeoutSeconds: 900, PinValiditySec: 0, PanicAction: "safe-lock",
			DefaultRetentionSec: 28 * 24 * 3600,
			NotifyShellEnabled:  true, NotifyShowSender: true, NotifyShowBody: false,
			NotifyDisguiseEnabled: false, NotifyNoisy: true, NotifyPersistUntilOpen: true,
			NotifyDeepLink: true, NotificationsOnLock: true,
			ShareTargetEnabled: true, ShowFailedDestroyAttempts: true,
		},
		vault.PresetDomestic: {
			ShowLoginForm: true, SayPassphraseIsDefault: true, BypassSlotEnabled: true,
			IdleAction: "soft-lock", IdleTimeoutSeconds: 300, PinValiditySec: 86400, PanicAction: "safe-lock",
			DefaultRetentionSec: 7 * 24 * 3600,
			NotifyShellEnabled:  true, NotifyShowSender: false, NotifyShowBody: false,
			NotifyDisguiseEnabled: true, NotifyNoisy: false, NotifyPersistUntilOpen: true,
			NotifyDeepLink: false, NotificationsOnLock: true,
			ShareTargetEnabled: false, ShowFailedDestroyAttempts: false,
		},
		vault.PresetPower: {
			ShowLoginForm: true, SayPassphraseIsDefault: true, BypassSlotEnabled: false,
			IdleAction: "safe-lock", IdleTimeoutSeconds: 60, PinValiditySec: 300, PanicAction: "hard-lock",
			DefaultRetentionSec: 24 * 3600,
			NotifyShellEnabled:  true, NotifyShowSender: false, NotifyShowBody: false,
			NotifyDisguiseEnabled: true, NotifyNoisy: false, NotifyPersistUntilOpen: false,
			NotifyDeepLink: false, NotificationsOnLock: false,
			ShareTargetEnabled: false, ShowFailedDestroyAttempts: false,
		},
		vault.PresetForce: {
			ShowLoginForm: true, SayPassphraseIsDefault: true, BypassSlotEnabled: false,
			IdleAction: "hard-lock", IdleTimeoutSeconds: 30, PinValiditySec: 0, PanicAction: "self-destruct",
			DefaultRetentionSec: 3600,
			NotifyShellEnabled:  false, NotifyShowSender: false, NotifyShowBody: false,
			NotifyDisguiseEnabled: true, NotifyNoisy: false, NotifyPersistUntilOpen: false,
			NotifyDeepLink: false, NotificationsOnLock: false,
			ShareTargetEnabled: false, ShowFailedDestroyAttempts: false,
		},
	}
	if len(vault.ThreatPresetBundles) != len(want) {
		t.Fatalf("bundle count = %d, want %d", len(vault.ThreatPresetBundles), len(want))
	}
	for preset, w := range want {
		if got := vault.ThreatPresetBundles[preset]; got != w {
			t.Errorf("%s bundle:\n got  %+v\n want %+v", preset, got, w)
		}
	}
}

func TestApplyThreatPreset_NobodyCarriesTheOldBaseline(t *testing.T) {
	vc := newTestController(t)
	if err := vc.ApplyThreatPreset(vault.PresetNobody); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got := vc.snapshot()
	if !got.ShareTargetEnabled {
		t.Error("nobody must offer the share sheet — that is the baseline it took over")
	}
	if !got.ShowFailedDestroyAttempts {
		t.Error("nobody must show failed destroy attempts")
	}
	if got.ShowLoginForm {
		t.Error("nobody must skip the login form")
	}
	if got.SayPassphraseIsDefault {
		t.Error("nobody must not say the passphrase is the default")
	}
}

func TestApplyThreatPreset_ShareTargetSurvivesWhenTheTierSaysOn(t *testing.T) {
	vc := newTestController(t)
	if err := vc.Mutate("share", func(p *vault.Payload) error {
		p.ShareTargetEnabled = true
		return nil
	}); err != nil {
		t.Fatalf("seed share target: %v", err)
	}
	if err := vc.ApplyThreatPreset(vault.PresetNobody); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !vc.snapshot().ShareTargetEnabled {
		t.Error("nobody force-cleared the share target — it is an ordinary cell now")
	}
}

func TestApplyThreatPreset_PersistsAcrossReopen(t *testing.T) {
	vc := newTestController(t)
	if err := vc.ApplyThreatPreset(vault.PresetForce); err != nil {
		t.Fatalf("apply: %v", err)
	}
	reopened, _, err := vault.Open(vc.path, vc.passphrase)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.ThreatProfile != vault.PresetForce {
		t.Errorf("disk ThreatProfile = %q, want %q", reopened.ThreatProfile, vault.PresetForce)
	}
	want := wantVaultCells(vault.ThreatPresetBundles[vault.PresetForce])
	if got := presetVaultCells(reopened); got != want {
		t.Errorf("disk cells differ from the bundle:\n got  %+v\n want %+v", got, want)
	}
}

func TestApplyThreatPreset_RetiredNamesRejected(t *testing.T) {
	vc := newTestController(t)
	for _, retired := range []string{"minimal", "privacy", "activist"} {
		before := vc.snapshot()
		if err := vc.ApplyThreatPreset(retired); err == nil {
			t.Errorf("ApplyThreatPreset(%q) = nil, want an error", retired)
		}
		if after := vc.snapshot(); after.ThreatProfile != before.ThreatProfile {
			t.Errorf("%q mutated the payload — a rejected preset writes nothing", retired)
		}
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
	newPass := []byte("new-strong-pass")
	if err := vc.ChangePassphrase([]byte("test-pass"), newPass); err != nil {
		t.Fatalf("change: %v", err)
	}

	if !bytes.Equal(vc.passphrase, newPass) {
		t.Errorf("vc.passphrase = %q, want %q", vc.passphrase, newPass)
	}

	if _, _, err := vault.Open(vc.path, []byte("test-pass")); err == nil {
		t.Error("vault should NOT open under old passphrase")
	}
	if _, _, err := vault.Open(vc.path, newPass); err != nil {
		t.Errorf("vault must open under new passphrase: %v", err)
	}
}

func TestChangePassphrase_RejectsWrongOld(t *testing.T) {
	vc := newTestController(t)
	err := vc.ChangePassphrase([]byte("not-the-current-pass"), []byte("anything"))
	if err == nil {
		t.Fatal("expected error for wrong old passphrase")
	}

	if !bytes.Equal(vc.passphrase, []byte("test-pass")) {
		t.Errorf("vc.passphrase = %q, want unchanged", vc.passphrase)
	}
}

func TestVaultController_Wipe_ZerosPassphraseAndIsIdempotent(t *testing.T) {
	vc := newTestController(t)

	backing := vc.passphrase
	vc.Zero()
	if vc.passphrase != nil {
		t.Errorf("Wipe should nil the slice header; got %v", vc.passphrase)
	}
	for i, b := range backing {
		if b != 0 {
			t.Errorf("backing[%d]=%d, want 0 after Wipe", i, b)
		}
	}

	vc.Zero()
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
	for _, ok := range []string{
		"", vault.PresetNobody, vault.PresetDomestic, vault.PresetPower, vault.PresetForce,
	} {
		if err := vc.SetThreatProfile(ok); err != nil {
			t.Errorf("SetThreatProfile(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{
		"minimal", "privacy", "activist", "casual", "investigator", "custom", "DOMESTIC",
	} {
		if err := vc.SetThreatProfile(bad); err == nil {
			t.Errorf("SetThreatProfile(%q) = nil, want an error — that name is retired", bad)
		}
	}
}

func TestEmptyThreatProfile_IsNotATier(t *testing.T) {
	if _, ok := vault.ThreatPresetBundles[""]; ok {
		t.Error("the empty profile has a bundle — it means the picker has not run, not a tier")
	}
	vc := newTestController(t)
	if err := vc.ApplyThreatPreset(""); err == nil {
		t.Error("ApplyThreatPreset(\"\") = nil, want an error")
	}

	if err := vc.ApplyThreatPreset(vault.PresetForce); err != nil {
		t.Fatalf("apply force: %v", err)
	}
	before := vc.snapshot()
	if err := vc.SetThreatProfile(""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	after := vc.snapshot()
	if after.ThreatProfile != "" {
		t.Errorf("ThreatProfile = %q, want empty", after.ThreatProfile)
	}
	before.ThreatProfile = ""
	if presetVaultCells(after) != presetVaultCells(before) {
		t.Error("clearing the label changed a setting — it must only clear the label")
	}
}

func TestMintFreshPayload_SeedsTheUnlockBools(t *testing.T) {
	p, err := vault.MintFreshPayload()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if p.ThreatProfile != "" {
		t.Errorf("ThreatProfile = %q, want empty — the picker has not run", p.ThreatProfile)
	}
	if !p.ShowLoginForm {
		t.Error("ShowLoginForm = false on a fresh vault — the login form must be shown")
	}
	if !p.SayPassphraseIsDefault {
		t.Error("SayPassphraseIsDefault = false on a fresh vault — the notice must be said")
	}
}

func TestSaysPassphraseIsDefault_OnlyTheLowestTierStaysQuiet(t *testing.T) {
	for _, tc := range []struct {
		preset string
		want   bool
	}{
		{vault.PresetNobody, false},
		{vault.PresetDomestic, true},
		{vault.PresetPower, true},
		{vault.PresetForce, true},
	} {
		t.Run(tc.preset, func(t *testing.T) {
			vc := newTestController(t)
			if err := vc.ApplyThreatPreset(tc.preset); err != nil {
				t.Fatalf("apply: %v", err)
			}
			if got := vc.SaysPassphraseIsDefault(); got != tc.want {
				t.Errorf("SaysPassphraseIsDefault() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSaysPassphraseIsDefault_AnOldVaultAtTheLowestTierStaysQuiet(t *testing.T) {
	vc := newTestController(t)
	if err := vc.SetThreatProfile(vault.PresetNobody); err != nil {
		t.Fatalf("set profile: %v", err)
	}

	vc.mu.Lock()
	vc.payload.SayPassphraseIsDefault = vault.DefaultSayPassphraseIsDefault
	vc.mu.Unlock()

	if vc.SaysPassphraseIsDefault() {
		t.Error("an upgraded nobody device must stay quiet without re-applying its tier")
	}
}

func TestCheckPIN(t *testing.T) {
	vc := newTestController(t)
	const pin = "9137"
	if err := vc.ChangePIN("", pin); err != nil {
		t.Fatalf("set the PIN: %v", err)
	}

	if !vc.CheckPIN(pin) {
		t.Error("the right PIN was refused")
	}
	if vc.CheckPIN("9138") {
		t.Error("a wrong PIN was accepted")
	}
	if vc.CheckPIN("") {
		t.Error("an empty answer was accepted")
	}
	if vc.CheckPIN(pin + " ") {
		t.Error("a PIN with a trailing space was accepted")
	}
}

func TestCheckPIN_RefusesWhenNoPINIsSet(t *testing.T) {
	vc := newTestController(t)
	vc.payload.PIN = ""

	if vc.CheckPIN("") {
		t.Error("an empty answer was accepted on a device with no PIN")
	}
	if vc.CheckPIN("0000") {
		t.Error("a guess was accepted on a device with no PIN")
	}
}
