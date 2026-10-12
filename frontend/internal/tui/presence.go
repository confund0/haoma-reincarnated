package tui

import (
	"log/slog"
	"strings"

	"github.com/gdamore/tcell/v2"

	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/logid"
	"haoma-frontend/internal/msg"
)

func presenceTag(label string) string {
	switch label {
	case "available":
		return StylePresenceAvailable
	case "away":
		return StylePresenceAway
	case "busy":
		return StylePresenceBusy
	default:
		return StylePresenceUnknown
	}
}

type presenceView struct {
	accepting bool
	sending   bool
	chatty    string
}

func presenceArrowsTag(v presenceView) string {
	return arrowTag(v.accepting) + GlyphPresenceUp + arrowTag(v.sending) + GlyphPresenceDown + StyleReset
}

func arrowTag(on bool) string {
	if on {
		return StylePresenceArrowOn
	}
	return StylePresenceArrowOff
}

func presenceNameColor(chatty string) (tcell.Color, bool) {
	switch chatty {
	case msg.PresenceAvailable:
		return ColorPresenceAvailable, true
	case msg.PresenceAway:
		return ColorPresenceAway, true
	case msg.PresenceBusy:
		return ColorPresenceBusy, true
	default:
		return tcell.ColorDefault, false
	}
}

func presenceNameTag(chatty string) string {
	switch chatty {
	case msg.PresenceAvailable:
		return StylePresenceAvailable
	case msg.PresenceAway:
		return StylePresenceAway
	case msg.PresenceBusy:
		return StylePresenceBusy
	default:
		return ""
	}
}

func effectiveSelfLabel(selfPresence string) string {
	switch selfPresence {
	case msg.PresenceAway:
		return "away"
	case msg.PresenceBusy:
		return "busy"
	default:
		return "available"
	}
}

func (a *App) chatPresenceLocked(chatID string) presenceView {
	for i := range a.chats {
		if a.chats[i].ChatID == chatID {
			c := a.chats[i]
			return presenceView{accepting: c.Accepting, sending: c.Sending, chatty: c.Chatty}
		}
	}
	return presenceView{}
}

func (a *App) cmdStatus(arg string) {
	state := ""
	switch arg {
	case "":
	case msg.PresenceAvailable, msg.PresenceAway, msg.PresenceBusy:
		state = arg
	default:
		a.log("[red]/status[white] state must be available|away|busy (or empty to reset)")
		return
	}

	a.winMu.Lock()
	a.selfPresence = state
	a.winMu.Unlock()
	a.sysBar.SetText(a.sysBarText())

	a.sendRequest(ipc.FrameSetPresenceOverride, ipc.SetPresenceOverrideRequest{State: state}, nil)

	target := a.activeChat()
	a.sendRequest(ipc.FramePushPresence, ipc.PushPresenceRequest{Target: target}, nil)

	slog.Debug("tui: /status invoked",
		slog.String("state", state),
		slog.String("target", logid.Short(target)),
	)

	label := effectiveSelfLabel(state)
	if target == "" {
		a.log("/status: %s%s%s — broadcasting to all peers", presenceTag(label), label, StyleReset)
	} else {
		a.log("/status: %s%s%s → %s", presenceTag(label), label, StyleReset, shortID(target))
	}
}

func (a *App) cmdNick(arg string) {
	clean := strings.TrimSpace(arg)
	if clean == "" {
		a.winMu.Lock()
		nick := a.selfNick
		isDefault := a.selfNickIsDefault
		a.winMu.Unlock()
		if nick == "" {
			a.log("[gray]your nick is unknown (waiting for daemon welcome)[white]")
			return
		}
		if isDefault {
			a.log("your nick is '[yellow]%s[white]' (default) — set yours via [yellow]/nick <name>[white]", nick)
		} else {
			a.log("your nick is '[yellow]%s[white]'", nick)
		}
		return
	}
	a.sendRequest(ipc.FrameSetNick, ipc.SetNickRequest{Nick: clean}, func(f ipc.Frame) {

		if f.Type == ipc.FrameError {
			a.renderError(f)
		}
	})
}
