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

type idSet struct {
	seen map[string]string
}

func newIDSet() *idSet { return &idSet{seen: map[string]string{}} }

func (s *idSet) add(v string) {
	if v == "" {
		return
	}
	c := canonicalID(v)
	if old, ok := s.seen[c]; !ok || len(v) > len(old) {
		s.seen[c] = v
	}
}

func (s *idSet) has(v string) bool {
	if v == "" {
		return false
	}
	_, ok := s.seen[canonicalID(v)]
	return ok
}

func (s *idSet) len() int { return len(s.seen) }

func (s *idSet) display(v string) string {
	if got, ok := s.seen[canonicalID(v)]; ok {
		return got
	}
	return v
}

func short(v string) string { return logid.Short(v) }
