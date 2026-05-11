package emoji_test

import (
	"encoding/json"
	"strings"
	"testing"

	"haoma-frontend/internal/emoji"
)

func TestCatalogLoads(t *testing.T) {
	cats := emoji.Categories()
	if len(cats) == 0 {
		t.Fatal("no categories — embedded data.json failed to load or is empty")
	}
	all := emoji.All()
	if len(all) < 100 {
		t.Errorf("catalog suspiciously small: %d entries (expected ~1400 from siproxylin port)", len(all))
	}
}

func TestTerminalFriendly_OnlyFlaggedEntries(t *testing.T) {
	got := emoji.TerminalFriendly()
	if len(got) == 0 {
		t.Fatal("TerminalFriendly returned 0 entries; expected at least the curated tier")
	}
	for _, e := range got {
		if !e.TerminalFriendly {
			t.Errorf("entry %q surfaced in TerminalFriendly() but flag is false", e.Emoji)
		}
		if e.AsciiFallback == "" {
			t.Errorf("terminal-friendly entry %q has no ascii_fallback — required for non-Unicode rendering", e.Emoji)
		}
	}
}

func TestTerminalFriendly_IncludesUserConfirmedSet(t *testing.T) {

	want := []string{"👍", "👎", "❤️", "😁", "😘", "😍", "💩", "🖕"}
	got := emoji.TerminalFriendly()
	have := map[string]bool{}
	for _, e := range got {
		have[e.Emoji] = true
	}
	for _, w := range want {
		if !have[w] {
			t.Errorf("expected %q in terminal-friendly tier, missing", w)
		}
	}
}

func TestLookup_FindsKnownEmoji(t *testing.T) {
	e := emoji.Lookup("👍")
	if e == nil {
		t.Fatal("Lookup(👍) = nil, want entry")
	}
	if e.AsciiFallback != "+1" {
		t.Errorf("ascii_fallback = %q, want '+1'", e.AsciiFallback)
	}
}

func TestLookup_UnknownReturnsNil(t *testing.T) {
	if got := emoji.Lookup("THIS_IS_NOT_AN_EMOJI"); got != nil {
		t.Errorf("Lookup(garbage) = %+v, want nil", got)
	}
}

func TestRender_UnicodeRoundTrip(t *testing.T) {
	if !emoji.IsUnicodeCapable() {
		t.Skip("test runner locale is not UTF-8 — skipping unicode-roundtrip assertion")
	}
	if got := emoji.Render("👍"); got != "👍" {
		t.Errorf("Render(👍) = %q, want passthrough", got)
	}
}

func TestRender_AsciiFallback_LookupContract(t *testing.T) {
	cases := []struct{ glyph, want string }{
		{"👍", "+1"},
		{"👎", "-1"},
		{"❤️", "<3"},
		{"😘", ":*"},
		{"💩", ":poop:"},
	}
	for _, c := range cases {
		t.Run(c.glyph, func(t *testing.T) {
			e := emoji.Lookup(c.glyph)
			if e == nil {
				t.Fatalf("Lookup(%q) missing", c.glyph)
			}
			if e.AsciiFallback != c.want {
				t.Errorf("ascii_fallback = %q, want %q", e.AsciiFallback, c.want)
			}
		})
	}
}

func TestAll_ContainsTerminalFriendly(t *testing.T) {
	tf := emoji.TerminalFriendly()
	all := emoji.All()
	if len(all) < len(tf) {
		t.Fatalf("All() has %d entries, less than TerminalFriendly() %d", len(all), len(tf))
	}
	allSet := map[string]bool{}
	for _, e := range all {
		allSet[e.Emoji] = true
	}
	for _, e := range tf {
		if !allSet[e.Emoji] {
			t.Errorf("terminal-friendly entry %q missing from All()", e.Emoji)
		}
	}
}

func TestAll_FallbackPopulated(t *testing.T) {
	for _, e := range emoji.All() {
		if e.AsciiFallback == "" {
			t.Errorf("%q has no ascii_fallback — render path falls back to ':?:'", e.Emoji)
		}
	}
}

func TestCategories_OrderStable(t *testing.T) {
	cats := emoji.Categories()
	want := []string{
		"Smileys & Emotion", "People & Body", "Animals & Nature",
		"Food & Drink", "Travel & Places", "Activities",
		"Objects", "Symbols", "Flags",
	}
	if len(cats) != len(want) {
		t.Fatalf("got %d categories, want %d", len(cats), len(want))
	}
	for i, c := range cats {
		if c != want[i] {
			t.Errorf("category[%d] = %q, want %q", i, c, want[i])
		}
	}
}

func TestRawJSON_ParsesAsCatalog(t *testing.T) {
	var c emoji.Catalog
	if err := json.Unmarshal(emoji.RawJSON(), &c); err != nil {
		t.Fatalf("RawJSON does not parse as Catalog: %v", err)
	}
	if len(c.CategoriesOrder) == 0 {
		t.Error("RawJSON parsed but categories_order is empty")
	}
	if c.Categories == nil {
		t.Error("RawJSON parsed but categories map is nil")
	}
}

func TestKeywords_NoneEmpty(t *testing.T) {
	for _, e := range emoji.TerminalFriendly() {
		if len(e.Keywords) == 0 {
			t.Errorf("%q has no keywords — search would never find it", e.Emoji)
		}
		for _, kw := range e.Keywords {
			if strings.TrimSpace(kw) == "" {
				t.Errorf("%q has a blank keyword in %v", e.Emoji, e.Keywords)
			}
		}
	}
}
