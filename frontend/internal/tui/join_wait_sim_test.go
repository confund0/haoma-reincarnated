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

func heldJoinScript(join ipc.FrameType, code string, hold time.Duration, joins *atomic.Int32) func(context.Context, *ipc.Session) {
	return func(_ context.Context, s *ipc.Session) {
		for {
			f, err := s.Recv()
			if err != nil {
				return
			}
			var out ipc.Frame
			switch f.Type {
			case ipc.FrameDeviceLinksList:
				out, err = ipc.NewFrame(ipc.FrameDeviceLinksListed, f.ID, ipc.DeviceLinksListedResponse{})
			case ipc.FrameGetDestroyPIN:
				out, err = ipc.NewFrame(ipc.FrameDestroyPINListed, f.ID, ipc.DestroyPINListedResponse{})
			case join:
				joins.Add(1)
				id := f.ID
				go func() {
					time.Sleep(hold)
					out, err := ipc.NewFrame(ipc.FrameError, id, ipc.ErrorPayload{Code: code, Message: "the rendezvous did not answer"})
					if err == nil {
						_ = s.Send(out)
					}
				}()
				continue
			default:
				continue
			}
			if err != nil || s.Send(out) != nil {
				return
			}
		}
	}
}

func TestJoinWait_ALinkJoinThatTorDidNotCarryKeepsTheForm(t *testing.T) {
	var joins atomic.Int32
	dir, addr, cancel := spinScriptedIPCServer(t,
		heldJoinScript(ipc.FrameDeviceLinkAccept, ipc.ReasonBackendDown, 2*time.Second, &joins))
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}
	sim.SetSize(120, 50)

	typeCommand(sim, "/link-join")
	waitForScreen(t, sim, "Type the seven words shown on your other device")
	typeText(sim, testDestroyPIN)
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	typeText(sim, otherTestDestroyPIN)
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	words := strings.Join(testLinkWords, " ")
	typeText(sim, words)
	waitForScreen(t, sim, words)

	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)

	waitForScreen(t, sim, "contacting your other device")
	waitForScreen(t, sim, "seconds left.")

	waitForScreen(t, sim, "Tor is slow at the moment. Try again in a couple of minutes, or check the seven")
	waitForScreen(t, sim, "Proceed")
	got := flatten(screenText(sim))
	if !strings.Contains(got, words) {
		t.Fatalf("the seven words did not survive the failed join; screen:\n%s", screenText(sim))
	}
	if !strings.Contains(got, "PIN for THIS device ****") || !strings.Contains(got, "PIN for OTHER device ****") {
		t.Fatalf("a typed PIN did not survive the failed join; screen:\n%s", screenText(sim))
	}
	if strings.Contains(got, "Waiting for Tor") {
		t.Fatalf("the countdown is still drawn after the answer; screen:\n%s", screenText(sim))
	}

	for range 3 {
		sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	}
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
	deadline := time.Now().Add(10 * time.Second)
	for joins.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("Proceed on the kept form sent no second join; screen:\n%s", screenText(sim))
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestJoinWait_APairingJoinRefusalSaysWhatToDo(t *testing.T) {
	for _, c := range []struct {
		code  string
		line  string
		reuse bool
	}{
		{ipc.ReasonOnionAcceptFailed, "Tor is slow at the moment. Try again in a couple of minutes, or check the seven words.", true},
		{ipc.ReasonJoinWordsInvalid, "One of the words is wrong, so check the seven words and try again.", true},
		{ipc.ReasonJoinInvitationSpent, "This invitation was already used or has run out, so ask for a new one.", false},
	} {
		t.Run(c.code, func(t *testing.T) {
			var joins atomic.Int32
			dir, addr, cancel := spinScriptedIPCServer(t,
				heldJoinScript(ipc.FramePairOnionAccept, c.code, 2*time.Second, &joins))
			defer cancel()

			_, sim := launchApp(t, dir, addr)
			sim.SetSize(120, 40)

			words := strings.Join(testLinkWords, " ")
			typeCommand(sim, "/accept-tor "+words)
			waitForScreen(t, sim, "Waiting for Tor: ")
			waitForScreen(t, sim, "seconds left.")

			waitForScreen(t, sim, c.line)
			if !c.reuse {

				time.Sleep(300 * time.Millisecond)
				if got := flatten(screenText(sim)); strings.Contains(got, "> /accept-tor") {
					t.Fatalf("the words of a spent invitation were put back; screen:\n%s", screenText(sim))
				}
				return
			}
			waitForScreen(t, sim, "> /accept-tor "+words)
			if got := flatten(screenText(sim)); strings.Contains(got, "Waiting for Tor") {
				t.Fatalf("the countdown is still in the bar after the answer; screen:\n%s", screenText(sim))
			}

			sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
			deadline := time.Now().Add(10 * time.Second)
			for joins.Load() < 2 {
				if time.Now().After(deadline) {
					t.Fatalf("Enter on the kept line sent no second join; screen:\n%s", screenText(sim))
				}
				time.Sleep(25 * time.Millisecond)
			}
		})
	}
}

