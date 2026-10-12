package tui_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/tui"
	"haoma-frontend/internal/vault"
)

type payloadVault struct {
	stubVault
	mu sync.Mutex
	p  vault.Payload

	mutations map[string]int
}

func (v *payloadVault) ApplyThreatPreset(preset string) error {
	b, ok := vault.ThreatPresetBundles[preset]
	if !ok {
		return errUnknownPreset
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.p.ThreatProfile = preset
	v.p.ShowLoginForm = b.ShowLoginForm
	v.p.SayPassphraseIsDefault = b.SayPassphraseIsDefault
	v.p.IdleAction = b.IdleAction
	v.p.IdleTimeoutSeconds = b.IdleTimeoutSeconds
	v.p.PinValiditySec = b.PinValiditySec
	v.p.PanicAction = b.PanicAction
	v.p.NotifyShellEnabled = b.NotifyShellEnabled
	v.p.NotifyShowSender = b.NotifyShowSender
	v.p.NotifyShowBody = b.NotifyShowBody
	v.p.NotifyDisguiseEnabled = b.NotifyDisguiseEnabled
	v.p.NotifyNoisy = b.NotifyNoisy
	v.p.NotifyPersistUntilOpen = b.NotifyPersistUntilOpen
	v.p.NotifyDeepLink = b.NotifyDeepLink
	v.p.NotificationsOnLock = b.NotificationsOnLock
	v.p.ShareTargetEnabled = b.ShareTargetEnabled
	v.p.ShowFailedDestroyAttempts = b.ShowFailedDestroyAttempts
	return nil
}

func (v *payloadVault) Mutate(label string, transform func(*vault.Payload) error) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.mutations == nil {
		v.mutations = map[string]int{}
	}
	v.mutations[label]++
	return transform(&v.p)
}

func (v *payloadVault) mutationCount(label string) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.mutations[label]
}

func waitForMutation(t *testing.T, sim tcell.SimulationScreen, v *payloadVault, label string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("the %q save never reached the vault; last frame:\n%s", label, screenText(sim))
		default:
			if v.mutationCount(label) > 0 {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
}

func (v *payloadVault) SetThreatProfile(s string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.p.ThreatProfile = s
	return nil
}

func (v *payloadVault) Settings() ipc.Settings {
	v.mu.Lock()
	defer v.mu.Unlock()
	return ipc.Settings{
		IdleAction:                v.p.IdleAction,
		IdleTimeoutSeconds:        v.p.IdleTimeoutSeconds,
		PinValiditySec:            v.p.PinValiditySec,
		NotifyShellEnabled:        v.p.NotifyShellEnabled,
		NotifyShowSender:          v.p.NotifyShowSender,
		NotifyShowBody:            v.p.NotifyShowBody,
		NotificationsOnLock:       v.p.NotificationsOnLock,
		NotifyPersistUntilOpen:    v.p.NotifyPersistUntilOpen,
		NotifyDeepLink:            v.p.NotifyDeepLink,
		ShowFailedDestroyAttempts: v.p.ShowFailedDestroyAttempts,
		ThreatProfile:             v.p.ThreatProfile,
		PanicAction:               v.p.PanicAction,
	}
}

func (v *payloadVault) snapshot() vault.Payload {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.p
}

var errUnknownPreset = errUnknown("unknown threat preset")

type errUnknown string

func (e errUnknown) Error() string { return string(e) }

func recordChatDefaults(t *testing.T) (func(context.Context, *ipc.Session), <-chan ipc.SetChatDefaultsRequest) {
	t.Helper()
	got := make(chan ipc.SetChatDefaultsRequest, 8)
	script := func(_ context.Context, s *ipc.Session) {
		for {
			f, err := s.Recv()
			if err != nil {
				return
			}
			switch f.Type {
			case ipc.FrameDeviceLinksList:
				out, err := ipc.NewFrame(ipc.FrameDeviceLinksListed, f.ID, ipc.DeviceLinksListedResponse{})
				if err != nil || s.Send(out) != nil {
					return
				}
			case ipc.FrameGetDestroyPIN:
				out, err := ipc.NewFrame(ipc.FrameDestroyPINListed, f.ID, ipc.DestroyPINListedResponse{})
				if err != nil || s.Send(out) != nil {
					return
				}
			case ipc.FrameSetChatDefaults:
				var req ipc.SetChatDefaultsRequest
				if len(f.Payload) > 0 {
					_ = json.Unmarshal(f.Payload, &req)
				}
				select {
				case got <- req:
				default:
				}
				out, err := ipc.NewFrame(ipc.FrameChatDefaultsChanged, "", ipc.ChatDefaultsPayload(req))
				if err != nil || s.Send(out) != nil {
					return
				}
			}
		}
	}
	return script, got
}

var settingsRowsAboveLock = []string{"Profile", "Chat defaults", "Files"}

var settingsRowsBetweenLockAndNotifs = []string{"Tor", "Linked devices"}

func openLockPage(t *testing.T, sim tcell.SimulationScreen) {
	t.Helper()
	typeCommand(sim, "/settings")
	waitForScreen(t, sim, "Chat defaults")
	for range settingsRowsAboveLock {
		sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	}
	waitForScreen(t, sim, "Who is after you?")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
}

func applyTierAnswer(t *testing.T, sim tcell.SimulationScreen, steps int, answer string) {
	t.Helper()
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	for i := 0; i < steps; i++ {
		sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	}
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitForScreen(t, sim, "The \""+answer+"\" answer sets")

	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)

	waitForScreen(t, sim, "This writes the lock,")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
}

