package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

const (
	siblingCasesList = "list"
	siblingCasesHelp = "help"
)

type siblingPlan struct {
	run   []siblingGroup
	asked []string
	added []string
	total int

	warmUp bool

	metByWarmUp []string
}

func (p siblingPlan) filtered() bool { return len(p.run) < p.total }

func (p siblingPlan) describe() string {
	if !p.filtered() {
		return fmt.Sprintf("sibling groups: all %d selected (the full run)", p.total)
	}
	added := "none"
	if len(p.added) > 0 {
		added = strings.Join(p.added, ",")
	}
	warm := "warm-up: off (-warmup=false)"
	if p.warmUp {
		met := "none"
		if len(p.metByWarmUp) > 0 {
			met = strings.Join(p.metByWarmUp, ",")
		}
		warm = "warm-up: on, needs it met: " + met
	}
	return fmt.Sprintf("FILTERED sibling run, %d of %d groups selected — asked: %s; added as prerequisites: %s; %s. Not a full sibling pass",
		len(p.run), p.total, strings.Join(p.asked, ","), added, warm)
}

func (p siblingPlan) ranLine(ran []string) string {
	switch {
	case len(ran) == len(p.run):
		return fmt.Sprintf("%s; all %d selected ran", p.describe(), len(ran))
	case len(ran) == 0:
		return fmt.Sprintf("%s; none ran — the setup stopped the run", p.describe())
	default:
		return fmt.Sprintf("%s; %d of %d selected ran: %s", p.describe(), len(ran), len(p.run), strings.Join(ran, ","))
	}
}

func checkSiblingPlan(p siblingPlan) error {
	if p.total != len(siblingGroups) || len(p.run) == 0 {
		return fmt.Errorf("the sibling plan selects %d group(s) out of a table of %d, but siblingGroups holds %d — it was not resolved from the table, and a run on it would check nothing past its setup",
			len(p.run), p.total, len(siblingGroups))
	}
	return nil
}

func isSiblingCasesList(spec string) bool {
	s := strings.TrimSpace(spec)
	return s == siblingCasesList || s == siblingCasesHelp
}

func resolveSiblingCases(groups []siblingGroup, spec string, warmUp bool) (siblingPlan, error) {
	plan := siblingPlan{total: len(groups)}
	if strings.TrimSpace(spec) == "" {
		plan.run = groups
		return plan, nil
	}

	index := make(map[string]int, len(groups))
	for i, g := range groups {
		index[g.name] = i
	}

	asked := map[string]bool{}
	var unknown []string
	for _, item := range strings.Split(spec, ",") {
		name := strings.TrimSpace(item)
		if name == "" {
			continue
		}
		if _, ok := index[name]; !ok {
			unknown = append(unknown, fmt.Sprintf("%q", name))
			continue
		}
		asked[name] = true
	}
	if len(unknown) > 0 {
		return siblingPlan{}, fmt.Errorf("unknown -cases group %s (want one or more of: %s; -cases list shows what each one needs)",
			strings.Join(unknown, ", "), strings.Join(siblingGroupNames(groups), ", "))
	}
	if len(asked) == 0 {
		return siblingPlan{}, fmt.Errorf("-cases %q names no group (want one or more of: %s)",
			spec, strings.Join(siblingGroupNames(groups), ", "))
	}

	take := map[string]bool{}
	met := map[string]bool{}
	var walk func(name string) error
	walk = func(name string) error {
		if take[name] {
			return nil
		}
		take[name] = true
		for _, n := range groups[index[name]].needs {
			if _, ok := index[n.group]; !ok {
				return fmt.Errorf("sibling group %q needs %q, which is not a group — the table is wrong", name, n.group)
			}
			if warmUp && n.state != "" && warmUpProvides[n.state] {
				met[name] = true
				continue
			}
			if err := walk(n.group); err != nil {
				return err
			}
		}
		return nil
	}
	names := make([]string, 0, len(asked))
	for name := range asked {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := walk(name); err != nil {
			return siblingPlan{}, err
		}
	}

	for _, g := range groups {
		if !take[g.name] {
			continue
		}
		plan.run = append(plan.run, g)
		if asked[g.name] {
			plan.asked = append(plan.asked, g.name)
		} else {
			plan.added = append(plan.added, g.name)
		}
		if met[g.name] {
			plan.metByWarmUp = append(plan.metByWarmUp, g.name)
		}
	}

	if plan.warmUp = warmUp && plan.filtered(); !plan.warmUp {
		plan.metByWarmUp = nil
	}
	return plan, nil
}

func resolveRunCases(cfg *config) error {
	if cfg.scenario != scenarioSibling {
		if strings.TrimSpace(cfg.cases) != "" {
			return fmt.Errorf("-cases is only for -scenario %s (got -scenario %s)", scenarioSibling, cfg.scenario)
		}
		return nil
	}
	plan, err := resolveSiblingCases(siblingGroups, cfg.cases, cfg.warmUp)
	if err != nil {
		return err
	}
	cfg.siblingPlan = plan
	return nil
}

func siblingGroupNames(groups []siblingGroup) []string {
	names := make([]string, len(groups))
	for i, g := range groups {
		names[i] = g.name
	}
	return names
}

func printSiblingGroups(w io.Writer, groups []siblingGroup) {
	fmt.Fprintln(w, "sibling groups, in the order every run takes them. The setup (pair, link, pair) always runs first.")
	fmt.Fprintln(w, "A group named in -cases brings the groups it needs.")
	fmt.Fprintln(w, "A filtered run warms up at the end of the setup: one text each way on every pairing, a sync on each of my")
	fmt.Fprintln(w, "devices, then a wait until no sync sweep is armed. A need marked [met by the warm-up] then adds no group;")
	fmt.Fprintln(w, "with -warmup=false it does. The full run takes no warm-up.")
	width := 0
	for _, g := range groups {
		width = max(width, len(g.name))
	}
	for _, g := range groups {
		if len(g.needs) == 0 {
			fmt.Fprintf(w, "  %-*s  needs nothing\n", width, g.name)
			continue
		}
		for i, n := range g.needs {
			label := g.name
			if i > 0 {
				label = ""
			}
			mark := ""
			if n.state != "" && warmUpProvides[n.state] {
				mark = " [met by the warm-up]"
			}
			fmt.Fprintf(w, "  %-*s  needs %s%s: %s\n", width, label, n.group, mark, n.why)
		}
	}
}
