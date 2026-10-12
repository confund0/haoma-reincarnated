package tui_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gdamore/tcell/v2"

	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/tui"
	"haoma-frontend/internal/vault"
)

func holdChatDefaults(t *testing.T) (func(context.Context, *ipc.Session), chan<- struct{}, <-chan struct{}) {
	t.Helper()
	release := make(chan struct{})
	sent := make(chan struct{})
	script := func(ctx context.Context, s *ipc.Session) {
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
				go func() {
					select {
					case <-release:
					case <-ctx.Done():
						return
					}
					out, err := ipc.NewFrame(ipc.FrameChatDefaultsChanged, "", ipc.ChatDefaultsPayload(req))
					if err == nil {
						_ = s.Send(out)
					}
					close(sent)
				}()
			}
		}
	}
	return script, release, sent
}

func TestSettings_ChatDefaultsShowsTheTierRetentionAfterAnApply(t *testing.T) {
	script, release, _ := holdChatDefaults(t)
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

	for i := 0; i < 2; i++ {
		sim.InjectKey(tcell.KeyUp, 0, tcell.ModNone)
	}
	waitForScreen(t, sim, "Default disappearing messages")
	waitForScreen(t, sim, "1h")
	close(release)
}

func TestSettings_ADaemonBroadcastKeepsAnEditInProgress(t *testing.T) {
	script, release, sent := holdChatDefaults(t)
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
	waitForScreen(t, sim, tui.RetentionNoteWaiting)

	for i := 0; i < 2; i++ {
		sim.InjectKey(tcell.KeyUp, 0, tcell.ModNone)
	}
	waitForScreen(t, sim, "Default disappearing messages")
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	waitForScreen(t, sim, "[ ] Send read receipts")

	close(release)
	<-sent

	sim.InjectKey(tcell.KeyEscape, 0, tcell.ModNone)
	for i := 0; i < 2; i++ {
		sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	}
	waitForScreen(t, sim, "How long messages are kept: 1h.")

	for i := 0; i < 2; i++ {
		sim.InjectKey(tcell.KeyUp, 0, tcell.ModNone)
	}
	waitForScreen(t, sim, "[ ] Send read receipts")
}
