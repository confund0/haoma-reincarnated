package tui_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/tui"
	"haoma-frontend/internal/vault"
)

type stubVault struct{ pin string }

func (stubVault) ChangePassphrase(_, _ []byte) error              { return nil }
func (stubVault) ChangePIN(_, _ string) error                     { return nil }
func (stubVault) SetIdleAction(string) error                      { return nil }
func (stubVault) SetIdleTimeoutSeconds(int) error                 { return nil }
func (stubVault) SetPinValiditySec(int) error                     { return nil }
func (stubVault) SetTorPassword(string) error                     { return nil }
func (stubVault) IsInsecureDefaultPassphrase() bool               { return false }
func (stubVault) IsInsecureDefaultPIN() bool                      { return false }
func (stubVault) SetNotifyShellEnabled(bool) error                { return nil }
func (stubVault) SetNotifyShowSender(bool) error                  { return nil }
func (stubVault) SetNotifyShowBody(bool) error                    { return nil }
func (stubVault) SetNotificationsOnLock(bool) error               { return nil }
func (stubVault) SetNotifyPersistUntilOpen(bool) error            { return nil }
func (stubVault) SetNotifyDeepLink(bool) error                    { return nil }
func (stubVault) SetThreatProfile(string) error                   { return nil }
func (stubVault) SetPanicAction(string) error                     { return nil }
func (stubVault) ApplyThreatPreset(string) error                  { return nil }
func (stubVault) SetDefaultSaveDir(string) error                  { return nil }
func (stubVault) SetDefaultAttachStartDir(string) error           { return nil }
func (stubVault) Settings() ipc.Settings                          { return ipc.Settings{} }
func (stubVault) Mutate(string, func(*vault.Payload) error) error { return nil }
func (v stubVault) CheckPIN(s string) bool                        { return v.pin != "" && s == v.pin }

func TestSettings_LinkedDevicesPageOpensOnASoloDevice(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, replyScript(map[ipc.FrameType]reply{
		ipc.FrameDeviceLinksList: {ipc.FrameDeviceLinksListed, ipc.DeviceLinksListedResponse{}},
		ipc.FrameGetDestroyPIN:   {ipc.FrameDestroyPINListed, ipc.DestroyPINListedResponse{}},
	}))
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}

	typeCommand(sim, "/settings")
	waitForScreen(t, sim, "Chat defaults")

	for i := 0; i < len(settingsRowsAboveLinkedDevices); i++ {
		sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	}
	waitForScreen(t, sim, "This device has no linked device")
	waitForScreen(t, sim, "Link a new device")
	waitForScreen(t, sim, "Join a link invitation")
}

func TestSettings_LinkedDevicesPageOnALinkedDevice(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, replyScript(map[ipc.FrameType]reply{
		ipc.FrameDeviceLinksList: {ipc.FrameDeviceLinksListed, ipc.DeviceLinksListedResponse{
			Devices: []ipc.DeviceLinkEntry{{PeerID: "peer-aaa111"}},
		}},
		ipc.FrameGetDestroyPIN: {ipc.FrameDestroyPINListed, ipc.DestroyPINListedResponse{Set: true}},
	}))
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}

	typeCommand(sim, "/settings")
	waitForScreen(t, sim, "Chat defaults")
	for i := 0; i < len(settingsRowsAboveLinkedDevices); i++ {
		sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	}
	waitForScreen(t, sim, "Destroy PIN: set")
	waitForScreen(t, sim, "Verify destroy PIN")
}

var settingsRowsAboveLinkedDevices = []string{
	"Profile", "Chat defaults", "Files", "Security", "Tor",
}

func TestSettings_LinkNewDeviceOpensTheCollectState(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, replyScript(map[ipc.FrameType]reply{
		ipc.FrameDeviceLinksList: {ipc.FrameDeviceLinksListed, ipc.DeviceLinksListedResponse{}},
		ipc.FrameGetDestroyPIN:   {ipc.FrameDestroyPINListed, ipc.DestroyPINListedResponse{}},
	}))
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}

	typeCommand(sim, "/settings")
	waitForScreen(t, sim, "Chat defaults")
	for range settingsRowsAboveLinkedDevices {
		sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	}
	waitForScreen(t, sim, "Link a new device")

	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)

	waitForScreen(t, sim, "PIN for THIS device")
	waitForScreen(t, sim, "PIN for OTHER device")
	waitForScreen(t, sim, "Proceed")
}

