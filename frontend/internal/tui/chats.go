package tui

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"haoma-frontend/internal/ipc"
)

func buildChatsTable(chats []ipc.ChatEntry, peerLabel func(peerID string) string, presenceLabel func(peerID string) string, rotationCell func(peerID string) rotationCellInfo, onOpen func(peerID string)) *tview.Table {
	t := tview.NewTable().
		SetBorders(false).
		SetSelectable(true, false).
		SetFixed(1, 0)
	t.SetBorder(true).SetTitle(" chats ")

	style := tcell.StyleDefault.Bold(true)
	t.SetCell(0, 0, tview.NewTableCell("P").SetStyle(style).SetMaxWidth(1))
	t.SetCell(0, 1, tview.NewTableCell("R").SetStyle(style).SetMaxWidth(1))
	t.SetCell(0, 2, tview.NewTableCell("Label").SetStyle(style).SetExpansion(3))
	t.SetCell(0, 3, tview.NewTableCell("Kind").SetStyle(style).SetExpansion(1))
	t.SetCell(0, 4, tview.NewTableCell("Disappears").SetStyle(style).SetExpansion(1))
	t.SetCell(0, 5, tview.NewTableCell("Unread").SetStyle(style).SetExpansion(1))
	t.SetCell(0, 6, tview.NewTableCell("Last activity").SetStyle(style).SetExpansion(2))

	now := time.Now()
	dimmed := tcell.StyleDefault.Foreground(ColorRetiredRow)
	unreadStyle := tcell.StyleDefault.Foreground(ColorWinBarUnread)
	for i, c := range chats {
		label := renderChatLabel(c, peerLabel)
		kind := string(c.Kind)
		ttl := retentionLabel(c.RetentionTTL)
		hasUnread := c.UnreadCount > 0
		unread := strconv.FormatUint(uint64(c.UnreadCount), 10)
		if hasUnread {
			label = label + " " + GlyphUnread
		}
		last := relativeTime(now, c.LastActivityAt)

		presenceText := ""
		var presenceStyleVal tcell.Style
		hasPresence := false
		if c.Kind == ipc.ChatKindDirect && c.PeerID != "" {
			pl := "unknown"
			if presenceLabel != nil {
				if v := presenceLabel(c.PeerID); v != "" {
					pl = v
				}
			}
			presenceText = presenceCellText(pl)
			presenceStyleVal = presenceCellStyle(pl)
			hasPresence = true
		}
		row := i + 1
		presenceCell := tview.NewTableCell(presenceText).SetMaxWidth(1)
		labelCell := tview.NewTableCell(label).SetExpansion(3)
		kindCell := tview.NewTableCell(kind).SetExpansion(1)
		ttlCell := tview.NewTableCell(ttl).SetExpansion(1)
		unreadCell := tview.NewTableCell(unread).SetExpansion(1)
		lastCell := tview.NewTableCell(last).SetExpansion(2)
		if hasUnread {

			labelCell.SetStyle(unreadStyle)
			kindCell.SetStyle(unreadStyle)
			ttlCell.SetStyle(unreadStyle)
			unreadCell.SetStyle(unreadStyle)
			lastCell.SetStyle(unreadStyle)
			if hasPresence {
				presenceCell.SetStyle(presenceStyleVal)
			}
		} else {
			if hasPresence {
				presenceCell.SetStyle(presenceStyleVal)
			}

			unreadCell.SetStyle(dimmed)
			if c.LastActivityAt == 0 {
				lastCell.SetStyle(dimmed)
			}
		}

		rotText := ""
		var rotInfo rotationCellInfo
		if c.Kind == ipc.ChatKindDirect && c.PeerID != "" && rotationCell != nil {
			rotInfo = rotationCell(c.PeerID)
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
		}

		t.SetCell(row, 0, presenceCell)
		t.SetCell(row, 1, rotTableCell)
		t.SetCell(row, 2, labelCell)
		t.SetCell(row, 3, kindCell)
		t.SetCell(row, 4, ttlCell)
		t.SetCell(row, 5, unreadCell)
		t.SetCell(row, 6, lastCell)
	}

	t.SetSelectedFunc(func(row, _ int) {
		if row == 0 || row-1 >= len(chats) {
			return
		}

		c := chats[row-1]
		if c.Kind == ipc.ChatKindDirect && c.PeerID != "" {
			onOpen(c.PeerID)
		}
	})

	return t
}

