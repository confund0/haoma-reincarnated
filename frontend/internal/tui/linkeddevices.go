package tui

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"haoma-frontend/internal/ipc"
)

const (
	labelOwnPIN   = "PIN for THIS device   "
	labelOtherPIN = "PIN for OTHER device  "
	labelWords    = "7 words               "
)

type linkedDevicesState struct {
	devices      []ipc.DeviceLinkEntry
	devicesKnown bool

	destroyPIN      ipc.DestroyPINListedResponse
	destroyPINKnown bool

	risksAcked bool

	verifyNote string
	busy       bool
	busyLabel  string

	verifyFrame int

	peerIDs []string
}

type linkLiveWidgets struct {
	intro *tview.TextView
	log   *tview.TextView
	words *tview.InputField

	buttons *tview.Form
}

func (st linkedDevicesState) hasContacts() bool {
	mine := map[string]bool{}
	for _, d := range st.devices {
		mine[d.PeerID] = true
	}
	for _, id := range st.peerIDs {
		if !mine[id] {
			return true
		}
	}
	return false
}

func linkedDeviceIntro(st linkedDevicesState, fs *linkFlowState) string {
	if fs != nil {
		switch {
		case fs.phase == phaseCollect && fs.mode == flowOffer:
			return "1. Set a PIN for each device. They must differ.\n" +
				"2. The seven words appear below once the invitation is\n" +
				"   published."
		case fs.phase == phaseCollect && fs.mode == flowJoin:
			return "1. Set a PIN for each device. They must differ.\n" +
				"2. Type the seven words shown on your other device."
		case fs.phase == phaseCollect && fs.mode == flowGuardOnly:
			return "This device is linked and has no destroy PIN.\n" +
				"Set a PIN for each device. They must differ."
		case fs.mode == flowGuardOnly:
			return "Setting the destroy PIN. Keep both devices on."
		case fs.mode == flowOffer:
			return "Linking. Keep this device on until it finishes."
		default:
			return "Joining. Keep this device on until it finishes."
		}
	}

	out := ""
	switch {

	case st.destroyPIN.Waiting != nil:
		w := st.destroyPIN.Waiting
		out = "Holding this device's address another " +
			destroyWaitRemaining(w.Deadline, time.Now().Unix()) + " so " +
			shortID(w.PeerID) + " can be told it was removed.\n\n" +
			"It is already off this identity. If it was switched off when\n" +
			"you destroyed it, this is how it finds out and erases itself\n" +
			"— the first time it reaches this device.\n\n" +
			"Until the window ends you cannot link another device.\n\n" +
			"Stopping lets you link one now. It does not undo the destroy,\n" +
			"and that device will never be told, so whether it erased\n" +
			"itself stays unknown."
	case !st.devicesKnown:
		out = "Asking the daemon…"
	case len(st.devices) == 0 && st.hasContacts():

		out = "This device has no linked device.\n\n" +
			"It can link another device of your own. It cannot join one. " +
			"Only an empty device can join, and this one already has " +
			"contacts or chats.\n\n" +
			"To make it empty, erase it: Advanced, at the foot of the " +
			"page, or /wipe-device."
	case len(st.devices) == 0:
		out = "This device has no linked device.\n\n" +
			"Link another device of your own and the two share one " +
			"identity: contacts, conversations and history."
	default:
		out = "Linked device: " + shortID(st.devices[0].PeerID)
		switch {
		case !st.destroyPINKnown:
			out += "\nDestroy PIN: asking the daemon…"
		case st.destroyPIN.Set:
			out += "\nDestroy PIN: set. It cannot be changed."
		default:
			out += "\nDestroy PIN: none on this device."
		}

		out += "\n\nUnlink: that device comes off this identity and keeps its data." +
			"\nDestroy: the same, and it is told to erase itself. Needs its PIN."
	}
	return out + linkedDeviceVerifyLine(st)
}

