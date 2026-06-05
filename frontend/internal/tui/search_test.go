package tui

import (
	"strings"
	"testing"
)

func TestHighlightAllInsensitive_MultipleMatches(t *testing.T) {
	got := highlightAllInsensitive("Hello, HELLO, hello!", "hello")
	wantHits := strings.Count(got, StyleSearchHighlight)
	if wantHits != 3 {
		t.Errorf("highlight open count = %d, want 3 (got %q)", wantHits, got)
	}
	if c := strings.Count(got, StyleSearchHighlightOff); c != 3 {
		t.Errorf("highlight close count = %d, want 3", c)
	}
}

func TestHighlightAllInsensitive_PreservesCase(t *testing.T) {

	got := highlightAllInsensitive("Hello WORLD", "wor")
	if !strings.Contains(got, "WOR") {
		t.Errorf("original casing not preserved: %q", got)
	}
	if strings.Contains(got, "wor"+StyleSearchHighlightOff) {
		t.Errorf("inserted lowercased substring: %q", got)
	}
}

func TestHighlightAllInsensitive_EmptyInputs(t *testing.T) {
	if got := highlightAllInsensitive("anything", ""); got != "anything" {
		t.Errorf("empty query mutated string: %q", got)
	}
	if got := highlightAllInsensitive("", "q"); got != "" {
		t.Errorf("empty body mutated: %q", got)
	}
}

func TestHighlightAllInsensitive_NoMatch(t *testing.T) {
	if got := highlightAllInsensitive("foo bar baz", "xyz"); got != "foo bar baz" {
		t.Errorf("no-match path mutated string: %q", got)
	}
}

func TestHighlightAllInsensitive_AdjacentMatches(t *testing.T) {
	got := highlightAllInsensitive("aaaa", "aa")

	if c := strings.Count(got, StyleSearchHighlight); c != 2 {
		t.Errorf("adjacent matches: open count = %d, want 2 (got %q)", c, got)
	}
}
