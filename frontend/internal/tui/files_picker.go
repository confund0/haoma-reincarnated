package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"haoma-frontend/internal/files/opener"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/paths"
	"haoma-frontend/internal/tui/haomafiledialog"
)

func (a *App) cmdFiles() {
	front, _ := a.pages.GetFrontPage()
	if !strings.HasPrefix(front, "chat:") {
		a.log("[red]/files[white] must be used inside a chat window")
		return
	}
	chatID := strings.TrimPrefix(front, "chat:")
	a.winMu.Lock()
	cp := a.chatPages[chatID]
	a.winMu.Unlock()
	if cp == nil {
		a.log("[red]/files[white] no active chat page")
		return
	}
	a.sendRequest(ipc.FrameListFiles, ipc.ListFilesRequest{ChatID: chatID}, func(f ipc.Frame) {
		if f.Type == ipc.FrameError {
			a.renderError(f)
			return
		}
		if f.Type != ipc.FrameFilesList {
			a.log("[red]/files[white] unexpected response: %s", f.Type)
			return
		}
		var p ipc.FilesListResponse
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			a.log("[red]/files[white] decode failed: %v", err)
			return
		}
		a.app.QueueUpdateDraw(func() {
			a.showFilesPicker(cp, p.Files)
		})
	})
}

func (a *App) showFilesPicker(cp *chatPage, files []ipc.FileEntry) {
	const pageName = "files-picker"

	table := tview.NewTable().
		SetBorders(false).
		SetSelectable(true, false).
		SetFixed(1, 0)

	headerStyle := tcell.StyleDefault.Bold(true)
	headers := []struct {
		label  string
		expand int
	}{
		{"Time", 1},
		{"Dir", 1},
		{"Name", 5},
		{"Size", 1},
		{"MIME", 3},
		{"State", 2},
	}
	for col, h := range headers {
		table.SetCell(0, col, tview.NewTableCell(h.label).SetStyle(headerStyle).SetExpansion(h.expand))
	}

	for r, fe := range files {
		stamp := "—"
		if fe.DisplayTs > 0 {
			stamp = time.Unix(fe.DisplayTs, 0).Format("01-02 15:04")
		}
		dir := filesDirGlyph(fe.Direction)
		name := fe.OriginalName
		if name == "" {
			name = "(unnamed)"
		}
		mime := fe.Mime
		if mime == "" {
			mime = "—"
		}
		state := fe.State
		if state == "" {
			state = "pending"
		}
		table.SetCell(r+1, 0, tview.NewTableCell(stamp).SetExpansion(1))
		table.SetCell(r+1, 1, tview.NewTableCell(dir).SetExpansion(1))
		table.SetCell(r+1, 2, tview.NewTableCell(name).SetExpansion(5))
		table.SetCell(r+1, 3, tview.NewTableCell(formatFileSize(fe.Size)).SetExpansion(1))
		table.SetCell(r+1, 4, tview.NewTableCell(mime).SetExpansion(3))
		table.SetCell(r+1, 5, tview.NewTableCell(state).SetExpansion(2))
	}
	if len(files) > 0 {
		table.Select(1, 0)
	}

	dismiss := func() {
		a.pages.RemovePage(pageName)
		if cp != nil {
			pageID := "chat:" + cp.chatID
			a.pages.SwitchToPage(pageID)
			a.app.SetFocus(a.input)
		}
	}

	table.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Key() {
		case tcell.KeyEscape:
			dismiss()
			return nil
		case tcell.KeyEnter:
			row, _ := table.GetSelection()
			if row > 0 && row-1 < len(files) {
				a.showFileActionModal(cp, table, files[row-1])
			}
			return nil
		}
		return ev
	})

	title := fmt.Sprintf(" files — %d in this chat ", len(files))
	if len(files) == 0 {
		title = " files — none in this chat "
	}
	frame := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(table, 0, 1, true)
	frame.SetBorder(true).SetTitle(title)

	grid := tview.NewGrid().
		SetColumns(0, 100, 0).
		SetRows(0, 20, 0).
		AddItem(frame, 1, 1, 1, 1, 0, 0, true)

	a.pages.AddPage(pageName, grid, true, true)
	a.app.SetFocus(table)
}

