package main

import (
	"strings"
	"testing"

	"haoma-frontend/internal/logid"
)

func TestShortenedAndFullFormMatch(t *testing.T) {
	full := "1b3f1f840846bcef88679e20dfce7bd2"
	written := logid.Short(full)
	if written == full {
		t.Fatalf("the helper left %q unchanged; this test needs a value it shortens", full)
	}
	cases := []struct {
		name string
		a, b string
	}{
		{"full against what the log holds", full, written},
		{"what the log holds against full", written, full},
		{"full against full", full, full},
		{"log form against log form", written, written},
		{"prefix typed without the marker", strings.TrimSuffix(written, shortMark), full},
	}
	for _, c := range cases {
		if !sameID(c.a, c.b) {
			t.Errorf("%s: sameID(%q, %q) = false, want true", c.name, c.a, c.b)
		}
	}
}

func TestDifferentIdentifiersDoNotMatch(t *testing.T) {
	a := "1b3f1f840846bcef88679e20dfce7bd2"
	b := "7ae43d8911a28784ec71ea9697d1e1ff"
	if sameID(a, b) {
		t.Errorf("sameID(%q, %q) = true, want false", a, b)
	}
	if sameID(logid.Short(a), logid.Short(b)) {
		t.Error("two different identifiers matched in their shortened form")
	}
	if sameID("", a) || sameID(a, "") {
		t.Error("an empty value matched an identifier")
	}
}

func TestShortRuleComesFromTheHelper(t *testing.T) {
	sample := "0123456789abcdef0123456789abcdef"
	written := logid.Short(sample)
	if !strings.HasSuffix(written, shortMark) {
		t.Fatalf("the helper wrote %q, which does not end in the marker %q", written, shortMark)
	}
	if got := len(strings.TrimSuffix(written, shortMark)); got != shortLen {
		t.Fatalf("the helper kept %d characters, this file expects %d", got, shortLen)
	}
	if canonicalID(sample) != sample[:shortLen] {
		t.Fatalf("canonicalID(%q) = %q, want %q", sample, canonicalID(sample), sample[:shortLen])
	}
}

func TestIDSetKeepsTheLongestForm(t *testing.T) {
	full := "1b3f1f840846bcef88679e20dfce7bd2"
	s := newIDSet()
	s.add(logid.Short(full))
	s.add(full)
	if got := s.display(logid.Short(full)); got != full {
		t.Errorf("display = %q, want the full form %q", got, full)
	}
	if !s.has(full) || !s.has(logid.Short(full)) {
		t.Error("the set does not hold both written forms of the same identifier")
	}
	if s.len() != 1 {
		t.Errorf("the set holds %d identifiers, want 1", s.len())
	}
}
