package tui

import (
	"testing"

	"haoma-frontend/internal/msg"
)

func TestPresenceColor_KnownLabels(t *testing.T) {
	cases := []struct {
		label string
		want  string
	}{
		{"available", "Green"},
		{"away", "Orange"},
		{"busy", "Red"},
		{"accepting", "Accepting"},
		{"unknown", "Gray"},
		{"unrecognised-label", "Gray (fallback)"},
	}
	for _, c := range cases {
		got := presenceColor(c.label)
		switch c.label {
		case "available":
			if got != ColorPresenceAvailable {
				t.Errorf("%s: wrong color", c.label)
			}
		case "away":
			if got != ColorPresenceAway {
				t.Errorf("%s: wrong color", c.label)
			}
		case "busy":
			if got != ColorPresenceBusy {
				t.Errorf("%s: wrong color", c.label)
			}
		case "accepting":
			if got != ColorPresenceAccepting {
				t.Errorf("%s: wrong color", c.label)
			}
		default:
			if got != ColorPresenceUnknown {
				t.Errorf("%s: wrong color (want %s)", c.label, c.want)
			}
		}
	}
}

func TestPresenceTag(t *testing.T) {
	cases := map[string]string{
		"available":           StylePresenceAvailable,
		"away":                StylePresenceAway,
		"busy":                StylePresenceBusy,
		"accepting":           StylePresenceAccepting,
		"unknown":             StylePresenceUnknown,
		"anything-else-falls": StylePresenceUnknown,
	}
	for label, want := range cases {
		if got := presenceTag(label); got != want {
			t.Errorf("presenceTag(%q) = %q, want %q", label, got, want)
		}
	}
}

func TestPresenceGlyph(t *testing.T) {
	cases := map[string]string{
		"available": GlyphPresenceOnline,
		"away":      GlyphPresenceOnline,
		"busy":      GlyphPresenceOnline,
		"accepting": GlyphPresenceAccepting,
		"unknown":   GlyphPresenceUnknown,
	}
	for label, want := range cases {
		if got := presenceGlyph(label); got != want {
			t.Errorf("presenceGlyph(%q) = %q, want %q", label, got, want)
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
