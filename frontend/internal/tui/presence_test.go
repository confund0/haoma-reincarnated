package tui

import (
	"testing"

	"github.com/gdamore/tcell/v2"

	"haoma-frontend/internal/msg"
)

func TestPresenceNameColor(t *testing.T) {
	cases := []struct {
		state string
		want  tcell.Color
		ok    bool
	}{
		{"available", ColorPresenceAvailable, true},
		{"away", ColorPresenceAway, true},
		{"busy", ColorPresenceBusy, true},
		{"", tcell.ColorDefault, false},
		{"accepting", tcell.ColorDefault, false},
		{"a-state-this-build-does-not-know", tcell.ColorDefault, false},
	}
	for _, c := range cases {
		got, ok := presenceNameColor(c.state)
		if got != c.want || ok != c.ok {
			t.Errorf("presenceNameColor(%q) = %v, %v; want %v, %v", c.state, got, ok, c.want, c.ok)
		}
	}
}

func TestPresenceNameTag(t *testing.T) {
	cases := map[string]string{
		"available": StylePresenceAvailable,
		"away":      StylePresenceAway,
		"busy":      StylePresenceBusy,
		"":          "",
		"accepting": "",
		"later":     "",
	}
	for state, want := range cases {
		if got := presenceNameTag(state); got != want {
			t.Errorf("presenceNameTag(%q) = %q, want %q", state, got, want)
		}
	}
}

func TestPresenceArrowsTag(t *testing.T) {
	on, off := StylePresenceArrowOn, StylePresenceArrowOff
	cases := []struct {
		v    presenceView
		want string
	}{
		{presenceView{}, off + "↑" + off + "↓" + StyleReset},
		{presenceView{accepting: true}, on + "↑" + off + "↓" + StyleReset},
		{presenceView{sending: true}, off + "↑" + on + "↓" + StyleReset},
		{presenceView{accepting: true, sending: true, chatty: "busy"}, on + "↑" + on + "↓" + StyleReset},
	}
	for _, c := range cases {
		if got := presenceArrowsTag(c.v); got != c.want {
			t.Errorf("presenceArrowsTag(%+v) = %q, want %q", c.v, got, c.want)
		}
	}
}

func TestPresenceTag(t *testing.T) {
	cases := map[string]string{
		"available":           StylePresenceAvailable,
		"away":                StylePresenceAway,
		"busy":                StylePresenceBusy,
		"unknown":             StylePresenceUnknown,
		"anything-else-falls": StylePresenceUnknown,
	}
	for label, want := range cases {
		if got := presenceTag(label); got != want {
			t.Errorf("presenceTag(%q) = %q, want %q", label, got, want)
		}
	}
}

func TestEffectiveSelfLabel(t *testing.T) {
	cases := map[string]string{
		"":                    "available",
		msg.PresenceAvailable: "available",
		msg.PresenceAway:      "away",
		msg.PresenceBusy:      "busy",
		"unknown-state-falls": "available",
	}
	for in, want := range cases {
		if got := effectiveSelfLabel(in); got != want {
			t.Errorf("effectiveSelfLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