func TestSettings_TabReachesProceedFromTheFields(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, replyScript(map[ipc.FrameType]reply{
		ipc.FrameDeviceLinksList: {ipc.FrameDeviceLinksListed, ipc.DeviceLinksListedResponse{}},
		ipc.FrameGetDestroyPIN:   {ipc.FrameDestroyPINListed, ipc.DestroyPINListedResponse{}},
	}))
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}

	typeCommand(sim, "/settings")
	waitForScreen(t, sim, "Chat defaults")
	for range settingsRowsAboveLinkedDevices {
		sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	}
	waitForScreen(t, sim, "Link a new device")
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
	waitForScreen(t, sim, "PIN for THIS device")

	for _, r := range "1379" {
		sim.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	for _, r := range "2468" {
		sim.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}

	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)

	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
	waitForScreen(t, sim, "publishing the invitation")
}

func TestSettings_ALinkedDeviceOffersUnlinkAndDestroy(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, replyScript(map[ipc.FrameType]reply{
		ipc.FrameDeviceLinksList: {ipc.FrameDeviceLinksListed, ipc.DeviceLinksListedResponse{
			Devices: []ipc.DeviceLinkEntry{{PeerID: "peer-sibling"}},
		}},
		ipc.FrameGetDestroyPIN: {ipc.FrameDestroyPINListed, ipc.DestroyPINListedResponse{Set: true}},
	}))
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}

	typeCommand(sim, "/settings")
	waitForScreen(t, sim, "Chat defaults")
	for i := 0; i < len(settingsRowsAboveLinkedDevices); i++ {
		sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	}

	waitForLinkedButtons(t, app, sim, linkedDeviceButtons...)

	waitForScreen(t, sim, "Unlink: that device comes off this identity")
	waitForScreen(t, sim, "Destroy: the same")

	waitForScreen(t, sim, "[ ] I understand risks")

	waitForScreen(t, sim, "keeps its data")
}

func linkedDeviceReplies() map[ipc.FrameType]reply {
	return map[ipc.FrameType]reply{
		ipc.FrameDeviceLinksList: {ipc.FrameDeviceLinksListed, ipc.DeviceLinksListedResponse{
			Devices: []ipc.DeviceLinkEntry{{PeerID: "peer-sibling"}},
		}},
		ipc.FrameGetDestroyPIN: {ipc.FrameDestroyPINListed, ipc.DestroyPINListedResponse{Set: true}},
	}
}

func scriptWatchingUnlinks(unlinks chan<- ipc.Frame) func(context.Context, *ipc.Session) {
	replies := linkedDeviceReplies()
	return func(_ context.Context, s *ipc.Session) {
		for {
			f, err := s.Recv()
			if err != nil {
				return
			}
			if f.Type == ipc.FrameUnlinkDevice {
				select {
				case unlinks <- f:
				default:
				}
			}
			r, ok := replies[f.Type]
			if !ok {
				continue
			}
			out, err := ipc.NewFrame(r.Type, f.ID, r.Payload)
			if err != nil {
				return
			}
			if err := s.Send(out); err != nil {
				return
			}
		}
	}
}

const (
	floorCols = 102
	floorRows = 32
)

func openLinkedDevicesDomain(t *testing.T, app *tui.App, sim tcell.SimulationScreen) {
	t.Helper()
	sim.SetSize(floorCols, floorRows)
	typeCommand(sim, "/settings")
	waitForScreen(t, sim, "Chat defaults")
	for range settingsRowsAboveLinkedDevices {
		sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	}
	waitForLinkedButtons(t, app, sim, linkedDeviceButtons...)
}

var linkedDeviceButtons = []string{"Verify destroy PIN…", "Unlink", "Destroy…"}

func waitForLinkedButtons(t *testing.T, app *tui.App, sim tcell.SimulationScreen, want ...string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		got := app.LinkedDevicesButtons()
		if sameLabels(got, want) {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("the linked-devices page never drew the buttons %q; it drew %q; last frame:\n%s",
				want, got, screenText(sim))
		default:
			time.Sleep(25 * time.Millisecond)
		}
	}
}

func sameLabels(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func enterPaneAndTick(t *testing.T, sim tcell.SimulationScreen) {
	t.Helper()
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
	sim.InjectKey(tcell.KeyRune, ' ', tcell.ModNone)
	waitForScreen(t, sim, "[X] I understand risks")
}

func leaveAndReturn(t *testing.T, sim tcell.SimulationScreen) {
	t.Helper()
	sim.InjectKey(tcell.KeyEscape, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)

	waitForScreen(t, sim, "Show sender name in notifications")
	sim.InjectKey(tcell.KeyUp, 0, tcell.ModNone)

	waitForScreen(t, sim, "[ ] I understand risks")
}

func TestSettings_LeavingTheDomainDisarmsTheDangerTick(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, replyScript(linkedDeviceReplies()))
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}

	openLinkedDevicesDomain(t, app, sim)
	enterPaneAndTick(t, sim)
	leaveAndReturn(t, sim)

	waitForScreenGone(t, sim, "[X] I understand risks")
	waitForScreen(t, sim, "[ ] I understand risks")
}

