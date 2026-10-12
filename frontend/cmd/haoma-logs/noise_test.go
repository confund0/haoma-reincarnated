package main

import (
	"strings"
	"testing"
	"time"
)

var noisyMessage = noiseLines[0].msg

func noisyLine(ts, level string) string {
	return "time=" + ts + " level=" + level + ` msg="` + noisyMessage + `" service=haomad`
}

func realLine(ts string) string {
	return "time=" + ts + ` level=DEBUG msg="mirror: mutation mirrored to sibling" service=haoma kind=text msg_id=27a1474d… envelope_id=9dc2c40c…`
}

func noiseFixture(t *testing.T) string {
	t.Helper()
	return writeRun(t, map[string]string{
		"alice-vault/haomad.log": lines(
			bootLine("2026-08-15T10:00:00.000+01:00"),
			noisyLine("2026-08-15T10:00:01.000+01:00", "INFO"),
			noisyLine("2026-08-15T10:00:02.000+01:00", "INFO"),
			noisyLine("2026-08-15T10:00:03.000+01:00", "INFO"),
			realLine("2026-08-15T10:00:04.000+01:00"),

			noisyLine("2026-08-15T10:00:05.000+01:00", "WARN"),
			noisyLine("2026-08-15T10:00:06.000+01:00", "ERROR"),
		),
	})
}

