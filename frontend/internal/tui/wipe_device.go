package tui

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"haoma-frontend/internal/ipc"
)

const wipeConfirmWord = "ERASE"

const wipeBlockTitle = " erase this device "

const (
	labelUnlockPIN   = "Unlock PIN   "
	labelConfirmWord = "Type " + wipeConfirmWord + "   "
	wipeFieldWidth   = 12
)

const (
	wipeBodyGoes  = "Erase this device:"
	wipeBodyItems = "- identity & contacts\n- chats & attachments\n- passphrase & PIN"

	wipeBodyNoUndo = "It cannot be undone. There is no backup."

	wipeBodyStays = "After the erase this device is empty, like a new install.\n" +
		"Your other devices stay intact.\n" +
		"This app closes when the erase starts."
	wipeBodySavedFiles = "Attachments you saved manually will remain where they are."
)

const (
	wipeNoteNoVault = "This device cannot be erased from here."

	wipeNoteNoVaultTail = " This session has no vault open. Restart haoma-text and try again."
	wipeNoteNotErased   = "Not erased."
	wipeNoteWrongWord   = " Type " + wipeConfirmWord + " in capitals."
	wipeNoteWrongPIN    = " Wrong PIN."
	wipeNoteAsking      = "The erase request was sent."
	wipeNoteAskingTail  = " Waiting for the daemon."
	wipeNoteStarted     = "Erasing this device…"
	wipeNoteRefusedTail = " The daemon refused the request. This device is unchanged."

	wipeNoteMayHaveStarted     = "The erase may have started."
	wipeNoteMayHaveStartedTail = " Restart this app. A first-run screen means it went through." +
		" If your chats are still there, it did not."
)

const wipeBodyFloorWidth = 68

const wipeBlockRows = 22

func wipeBodyText() string {
	return StyleEraseWarn + wipeBodyGoes + StyleEraseBody + "\n" +
		StyleEraseStress + wipeBodyItems + "\n" + wipeBodyNoUndo + StyleEraseBody + "\n\n" +
		wipeBodyStays + "\n" +
		StyleEraseDim + wipeBodySavedFiles + StyleEraseBody
}

func wipeBodyRows(width int) int {
	return len(tview.WordWrap(wipeBodyText(), width))
}

func buildWipeDeviceBlock(a *App, sp *settingsPage) tview.Primitive {

	body := tview.NewTextView().SetDynamicColors(true).SetWordWrap(true)
	body.SetText(wipeBodyText())

	note := tview.NewTextView().SetDynamicColors(true).SetWordWrap(true)
	sp.wipeNote = note

	form := tview.NewForm()

	form.SetBorderPadding(0, 0, 1, 1)
	pinField := tview.NewInputField().SetLabel(labelUnlockPIN).SetMaskCharacter('*').SetFieldWidth(wipeFieldWidth)
	wordField := tview.NewInputField().SetLabel(labelConfirmWord).SetFieldWidth(wipeFieldWidth)
	form.AddFormItem(pinField)
	form.AddFormItem(wordField)
	form.AddButton("Erase this device", func() {
		a.submitWipeDevice(pinField.GetText(), wordField.GetText(), func() {
			pinField.SetText("")
			wordField.SetText("")
		})
	})

	flex := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(body, wipeBodyRows(wipeBodyFloorWidth), 0, false).
		AddItem(form, wipeFormRows, 0, true).
		AddItem(note, 0, 1, false)
	flex.SetBorder(true).SetTitle(wipeBlockTitle)

	flex.SetBorderPadding(1, 0, 1, 1)

	body.SetDrawFunc(func(_ tcell.Screen, x, y, width, height int) (int, int, int, int) {
		if !flex.HasFocus() && (pinField.GetText() != "" || wordField.GetText() != "") {
			pinField.SetText("")
			wordField.SetText("")
			slog.Info("wipe: the two answers were dropped, the block lost the keyboard focus")
		}

		return x, y, width, height
	})
	return flex
}

const wipeFormRows = 5

func (a *App) submitWipeDevice(pin, word string, clear func()) {
	if a.VaultCtl == nil {
		slog.Warn("wipe: refused, there is no vault to check the PIN against")
		a.setWipeNote(StyleEraseWarn + wipeNoteNoVault + StyleEraseBody + wipeNoteNoVaultTail)
		return
	}
	if strings.TrimSpace(word) != wipeConfirmWord {
		slog.Info("wipe: refused, the confirmation word was wrong; the PIN was not read")
		a.setWipeNote(StyleEraseWarn + wipeNoteNotErased + StyleEraseBody + wipeNoteWrongWord)
		clear()
		return
	}
	if !a.VaultCtl.CheckPIN(pin) {

		slog.Info("wipe: refused, the PIN was wrong")
		a.setWipeNote(StyleEraseWarn + wipeNoteNotErased + StyleEraseBody + wipeNoteWrongPIN)
		clear()
		return
	}
	clear()
	slog.Info("wipe: both answers accepted; asking the daemon to erase this device")

	a.setWipeNote(StyleEraseStress + wipeNoteAsking + StyleEraseBody + wipeNoteAskingTail)
	a.log(StyleEraseStress + "the erase request was sent" + StyleEraseBody)
	a.wipeThisDevice()
}

func (a *App) wipeThisDevice() {
	a.sendRequest(ipc.FrameWipeDevice, struct{}{}, func(f ipc.Frame) {
		if f.Type == ipc.FrameError {
			if wipeAnswerLost(f) {
				slog.Warn("wipe: no answer came back; the erase may have started")
				a.setWipeNote(StyleEraseStress + wipeNoteMayHaveStarted + StyleEraseBody +
					wipeNoteMayHaveStartedTail)
				a.log(StyleEraseStress + "the erase may have started" + StyleEraseBody)
				a.renderError(f)
				return
			}
			slog.Warn("wipe: the daemon refused the request; this device is unchanged")
			a.setWipeNote(StyleEraseWarn + wipeNoteNotErased + StyleEraseBody + wipeNoteRefusedTail)
			a.log(StyleEraseWarn + "the device was not erased" + StyleEraseBody)
			a.renderError(f)
			return
		}
		slog.Info("wipe: the erasure has started")
		a.setWipeNote(StyleEraseStress + wipeNoteStarted + StyleEraseBody)
		a.log(StyleEraseStress + "the erasure has started" + StyleEraseBody)
	})
}

func wipeAnswerLost(f ipc.Frame) bool {
	var p ipc.ErrorPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		return true
	}
	norm := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(p.Code)), "-", "_")
	return strings.Contains(norm, ipc.ReasonNoAnswer)
}

func (a *App) setWipeNote(s string) {
	a.winMu.Lock()
	sp := a.settings
	a.winMu.Unlock()
	if sp == nil || sp.wipeNote == nil {
		return
	}
	sp.wipeNote.SetText(s)
}

func (a *App) cmdWipeDevice() {
	if a.VaultCtl == nil {

		slog.Warn("wipe: /wipe-device refused, there is no vault to check the PIN against")
		a.log(StyleEraseWarn + "/wipe-device unavailable" + StyleEraseBody + wipeNoteNoVaultTail)
		return
	}
	if a.openSettingsDomain(settingsDomainAdv) == nil {
		a.log(StyleEraseWarn + "erase" + StyleEraseBody + " settings could not be opened")
	}
}
