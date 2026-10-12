package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"haoma-frontend/internal/ipc"
)

func relativeTime(now time.Time, ts int64) string {
	if ts <= 0 {
		return "—"
	}
	d := now.Sub(time.Unix(ts, 0))
	switch {
	case d < 0:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return time.Unix(ts, 0).Format("2006-01-02")
	}
}

type contactRow struct {
	peer *ipc.PeerEntry
}

const inheritedFrameTitle = "linked device"

const (
	colPresence = iota
	colRotation
	colNick
	colAlias
	colPeerID
	colActive
	colPassive
	contactsColumns
)

func buildContactsTable(rows []contactRow, presenceOf func(peerID string) presenceView, rotationCell func(peerID string) rotationCellInfo, onOpen func(peerID string)) *tview.Table {
	t := tview.NewTable().
		SetBorders(false).
		SetSelectable(true, false).
		SetFixed(1, 0)
	t.SetBorder(true).SetTitle(" contacts ")

	style := tcell.StyleDefault.Bold(true)
	t.SetCell(0, colPresence, tview.NewTableCell("P").SetStyle(style).SetMaxWidth(2))
	t.SetCell(0, colRotation, tview.NewTableCell("R").SetStyle(style).SetMaxWidth(1))
	t.SetCell(0, colNick, tview.NewTableCell("Nick").SetStyle(style).SetExpansion(2))
	t.SetCell(0, colAlias, tview.NewTableCell("Alias").SetStyle(style).SetExpansion(2))
	t.SetCell(0, colPeerID, tview.NewTableCell("Peer ID").SetStyle(style).SetExpansion(3))
	t.SetCell(0, colActive, tview.NewTableCell("Active").SetStyle(style).SetExpansion(1))
	t.SetCell(0, colPassive, tview.NewTableCell("Passive").SetStyle(style).SetExpansion(1))

	dimmed := tcell.StyleDefault.Foreground(ColorRetiredRow)
	now := time.Now()
	for i, r := range rows {
		row := i + 1
		p := *r.peer
		retired := p.RetiredAt != 0

		nickText := p.Nick
		nickEmpty := nickText == ""
		if nickEmpty {
			nickText = "—"
		}
		aliasText := p.Alias
		aliasEmpty := aliasText == ""
		if aliasEmpty {
			aliasText = "—"
		}
		idText := p.ID
		activeText := relativeTime(now, p.LastActiveAt)
		passiveText := relativeTime(now, p.LastPassiveAt)

		presenceText := "—"
		var view presenceView
		if !retired {
			if presenceOf != nil {
				view = presenceOf(p.ID)
			}
			presenceText = presenceArrowsTag(view)
		}
		if retired {
			suffix := " (retired " + time.Unix(p.RetiredAt, 0).Format("2006-01-02") + ")"
			if p.Alias != "" {
				aliasText += suffix
			} else if p.Nick != "" {
				nickText += suffix
			} else {
				idText += suffix
			}
		}

		presenceCell := tview.NewTableCell(presenceText).SetMaxWidth(2)
		nickCell := tview.NewTableCell(nickText).SetExpansion(2)
		aliasCell := tview.NewTableCell(aliasText).SetExpansion(2)
		idCell := tview.NewTableCell(idText).SetExpansion(3)
		activeCell := tview.NewTableCell(activeText).SetExpansion(1)
		passiveCell := tview.NewTableCell(passiveText).SetExpansion(1)
		switch {
		case retired:
			presenceCell.SetStyle(dimmed)
			nickCell.SetStyle(dimmed)
			aliasCell.SetStyle(dimmed)
			idCell.SetStyle(dimmed)
			activeCell.SetStyle(dimmed)
			passiveCell.SetStyle(dimmed)
		case p.Unreachable:

			gone := tcell.StyleDefault.Foreground(ColorUnreachableRow)
			nickCell.SetStyle(gone)
			aliasCell.SetStyle(gone)
			idCell.SetStyle(gone)
			activeCell.SetStyle(gone)
			passiveCell.SetStyle(gone)
		default:

			nameColour, coloured := presenceNameColor(view.chatty)
			switch {
			case nickEmpty:
				nickCell.SetStyle(dimmed)
			case coloured:
				nickCell.SetStyle(tcell.StyleDefault.Foreground(nameColour))
			}
			switch {
			case aliasEmpty:
				aliasCell.SetStyle(dimmed)
			case coloured:
				aliasCell.SetStyle(tcell.StyleDefault.Foreground(nameColour))
			}
			if p.LastActiveAt == 0 {
				activeCell.SetStyle(dimmed)
			}
			if p.LastPassiveAt == 0 {
				passiveCell.SetStyle(dimmed)
			}
		}

		rotText := ""
		var rotInfo rotationCellInfo
		if !retired && rotationCell != nil {
			rotInfo = rotationCell(p.ID)
			switch rotInfo.Direction {
			case "we":
				rotText = GlyphRotationOurs
			case "they":
				rotText = GlyphRotationTheirs
			}
		}
		rotTableCell := tview.NewTableCell(rotText).SetMaxWidth(1)
		if rotText != "" {
			color := ColorRotationActive
			if rotInfo.Cooldown {
				color = ColorRotationCooldown
			}
			rotTableCell.SetStyle(tcell.StyleDefault.Foreground(color))
		} else if retired {
			rotTableCell.SetStyle(dimmed)
		}

		t.SetCell(row, colPresence, presenceCell)
		t.SetCell(row, colRotation, rotTableCell)
		t.SetCell(row, colNick, nickCell)
		t.SetCell(row, colAlias, aliasCell)
		t.SetCell(row, colPeerID, idCell)
		t.SetCell(row, colActive, activeCell)
		t.SetCell(row, colPassive, passiveCell)
	}

	t.SetSelectedFunc(func(row, _ int) {
		if row == 0 || row-1 >= len(rows) {
			return
		}
		r := rows[row-1]
		if r.peer == nil {

			return
		}
		onOpen(r.peer.ID)
	})

	return t
}

