package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const bootMessage = "local API listening (TLS)"

const downMessage = "shutdown requested"

const daemonProcess = "haomad"

const defaultGap = 5 * time.Minute

type boundaryRule string

const (
	ruleBoot boundaryRule = "the daemon boot lines"
	ruleGap  boundaryRule = "a gap between log lines"
)

type runWindow struct {
	number int
	start  time.Time
	end    time.Time
}

type runSet struct {
	runs   []runWindow
	rule   boundaryRule
	gap    time.Duration
	newest time.Time
	oldest time.Time
}

func (rs *runSet) count() int { return len(rs.runs) }

func (rs *runSet) latest() (runWindow, bool) {
	if len(rs.runs) == 0 {
		return runWindow{}, false
	}
	return rs.runs[len(rs.runs)-1], true
}

func collectRuns(srcs []source, gap time.Duration) (*runSet, error) {
	if gap <= 0 {
		gap = defaultGap
	}

	type marker struct {
		account string
		at      time.Time
		up      bool
	}
	var markers []marker
	seenBoot := map[string]bool{}
	daemonAccounts := map[string]bool{}
	var gapStarts []time.Time
	var prev, newest, oldest time.Time

	for _, s := range srcs {
		if s.process == daemonProcess && !s.stderr {
			daemonAccounts[s.account] = true
		}
	}

	err := eachEntry(srcs, func(e entry) bool {
		if !e.ts.IsZero() {
			if oldest.IsZero() {
				oldest = e.ts
				gapStarts = append(gapStarts, e.ts)
			}
			if !prev.IsZero() && e.ts.Sub(prev) > gap {
				gapStarts = append(gapStarts, e.ts)
			}
			prev = e.ts
			if e.ts.After(newest) {
				newest = e.ts
			}
		}
		if e.process == daemonProcess {
			switch e.msg {
			case bootMessage:
				markers = append(markers, marker{account: e.account, at: e.ts, up: true})
				seenBoot[e.account] = true
			case downMessage:
				markers = append(markers, marker{account: e.account, at: e.ts, up: false})
			}
		}
		return true
	})
	if err != nil {
		return nil, err
	}

	rs := &runSet{gap: gap, newest: newest, oldest: oldest}

	usable := len(daemonAccounts) > 0
	for a := range daemonAccounts {
		if !seenBoot[a] {
			usable = false
			break
		}
	}
	if usable {
		rs.rule = ruleBoot
		var starts []time.Time
		live := map[string]bool{}
		upCount := 0
		for _, m := range markers {
			if m.up {

				if upCount == 0 {
					starts = append(starts, m.at)
				}
				if !live[m.account] {
					live[m.account] = true
					upCount++
				}
				continue
			}
			if live[m.account] {
				live[m.account] = false
				upCount--
			}
		}
		rs.runs = runsFromStarts(starts)
	} else {
		rs.rule = ruleGap
		rs.runs = runsFromStarts(gapStarts)
	}
	rs.fillEnds(newest)
	return rs, nil
}

func runsFromStarts(starts []time.Time) []runWindow {
	sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })
	out := make([]runWindow, 0, len(starts))
	for i, s := range starts {
		out = append(out, runWindow{number: i + 1, start: s})
	}
	return out
}

func (rs *runSet) fillEnds(newest time.Time) {
	for i := range rs.runs {
		if i+1 < len(rs.runs) {
			rs.runs[i].end = rs.runs[i+1].start
		} else {
			rs.runs[i].end = newest
		}
	}
}

type window struct {
	from    time.Time
	to      time.Time
	hasFrom bool
	hasTo   bool
	why     string
	hidden  string
}

func chooseWindow(rs *runSet, all bool, from, to time.Time, hasFrom, hasTo bool, since time.Duration) window {
	runs := plural(rs.count(), "run", "runs")

	switch {
	case all:
		return window{why: "Showing every line in this directory: " + runs + "."}

	case hasFrom || hasTo:
		w := window{from: from, to: to, hasFrom: hasFrom, hasTo: hasTo}
		switch {
		case hasFrom && hasTo:
			w.why = fmt.Sprintf("Showing %s to %s, as given. This directory holds %s.",
				clock(from), clock(to), runs)
		case hasFrom:
			w.why = fmt.Sprintf("Showing %s onwards, as given. This directory holds %s.",
				clock(from), runs)
		default:
			w.why = fmt.Sprintf("Showing everything up to %s, as given. This directory holds %s.",
				clock(to), runs)
		}
		return w

	case since > 0:
		start := rs.newest.Add(-since)
		return window{
			from: start, hasFrom: true,
			why: fmt.Sprintf("Showing the last %s, from %s. This directory holds %s.",
				since, clock(start), runs),
		}
	}

	latest, ok := rs.latest()
	if !ok {
		return window{why: "Showing every line in this directory. No run boundary was found."}
	}
	w := window{
		from: latest.start, hasFrom: true,
		why: fmt.Sprintf("Showing the most recent run, %d of %d, from %s to %s. Boundaries come from %s.",
			latest.number, rs.count(), clock(latest.start), clock(latest.end), rs.rule),
	}
	if hidden := rs.count() - 1; hidden > 0 {
		what := "The earlier run is"
		if hidden > 1 {
			what = fmt.Sprintf("The %d earlier runs are", hidden)
		}
		w.hidden = what + " not shown. List them with -runs. Read them all with -all," +
			" or name a start with -from or a length with -since."
	}
	return w
}

func (w window) apply(f filters) filters {
	if w.hasFrom && !f.hasFrom {
		f.from, f.hasFrom = w.from, true
	}
	if w.hasTo && !f.hasTo {
		f.to, f.hasTo = w.to, true
	}
	return f
}

func clock(t time.Time) string {
	if t.IsZero() {
		return "the start"
	}
	return t.Format(stampLayout)
}

func (rs *runSet) list() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s in this directory, boundaries from %s\n",
		plural(rs.count(), "run", "runs"), rs.rule)
	for i := len(rs.runs) - 1; i >= 0; i-- {
		r := rs.runs[i]
		fmt.Fprintf(&b, "  run %d of %d   %s to %s\n", r.number, rs.count(), clock(r.start), clock(r.end))
	}
	return b.String()
}
