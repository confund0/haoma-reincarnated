package main

import (
	"os"
	"strings"
	"testing"
)

func TestCountLocalTriggerSweepsCountsOnlyStartedSweeps(t *testing.T) {
	r := writeRigLog(t, "dev-a",
		shortIDLine("synctrigger: journal growth started a sweep", "waited", "5.001s"),
		shortIDLine("synctrigger: journal growth started no sweep — this device has no sibling to ask"),
		shortIDLine("synctrigger: journal growth started no sweep — one was already running; this growth rides the next one", "waited", "5.002s"),
		shortIDLine("synctrigger: the journal grew; a sweep is armed", "wait", "5s", "held_by", "debounce"),
		shortIDLine("synctrigger: journal growth started a sweep", "waited", "60.4s"),
	)
	n, err := countLocalTriggerSweeps(r)
	if err != nil {
		t.Fatalf("countLocalTriggerSweeps: %v", err)
	}
	if n != 2 {
		t.Fatalf("counted %d started sweeps, want 2 — an armed or declined trigger was counted as a sweep", n)
	}
}

func TestCountLocalTriggerSweepsIsZeroWhenItNeverFired(t *testing.T) {
	r := writeRigLog(t, "dev-a",
		shortIDLine("gapfill: sweep starting", "sibling_count", "1"),
		shortIDLine("synctrigger: the journal grew; a sweep is armed", "wait", "5s", "held_by", "debounce"),
	)
	n, err := countLocalTriggerSweeps(r)
	if err != nil {
		t.Fatalf("countLocalTriggerSweeps: %v", err)
	}
	if n != 0 {
		t.Fatalf("counted %d started sweeps on a device that only armed one, want 0", n)
	}
}

func TestCountLocalTriggerSweepsRefusesAMissingLog(t *testing.T) {
	r := &rig{name: "dev-a", root: t.TempDir()}
	if _, err := countLocalTriggerSweeps(r); err == nil {
		t.Error("countLocalTriggerSweeps returned no error on a rig with no log")
	}
}

func TestCompleteLocalTriggerVerdicts(t *testing.T) {
	out := completeLocalTriggerVerdicts(map[string]error{})
	if len(out) != len(localTriggerVerdicts) {
		t.Fatalf("got %d verdicts, want %d", len(out), len(localTriggerVerdicts))
	}
	for _, n := range localTriggerVerdicts {
		if out[n] == nil {
			t.Errorf("verdict %q came back nil — an unset verdict must report itself as a driver bug", n)
		}
	}
}

const syncTriggerSource = "../haoma/synctrigger.go"

func TestLocalTriggerMarkerMatchesTheDaemonSource(t *testing.T) {
	src, err := os.ReadFile(syncTriggerSource)
	if err != nil {
		t.Fatalf("cannot read %s: %v\nthe daemon source moved. Point this test at its new home, or the marker and its reader drift apart in silence.", syncTriggerSource, err)
	}
	if !strings.Contains(string(src), localTriggerMarker) {
		t.Errorf("%s no longer contains %q — this driver would count zero sweeps and report the trigger as dead", syncTriggerSource, localTriggerMarker)
	}

	for _, decline := range []string{
		"synctrigger: journal growth started no sweep",
	} {
		if !strings.Contains(string(src), decline) {
			t.Errorf("%s no longer says why the trigger declined — a no-op and a real sweep would leave the same trace", syncTriggerSource)
		}
		if strings.Contains(decline, localTriggerMarker) {
			t.Errorf("the decline line %q now contains the started-sweep marker — every decline would be counted as a sweep", decline)
		}
	}
}
