package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

type problem struct {
	category string
	e        entry
}

func sweep(srcs []source, f filters, limit int) ([]problem, map[string]int, error) {
	var out []problem
	counts := map[string]int{}
	err := eachEntry(srcs, func(e entry) bool {
		if !f.match(e) {
			return true
		}
		low := strings.ToLower(e.raw)
		hit := ""
		for _, sig := range denylist {
			if strings.Contains(low, strings.ToLower(sig)) {
				hit = sig
				break
			}
		}
		switch {
		case hit != "":
			counts[e.account+" denylist"]++
			out = append(out, problem{category: "DENY " + hit, e: e})
		case e.level == "ERROR":
			counts[e.account+" error"]++
			out = append(out, problem{category: "ERROR", e: e})
		case e.level == "WARN":
			counts[e.account+" warning"]++
			out = append(out, problem{category: "WARN", e: e})
		default:
			return true
		}
		return limit <= 0 || len(out) < limit
	})
	return out, counts, err
}

func writeProblems(w io.Writer, ps []problem, counts map[string]int) {
	fmt.Fprintln(w, "Warnings and errors")
	if len(ps) == 0 {
		fmt.Fprintln(w, "  none")
		return
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(w, "  %-28s %d\n", k, counts[k])
	}
	fmt.Fprintln(w)
	for _, p := range ps {
		fmt.Fprintf(w, "%-24s %-16s %-10s %s\n",
			stamp(p.e), p.e.account+"/"+p.e.process, p.category, trimTime(p.e.raw))
	}
}