func (a *App) showFileActionModal(cp *chatPage, table *tview.Table, fe ipc.FileEntry) {
	const pageName = "files-action"

	name := fe.OriginalName
	if name == "" {
		name = "(unnamed)"
	}
	mime := fe.Mime
	if mime == "" {
		mime = "—"
	}
	state := fe.State
	if state == "" {
		state = "pending"
	}

	saveable := fe.State == "ready"
	openable := saveable && opener.Detect().Available()
	deletable := fe.Deletable

	lead := "Attachment"
	if !saveable && !deletable {
		lead = fmt.Sprintf("Attachment (no actions — state: %s)", state)
	}
	text := fmt.Sprintf("%s\n\n  Name: %s\n  Size: %s\n  MIME: %s\n  State: %s",
		lead, name, formatFileSize(fe.Size), mime, state)

	buttons := []string{}
	if saveable {
		buttons = append(buttons, "Save as")
	}
	if openable {
		buttons = append(buttons, "Open")
	}
	if deletable {
		buttons = append(buttons, "Delete")
	}
	buttons = append(buttons, "Cancel")

	modal := tview.NewModal().
		SetText(text).
		AddButtons(buttons)
	modal.SetDoneFunc(func(_ int, label string) {
		a.pages.RemovePage(pageName)
		switch label {
		case "Save as":
			a.showFileSaveDialog(cp, table, fe)
		case "Open":
			a.dispatchOpenFile(cp, table, fe)
		case "Delete":
			a.showFileDeleteConfirm(cp, table, fe)
		default:
			a.app.SetFocus(table)
		}
	})
	a.pages.AddPage(pageName, modal, true, true)
	a.app.SetFocus(modal)
}

func (a *App) showFileDeleteConfirm(cp *chatPage, table *tview.Table, fe ipc.FileEntry) {
	const pageName = "files-delete-confirm"

	name := fe.OriginalName
	if name == "" {
		name = "(unnamed)"
	}
	text := fmt.Sprintf("Delete %s?\n\n"+
		"This removes the file for both sender and recipient.\n"+
		"This cannot be undone.", name)

	modal := tview.NewModal().
		SetText(text).
		AddButtons([]string{"Delete", "Cancel"})
	modal.SetDoneFunc(func(_ int, label string) {
		a.pages.RemovePage(pageName)
		switch label {
		case "Delete":
			a.dispatchDeleteFile(cp, table, fe)
		default:
			a.app.SetFocus(table)
		}
	})
	a.pages.AddPage(pageName, modal, true, true)
	a.app.SetFocus(modal)
}

func (a *App) dispatchDeleteFile(cp *chatPage, table *tview.Table, fe ipc.FileEntry) {
	a.sendRequest(ipc.FrameSendDelete, ipc.SendDeleteRequest{
		PeerID:      cp.peerID,
		TargetMsgID: fe.MsgID,
	}, func(resp ipc.Frame) {
		if resp.Type == ipc.FrameError {
			a.renderError(resp)
			a.app.QueueUpdateDraw(func() { a.app.SetFocus(table) })
			return
		}
		if resp.Type != ipc.FrameDeleteSent {
			a.log("[red]/files[white] delete unexpected response: %s", resp.Type)
			a.app.QueueUpdateDraw(func() { a.app.SetFocus(table) })
			return
		}
		name := fe.OriginalName
		if name == "" {
			name = fe.MsgID
		}
		a.log("[green]/files[white] deleted %s", name)

		a.app.QueueUpdateDraw(func() {
			a.pages.RemovePage("files-picker")
			a.cmdFiles()
		})
	})
}

func (a *App) showFileSaveDialog(cp *chatPage, table *tview.Table, fe ipc.FileEntry) {
	const pageName = "files-save-dialog"

	dlg := haomafiledialog.New(haomafiledialog.Options{
		Mode:     haomafiledialog.ModeDirSelect,
		StartDir: paths.ResolveSaveStartDir(a.vaultSaveDir()),
		Title:    "save attachment — pick destination directory",
		OnPick: func(destDir string) {
			a.dispatchSaveFile(cp, table, fe, destDir)
		},
		OnCancel: func() {
			a.app.SetFocus(table)
		},
	})
	dlg.Show(a.app, a.pages, pageName)
}

func (a *App) vaultSaveDir() string {
	if a.VaultCtl == nil {
		return ""
	}
	return a.VaultCtl.Settings().DefaultSaveDir
}

