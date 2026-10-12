package tui_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/tui"
)

type checkAnswer func(id string) (ipc.Frame, error)

func verifiedWith(resp ipc.DestroyPINVerifiedResponse) checkAnswer {
	return func(id string) (ipc.Frame, error) { return ipc.NewFrame(ipc.FrameDestroyPINVerified, id, resp) }
}

func refusedWith(code string) checkAnswer {
	return func(id string) (ipc.Frame, error) {
		return ipc.NewFrame(ipc.FrameError, id, ipc.ErrorPayload{Code: code, Message: "the check did not go out"})
	}
}

func linkThenCheckScript(check checkAnswer) func(context.Context, *ipc.Session) {
	return func(_ context.Context, s *ipc.Session) {
		var linked atomic.Bool
		for {
			f, err := s.Recv()
			if err != nil {
				return
			}
			var out ipc.Frame
			switch f.Type {
			case ipc.FrameDeviceLinksList:
				resp := ipc.DeviceLinksListedResponse{}
				if linked.Load() {
					resp.Devices = []ipc.DeviceLinkEntry{{PeerID: "peer-aaa111", LinkedAt: 1}}
				}
				out, err = ipc.NewFrame(ipc.FrameDeviceLinksListed, f.ID, resp)
			case ipc.FrameDeviceLinkAccept:
				linked.Store(true)
				out, err = ipc.NewFrame(ipc.FrameDeviceLinkAccepted, f.ID, ipc.DeviceLinkAcceptedResponse{
					PeerID: "peer-aaa111", AdoptedFingerprint: "fp-deadbeef",
				})
			case ipc.FrameSetDestroyPIN:
				out, err = ipc.NewFrame(ipc.FrameDestroyPINSet, f.ID, ipc.DestroyPINSetResponse{Accepted: true})
			case ipc.FrameGetDestroyPIN:
				resp := ipc.DestroyPINListedResponse{}
				if linked.Load() {
					resp.Set = true
					resp.Siblings = []ipc.DestroyPINSiblingEntry{{PeerID: "peer-aaa111", SaltHeld: true}}
				}
				out, err = ipc.NewFrame(ipc.FrameDestroyPINListed, f.ID, resp)
			case ipc.FrameVerifyDestroyPIN:
				out, err = check(f.ID)
			default:
				continue
			}
			if err != nil || s.Send(out) != nil {
				return
			}
		}
	}
}

func joinAndProceed(t *testing.T, sim tcell.SimulationScreen) {
	t.Helper()
	typeCommand(sim, "/link-join")
	waitForScreen(t, sim, "Type the seven words shown on your other device")
	typeText(sim, testDestroyPIN)
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	typeText(sim, otherTestDestroyPIN)
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	typeText(sim, strings.Join(testLinkWords, " "))
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
}

func TestDeviceLink_ACheckNobodyAnsweredEndsLinkedWithAWarning(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, linkThenCheckScript(verifiedWith(ipc.DestroyPINVerifiedResponse{})))
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}
	sim.SetSize(200, 50)

	joinAndProceed(t, sim)

	waitForScreen(t, sim, "checking your other device's PIN")
	waitForScreen(t, sim, "WARNING")
	waitForScreen(t, sim, "Your other device did not answer.")
	waitForScreen(t, sim, "Linked. Please press [Verify destroy PIN…] later to check your other device's PIN.")
	waitForLinkedButtons(t, app, sim, "Close")

	got := flatten(screenText(sim))
	if strings.Contains(got, "FAILED") {
		t.Fatalf("a run whose link came up shows a failed step; screen:\n%s", screenText(sim))
	}
	if strings.Contains(got, "both PINs check out") {
		t.Fatalf("a check nobody answered was reported as passed; screen:\n%s", screenText(sim))
	}

	if !strings.Contains(got, "checking your other device's PIN") || !outcomeFollows(got, "checking your other device's PIN", "WARNING") {
		t.Fatalf("the check step does not carry the warning; screen:\n%s", screenText(sim))
	}

	pressLinkedButton(t, app, sim, "Close")
	waitForLinkedButtons(t, app, sim, linkedDeviceButtons...)
}

