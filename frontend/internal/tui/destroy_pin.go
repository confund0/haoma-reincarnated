package tui

import (
	"encoding/json"
	"time"

	"github.com/rivo/tview"

	"haoma-frontend/internal/ipc"
)

const labelOtherDestroyPIN = "Destroy PIN of the other device"

const destroyScreenGrace = 700 * time.Millisecond

func (a *App) showOtherDestroyPINForm(title, action string, onSubmit func(pin string)) {
	const pageName = "destroy-pin-other"

	form := tview.NewForm()
	form.SetBorder(true).SetTitle(" " + title + " ")
	form.AddPasswordField(labelOtherDestroyPIN, "", 16, '*', nil)

	back, _ := a.pages.GetFrontPage()
	dismiss := func() {
		if back != "" {
			a.pages.SwitchToPage(back)
		}

		a.dismissModalToSettings(pageName)
	}

	form.AddButton(action, func() {
		pin := form.GetFormItemByLabel(labelOtherDestroyPIN).(*tview.InputField).GetText()
		if pin == "" {
			return
		}
		dismiss()
		onSubmit(pin)
	})
	form.AddButton("Cancel", dismiss)
	form.SetCancelFunc(dismiss)
	wrapInModal(a, pageName, form, 68, 9)
}

func (a *App) routeDestroyAttemptsSeen(f ipc.Frame) {
	var p ipc.DestroyAttemptsSeenPush
	if err := json.Unmarshal(f.Payload, &p); err != nil || p.Count == 0 {
		return
	}
	a.app.QueueUpdateDraw(func() {
		a.log(StyleLinkBad + "destroy" + StyleLinkBody + " somebody tried the destroy PIN on this device")
		if p.Count == 1 {
			a.log("  " + StyleLinkDim + "one wrong attempt" + StyleLinkBody)
		} else {
			a.log("  "+StyleLinkDim+"%d wrong attempts"+StyleLinkBody, p.Count)
		}
		if p.LastAt > 0 {
			a.log("  "+StyleLinkDim+"the last one at %s"+StyleLinkBody,
				time.Unix(p.LastAt, 0).Format("2006-01-02 15:04"))
		}
	})
}

func (a *App) routeDestroyDevice(f ipc.Frame) {
	var p ipc.DestroyDevicePush
	_ = json.Unmarshal(f.Payload, &p)

	reason := p.Reason
	if reason == "" {
		reason = "the other device commanded it"
	}
	a.app.QueueUpdateDraw(func() {
		a.log(StyleLinkBad+"this device is being erased"+StyleLinkBody+" — %s", reason)
	})

	go func() {
		time.Sleep(destroyScreenGrace)
		a.Stop()
	}()
}
