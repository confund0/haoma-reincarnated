package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"haoma-frontend/internal/ipc"
)

func (a *App) cmdSearch(query string) {
	query = strings.TrimSpace(query)
	if query == "" {
		a.log("[yellow]/search <text>[white] — type a substring to search the current chat")
		return
	}
	front, _ := a.pages.GetFrontPage()
	if !strings.HasPrefix(front, "chat:") {
		a.log("[red]/search[white] only works inside a chat window")
		return
	}
	chatID := strings.TrimPrefix(front, "chat:")
	a.winMu.Lock()
	cp := a.chatPages[chatID]
	a.winMu.Unlock()
	if cp == nil {
		a.log("[red]/search[white] no active chat page")
		return
	}

	a.sendRequest(ipc.FrameChatSearch, ipc.ChatSearchRequest{
		ChatID: chatID,
		Query:  query,
	}, func(f ipc.Frame) {
		a.handleChatSearched(chatID, query, f)
	})
}

func (a *App) handleChatSearched(chatID, query string, f ipc.Frame) {
	if f.Type == ipc.FrameError {
		var ep ipc.ErrorPayload
		_ = json.Unmarshal(f.Payload, &ep)
		a.app.QueueUpdateDraw(func() {
			a.log("[red]/search failed[white] %s: %s", ep.Code, ep.Message)
		})
		return
	}
	var resp ipc.ChatSearchResponse
	if err := json.Unmarshal(f.Payload, &resp); err != nil {
		a.app.QueueUpdateDraw(func() {
			a.log("[red]/search[white] decode response: %v", err)
		})
		return
	}
	a.app.QueueUpdateDraw(func() {
		if len(resp.Matches) == 0 {
			a.log("[yellow]/search[white] no matches for %q", query)
			return
		}
		a.enterSearchMode(chatID, query, resp.Matches, resp.Truncated)
	})
}

func (a *App) enterSearchMode(chatID, query string, matches []ipc.ChatSearchMatch, truncated bool) {
	a.winMu.Lock()
	a.searchActive = true
	a.searchChatID = chatID
	a.searchQuery = query
	a.searchMatches = matches
	a.searchIdx = 0
	a.searchTruncated = truncated
	a.searchDraft = a.input.GetText()
	a.winMu.Unlock()

	a.input.SetAcceptanceFunc(func(string, rune) bool { return false })
	a.input.SetText("")
	a.input.SetLabel(a.searchPromptLabel())

	if truncated {
		a.log("[yellow]/search[white] showing newest %d (more matches exist — refine the query)", len(matches))
	}
	a.scrollToCurrentMatch()
}

func (a *App) exitSearchMode() {
	a.winMu.Lock()
	if !a.searchActive {
		a.winMu.Unlock()
		return
	}
	draft := a.searchDraft
	chatID := a.searchChatID
	a.searchActive = false
	a.searchChatID = ""
	a.searchQuery = ""
	a.searchMatches = nil
	a.searchIdx = 0
	a.searchTruncated = false
	a.searchDraft = ""
	cp := a.chatPages[chatID]
	a.winMu.Unlock()

	a.input.SetAcceptanceFunc(nil)
	a.input.SetLabel("> ")
	a.input.SetText(draft)

	if cp != nil {
		cp.clearSearchHighlight()
		cp.rebuildNoScroll()
	}
}

func (a *App) stepSearchMatch(delta int) {
	a.winMu.Lock()
	if !a.searchActive || len(a.searchMatches) == 0 {
		a.winMu.Unlock()
		return
	}
	n := len(a.searchMatches)
	a.searchIdx = ((a.searchIdx+delta)%n + n) % n
	a.winMu.Unlock()

	a.input.SetLabel(a.searchPromptLabel())
	a.scrollToCurrentMatch()
}

func (a *App) scrollToCurrentMatch() {
	a.winMu.Lock()
	if !a.searchActive || len(a.searchMatches) == 0 {
		a.winMu.Unlock()
		return
	}
	cp := a.chatPages[a.searchChatID]
	match := a.searchMatches[a.searchIdx]
	query := a.searchQuery
	a.winMu.Unlock()

	if cp == nil {
		return
	}
	cp.setSearchHighlight(query, match.MsgID)
	cp.rebuildNoScroll()
	cp.scrollToMsgID(match.MsgID)
}

func (a *App) searchPromptLabel() string {
	a.winMu.Lock()
	q := a.searchQuery
	n := len(a.searchMatches)
	idx := a.searchIdx + 1
	truncated := a.searchTruncated
	a.winMu.Unlock()

	suffix := ""
	if truncated {
		suffix = "+"
	}
	return fmt.Sprintf("searching: %s  %d/%d%s  ↑/↓ navigate  esc exit  ", q, idx, n, suffix)
}