func (a *App) contactRowsForTable() []contactRow {
	a.winMu.Lock()
	defer a.winMu.Unlock()
	rows := make([]contactRow, 0, len(a.peers))
	for i := range a.peers {

		if a.peers[i].Sibling {
			continue
		}
		rows = append(rows, contactRow{peer: &a.peers[i]})
	}
	a.contactRows = rows
	return rows
}

func (a *App) rebuildContactsPage() {
	anchor := a.contactAnchor()
	frameAnchor, frameWasFocused := a.inheritedFrameAnchor()
	rows := a.contactRowsForTable()
	front, _ := a.pages.GetFrontPage()

	a.contactTable = buildContactsTable(rows, a.peerPresenceOf, a.peerRotationCell, a.openChat)
	a.installContactsKeys(a.contactTable)
	selectContactRow(a.contactTable, rows, anchor)

	a.winMu.Lock()
	linked := a.linked
	a.winMu.Unlock()

	var page tview.Primitive = a.contactTable
	if linked {
		drawn, frame, frameRows := buildInheritedFrame(a.inheritedFrameEntries(), a.inheritedFrameHeading())

		if frame != nil {
			a.installInheritedFrameKeys(frame)
		}
		a.winMu.Lock()
		a.inheritedFrame = frame
		a.inheritedRows = frameRows
		a.winMu.Unlock()
		if frame != nil {
			selectInheritedRow(frame, frameRows, frameAnchor)
		} else {

			frameWasFocused = false
		}
		page = tview.NewFlex().
			SetDirection(tview.FlexColumn).
			AddItem(a.contactTable, 0, 1, true).
			AddItem(drawn, inheritedFrameWidth, 0, false)
	} else {
		a.winMu.Lock()
		a.inheritedFrame = nil
		a.inheritedRows = nil
		a.winMu.Unlock()
		frameWasFocused = false
	}

	a.pages.RemovePage("contacts")
	a.pages.AddPage("contacts", page, true, false)
	if front == "contacts" {
		a.pages.SwitchToPage("contacts")
		if frameWasFocused {
			a.focusInheritedFrame()
		} else {

			a.focusContactsTable()
		}
	}
}

func (a *App) focusContactsTable() {
	if a.contactTable == nil {
		return
	}
	a.app.SetFocus(a.contactTable)
	a.setHint(a.hintBarText("contacts"))
	a.repaintInheritedCursor()
}

func (a *App) repaintInheritedCursor() {
	a.winMu.Lock()
	frame := a.inheritedFrame
	rows := a.inheritedRows
	a.winMu.Unlock()
	if frame == nil {
		return
	}
	paintInheritedCursor(frame, rows)
}

func (a *App) focusInheritedFrame() {
	a.winMu.Lock()
	frame := a.inheritedFrame
	a.winMu.Unlock()
	if frame == nil {
		return
	}
	a.app.SetFocus(frame)
	a.setHint(hintInherited)
	a.repaintInheritedCursor()
}

func (a *App) inheritedFrameAnchor() (string, bool) {
	a.winMu.Lock()
	frame := a.inheritedFrame
	rows := a.inheritedRows
	a.winMu.Unlock()
	if frame == nil {
		return "", false
	}
	focused := frame.HasFocus()
	row, _ := frame.GetSelection()
	if row < 0 || row >= len(rows) {
		return "", focused
	}
	return inheritedRowKey(rows[row]), focused
}