func renderChatLabel(c ipc.ChatEntry, peerLabel func(peerID string) string) string {
	if c.Label != "" {
		return c.Label
	}
	switch c.Kind {
	case ipc.ChatKindGroup:
		if c.GroupAlias != "" {
			return c.GroupAlias
		}
		if c.GroupName != "" {
			return c.GroupName
		}
		return shortID(c.ChatID)
	case ipc.ChatKindDirect:
		if peerLabel != nil {
			if lbl := peerLabel(c.PeerID); lbl != "" {
				return lbl
			}
		}
		return shortID(c.PeerID)
	default:
		return shortID(c.ChatID)
	}
}

func (a *App) installChatsKeys(tbl *tview.Table) {
	tbl.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Key() {
		case tcell.KeyEscape:
			a.escPending = true
			return nil
		case tcell.KeyRune:
			if ev.Rune() == 'e' {
				row, _ := tbl.GetSelection()
				if row <= 0 {
					return nil
				}
				a.winMu.Lock()
				if row-1 >= len(a.chats) {
					a.winMu.Unlock()
					return nil
				}
				c := a.chats[row-1]
				a.winMu.Unlock()
				a.showChatModal(c)
				return nil
			}
		}
		return ev
	})
}

func (a *App) showChatModal(c ipc.ChatEntry) {
	const pageName = "chat-modal"

	dismiss := func() {
		a.pages.RemovePage(pageName)
		a.switchTo("chats")
	}

	top := tview.NewForm()
	top.SetButtonsAlign(tview.AlignCenter)

	if c.Kind == ipc.ChatKindGroup {
		top.AddInputField("Group name", c.GroupName, 30, nil, nil)
		top.AddInputField("Group alias", c.GroupAlias, 30, nil, nil)
	}

	retentionDropdown := tview.NewDropDown().
		SetLabel("Disappearing messages").
		SetOptions(retentionLabels(), func(string, int) {})
	top.AddFormItem(retentionDropdown)
	retentionDropdown.SetCurrentOption(retentionOptionIndex(c.RetentionTTL))

	sendReceipts := !c.DisableReadReceipts
	receiptsCheckbox := newBracketCheckbox("Send read receipts", sendReceipts, func(checked bool) { sendReceipts = checked })
	top.AddFormItem(receiptsCheckbox)

	muted := c.NotificationsMuted
	muteCheckbox := newBracketCheckbox("Mute notifications", muted, func(checked bool) { muted = checked })
	top.AddFormItem(muteCheckbox)

	initialRetention := c.RetentionTTL
	initialReceiptsDisabled := c.DisableReadReceipts
	initialMuted := c.NotificationsMuted
	save := func() {
		idx, _ := retentionDropdown.GetCurrentOption()
		if idx < 0 || idx >= len(retentionLevels) {
			return
		}
		wantTTL := retentionLevels[idx].seconds
		wantReceiptsDisabled := !sendReceipts
		wantMuted := muted
		if wantTTL == initialRetention && wantReceiptsDisabled == initialReceiptsDisabled && wantMuted == initialMuted {
			return
		}
		a.sendRequest(ipc.FrameSetChatSettings, ipc.SetChatSettingsRequest{
			ChatID:              c.ChatID,
			RetentionTTL:        wantTTL,
			DisableReadReceipts: wantReceiptsDisabled,
			NotificationsMuted:  wantMuted,
		}, func(f ipc.Frame) {
			if f.Type == ipc.FrameError {
				a.renderError(f)
				return
			}
			label := renderChatLabel(c, a.peerLabelForChat)
			if wantTTL != initialRetention {
				a.log("[green]retention[white] %s → %s", label, retentionLevels[idx].label)
			}
			if wantReceiptsDisabled != initialReceiptsDisabled {
				state := "off"
				if !wantReceiptsDisabled {
					state = "on"
				}
				a.log("[green]read receipts[white] %s → %s", label, state)
			}
			if wantMuted != initialMuted {
				state := "off"
				if wantMuted {
					state = "on"
				}
				a.log("[green]mute[white] %s → %s", label, state)
			}
		})
	}
	top.AddButton("Save", func() {
		save()
		dismiss()
	})
	top.AddButton("Cancel", dismiss)

	bottom := tview.NewForm()
	bottom.SetButtonsAlign(tview.AlignCenter)
	var risksAcked bool
	bottom.AddFormItem(newBracketCheckbox("I understand risks", false, func(checked bool) { risksAcked = checked }))

	fireChatAction := func(action ipc.ChatAction, label string) {

		a.sendRequest(ipc.FrameChatAction, ipc.ChatActionRequest{
			ChatID: c.ChatID,
			Action: action,
		}, func(f ipc.Frame) {
			if f.Type == ipc.FrameError {
				a.renderError(f)
				return
			}
			if f.Type != ipc.FrameChatActionApplied {
				a.log("[red]chat action[white] unexpected response: %s", f.Type)
				return
			}
			var p ipc.ChatActionAppliedResponse
			if err := json.Unmarshal(f.Payload, &p); err != nil {
				a.log("[red]chat action[white] decode: %v", err)
				return
			}
			name := renderChatLabel(p.Chat, a.peerLabelForChat)
			if p.DeletedCount > 0 {
				a.log("[green]%s[white] %s (%d events purged)", label, name, p.DeletedCount)
			} else {
				a.log("[green]%s[white] %s", label, name)
			}
		})
	}

	bottom.AddButton("Clear chat", func() {
		if !risksAcked {
			a.log("[yellow]check the risks box first[white]")
			return
		}
		dismiss()
		fireChatAction(ipc.ChatActionClear, "cleared")
	})
	bottom.AddButton("Delete chat", func() {
		if !risksAcked {
			a.log("[yellow]check the risks box first[white]")
			return
		}
		dismiss()
		fireChatAction(ipc.ChatActionDelete, "deleted")
	})

	title := "edit conversation — " + renderChatLabel(c, a.peerLabelForChat)

	topRows := 7
	if c.Kind == ipc.ChatKindGroup {
		topRows = 11
	}
	bottomRows := 5
	modal := a.twoSectionModal(title, top, bottom, topRows, bottomRows, dismiss)

	grid := tview.NewGrid().
		SetColumns(0, 60, 0).
		SetRows(0, topRows+1+bottomRows+2, 0).
		AddItem(modal, 1, 1, 1, 1, 0, 0, true)

	a.pages.AddPage(pageName, grid, true, true)
	a.app.SetFocus(top)
}

