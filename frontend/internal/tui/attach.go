package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"haoma-frontend/internal/paths"
	"haoma-frontend/internal/tui/haomafiledialog"
)

func (a *App) cmdAttach() {
	active := a.activeChat()
	if active == "" {
		a.log("[red]/attach[white] must be used inside a chat window")
		return
	}
	if a.peerRetiredAt(active) != 0 {
		a.log("[red]peer retired[white] — can't attach")
		return
	}
	a.openAttachPicker(active)
}

func (a *App) openAttachPicker(peerID string) {
	startDir := paths.ResolveAttachStartDir(a.vaultAttachDir())
	dlg := haomafiledialog.New(haomafiledialog.Options{
		Mode:     haomafiledialog.ModeFileSelect,
		StartDir: startDir,
		Title:    "attach — pick a file to send",
		OnPick: func(path string) {
			a.showAttachConfirm(peerID, path)
		},
		OnCancel: func() {
			a.app.SetFocus(a.input)
		},
	})
	dlg.Show(a.app, a.pages, "attach-dialog")
}

func (a *App) vaultAttachDir() string {
	if a.VaultCtl == nil {
		return ""
	}
	return a.VaultCtl.Settings().DefaultAttachStartDir
}

func (a *App) showAttachConfirm(peerID, path string) {
	const pageName = "attach-confirm"

	info, err := os.Stat(path)
	if err != nil {
		a.log("[red]/attach[white] stat failed: %v", err)
		a.app.SetFocus(a.input)
		return
	}
	if info.IsDir() {
		a.log("[red]/attach[white] %s is a directory, not a file", path)
		a.app.SetFocus(a.input)
		return
	}

	name := filepath.Base(path)
	sizeStr := formatFileSize(uint64(info.Size()))

	a.winMu.Lock()
	peerLabel := a.peerNickLocked(peerID)
	a.winMu.Unlock()
	if peerLabel == "" {
		peerLabel = shortID(peerID)
	}

	text := fmt.Sprintf("Send %s (%s) to %s?\n\n  Path: %s",
		name, sizeStr, peerLabel, path)

	summary := tview.NewTextView().SetDynamicColors(true).SetText(text)

	var caption string
	form := tview.NewForm()
	form.SetButtonsAlign(tview.AlignCenter)

	form.AddInputField("Caption (optional)", "", 0, nil, func(v string) { caption = v })

	dismissTo := func(focusInput bool) {
		a.pages.RemovePage(pageName)
		if focusInput {
			a.app.SetFocus(a.input)
		}
	}
	form.AddButton("Send", func() {
		dismissTo(true)
		a.dispatchSendFileToPeer(peerID, path, strings.TrimSpace(caption))
	})
	form.AddButton("Pick again", func() {

		dismissTo(false)
		a.openAttachPicker(peerID)
	})
	form.AddButton("Cancel", func() { dismissTo(true) })
	form.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEscape {
			dismissTo(true)
			return nil
		}
		return ev
	})

	flex := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(summary, 5, 0, false).
		AddItem(form, 0, 1, true)
	flex.SetBorder(true).SetTitle(" send attachment ")

	grid := tview.NewGrid().
		SetColumns(0, 80, 0).
		SetRows(0, 12, 0).
		AddItem(flex, 1, 1, 1, 1, 0, 0, true)

	a.pages.AddPage(pageName, grid, true, true)
	a.app.SetFocus(form)
}
