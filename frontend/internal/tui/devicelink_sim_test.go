package tui_test

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/ipcclient"
	"haoma-frontend/internal/tui"
)

func spinScriptedIPCServer(t *testing.T, script func(ctx context.Context, s *ipc.Session)) (dir, addr string, cancel func()) {
	t.Helper()
	dir = t.TempDir()
	tlsCfg, err := ipc.LoadOrCreateTLS(dir)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := ipc.LoadOrCreateToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := ipc.NewServer(tok)
	srv.OnSession = script

	srv.WelcomeAugment = func(wp ipc.WelcomePayload) ipc.WelcomePayload {
		wp.DefaultRetentionSec = 28 * 24 * 3600
		wp.DefaultSendReceipts = true
		return wp
	}
	httpSrv := &http.Server{Handler: srv.Handler(), TLSConfig: tlsCfg}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = httpSrv.Serve(tls.NewListener(ln, tlsCfg)) }()
	return dir, ln.Addr().String(), func() { _ = httpSrv.Close() }
}

func replyScript(replies map[ipc.FrameType]struct {
	Type    ipc.FrameType
	Payload any
}) func(context.Context, *ipc.Session) {
	return func(_ context.Context, s *ipc.Session) {
		for {
			f, err := s.Recv()
			if err != nil {
				return
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

type reply = struct {
	Type    ipc.FrameType
	Payload any
}

func launchApp(t *testing.T, dir, addr string) (*tui.App, tcell.SimulationScreen) {
	t.Helper()
	client, err := ipcclient.New(ipcclient.Config{
		FrontendDir: dir,
		Addr:        addr,
		ClientName:  "linktest",
	})
	if err != nil {
		t.Fatalf("ipcclient.New: %v", err)
	}
	go func() { _ = client.Run() }()

	app := tui.New(client)
	sim := tcell.NewSimulationScreen("")
	app.SetScreen(sim)

	stopped := registerSimApp(t, sim, app)

	runErr := make(chan error, 1)
	go func() { runErr <- app.Run(); stopped() }()
	t.Cleanup(app.Stop)

	waitForScreen(t, sim, "connected")

	return app, sim
}

func waitForScreen(t *testing.T, sim tcell.SimulationScreen, want string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("never rendered %q; last frame:\n%s", want, screenText(sim))
		default:
			if strings.Contains(flatten(screenText(sim)), flatten(want)) {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
}

func flatten(s string) string { return strings.Join(strings.Fields(s), " ") }

func typeCommand(sim tcell.SimulationScreen, cmd string) {
	for _, r := range cmd {
		sim.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
}

func typeText(sim tcell.SimulationScreen, text string) {
	for _, r := range text {
		sim.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
}

var testLinkWords = []string{"acid", "acorn", "acre", "acts", "afar", "affix", "aged"}

const testDestroyPIN = "1379"

const otherTestDestroyPIN = "2468"

func enterDestroyPIN(t *testing.T, sim tcell.SimulationScreen, pin string) {
	t.Helper()
	waitForScreen(t, sim, "Destroy PIN for this device")
	for _, r := range pin {
		sim.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	for _, r := range pin {
		sim.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
}

func TestDeviceLink_OfferWarnsThenCollectsBothPINs(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, replyScript(map[ipc.FrameType]reply{
		ipc.FrameDeviceLinksList: {ipc.FrameDeviceLinksListed, ipc.DeviceLinksListedResponse{}},
		ipc.FrameGetDestroyPIN:   {ipc.FrameDestroyPINListed, ipc.DestroyPINListedResponse{}},
		ipc.FrameSetDestroyPIN:   {ipc.FrameDestroyPINSet, ipc.DestroyPINSetResponse{Accepted: true}},
		ipc.FrameDeviceLinkOffer: {ipc.FrameDeviceLinkStarted, ipc.DeviceLinkStartedResponse{
			HandleID:  "handle-7",
			Words:     testLinkWords,
			ExpiresAt: time.Now().Add(10 * time.Minute).Unix(),
		}},
	}))
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}

	typeCommand(sim, "/link-device")

	waitForScreen(t, sim, "copies this device's identity key")
	if got := flatten(screenText(sim)); strings.Contains(got, strings.Join(testLinkWords, " ")) {
		t.Fatalf("words revealed before the operator confirmed:\n%s", screenText(sim))
	}

	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
	waitForScreen(t, sim, "Set a PIN for each device")
	waitForScreen(t, sim, "PIN for THIS device")
	waitForScreen(t, sim, "PIN for OTHER device")

	if got := flatten(screenText(sim)); strings.Contains(got, strings.Join(testLinkWords, " ")) {
		t.Fatalf("words revealed before the run started:\n%s", screenText(sim))
	}
}

func TestDeviceLink_EveryFieldIsOnScreenAtOnce(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, replyScript(map[ipc.FrameType]reply{
		ipc.FrameDeviceLinksList: {ipc.FrameDeviceLinksListed, ipc.DeviceLinksListedResponse{}},
		ipc.FrameGetDestroyPIN:   {ipc.FrameDestroyPINListed, ipc.DestroyPINListedResponse{}},
	}))
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}

	typeCommand(sim, "/link-device")
	waitForScreen(t, sim, "copies this device's identity key")
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)

	waitForScreen(t, sim, "PIN for THIS device")
	waitForScreen(t, sim, "PIN for OTHER device")
	waitForScreen(t, sim, "7 words")
}

func TestDeviceLink_EqualPINsAreRefusedWhileTyping(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, replyScript(map[ipc.FrameType]reply{
		ipc.FrameDeviceLinksList: {ipc.FrameDeviceLinksListed, ipc.DeviceLinksListedResponse{}},
		ipc.FrameGetDestroyPIN:   {ipc.FrameDestroyPINListed, ipc.DestroyPINListedResponse{}},
	}))
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}

	typeCommand(sim, "/link-device")
	waitForScreen(t, sim, "copies this device's identity key")
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
	waitForScreen(t, sim, "Set a PIN for each device")

	typeText(sim, testDestroyPIN)
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	typeText(sim, testDestroyPIN)

	waitForScreen(t, sim, "The two PINs must differ")
}

func TestDeviceLink_OfferReportsReachabilityWhileItWaits(t *testing.T) {
	t.Skip("superseded by the linking flow (2026-08-22): " +
		"the probe lines now land in the flow's log area rather than the status\nwindow. Re-cover: a not-yet probe writes a waiting line, and a ready probe\nis what releases the words.")

	dir, addr, cancel := spinScriptedIPCServer(t, func(_ context.Context, s *ipc.Session) {
		for {
			f, err := s.Recv()
			if err != nil {
				return
			}

			if f.Type == ipc.FrameSetDestroyPIN {
				set, err := ipc.NewFrame(ipc.FrameDestroyPINSet, f.ID, ipc.DestroyPINSetResponse{Accepted: true})
				if err != nil || s.Send(set) != nil {
					return
				}
				continue
			}
			if f.Type != ipc.FrameDeviceLinkOffer {
				continue
			}
			started, err := ipc.NewFrame(ipc.FrameDeviceLinkStarted, f.ID, ipc.DeviceLinkStartedResponse{
				HandleID: "handle-7",
				Words:    testLinkWords,
			})
			if err != nil || s.Send(started) != nil {
				return
			}

			for i, p := range []ipc.DeviceLinkProbePush{
				{HandleID: "handle-7", Attempt: 2, Error: "unreachable"},
				{HandleID: "handle-7", Attempt: 3, Ready: true},
			} {
				if i > 0 {
					time.Sleep(time.Second)
				}
				push, err := ipc.NewFrame(ipc.FrameDeviceLinkProbe, "", p)
				if err != nil || s.Send(push) != nil {
					return
				}
			}
		}
	})
	defer cancel()

	_, sim := launchApp(t, dir, addr)

	typeCommand(sim, "/link-device")
	waitForScreen(t, sim, "copies this device's identity key")
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
	enterDestroyPIN(t, sim, testDestroyPIN)

	waitForScreen(t, sim, "still publishing this device's rendezvous (attempt 2)")

	waitForScreen(t, sim, "reachable")
	waitForScreen(t, sim, strings.Join(testLinkWords, " "))
	waitForScreen(t, sim, "carry these 7 words")

	if got := flatten(screenText(sim)); strings.Contains(got, string(ipc.FrameDeviceLinkProbe)) {
		t.Fatalf("the reachability push rendered as a raw frame name:\n%s", screenText(sim))
	}
}

func TestDeviceLink_OfferCancelKeepsWordsHidden(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, replyScript(map[ipc.FrameType]reply{

		ipc.FrameDeviceLinksList: {ipc.FrameDeviceLinksListed, ipc.DeviceLinksListedResponse{}},
		ipc.FrameSetDestroyPIN:   {ipc.FrameDestroyPINSet, ipc.DestroyPINSetResponse{Accepted: true}},
		ipc.FrameDeviceLinkOffer: {ipc.FrameDeviceLinkStarted, ipc.DeviceLinkStartedResponse{
			HandleID: "handle-7", Words: testLinkWords,
		}},
	}))
	defer cancel()

	app, sim := launchApp(t, dir, addr)

	app.VaultCtl = stubVault{}

	typeCommand(sim, "/link-device")
	waitForScreen(t, sim, "copies this device's identity key")

	sim.InjectKey(tcell.KeyEscape, 0, tcell.ModNone)

	time.Sleep(500 * time.Millisecond)
	if got := flatten(screenText(sim)); strings.Contains(got, strings.Join(testLinkWords, " ")) {
		t.Fatalf("words revealed after the operator backed out:\n%s", screenText(sim))
	}
}

func TestDeviceLink_JoinOnNonEmptyDeviceExplainsInPlainLanguage(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, replyScript(map[ipc.FrameType]reply{
		ipc.FrameDeviceLinksList: {ipc.FrameDeviceLinksListed, ipc.DeviceLinksListedResponse{}},
		ipc.FrameGetDestroyPIN:   {ipc.FrameDestroyPINListed, ipc.DestroyPINListedResponse{}},
		ipc.FrameDeviceLinkAccept: {ipc.FrameDeviceLinkFailed, ipc.DeviceLinkFailedPush{
			HandleID: "handle-7",
			Reason:   "device_not_empty",
		}},
	}))
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}

	typeCommand(sim, "/link-join")
	waitForScreen(t, sim, "Set a PIN for each device")

	typeText(sim, "1234")
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	typeText(sim, "5678")
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	typeText(sim, strings.Join(testLinkWords, " "))
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)

	waitForScreen(t, sim, "Use a device you have not set up yet")

	if got := flatten(screenText(sim)); strings.Contains(got, "device_not_empty") {
		t.Fatalf("raw refusal token leaked to the operator:\n%s", screenText(sim))
	}

	waitForScreen(t, sim, "Close")
	if got := screenText(sim); strings.ContainsAny(got, tui.SpinnerGlyphs()) {
		t.Fatalf("a finished run is still turning its marker:\n%s", got)
	}
}