func linkedDeviceVerifyLine(st linkedDevicesState) string {
	switch {
	case st.busy:

		return "\n" + st.busyLabel + ": " +
			string(spinnerFrames[st.verifyFrame%len(spinnerFrames)])
	case st.verifyNote != "":
		return "\n" + st.verifyNote
	}
	return ""
}

func linkedDevicesShape(st linkedDevicesState, fs *linkFlowState) string {
	if fs != nil {

		return fmt.Sprintf("run:%v:%v:%t:%t", fs.mode, fs.phase, fs.retryable, fs.handle != "")
	}
	switch {
	case st.destroyPIN.Waiting != nil:
		return "waiting"
	case len(st.devices) > 0 && st.destroyPINKnown && !st.destroyPIN.Set:
		return "linked-without-guard:" + st.devices[0].PeerID
	case len(st.devices) > 0:

		return "linked:" + st.devices[0].PeerID
	case st.hasContacts():
		return "holds-contacts"
	default:
		return "empty"
	}
}

func buildLinkedDevicesForm(a *App, sp *settingsPage) tview.Primitive {
	flow := a.linkFlow()

	var fs *linkFlowState
	if flow != nil {
		st := flow.state()
		fs = &st
	}

	sp.linkedShape = linkedDevicesShape(sp.linked, fs)

	intro := tview.NewTextView().SetWrap(true).SetDynamicColors(true)
	intro.SetText(linkedDeviceIntro(sp.linked, fs))

	fields := tview.NewForm()

	fields.SetItemPadding(0)
	buttons := tview.NewForm()

	logTV := tview.NewTextView().SetWrap(true).SetDynamicColors(true)

	logTV.ScrollToEnd()

	if flow == nil {
		sp.setLive(linkLiveWidgets{intro: intro, buttons: buttons})
		buildLinkIdleButtons(a, sp, buttons)
	} else {
		buildLinkFlowRows(a, sp, flow, fs, fields, buttons, intro, logTV)
	}

	introRows, fieldRows, logRows, buttonRows := linkBoxRows(fs, sp.linked)

	introFlex := 0
	if introRows == 0 {
		introFlex = 1
	}

	box := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(intro, introRows, introFlex, false).
		AddItem(fields, fieldRows, 0, flow != nil).
		AddItem(logTV, logRows, 0, false).
		AddItem(buttons, buttonRows, 0, flow == nil)
	box.SetBorder(true).SetTitle(" linked devices ")

	box.SetBorderPadding(1, 1, 2, 2)

	var outer *tview.Flex
	if flow != nil || sp.linked.destroyPIN.Waiting != nil {
		outer = tview.NewFlex().SetDirection(tview.FlexRow).AddItem(box, 0, 1, true)
	} else {
		height := introRows + buttonRows + 4
		outer = tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(box, height, 0, true).
			AddItem(nil, 0, 1, false)
	}

	outer.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEscape {
			sp.returnToList(a)
			return nil
		}
		return ev
	})
	return outer
}

func linkBoxRows(fs *linkFlowState, st linkedDevicesState) (intro, fields, log, buttons int) {
	if fs == nil {
		if st.destroyPIN.Waiting != nil {

			return 0, 0, 0, 5
		}

		return 6, 0, 0, 5
	}
	if fs.mode == flowGuardOnly {

		return 4, linkFieldRows(2), 10, 3
	}
	return 4, linkFieldRows(3), 10, 3
}

func linkFieldRows(fields int) int {
	return fields + 2
}

const riskLabel = "I understand risks"

const labelVerifyDestroyPIN = "Verify destroy PIN…"

