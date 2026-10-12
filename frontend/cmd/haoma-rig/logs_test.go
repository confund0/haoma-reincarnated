package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeRig(t *testing.T, base, name string) *rig {
	t.Helper()
	root := filepath.Join(base, name)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	lines := map[string]string{
		"haomad.log":    "time=2026-08-15T10:00:00+02:00 level=INFO msg=\"daemon up\" rig=" + name + "\n",
		"haoma.log":     "time=2026-08-15T10:00:01+02:00 level=INFO msg=\"client up\" rig=" + name + "\n",
		"haomad.stderr": "haomad stderr " + name + "\n",
		"haoma.stderr":  "haoma stderr " + name + "\n",
	}
	for file, body := range lines {
		if err := os.WriteFile(filepath.Join(root, file), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &rig{name: name, root: root}
}

func captureRun(t *testing.T, f func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := logOut
	logOut = &buf
	defer func() { logOut = prev }()
	f()
	return buf.String()
}

func runConfig(base, stamp string) config {
	return config{base: base, stamp: stamp}
}

func makeRunDir(t *testing.T, base, name string) string {
	t.Helper()
	dir := filepath.Join(base, name, "dev-a")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "haomad.log"), []byte("old run\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(base, name)
}

func TestPassingRunKeepsItsLogs(t *testing.T) {
	base := t.TempDir()
	rigs := []*rig{fakeRig(t, base, "dev-a"), fakeRig(t, base, "peer")}
	cfg := runConfig(base, "20260815-101500")

	out := captureRun(t, func() { keepRunLogs(cfg, runPassed, rigs) })

	dst := filepath.Join(base, "pass-20260815-101500")
	for _, r := range rigs {
		for _, name := range []string{"haomad.log", "haoma.log", "haomad.stderr", "haoma.stderr"} {
			got, err := os.ReadFile(filepath.Join(dst, r.name, name))
			if err != nil {
				t.Fatalf("passing run did not keep %s/%s: %v", r.name, name, err)
			}
			if !strings.Contains(string(got), r.name) {
				t.Fatalf("%s/%s: kept the wrong content %q", r.name, name, got)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "trace.log")); err != nil {
		t.Fatalf("passing run did not keep the merged trace: %v", err)
	}

	if !strings.Contains(out, "run logs: "+dst) {
		t.Fatalf("passing run did not print its log directory; output was:\n%s", out)
	}
}

func TestFailingRunKeepsItsLogs(t *testing.T) {
	base := t.TempDir()
	rigs := []*rig{fakeRig(t, base, "dev-a")}
	cfg := runConfig(base, "20260815-102000")

	out := captureRun(t, func() { keepRunLogs(cfg, runFailed, rigs) })

	dst := filepath.Join(base, "fail-20260815-102000")
	for _, name := range []string{"haomad.log", "haoma.log", "haomad.stderr", "haoma.stderr"} {
		if _, err := os.Stat(filepath.Join(dst, "dev-a", name)); err != nil {
			t.Fatalf("failing run did not keep %s: %v", name, err)
		}
	}
	if !strings.Contains(out, "run logs: "+dst) {
		t.Fatalf("failing run did not print its log directory; output was:\n%s", out)
	}
}

func TestKeptLogsIncludeTheRotatedHalf(t *testing.T) {
	base := t.TempDir()
	r := fakeRig(t, base, "dev-a")
	if err := os.WriteFile(filepath.Join(r.root, "haomad.log.prev"), []byte("first half\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := runConfig(base, "20260815-102500")

	captureRun(t, func() { keepRunLogs(cfg, runPassed, []*rig{r}) })

	got, err := os.ReadFile(filepath.Join(base, "pass-20260815-102500", "dev-a", "haomad.log.prev"))
	if err != nil {
		t.Fatalf("the rotated half was not kept: %v", err)
	}
	if string(got) != "first half\n" {
		t.Fatalf("rotated half kept the wrong content %q", got)
	}
}

func TestPruningKeepsTheNewestRuns(t *testing.T) {
	base := t.TempDir()
	var made []string
	for i := 0; i < keepPassingRuns+3; i++ {
		made = append(made, makeRunDir(t, base, fmt.Sprintf("pass-20260801-1000%02d", i)))
	}
	rigs := []*rig{fakeRig(t, base, "dev-a")}
	cfg := runConfig(base, "20260815-103000")

	captureRun(t, func() { keepRunLogs(cfg, runPassed, rigs) })

	for _, dir := range made[:4] {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("%s survived the prune", dir)
		}
	}
	for _, dir := range made[4:] {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("%s was pruned but should have been kept: %v", dir, err)
		}
	}
	left := countRunDirs(t, base, "pass-")
	if left != keepPassingRuns {
		t.Fatalf("kept %d passing runs, want %d", left, keepPassingRuns)
	}
}

func TestFailingRunsSurviveAPruneOfPassingRuns(t *testing.T) {
	base := t.TempDir()
	oldFail := makeRunDir(t, base, "fail-20260701-090000")
	for i := 0; i < keepPassingRuns+5; i++ {
		makeRunDir(t, base, fmt.Sprintf("pass-20260801-1000%02d", i))
	}
	rigs := []*rig{fakeRig(t, base, "dev-a")}
	cfg := runConfig(base, "20260815-104000")

	captureRun(t, func() { keepRunLogs(cfg, runPassed, rigs) })

	if _, err := os.Stat(filepath.Join(oldFail, "dev-a", "haomad.log")); err != nil {
		t.Fatalf("a passing run pruned a failing run's logs: %v", err)
	}
	if left := countRunDirs(t, base, "pass-"); left != keepPassingRuns {
		t.Fatalf("kept %d passing runs, want %d", left, keepPassingRuns)
	}
}

func TestKeepAllLogsPrunesNothing(t *testing.T) {
	base := t.TempDir()
	for i := 0; i < keepPassingRuns+3; i++ {
		makeRunDir(t, base, fmt.Sprintf("pass-20260801-1000%02d", i))
	}
	rigs := []*rig{fakeRig(t, base, "dev-a")}
	cfg := runConfig(base, "20260815-105000")
	cfg.keepAllLogs = true

	captureRun(t, func() { keepRunLogs(cfg, runPassed, rigs) })

	if left := countRunDirs(t, base, "pass-"); left != keepPassingRuns+4 {
		t.Fatalf("with -keep-all-logs %d passing runs are left, want %d", left, keepPassingRuns+4)
	}
}

func TestAbortedRunKeepsItsLogsAndPrintsThePath(t *testing.T) {
	base := t.TempDir()
	rigs := []*rig{fakeRig(t, base, "dev-a")}
	cfg := runConfig(base, "20260815-110000")
	evidence := &runEvidence{cfg: cfg, rigs: &rigs}

	out := captureRun(t, evidence.keepIfAborted)

	dst := filepath.Join(base, "abort-20260815-110000")
	for _, name := range []string{"haomad.log", "haoma.log", "haomad.stderr", "haoma.stderr"} {
		if _, err := os.Stat(filepath.Join(dst, "dev-a", name)); err != nil {
			t.Fatalf("aborted run did not keep %s: %v", name, err)
		}
	}
	if !strings.Contains(out, "run logs: "+dst) {
		t.Fatalf("aborted run did not print its log directory; output was:\n%s", out)
	}
}

func TestAnAbortedRunIsNamedApartFromAFailedOne(t *testing.T) {
	base := t.TempDir()
	rigs := []*rig{fakeRig(t, base, "dev-a")}
	cfg := runConfig(base, "20260815-111000")
	evidence := &runEvidence{cfg: cfg, rigs: &rigs}

	captureRun(t, evidence.keepIfAborted)

	if _, err := os.Stat(filepath.Join(base, "abort-20260815-111000")); err != nil {
		t.Fatalf("aborted run is not under its own name: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "fail-20260815-111000")); !os.IsNotExist(err) {
		t.Fatal("an aborted run was kept as a failed one")
	}
}

func TestAbortedRunsSurviveAPruneOfPassingRuns(t *testing.T) {
	base := t.TempDir()
	oldAbort := makeRunDir(t, base, "abort-20260701-090000")
	for i := 0; i < keepPassingRuns+5; i++ {
		makeRunDir(t, base, fmt.Sprintf("pass-20260801-1000%02d", i))
	}
	rigs := []*rig{fakeRig(t, base, "dev-a")}
	cfg := runConfig(base, "20260815-112000")

	captureRun(t, func() { keepRunLogs(cfg, runPassed, rigs) })

	if _, err := os.Stat(filepath.Join(oldAbort, "dev-a", "haomad.log")); err != nil {
		t.Fatalf("a passing run pruned an aborted run's logs: %v", err)
	}
	if left := countRunDirs(t, base, "pass-"); left != keepPassingRuns {
		t.Fatalf("kept %d passing runs, want %d", left, keepPassingRuns)
	}
}

func TestAFinishedRunKeepsItsLogsOnce(t *testing.T) {
	base := t.TempDir()
	rigs := []*rig{fakeRig(t, base, "dev-a")}
	cfg := runConfig(base, "20260815-113000")
	evidence := &runEvidence{cfg: cfg, rigs: &rigs}

	captureRun(t, func() {
		evidence.keep(runPassed)
		evidence.keepIfAborted()
	})

	if _, err := os.Stat(filepath.Join(base, "pass-20260815-113000")); err != nil {
		t.Fatalf("the finished run did not keep its logs: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "abort-20260815-113000")); !os.IsNotExist(err) {
		t.Fatal("a finished run also kept its logs as an aborted run")
	}
}

func TestARunThatLaunchedNothingWritesNoDirectory(t *testing.T) {
	base := t.TempDir()
	var rigs []*rig
	cfg := runConfig(base, "20260815-114000")
	evidence := &runEvidence{cfg: cfg, rigs: &rigs}

	captureRun(t, evidence.keepIfAborted)

	if _, err := os.Stat(filepath.Join(base, "abort-20260815-114000")); !os.IsNotExist(err) {
		t.Fatal("a run that launched nothing wrote a log directory")
	}
}

func TestPruningLeavesDirectoriesThatAreNotRuns(t *testing.T) {
	base := t.TempDir()
	byHand := filepath.Join(base, "fail-2025-notes")
	if err := os.MkdirAll(byHand, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < keepFailingRuns+2; i++ {
		makeRunDir(t, base, fmt.Sprintf("fail-20260801-1000%02d", i))
	}

	pruneRuns(base, "fail-", keepFailingRuns)

	if _, err := os.Stat(byHand); err != nil {
		t.Fatalf("pruning removed a directory that is not a run: %v", err)
	}
}

func countRunDirs(t *testing.T, base, prefix string) int {
	t.Helper()
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), prefix) {
			n++
		}
	}
	return n
}

func filteredConfig(t *testing.T, base, stamp string) config {
	t.Helper()
	cfg := runConfig(base, stamp)
	cfg.scenario, cfg.cases = scenarioSibling, "alias"
	if err := resolveRunCases(&cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestFilteredRunsAreKeptApart(t *testing.T) {
	for _, o := range runOutcomes {
		t.Run(o.prefix(), func(t *testing.T) {
			base := t.TempDir()
			var full, filtered []string
			for i := 0; i < keepRuns(o, false); i++ {
				full = append(full, makeRunDir(t, base, fmt.Sprintf("%s20260801-0900%02d", o.prefix(), i)))
			}
			fp := runDirPrefix(o, true)
			for i := 0; i < keepRuns(o, true)+3; i++ {
				filtered = append(filtered, makeRunDir(t, base, fmt.Sprintf("%s20260801-1000%02d", fp, i)))
			}
			rigs := []*rig{fakeRig(t, base, "dev-a")}
			cfg := filteredConfig(t, base, "20260815-110000")

			out := captureRun(t, func() { keepRunLogs(cfg, o, rigs) })

			dst := filepath.Join(base, fp+"20260815-110000")
			if _, err := os.Stat(filepath.Join(dst, "dev-a", "haomad.log")); err != nil {
				t.Fatalf("the filtered run was not kept under %s: %v", dst, err)
			}
			if !strings.Contains(out, dst) {
				t.Fatalf("the run did not print where its logs are:\n%s", out)
			}
			for _, dir := range full {
				if _, err := os.Stat(dir); err != nil {
					t.Fatalf("a filtered run pruned a full run %s: %v", dir, err)
				}
			}

			for _, dir := range filtered[:4] {
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatalf("%s survived the prune", dir)
				}
			}
			if left := countRunDirs(t, base, fp); left != keepRuns(o, true) {
				t.Fatalf("kept %d filtered runs, want %d", left, keepRuns(o, true))
			}
			if left := countRunDirs(t, base, o.prefix()); left != keepRuns(o, false) {
				t.Fatalf("kept %d full runs, want %d", left, keepRuns(o, false))
			}
		})
	}
}

func TestFullRunLeavesFilteredRuns(t *testing.T) {
	for _, o := range runOutcomes {
		t.Run(o.prefix(), func(t *testing.T) {
			base := t.TempDir()
			for i := 0; i < keepRuns(o, false)+2; i++ {
				makeRunDir(t, base, fmt.Sprintf("%s20260801-0900%02d", o.prefix(), i))
			}
			var filtered []string
			for i := 0; i < keepRuns(o, true); i++ {
				filtered = append(filtered, makeRunDir(t, base, fmt.Sprintf("%s20260801-1000%02d", runDirPrefix(o, true), i)))
			}
			rigs := []*rig{fakeRig(t, base, "dev-a")}
			captureRun(t, func() { keepRunLogs(runConfig(base, "20260815-111000"), o, rigs) })
			for _, dir := range filtered {
				if _, err := os.Stat(dir); err != nil {
					t.Fatalf("a full run pruned a filtered run %s: %v", dir, err)
				}
			}
			if left := countRunDirs(t, base, o.prefix()); left != keepRuns(o, false) {
				t.Fatalf("kept %d full runs, want %d", left, keepRuns(o, false))
			}
		})
	}
}

func TestRunDirPrefixesAreApart(t *testing.T) {
	var all []string
	for _, o := range runOutcomes {
		all = append(all, runDirPrefix(o, false), runDirPrefix(o, true))
	}
	for i, a := range all {
		for j, b := range all {
			if i != j && strings.HasPrefix(a, b) {
				t.Errorf("prefix %q starts with prefix %q, so a prune of one removes the other", a, b)
			}
		}
	}
}

func TestScopeFileIsWrittenForEveryOutcome(t *testing.T) {
	for _, o := range runOutcomes {
		for _, filtered := range []bool{false, true} {
			name := runDirPrefix(o, filtered)
			t.Run(name, func(t *testing.T) {
				base := t.TempDir()
				cfg := runConfig(base, "20260815-112000")
				cfg.scenario = scenarioSibling
				if filtered {
					cfg = filteredConfig(t, base, "20260815-112000")
				} else if err := resolveRunCases(&cfg); err != nil {
					t.Fatal(err)
				}
				captureRun(t, func() { keepRunLogs(cfg, o, []*rig{fakeRig(t, base, "dev-a")}) })
				got, err := os.ReadFile(filepath.Join(base, name+"20260815-112000", scopeFile))
				if err != nil {
					t.Fatalf("no %s: %v", scopeFile, err)
				}
				want := cfg.siblingPlan.describe() + "\noutcome: " + strings.TrimSuffix(o.prefix(), "-") + "\n"
				if string(got) != want {
					t.Fatalf("%s = %q, want %q", scopeFile, got, want)
				}
			})
		}
	}
}

func TestFinalLine(t *testing.T) {
	full := config{scenario: scenarioSibling}
	if err := resolveRunCases(&full); err != nil {
		t.Fatal(err)
	}
	part := filteredConfig(t, t.TempDir(), "20260815-113000")
	for _, tc := range []struct {
		name string
		cfg  config
		o    runOutcome
		want string
	}{
		{"a full sibling pass", full, runPassed, "ALL FLOWS PASSED + LOGS CLEAN"},
		{"another scenario's pass", config{scenario: scenarioStar}, runPassed, "ALL FLOWS PASSED + LOGS CLEAN"},
		{"a filtered sibling pass", part, runPassed, "ASKED GROUPS PASSED + LOGS CLEAN — " + part.siblingPlan.describe()},
		{"a filtered sibling failure", part, runFailed, ""},
		{"a full sibling abort", full, runAborted, ""},
	} {
		if got := finalLine(tc.cfg, tc.o); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	if runOutcomeFor(true) != runPassed || runOutcomeFor(false) != runFailed {
		t.Error("runOutcomeFor does not map ok to a pass and not-ok to a failure")
	}
}