func TestSettings_TheTickArmsUnlink(t *testing.T) {
	unlinks := make(chan ipc.Frame, 8)
	dir, addr, cancel := spinScriptedIPCServer(t, scriptWatchingUnlinks(unlinks))
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}

	openLinkedDevicesDomain(t, app, sim)
	enterPaneAndTick(t, sim)

	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)

	select {
	case <-unlinks:
	case <-time.After(10 * time.Second):
		t.Fatalf("a ticked box never armed unlink; last frame:\n%s", screenText(sim))
	}
}

func TestSettings_LeavingTheDomainDisarmsUnlink(t *testing.T) {
	unlinks := make(chan ipc.Frame, 8)
	dir, addr, cancel := spinScriptedIPCServer(t, scriptWatchingUnlinks(unlinks))
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}

	openLinkedDevicesDomain(t, app, sim)
	enterPaneAndTick(t, sim)
	leaveAndReturn(t, sim)

	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)

	waitForScreen(t, sim, `Tick "I understand risks" first.`)

	select {
	case <-unlinks:
		t.Fatalf("unlink ran on a box the operator never ticked; last frame:\n%s", screenText(sim))
	default:
	}
}

func TestSettings_DestroyIsFinishableFromTheKeyboard(t *testing.T) {
	unlinks := make(chan ipc.Frame, 8)
	dir, addr, cancel := spinScriptedIPCServer(t, scriptWatchingUnlinks(unlinks))
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}

	openLinkedDevicesDomain(t, app, sim)
	enterPaneAndTick(t, sim)

	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
	waitForScreen(t, sim, "destroy the other device")

	typeText(sim, otherTestDestroyPIN)
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)

	var got ipc.Frame
	select {
	case got = <-unlinks:
	case <-time.After(10 * time.Second):
		t.Fatalf("the destroy modal never reached the daemon; last frame:\n%s", screenText(sim))
	}
	var req ipc.UnlinkDeviceRequest
	if err := json.Unmarshal(got.Payload, &req); err != nil {
		t.Fatalf("unlink request could not be read: %v", err)
	}
	if req.PeerID != "peer-sibling" {
		t.Fatalf("destroy named %q, want the linked device", req.PeerID)
	}

	if req.PIN != otherTestDestroyPIN {
		t.Fatal("destroy carried no guard, so the modal's field never reached the frame")
	}
}

func TestSettings_CancellingTheGuardModalDisarmsUnlink(t *testing.T) {
	unlinks := make(chan ipc.Frame, 8)
	dir, addr, cancel := spinScriptedIPCServer(t, scriptWatchingUnlinks(unlinks))
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}

	openLinkedDevicesDomain(t, app, sim)
	enterPaneAndTick(t, sim)

	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
	waitForScreen(t, sim, "destroy the other device")

	sim.InjectKey(tcell.KeyEscape, 0, tcell.ModNone)
	waitForScreenGone(t, sim, "destroy the other device")

	waitForScreenGone(t, sim, "[X] "+tui.RiskLabel)
	waitForScreen(t, sim, "[ ] "+tui.RiskLabel)

	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
	tabToTheTick(t, sim)

	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)

	waitForScreen(t, sim, `Tick "I understand risks" first.`)

	select {
	case <-unlinks:
		t.Fatalf("unlink ran after the guard prompt was cancelled, on a box "+
			"the operator never ticked again; last frame:\n%s", screenText(sim))
	default:
	}
}

func TestSettings_TheWordsFieldStaysInsideTheBoxAtTheFloor(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, replyScript(map[ipc.FrameType]reply{
		ipc.FrameDeviceLinksList: {ipc.FrameDeviceLinksListed, ipc.DeviceLinksListedResponse{}},
		ipc.FrameGetDestroyPIN:   {ipc.FrameDestroyPINListed, ipc.DestroyPINListedResponse{}},
	}))
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}
	sim.SetSize(floorCols, floorRows)

	typeCommand(sim, "/link-join")
	waitForScreen(t, sim, "Type the seven words shown on your other device")

	typeText(sim, testDestroyPIN)
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	typeText(sim, otherTestDestroyPIN)
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	typeText(sim, strings.Join(testLinkWords, " "))
	waitForScreen(t, sim, strings.Join(testLinkWords, " "))

	rows := strings.Split(screenText(sim), "\n")
	pinRow := rowContaining(t, rows, "PIN for OTHER device")
	wordsRow := rowContaining(t, rows, "7 words")
	for _, col := range borderColumns(rows[pinRow]) {
		if !isBorderRune(rune(([]rune(rows[wordsRow]))[col])) {
			t.Fatalf("the words row lost the border at column %d; screen:\n%s", col, screenText(sim))
		}
	}
}