func buildLinkIdleButtons(a *App, sp *settingsPage, buttons *tview.Form) {
	st := sp.linked

	dangerZone := func() {
		buttons.AddFormItem(newBracketCheckbox(riskLabel, sp.linked.risksAcked,
			func(checked bool) { sp.linked.risksAcked = checked }))
	}
	risksTicked := func() bool {
		box, _ := buttons.GetFormItemByLabel(riskLabel).(*bracketCheckbox)
		return box != nil && box.Checked()
	}
	refuse := func() {

		a.setVerifyNote(StyleLinkStress +
			"Tick \"" + riskLabel + "\" first." + StyleLinkBody)
	}

	gated := func(do func()) func() {
		return func() {
			if !risksTicked() {
				refuse()
				return
			}
			sp.linked.risksAcked = false
			do()
			a.rebuildLinkedDevicesPage()
		}
	}

	gatedModal := func(do func()) func() {
		return func() {
			if !risksTicked() {
				refuse()
				return
			}
			sp.linked.risksAcked = false
			do()
		}
	}

	switch {
	case st.destroyPIN.Waiting != nil:

		dangerZone()
		buttons.AddButton("Stop waiting", gated(func() { a.stopDestroyWaiting() }))
	case len(st.devices) > 0 && st.destroyPINKnown && !st.destroyPIN.Set:

		buttons.AddButton("Set destroy PIN", func() { a.beginLinkFlow(flowGuardOnly) })
	case len(st.devices) > 0:
		peerID := st.devices[0].PeerID
		buttons.AddButton(labelVerifyDestroyPIN, func() {
			a.showOtherDestroyPINForm("verify destroy PIN", "Check", func(pin string) {
				a.verifyOtherDestroyPIN(peerID, pin)
			})
		})

		dangerZone()
		buttons.AddButton("Unlink", gated(func() {
			a.unlinkLinkedDevice(peerID)
		}))
		buttons.AddButton("Destroy…", gatedModal(func() {
			a.showOtherDestroyPINForm("destroy the other device", "Destroy", func(pin string) {
				a.destroyLinkedDevice(peerID, pin)
			})
		}))
	case st.hasContacts():

		buttons.AddButton("Link a new device", func() { a.beginLinkFlow(flowOffer) })
	default:
		buttons.AddButton("Link a new device", func() { a.beginLinkFlow(flowOffer) })
		buttons.AddButton("Join a link invitation", func() { a.beginLinkFlow(flowJoin) })
	}
}

func buildLinkFlowRows(a *App, sp *settingsPage, f *linkFlow, fs *linkFlowState, fields, buttons *tview.Form, intro, logTV *tview.TextView) {
	mode, phase := fs.mode, fs.phase
	own, other := fs.ownPIN, fs.otherPIN

	compare := func(string) {
		a.sayIfPINsMatch(f, fields)
	}
	fields.AddPasswordField(labelOwnPIN, own, 12, '*', compare)
	fields.AddPasswordField(labelOtherPIN, other, 12, '*', compare)

	words := tview.NewInputField().SetLabel(labelWords).SetFieldWidth(44)
	words.SetText(fs.words)
	if mode == flowGuardOnly {

		words = nil
	} else if mode == flowOffer {

		words.SetDisabled(true)
		words.SetFieldBackgroundColor(tcell.ColorDefault)
	}
	if words != nil {
		if mode == flowOffer {

			fields.AddFormItem(&fixedTextInput{InputField: words, textColor: ColorLinkWords})
		} else {
			fields.AddFormItem(words)
		}
	}

	logTV.SetText(fs.log)

	lastFocusable := fields.GetFormItemCount() - 1
	if mode == flowOffer {
		lastFocusable--
	}

	defer func() {
		if buttons.GetButtonCount() > 0 {
			linkTabBetween(a, fields, buttons, lastFocusable)
		}
	}()

	sp.setLive(linkLiveWidgets{intro: intro, log: logTV, words: words, buttons: buttons})

	switch phase {
	case phaseCollect:
		buttons.AddButton("Proceed", func() { a.proceedLinkFlow(f, fields) })
		buttons.AddButton("Cancel", func() { a.endLinkFlow() })
	case phaseDone:

		if fs.retryable {
			buttons.AddButton("Set destroy PIN", func() { a.retrySetOwnPIN(f) })
		}
		buttons.AddButton("Close", func() { a.endLinkFlow() })
	default:

		if handle := fs.handle; mode == flowOffer && handle != "" {
			buttons.AddButton("Cancel this invitation", func() {
				a.cmdLinkCancel(handle)
				a.endLinkFlow()
			})
		} else {
			buttons.AddButton("Cancel", func() { a.endLinkFlow() })
		}
	}
}

