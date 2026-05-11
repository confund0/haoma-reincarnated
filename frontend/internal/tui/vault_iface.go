package tui

import (
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/vault"
)

type VaultController interface {
	ChangePassphrase(old, new string) error
	ChangePIN(old, new string) error

	SetIdleAction(s string) error
	SetIdleTimeoutSeconds(n int) error
	SetPinValiditySec(n int) error

	SetTorPassword(s string) error

	IsInsecureDefaultPassphrase() bool
	IsInsecureDefaultPIN() bool

	SetDefaultRetentionSec(n uint64) error
	SetDefaultSendReceipts(b bool) error
	SetNotifyShellEnabled(b bool) error
	SetNotifyShowSender(b bool) error
	SetNotifyShowBody(b bool) error
	SetNotificationsOnLock(b bool) error
	SetThreatProfile(s string) error
	SetPanicAction(s string) error

	ApplyThreatPreset(preset string) error

	SetDefaultSaveDir(s string) error
	SetDefaultAttachStartDir(s string) error

	Settings() ipc.Settings

	Mutate(label string, transform func(*vault.Payload) error) error
}
