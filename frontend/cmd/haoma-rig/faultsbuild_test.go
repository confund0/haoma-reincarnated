package main

import (
	"os"
	"runtime/debug"
	"strings"
	"testing"
)

func TestHasFaultsTag(t *testing.T) {
	for _, c := range []struct {
		name     string
		settings []debug.BuildSetting
		want     bool
	}{
		{"no tags", []debug.BuildSetting{{Key: "-trimpath", Value: "true"}}, false},
		{"other tag", []debug.BuildSetting{{Key: "-tags", Value: "netgo"}}, false},
		{"a tag that only starts the same", []debug.BuildSetting{{Key: "-tags", Value: "faultsx"}}, false},
		{"the tag in another setting", []debug.BuildSetting{{Key: "-ldflags", Value: "faults"}}, false},
		{"the tag alone", []debug.BuildSetting{{Key: "-tags", Value: "faults"}}, true},
		{"the tag among others", []debug.BuildSetting{{Key: "-tags", Value: "netgo,faults"}}, true},
	} {
		if got := hasFaultsTag(c.settings); got != c.want {
			t.Errorf("%s: got %t, want %t", c.name, got, c.want)
		}
	}
}

func TestFaultsBuildTagIsTheOneTheProductReads(t *testing.T) {
	for _, src := range []string{haomaFaultsSource, haomadStallFaultSource} {
		if got := buildConstraint(t, src); got != "//go:build "+faultsBuildTag {
			t.Errorf("%s: build constraint %q, want %q", src, got, "//go:build "+faultsBuildTag)
		}
	}
}

func buildConstraint(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "//go:build ") {
			return strings.TrimSpace(line)
		}
		if strings.HasPrefix(line, "package ") {
			break
		}
	}
	return ""
}