func linkTabBetween(a *App, fields, buttons *tview.Form, lastFocusable int) {
	fields.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		item, _ := fields.GetFocusedItemIndex()
		switch ev.Key() {
		case tcell.KeyTab:
			if item >= lastFocusable {
				focusLinkButton(a, buttons, 0)
				return nil
			}
		case tcell.KeyBacktab:
			if item == 0 {
				focusLinkButton(a, buttons, buttons.GetButtonCount()-1)
				return nil
			}
		}
		return ev
	})
	buttons.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		_, btn := buttons.GetFocusedItemIndex()
		switch ev.Key() {
		case tcell.KeyBacktab:
			if btn == 0 {
				focusLinkField(a, fields, lastFocusable)
				return nil
			}
		case tcell.KeyTab:

			if btn >= buttons.GetButtonCount()-1 {
				focusLinkField(a, fields, 0)
				return nil
			}
		}
		return ev
	})
}

func focusLinkField(a *App, fields *tview.Form, index int) {
	if index < 0 {
		index = 0
	}
	fields.SetFocus(index)
	a.app.SetFocus(fields)
}

func focusLinkButton(a *App, buttons *tview.Form, index int) {
	if index < 0 {
		index = 0
	}
	buttons.SetFocus(buttons.GetFormItemCount() + index)
	a.app.SetFocus(buttons)
}

func (a *App) refreshLinkedDevicesState(sp *settingsPage) {
	a.sendRequest(ipc.FrameDeviceLinksList, ipc.DeviceLinksListRequest{}, func(fr ipc.Frame) {
		var listed ipc.DeviceLinksListedResponse
		if fr.Type != ipc.FrameDeviceLinksListed || json.Unmarshal(fr.Payload, &listed) != nil {
			return
		}
		a.app.QueueUpdateDraw(func() {
			sp.linked.devices = listed.Devices
			sp.linked.devicesKnown = true
			a.redrawLinkedDevicesPage()
		})
	})
	a.sendRequest(ipc.FrameGetDestroyPIN, ipc.GetDestroyPINRequest{}, func(fr ipc.Frame) {
		var got ipc.DestroyPINListedResponse
		if fr.Type != ipc.FrameDestroyPINListed || json.Unmarshal(fr.Payload, &got) != nil {
			return
		}
		a.app.QueueUpdateDraw(func() {
			sp.linked.destroyPIN = got
			sp.linked.destroyPINKnown = true
			a.redrawLinkedDevicesPage()
		})
	})
}