func TestDeviceLink_JoinSuccessSurfacesTheSibling(t *testing.T) {
	t.Skip("superseded by the linking flow (2026-08-22): " +
		"the join no longer chains modals for the PINs; both are collected before\nanything starts. Re-cover: a completed join writes the peer id and the\nadopted fingerprint, and the flow goes on to set this device's PIN.")

	dir, addr, cancel := spinScriptedIPCServer(t, replyScript(map[ipc.FrameType]reply{
		ipc.FrameDeviceLinkAccept: {ipc.FrameDeviceLinkAccepted, ipc.DeviceLinkAcceptedResponse{
			PeerID:             "peer-aaa111",
			AdoptedFingerprint: "fp-deadbeef",
		}},
		ipc.FrameVerifyDestroyPIN: {ipc.FrameDestroyPINVerified, ipc.DestroyPINVerifiedResponse{
			Answered: true, Verified: true,
		}},

		ipc.FrameGetDestroyPIN: {ipc.FrameDestroyPINListed, ipc.DestroyPINListedResponse{
			Set: true,
			Siblings: []ipc.DestroyPINSiblingEntry{
				{PeerID: "peer-aaa111", SaltHeld: true},
			},
		}},
		ipc.FrameSetDestroyPIN: {ipc.FrameDestroyPINSet, ipc.DestroyPINSetResponse{Accepted: true}},
	}))
	defer cancel()

	_, sim := launchApp(t, dir, addr)

	typeCommand(sim, "/link-join "+strings.Join(testLinkWords, " "))

	waitForScreen(t, sim, "Destroy PIN of the other device")
	for _, r := range otherTestDestroyPIN {
		sim.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)

	waitForScreen(t, sim, "cannot be changed")
	waitForScreen(t, sim, otherTestDestroyPIN)

	enterDestroyPIN(t, sim, otherTestDestroyPIN)
	waitForScreen(t, sim, "must differ from the other")

	enterDestroyPIN(t, sim, testDestroyPIN)
	waitForScreen(t, sim, "destroy PIN set")
	waitForScreen(t, sim, "while you are holding both")

	waitForScreen(t, sim, "this device is now one of your own devices")
	waitForScreen(t, sim, "peer-aaa111")
	waitForScreen(t, sim, "fp-deadbeef")
}

