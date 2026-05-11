package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"haoma-frontend/internal/emoji"
	"haoma-frontend/internal/ipc"
)

type reactCandidate struct {
	msgID     string
	text      string
	displayTs int64
	direction string
}

func (a *App) cmdReact() {
	front, _ := a.pages.GetFrontPage()
	if !strings.HasPrefix(front, "chat:") {
		a.log("[red]/react[white] must be used inside a chat window")
		return
	}
	chatID := strings.TrimPrefix(front, "chat:")
	a.winMu.Lock()
	cp := a.chatPages[chatID]
	a.winMu.Unlock()
	if cp == nil {
		a.log("[red]/react[white] no active chat page")
		return
	}
	cands := collectReactable(cp)
	if len(cands) == 0 {
		a.log("[yellow]/react[white] no reactable messages in this chat")
		return
	}
	a.showReactPicker(cp, cands)
}

func collectReactable(cp *chatPage) []reactCandidate {
	var out []reactCandidate
	for _, e := range cp.entries {
		var ev rawEvent
		if err := json.Unmarshal(e.evJSON, &ev); err != nil {
			continue
		}
		if ev.Kind != "text" {
			continue
		}
		if ev.DecryptStatus == "failed" {
			continue
		}
		if ev.MsgID == "" {
			continue
		}
		if ev.DeletedAt > 0 {
			continue
		}
		var body struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(ev.Body, &body)
		out = append(out, reactCandidate{
			msgID:     ev.MsgID,
			text:      body.Text,
			displayTs: ev.DisplayTs,
			direction: ev.Direction,
		})
	}
	return out
}

func (a *App) showReactPicker(cp *chatPage, cands []reactCandidate) {
	const pageName = "react-picker"

	table := tview.NewTable().
		SetBorders(false).
		SetSelectable(true, false).
		SetFixed(1, 0)
	headerStyle := tcell.StyleDefault.Bold(true)
	table.SetCell(0, 0, tview.NewTableCell("Time").SetStyle(headerStyle).SetExpansion(1))
	table.SetCell(0, 1, tview.NewTableCell("Dir").SetStyle(headerStyle).SetExpansion(1))
	table.SetCell(0, 2, tview.NewTableCell("Message").SetStyle(headerStyle).SetExpansion(6))

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

			arrow := "<-"
			if c.direction == "in" {
				arrow = "->"
			}
			table.SetCell(r+1, 0, tview.NewTableCell(time.Unix(c.displayTs, 0).Format("15:04")).SetExpansion(1))
			table.SetCell(r+1, 1, tview.NewTableCell(arrow).SetExpansion(1))
			preview := c.text
			if len(preview) > 80 {
				preview = preview[:80] + "…"
			}
			table.SetCell(r+1, 2, tview.NewTableCell(preview).SetExpansion(6))
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
		a.showEmojiPicker(cp, c)
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
	flex.SetBorder(true).SetTitle(fmt.Sprintf(" react — pick a target (%d candidate(s)) ", len(cands)))

	grid := tview.NewGrid().
		SetColumns(0, 80, 0).
		SetRows(0, 20, 0).
		AddItem(flex, 1, 1, 1, 1, 0, 0, true)

	a.pages.AddPage(pageName, grid, true, true)
	a.app.SetFocus(filter)
}

func (a *App) showEmojiPicker(cp *chatPage, c reactCandidate) {
	const pageName = "react-emoji"

	dismiss := func() {
		a.pages.RemovePage(pageName)
		if cp != nil {
			pageID := "chat:" + cp.chatID
			a.pages.SwitchToPage(pageID)
			a.app.SetFocus(a.input)
		}
	}

	send := func(glyph string) {
		peerID := cp.peerID
		dismiss()
		a.doSendReaction(peerID, c.msgID, glyph)
	}

	entries := emoji.TerminalFriendly()

	grid := tview.NewTable().
		SetBorders(false).
		SetSelectable(true, true)
	const cols = 6
	for i, e := range entries {
		row := i / cols
		col := i % cols

		label := emoji.Render(e.Emoji)
		cell := tview.NewTableCell(" " + label + " ").
			SetExpansion(1).
			SetAlign(tview.AlignCenter).
			SetReference(e.Emoji)
		grid.SetCell(row, col, cell)
	}
	grid.SetSelectedFunc(func(row, col int) {
		cell := grid.GetCell(row, col)
		if cell == nil || cell.Reference == nil {
			return
		}
		glyph, _ := cell.Reference.(string)
		if glyph == "" {
			return
		}
		send(glyph)
	})
	grid.Select(0, 0)

	input := tview.NewInputField().
		SetLabel("custom ").
		SetFieldWidth(20)
	input.SetDoneFunc(func(key tcell.Key) {
		if key != tcell.KeyEnter {
			return
		}
		send(input.GetText())
	})

	hint := tview.NewTextView().SetDynamicColors(true).SetText(
		StyleBreadcrumbBody + " ↑↓←→ to choose · Enter to send · custom field accepts any string · empty = remove prior reaction · Esc cancels" + StyleReset,
	)

	flex := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(grid, 0, 1, true).
		AddItem(input, 1, 0, false).
		AddItem(hint, 1, 0, false)
	flex.SetBorder(true).SetTitle(fmt.Sprintf(" react — pick an emoji for %s ", time.Unix(c.displayTs, 0).Format("15:04")))

	flex.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Key() {
		case tcell.KeyEscape:
			dismiss()
			return nil
		case tcell.KeyTab:

			if a.app.GetFocus() == grid {
				a.app.SetFocus(input)
			} else {
				a.app.SetFocus(grid)
			}
			return nil
		}
		return ev
	})

	wrap := tview.NewGrid().
		SetColumns(0, 60, 0).
		SetRows(0, 12, 0).
		AddItem(flex, 1, 1, 1, 1, 0, 0, true)

	a.pages.AddPage(pageName, wrap, true, true)
	a.app.SetFocus(grid)
}

func (a *App) doSendReaction(peerID, targetMsgID, emojiStr string) {
	if a.peerRetiredAt(peerID) != 0 {
		a.log("[red]peer retired[white] — reaction not sent")
		return
	}
	a.sendRequest(ipc.FrameSendReaction, ipc.SendReactionRequest{
		PeerID:      peerID,
		TargetMsgID: targetMsgID,
		Emoji:       emojiStr,
	}, func(f ipc.Frame) {
		if f.Type == ipc.FrameError {
			a.renderError(f)
			return
		}
		if f.Type != ipc.FrameReactionSent {
			a.log("[red]/react[white] unexpected response: %s", f.Type)
			return
		}
	})
}
