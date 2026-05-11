package main

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"haoma-frontend/internal/ipc"
)

func defaultSettings() *ipc.Settings {
	return &ipc.Settings{

		DefaultRetentionSec: 0,
		DefaultSendReceipts: true,
		IdleAction:          "safe-lock",
		IdleTimeoutSeconds:  1800,
		PinValiditySec:      0,
		NotifyShellEnabled:  true,
		NotifyShowSender:    false,
		NotifyShowBody:      false,
		NotificationsOnLock: true,
	}
}

func scopeSettings(full ipc.Settings, domain ipc.SettingsDomain) (ipc.Settings, error) {
	switch domain {
	case ipc.SettingsDomainAll:
		return full, nil
	case ipc.SettingsDomainIdentity:
		return ipc.Settings{
			DefaultRetentionSec: full.DefaultRetentionSec,
			DefaultSendReceipts: full.DefaultSendReceipts,
		}, nil
	case ipc.SettingsDomainLock:
		return ipc.Settings{
			IdleAction:         full.IdleAction,
			IdleTimeoutSeconds: full.IdleTimeoutSeconds,
			PinValiditySec:     full.PinValiditySec,
		}, nil
	case ipc.SettingsDomainTor:
		return ipc.Settings{
			HasTorPassword: full.HasTorPassword,
		}, nil
	case ipc.SettingsDomainNotifications:
		return ipc.Settings{
			NotifyShellEnabled:  full.NotifyShellEnabled,
			NotifyShowSender:    full.NotifyShowSender,
			NotifyShowBody:      full.NotifyShowBody,
			NotificationsOnLock: full.NotificationsOnLock,
		}, nil
	case ipc.SettingsDomainAdvanced:
		return ipc.Settings{
			ThreatProfile:    full.ThreatProfile,
			PanicAction:      full.PanicAction,
			SecurityWarnings: full.SecurityWarnings,
		}, nil
	case ipc.SettingsDomainFiles:
		return ipc.Settings{
			DefaultSaveDir:        full.DefaultSaveDir,
			DefaultAttachStartDir: full.DefaultAttachStartDir,
		}, nil
	default:
		return ipc.Settings{}, fmt.Errorf("unknown settings domain %q", domain)
	}
}

func (sd *sessionDispatcher) handleGetSettings(sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle get_settings")
	var req ipc.GetSettingsRequest
	if len(f.Payload) > 0 {
		if err := json.Unmarshal(f.Payload, &req); err != nil {
			sendError(sess, f.ID, "bad_frame", "decode get_settings: "+err.Error())
			return
		}
	}
	snap := sd.d.settingsSnapshot.Load()
	if snap == nil {

		snap = defaultSettings()
	}
	scoped, err := scopeSettings(*snap, req.Domain)
	if err != nil {
		sendError(sess, f.ID, "bad_request", err.Error())
		return
	}
	resp, err := ipc.NewFrame(ipc.FrameSettingsListed, f.ID, ipc.SettingsListedResponse{Settings: scoped})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send settings_listed frame failed", slog.Any("err", err))
	}
}

func (sd *sessionDispatcher) handleSyncSettings(sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle sync_settings")
	var req ipc.SyncSettingsRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_frame", "decode sync_settings: "+err.Error())
		return
	}
	prev := sd.d.settingsSnapshot.Load()
	settings := req.Settings
	sd.d.settingsSnapshot.Store(&settings)
	slog.Debug("settings snapshot updated",
		slog.Bool("had_previous", prev != nil),
		slog.Bool("notify_shell_enabled", settings.NotifyShellEnabled),
		slog.Bool("notify_show_sender", settings.NotifyShowSender),
		slog.Bool("notify_show_body", settings.NotifyShowBody),
		slog.Uint64("default_retention_sec", settings.DefaultRetentionSec),
		slog.Bool("default_send_receipts", settings.DefaultSendReceipts),
		slog.String("idle_action", settings.IdleAction),
		slog.String("threat_profile", settings.ThreatProfile),
		slog.Bool("has_tor_password", settings.HasTorPassword),
	)
	push(sd.d.ipcSrv, ipc.FrameSettingsChanged, "", ipc.SettingsChangedPayload{Settings: settings})
}

func (sd *sessionDispatcher) handleClientLockState(sess *ipc.Session, f ipc.Frame) {
	var req ipc.ClientLockStateRequest
	if len(f.Payload) > 0 {
		if err := json.Unmarshal(f.Payload, &req); err != nil {
			sendError(sess, f.ID, "bad_frame", "decode client_lock_state: "+err.Error())
			return
		}
	}
	prev := sd.d.clientSoftLocked.Swap(req.SoftLocked)
	if prev != req.SoftLocked {
		slog.Debug("client lock state changed",
			slog.Bool("soft_locked", req.SoftLocked),
		)
	}
}
