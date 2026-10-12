package main

import (
	"fmt"
	"os"
	"path/filepath"
)

var repoMarkers = []string{
	filepath.Join("backend", "go.mod"),
	filepath.Join("frontend", "go.mod"),
}

var defaultLogDir = filepath.Join("tmp", "test")

func findRepoRoot(start string) string {
	dir, err := filepath.Abs(start)
	if err != nil {
		return ""
	}
	for {
		if hasMarkers(dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func hasMarkers(dir string) bool {
	for _, m := range repoMarkers {
		if _, err := os.Stat(filepath.Join(dir, m)); err != nil {
			return false
		}
	}
	return true
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func resolveRoot(given, cwd string) (string, error) {
	repo := findRepoRoot(cwd)

	if given == "" {
		if repo == "" {
			return "", fmt.Errorf(
				"no run directory given, and %s is not inside the haoma tree, so there is no %s to fall back on\n"+
					"give the directory with -root, or run this from inside the tree",
				cwd, defaultLogDir)
		}
		path := filepath.Join(repo, defaultLogDir)
		if !isDir(path) {
			return "", fmt.Errorf(
				"no run directory given, and the usual one is not there: %s\n"+
					"give the directory with -root", path)
		}
		return path, nil
	}

	if filepath.IsAbs(given) {
		if !isDir(given) {
			return "", fmt.Errorf("no directory at %s\ngive the run directory with -root", given)
		}
		return given, nil
	}

	fromCwd := filepath.Join(cwd, given)
	if isDir(fromCwd) {
		return fromCwd, nil
	}
	if repo != "" {
		fromRepo := filepath.Join(repo, given)
		if isDir(fromRepo) {
			return fromRepo, nil
		}
		return "", fmt.Errorf(
			"no directory at %s, and none at %s either\ngive the run directory with -root",
			fromCwd, fromRepo)
	}
	return "", fmt.Errorf(
		"no directory at %s, and %s is not inside the haoma tree, so there was nowhere else to look\n"+
			"give the run directory with -root",
		fromCwd, cwd)
}

func countAccounts(srcs []source) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range srcs {
		if !seen[s.account] {
			seen[s.account] = true
			out = append(out, s.account)
		}
	}
	return out
}