func TestDeviceLink_TheCommandOpensTheSettingsPage(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, replyScript(map[ipc.FrameType]reply{
		ipc.FrameDeviceLinksList: {ipc.FrameDeviceLinksListed, ipc.DeviceLinksListedResponse{
			Devices: []ipc.DeviceLinkEntry{
				{PeerID: "peer-aaa111", LinkedAt: time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC).Unix()},
			},
		}},
		ipc.FrameGetDestroyPIN: {ipc.FrameDestroyPINListed, ipc.DestroyPINListedResponse{}},
	}))
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}

	typeCommand(sim, "/linked-devices")

	waitForScreen(t, sim, "Linked device: peer-aaa")

	waitForScreen(t, sim, "Set destroy PIN")

	if strings.Contains(screenText(sim), "my linked devices") {
		t.Fatalf("the read-only readout is still drawn")
	}
}

func TestDeviceLink_TheCommandOpensTheSettingsPageOnASoloDevice(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, replyScript(map[ipc.FrameType]reply{
		ipc.FrameDeviceLinksList: {ipc.FrameDeviceLinksListed, ipc.DeviceLinksListedResponse{}},
		ipc.FrameGetDestroyPIN:   {ipc.FrameDestroyPINListed, ipc.DestroyPINListedResponse{}},
	}))
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}

	typeCommand(sim, "/linked-devices")

	waitForScreen(t, sim, "Link a new device")
	waitForScreen(t, sim, "Join a link invitation")
}

