package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rivo/tview"

	"haoma-frontend/internal/calls"
	"haoma-frontend/internal/ipc"
)

func (a *App) cmdCall() {
	front, _ := a.pages.GetFrontPage()
	if !strings.HasPrefix(front, "chat:") {
		a.log("[red]/call[white] must be used inside a chat window")
		return
	}
	chatID := strings.TrimPrefix(front, "chat:")
	peerID := a.activeChat()
	if peerID == "" {
		a.log("[red]/call[white] no active chat page")
		return
	}
	if a.peerRetiredAt(peerID) != 0 {
		a.log("[red]peer retired[white] — can't call")
		return
	}
	if a.hasRotationForPeer(peerID) {
		a.log("[red]/call[white] not available during an in-flight rotation with this peer")
		return
	}

	if existing, ok := a.findActiveCallForChat(chatID); ok {

		if existing.Status == "accepted" {
			a.openLiveCallWindow(existing)
		} else {
			a.log("[yellow]/call[white] already %s with %s (call_id=%s) — live window opens once connected",
				existing.Status, a.peerLabelFromID(existing.PeerID), shortCallID(existing.CallID))
		}
		return
	}

	a.sendRequest(ipc.FrameStartCall, ipc.StartCallRequest{ChatID: chatID}, func(f ipc.Frame) {
		if f.Type == ipc.FrameError {
			a.renderError(f)
			return
		}
		if f.Type != ipc.FrameCallStarted {
			a.log("[red]/call[white] unexpected response: %s", f.Type)
			return
		}
		var p ipc.CallStartedResponse
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			a.log("[red]/call[white] decode failed: %v", err)
			return
		}
		a.app.QueueUpdateDraw(func() {
			a.log("[yellow]calling[white] %s (call_id=%s)", a.peerLabelFromID(p.Call.PeerID), shortCallID(p.CallID))
		})
	})
}

func (a *App) cmdAnswer() {
	matches := a.findCallsByDirAndStatus("in", "ringing")
	switch len(matches) {
	case 0:
		a.log("[red]/answer[white] no incoming call to answer")
	case 1:
		c := matches[0]
		a.log("[yellow]answering[white] call from %s (call_id=%s)", a.peerLabelFromID(c.PeerID), shortCallID(c.CallID))
		a.respondCall(c.CallID, ipc.CallActionAccept, "")
	default:
		a.log("[yellow]/answer[white] %d incoming calls — pick one via the ringer modal:", len(matches))
		for _, c := range matches {
			a.log("  • from %s (call_id=%s)", a.peerLabelFromID(c.PeerID), shortCallID(c.CallID))
		}
	}
}

func (a *App) cmdDecline() {
	matches := a.findCallsByDirAndStatus("in", "ringing")
	switch len(matches) {
	case 0:
		a.log("[red]/decline[white] no incoming call to decline")
	case 1:
		c := matches[0]
		a.log("[yellow]declining[white] call from %s (call_id=%s)", a.peerLabelFromID(c.PeerID), shortCallID(c.CallID))
		a.respondCall(c.CallID, ipc.CallActionReject, "")
	default:
		a.log("[yellow]/decline[white] %d incoming calls — pick one via the ringer modal:", len(matches))
		for _, c := range matches {
			a.log("  • from %s (call_id=%s)", a.peerLabelFromID(c.PeerID), shortCallID(c.CallID))
		}
	}
}

func (a *App) cmdHangup() {
	matches := a.findActiveCallsAll()
	switch len(matches) {
	case 0:
		a.log("[red]/hangup[white] no active call")
	case 1:
		c := matches[0]
		a.log("[yellow]hanging up[white] call with %s (call_id=%s)", a.peerLabelFromID(c.PeerID), shortCallID(c.CallID))
		a.respondCall(c.CallID, ipc.CallActionEnd, "")
	default:
		a.log("[yellow]/hangup[white] %d active calls — be more specific:", len(matches))
		for _, c := range matches {
			a.log("  • %s with %s (call_id=%s)", c.Status, a.peerLabelFromID(c.PeerID), shortCallID(c.CallID))
		}
	}
}

func (a *App) upsertActiveCall(call ipc.CallEntry) {
	a.winMu.Lock()
	defer a.winMu.Unlock()
	if call.Status == "rejected" || call.Status == "ended" || call.Status == "failed" {
		delete(a.activeCalls, call.CallID)
		return
	}
	a.activeCalls[call.CallID] = call
}

func (a *App) findActiveCallForChat(chatID string) (ipc.CallEntry, bool) {
	a.winMu.Lock()
	defer a.winMu.Unlock()
	var best ipc.CallEntry
	found := false
	for _, c := range a.activeCalls {
		if c.ChatID != chatID {
			continue
		}
		if !found || c.StartedAt > best.StartedAt {
			best = c
			found = true
		}
	}
	return best, found
}

