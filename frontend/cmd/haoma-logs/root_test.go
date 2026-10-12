package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, m := range repoMarkers {
		p := filepath.Join(root, m)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("module fake\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, defaultLogDir, "alice-vault"), 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestFindRepoRootWalksUpToTheMarkers(t *testing.T) {
	root := fakeTree(t)
	deep := filepath.Join(root, "frontend", "cmd", "haoma-logs")
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := findRepoRoot(deep); got != root {
		t.Errorf("from %s got %q, want %q", deep, got, root)
	}
	if got := findRepoRoot(root); got != root {
		t.Errorf("from the top itself got %q, want %q", got, root)
	}
}

func TestFindRepoRootSaysNothingOutsideTheTree(t *testing.T) {
	outside := t.TempDir()
	if got := findRepoRoot(outside); got != "" {
		t.Errorf("outside the tree got %q, want the empty string", got)
	}
}

func TestAbsoluteRootIsUsedAsGiven(t *testing.T) {
	root := fakeTree(t)
	logs := filepath.Join(root, defaultLogDir)
	elsewhere := t.TempDir()

	got, err := resolveRoot(logs, elsewhere)
	if err != nil {
		t.Fatal(err)
	}
	if got != logs {
		t.Errorf("got %q, want %q", got, logs)
	}
}

func TestRelativeRootPrefersTheCurrentDirectory(t *testing.T) {
	root := fakeTree(t)

	here := filepath.Join(root, "frontend")
	if err := os.MkdirAll(filepath.Join(here, defaultLogDir, "eddie-vault"), 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := resolveRoot(defaultLogDir, here)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(here, defaultLogDir)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRelativeRootFallsBackToTheTopOfTheTree(t *testing.T) {
	root := fakeTree(t)
	here := filepath.Join(root, "frontend")
	if err := os.MkdirAll(here, 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := resolveRoot(defaultLogDir, here)
	if err != nil {
		t.Fatalf("standing in %s, %q did not resolve: %v", here, defaultLogDir, err)
	}
	want := filepath.Join(root, defaultLogDir)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNoRootGivenUsesTheUsualPlace(t *testing.T) {
	root := fakeTree(t)
	here := filepath.Join(root, "frontend", "cmd", "haoma-logs")
	if err := os.MkdirAll(here, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := resolveRoot("", here)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, defaultLogDir)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMissingRootIsAnErrorThatNamesThePathAndTheFlag(t *testing.T) {
	root := fakeTree(t)
	here := filepath.Join(root, "frontend")
	if err := os.MkdirAll(here, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()

	cases := []struct {
		name     string
		given    string
		cwd      string
		mustName []string
	}{
		{
			name:     "a relative path that is in neither place",
			given:    "no-such-run",
			cwd:      here,
			mustName: []string{filepath.Join(here, "no-such-run"), filepath.Join(root, "no-such-run"), "-root"},
		},
		{
			name:     "a path starting at the root of the filesystem that is not there",
			given:    filepath.Join(outside, "no-such-run"),
			cwd:      here,
			mustName: []string{filepath.Join(outside, "no-such-run"), "-root"},
		},
		{
			name:     "a relative path from outside the tree",
			given:    "no-such-run",
			cwd:      outside,
			mustName: []string{filepath.Join(outside, "no-such-run"), "-root"},
		},
		{
			name:     "nothing given, from outside the tree",
			given:    "",
			cwd:      outside,
			mustName: []string{outside, "-root"},
		},
	}
	for _, c := range cases {
		got, err := resolveRoot(c.given, c.cwd)
		if err == nil {
			t.Errorf("%s: resolved to %q, want an error", c.name, got)
			continue
		}
		for _, want := range c.mustName {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: the error does not name %q:\n%v", c.name, want, err)
			}
		}
	}
}

func TestNoRootGivenAndNothingInTheUsualPlaceIsAnError(t *testing.T) {
	root := t.TempDir()
	for _, m := range repoMarkers {
		p := filepath.Join(root, m)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("module fake\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, err := resolveRoot("", root)
	if err == nil {
		t.Fatal("a tree with no run directory resolved without an error")
	}
	if !strings.Contains(err.Error(), filepath.Join(root, defaultLogDir)) {
		t.Errorf("the error does not name the path it tried:\n%v", err)
	}
	if !strings.Contains(err.Error(), "-root") {
		t.Errorf("the error does not name the flag:\n%v", err)
	}
}

func TestDirectoryWithNoLogsIsAnError(t *testing.T) {
	empty := t.TempDir()
	err := run(empty, options{mapOnly: true})
	if err == nil {
		t.Fatal("a directory with no logs produced no error")
	}
	if !strings.Contains(err.Error(), empty) {
		t.Errorf("the error does not name the directory:\n%v", err)
	}
	if !strings.Contains(err.Error(), "-root") {
		t.Errorf("the error does not name the flag:\n%v", err)
	}
}

func TestCountAccounts(t *testing.T) {
	srcs := []source{
		{account: "alice", process: "haoma"},
		{account: "alice", process: "haomad"},
		{account: "eddie", process: "haomad"},
	}
	got := countAccounts(srcs)
	if strings.Join(got, ",") != "alice,eddie" {
		t.Errorf("got %v, want [alice eddie]", got)
	}
}
