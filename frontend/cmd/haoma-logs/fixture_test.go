package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRun(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func onion(prefix string) string {
	return prefix + strings.Repeat("a", onionLen-len(prefix))
}

func sourcesFor(t *testing.T, root string) []source {
	t.Helper()
	srcs, err := discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) == 0 {
		t.Fatalf("no log file found under %s", root)
	}
	return srcs
}

func collect(t *testing.T, srcs []source) []entry {
	t.Helper()
	var out []entry
	if err := eachEntry(srcs, func(e entry) bool {
		out = append(out, e)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	return out
}