func (a *App) dispatchOpenFile(cp *chatPage, table *tview.Table, fe ipc.FileEntry) {
	a.sendRequest(ipc.FrameOpenFile, ipc.OpenFileRequest{
		ChatID: cp.chatID,
		MsgID:  fe.MsgID,
	}, func(resp ipc.Frame) {
		if resp.Type == ipc.FrameError {
			a.renderError(resp)
			a.app.QueueUpdateDraw(func() { a.app.SetFocus(table) })
			return
		}
		if resp.Type != ipc.FrameFileOpenReady {
			a.log("[red]/files[white] open unexpected response: %s", resp.Type)
			a.app.QueueUpdateDraw(func() { a.app.SetFocus(table) })
			return
		}
		var p ipc.OpenFileReadyResponse
		if err := json.Unmarshal(resp.Payload, &p); err != nil {
			a.log("[red]/files[white] open decode failed: %v", err)
			a.app.QueueUpdateDraw(func() { a.app.SetFocus(table) })
			return
		}
		if !p.MIMEMatches {
			a.app.QueueUpdateDraw(func() {
				a.showFileOpenWarnModal(table, fe, p)
			})
			return
		}
		a.spawnOpener(table, p.FullPath)
	})
}

func (a *App) showFileOpenWarnModal(table *tview.Table, fe ipc.FileEntry, p ipc.OpenFileReadyResponse) {
	const pageName = "files-open-warn"

	declared := fe.Mime
	if declared == "" {
		declared = "(no declared MIME)"
	}
	sniffed := p.SniffedMIME
	if sniffed == "" {
		sniffed = "(unknown)"
	}
	text := fmt.Sprintf("MIME mismatch.\n\n"+
		"  Sender claimed: %s\n"+
		"  Bytes look like: %s\n\n"+
		"Open anyway?", declared, sniffed)

	modal := tview.NewModal().
		SetText(text).
		AddButtons([]string{"Open anyway", "Cancel"})
	modal.SetDoneFunc(func(_ int, label string) {
		a.pages.RemovePage(pageName)
		switch label {
		case "Open anyway":
			a.spawnOpener(table, p.FullPath)
		default:
			a.app.SetFocus(table)
		}
	})
	a.pages.AddPage(pageName, modal, true, true)
	a.app.SetFocus(modal)
}

func (a *App) spawnOpener(table *tview.Table, path string) {
	op := opener.Detect()
	if !op.Available() {
		a.log("[red]/files[white] open failed: no per-OS opener detected")
		a.app.QueueUpdateDraw(func() { a.app.SetFocus(table) })
		return
	}
	if err := op.Open(context.Background(), path); err != nil {
		a.log("[red]/files[white] open failed: %v", err)
		a.app.QueueUpdateDraw(func() { a.app.SetFocus(table) })
		return
	}
	a.log("[green]/files[white] opening %s (via %s)", path, op.Name())
	a.app.QueueUpdateDraw(func() { a.app.SetFocus(table) })
}

func (a *App) dispatchSaveFile(cp *chatPage, table *tview.Table, fe ipc.FileEntry, destDir string) {
	a.sendRequest(ipc.FrameSaveFile, ipc.SaveFileRequest{
		ChatID:  cp.chatID,
		MsgID:   fe.MsgID,
		DestDir: destDir,
	}, func(resp ipc.Frame) {
		if resp.Type == ipc.FrameError {
			a.renderError(resp)
			a.app.QueueUpdateDraw(func() { a.app.SetFocus(table) })
			return
		}
		if resp.Type != ipc.FrameFileSaved {
			a.log("[red]/files[white] save unexpected response: %s", resp.Type)
			a.app.QueueUpdateDraw(func() { a.app.SetFocus(table) })
			return
		}
		var p ipc.SaveFileResponse
		if err := json.Unmarshal(resp.Payload, &p); err != nil {
			a.log("[red]/files[white] save decode failed: %v", err)
			a.app.QueueUpdateDraw(func() { a.app.SetFocus(table) })
			return
		}
		a.log("[green]/files[white] saved to %s", p.FullPath)
		a.app.QueueUpdateDraw(func() { a.app.SetFocus(table) })
	})
}

func filesDirGlyph(direction string) string {
	switch direction {
	case "out":
		return "<-"
	case "in":
		return "->"
	default:
		return "??"
	}
}
