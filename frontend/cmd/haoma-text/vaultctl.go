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
	passphrase    string
	params        vault.KDFParams
	path          string
	haomaVaultBin string
}

func newVaultController(path, passphrase string, payload vault.Payload, params vault.KDFParams, haomaVaultBin string) *vaultController {
	return &vaultController{
		path:          path,
		passphrase:    passphrase,
		payload:       payload,
		params:        params,
		haomaVaultBin: haomaVaultBin,
	}
}

func (vc *vaultController) snapshot() vault.Payload {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	return vc.payload
}

func (vc *vaultController) resealLocked() error {
	return vc.resealWithLocked(vc.passphrase)
}

func (vc *vaultController) resealWithLocked(passphrase string) error {
	if err := vc.payload.Validate(); err != nil {
		return fmt.Errorf("validate: %w", err)
	}
	jsonPayload, err := json.Marshal(vc.payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	cfgDir := filepath.Dir(vc.path)
	bin := vc.haomaVaultBin
	if bin == "" {
		bin = "haoma-vault"
	}
	cmd := exec.Command(bin, "--cfg-dir", cfgDir, "-w")
	cmd.Stdin = strings.NewReader(passphrase + "\n" + string(jsonPayload))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	slog.Debug("vault: reseal initiated", slog.String("bin", bin), slog.Int("payload_bytes", len(jsonPayload)))
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
	slog.Info("vault: mutate ok", slog.String("label", label))
	return nil
}

func (vc *vaultController) ChangePassphrase(old, newPass string) error {
	if newPass == "" {
		return errors.New("new passphrase cannot be empty (would brick the vault)")
	}
	vc.mu.Lock()
	defer vc.mu.Unlock()
	if subtle.ConstantTimeCompare([]byte(old), []byte(vc.passphrase)) != 1 {
		return errors.New("current passphrase incorrect")
	}
	if err := vc.resealWithLocked(newPass); err != nil {
		return fmt.Errorf("re-seal: %w", err)
	}
	vc.passphrase = newPass
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

func (vc *vaultController) SetDefaultRetentionSec(n uint64) error {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	prev := vc.payload.DefaultRetentionSec
	vc.payload.DefaultRetentionSec = n
	if err := vc.resealLocked(); err != nil {
		vc.payload.DefaultRetentionSec = prev
		return fmt.Errorf("re-seal: %w", err)
	}
	slog.Info("vault: DefaultRetentionSec set", slog.Uint64("seconds", n))
	return nil
}

func (vc *vaultController) SetDefaultSendReceipts(b bool) error {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	prev := vc.payload.DefaultSendReceipts
	vc.payload.DefaultSendReceipts = b
	if err := vc.resealLocked(); err != nil {
		vc.payload.DefaultSendReceipts = prev
		return fmt.Errorf("re-seal: %w", err)
	}
	slog.Info("vault: DefaultSendReceipts set", slog.Bool("on", b))
	return nil
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

func (vc *vaultController) SetThreatProfile(s string) error {
	switch s {
	case "", vault.PresetDomestic, vault.PresetPrivacy, vault.PresetActivist:

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
	if preset == vault.PresetActivist {
		return errors.New("activist preset not yet implemented (waits on hard-kill + data-destruction primitives)")
	}
	bundle, ok := vault.ThreatPresetBundles[preset]
	if !ok {
		return fmt.Errorf("unknown threat preset %q", preset)
	}
	vc.mu.Lock()
	defer vc.mu.Unlock()
	prev := vc.payload
	vc.payload.ThreatProfile = preset
	vc.payload.IdleAction = bundle.IdleAction
	vc.payload.IdleTimeoutSeconds = bundle.IdleTimeoutSeconds
	vc.payload.PinValiditySec = bundle.PinValiditySec
	vc.payload.PanicAction = bundle.PanicAction
	if err := vc.resealLocked(); err != nil {
		vc.payload.ThreatProfile = prev.ThreatProfile
		vc.payload.IdleAction = prev.IdleAction
		vc.payload.IdleTimeoutSeconds = prev.IdleTimeoutSeconds
		vc.payload.PinValiditySec = prev.PinValiditySec
		vc.payload.PanicAction = prev.PanicAction
		return fmt.Errorf("re-seal: %w", err)
	}
	slog.Info("vault: threat preset applied",
		slog.String("preset", preset),
		slog.String("idle_action", bundle.IdleAction),
		slog.Int("idle_timeout_sec", bundle.IdleTimeoutSeconds),
		slog.Int("pin_validity_sec", bundle.PinValiditySec),
		slog.String("panic_action", bundle.PanicAction))
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
		DefaultRetentionSec:   p.DefaultRetentionSec,
		DefaultSendReceipts:   p.DefaultSendReceipts,
		IdleAction:            p.IdleAction,
		IdleTimeoutSeconds:    p.IdleTimeoutSeconds,
		PinValiditySec:        p.PinValiditySec,
		NotifyShellEnabled:    p.NotifyShellEnabled,
		NotifyShowSender:      p.NotifyShowSender,
		NotifyShowBody:        p.NotifyShowBody,
		NotificationsOnLock:   p.NotificationsOnLock,
		ThreatProfile:         p.ThreatProfile,
		PanicAction:           p.PanicAction,
		SecurityWarnings:      append([]string(nil), p.SecurityWarnings...),
		HasTorPassword:        p.TorPassword != "",
		DefaultSaveDir:        p.DefaultSaveDir,
		DefaultAttachStartDir: p.DefaultAttachStartDir,
	}
}