func (a *App) verifyOtherDestroyPIN(peerID, pin string) {
	a.setBusyNote("Verify destroy PIN")

	req := ipc.VerifyDestroyPINRequest{PeerID: peerID, PIN: pin}
	a.sendRequest(ipc.FrameVerifyDestroyPIN, req, func(f ipc.Frame) {
		if f.Type == ipc.FrameError {
			a.setVerifyNote(StyleLinkBad + "Verify destroy PIN: the check could not be sent." + StyleLinkBody)
			return
		}
		var out ipc.DestroyPINVerifiedResponse
		if err := json.Unmarshal(f.Payload, &out); err != nil {
			a.setVerifyNote(StyleLinkBad + "Verify destroy PIN: the answer could not be read." + StyleLinkBody)
			return
		}
		switch {

		case out.Reason == ipc.DestroyPINVerifyNoSalt:
			a.setVerifyNote(StyleLinkBad + "Verify destroy PIN: your other device has not sent what is needed yet." + StyleLinkBody)
		case out.Reason == ipc.DestroyPINRefusedShape:
			a.setVerifyNote(StyleLinkBad + "Verify destroy PIN: a destroy PIN is four digits." + StyleLinkBody)
		case out.Reason == ipc.DestroyPINVerifyUnknownSibling:
			a.setVerifyNote(StyleLinkBad + "Verify destroy PIN: that is not one of your devices." + StyleLinkBody)
		case out.Reason == ipc.DestroyPINVerifyAnswerBad:

			a.setVerifyNote(StyleLinkBad + "Verify destroy PIN: your other device's answer did not check out." + StyleLinkBody)
		case !out.Answered:
			a.setVerifyNote(StyleLinkBad + "Verify destroy PIN: NO ANSWER" + StyleLinkBody)
		case out.Verified:
			a.setVerifyNote(StyleLinkOK + "Verify destroy PIN: OK" + StyleLinkBody)
		default:
			a.setVerifyNote(StyleLinkBad + "Verify destroy PIN: FAILED" + StyleLinkBody)
		}
	})
}

func (a *App) setVerifyNote(note string) {
	a.writeActionNote(note, "")
}

func (a *App) setBusyNote(label string) {
	a.writeActionNote("", label)
}

func (a *App) writeActionNote(note, label string) {
	sp := a.openSettingsPage()
	if sp == nil {
		return
	}

	if label != "" {
		a.startVerifyMarker(sp)
	} else {
		a.stopVerifyMarker()
	}
	go a.app.QueueUpdateDraw(func() {
		sp.linked.verifyNote = note
		sp.linked.busy = label != ""
		sp.linked.busyLabel = label
		if w := sp.liveWidgets(); w.intro != nil {
			w.intro.SetText(linkedDeviceIntro(sp.linked, a.linkFlowSnapshot()))
		}
	})
}

func (a *App) linkFlowSnapshot() *linkFlowState {
	f := a.linkFlow()
	if f == nil {
		return nil
	}
	s := f.state()
	return &s
}

func (a *App) startVerifyMarker(sp *settingsPage) {
	a.stopVerifyMarker()

	stop := make(chan struct{})
	a.verifyMu.Lock()
	a.verifyStop = stop
	a.verifyMu.Unlock()

	go func() {
		t := time.NewTicker(linkCeremonyTick)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				a.app.QueueUpdateDraw(func() {
					if !sp.linked.busy {
						return
					}
					sp.linked.verifyFrame++
					if w := sp.liveWidgets(); w.intro != nil {
						w.intro.SetText(linkedDeviceIntro(sp.linked, a.linkFlowSnapshot()))
					}
				})
			}
		}
	}()
}

func (a *App) stopVerifyMarker() {
	a.verifyMu.Lock()
	stop := a.verifyStop
	a.verifyStop = nil
	a.verifyMu.Unlock()
	if stop != nil {
		close(stop)
	}
}

type fixedTextInput struct {
	*tview.InputField
	textColor tcell.Color
}

func (f *fixedTextInput) SetFormAttributes(labelWidth int, labelColor, bgColor, _, fieldBgColor tcell.Color) tview.FormItem {
	f.InputField.SetFormAttributes(labelWidth, labelColor, bgColor, f.textColor, fieldBgColor)
	return f
}

func (a *App) unlinkLinkedDevice(peerID string) {
	a.runUnlink(peerID, "")
}

func (a *App) destroyLinkedDevice(peerID, pin string) {
	a.runUnlink(peerID, pin)
}

