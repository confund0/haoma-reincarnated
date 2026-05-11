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

func buildContactsTable(peers []ipc.PeerEntry, presenceLabel func(peerID string) string, rotationCell func(peerID string) rotationCellInfo, onOpen func(peerID string)) *tview.Table {
	t := tview.NewTable().
		SetBorders(false).
		SetSelectable(true, false).
		SetFixed(1, 0)
	t.SetBorder(true).SetTitle(" contacts ")

	style := tcell.StyleDefault.Bold(true)
	t.SetCell(0, 0, tview.NewTableCell("P").SetStyle(style).SetMaxWidth(1))
	t.SetCell(0, 1, tview.NewTableCell("R").SetStyle(style).SetMaxWidth(1))
	t.SetCell(0, 2, tview.NewTableCell("Nick").SetStyle(style).SetExpansion(2))
	t.SetCell(0, 3, tview.NewTableCell("Alias").SetStyle(style).SetExpansion(2))
	t.SetCell(0, 4, tview.NewTableCell("Peer ID").SetStyle(style).SetExpansion(3))
	t.SetCell(0, 5, tview.NewTableCell("Active").SetStyle(style).SetExpansion(1))
	t.SetCell(0, 6, tview.NewTableCell("Passive").SetStyle(style).SetExpansion(1))

	dimmed := tcell.StyleDefault.Foreground(ColorRetiredRow)
	now := time.Now()
	for i, p := range peers {
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
		var presenceStyle tcell.Style
		if !retired {
			label := "unknown"
			if presenceLabel != nil {
				if v := presenceLabel(p.ID); v != "" {
					label = v
				}
			}
			presenceText = presenceCellText(label)
			presenceStyle = presenceCellStyle(label)
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

		presenceCell := tview.NewTableCell(presenceText).SetMaxWidth(1)
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
		default:
			presenceCell.SetStyle(presenceStyle)
			if nickEmpty {
				nickCell.SetStyle(dimmed)
			}
			if aliasEmpty {
				aliasCell.SetStyle(dimmed)
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

		row := i + 1
		t.SetCell(row, 0, presenceCell)
		t.SetCell(row, 1, rotTableCell)
		t.SetCell(row, 2, nickCell)
		t.SetCell(row, 3, aliasCell)
		t.SetCell(row, 4, idCell)
		t.SetCell(row, 5, activeCell)
		t.SetCell(row, 6, passiveCell)
	}

	t.SetSelectedFunc(func(row, _ int) {
		if row == 0 || row-1 >= len(peers) {
			return
		}
		onOpen(peers[row-1].ID)
	})

	return t
}

func (a *App) installContactsKeys(tbl *tview.Table) {
	pickRow := func() (ipc.PeerEntry, bool) {
		row, _ := tbl.GetSelection()
		if row <= 0 {
			return ipc.PeerEntry{}, false
		}
		a.winMu.Lock()
		defer a.winMu.Unlock()
		if row-1 >= len(a.peers) {
			return ipc.PeerEntry{}, false
		}
		return a.peers[row-1], true
	}
	tbl.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Key() {
		case tcell.KeyEscape:
			a.escPending = true
			return nil
		case tcell.KeyRune:
			switch ev.Rune() {
			case 'e':
				if p, ok := pickRow(); ok {
					a.showPeerModal(p.ID, p.Alias, p.RetiredAt != 0)
				}
				return nil
			case 'f':
				if p, ok := pickRow(); ok {
					a.showFingerprintModal(p.ID, p.Label)
				}
				return nil
			}
		}
		return ev
	})
}

func (a *App) showPeerModal(peerID, current string, retired bool) {
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
	var risksAcked bool
	bottom.AddFormItem(newBracketCheckbox("I understand risks", false, func(checked bool) { risksAcked = checked }))

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
			if !risksAcked {
				a.log("[yellow]check the risks box first[white]")
				return
			}
			dismiss()
			sendAction(ipc.PeerActionRetire, "unpaired")
		})
	}
	bottom.AddButton("Delete peer", func() {
		if !risksAcked {
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

	modal := a.twoSectionModal(title, top, bottom, 5, 5, dismiss)

	grid := tview.NewGrid().
		SetColumns(0, 60, 0).
		SetRows(0, 13, 0).
		AddItem(modal, 1, 1, 1, 1, 0, 0, true)

	a.pages.AddPage(pageName, grid, true, true)
	a.app.SetFocus(top)
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