func TestDeviceLink_ACheckAnsweredNoStillFails(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, linkThenCheckScript(verifiedWith(ipc.DestroyPINVerifiedResponse{Answered: true})))
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}
	sim.SetSize(200, 50)

	joinAndProceed(t, sim)

	waitForScreen(t, sim, "Your other device did not accept that PIN.")
	waitForScreen(t, sim, "FAILED")
	got := flatten(screenText(sim))
	if strings.Contains(got, "WARNING") || strings.Contains(got, "Please press") {
		t.Fatalf("a refused PIN was read as no answer; screen:\n%s", screenText(sim))
	}
}

func TestDeviceLink_ACheckHaomaDidNotSendStillFails(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, linkThenCheckScript(verifiedWith(ipc.DestroyPINVerifiedResponse{
		Reason: ipc.DestroyPINVerifyNoSalt,
	})))
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}
	sim.SetSize(200, 50)

	joinAndProceed(t, sim)

	waitForScreen(t, sim, "Your other device has not sent its details yet, so its PIN cannot be checked.")
	waitForScreen(t, sim, "FAILED")
	got := flatten(screenText(sim))
	if strings.Contains(got, "WARNING") || strings.Contains(got, "Please press") || strings.Contains(got, "did not answer") {
		t.Fatalf("a check haoma did not send was read as no answer; screen:\n%s", screenText(sim))
	}
}

func TestDeviceLink_AReplyThatDidNotCheckOutFails(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, linkThenCheckScript(verifiedWith(ipc.DestroyPINVerifiedResponse{
		Reason: ipc.DestroyPINVerifyAnswerBad,
	})))
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}
	sim.SetSize(200, 50)

	joinAndProceed(t, sim)

	waitForScreen(t, sim, "Your other device's answer did not check out.")
	waitForScreen(t, sim, "FAILED")
	got := flatten(screenText(sim))
	if strings.Contains(got, "WARNING") || strings.Contains(got, "did not answer") {
		t.Fatalf("a reply that did not check out was read as no answer; screen:\n%s", screenText(sim))
	}
}

func TestDeviceLink_ACheckThatDidNotFinishEndsLinkedWithAWarning(t *testing.T) {
	for _, tc := range []struct {
		code, reason string
	}{
		{ipc.ReasonBackendDown, "The check could not be sent."},
		{ipc.ReasonNoAnswer, "No answer came back from the daemon, so the check did not finish."},
	} {
		t.Run(tc.code, func(t *testing.T) {
			dir, addr, cancel := spinScriptedIPCServer(t, linkThenCheckScript(refusedWith(tc.code)))
			defer cancel()

			app, sim := launchApp(t, dir, addr)
			app.VaultCtl = stubVault{}
			sim.SetSize(200, 50)

			joinAndProceed(t, sim)

			waitForScreen(t, sim, tc.reason)
			waitForScreen(t, sim, "Linked. Please press [Verify destroy PIN…] later to check your other device's PIN.")
			waitForLinkedButtons(t, app, sim, "Close")
			got := flatten(screenText(sim))
			if strings.Contains(got, "FAILED") {
				t.Fatalf("a run whose link came up shows a failed step; screen:\n%s", screenText(sim))
			}
			if !outcomeFollows(got, "checking your other device's PIN", "WARNING") {
				t.Fatalf("the check step does not carry the warning; screen:\n%s", screenText(sim))
			}
		})
	}
}

func outcomeFollows(screen, step, outcome string) bool {
	i := strings.Index(screen, step)
	if i < 0 {
		return false
	}
	rest := screen[i+len(step):]
	j := strings.Index(rest, outcome)

	return j >= 0 && strings.Trim(rest[:j], ". ") == ""
}

func pressLinkedButton(t *testing.T, app *tui.App, sim tcell.SimulationScreen, label string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !app.LinkedButtonFocused(label) {
		if time.Now().After(deadline) {
			t.Fatalf("the focus never reached %q; screen:\n%s", label, screenText(sim))
		}
		sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
		time.Sleep(25 * time.Millisecond)
	}
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
}
