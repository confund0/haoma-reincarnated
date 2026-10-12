package health

import "testing"

func TestParseBootstrap(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"NOTICE BOOTSTRAP PROGRESS=100 TAG=done SUMMARY=Done", 100},
		{"NOTICE BOOTSTRAP PROGRESS=42 TAG=loading_descriptors SUMMARY=...", 42},
		{"NOTICE BOOTSTRAP PROGRESS=0 TAG=starting SUMMARY=Starting", 0},
		{"", 0},
		{"PROGRESS=notanint", 0},
		{"no progress field here", 0},
	}
	for _, c := range cases {
		got := parseBootstrapPhase(c.in).Progress
		if got != c.want {
			t.Errorf("parseBootstrapPhase(%q).Progress = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestParseBootstrapPhaseReadsTheWholeLine(t *testing.T) {
	in := `WARN BOOTSTRAP PROGRESS=50 TAG=loading_descriptors SUMMARY="Loading relay descriptors" ` +
		`WARNING="Missing public key for onion-key" REASON=MISC COUNT=3 RECOMMENDATION=warn ` +
		`HOSTID="$AAAA" HOSTADDR="192.0.2.1:9001"`
	got := parseBootstrapPhase(in)
	want := BootstrapPhase{
		Severity:       "WARN",
		Progress:       50,
		Tag:            "loading_descriptors",
		Summary:        "Loading relay descriptors",
		Warning:        "Missing public key for onion-key",
		Reason:         "MISC",
		Count:          3,
		Recommendation: "warn",
	}
	if got != want {
		t.Errorf("parseBootstrapPhase:\n got  %+v\n want %+v", got, want)
	}
}

func TestParseBootstrapPhaseKeepsEscapedQuotes(t *testing.T) {
	in := `NOTICE BOOTSTRAP PROGRESS=10 TAG=conn_done SUMMARY="a \"quoted\" word"`
	if got := parseBootstrapPhase(in).Summary; got != `a "quoted" word` {
		t.Errorf("Summary = %q", got)
	}
}

func TestParseBootstrapPhaseSurvivesAnUnclosedQuote(t *testing.T) {
	in := `NOTICE BOOTSTRAP PROGRESS=10 TAG=conn_done SUMMARY="no end`
	got := parseBootstrapPhase(in)
	if got.Progress != 10 || got.Tag != "conn_done" || got.Summary != "no end" {
		t.Errorf("got %+v", got)
	}
}

func TestWarnKeyChangesOnlyWithTheWarning(t *testing.T) {
	a := BootstrapPhase{Severity: "WARN", Tag: "loading_descriptors", Warning: "x", Reason: "MISC", Count: 1}
	b := a
	b.Count = 7
	if a.warnKey() != b.warnKey() {
		t.Error("a rising count must not count as a new warning")
	}
	c := a
	c.Reason = "TIMEOUT"
	if a.warnKey() == c.warnKey() {
		t.Error("a new reason must count as a new warning")
	}
	if (BootstrapPhase{Severity: "NOTICE", Tag: "done"}).warnKey() != "" {
		t.Error("a NOTICE phase has no warning")
	}
}
