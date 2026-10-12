package main

import (
	"strings"
	"testing"
	"time"
)

func bootLine(ts string) string {
	return "time=" + ts + ` level=INFO msg="` + bootMessage + `" service=haomad addr=127.0.0.1:45335`
}

func downLine(ts string) string {
	return "time=" + ts + ` level=INFO msg="` + downMessage + `" service=haomad`
}

func noiseLine(ts string) string {
	return "time=" + ts + ` level=DEBUG msg="tor control: auth method downgraded" service=haomad`
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	got, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func twoRunFixture(t *testing.T) string {
	t.Helper()
	return writeRun(t, map[string]string{
		"alice-vault/haomad.log": lines(
			bootLine("2026-08-14T15:03:56.000+01:00"),
			noiseLine("2026-08-14T15:30:00.000+01:00"),
			downLine("2026-08-15T10:08:01.000+01:00"),
			bootLine("2026-08-15T10:08:47.000+01:00"),
			noiseLine("2026-08-15T10:15:00.000+01:00"),
			downLine("2026-08-15T10:17:42.000+01:00"),
			bootLine("2026-08-15T10:18:41.000+01:00"),
			noiseLine("2026-08-15T11:00:00.000+01:00"),
		),
		"alice-linked-vault/haomad.log": lines(
			bootLine("2026-08-14T15:03:58.000+01:00"),
			downLine("2026-08-14T15:21:16.000+01:00"),
			bootLine("2026-08-14T15:23:13.000+01:00"),
			downLine("2026-08-15T10:08:01.100+01:00"),
			bootLine("2026-08-15T10:08:49.000+01:00"),
			noiseLine("2026-08-15T11:30:00.000+01:00"),
		),
		"eddie-vault/haomad.log": lines(
			bootLine("2026-08-14T15:04:00.000+01:00"),
			downLine("2026-08-15T10:08:01.200+01:00"),
			bootLine("2026-08-15T10:08:52.000+01:00"),
			downLine("2026-08-15T10:20:11.000+01:00"),
			bootLine("2026-08-15T10:23:49.000+01:00"),
			noiseLine("2026-08-15T12:00:00.000+01:00"),
		),
	})
}

func TestTwoRunsAreFoundAndRestartsAreNotBoundaries(t *testing.T) {
	root := twoRunFixture(t)
	rs, err := collectRuns(logsOnly(sourcesFor(t, root)), defaultGap)
	if err != nil {
		t.Fatal(err)
	}
	if rs.rule != ruleBoot {
		t.Fatalf("boundaries came from %q, want the boot lines", rs.rule)
	}
	if rs.count() != 2 {
		t.Fatalf("found %d runs, want 2", rs.count())
		return
	}

	want1 := mustTime(t, "2026-08-14T15:03:56.000+01:00")
	want2 := mustTime(t, "2026-08-15T10:08:47.000+01:00")
	if !rs.runs[0].start.Equal(want1) {
		t.Errorf("run 1 starts at %s, want %s", rs.runs[0].start, want1)
	}
	if !rs.runs[1].start.Equal(want2) {
		t.Errorf("run 2 starts at %s, want %s", rs.runs[1].start, want2)
	}
	latest, ok := rs.latest()
	if !ok || !latest.start.Equal(want2) || latest.number != 2 {
		t.Errorf("the most recent run is %+v, want run 2 starting %s", latest, want2)
	}

	if !rs.runs[0].end.Equal(want2) {
		t.Errorf("run 1 ends at %s, want %s", rs.runs[0].end, want2)
	}
	if !rs.runs[1].end.Equal(rs.newest) {
		t.Errorf("run 2 ends at %s, want the newest line %s", rs.runs[1].end, rs.newest)
	}
}

func TestOneRunDirectory(t *testing.T) {
	root := writeRun(t, map[string]string{
		"alice-vault/haomad.log": lines(
			bootLine("2026-08-15T10:08:47.000+01:00"),
			noiseLine("2026-08-15T10:30:00.000+01:00"),
		),
		"eddie-vault/haomad.log": lines(
			bootLine("2026-08-15T10:08:52.000+01:00"),
			noiseLine("2026-08-15T10:40:00.000+01:00"),
		),
	})
	rs, err := collectRuns(logsOnly(sourcesFor(t, root)), defaultGap)
	if err != nil {
		t.Fatal(err)
	}
	if rs.count() != 1 {
		t.Fatalf("found %d runs, want 1", rs.count())
	}
	if rs.rule != ruleBoot {
		t.Errorf("boundaries came from %q, want the boot lines", rs.rule)
	}
	want := mustTime(t, "2026-08-15T10:08:47.000+01:00")
	if !rs.runs[0].start.Equal(want) {
		t.Errorf("the run starts at %s, want %s", rs.runs[0].start, want)
	}
	w := chooseWindow(rs, false, time.Time{}, time.Time{}, false, false, 0)
	if w.hidden != "" {
		t.Errorf("one run, yet the header says something is hidden: %q", w.hidden)
	}
	if !strings.Contains(w.why, "1 of 1") {
		t.Errorf("the header does not name the run: %q", w.why)
	}
}

func TestSingleAccountRuns(t *testing.T) {
	root := writeRun(t, map[string]string{
		"alice-vault/haomad.log": lines(
			bootLine("2026-08-14T15:03:56.000+01:00"),
			noiseLine("2026-08-14T15:30:00.000+01:00"),
			downLine("2026-08-15T10:08:01.000+01:00"),
			bootLine("2026-08-15T10:08:47.000+01:00"),
			noiseLine("2026-08-15T10:30:00.000+01:00"),
		),
	})
	rs, err := collectRuns(logsOnly(sourcesFor(t, root)), defaultGap)
	if err != nil {
		t.Fatal(err)
	}
	if rs.count() != 2 {
		t.Fatalf("found %d runs, want 2", rs.count())
	}
}

func TestManyRestartsOfOneAccountAreStillOneRun(t *testing.T) {
	root := writeRun(t, map[string]string{
		"dev-a/haomad.log": lines(
			bootLine("2026-08-14T18:13:04.138+01:00"),
			noiseLine("2026-08-14T18:25:00.000+01:00"),
		),
		"dev-b/haomad.log": lines(
			bootLine("2026-08-14T18:13:04.875+01:00"),
			downLine("2026-08-14T18:18:05.373+01:00"),
			bootLine("2026-08-14T18:18:13.367+01:00"),
			downLine("2026-08-14T18:20:30.489+01:00"),
			bootLine("2026-08-14T18:22:44.003+01:00"),
			downLine("2026-08-14T18:23:04.523+01:00"),
			bootLine("2026-08-14T18:23:26.965+01:00"),
			downLine("2026-08-14T18:24:14.225+01:00"),
			bootLine("2026-08-14T18:26:28.022+01:00"),
		),
		"peer/haomad.log": lines(
			bootLine("2026-08-14T18:13:05.617+01:00"),
			noiseLine("2026-08-14T18:29:41.891+01:00"),
		),
	})
	rs, err := collectRuns(logsOnly(sourcesFor(t, root)), defaultGap)
	if err != nil {
		t.Fatal(err)
	}
	if rs.count() != 1 {
		t.Fatalf("found %d runs, want 1 — restarts of one account while the others stay up are not run boundaries", rs.count())
	}
	want := mustTime(t, "2026-08-14T18:13:04.138+01:00")
	if !rs.runs[0].start.Equal(want) {
		t.Errorf("the run starts at %s, want the fleet's first boot %s", rs.runs[0].start, want)
	}
}

func TestFleetDownAndUpIsANewRun(t *testing.T) {
	root := writeRun(t, map[string]string{
		"dev-a/haomad.log": lines(
			bootLine("2026-08-14T18:13:04.000+01:00"),
			downLine("2026-08-14T18:29:40.000+01:00"),
			bootLine("2026-08-14T19:00:00.000+01:00"),
			noiseLine("2026-08-14T19:05:00.000+01:00"),
		),
		"peer/haomad.log": lines(
			bootLine("2026-08-14T18:13:05.000+01:00"),
			downLine("2026-08-14T18:29:41.000+01:00"),
			bootLine("2026-08-14T19:00:01.000+01:00"),
			noiseLine("2026-08-14T19:06:00.000+01:00"),
		),
	})
	rs, err := collectRuns(logsOnly(sourcesFor(t, root)), defaultGap)
	if err != nil {
		t.Fatal(err)
	}
	if rs.count() != 2 {
		t.Fatalf("found %d runs, want 2", rs.count())
	}
	want := mustTime(t, "2026-08-14T19:00:00.000+01:00")
	if !rs.runs[1].start.Equal(want) {
		t.Errorf("the second run starts at %s, want %s", rs.runs[1].start, want)
	}
}

func TestClientBootIsNotARunBoundary(t *testing.T) {
	root := writeRun(t, map[string]string{
		"alice-vault/haomad.log": lines(
			bootLine("2026-08-15T10:08:47.000+01:00"),
			noiseLine("2026-08-15T11:00:00.000+01:00"),
		),
		"alice-vault/haoma.log": lines(
			`time=2026-08-15T10:09:00.000+01:00 level=INFO msg="data dir" service=haoma path=/x`,
			`time=2026-08-15T10:40:00.000+01:00 level=INFO msg="data dir" service=haoma path=/x`,
			`time=2026-08-15T10:50:00.000+01:00 level=INFO msg="`+bootMessage+`" service=haoma addr=127.0.0.1:1`,
		),
	})
	rs, err := collectRuns(logsOnly(sourcesFor(t, root)), defaultGap)
	if err != nil {
		t.Fatal(err)
	}
	if rs.count() != 1 {
		t.Fatalf("found %d runs, want 1 — a client restart is not a run boundary", rs.count())
	}
}

func TestGapRuleTakesOverWhenTheBootLinesAreGone(t *testing.T) {
	root := writeRun(t, map[string]string{
		"alice-vault/haomad.log": lines(
			noiseLine("2026-08-14T15:03:56.000+01:00"),
			noiseLine("2026-08-14T15:04:56.000+01:00"),
			noiseLine("2026-08-15T10:08:47.000+01:00"),
			noiseLine("2026-08-15T10:09:47.000+01:00"),
		),
	})
	rs, err := collectRuns(logsOnly(sourcesFor(t, root)), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if rs.rule != ruleGap {
		t.Fatalf("boundaries came from %q, want the gap between lines", rs.rule)
	}
	if rs.count() != 2 {
		t.Fatalf("found %d runs, want 2", rs.count())
	}
	want := mustTime(t, "2026-08-15T10:08:47.000+01:00")
	if !rs.runs[1].start.Equal(want) {
		t.Errorf("the second run starts at %s, want %s", rs.runs[1].start, want)
	}

	w := chooseWindow(rs, false, time.Time{}, time.Time{}, false, false, 0)
	if !strings.Contains(w.why, string(ruleGap)) {
		t.Errorf("the header does not name the rule used: %q", w.why)
	}
	if strings.Contains(w.why, string(ruleBoot)) {
		t.Errorf("the header names both rules: %q", w.why)
	}
}

func TestOneAccountWithoutBootLinesDropsToTheFallback(t *testing.T) {
	root := writeRun(t, map[string]string{
		"alice-vault/haomad.log": lines(
			bootLine("2026-08-15T10:08:47.000+01:00"),
			noiseLine("2026-08-15T10:30:00.000+01:00"),
		),
		"eddie-vault/haomad.log": lines(
			noiseLine("2026-08-15T10:08:52.000+01:00"),
			noiseLine("2026-08-15T10:31:00.000+01:00"),
		),
	})
	rs, err := collectRuns(logsOnly(sourcesFor(t, root)), defaultGap)
	if err != nil {
		t.Fatal(err)
	}
	if rs.rule != ruleGap {
		t.Errorf("boundaries came from %q, want the gap between lines", rs.rule)
	}
}

func TestGapRuleFindsNothingWhenTheFleetKeepsWriting(t *testing.T) {
	var body []string
	base := mustTime(t, "2026-08-15T10:00:00.000+01:00")
	for i := 0; i < 40; i++ {
		body = append(body, noiseLine(base.Add(time.Duration(i)*40*time.Second).Format(time.RFC3339Nano)))
	}
	root := writeRun(t, map[string]string{
		"alice-vault/haomad.log": lines(body...),
	})
	rs, err := collectRuns(logsOnly(sourcesFor(t, root)), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if rs.rule != ruleGap {
		t.Fatalf("rule = %q, want the gap rule", rs.rule)
	}
	if rs.count() != 1 {
		t.Errorf("found %d runs in a fleet that never stopped writing, want 1", rs.count())
	}
}

func TestWindowPrecedence(t *testing.T) {
	root := twoRunFixture(t)
	rs, err := collectRuns(logsOnly(sourcesFor(t, root)), defaultGap)
	if err != nil {
		t.Fatal(err)
	}
	from := mustTime(t, "2026-08-15T09:00:00.000+01:00")
	to := mustTime(t, "2026-08-15T13:00:00.000+01:00")

	all := chooseWindow(rs, true, from, to, true, true, time.Hour)
	if all.hasFrom || all.hasTo {
		t.Error("reading everything still narrowed the window")
	}
	if !strings.Contains(all.why, "every line") {
		t.Errorf("the header does not say it read everything: %q", all.why)
	}

	explicit := chooseWindow(rs, false, from, to, true, true, time.Hour)
	if !explicit.from.Equal(from) || !explicit.to.Equal(to) {
		t.Errorf("an explicit start and end were not used: %+v", explicit)
	}

	since := chooseWindow(rs, false, time.Time{}, time.Time{}, false, false, time.Hour)
	wantSince := rs.newest.Add(-time.Hour)
	if !since.from.Equal(wantSince) {
		t.Errorf("a length gave %s, want %s", since.from, wantSince)
	}
	if since.hasTo {
		t.Error("a length narrowed the end as well")
	}

	byDefault := chooseWindow(rs, false, time.Time{}, time.Time{}, false, false, 0)
	latest, _ := rs.latest()
	if !byDefault.from.Equal(latest.start) {
		t.Errorf("the default gave %s, want the most recent run's start %s", byDefault.from, latest.start)
	}
	if byDefault.hidden == "" {
		t.Error("the default hid an earlier run without saying so")
	}
}

func TestWindowDoesNotOverrideWhatTheCallerGave(t *testing.T) {
	given := mustTime(t, "2026-08-15T12:00:00.000+01:00")
	w := window{from: mustTime(t, "2026-08-15T10:00:00.000+01:00"), hasFrom: true}
	f := w.apply(filters{from: given, hasFrom: true})
	if !f.from.Equal(given) {
		t.Errorf("the window overwrote the start the caller gave: %s", f.from)
	}
}

func TestDefaultWindowCutsTheOlderRunOut(t *testing.T) {
	root := twoRunFixture(t)
	srcs := sourcesFor(t, root)
	rs, err := collectRuns(logsOnly(srcs), defaultGap)
	if err != nil {
		t.Fatal(err)
	}
	w := chooseWindow(rs, false, time.Time{}, time.Time{}, false, false, 0)

	count := func(f filters) int {
		n := 0
		if err := eachEntry(logsOnly(srcs), func(e entry) bool {
			if f.match(e) {
				n++
			}
			return true
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	everything := count(filters{})
	oneRun := count(w.apply(filters{}))
	if oneRun >= everything {
		t.Errorf("the default read %d lines out of %d — it did not narrow anything", oneRun, everything)
	}
	if oneRun == 0 {
		t.Error("the default read nothing at all")
	}
}

func TestRunListNamesEveryRun(t *testing.T) {
	root := twoRunFixture(t)
	rs, err := collectRuns(logsOnly(sourcesFor(t, root)), defaultGap)
	if err != nil {
		t.Fatal(err)
	}
	got := rs.list()
	for _, want := range []string{"2 runs", "run 2 of 2", "run 1 of 2", string(ruleBoot)} {
		if !strings.Contains(got, want) {
			t.Errorf("the listing does not carry %q:\n%s", want, got)
		}
	}
}