func (a *App) findCallsByDirAndStatus(dir, status string) []ipc.CallEntry {
	a.winMu.Lock()
	defer a.winMu.Unlock()
	var out []ipc.CallEntry
	for _, c := range a.activeCalls {
		if dir != "" && c.Direction != dir {
			continue
		}
		if status != "" && c.Status != status {
			continue
		}
		out = append(out, c)
	}
	return out
}

func (a *App) findActiveCallsAll() []ipc.CallEntry {
	a.winMu.Lock()
	defer a.winMu.Unlock()
	out := make([]ipc.CallEntry, 0, len(a.activeCalls))
	for _, c := range a.activeCalls {
		out = append(out, c)
	}
	return out
}

func (a *App) routeCallStateChanged(f ipc.Frame) {
	var p ipc.CallStateChangedPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		a.log("[red]call.state-changed[white] decode failed: %v", err)
		return
	}
	call := p.Call
	a.app.QueueUpdateDraw(func() {
		a.applyCallStateChange(call)
	})
}

func (a *App) applyCallStateChange(call ipc.CallEntry) {
	a.winMu.Lock()
	prior, hadPrior := a.activeCalls[call.CallID]
	a.winMu.Unlock()
	a.upsertActiveCall(call)

	label := a.peerLabelFromID(call.PeerID)

	if call.Status == "rejected" || call.Status == "ended" || call.Status == "failed" {
		a.dismissLiveCallWindow(call.CallID)
	}

	defer a.winBar.SetText(a.winBarText())

	switch call.Direction {
	case "in":
		switch call.Status {
		case "ringing":
			a.showRingerModal(call, label)
		case "accepted":
			a.dismissRingerModal(call.CallID)
			a.log("[green]connected[white] — call with %s", label)
			a.openLiveCallWindow(call)
		case "rejected":
			a.dismissRingerModal(call.CallID)
			a.log("[gray]call from %s ended (declined)[white]", label)
		case "ended":
			a.dismissRingerModal(call.CallID)

			if hadPrior && prior.Status == "ringing" {
				a.log("[red]missed call[white] from %s", label)
			} else {
				a.log("[gray]call from %s ended[white]", label)
			}
		case "failed":
			a.dismissRingerModal(call.CallID)
			a.log("[red]call from %s failed[white]: %s", label, call.FailReason)
		}
	case "out":
		switch call.Status {
		case "offered":

		case "accepted":
			a.log("[green]connected[white] — %s answered", label)
			a.openLiveCallWindow(call)
		case "rejected":
			reason := call.FailReason
			if reason == "" {
				reason = "declined"
			}
			a.log("[gray]%s declined[white]: %s", label, reason)
		case "ended":
			a.log("[gray]call with %s ended[white]", label)
		case "failed":

			if call.FailReason == calls.FailReasonNoAnswer {
				a.log("[gray]no answer[white] from %s", label)
			} else {
				a.log("[red]outgoing call to %s failed[white]: %s", label, call.FailReason)
			}
		}
	}
}

func ringerPageName(callID string) string {
	return "call-ringer:" + callID
}

func (a *App) showRingerModal(call ipc.CallEntry, peerLabel string) {
	page := ringerPageName(call.CallID)
	text := fmt.Sprintf("Incoming call\n\nFrom: %s\nCall ID: %s\nModalities: %s",
		peerLabel,
		shortCallID(call.CallID),
		strings.Join(call.Modalities, ", "),
	)
	modal := tview.NewModal().
		SetText(text).
		AddButtons([]string{"Answer", "Decline", "Silence"})
	modal.SetDoneFunc(func(_ int, label string) {
		a.pages.RemovePage(page)
		switch label {
		case "Answer":
			a.respondCall(call.CallID, ipc.CallActionAccept, "")
		case "Decline":
			a.respondCall(call.CallID, ipc.CallActionReject, "")
		case "Silence":

			a.log("[gray]ringer silenced (no response sent)[white]")
		}
		a.app.SetFocus(a.input)
	})
	a.pages.AddPage(page, modal, true, true)
	a.app.SetFocus(modal)
}

func (a *App) dismissRingerModal(callID string) {
	a.pages.RemovePage(ringerPageName(callID))
}

func (a *App) respondCall(callID, action, reason string) {
	a.sendRequest(ipc.FrameRespondCall, ipc.RespondCallRequest{
		CallID: callID,
		Action: action,
		Reason: reason,
	}, func(f ipc.Frame) {
		if f.Type == ipc.FrameError {
			a.renderError(f)
			return
		}
		if f.Type != ipc.FrameCallResponded {
			a.log("[red]respond_call[white] unexpected response: %s", f.Type)
			return
		}

	})
}

func (a *App) peerLabelFromID(peerID string) string {
	if peerID == "" {
		return "<unknown>"
	}
	a.winMu.Lock()
	label := a.peerNickLocked(peerID)
	a.winMu.Unlock()
	if label == "" {
		return shortID(peerID)
	}
	return label
}

func shortCallID(callID string) string {
	if len(callID) <= 8 {
		return callID
	}
	return callID[:8] + "…"
}
