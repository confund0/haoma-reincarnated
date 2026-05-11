package tui

import (
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"haoma-frontend/internal/ipc"
)

const rotationCooldownDur = 2 * time.Minute

type rotationCooldown struct {
	Role      string
	ExpiresAt int64
}

type rotationCellInfo struct {
	Direction string
	Cooldown  bool
}

type rotationView struct {
	RotationID string
	PeerID     string
	Role       string
	State      string
}

func (a *App) cmdRotateTor() {
	front, _ := a.pages.GetFrontPage()
	if !strings.HasPrefix(front, "chat:") {
		a.log("[red]/rotate-tor[white] only works inside a chat window")
		return
	}
	peerID := a.activeChat()
	if peerID == "" {
		a.log("[red]/rotate-tor[white] no active chat page")
		return
	}
	if a.peerRetiredAt(peerID) != 0 {
		a.log("[red]peer retired[white] — can't rotate")
		return
	}

	a.winMu.Lock()
	chatID := strings.TrimPrefix(front, "chat:")
	hasCall := false
	for _, c := range a.activeCalls {
		if c.ChatID == chatID {
			hasCall = true
			break
		}
	}
	_, hasRot := a.rotations[peerID]
	a.winMu.Unlock()

	if hasCall {
		a.log("[red]/rotate-tor[white] not available during an active call with this peer")
		return
	}
	if hasRot {
		a.log("[yellow]/rotate-tor[white] rotation already in progress with %s", a.peerLabelFromID(peerID))
		return
	}

	label := a.peerLabelFromID(peerID)
	a.sendRequest(ipc.FrameRotateBegin, ipc.RotateBeginRequest{PeerID: peerID}, func(f ipc.Frame) {
		if f.Type == ipc.FrameError {
			a.renderError(f)
			return
		}
		if f.Type != ipc.FrameRotateBegun {
			a.log("[red]/rotate-tor[white] unexpected response: %s", f.Type)
			return
		}
		var p ipc.RotateBegunResponse
		if err := json.Unmarshal(f.Payload, &p); err != nil {
			a.log("[red]/rotate-tor[white] decode: %v", err)
			return
		}
		a.log("[yellow]rotating[white] address with %s (rotation_id=%s)", label, shortRotationID(p.RotationID))
	})
}

func (a *App) routeRotateRequested(f ipc.Frame) {
	var p ipc.RotateRequestedPush
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		return
	}
	if p.PeerID == "" || p.RotationID == "" {
		return
	}
	a.winMu.Lock()
	a.rotations[p.PeerID] = rotationView{
		RotationID: p.RotationID,
		PeerID:     p.PeerID,
		Role:       "responder",
		State:      "requested",
	}
	a.winMu.Unlock()
	slog.Debug("tui: rotation requested",
		slog.String("peer_id", p.PeerID),
		slog.String("rotation_id", p.RotationID),
	)
	a.app.QueueUpdateDraw(a.refreshAfterRotationChange)
}

func (a *App) routeRotateLifecycle(f ipc.Frame) {
	var p ipc.RotateLifecyclePush
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		return
	}
	if p.PeerID == "" || p.RotationID == "" {
		return
	}
	terminal := p.State == "confirmed" || p.State == "failed"
	a.winMu.Lock()
	if terminal {
		delete(a.rotations, p.PeerID)
	} else {
		a.rotations[p.PeerID] = rotationView{
			RotationID: p.RotationID,
			PeerID:     p.PeerID,
			Role:       p.Role,
			State:      p.State,
		}
	}

	if terminal && p.State == "confirmed" {
		expiry := time.Now().Add(rotationCooldownDur).Unix()
		a.rotationCooldowns[p.PeerID] = rotationCooldown{
			Role:      p.Role,
			ExpiresAt: expiry,
		}
		peerID := p.PeerID
		time.AfterFunc(rotationCooldownDur, func() {
			a.winMu.Lock()
			if cd, ok := a.rotationCooldowns[peerID]; ok && cd.ExpiresAt == expiry {
				delete(a.rotationCooldowns, peerID)
			}
			a.winMu.Unlock()
			a.app.QueueUpdateDraw(a.refreshAfterRotationChange)
		})
	}
	a.winMu.Unlock()
	slog.Debug("tui: rotation lifecycle",
		slog.String("peer_id", p.PeerID),
		slog.String("rotation_id", p.RotationID),
		slog.String("role", p.Role),
		slog.String("state", p.State),
		slog.Bool("terminal", terminal),
	)

	label := a.peerLabelFromID(p.PeerID)
	a.app.QueueUpdateDraw(func() {
		a.refreshAfterRotationChange()
		if !terminal {
			return
		}
		switch p.State {
		case "confirmed":

			switch p.Role {
			case "initiator":
				a.log("[green]rotated our address with[white] %s ([yellow]%s[white])", label, GlyphRotationOurs)
			case "responder":
				a.log("%s [green]rotated their address with us[white] ([yellow]%s[white])", label, GlyphRotationTheirs)
			default:
				a.log("[green]rotated[white] address with %s", label)
			}
		case "failed":
			reason := p.Reason
			if reason == "" {
				reason = "unknown"
			}
			a.log("[red]rotation with %s failed[white]: %s", label, reason)
		}
	})
}

func (a *App) hasRotationForPeer(peerID string) bool {
	if peerID == "" {
		return false
	}
	a.winMu.Lock()
	defer a.winMu.Unlock()
	_, ok := a.rotations[peerID]
	return ok
}

func (a *App) peerRotationCell(peerID string) rotationCellInfo {
	if peerID == "" {
		return rotationCellInfo{}
	}
	a.winMu.Lock()
	defer a.winMu.Unlock()
	if rv, ok := a.rotations[peerID]; ok {
		return rotationCellInfo{Direction: roleToDirection(rv.Role), Cooldown: false}
	}
	if cd, ok := a.rotationCooldowns[peerID]; ok && cd.ExpiresAt > time.Now().Unix() {
		return rotationCellInfo{Direction: roleToDirection(cd.Role), Cooldown: true}
	}
	return rotationCellInfo{}
}

func roleToDirection(role string) string {
	switch role {
	case "initiator":
		return "we"
	case "responder":
		return "they"
	default:
		return ""
	}
}

func (a *App) refreshAfterRotationChange() {
	a.winMu.Lock()
	peers := a.peers
	chats := a.chats
	a.winMu.Unlock()
	front, _ := a.pages.GetFrontPage()
	a.contactTable = buildContactsTable(peers, a.peerPresenceLabel, a.peerRotationCell, a.openChat)
	a.installContactsKeys(a.contactTable)
	a.pages.RemovePage("contacts")
	a.pages.AddPage("contacts", a.contactTable, true, false)
	a.chatsTable = buildChatsTable(chats, a.peerLabelForChat, a.peerPresenceLabel, a.peerRotationCell, a.openChat)
	a.installChatsKeys(a.chatsTable)
	a.pages.RemovePage("chats")
	a.pages.AddPage("chats", a.chatsTable, true, false)
	switch front {
	case "contacts":
		a.pages.SwitchToPage("contacts")
		a.app.SetFocus(a.contactTable)
	case "chats":
		a.pages.SwitchToPage("chats")
		a.app.SetFocus(a.chatsTable)
	}
	a.sysBar.SetText(a.sysBarText())
}

func shortRotationID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12] + "…"
}
