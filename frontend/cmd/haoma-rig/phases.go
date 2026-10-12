package main

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

type phaseRecord struct {
	name  string
	dur   time.Duration
	count int
	order int
}

var (
	phaseMu sync.Mutex
	phases  = map[string]*phaseRecord{}
	sleeps  = map[string]*phaseRecord{}
	seq     int
)

func timePhase(name string, fn func() error) error {
	start := time.Now()
	err := fn()
	addTo(phases, name, time.Since(start))
	return err
}

func markPhase(name string, since time.Time) {
	addTo(phases, name, time.Since(since))
}

func rigSleep(d time.Duration, why string) {
	time.Sleep(d)
	addTo(sleeps, why, d)
}

func addTo(into map[string]*phaseRecord, name string, d time.Duration) {
	phaseMu.Lock()
	defer phaseMu.Unlock()
	p, seen := into[name]
	if !seen {
		seq++
		p = &phaseRecord{name: name, order: seq}
		into[name] = p
	}
	p.dur += d
	p.count++
}

func reportPhases(total time.Duration) {
	phaseMu.Lock()
	defer phaseMu.Unlock()
	if len(phases) == 0 {
		return
	}
	logf("─────────────────────────")
	logf("where the time went (total %s)", total.Truncate(time.Millisecond))
	for _, p := range byLongest(phases) {
		logf("  %-34s %9s%s", p.name, p.dur.Truncate(time.Millisecond), timesRan(p))
	}
	if len(sleeps) == 0 {
		return
	}
	var slept time.Duration
	for _, p := range sleeps {
		slept += p.dur
	}
	logf("  of which fixed sleeps            %9s", slept.Truncate(time.Millisecond))
	for _, p := range byLongest(sleeps) {
		logf("    %-32s %9s%s", p.name, p.dur.Truncate(time.Millisecond), timesRan(p))
	}
}

func byLongest(in map[string]*phaseRecord) []*phaseRecord {
	out := make([]*phaseRecord, 0, len(in))
	for _, p := range in {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].dur != out[j].dur {
			return out[i].dur > out[j].dur
		}
		return out[i].order < out[j].order
	})
	return out
}

func timesRan(p *phaseRecord) string {
	if p.count < 2 {
		return ""
	}
	return fmt.Sprintf("  (%d runs)", p.count)
}
