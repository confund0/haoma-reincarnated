//go:build !linux

package main

import "time"

type procEntry struct {
	pid  int
	ppid int
	comm string
}

type sweepResult struct {
	skipped string
	ended   []procEntry
	refused []procEntry
	changed []procEntry
	stuck   []procEntry
}

func endLeftoverTors(string, string, time.Duration) sweepResult {
	return sweepResult{skipped: "no /proc on this system"}
}

func describeProcs([]procEntry) string { return "" }
