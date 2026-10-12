package main

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/vault"
)

func validateDirPath(p string) error {
	info, err := os.Stat(p)
	if err != nil {
		return fmt.Errorf("stat %s: %w", p, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", p)
	}
	return nil
}

type vaultController struct {
	mu            sync.Mutex
	payload       vault.Payload
	passphrase    []byte
	params        vault.KDFParams
	path          string
	haomaVaultBin string
}

func newVaultController(path string, passphrase []byte, payload vault.Payload, params vault.KDFParams, haomaVaultBin string) *vaultController {
	return &vaultController{
		path:          path,
		passphrase:    passphrase,
		payload:       payload,
		params:        params,
		haomaVaultBin: haomaVaultBin,
	}
}

func (vc *vaultController) Zero() {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	if len(vc.passphrase) == 0 {
		return
	}
	slog.Debug("vault: zero master passphrase")
	clear(vc.passphrase)
	vc.passphrase = nil
}

func (vc *vaultController) snapshot() vault.Payload {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	return vc.payload
}

func (vc *vaultController) resealLocked() error {
	return vc.resealWithLocked(vc.passphrase)
}

func (vc *vaultController) resealWithLocked(passphrase []byte) error {
	if err := vc.payload.Validate(); err != nil {
		return fmt.Errorf("validate: %w", err)
	}
	jsonPayload, err := json.Marshal(vc.payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	stdin := make([]byte, 0, len(passphrase)+1+len(jsonPayload))
	stdin = append(stdin, passphrase...)
	stdin = append(stdin, '\n')
	stdin = append(stdin, jsonPayload...)
	defer clear(stdin)
	defer clear(jsonPayload)

	cfgDir := filepath.Dir(vc.path)
	bin := vc.haomaVaultBin
	if bin == "" {
		bin = "haoma-vault"
	}
	cmd := exec.Command(bin, "--cfg-dir", cfgDir, "-w")
	cmd.Stdin = bytes.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	slog.Debug("vault: reseal initiated",
		slog.String("bin", bin),
		slog.Bool("empty", len(passphrase) == 0),
		slog.Int("payload_bytes", len(jsonPayload)),
	)
	start := time.Now()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("haoma-vault -w: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}

	if tail := strings.TrimSpace(stderr.String()); tail != "" {
		slog.Debug("vault: subprocess stderr", slog.String("tail", tail))
	}
	slog.Info("vault: reseal ok", slog.Duration("dur", time.Since(start)))
	return nil
}

func (vc *vaultController) Mutate(label string, transform func(*vault.Payload) error) error {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	before := vc.payload
	if err := transform(&vc.payload); err != nil {
		vc.payload = before
		return err
	}
	if err := vc.resealLocked(); err != nil {
		vc.payload = before
		return fmt.Errorf("re-seal: %w", err)
	}
	slog.Info("vault: mutate ok", slog.String("op", label))
	return nil
}

func (vc *vaultController) ChangePassphrase(old, newPass []byte) error {
	if len(newPass) == 0 {
		return errors.New("new passphrase cannot be empty (would brick the vault)")
	}
	vc.mu.Lock()
	defer vc.mu.Unlock()
	if subtle.ConstantTimeCompare(old, vc.passphrase) != 1 {
		return errors.New("current passphrase incorrect")
	}
	if err := vc.resealWithLocked(newPass); err != nil {
		return fmt.Errorf("re-seal: %w", err)
	}
	clear(vc.passphrase)

	vc.passphrase = append([]byte(nil), newPass...)
	slog.Info("vault: master passphrase changed")
	return nil
}

func (vc *vaultController) ChangePIN(old, newPIN string) error {
	if newPIN == "" {
		return errors.New("new PIN cannot be empty")
	}
	vc.mu.Lock()
	defer vc.mu.Unlock()
	if !vault.IsInsecureDefaultPIN(vc.payload.PIN) &&
		subtle.ConstantTimeCompare([]byte(old), []byte(vc.payload.PIN)) != 1 {
		return errors.New("current PIN incorrect")
	}
	prev := vc.payload.PIN
	vc.payload.PIN = newPIN
	if err := vc.resealLocked(); err != nil {
		vc.payload.PIN = prev
		return fmt.Errorf("re-seal: %w", err)
	}
	slog.Info("vault: PIN changed")
	return nil
}

func (vc *vaultController) CheckPIN(s string) bool {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	if s == "" || vc.payload.PIN == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(s), []byte(vc.payload.PIN)) == 1
}

func (vc *vaultController) SetIdleAction(action string) error {
	switch action {
	case "soft-lock", "safe-lock", "hard-lock":

	default:
		return fmt.Errorf("invalid action %q (want soft-lock, safe-lock or hard-lock)", action)
	}
	vc.mu.Lock()
	defer vc.mu.Unlock()
	prev := vc.payload.IdleAction
	vc.payload.IdleAction = action
	if err := vc.resealLocked(); err != nil {
		vc.payload.IdleAction = prev
		return fmt.Errorf("re-seal: %w", err)
	}
	slog.Info("vault: IdleAction set", slog.String("action", action))
	return nil
}

func (vc *vaultController) SetIdleTimeoutSeconds(n int) error {
	if n <= 0 {
		return errors.New("idle timeout must be positive (use a separate disable path if you really mean off)")
	}
	vc.mu.Lock()
	defer vc.mu.Unlock()
	prev := vc.payload.IdleTimeoutSeconds
	vc.payload.IdleTimeoutSeconds = n
	if err := vc.resealLocked(); err != nil {
		vc.payload.IdleTimeoutSeconds = prev
		return fmt.Errorf("re-seal: %w", err)
	}
	slog.Info("vault: IdleTimeoutSeconds set", slog.Int("seconds", n))
	return nil
}

func (vc *vaultController) SetPinValiditySec(n int) error {
	if n < 0 {
		return errors.New("pin validity must be >= 0")
	}
	vc.mu.Lock()
	defer vc.mu.Unlock()
	prev := vc.payload.PinValiditySec
	vc.payload.PinValiditySec = n
	if err := vc.resealLocked(); err != nil {
		vc.payload.PinValiditySec = prev
		return fmt.Errorf("re-seal: %w", err)
	}
	slog.Info("vault: PinValiditySec set", slog.Int("seconds", n))
	return nil
}

func (vc *vaultController) SetTorPassword(s string) error {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	prev := vc.payload.TorPassword
	vc.payload.TorPassword = s
	if err := vc.resealLocked(); err != nil {
		vc.payload.TorPassword = prev
		return fmt.Errorf("re-seal: %w", err)
	}
	slog.Info("vault: TorPassword updated (haomad restart required to take effect)")
	return nil
}

func (vc *vaultController) IsInsecureDefaultPassphrase() bool {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	return vault.IsInsecureDefaultPassphrase(vc.passphrase)
}

func (vc *vaultController) IsInsecureDefaultPIN() bool {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	return vault.IsInsecureDefaultPIN(vc.payload.PIN)
}

func (vc *vaultController) SaysPassphraseIsDefault() bool {
	vc.mu.Lock()
	defer vc.mu.Unlock()

	if vc.payload.ThreatProfile == vault.PresetNobody {
		return false
	}
	return vc.payload.SayPassphraseIsDefault
}

func (vc *vaultController) SetNotifyShellEnabled(b bool) error {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	prev := vc.payload.NotifyShellEnabled
	vc.payload.NotifyShellEnabled = b
	if err := vc.resealLocked(); err != nil {
		vc.payload.NotifyShellEnabled = prev
		return fmt.Errorf("re-seal: %w", err)
	}
	slog.Info("vault: NotifyShellEnabled set", slog.Bool("on", b))
	return nil
}

func (vc *vaultController) SetNotifyShowSender(b bool) error {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	prev := vc.payload.NotifyShowSender
	vc.payload.NotifyShowSender = b
	if err := vc.resealLocked(); err != nil {
		vc.payload.NotifyShowSender = prev
		return fmt.Errorf("re-seal: %w", err)
	}
	slog.Info("vault: NotifyShowSender set", slog.Bool("on", b))
	return nil
}

func (vc *vaultController) SetNotifyShowBody(b bool) error {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	prev := vc.payload.NotifyShowBody
	vc.payload.NotifyShowBody = b
	if err := vc.resealLocked(); err != nil {
		vc.payload.NotifyShowBody = prev
		return fmt.Errorf("re-seal: %w", err)
	}
	slog.Info("vault: NotifyShowBody set", slog.Bool("on", b))
	return nil
}

func (vc *vaultController) SetNotificationsOnLock(b bool) error {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	prev := vc.payload.NotificationsOnLock
	vc.payload.NotificationsOnLock = b
	if err := vc.resealLocked(); err != nil {
		vc.payload.NotificationsOnLock = prev
		return fmt.Errorf("re-seal: %w", err)
	}
	slog.Info("vault: NotificationsOnLock set", slog.Bool("on", b))
	return nil
}

func (vc *vaultController) SetNotifyPersistUntilOpen(b bool) error {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	prev := vc.payload.NotifyPersistUntilOpen
	vc.payload.NotifyPersistUntilOpen = b
	if err := vc.resealLocked(); err != nil {
		vc.payload.NotifyPersistUntilOpen = prev
		return fmt.Errorf("re-seal: %w", err)
	}
	slog.Info("vault: NotifyPersistUntilOpen set", slog.Bool("on", b))
	return nil
}

func (vc *vaultController) SetNotifyDeepLink(b bool) error {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	prev := vc.payload.NotifyDeepLink
	vc.payload.NotifyDeepLink = b
	if err := vc.resealLocked(); err != nil {
		vc.payload.NotifyDeepLink = prev
		return fmt.Errorf("re-seal: %w", err)
	}
	slog.Info("vault: NotifyDeepLink set", slog.Bool("on", b))
	return nil
}

func (vc *vaultController) SetThreatProfile(s string) error {
	switch s {
	case "", vault.PresetNobody, vault.PresetDomestic, vault.PresetPower, vault.PresetForce:

	default:
		return fmt.Errorf("invalid threat profile %q", s)
	}
	vc.mu.Lock()
	defer vc.mu.Unlock()
	prev := vc.payload.ThreatProfile
	vc.payload.ThreatProfile = s
	if err := vc.resealLocked(); err != nil {
		vc.payload.ThreatProfile = prev
		return fmt.Errorf("re-seal: %w", err)
	}
	slog.Info("vault: ThreatProfile set", slog.String("profile", s))
	return nil
}

func (vc *vaultController) ApplyThreatPreset(preset string) error {
	bundle, ok := vault.ThreatPresetBundles[preset]
	if !ok {
		return fmt.Errorf("unknown threat preset %q", preset)
	}
	vc.mu.Lock()
	defer vc.mu.Unlock()
	prev := vc.payload
	vc.payload.ThreatProfile = preset

	vc.payload.ShowLoginForm = bundle.ShowLoginForm
	vc.payload.SayPassphraseIsDefault = bundle.SayPassphraseIsDefault

	vc.payload.IdleAction = bundle.IdleAction
	vc.payload.IdleTimeoutSeconds = bundle.IdleTimeoutSeconds
	vc.payload.PinValiditySec = bundle.PinValiditySec
	vc.payload.PanicAction = bundle.PanicAction

	vc.payload.NotifyShellEnabled = bundle.NotifyShellEnabled
	vc.payload.NotifyShowSender = bundle.NotifyShowSender
	vc.payload.NotifyShowBody = bundle.NotifyShowBody
	vc.payload.NotifyDisguiseEnabled = bundle.NotifyDisguiseEnabled
	vc.payload.NotifyNoisy = bundle.NotifyNoisy
	vc.payload.NotifyPersistUntilOpen = bundle.NotifyPersistUntilOpen
	vc.payload.NotifyDeepLink = bundle.NotifyDeepLink
	vc.payload.NotificationsOnLock = bundle.NotificationsOnLock

	vc.payload.ShareTargetEnabled = bundle.ShareTargetEnabled
	vc.payload.ShowFailedDestroyAttempts = bundle.ShowFailedDestroyAttempts

	if err := vc.resealLocked(); err != nil {
		vc.payload = prev
		return fmt.Errorf("re-seal: %w", err)
	}
	slog.Info("vault: threat preset applied", slog.String("preset", preset))

	slog.Debug("vault: threat preset unlock cells written",
		slog.String("preset", preset),
		slog.Bool("show_login_form", bundle.ShowLoginForm),
		slog.Bool("say_passphrase_is_default", bundle.SayPassphraseIsDefault))
	slog.Debug("vault: threat preset lock cells written",
		slog.String("preset", preset),
		slog.String("idle_action", bundle.IdleAction),
		slog.Int("idle_timeout_sec", bundle.IdleTimeoutSeconds),
		slog.Int("pin_validity_sec", bundle.PinValiditySec),
		slog.String("panic_action", bundle.PanicAction))
	slog.Debug("vault: threat preset notification cells written",
		slog.String("preset", preset),
		slog.Bool("notify_shell_enabled", bundle.NotifyShellEnabled),
		slog.Bool("notify_show_sender", bundle.NotifyShowSender),
		slog.Bool("notify_show_body", bundle.NotifyShowBody),
		slog.Bool("notify_disguise_enabled", bundle.NotifyDisguiseEnabled),
		slog.Bool("notify_noisy", bundle.NotifyNoisy),
		slog.Bool("notify_persist_until_open", bundle.NotifyPersistUntilOpen),
		slog.Bool("notify_deep_link", bundle.NotifyDeepLink),
		slog.Bool("notifications_on_lock", bundle.NotificationsOnLock))
	slog.Debug("vault: threat preset mobile cells written",
		slog.String("preset", preset),
		slog.Bool("share_target_enabled", bundle.ShareTargetEnabled),
		slog.Bool("show_failed_destroy_attempts", bundle.ShowFailedDestroyAttempts))
	return nil
}

func (vc *vaultController) SetPanicAction(s string) error {
	switch s {
	case "", "safe-lock", "hard-lock", "self-destruct":

	default:
		return fmt.Errorf("invalid panic action %q", s)
	}
	vc.mu.Lock()
	defer vc.mu.Unlock()
	prev := vc.payload.PanicAction
	vc.payload.PanicAction = s
	if err := vc.resealLocked(); err != nil {
		vc.payload.PanicAction = prev
		return fmt.Errorf("re-seal: %w", err)
	}
	slog.Info("vault: PanicAction set", slog.String("action", s))
	return nil
}

func (vc *vaultController) SetDefaultSaveDir(s string) error {
	if s != "" {
		if err := validateDirPath(s); err != nil {
			return fmt.Errorf("default save dir: %w", err)
		}
	}
	vc.mu.Lock()
	defer vc.mu.Unlock()
	prev := vc.payload.DefaultSaveDir
	vc.payload.DefaultSaveDir = s
	if err := vc.resealLocked(); err != nil {
		vc.payload.DefaultSaveDir = prev
		return fmt.Errorf("re-seal: %w", err)
	}
	slog.Info("vault: DefaultSaveDir set", slog.String("path", s))
	return nil
}

func (vc *vaultController) SetDefaultAttachStartDir(s string) error {
	if s != "" {
		if err := validateDirPath(s); err != nil {
			return fmt.Errorf("default attach start dir: %w", err)
		}
	}
	vc.mu.Lock()
	defer vc.mu.Unlock()
	prev := vc.payload.DefaultAttachStartDir
	vc.payload.DefaultAttachStartDir = s
	if err := vc.resealLocked(); err != nil {
		vc.payload.DefaultAttachStartDir = prev
		return fmt.Errorf("re-seal: %w", err)
	}
	slog.Info("vault: DefaultAttachStartDir set", slog.String("path", s))
	return nil
}

func (vc *vaultController) Settings() ipc.Settings {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	p := vc.payload
	return ipc.Settings{
		IdleAction:                p.IdleAction,
		IdleTimeoutSeconds:        p.IdleTimeoutSeconds,
		PinValiditySec:            p.PinValiditySec,
		NotifyShellEnabled:        p.NotifyShellEnabled,
		NotifyShowSender:          p.NotifyShowSender,
		NotifyShowBody:            p.NotifyShowBody,
		NotificationsOnLock:       p.NotificationsOnLock,
		NotifyPersistUntilOpen:    p.NotifyPersistUntilOpen,
		NotifyDeepLink:            p.NotifyDeepLink,
		ShowFailedDestroyAttempts: p.ShowFailedDestroyAttempts,
		ThreatProfile:             p.ThreatProfile,
		PanicAction:               p.PanicAction,
		SecurityWarnings:          append([]string(nil), p.SecurityWarnings...),
		HasTorPassword:            p.TorPassword != "",
		DefaultSaveDir:            p.DefaultSaveDir,
		DefaultAttachStartDir:     p.DefaultAttachStartDir,
	}
}