func TestSettings_ApplyingATierSendsItsRetentionDefault(t *testing.T) {
	script, got := recordChatDefaults(t)
	dir, addr, cancel := spinScriptedIPCServer(t, script)
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}

	openLockPage(t, sim)

	applyTierAnswer(t, sim, 4, "People with force")

	want := vault.ThreatPresetBundles[vault.PresetForce].DefaultRetentionSec
	select {
	case req := <-got:
		if req.RetentionSec != want {
			t.Errorf("daemon was told to keep messages for %ds, want %ds", req.RetentionSec, want)
		}

		if !req.SendReceipts {
			t.Error("the preset told the daemon to stop sending read receipts by default — " +
				"the receipts value the welcome carried did not survive to the frame")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no set_chat_defaults frame reached the daemon — the retention cell of the bundle went nowhere")
	}
}

func TestSettings_ANotificationSaveAfterAnApplyKeepsTheTier(t *testing.T) {
	script, _ := recordChatDefaults(t)
	dir, addr, cancel := spinScriptedIPCServer(t, script)
	defer cancel()

	app, sim := launchApp(t, dir, addr)

	vc := &payloadVault{}
	if err := vc.ApplyThreatPreset(vault.PresetNobody); err != nil {
		t.Fatalf("seed the vault: %v", err)
	}
	app.VaultCtl = vc

	openLockPage(t, sim)

	applyTierAnswer(t, sim, 3, "People with force")
	waitForScreen(t, sim, tui.ThreatAnswerStatusPrefix+"People with force")

	for range settingsRowsBetweenLockAndNotifs {
		sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	}
	sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	waitForScreen(t, sim, "Show sender name in notifications")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)

	for i := 0; i < 7; i++ {
		sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	}
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitForMutation(t, sim, vc, "notifications")

	force := vault.ThreatPresetBundles[vault.PresetForce]
	got := vc.snapshot()
	if got.ThreatProfile != vault.PresetForce {
		t.Fatalf("threat profile is %q after the save, want %q", got.ThreatProfile, vault.PresetForce)
	}
	cells := []struct {
		name string
		got  bool
		want bool
	}{
		{"notify_shell_enabled", got.NotifyShellEnabled, force.NotifyShellEnabled},
		{"notify_show_sender", got.NotifyShowSender, force.NotifyShowSender},
		{"notify_show_body", got.NotifyShowBody, force.NotifyShowBody},
		{"notifications_on_lock", got.NotificationsOnLock, force.NotificationsOnLock},
		{"notify_persist_until_open", got.NotifyPersistUntilOpen, force.NotifyPersistUntilOpen},
		{"notify_deep_link", got.NotifyDeepLink, force.NotifyDeepLink},
		{"show_failed_destroy_attempts", got.ShowFailedDestroyAttempts, force.ShowFailedDestroyAttempts},
	}
	for _, c := range cells {
		if c.got != c.want {
			t.Errorf("%s is %v after the Notifications save, want %v — "+
				"the form was not rebuilt and wrote its pre-apply value back over the tier",
				c.name, c.got, c.want)
		}
	}
}

func TestSettings_LockPageFitsASmallTerminal(t *testing.T) {
	script, _ := recordChatDefaults(t)
	dir, addr, cancel := spinScriptedIPCServer(t, script)
	defer cancel()

	app, sim := launchApp(t, dir, addr)

	app.VaultCtl = &payloadVault{}
	sim.SetSize(80, 24)

	openLockPage(t, sim)
	applyTierAnswer(t, sim, 4, "People with force")

	waitForScreen(t, sim, "The \"People with force\" answer sets")
	waitForScreen(t, sim, "messages kept: 1h")

	waitForScreen(t, sim, "How long messages are kept: 1h.")

	waitForScreen(t, sim, "Save")
	waitForScreen(t, sim, "Cancel")
}