func rowContaining(t *testing.T, rows []string, want string) int {
	t.Helper()
	for i, r := range rows {
		if strings.Contains(r, want) {
			return i
		}
	}
	t.Fatalf("no row holds %q; screen:\n%s", want, strings.Join(rows, "\n"))
	return -1
}

func borderColumns(row string) []int {
	var out []int
	for i, r := range []rune(row) {
		if isBorderRune(r) {
			out = append(out, i)
		}
	}
	return out
}

func isBorderRune(r rune) bool { return r == '│' || r == '║' || r == '|' }

func TestSettings_LeavingTheDomainMidRunKeepsTheTypedFields(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, replyScript(map[ipc.FrameType]reply{
		ipc.FrameDeviceLinksList: {ipc.FrameDeviceLinksListed, ipc.DeviceLinksListedResponse{}},
		ipc.FrameGetDestroyPIN:   {ipc.FrameDestroyPINListed, ipc.DestroyPINListedResponse{}},
	}))
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}
	sim.SetSize(floorCols, floorRows)

	typeCommand(sim, "/link-join")
	waitForScreen(t, sim, "Type the seven words shown on your other device")

	typeText(sim, testDestroyPIN)
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	typeText(sim, otherTestDestroyPIN)
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	typeText(sim, strings.Join(testLinkWords, " "))

	waitForScreen(t, sim, strings.Join(testLinkWords, " "))

	sim.InjectKey(tcell.KeyEscape, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	waitForScreen(t, sim, "Show sender name in notifications")
	sim.InjectKey(tcell.KeyUp, 0, tcell.ModNone)
	waitForScreen(t, sim, "Type the seven words shown on your other device")

	if got := flatten(screenText(sim)); !strings.Contains(got, strings.Join(testLinkWords, " ")) {
		t.Fatalf("the seven words did not survive a walk off the domain; screen:\n%s", screenText(sim))
	}

	if got := flatten(screenText(sim)); !strings.Contains(got, "PIN for THIS device ****") ||
		!strings.Contains(got, "PIN for OTHER device ****") {
		t.Fatalf("a typed PIN did not survive a walk off the domain; screen:\n%s", screenText(sim))
	}
}

func TestSettings_ADeviceLinkAnswerDoesNotMoveTheCursor(t *testing.T) {
	release := make(chan struct{})
	dir, addr, cancel := spinScriptedIPCServer(t, func(ctx context.Context, s *ipc.Session) {
		for {
			f, err := s.Recv()
			if err != nil {
				return
			}
			switch f.Type {
			case ipc.FrameGetDestroyPIN:
				out, err := ipc.NewFrame(ipc.FrameDestroyPINListed, f.ID, ipc.DestroyPINListedResponse{})
				if err != nil || s.Send(out) != nil {
					return
				}
			case ipc.FrameDeviceLinksList:
				id := f.ID
				go func() {
					select {
					case <-release:
					case <-ctx.Done():
						return
					}
					out, err := ipc.NewFrame(ipc.FrameDeviceLinksListed, id, ipc.DeviceLinksListedResponse{
						Devices: []ipc.DeviceLinkEntry{{PeerID: "peer-sibling"}},
					})
					if err == nil {
						_ = s.Send(out)
					}
				}()
			}
		}
	})
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}
	sim.SetSize(floorCols, floorRows)

	typeCommand(sim, "/settings")
	waitForScreen(t, sim, "Self nick")
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
	typeText(sim, "halftyped")
	waitForScreen(t, sim, "halftyped")

	before := app.LinkedDevicesShape()

	close(release)

	deadline := time.After(10 * time.Second)
	for app.LinkedDevicesShape() == before {
		select {
		case <-deadline:
			t.Fatalf("the linked-devices page was never built again; last frame:\n%s", screenText(sim))
		default:
			time.Sleep(25 * time.Millisecond)
		}
	}

	typeText(sim, "nick")
	waitForScreen(t, sim, "halftypednick")
}

