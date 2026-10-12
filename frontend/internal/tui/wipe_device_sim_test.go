package tui_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"haoma-frontend/internal/ipc"
)

var settingsRowsAboveAdvanced = []string{
	"Profile", "Chat defaults", "Files", "Security", "Tor",
	"Linked devices", "Notifications",
}

const testUnlockPIN = "4821"

type wipeAnswer int

const (
	wipeAnswerStarted wipeAnswer = iota

	wipeAnswerSilent

	wipeAnswerDropped
)

func openEraseBlock(t *testing.T) (sim tcell.SimulationScreen, sent chan ipc.Frame) {
	t.Helper()
	return openEraseBlockWith(t, wipeAnswerStarted)
}

func openEraseBlockWith(t *testing.T, answer wipeAnswer) (sim tcell.SimulationScreen, sent chan ipc.Frame) {
	t.Helper()
	sent = make(chan ipc.Frame, 1)
	dir, addr, cancel := spinScriptedIPCServer(t, func(_ context.Context, s *ipc.Session) {
		replies := map[ipc.FrameType]reply{
			ipc.FrameDeviceLinksList: {ipc.FrameDeviceLinksListed, ipc.DeviceLinksListedResponse{}},
			ipc.FrameGetDestroyPIN:   {ipc.FrameDestroyPINListed, ipc.DestroyPINListedResponse{}},

			ipc.FrameWipeDevice: {ipc.FrameDeviceWiping, ipc.DeviceWipingResponse{Started: true}},
		}
		for {
			f, err := s.Recv()
			if err != nil {
				return
			}
			if f.Type == ipc.FrameWipeDevice {
				select {
				case sent <- f:
				default:
				}
				switch answer {
				case wipeAnswerSilent:

					continue
				case wipeAnswerDropped:

					return
				}
			}
			r, ok := replies[f.Type]
			if !ok {
				continue
			}
			out, err := ipc.NewFrame(r.Type, f.ID, r.Payload)
			if err != nil || s.Send(out) != nil {
				return
			}
		}
	})
	t.Cleanup(cancel)

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{pin: testUnlockPIN}
	sim.SetSize(floorCols, floorRows)

	typeCommand(sim, "/settings")
	waitForScreen(t, sim, "Chat defaults")
	for range settingsRowsAboveAdvanced {
		sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	}
	waitForScreen(t, sim, "Erase this device")

	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
	return sim, sent
}

