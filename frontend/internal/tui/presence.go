package tui

import (
	"log/slog"
	"strings"

	"github.com/gdamore/tcell/v2"

	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/msg"
)

func presenceColor(label string) tcell.Color {
	switch label {
	case "available":
		return ColorPresenceAvailable
	case "away":
		return ColorPresenceAway
	case "busy":
		return ColorPresenceBusy
	case "accepting":
		return ColorPresenceAccepting
	default:
		return ColorPresenceUnknown
	}
}

func presenceTag(label string) string {
	switch label {
	case "available":
		return StylePresenceAvailable
	case "away":
		return StylePresenceAway
	case "busy":
		return StylePresenceBusy
	case "accepting":
		return StylePresenceAccepting
	default:
		return StylePresenceUnknown
	}
}

func presenceGlyph(label string) string {
	switch label {
	case "accepting":
		return GlyphPresenceAccepting
	case "unknown":
		return GlyphPresenceUnknown
	default:
		return GlyphPresenceOnline
	}
}

func presenceCellText(label string) string {
	if label == "" {
		label = "unknown"
	}
	return presenceGlyph(label)
}

func presenceCellStyle(label string) tcell.Style {
	if label == "" {
		label = "unknown"
	}
	return tcell.StyleDefault.Foreground(presenceColor(label))
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

func (a *App) peerEffectiveLabel(peerID string) string {
	if v := a.peerPresence[peerID]; v != "" {
		return v
	}
	return "unknown"
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
		slog.String("target", target),
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
