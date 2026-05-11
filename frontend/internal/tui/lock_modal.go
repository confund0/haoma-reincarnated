package tui

import (
	"crypto/subtle"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"haoma-frontend/internal/ipc"
)

const pinMaxAttempts = 3

type pinOutcome int

const (
	pinOK pinOutcome = iota
	pinWrong
	pinEscalate
)

func (a *App) ShowPINGate(expected string, validitySec int) bool {
	resultCh := make(chan pinOutcome, 1)
	deliver := func(v pinOutcome) {

		select {
		case resultCh <- v:
		default:
		}
	}

	var timer *time.Timer
	if validitySec > 0 {
		timer = time.AfterFunc(time.Duration(validitySec)*time.Second, func() {
			deliver(pinEscalate)
		})
	}

	buildAndShow := func(retryBanner bool) {
		field := tview.NewInputField().
			SetLabel("PIN: ").
			SetMaskCharacter('•').
			SetFieldBackgroundColor(tcell.ColorDefault)

		msgText := "Enter PIN to unlock. Ctrl-D = hard-lock."
		if retryBanner {
			msgText = "[red]Wrong PIN.[-] Try again. Ctrl-D = hard-lock."
		}
		msg := tview.NewTextView().
			SetDynamicColors(true).
			SetText(msgText)
		msg.SetTextAlign(tview.AlignLeft)

		field.SetDoneFunc(func(key tcell.Key) {
			if key != tcell.KeyEnter {
				return
			}
			if subtle.ConstantTimeCompare([]byte(field.GetText()), []byte(expected)) == 1 {
				deliver(pinOK)
				return
			}
			deliver(pinWrong)
		})

		field.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
			if ev.Key() == tcell.KeyCtrlD {
				deliver(pinEscalate)
				return nil
			}
			return ev
		})

		inner := tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(msg, 1, 0, false).
			AddItem(field, 1, 0, true)
		inner.SetBorder(true).
			SetBorderPadding(1, 1, 1, 0).
			SetTitle(" LOCKED ").
			SetTitleAlign(tview.AlignCenter).
			SetTitleColor(tcell.ColorRed)

		const modalW = 56
		const modalH = 6
		grid := tview.NewGrid().
			SetColumns(0, modalW, 0).
			SetRows(0, modalH, 0).
			AddItem(inner, 1, 1, 1, 1, 0, 0, true)

		a.app.QueueUpdateDraw(func() {
			a.app.SetRoot(grid, true).SetFocus(field)
		})
	}

	a.locked.Store(true)

	a.sendRequest(ipc.FrameClientLockState, ipc.ClientLockStateRequest{SoftLocked: true}, nil)

	buildAndShow(false)

	attempts := 0
	var ok bool
loop:
	for {
		switch <-resultCh {
		case pinOK:
			ok = true
			break loop
		case pinWrong:
			attempts++
			if attempts >= pinMaxAttempts {
				ok = false
				break loop
			}
			buildAndShow(true)
		case pinEscalate:
			ok = false
			break loop
		}
	}

	if timer != nil {
		timer.Stop()
	}

	a.app.QueueUpdateDraw(func() {
		a.app.SetRoot(a.mainRoot, true).SetFocus(a.input)
	})
	a.locked.Store(false)
	a.sendRequest(ipc.FrameClientLockState, ipc.ClientLockStateRequest{SoftLocked: false}, nil)

	if ok {
		a.lastActivity.Store(time.Now().Unix())
	}
	return ok
}