func (a *App) peerLabelForChat(peerID string) string {
	if peerID == "" {
		return ""
	}
	a.winMu.Lock()
	defer a.winMu.Unlock()
	if p := a.peerByIDLocked(peerID); p != nil {
		return p.Label
	}
	return ""
}

func (a *App) peerPresenceLabel(peerID string) string {
	if peerID == "" {
		return "unknown"
	}
	a.winMu.Lock()
	defer a.winMu.Unlock()
	if v := a.peerPresence[peerID]; v != "" {
		return v
	}
	return "unknown"
}

func (a *App) handleChatsListed(f ipc.Frame) {
	if f.Type == ipc.FrameError {
		a.renderError(f)
		return
	}
	if f.Type != ipc.FrameChatsListed {
		return
	}
	var p ipc.ChatsListedResponse
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		return
	}
	a.winMu.Lock()
	a.chats = p.Chats
	a.winMu.Unlock()

	a.app.QueueUpdateDraw(func() {
		a.winMu.Lock()
		chats := a.chats
		a.winMu.Unlock()
		front, _ := a.pages.GetFrontPage()
		a.chatsTable = buildChatsTable(chats, a.peerLabelForChat, a.peerPresenceLabel, a.peerRotationCell, a.openChat)
		a.installChatsKeys(a.chatsTable)
		a.pages.RemovePage("chats")
		a.pages.AddPage("chats", a.chatsTable, true, false)
		if front == "chats" {
			a.pages.SwitchToPage("chats")
			a.app.SetFocus(a.chatsTable)
		}
	})
}
