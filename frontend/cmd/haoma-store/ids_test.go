package main

import (
	"strings"
	"testing"

	"haoma-frontend/internal/logid"
)

func TestCanonicalID_ReadsTheRuleOutOfTheHelper(t *testing.T) {
	const sample = "a1b2c3d4e5f60718293a4b5c6d7e8f90"

	written := logid.Short(sample)
	if written == sample {
		t.Fatal("the helper left the sample unchanged, so it is too short to test with")
	}
	if shortMark == "" {
		t.Fatal("no marker was found at the end of a shortened value")
	}
	if got := len(strings.TrimSuffix(written, shortMark)); got != shortLen {
		t.Fatalf("the helper kept %d characters, this file expects %d", got, shortLen)
	}
	if canonicalID(sample) != sample[:shortLen] {
		t.Fatalf("canonicalID(%q) = %q, want %q", sample, canonicalID(sample), sample[:shortLen])
	}
}

func TestSameID_FullAndShortenedFormsMatch(t *testing.T) {
	const full = "a1b2c3d4e5f60718293a4b5c6d7e8f90"
	short := logid.Short(full)

	if !sameID(full, short) {
		t.Fatalf("sameID(%q, %q) = false", full, short)
	}
	if !sameID(short, full) {
		t.Fatalf("sameID(%q, %q) = false", short, full)
	}
	if sameID(full, "ffffffffffffffffffffffffffffffff") {
		t.Fatal("two different identifiers matched")
	}
	if sameID(full, "") || sameID("", full) {
		t.Fatal("an empty value matched")
	}
}
