package tui

import (
	"fmt"
	"os"
	"path/filepath"

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

	startDir := paths.ResolveAttachStartDir(a.vaultAttachDir())
	dlg := haomafiledialog.New(haomafiledialog.Options{
		Mode:     haomafiledialog.ModeFileSelect,
		StartDir: startDir,
		Title:    "attach — pick a file to send",
		OnPick: func(path string) {
			a.showAttachConfirm(active, path)
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

	modal := tview.NewModal().
		SetText(text).
		AddButtons([]string{"Send", "Cancel"})
	modal.SetDoneFunc(func(_ int, label string) {
		a.pages.RemovePage(pageName)
		switch label {
		case "Send":
			a.dispatchSendFileToPeer(peerID, path)
		}
		a.app.SetFocus(a.input)
	})
	a.pages.AddPage(pageName, modal, true, true)
	a.app.SetFocus(modal)
}