func TestJoinWait_ALinkWithoutTheDetailsEndsLinked(t *testing.T) {
	defer tui.SetSaltWaits(600*time.Millisecond, 100*time.Millisecond, 200*time.Millisecond)()

	dir, addr, cancel := spinScriptedIPCServer(t, replyScript(map[ipc.FrameType]reply{
		ipc.FrameDeviceLinksList: {ipc.FrameDeviceLinksListed, ipc.DeviceLinksListedResponse{}},
		ipc.FrameDeviceLinkAccept: {ipc.FrameDeviceLinkAccepted, ipc.DeviceLinkAcceptedResponse{
			PeerID: "peer-aaa111", AdoptedFingerprint: "fp-deadbeef",
		}},
		ipc.FrameSetDestroyPIN: {ipc.FrameDestroyPINSet, ipc.DestroyPINSetResponse{Accepted: true}},

		ipc.FrameGetDestroyPIN: {ipc.FrameDestroyPINListed, ipc.DestroyPINListedResponse{
			Set:      true,
			Siblings: []ipc.DestroyPINSiblingEntry{{PeerID: "peer-aaa111", SaltHeld: false}},
		}},
	}))
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}
	sim.SetSize(120, 50)

	typeCommand(sim, "/link-join")
	waitForScreen(t, sim, "Type the seven words shown on your other device")
	typeText(sim, testDestroyPIN)
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	typeText(sim, otherTestDestroyPIN)
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	typeText(sim, strings.Join(testLinkWords, " "))
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)

	waitForScreen(t, sim, "waiting for your other device")

	waitForScreen(t, sim, "Your other device's PIN: details not")
	waitForScreen(t, sim, "received yet. Check it later from this page.")
	waitForScreen(t, sim, "Close")
	if got := flatten(screenText(sim)); strings.Contains(got, "FAILED") {
		t.Fatalf("a run whose link came up shows a failed step; screen:\n%s", screenText(sim))
	}
}

func statusRow(sim tcell.SimulationScreen) (int, string) {
	for i, row := range strings.Split(screenText(sim), "\n") {
		if strings.Contains(row, "[UI]") {
			return i, row
		}
	}
	return -1, ""
}

func TestJoinWait_ThePairingCountdownIsRightAlignedAndMovesNoChip(t *testing.T) {
	var joins atomic.Int32
	dir, addr, cancel := spinScriptedIPCServer(t,
		heldJoinScript(ipc.FramePairOnionAccept, ipc.ReasonOnionAcceptFailed, 2*time.Second, &joins))
	defer cancel()

	_, sim := launchApp(t, dir, addr)
	const width, height = 120, 40
	sim.SetSize(width, height)

	typeCommand(sim, "/accept-tor "+strings.Join(testLinkWords, " "))
	waitForScreen(t, sim, "seconds left.")

	row, during := statusRow(sim)
	if row != height-2 {
		t.Fatalf("the status bar is on row %d, not above the input line:\n%s", row, screenText(sim))
	}
	if !strings.Contains(during, "Waiting for Tor: ") {
		t.Fatalf("the countdown is not on the status bar row:\n%q", during)
	}

	runes := []rune(during)
	trimmed := []rune(strings.TrimRight(during, " "))
	if len(runes) != width || len(trimmed) != width-1 || !strings.HasSuffix(string(trimmed), "left.") {
		t.Fatalf("the countdown does not end at the right edge (%d of %d columns):\n%q", len(trimmed), width, during)
	}
	meCol := strings.Index(during, "[me: ")
	if meCol < 0 || strings.Index(during, "Waiting for Tor") <= meCol {
		t.Fatalf("the countdown is not after the chips:\n%q", during)
	}

	waitForScreenGone(t, sim, "Waiting for Tor")
	afterRow, after := statusRow(sim)
	if afterRow != row {
		t.Fatalf("the status bar moved from row %d to row %d", row, afterRow)
	}

	chips := strings.TrimRight(during[:strings.Index(during, "Waiting for Tor")], " ")
	if got := strings.TrimRight(after, " "); got != chips {
		t.Fatalf("the countdown moved the chips:\nduring %q\nafter  %q", chips, got)
	}
}
