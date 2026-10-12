package health

import (
	"sync"
	"testing"
)

func TestParseClockSkew(t *testing.T) {
	for _, tc := range []struct {
		name  string
		raw   string
		want  int
		known bool
	}{
		{"skew with a source", "SKEW=90 SOURCE=DIRSERV:171.25.193.9:443", 90, true},
		{"negative skew", "SKEW=-120 SOURCE=NETINFO:1.2.3.4:9001", -120, true},
		{"skew alone", "SKEW=7", 7, true},
		{"zero is a real reading", "SKEW=0 SOURCE=CONSENSUS", 0, true},
		{"lower case key", "skew=42", 42, true},
		{"source first", "SOURCE=CONSENSUS SKEW=11", 11, true},

		{"no estimate yet", "", 0, false},
		{"source but no skew", "SOURCE=CONSENSUS", 0, false},
		{"unparseable value", "SKEW=soon", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, known := parseClockSkew(tc.raw)
			if got != tc.want || known != tc.known {
				t.Fatalf("parseClockSkew(%q) = %d,%v; want %d,%v", tc.raw, got, known, tc.want, tc.known)
			}
		})
	}
}

func TestParseClockSkewTruncatesFractions(t *testing.T) {
	got, known := parseClockSkew("SKEW=90.7 SOURCE=CONSENSUS")
	if !known || got != 90 {
		t.Fatalf("got %d,%v; want 90,true", got, known)
	}
}

func TestSkewOverLimitNeedsAReading(t *testing.T) {
	if (Status{ClockSkew: 9999, ClockSkewKnown: false}).SkewOverLimit() {
		t.Fatal("a reading marked unknown raised the warning")
	}
	if (Status{}).SkewOverLimit() {
		t.Fatal("a status with no reading at all raised the warning")
	}
}

func TestSkewOverLimitBoundary(t *testing.T) {
	for _, tc := range []struct {
		skew int
		want bool
	}{
		{0, false},
		{ClockSkewLimit, false},
		{-ClockSkewLimit, false},
		{ClockSkewLimit + 1, true},
		{-ClockSkewLimit - 1, true},
	} {
		got := Status{ClockSkew: tc.skew, ClockSkewKnown: true}.SkewOverLimit()
		if got != tc.want {
			t.Fatalf("skew %d over the %ds limit = %v; want %v", tc.skew, ClockSkewLimit, got, tc.want)
		}
	}
}

func TestClockSkewLimitIsWiderThanTheStoreTolerance(t *testing.T) {
	if ClockSkewLimit <= 5 {
		t.Fatalf("the clock skew limit is %ds, which is not wider than the five-second store tolerance.\n"+
			"Tor's estimate is coarse to the second. A limit this narrow reports its noise as a wrong clock.",
			ClockSkewLimit)
	}
}

func resetSkewOffset(t *testing.T) {
	t.Helper()
	skewOnce = sync.Once{}
	skewSecs = 0
	t.Cleanup(func() {
		skewOnce = sync.Once{}
		skewSecs = 0
	})
}

func TestNoOffsetByDefault(t *testing.T) {
	resetSkewOffset(t)
	t.Setenv(skewEnv, "")
	if got := skewOffset(); got != 0 {
		t.Fatalf("offset is %d with nothing set, want 0", got)
	}
}

func TestOffsetShiftsTheReading(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int
	}{{"300", 300}, {"-300", -300}} {
		t.Run(tc.raw, func(t *testing.T) {
			resetSkewOffset(t)
			t.Setenv(skewEnv, tc.raw)
			if got := skewOffset(); got != tc.want {
				t.Fatalf("offset %q gave %d, want %d", tc.raw, got, tc.want)
			}
		})
	}
}

func TestMalformedOffsetIsIgnored(t *testing.T) {
	resetSkewOffset(t)
	t.Setenv(skewEnv, "five minutes")
	if got := skewOffset(); got != 0 {
		t.Fatalf("a malformed offset gave %d, want 0", got)
	}
}
