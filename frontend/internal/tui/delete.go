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

type deleteCandidate struct {
	msgID     string
	text      string
	displayTs int64
}

func (a *App) cmdDelete() {
	front, _ := a.pages.GetFrontPage()
	if !strings.HasPrefix(front, "chat:") {
		a.log("[red]/delete[white] must be used inside a chat window")
		return
	}
	chatID := strings.TrimPrefix(front, "chat:")
	a.winMu.Lock()
	cp := a.chatPages[chatID]
	a.winMu.Unlock()
	if cp == nil {
		a.log("[red]/delete[white] no active chat page")
		return
	}
	cands := collectDeletable(cp)
	if len(cands) == 0 {
		a.log("[yellow]/delete[white] no deletable messages (own text, last 24h, not already deleted)")
		return
	}
	a.showDeletePicker(cp, cands)
}

func collectDeletable(cp *chatPage) []deleteCandidate {
	cutoff := time.Now().Unix() - editWindowSeconds
	var out []deleteCandidate
	for _, e := range cp.entries {
		var ev rawEvent
		if err := json.Unmarshal(e.evJSON, &ev); err != nil {
			continue
		}
		if ev.Direction != "out" || ev.Kind != "text" {
			continue
		}
		if ev.DecryptStatus == "failed" {
			continue
		}
		if ev.MsgID == "" {
			continue
		}
		if ev.DisplayTs < cutoff {
			continue
		}
		if ev.DeletedAt > 0 {
			continue
		}
		var body struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(ev.Body, &body)
		out = append(out, deleteCandidate{
			msgID:     ev.MsgID,
			text:      body.Text,
			displayTs: ev.DisplayTs,
		})
	}
	return out
}

func (a *App) showDeletePicker(cp *chatPage, cands []deleteCandidate) {
	const pageName = "delete-picker"

	table := tview.NewTable().
		SetBorders(false).
		SetSelectable(true, false).
		SetFixed(1, 0)
	headerStyle := tcell.StyleDefault.Bold(true)
	table.SetCell(0, 0, tview.NewTableCell("Time").SetStyle(headerStyle).SetExpansion(1))
	table.SetCell(0, 1, tview.NewTableCell("Message").SetStyle(headerStyle).SetExpansion(6))

	visible := make([]int, len(cands))
	for i := range cands {
		visible[i] = i
	}

	refill := func() {
		for r := table.GetRowCount() - 1; r >= 1; r-- {
			table.RemoveRow(r)
		}
		for r, idx := range visible {
			c := cands[idx]
			table.SetCell(r+1, 0, tview.NewTableCell(time.Unix(c.displayTs, 0).Format("15:04")).SetExpansion(1))
			preview := c.text
			if len(preview) > 80 {
				preview = preview[:80] + "…"
			}
			table.SetCell(r+1, 1, tview.NewTableCell(preview).SetExpansion(6))
		}
		if len(visible) > 0 {
			table.Select(1, 0)
		}
	}

	filter := tview.NewInputField().
		SetLabel("filter ").
		SetFieldWidth(40)
	filter.SetChangedFunc(func(text string) {
		q := strings.ToLower(strings.TrimSpace(text))
		visible = visible[:0]
		for i, c := range cands {
			if q == "" || strings.Contains(strings.ToLower(c.text), q) {
				visible = append(visible, i)
			}
		}
		refill()
	})

	dismiss := func() {
		a.pages.RemovePage(pageName)
		if cp != nil {
			pageID := "chat:" + cp.chatID
			a.pages.SwitchToPage(pageID)
			a.app.SetFocus(a.input)
		}
	}

	pick := func(row int) {
		if row <= 0 || row-1 >= len(visible) {
			return
		}
		c := cands[visible[row-1]]
		dismiss()
		a.showDeleteConfirm(cp, c)
	}

	table.SetSelectedFunc(func(row, _ int) { pick(row) })

	filter.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Key() {
		case tcell.KeyEnter:
			row, _ := table.GetSelection()
			pick(row)
			return nil
		case tcell.KeyDown, tcell.KeyUp:
			a.app.SetFocus(table)
			return ev
		case tcell.KeyEscape:
			dismiss()
			return nil
		}
		return ev
	})

	table.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEscape {
			dismiss()
			return nil
		}
		return ev
	})

	refill()

	flex := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(filter, 1, 0, true).
		AddItem(table, 0, 1, false)
	flex.SetBorder(true).SetTitle(fmt.Sprintf(" delete message — %d candidate(s) ", len(cands)))

	grid := tview.NewGrid().
		SetColumns(0, 80, 0).
		SetRows(0, 20, 0).
		AddItem(flex, 1, 1, 1, 1, 0, 0, true)

	a.pages.AddPage(pageName, grid, true, true)
	a.app.SetFocus(filter)
}

func (a *App) showDeleteConfirm(cp *chatPage, c deleteCandidate) {
	const pageName = "delete-confirm"

	dismiss := func() {
		a.pages.RemovePage(pageName)
		if cp != nil {
			pageID := "chat:" + cp.chatID
			a.pages.SwitchToPage(pageID)
			a.app.SetFocus(a.input)
		}
	}

	form := tview.NewForm()
	form.SetButtonsAlign(tview.AlignCenter)

	preview := c.text
	if len(preview) > 80 {
		preview = preview[:80] + "…"
	}
	form.AddTextView("Delete for everyone?", preview, 60, 3, true, false)

	form.AddButton("Delete", func() {
		peerID := cp.peerID
		msgID := c.msgID
		dismiss()
		a.doSendDelete(peerID, msgID)
	})
	form.AddButton("Cancel", dismiss)

	form.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEscape {
			dismiss()
			return nil
		}
		return ev
	})

	form.SetBorder(true).SetTitle(" delete message — for everyone ")
	grid := tview.NewGrid().
		SetColumns(0, 80, 0).
		SetRows(0, 9, 0).
		AddItem(form, 1, 1, 1, 1, 0, 0, true)

	a.pages.AddPage(pageName, grid, true, true)
	a.app.SetFocus(form)
}

func (a *App) doSendDelete(peerID, targetMsgID string) {
	if a.peerRetiredAt(peerID) != 0 {
		a.log("[red]peer retired[white] — delete not sent")
		return
	}
	a.sendRequest(ipc.FrameSendDelete, ipc.SendDeleteRequest{
		PeerID:      peerID,
		TargetMsgID: targetMsgID,
	}, func(f ipc.Frame) {
		if f.Type == ipc.FrameError {
			a.renderError(f)
			return
		}
		if f.Type != ipc.FrameDeleteSent {
			a.log("[red]/delete[white] unexpected response: %s", f.Type)
			return
		}
	})
}
