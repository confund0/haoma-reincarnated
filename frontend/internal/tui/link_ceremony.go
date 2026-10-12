package tui

import (
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/rivo/tview"

	"haoma-frontend/internal/ipc"
)

func (a *App) linkFlow() *linkFlow {
	a.ceremonyMu.Lock()
	defer a.ceremonyMu.Unlock()
	return a.flow
}

func (a *App) beginLinkFlow(mode linkFlowMode) {
	a.beginLinkFlowWithWords(mode, "")
}

func (a *App) beginLinkFlowWithWords(mode linkFlowMode, words string) {
	a.ceremonyMu.Lock()
	if a.flow != nil {
		a.ceremonyMu.Unlock()
		return
	}
	f := &linkFlow{mode: mode, phase: phaseCollect, words: strings.TrimSpace(words)}
	a.flow = f
	stop := make(chan struct{})
	a.ceremonyStop = stop
	a.ceremonyMu.Unlock()

	go a.tickLinkFlow(f, stop)
	a.redrawLinkedDevicesPage()
}

func (a *App) endLinkFlow() {
	a.ceremonyMu.Lock()
	stop := a.ceremonyStop
	a.flow = nil
	a.ceremonyStop = nil
	a.ceremonyMu.Unlock()

	if stop != nil {
		close(stop)
	}
	a.redrawLinkedDevicesPage()

	a.winMu.Lock()
	sp := a.settings
	a.winMu.Unlock()
	if sp != nil {
		a.refreshLinkedDevicesState(sp)
	}
}

func (a *App) flowIsCurrent(f *linkFlow) bool {
	a.ceremonyMu.Lock()
	defer a.ceremonyMu.Unlock()
	return a.flow != nil && a.flow == f
}

func (a *App) tickLinkFlow(f *linkFlow, stop chan struct{}) {
	t := time.NewTicker(linkCeremonyTick)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			f.Tick()
			a.refreshLinkFlowText(f)
		}
	}
}

func (a *App) redrawLinkedDevicesPage() {
	sp := a.openSettingsPage()
	if sp == nil {
		return
	}
	go a.app.QueueUpdateDraw(func() { a.drawLinkedDevicesPage(sp, false) })
}

func (a *App) rebuildLinkedDevicesPage() {
	sp := a.openSettingsPage()
	if sp == nil {
		return
	}
	go a.app.QueueUpdateDraw(func() { a.drawLinkedDevicesPage(sp, true) })
}

func (a *App) openSettingsPage() *settingsPage {
	a.winMu.Lock()
	defer a.winMu.Unlock()
	return a.settings
}

func (a *App) drawLinkedDevicesPage(sp *settingsPage, build bool) {
	fs := a.linkFlowSnapshot()
	if !build && sp.linkedShape == linkedDevicesShape(sp.linked, fs) {
		writeLinkedDevicesText(sp, fs)
		return
	}
	sp.rebuildForm(a, settingsDomainLinked)

	if sp.activePage != settingsDomainLinked {
		return
	}
	sp.forms.SwitchToPage(settingsDomainLinked)

	if fs == nil {
		return
	}
	if prim := sp.formForDomain(settingsDomainLinked); prim != nil {
		a.app.SetFocus(prim)
	}
}

func writeLinkedDevicesText(sp *settingsPage, fs *linkFlowState) {
	w := sp.liveWidgets()
	if w.intro != nil {
		w.intro.SetText(linkedDeviceIntro(sp.linked, fs))
	}
	if fs == nil {
		return
	}
	if w.log != nil {
		w.log.SetText(fs.log)
		w.log.ScrollToEnd()
	}

	if fs.mode == flowOffer && w.words != nil && w.words.GetText() != fs.words {
		w.words.SetText(fs.words)
	}
}

func (a *App) refreshLinkFlowText(_ *linkFlow) {
	a.redrawLinkedDevicesPage()
}

