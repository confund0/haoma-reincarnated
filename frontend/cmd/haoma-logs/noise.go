package main

import (
	"fmt"
	"io"
	"strings"
)

type noiseEntry struct {
	msg    string
	reason string
}

var noiseLines = []noiseEntry{
	{
		msg:    "tor control: auth method downgraded",
		reason: "the control port re-runs its whole auth sequence on every connection and writes one of these for each method that fails; it names no peer, no message and no envelope",
	},
}

func dropsAsNoise(e entry, f filters, keep bool) bool {
	if keep {
		return false
	}

	if e.level == "WARN" || e.level == "ERROR" {
		return false
	}

	if f.message != "" && strings.Contains(e.msg, f.message) {
		return false
	}
	for _, n := range noiseLines {
		if e.msg == n.msg {
			return true
		}
	}
	return false
}

func countNoise(srcs []source, f filters, keep bool) (map[string]int, error) {
	counts := map[string]int{}
	if keep {
		return counts, nil
	}
	err := eachEntry(srcs, func(e entry) bool {
		if !f.match(e) {
			return true
		}
		if dropsAsNoise(e, f, keep) {
			counts[e.msg]++
		}
		return true
	})
	return counts, err
}

func writeNoiseReport(w io.Writer, counts map[string]int) {
	total := 0
	for _, n := range counts {
		total += n
	}
	if total == 0 {
		return
	}
	fmt.Fprintln(w, "Left out as repeated noise. Keep them with -noise, or ask for one by name with -message:")
	for _, n := range noiseLines {
		if c := counts[n.msg]; c > 0 {
			fmt.Fprintf(w, "  %6d  %s\n", c, n.msg)
		}
	}
}