func timelineOf(t *testing.T, root string, f filters, keepNoise bool) []entry {
	t.Helper()
	var out []entry
	if err := eachEntry(logsOnly(sourcesFor(t, root)), func(e entry) bool {
		if !f.match(e) {
			return true
		}
		if dropsAsNoise(e, f, keepNoise) {
			return true
		}
		out = append(out, e)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func countLevel(es []entry, level string) int {
	n := 0
	for _, e := range es {
		if e.level == level {
			n++
		}
	}
	return n
}

func TestNamedRepeatedLinesAreLeftOut(t *testing.T) {
	root := noiseFixture(t)
	kept := timelineOf(t, root, filters{}, false)
	for _, e := range kept {
		if e.msg == noisyMessage && e.level == "INFO" {
			t.Fatalf("a repeated line at %s was not left out", e.ts)
		}
	}

	var sawReal bool
	for _, e := range kept {
		if strings.HasPrefix(e.msg, "mirror:") {
			sawReal = true
		}
	}
	if !sawReal {
		t.Error("the line that matters was left out along with the noise")
	}
}

func TestAWarningOrAnErrorIsNeverLeftOut(t *testing.T) {
	root := noiseFixture(t)
	kept := timelineOf(t, root, filters{}, false)
	if got := countLevel(kept, "WARN"); got != 1 {
		t.Errorf("kept %d warnings, want 1 — a warning was dropped as noise", got)
	}
	if got := countLevel(kept, "ERROR"); got != 1 {
		t.Errorf("kept %d errors, want 1 — an error was dropped as noise", got)
	}

	for _, level := range []string{"WARN", "ERROR"} {
		e := parseLine(noisyLine("2026-08-15T10:00:05.000+01:00", level))
		if dropsAsNoise(e, filters{}, false) {
			t.Errorf("a line at %s carrying a named noise message was dropped", level)
		}
	}
}

func TestKeepingNoiseBringsEveryLineBack(t *testing.T) {
	root := noiseFixture(t)
	dropped := timelineOf(t, root, filters{}, false)
	kept := timelineOf(t, root, filters{}, true)
	if len(kept) <= len(dropped) {
		t.Fatalf("keeping the noise gave %d lines, dropping it gave %d", len(kept), len(dropped))
	}
	n := 0
	for _, e := range kept {
		if e.msg == noisyMessage {
			n++
		}
	}
	if n != 5 {
		t.Errorf("kept %d of the repeated lines, want all 5", n)
	}
}

func TestNamingALineReachesItEvenThoughItIsNoise(t *testing.T) {
	root := noiseFixture(t)
	f := filters{message: noisyMessage}
	kept := timelineOf(t, root, f, false)
	if len(kept) != 5 {
		t.Fatalf("naming the line gave %d lines, want all 5", len(kept))
	}

	part := filters{message: strings.Fields(noisyMessage)[0]}
	if got := len(timelineOf(t, root, part, false)); got != 5 {
		t.Errorf("naming part of the line gave %d lines, want 5", got)
	}

	other := filters{message: "mirror:"}
	for _, e := range timelineOf(t, root, other, false) {
		if e.msg == noisyMessage {
			t.Error("naming another line brought the noise back")
		}
	}
}

func TestWhatWasLeftOutIsCountedPerEntry(t *testing.T) {
	root := noiseFixture(t)
	counts, err := countNoise(logsOnly(sourcesFor(t, root)), filters{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if counts[noisyMessage] != 3 {
		t.Errorf("counted %d left out, want the 3 informational ones", counts[noisyMessage])
	}
	var b strings.Builder
	writeNoiseReport(&b, counts)
	out := b.String()
	if !strings.Contains(out, noisyMessage) {
		t.Errorf("the report does not name the line:\n%s", out)
	}
	if !strings.Contains(out, "3") {
		t.Errorf("the report does not carry the count:\n%s", out)
	}
	for _, flag := range []string{"-noise", "-message"} {
		if !strings.Contains(out, flag) {
			t.Errorf("the report does not say how to get the lines back with %s:\n%s", flag, out)
		}
	}
}

func TestNothingLeftOutMeansNothingSaid(t *testing.T) {
	root := writeRun(t, map[string]string{
		"alice-vault/haomad.log": lines(
			bootLine("2026-08-15T10:00:00.000+01:00"),
			realLine("2026-08-15T10:00:04.000+01:00"),
		),
	})
	counts, err := countNoise(logsOnly(sourcesFor(t, root)), filters{}, false)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	writeNoiseReport(&b, counts)
	if b.String() != "" {
		t.Errorf("nothing was left out, yet the header said something:\n%s", b.String())
	}
}

func TestTheCountFollowsTheWindow(t *testing.T) {
	root := noiseFixture(t)
	from, err := time.Parse(time.RFC3339Nano, "2026-08-15T10:00:03.000+01:00")
	if err != nil {
		t.Fatal(err)
	}
	counts, cerr := countNoise(logsOnly(sourcesFor(t, root)), filters{from: from, hasFrom: true}, false)
	if cerr != nil {
		t.Fatal(cerr)
	}
	if counts[noisyMessage] != 1 {
		t.Errorf("counted %d left out inside the window, want 1", counts[noisyMessage])
	}
}

func TestKeepingNoiseCountsNothingAsLeftOut(t *testing.T) {
	root := noiseFixture(t)
	counts, err := countNoise(logsOnly(sourcesFor(t, root)), filters{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(counts) != 0 {
		t.Errorf("nothing should be left out when the noise is kept, got %v", counts)
	}
}

func TestTheSweepStillSeesTheRepeatedLines(t *testing.T) {
	root := noiseFixture(t)
	ps, counts, err := sweep(pick(sourcesFor(t, root), false, true), filters{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if counts["alice warning"] != 1 || counts["alice error"] != 1 {
		t.Errorf("the sweep lost a warning or an error to the noise set: %v", counts)
	}
	var sawNoisy bool
	for _, p := range ps {
		if p.e.msg == noisyMessage {
			sawNoisy = true
		}
	}
	if !sawNoisy {
		t.Error("the sweep did not report the warning carrying a named noise message")
	}
}

func TestEveryNoiseEntryCarriesAReason(t *testing.T) {
	if len(noiseLines) == 0 {
		t.Fatal("the set is empty")
	}
	seen := map[string]bool{}
	for _, n := range noiseLines {
		if strings.TrimSpace(n.msg) == "" {
			t.Error("an entry has no message")
		}
		if len(strings.TrimSpace(n.reason)) < 20 {
			t.Errorf("the entry %q has no usable reason", n.msg)
		}
		if seen[n.msg] {
			t.Errorf("the entry %q is in the set twice", n.msg)
		}
		seen[n.msg] = true
	}
}