func (a *App) proceedLinkFlow(f *linkFlow, fields *tview.Form) {

	own := strings.TrimSpace(fields.GetFormItemByLabel(labelOwnPIN).(*tview.InputField).GetText())
	other := strings.TrimSpace(fields.GetFormItemByLabel(labelOtherPIN).(*tview.InputField).GetText())
	mode, phase := f.snapshot()

	if phase != phaseCollect {
		slog.Debug("linking: proceed ignored; the run is not collecting")
		return
	}

	words := ""
	if item, ok := fields.GetFormItemByLabel(labelWords).(interface{ GetText() string }); ok && item != nil {
		words = strings.TrimSpace(item.GetText())
	}

	switch {
	case !isFourDigits(own) || !isFourDigits(other):
		f.Say("Each PIN is four digits.")
		a.refreshLinkFlowText(f)
		return
	case own == other:

		f.Say("The two PINs must differ.")
		a.refreshLinkFlowText(f)
		return
	case mode == flowJoin && len(strings.Fields(words)) < 3:
		f.Say("The seven words are needed to join.")
		a.refreshLinkFlowText(f)
		return
	}

	f.mu.Lock()
	if f.phase != phaseCollect {
		f.mu.Unlock()
		return
	}
	f.ownPIN, f.otherPIN = own, other
	if mode == flowJoin {
		f.words = words
	}
	f.phase = phaseRun
	f.mu.Unlock()

	switch mode {
	case flowOffer:
		f.Say("publishing the invitation")
		a.redrawLinkedDevicesPage()
		a.sendLinkOffer()
	case flowGuardOnly:

		sibling := ""
		if devs := a.linkedDevicesSnapshot(); len(devs) > 0 {
			sibling = devs[0]
		}
		if sibling == "" {
			f.Fail("This device is not linked, so there is nothing to guard.")
			a.redrawLinkedDevicesPage()
			return
		}
		a.redrawLinkedDevicesPage()
		go a.finishLinkFlow(f, sibling)
	default:
		f.Say("contacting your other device")
		a.redrawLinkedDevicesPage()
		a.cmdLinkJoin(words)
	}
}