func (a *App) dismissModalToSettings(pageName string) {
	a.pages.RemovePage(pageName)
	a.winMu.Lock()
	sp := a.settings
	a.winMu.Unlock()
	if sp != nil {
		a.app.SetFocus(sp.list)
		a.rebuildLinkedDevicesPage()
		return
	}
	a.app.SetFocus(a.input)
}

func (a *App) runUnlink(peerID, pin string) {

	a.setBusyNote(ifElse(pin != "", "Destroy", "Unlink"))

	req := ipc.UnlinkDeviceRequest{PeerID: peerID, PIN: pin}
	a.sendRequest(ipc.FrameUnlinkDevice, req, func(f ipc.Frame) {
		if f.Type == ipc.FrameError {
			a.setVerifyNote(StyleLinkBad + "Unlink: it could not be done." + StyleLinkBody)
			return
		}
		var out ipc.DeviceUnlinkedResponse
		if err := json.Unmarshal(f.Payload, &out); err != nil {
			a.setVerifyNote(StyleLinkBad + "Unlink: the answer could not be read." + StyleLinkBody)
			return
		}
		a.setVerifyNote(unlinkOutcomeLine(out, pin != ""))

		if out.Unlinked {

			a.winMu.Lock()
			sp := a.settings
			a.winMu.Unlock()
			if sp != nil {
				a.refreshLinkedDevicesState(sp)
			}
			a.refreshAfterLink()
		}
	})
}

func unlinkOutcomeLine(out ipc.DeviceUnlinkedResponse, wantedDestroy bool) string {
	switch {
	case out.Reason == ipc.DestroyPINRefusedWrong:
		return StyleLinkBad + "Destroy: that device did not accept that PIN. " +
			"Nothing was changed." + StyleLinkBody
	case out.Reason == ipc.DestroyPINRefusedShape:
		return StyleLinkBad + "Destroy: a destroy PIN is four digits." + StyleLinkBody
	case out.Reason == ipc.DestroyPINVerifyNoSalt:
		return StyleLinkBad + "Destroy: that device has not sent what is needed yet." + StyleLinkBody
	case out.Reason == ipc.DestroyPINVerifyUnknownSibling:
		return StyleLinkBad + "Unlink: that is not one of your devices." + StyleLinkBody
	case !out.Unlinked:
		return StyleLinkBad + "Unlink: it could not be done." + StyleLinkBody
	case !wantedDestroy:
		return StyleLinkOK + "Unlinked." + StyleLinkBody
	case out.Receipted:
		return StyleLinkOK + "Unlinked. That device answered that it erased itself." + StyleLinkBody
	case out.Sent:
		return StyleLinkOK + "Unlinked." + StyleLinkBody +
			" The command went out and no answer came back."
	default:
		return StyleLinkOK + "Unlinked." + StyleLinkBody +
			" That device could not be reached, so nothing was commanded."
	}
}

func destroyWaitRemaining(deadline, nowSec int64) string {
	left := deadline - nowSec
	if left <= 0 {
		return "00:00"
	}
	minutes := (left + 59) / 60
	return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60)
}

func (a *App) stopDestroyWaiting() {

	a.setBusyNote("Stop waiting")
	a.sendRequest(ipc.FrameStopDestroyWaiting, struct{}{}, func(f ipc.Frame) {
		if f.Type == ipc.FrameError {
			a.setVerifyNote(StyleLinkBad + "Stop waiting: it could not be done." + StyleLinkBody)
			a.log(StyleLinkBad + "the wait could not be stopped" + StyleLinkBody)
			a.renderError(f)
			return
		}
		a.setVerifyNote(StyleLinkOK + "Stopped waiting." + StyleLinkBody +
			" That device is off this identity and another can be linked.")
		a.log(StyleLinkOK + "stopped waiting" + StyleLinkBody +
			" — that device is off this identity and another can be linked")

		a.winMu.Lock()
		sp := a.settings
		a.winMu.Unlock()
		if sp != nil {
			a.refreshLinkedDevicesState(sp)
		}
	})
}