func inheritedRowKey(r inheritedRow) string {
	switch {
	case r.all:
		return "action:all"
	case r.fingerprint != "" && r.state == "":
		return "contact:" + r.fingerprint
	}
	return ""
}

func selectInheritedRow(t *tview.Table, rows []inheritedRow, anchor string) {
	if anchor == "" {
		return
	}
	for i, r := range rows {
		if inheritedRowKey(r) == anchor {
			t.Select(i, 0)
			return
		}
	}
}

func (a *App) inheritedFrameHeading() string {
	a.winMu.Lock()
	var view presenceView
	for i := range a.peers {
		if !a.peers[i].Sibling {
			continue
		}
		view = a.peerPresence[a.peers[i].ID]
		break
	}
	a.winMu.Unlock()
	return presenceArrowsTag(view) + " " + inheritedFrameTitle
}

func (a *App) contactAnchor() string {
	if a.contactTable == nil {
		return ""
	}
	row, _ := a.contactTable.GetSelection()
	if row <= 0 {
		return ""
	}
	a.winMu.Lock()
	defer a.winMu.Unlock()
	if row-1 >= len(a.contactRows) {
		return ""
	}
	return contactRowKey(a.contactRows[row-1])
}

func selectContactRow(t *tview.Table, rows []contactRow, anchor string) {
	if anchor == "" {
		return
	}
	for i, r := range rows {
		if contactRowKey(r) == anchor {
			t.Select(i+1, 0)
			return
		}
	}
}

func contactRowKey(r contactRow) string {
	if r.peer == nil {
		return ""
	}
	return "pairing:" + r.peer.ID
}

func (a *App) installContactsKeys(tbl *tview.Table) {
	pickRow := func() (contactRow, bool) {
		row, _ := tbl.GetSelection()
		if row <= 0 {
			return contactRow{}, false
		}
		a.winMu.Lock()
		defer a.winMu.Unlock()
		if row-1 >= len(a.contactRows) {
			return contactRow{}, false
		}
		return a.contactRows[row-1], true
	}
	tbl.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Key() {
		case tcell.KeyEscape:
			a.escPending = true
			return nil
		case tcell.KeyTab, tcell.KeyBacktab:
			a.focusInheritedFrame()
			return nil
		case tcell.KeyRune:
			switch ev.Rune() {
			case 'e':
				if r, ok := pickRow(); ok && r.peer != nil {
					a.showPeerModal(r.peer.ID, r.peer.Alias, r.peer.RetiredAt != 0, r.peer.Unreachable)
				}
				return nil
			case 'f':
				if r, ok := pickRow(); ok && r.peer != nil {
					a.showFingerprintModal(r.peer.ID, r.peer.Label)
				}
				return nil
			}
		}
		return ev
	})
}