func TestDeviceLink_CancelWithdrawsTheRememberedOffer(t *testing.T) {
	t.Skip("superseded by the linking flow (2026-08-22): " +
		"cancelling is a button on the flow's page now, not a slash command run\nfrom the status window. Re-cover: the button withdraws the offer this run\nmade and takes the run down.")

	dir, addr, cancel := spinScriptedIPCServer(t, replyScript(map[ipc.FrameType]reply{
		ipc.FrameSetDestroyPIN: {ipc.FrameDestroyPINSet, ipc.DestroyPINSetResponse{Accepted: true}},
		ipc.FrameDeviceLinkOffer: {ipc.FrameDeviceLinkStarted, ipc.DeviceLinkStartedResponse{
			HandleID: "handle-7", Words: testLinkWords,
		}},
		ipc.FrameDeviceLinkCancel: {ipc.FrameDeviceLinkCancelled, ipc.DeviceLinkCancelRequest{
			HandleID: "handle-7",
		}},
	}))
	defer cancel()

	_, sim := launchApp(t, dir, addr)

	typeCommand(sim, "/link-device")
	waitForScreen(t, sim, "copies this device's identity key")
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
	enterDestroyPIN(t, sim, testDestroyPIN)
	waitForScreen(t, sim, "handle: handle-7")

	typeCommand(sim, "/link-cancel")
	waitForScreen(t, sim, "offer withdrawn handle-7")
}

