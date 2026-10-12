package main

import (
	"strings"

	"haoma-frontend/internal/logid"
)

const probe = "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"

var (
	probeShort = logid.Short(probe)
	shortMark  = strings.TrimLeft(probeShort, "z")
	shortLen   = len(probeShort) - len(shortMark)
)

func canonicalID(s string) string {
	return strings.TrimSuffix(logid.Short(s), shortMark)
}

func sameID(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return canonicalID(a) == canonicalID(b)
}

func shortID(v string) string { return logid.Short(v) }
