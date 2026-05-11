package tui

import (
	"strings"
	"testing"
)

func TestFormatFingerprint_Empty(t *testing.T) {
	got := formatFingerprint("")
	if !strings.Contains(got, "no session yet") {
		t.Errorf("empty fingerprint should hint at missing session, got %q", got)
	}
}

func TestFormatFingerprint_SingleLine11x6(t *testing.T) {

	const fp = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef00"
	got := formatFingerprint(fp)

	if strings.Contains(got, "\n") {
		t.Fatalf("expected single-line output, got %q", got)
	}

	groups := strings.Fields(got)
	if len(groups) != 11 {
		t.Errorf("got %d groups, want 11 (66 hex / 6 = 11)", len(groups))
	}
	for i, g := range groups {
		if len(g) != 6 {
			t.Errorf("group %d %q: not 6 chars (66 = 11×6, no orphans)", i, g)
		}
	}

	stripped := strings.ReplaceAll(got, " ", "")
	if stripped != fp {
		t.Errorf("round-trip = %q, want %q", stripped, fp)
	}
}