func (a *App) showPeerModal(peerID, current string, retired, unreachable bool) {
	const pageName = "peer-modal"

	dismiss := func() {
		a.pages.RemovePage(pageName)
		a.switchTo("contacts")
	}

	top := tview.NewForm()
	top.SetButtonsAlign(tview.AlignCenter)
	top.AddInputField("Alias", current, 30, nil, nil)

	saveAlias := func() {
		newAlias := top.GetFormItemByLabel("Alias").(*tview.InputField).GetText()
		if newAlias == current {
			return
		}

		a.sendRequest(ipc.FrameSetAlias, ipc.SetAliasRequest{
			PeerID: peerID,
			Alias:  newAlias,
		}, func(f ipc.Frame) {
			if f.Type == ipc.FrameError {
				a.renderError(f)
				return
			}
			if f.Type != ipc.FrameAliasUpdated {
				a.log("[red]alias edit[white] unexpected response: %s", f.Type)
				return
			}
			var p ipc.AliasUpdatedResponse
			if err := json.Unmarshal(f.Payload, &p); err != nil {
				a.log("[red]alias edit[white] decode: %v", err)
				return
			}
			a.log("[green]renamed[white] %s → %s", shortID(p.Peer.ID), coalesce(p.Peer.Label, "(unnamed)"))
		})
	}
	top.AddButton("Save", func() {
		saveAlias()
		dismiss()
	})
	top.AddButton("Cancel", dismiss)

	bottom := tview.NewForm()
	bottom.SetButtonsAlign(tview.AlignCenter)

	risksBox := newBracketCheckbox("I understand risks", false, nil)
	bottom.AddFormItem(risksBox)

	sendAction := func(action ipc.PeerAction, label string) {

		a.sendRequest(ipc.FramePeerAction, ipc.PeerActionRequest{
			PeerID: peerID,
			Action: action,
		}, func(f ipc.Frame) {
			if f.Type == ipc.FrameError {
				a.renderError(f)
				return
			}
			if f.Type != ipc.FramePeerActionApplied {
				a.log("[red]peer action[white] unexpected response: %s", f.Type)
				return
			}
			var p ipc.PeerActionAppliedResponse
			if err := json.Unmarshal(f.Payload, &p); err != nil {
				a.log("[red]peer action[white] decode: %v", err)
				return
			}
			displayed := coalesce(p.Peer.Label, "(unnamed)")
			if p.DeletedCount > 0 {
				a.log("[green]%s[white] %s (%d events purged)", label, displayed, p.DeletedCount)
			} else {
				a.log("[green]%s[white] %s", label, displayed)
			}
		})
	}

	if !retired {
		bottom.AddButton("Unpair", func() {
			if !risksBox.Checked() {
				a.log("[yellow]check the risks box first[white]")
				return
			}
			dismiss()
			sendAction(ipc.PeerActionRetire, "unpaired")
		})
	}
	bottom.AddButton("Delete peer", func() {
		if !risksBox.Checked() {
			a.log("[yellow]check the risks box first[white]")
			return
		}
		dismiss()
		sendAction(ipc.PeerActionDelete, "deleted")
	})

	title := "edit contact"
	if retired {
		title = "edit contact — retired"
	}

	var header *tview.TextView
	headerRows := 0
	if unreachable {
		header = unreachableWarningView()
		headerRows = unreachableWarningRows
	}
	modal := a.twoSectionModalWithHeader(title, header, headerRows, top, bottom, 5, 5, dismiss)

	grid := tview.NewGrid().
		SetColumns(0, 60, 0).
		SetRows(0, 13+headerRows, 0).
		AddItem(modal, 1, 1, 1, 1, 0, 0, true)

	a.pages.AddPage(pageName, grid, true, true)
	a.app.SetFocus(top)
}

const unreachableWarning = "This contact does not answer. If you know they are gone, " +
	"use Delete peer at the bottom of this page. THIS ALSO DELETES THE CHAT HISTORY."

const unreachableWarningRows = 4

func unreachableWarningView() *tview.TextView {
	return tview.NewTextView().
		SetDynamicColors(true).
		SetWordWrap(true).
		SetText(StyleUnreachable + tview.Escape(unreachableWarning) + StyleReset)
}

func (a *App) showFingerprintModal(peerID, label string) {
	const pageName = "fingerprint-modal"

	dismiss := func() {
		a.pages.RemovePage(pageName)
		a.switchTo("contacts")
	}

	displayLabel := label
	if displayLabel == "" {
		displayLabel = shortID(peerID)
	}

	form := tview.NewForm()
	form.SetButtonsAlign(tview.AlignCenter)
	form.AddTextView("Peer", displayLabel, 0, 1, false, false)
	form.AddTextView("Fingerprint", "(loading…)", 0, 1, false, false)
	form.AddButton("Close", dismiss)

	form.SetBorder(true).SetTitle(" peer fingerprint ")

	const modalWidth = 96
	grid := tview.NewGrid().
		SetColumns(0, modalWidth, 0).
		SetRows(0, 9, 0).
		AddItem(form, 1, 1, 1, 1, 0, 0, true)

	grid.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEscape {
			dismiss()
			return nil
		}
		return ev
	})

	a.pages.AddPage(pageName, grid, true, true)
	a.app.SetFocus(form)

	a.sendRequest(ipc.FrameGetPeerFingerprint, ipc.GetPeerFingerprintRequest{
		PeerID: peerID,
	}, func(f ipc.Frame) {
		display := "(unavailable)"
		if f.Type == ipc.FramePeerFingerprint {
			var p ipc.PeerFingerprintPayload
			if err := json.Unmarshal(f.Payload, &p); err == nil {
				display = formatFingerprint(p.Fingerprint)
			}
		}
		a.app.QueueUpdateDraw(func() {
			if !a.pages.HasPage(pageName) {
				return
			}
			if item := form.GetFormItemByLabel("Fingerprint"); item != nil {
				if tv, ok := item.(*tview.TextView); ok {
					tv.SetText(display)
				}
			}
		})
	})
}

func formatFingerprint(hex string) string {
	if hex == "" {
		return "(no session yet — exchange a message first)"
	}
	const groupSize = 6
	groups := make([]string, 0, len(hex)/groupSize+1)
	for i := 0; i < len(hex); i += groupSize {
		end := i + groupSize
		if end > len(hex) {
			end = len(hex)
		}
		groups = append(groups, hex[i:end])
	}
	return strings.Join(groups, " ")
}