func TestDeviceLink_JoinWaitsForTheSaltThenDefersTheCheck(t *testing.T) {
	t.Skip("superseded by the linking flow (2026-08-22): " +
		"the readout moved from a modal into the linked-devices page. Re-cover: the\nwait is bounded, the step says what did not arrive, and the run offers a\nway out rather than a marker that never stops.")

	defer tui.SetSaltWaits(3*time.Second, 200*time.Millisecond, time.Second)()

	dir, addr, cancel := spinScriptedIPCServer(t, replyScript(map[ipc.FrameType]reply{
		ipc.FrameDeviceLinkAccept: {ipc.FrameDeviceLinkAccepted, ipc.DeviceLinkAcceptedResponse{
			PeerID: "peer-aaa111",
		}},
		ipc.FrameDeviceLinksList: {ipc.FrameDeviceLinksListed, ipc.DeviceLinksListedResponse{}},

		ipc.FrameGetDestroyPIN: {ipc.FrameDestroyPINListed, ipc.DestroyPINListedResponse{
			Set: true,
			Siblings: []ipc.DestroyPINSiblingEntry{
				{PeerID: "peer-aaa111", SaltHeld: false},
			},
		}},
	}))
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}

	typeCommand(sim, "/link-join "+strings.Join(testLinkWords, " "))
	waitForScreen(t, sim, "Destroy PIN of the other device")
	for _, r := range otherTestDestroyPIN {
		sim.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)

	waitForScreen(t, sim, "Settings, Linked devices")

	typeCommand(sim, "/settings")

	waitForScreen(t, sim, "Chat defaults")
	for range settingsRowsAboveLinkedDevices {
		sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	}

	waitForScreen(t, sim, "Contacting your other device")
	waitForScreen(t, sim, "Preparing the PIN check")

	waitForScreen(t, sim, "has not sent it yet")
	waitForScreen(t, sim, "failed")
	waitForScreen(t, sim, "Close")
}

func TestDeviceLink_TheDestroyInstructionTakesTheScreenDown(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, func(_ context.Context, s *ipc.Session) {

		go func() {
			for {
				if _, err := s.Recv(); err != nil {
					return
				}
			}
		}()

		time.Sleep(1500 * time.Millisecond)
		out, err := ipc.NewFrame(ipc.FrameDestroyDevice, "", ipc.DestroyDevicePush{
			Reason: "the other device commanded it",
		})
		if err != nil {
			return
		}
		_ = s.Send(out)
		<-make(chan struct{})
	})
	defer cancel()

	_, sim := launchApp(t, dir, addr)

	waitForScreen(t, sim, "this device is being erased")
	waitForScreen(t, sim, "the other device commanded it")
}