func dangerTickCells(sim tcell.SimulationScreen) (bracket, mark tcell.Style, found bool) {
	cells, w, h := simCells(sim)
	runeAt := func(row, col int) rune {
		r := cells[row*w+col].Runes
		if len(r) == 0 || r[0] == 0 {
			return ' '
		}
		return r[0]
	}
	label := []rune(tui.RiskLabel)
	for row := 0; row < h; row++ {
		for col := 2; col+len(label) < w; col++ {
			match := true
			for i, want := range label {
				if runeAt(row, col+i) != want {
					match = false
					break
				}
			}
			if !match || runeAt(row, col-2) != ']' {
				continue
			}
			return cells[row*w+col-4].Style, cells[row*w+col-3].Style, true
		}
	}
	return tcell.StyleDefault, tcell.StyleDefault, false
}

func tabToTheTick(t *testing.T, sim tcell.SimulationScreen) {
	t.Helper()

	for i := 0; i < 5; i++ {
		if b, _, found := dangerTickCells(sim); found {
			if _, _, attr := b.Decompose(); attr&tcell.AttrBold != 0 {
				return
			}
		}
		sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)

		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("the danger tick never took the cursor; last frame:\n%s", screenText(sim))
}

func waitForTickCursor(t *testing.T, sim tcell.SimulationScreen, want bool) (bracket, mark tcell.Style) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("the danger tick never drew with cursor=%t; last frame:\n%s", want, screenText(sim))
		default:
			b, m, found := dangerTickCells(sim)
			if found {
				if _, _, attr := b.Decompose(); (attr&tcell.AttrBold != 0) == want {
					return b, m
				}
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
}

func TestSettings_TheTickCursorIsDrawnApartFromTheTick(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, replyScript(linkedDeviceReplies()))
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}

	openLinkedDevicesDomain(t, app, sim)

	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
	onBracket, onMark := waitForTickCursor(t, sim, true)

	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	offBracket, offMark := waitForTickCursor(t, sim, false)

	onFG, _, _ := onBracket.Decompose()
	if onFG != tui.ColorCheckboxCursor {
		t.Fatalf("the bracket under the cursor is drawn %v, want the cursor token %v",
			onFG, tui.ColorCheckboxCursor)
	}

	offFG, _, _ := offBracket.Decompose()
	if offFG == tui.ColorCheckboxCursor {
		t.Fatalf("the bracket is drawn in the cursor colour %v with no cursor on the row", offFG)
	}

	if onMark != offMark {
		t.Fatal("the cursor changed the cell that carries the tick")
	}
}

func TestSettings_StopWaitingRefusesWithoutTheTick(t *testing.T) {
	stops := make(chan ipc.Frame, 8)
	waiting := ipc.DestroyPINListedResponse{
		Waiting: &ipc.DestroyWaiting{
			PeerID:   "peer-destroyed",
			Deadline: time.Now().Add(90 * time.Minute).Unix(),
		},
	}
	dir, addr, cancel := spinScriptedIPCServer(t, func(_ context.Context, s *ipc.Session) {
		replies := map[ipc.FrameType]reply{
			ipc.FrameDeviceLinksList: {ipc.FrameDeviceLinksListed, ipc.DeviceLinksListedResponse{
				Devices: []ipc.DeviceLinkEntry{{PeerID: "peer-destroyed"}},
			}},
			ipc.FrameGetDestroyPIN: {ipc.FrameDestroyPINListed, waiting},

			ipc.FrameStopDestroyWaiting: {ipc.FrameDestroyWaitingStopped, struct{}{}},
		}
		for {
			f, err := s.Recv()
			if err != nil {
				return
			}
			if f.Type == ipc.FrameStopDestroyWaiting {
				select {
				case stops <- f:
				default:
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
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}
	sim.SetSize(floorCols, floorRows)

	typeCommand(sim, "/settings")
	waitForScreen(t, sim, "Chat defaults")
	for range settingsRowsAboveLinkedDevices {
		sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	}
	waitForScreen(t, sim, "Stop waiting")
	waitForScreen(t, sim, "[ ] "+tui.RiskLabel)

	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)

	waitForScreen(t, sim, `Tick "I understand risks" first.`)
	select {
	case <-stops:
		t.Fatalf("the wait was stopped on a box the operator never ticked; last frame:\n%s", screenText(sim))
	default:
	}

	sim.InjectKey(tcell.KeyBacktab, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyRune, ' ', tcell.ModNone)
	waitForScreen(t, sim, "[X] "+tui.RiskLabel)
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)

	select {
	case <-stops:
	case <-time.After(10 * time.Second):
		t.Fatalf("a ticked box never armed stop waiting; last frame:\n%s", screenText(sim))
	}
	waitForScreen(t, sim, "Stopped waiting.")
}
