package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"haoma-frontend/internal/ipc"
)

func TestScopeSettings_Domains(t *testing.T) {
	full := ipc.Settings{
		DefaultRetentionSec: 86400,
		DefaultSendReceipts: true,
		IdleAction:          "soft-lock",
		IdleTimeoutSeconds:  900,
		PinValiditySec:      300,
		HasTorPassword:      true,
		NotifyShellEnabled:  true,
		NotifyShowSender:    true,
		NotifyShowBody:      true,
		NotificationsOnLock: true,
		ThreatProfile:       "privacy",
		PanicAction:         "hard-lock",
		SecurityWarnings:    []string{"warn-1"},
	}

	tests := []struct {
		name   string
		domain ipc.SettingsDomain
		check  func(t *testing.T, got ipc.Settings)
	}{
		{
			name:   "all",
			domain: ipc.SettingsDomainAll,
			check: func(t *testing.T, got ipc.Settings) {
				if got.IdleAction != "soft-lock" || got.NotifyShowBody != true || got.ThreatProfile != "privacy" {
					t.Errorf("all domain should return everything, got %+v", got)
				}
			},
		},
		{
			name:   "identity",
			domain: ipc.SettingsDomainIdentity,
			check: func(t *testing.T, got ipc.Settings) {
				if got.DefaultRetentionSec != 86400 || got.DefaultSendReceipts != true {
					t.Errorf("identity: missing expected fields: %+v", got)
				}
				if got.IdleAction != "" || got.PanicAction != "" || got.NotifyShellEnabled {
					t.Errorf("identity: bled across domains: %+v", got)
				}
			},
		},
		{
			name:   "lock",
			domain: ipc.SettingsDomainLock,
			check: func(t *testing.T, got ipc.Settings) {
				if got.IdleAction != "soft-lock" || got.IdleTimeoutSeconds != 900 || got.PinValiditySec != 300 {
					t.Errorf("lock: missing: %+v", got)
				}
				if got.DefaultRetentionSec != 0 || got.NotifyShellEnabled {
					t.Errorf("lock: bled: %+v", got)
				}
			},
		},
		{
			name:   "tor",
			domain: ipc.SettingsDomainTor,
			check: func(t *testing.T, got ipc.Settings) {
				if !got.HasTorPassword {
					t.Errorf("tor: HasTorPassword not propagated")
				}
				if got.IdleAction != "" || got.NotifyShellEnabled || got.ThreatProfile != "" {
					t.Errorf("tor: bled: %+v", got)
				}
			},
		},
		{
			name:   "notifications",
			domain: ipc.SettingsDomainNotifications,
			check: func(t *testing.T, got ipc.Settings) {
				if !got.NotifyShellEnabled || !got.NotifyShowSender || !got.NotifyShowBody || !got.NotificationsOnLock {
					t.Errorf("notifications: missing: %+v", got)
				}
				if got.IdleAction != "" || got.ThreatProfile != "" {
					t.Errorf("notifications: bled: %+v", got)
				}
			},
		},
		{
			name:   "advanced",
			domain: ipc.SettingsDomainAdvanced,
			check: func(t *testing.T, got ipc.Settings) {
				if got.ThreatProfile != "privacy" || got.PanicAction != "hard-lock" {
					t.Errorf("advanced: missing: %+v", got)
				}
				if len(got.SecurityWarnings) != 1 || got.SecurityWarnings[0] != "warn-1" {
					t.Errorf("advanced: SecurityWarnings drift: %+v", got.SecurityWarnings)
				}
				if got.IdleAction != "" || got.NotifyShellEnabled {
					t.Errorf("advanced: bled: %+v", got)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := scopeSettings(full, tt.domain)
			if err != nil {
				t.Fatalf("scopeSettings(%q): %v", tt.domain, err)
			}
			tt.check(t, got)
		})
	}
}

func TestScopeSettings_UnknownDomain(t *testing.T) {
	_, err := scopeSettings(ipc.Settings{}, ipc.SettingsDomain("bogus"))
	if err == nil {
		t.Fatal("expected error for unknown domain")
	}
}

func TestDispatch_GetSettings_ReturnsDefaults(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	_, addr, certPath, token := newTestDaemon(t, stub)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := dialTest(t, ctx, addr, certPath, token)
	writeFrame(t, ctx, conn, mustFrame(ipc.FrameGetSettings, "g-1", ipc.GetSettingsRequest{}))
	resp := readUntil(t, ctx, conn, ipc.FrameSettingsListed)
	if resp.ID != "g-1" {
		t.Errorf("corr-id = %q, want g-1", resp.ID)
	}
	var p ipc.SettingsListedResponse
	if err := json.Unmarshal(resp.Payload, &p); err != nil {
		t.Fatal(err)
	}
	want := defaultSettings()
	if p.Settings.DefaultSendReceipts != want.DefaultSendReceipts {
		t.Errorf("DefaultSendReceipts = %v, want %v (defaults)", p.Settings.DefaultSendReceipts, want.DefaultSendReceipts)
	}
	if !p.Settings.NotifyShellEnabled {
		t.Error("NotifyShellEnabled should default ON (banner stays anonymous via the two ShowSender / ShowBody flags)")
	}
	if p.Settings.NotifyShowSender {
		t.Error("NotifyShowSender should default OFF (banner reveals nothing about the peer)")
	}
	if p.Settings.NotifyShowBody {
		t.Error("NotifyShowBody should default OFF (banner reveals nothing about the message)")
	}
	if !p.Settings.NotificationsOnLock {
		t.Error("NotificationsOnLock should default true")
	}
}

func TestDispatch_SyncSettings_UpdatesSnapshotAndBroadcasts(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	_, addr, certPath, token := newTestDaemon(t, stub)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := dialTest(t, ctx, addr, certPath, token)

	want := ipc.Settings{
		DefaultRetentionSec: 3600,
		DefaultSendReceipts: false,
		IdleAction:          "soft-lock",
		IdleTimeoutSeconds:  600,
		PinValiditySec:      120,
		NotifyShellEnabled:  true,
		NotifyShowSender:    true,
		NotifyShowBody:      false,
		NotificationsOnLock: false,
		ThreatProfile:       "privacy",
		PanicAction:         "hard-lock",
		HasTorPassword:      true,
	}
	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSyncSettings, "", ipc.SyncSettingsRequest{Settings: want}))

	resp := readUntil(t, ctx, conn, ipc.FrameSettingsChanged)
	var pushPayload ipc.SettingsChangedPayload
	if err := json.Unmarshal(resp.Payload, &pushPayload); err != nil {
		t.Fatal(err)
	}
	if pushPayload.Settings.IdleAction != "soft-lock" {
		t.Errorf("broadcast IdleAction = %q, want soft-lock", pushPayload.Settings.IdleAction)
	}
	if pushPayload.Settings.NotifyShellEnabled != true {
		t.Errorf("broadcast NotifyShellEnabled drifted: %+v", pushPayload.Settings)
	}

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameGetSettings, "g-2", ipc.GetSettingsRequest{}))
	got := readUntil(t, ctx, conn, ipc.FrameSettingsListed)
	var p ipc.SettingsListedResponse
	if err := json.Unmarshal(got.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Settings.PanicAction != "hard-lock" {
		t.Errorf("PanicAction after sync = %q, want hard-lock", p.Settings.PanicAction)
	}
	if p.Settings.DefaultRetentionSec != 3600 {
		t.Errorf("DefaultRetentionSec after sync = %d, want 3600", p.Settings.DefaultRetentionSec)
	}
	if !p.Settings.HasTorPassword {
		t.Error("HasTorPassword should round-trip")
	}
}

func TestDispatch_GetSettings_DomainFilter(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	_, addr, certPath, token := newTestDaemon(t, stub)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSyncSettings, "", ipc.SyncSettingsRequest{Settings: ipc.Settings{
		IdleAction:         "soft-lock",
		IdleTimeoutSeconds: 600,
		NotifyShellEnabled: true,
	}}))
	_ = readUntil(t, ctx, conn, ipc.FrameSettingsChanged)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameGetSettings, "g-3", ipc.GetSettingsRequest{Domain: ipc.SettingsDomainLock}))
	resp := readUntil(t, ctx, conn, ipc.FrameSettingsListed)
	var p ipc.SettingsListedResponse
	if err := json.Unmarshal(resp.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Settings.IdleAction != "soft-lock" {
		t.Errorf("Lock domain should carry IdleAction; got %q", p.Settings.IdleAction)
	}
	if p.Settings.NotifyShellEnabled {
		t.Error("Lock domain should NOT carry NotifyShellEnabled (bleed)")
	}
}