func TestDeviceLink_TheCapRefusalReplacesTheHandoverDialog(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, replyScript(map[ipc.FrameType]reply{
		ipc.FrameDeviceLinksList: {ipc.FrameDeviceLinksListed, ipc.DeviceLinksListedResponse{
			Devices: []ipc.DeviceLinkEntry{{PeerID: "41984c9b", LinkedAt: 1}},
		}},
		ipc.FrameGetDestroyPIN: {ipc.FrameDestroyPINListed, ipc.DestroyPINListedResponse{}},
	}))
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}

	typeCommand(sim, "/link-device")
	waitForScreen(t, sim, "link a device — refused")

	waitForScreen(t, sim, "This device already has a linked device,")
	waitForScreen(t, sim, "you can only have one.")
	waitForScreen(t, sim, "Unlink it from /linked-devices")

	if strings.Contains(flatten(screenText(sim)), "copies this device's identity key") {
		t.Fatalf("the handover dialog opened on a device that cannot offer a link")
	}
}

func TestDeviceLink_TheCheckThatDidNotHappenSaysSo(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, replyScript(map[ipc.FrameType]reply{
		ipc.FrameGetDestroyPIN: {ipc.FrameDestroyPINListed, ipc.DestroyPINListedResponse{}},
	}))
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}

	typeCommand(sim, "/link-device")

	waitForScreen(t, sim, "link a device — not checked")
	waitForScreen(t, sim, "could not check whether it already has a linked device")

	waitForScreen(t, sim, "The daemon did not answer in time")

	waitForScreen(t, sim, "devices to see the set.")

	waitForScreen(t, sim, "Try again")

	got := flatten(screenText(sim))
	if strings.Contains(got, "copies this device's identity key") {
		t.Fatalf("the handover dialog opened on an unanswered check:\n%s", screenText(sim))
	}
	if strings.Contains(got, "already has a linked device, and you can only have one") {
		t.Fatalf("the cap refusal was stated on an unanswered check:\n%s", screenText(sim))
	}
}

func TestDeviceLink_AnErrorOnTheListIsNotAnAnswer(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, replyScript(map[ipc.FrameType]reply{

		ipc.FrameDeviceLinksList: {ipc.FrameError, ipc.ErrorPayload{
			Code:    ipc.ReasonNotReady,
			Message: "read linked-device set: store unavailable",
		}},
		ipc.FrameGetDestroyPIN: {ipc.FrameDestroyPINListed, ipc.DestroyPINListedResponse{}},
	}))
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}

	typeCommand(sim, "/link-device")

	waitForScreen(t, sim, "link a device — not checked")
	waitForScreen(t, sim, "The daemon could not read the set")

	got := flatten(screenText(sim))
	if strings.Contains(got, "copies this device's identity key") {
		t.Fatalf("the handover dialog opened on a refused check:\n%s", screenText(sim))
	}

	if strings.Contains(got, "store unavailable") {
		t.Fatalf("the daemon's detail leaked into the dialog:\n%s", screenText(sim))
	}
}

func TestDeviceLink_AnOfferRefusedForTheCapSaysSo(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, replyScript(map[ipc.FrameType]reply{
		ipc.FrameDeviceLinksList: {ipc.FrameDeviceLinksListed, ipc.DeviceLinksListedResponse{}},
		ipc.FrameGetDestroyPIN:   {ipc.FrameDestroyPINListed, ipc.DestroyPINListedResponse{}},

		ipc.FrameDeviceLinkOffer: {ipc.FrameError, ipc.ErrorPayload{
			Code:    "already_linked",
			Message: "this device already has 1 linked device(s)",
		}},
	}))
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}

	typeCommand(sim, "/link-device")
	waitForScreen(t, sim, "copies this device's identity key")
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
	waitForScreen(t, sim, "Set a PIN for each device")

	typeText(sim, "1111")
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	typeText(sim, "2222")
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)

	waitForScreen(t, sim, "This device already has a linked device,")
	waitForScreen(t, sim, "you can only have one.")
	waitForScreen(t, sim, "Unlink it from /linked-devices")
}