func typeInto(sim tcell.SimulationScreen, s string) {
	for _, r := range s {
		sim.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
}

func pressErase(sim tcell.SimulationScreen, fieldsLeft int) {
	for i := 0; i < fieldsLeft; i++ {
		sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	}
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
}

func answerBoth(t *testing.T, sim tcell.SimulationScreen) {
	t.Helper()
	typeInto(sim, testUnlockPIN)
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	typeInto(sim, "ERASE")
}

func TestWipeDevice_TheWordIsRequired(t *testing.T) {
	sim, sent := openEraseBlock(t)

	typeInto(sim, testUnlockPIN)
	pressErase(sim, 2)

	waitForScreen(t, sim, "Type ERASE in capitals")
	select {
	case f := <-sent:
		t.Fatalf("the device was erased with no confirmation word; frame: %+v\n%s", f, screenText(sim))
	case <-time.After(2 * time.Second):
	}
}

func TestWipeDevice_TheUnlockPINIsRequired(t *testing.T) {
	sim, sent := openEraseBlock(t)

	typeInto(sim, "0000")
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	typeInto(sim, "ERASE")
	pressErase(sim, 1)

	waitForScreen(t, sim, "Wrong PIN")
	select {
	case f := <-sent:
		t.Fatalf("the device was erased on a wrong PIN; frame: %+v\n%s", f, screenText(sim))
	case <-time.After(2 * time.Second):
	}
}

func TestWipeDevice_BothAnswersSendTheRequest(t *testing.T) {
	sim, sent := openEraseBlock(t)

	answerBoth(t, sim)
	pressErase(sim, 1)

	select {
	case <-sent:
	case <-time.After(10 * time.Second):
		t.Fatalf("both answers were right and nothing was sent:\n%s", screenText(sim))
	}
}

func TestWipeDevice_ARefusalClearsWhatWasTyped(t *testing.T) {
	sim, _ := openEraseBlock(t)

	typeInto(sim, testUnlockPIN)
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	typeInto(sim, "zzz")
	waitForScreen(t, sim, "zzz")
	pressErase(sim, 1)

	waitForScreen(t, sim, "Type ERASE in capitals")
	waitForScreenGone(t, sim, "zzz")
}

func TestWipeDevice_LeavingTheBlockClearsBothAnswers(t *testing.T) {
	sim, sent := openEraseBlock(t)

	typeInto(sim, testUnlockPIN)
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	typeInto(sim, "zzz")
	waitForScreen(t, sim, "zzz")
	waitForScreen(t, sim, "****")

	sim.InjectKey(tcell.KeyEscape, 0, tcell.ModNone)

	waitForScreenGone(t, sim, "zzz")
	waitForScreenGone(t, sim, "****")
	select {
	case f := <-sent:
		t.Fatalf("leaving the block sent an erase request; frame: %+v\n%s", f, screenText(sim))
	case <-time.After(time.Second):
	}
}

func TestWipeDevice_TheWholeBodyIsOnScreenAtTheFloor(t *testing.T) {
	sim, _ := openEraseBlock(t)
	waitForScreen(t, sim, "Attachments you saved manually will remain where they are.")

	rows := strings.Split(screenText(sim), "\n")
	last := 0
	for _, line := range []string{
		"Erase this device:",
		"- identity & contacts",
		"- chats & attachments",
		"- passphrase & PIN",
		"It cannot be undone. There is no backup.",
		"After the erase this device is empty, like a new install.",
		"Your other devices stay intact.",
		"This app closes when the erase starts.",
		"Attachments you saved manually will remain where they are.",
	} {
		row := rowContaining(t, rows, line)
		if row < last {
			t.Fatalf("%q is drawn above the line before it; screen:\n%s", line, screenText(sim))
		}
		last = row
	}

	for _, label := range []string{"Unlock PIN", "Type ERASE"} {
		if row := rowContaining(t, rows, label); row <= last {
			t.Fatalf("the %q row is inside the body; screen:\n%s", label, screenText(sim))
		}
	}
	if rowContaining(t, rows, "  Erase this device") <= rowContaining(t, rows, "Type ERASE") {
		t.Fatalf("the button is not under the fields; screen:\n%s", screenText(sim))
	}
}

func TestWipeDevice_TheFieldsStayInsideTheBoxAtTheFloor(t *testing.T) {
	sim, _ := openEraseBlock(t)

	answerBoth(t, sim)
	waitForScreen(t, sim, "ERASE")

	rows := strings.Split(screenText(sim), "\n")
	ref := rowContaining(t, rows, "Attachments you saved manually will remain where they are.")
	for _, label := range []string{"Unlock PIN", "Type ERASE"} {
		row := rowContaining(t, rows, label)
		for _, col := range borderColumns(rows[ref]) {
			if !isBorderRune(([]rune(rows[row]))[col]) {
				t.Fatalf("the %q row lost the border at column %d; screen:\n%s",
					label, col, screenText(sim))
			}
		}
	}
}

func TestWipeDevice_AnUnansweredRequestDoesNotSayTheEraseIsRunning(t *testing.T) {
	sim, sent := openEraseBlockWith(t, wipeAnswerSilent)

	answerBoth(t, sim)
	pressErase(sim, 1)

	select {
	case <-sent:
	case <-time.After(10 * time.Second):
		t.Fatalf("the request never reached the fixture:\n%s", screenText(sim))
	}
	waitForScreen(t, sim, "The erase request was sent.")
	if strings.Contains(screenText(sim), "Erasing this device") {
		t.Fatalf("the screen says the erase is running before the daemon answered:\n%s", screenText(sim))
	}
}

func TestWipeDevice_TheStartedSentenceWaitsForTheDaemon(t *testing.T) {
	sim, sent := openEraseBlock(t)

	answerBoth(t, sim)
	pressErase(sim, 1)

	select {
	case <-sent:
	case <-time.After(10 * time.Second):
		t.Fatalf("the request never reached the fixture:\n%s", screenText(sim))
	}
	waitForScreen(t, sim, "Erasing this device")
}

func TestWipeDevice_ALostAnswerDoesNotClaimTheDeviceIsUnchanged(t *testing.T) {
	sim, sent := openEraseBlockWith(t, wipeAnswerDropped)

	answerBoth(t, sim)
	pressErase(sim, 1)

	select {
	case <-sent:
	case <-time.After(10 * time.Second):
		t.Fatalf("the request never reached the fixture:\n%s", screenText(sim))
	}
	waitForScreen(t, sim, "The erase may have started.")

	waitForScreen(t, sim, "it did not.")
	if strings.Contains(screenText(sim), "This device is unchanged") {
		t.Fatalf("a lost answer was worded as a refusal:\n%s", screenText(sim))
	}
}

func TestWipeDevice_ARefusedRequestSaysTheDeviceIsUnchanged(t *testing.T) {
	sent := make(chan ipc.Frame, 1)
	dir, addr, cancel := spinScriptedIPCServer(t, func(_ context.Context, s *ipc.Session) {
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
			case ipc.FrameWipeDevice:
				select {
				case sent <- f:
				default:
				}
				out, err = ipc.NewFrame(ipc.FrameError, f.ID, ipc.ErrorPayload{
					Code:    "not_ready",
					Message: "the store is not open",
				})
			default:
				continue
			}
			if err != nil || s.Send(out) != nil {
				return
			}
		}
	})
	t.Cleanup(cancel)

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{pin: testUnlockPIN}
	sim.SetSize(floorCols, floorRows)
	typeCommand(sim, "/settings")
	waitForScreen(t, sim, "Chat defaults")
	for range settingsRowsAboveAdvanced {
		sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	}
	waitForScreen(t, sim, "Erase this device")
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)

	answerBoth(t, sim)
	pressErase(sim, 1)

	select {
	case <-sent:
	case <-time.After(10 * time.Second):
		t.Fatalf("the request never reached the fixture:\n%s", screenText(sim))
	}

	waitForScreen(t, sim, "The daemon refused the request.")
	waitForScreen(t, sim, "unchanged.")
	if strings.Contains(screenText(sim), "may have started") {
		t.Fatalf("a refusal was worded as a lost answer:\n%s", screenText(sim))
	}
}