func isFourDigits(s string) bool {
	if len(s) != 4 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func (a *App) finishLinkFlow(f *linkFlow, siblingPeerID string) {

	if _, phase := f.snapshot(); phase != phaseRun {
		slog.Debug("linking: completion ignored; the run is not at the step that takes it")
		return
	}
	f.MarkLinked()
	own, other := f.PINs()
	if own == "" || other == "" {
		f.Fail("The PINs were not collected, so this device has no guard.")
		a.redrawLinkedDevicesPage()
		return
	}
	f.Step("OK", "setting this device's destroy PIN")
	a.redrawLinkedDevicesPage()

	a.sendRequest(ipc.FrameSetDestroyPIN, ipc.SetDestroyPINRequest{New: own}, func(fr ipc.Frame) {
		if !a.flowIsCurrent(f) {
			return
		}
		var out ipc.DestroyPINSetResponse
		if fr.Type == ipc.FrameError || json.Unmarshal(fr.Payload, &out) != nil || !out.Accepted {

			f.FailRetryable("This device did not take that PIN. " +
				"The two are linked; set it again from this page.")
			a.redrawLinkedDevicesPage()
			return
		}
		f.Step("OK", "waiting for your other device")
		a.redrawLinkedDevicesPage()
		go a.awaitSaltThenVerify(f, siblingPeerID, other)
	})
}

func (a *App) awaitSaltThenVerify(f *linkFlow, peerID, otherPIN string) {
	deadline := time.Now().Add(saltWaitTimeout)
	for {
		held := make(chan bool, 1)
		a.sendRequest(ipc.FrameGetDestroyPIN, ipc.GetDestroyPINRequest{}, func(fr ipc.Frame) {
			var got ipc.DestroyPINListedResponse
			if fr.Type != ipc.FrameDestroyPINListed || json.Unmarshal(fr.Payload, &got) != nil {
				held <- false
				return
			}
			for _, s := range got.Siblings {
				if s.PeerID == peerID && s.SaltHeld {
					held <- true
					return
				}
			}
			held <- false
		})

		if !a.flowIsCurrent(f) {
			slog.Debug("linking: salt wait abandoned; the run was closed")
			return
		}

		select {
		case ok := <-held:
			if ok {
				f.Step("OK", "checking your other device's PIN")
				a.redrawLinkedDevicesPage()
				a.verifyInFlow(f, peerID, otherPIN)
				return
			}
		case <-time.After(saltPollTimeout):
		}

		if time.Now().After(deadline) {
			if !a.flowIsCurrent(f) {
				return
			}

			f.FinishLinked(linkedDetailsNotReceivedLine)
			a.redrawLinkedDevicesPage()
			return
		}
		time.Sleep(saltPollInterval)
	}
}

func (a *App) verifyInFlow(f *linkFlow, peerID, otherPIN string) {
	req := ipc.VerifyDestroyPINRequest{PeerID: peerID, PIN: otherPIN}
	a.sendRequest(ipc.FrameVerifyDestroyPIN, req, func(fr ipc.Frame) {
		if !a.flowIsCurrent(f) {
			return
		}

		var out ipc.DestroyPINVerifiedResponse
		if fr.Type == ipc.FrameError {
			f.FinishLinkedWarning(verifyErrorLine(fr), linkedVerifyLaterLine)
			a.redrawLinkedDevicesPage()
			return
		}
		if json.Unmarshal(fr.Payload, &out) != nil {
			f.FinishLinkedWarning(verifyUnreadableLine, linkedVerifyLaterLine)
			a.redrawLinkedDevicesPage()
			return
		}
		switch {
		case out.Reason != "":

			f.Fail(verifyReasonLine(out.Reason))
		case !out.Answered:
			f.FinishLinkedWarning(verifyNoAnswerLine, linkedVerifyLaterLine)
		case !out.Verified:
			f.Fail("Your other device did not accept that PIN.")
		default:
			f.Finish("linked, and both PINs check out")
		}
		a.redrawLinkedDevicesPage()
	})
}

const (
	verifyNoAnswerLine       = "Your other device did not answer."
	verifyNotSentLine        = "The check could not be sent."
	verifyUnreadableLine     = "The answer could not be read."
	verifyDaemonNoAnswerLine = "No answer came back from the daemon, so the check did not finish."
	verifyAnswerBadLine      = "Your other device's answer did not check out."
	verifyNoDetailsLine      = "Your other device has not sent its details yet, so its PIN cannot be checked."
	verifyNotMyDeviceLine    = "That is not one of your devices."
	verifyShapeLine          = "A destroy PIN is four digits."
)

func verifyErrorLine(fr ipc.Frame) string {
	var p ipc.ErrorPayload
	if json.Unmarshal(fr.Payload, &p) == nil && p.Code == ipc.ReasonNoAnswer {
		return verifyDaemonNoAnswerLine
	}
	return verifyNotSentLine
}

func verifyReasonLine(reason string) string {
	switch reason {
	case ipc.DestroyPINVerifyAnswerBad:
		return verifyAnswerBadLine
	case ipc.DestroyPINVerifyNoSalt:
		return verifyNoDetailsLine
	case ipc.DestroyPINVerifyUnknownSibling:
		return verifyNotMyDeviceLine
	case ipc.DestroyPINRefusedShape:
		return verifyShapeLine
	}
	return verifyNotSentLine
}

const linkedVerifyLaterLine = "Linked. Please press [" + labelVerifyDestroyPIN +
	"] later to check your other device's PIN."

const linkedDetailsNotReceivedLine = "linked, and this device's PIN is set. " +
	"Your other device's PIN: details not received yet. Check it later from this page."

var (
	saltWaitTimeout  = 90 * time.Second
	saltPollTimeout  = 5 * time.Second
	saltPollInterval = 2 * time.Second
)

func (a *App) openLinkedDevicesPage(then func()) {
	go a.app.QueueUpdateDraw(func() {
		if a.openSettingsDomain(settingsDomainLinked) == nil {
			a.log(StyleLinkBad + "linking" + StyleLinkBody + " settings could not be opened")
			return
		}
		if then != nil {
			then()
		}
	})
}

func (a *App) openSettingsDomain(domain string) *settingsPage {
	a.cmdSettings()

	a.winMu.Lock()
	sp := a.settings
	a.winMu.Unlock()
	if sp == nil {
		return nil
	}
	sp.activePage = domain
	sp.forms.SwitchToPage(domain)
	sp.selectDomainRow(domain)
	if prim := sp.formForDomain(domain); prim != nil {
		a.app.SetFocus(prim)
	}
	return sp
}

func (a *App) sayIfPINsMatch(f *linkFlow, fields *tview.Form) {
	own := strings.TrimSpace(fields.GetFormItemByLabel(labelOwnPIN).(*tview.InputField).GetText())
	other := strings.TrimSpace(fields.GetFormItemByLabel(labelOtherPIN).(*tview.InputField).GetText())
	if !isFourDigits(own) || !isFourDigits(other) {
		f.ClearNote()
		a.refreshLinkFlowText(f)
		return
	}
	if own == other {
		f.Note("The two PINs must differ.")
	} else {
		f.ClearNote()
	}
	a.refreshLinkFlowText(f)
}

func (a *App) retrySetOwnPIN(f *linkFlow) {
	sibling := ""
	if st := a.linkedDevicesSnapshot(); len(st) > 0 {
		sibling = st[0]
	}
	if sibling == "" {

		f.Fail("This device is not linked, so there is nothing to guard.")
		a.redrawLinkedDevicesPage()
		return
	}
	f.Resume("setting this device's destroy PIN")
	a.redrawLinkedDevicesPage()
	a.finishLinkFlow(f, sibling)
}

func (a *App) linkedDevicesSnapshot() []string {
	a.winMu.Lock()
	sp := a.settings
	a.winMu.Unlock()
	if sp == nil {
		return nil
	}
	out := make([]string, 0, len(sp.linked.devices))
	for _, d := range sp.linked.devices {
		out = append(out, d.PeerID)
	}
	return out
}

func (a *App) failLinkRun(reason string) {
	f := a.linkFlow()
	if f == nil {
		return
	}
	f.Fail(reason)
	a.redrawLinkedDevicesPage()
}

func linkPushBelongsTo(f *linkFlow, handleID string) bool {
	mine := f.Handle()
	if mine == "" {
		return true
	}
	return handleID == "" || handleID == mine
}