func TestDeviceLink_AnOfferRefusedForAFailedReadSaysOffering(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, replyScript(map[ipc.FrameType]reply{
		ipc.FrameDeviceLinksList: {ipc.FrameDeviceLinksListed, ipc.DeviceLinksListedResponse{}},
		ipc.FrameGetDestroyPIN:   {ipc.FrameDestroyPINListed, ipc.DestroyPINListedResponse{}},

		ipc.FrameDeviceLinkOffer: {ipc.FrameError, ipc.ErrorPayload{
			Code:    "peer_check_failed",
			Message: "read linked-device set: store unavailable",
		}},
	}))
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}

	typeCommand(sim, "/link-device")
	waitForScreen(t, sim, "copies this device's identity key")
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
	waitForScreen(t, sim, "Set a PIN for each device")

	typeText(sim, "1111")
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	typeText(sim, "2222")
	sim.InjectKey(tcell.KeyTab, 0, tcell.ModNone)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)

	waitForScreen(t, sim, "then run /link-device again")

	got := flatten(screenText(sim))
	if strings.Contains(got, "/link-join") {
		t.Fatalf("the offering device was sent to the joining command:\n%s", screenText(sim))
	}
	if strings.Contains(got, "did not join") {
		t.Fatalf("the offering device was told it did not join:\n%s", screenText(sim))
	}
	if strings.Contains(got, "no contacts and no chats") {
		t.Fatalf("the identity device was told joining needs a clean slate:\n%s", screenText(sim))
	}
	if strings.Contains(got, "peer_check_failed") {
		t.Fatalf("raw refusal token leaked to the operator:\n%s", screenText(sim))
	}
}

func TestDeviceLink_TheCommandSaysWhyItCannotOpenWithoutAVault(t *testing.T) {
	for _, command := range []string{"/linked-devices", "/link-device", "/link-join"} {
		t.Run(command, func(t *testing.T) {
			dir, addr, cancel := spinScriptedIPCServer(t, replyScript(map[ipc.FrameType]reply{
				ipc.FrameDeviceLinksList: {ipc.FrameDeviceLinksListed, ipc.DeviceLinksListedResponse{}},
				ipc.FrameGetDestroyPIN:   {ipc.FrameDestroyPINListed, ipc.DestroyPINListedResponse{}},
			}))
			defer cancel()

			_, sim := launchApp(t, dir, addr)

			typeCommand(sim, command)

			waitForScreen(t, sim, command+" unavailable")

			waitForScreen(t, sim, "Restart haoma-text")

			got := flatten(screenText(sim))
			if strings.Contains(got, "/settings unavailable") {
				t.Fatalf("the refusal named a command the operator did not type:\n%s", screenText(sim))
			}
			if strings.Contains(got, "settings could not be opened") {
				t.Fatalf("the refusal was said twice:\n%s", screenText(sim))
			}

			if strings.Contains(got, "copies this device's identity key") {
				t.Fatalf("the handover dialog opened with no vault:\n%s", screenText(sim))
			}
		})
	}
}

func TestDeviceLink_TryAgainAsksTheDaemonAgain(t *testing.T) {
	dir, addr, cancel := spinScriptedIPCServer(t, func(_ context.Context, s *ipc.Session) {
		asked := 0
		for {
			f, err := s.Recv()
			if err != nil {
				return
			}
			switch f.Type {
			case ipc.FrameDeviceLinksList:
				asked++
				if asked == 1 {

					continue
				}
				out, err := ipc.NewFrame(ipc.FrameDeviceLinksListed, f.ID, ipc.DeviceLinksListedResponse{})
				if err != nil || s.Send(out) != nil {
					return
				}
			case ipc.FrameGetDestroyPIN:
				out, err := ipc.NewFrame(ipc.FrameDestroyPINListed, f.ID, ipc.DestroyPINListedResponse{})
				if err != nil || s.Send(out) != nil {
					return
				}
			}
		}
	})
	defer cancel()

	app, sim := launchApp(t, dir, addr)
	app.VaultCtl = stubVault{}

	typeCommand(sim, "/link-device")
	waitForScreen(t, sim, "link a device — not checked")

	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)

	waitForScreen(t, sim, "copies this device's identity key")
}
