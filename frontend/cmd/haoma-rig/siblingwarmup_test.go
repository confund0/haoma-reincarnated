package main

import (
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/ipc"
)

func TestTriggerLinesAreWhatHaomaLogs(t *testing.T) {
	src, err := os.ReadFile("../haoma/synctrigger.go")
	if err != nil {
		t.Fatalf("cannot read haoma's trigger: %v", err)
	}
	for _, want := range []string{strconv.Quote(triggerArmedLine), `"` + triggerFiredLine + ` a sweep"`, `"` + triggerFiredLine + ` no sweep`} {
		if !strings.Contains(string(src), want) {
			t.Errorf("cmd/haoma/synctrigger.go no longer holds %s", want)
		}
	}
}

func slogLine(msg string, fields ...string) string {
	return `time=2026-09-30T10:00:00+02:00 level=DEBUG msg=` + strconv.Quote(msg) + " " + strings.Join(fields, " ")
}

func TestStillQueued(t *testing.T) {
	rows := []backendapi.OutboxRow{
		{EnvelopeID: "e1", State: "enqueued"},
		{EnvelopeID: "e2", State: "sent"},
		{EnvelopeID: "e3", State: "delivered"},
		{EnvelopeID: "e4", State: "read"},
		{EnvelopeID: "e5", State: "failed"},
		{EnvelopeID: "e6", State: "enqueued"},
	}
	var got []string
	for _, r := range stillQueued(rows) {
		got = append(got, r.EnvelopeID)
	}
	if want := []string{"e1", "e6"}; !reflect.DeepEqual(got, want) {
		t.Errorf("still queued = %v, want %v", got, want)
	}
}

func TestOutboxStatesAreHaomads(t *testing.T) {
	src, err := os.ReadFile("../../../backend/internal/outbox/outbox.go")
	if err != nil {
		t.Fatalf("cannot read haomad's outbox: %v", err)
	}
	states := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^\s*(State[A-Z]\w*)\s*=\s*"(\w+)"`).FindAllStringSubmatch(string(src), -1) {
		states[m[1]] = m[2]
	}
	if states["StateEnqueued"] != outboxStateEnqueued {
		t.Errorf("haomad's StateEnqueued is %q, the warm-up asks for %q", states["StateEnqueued"], outboxStateEnqueued)
	}
	var values []string
	for _, v := range states {
		values = append(values, v)
	}
	sort.Strings(values)
	if want := []string{"delivered", "enqueued", "failed", "read", "sent"}; !reflect.DeepEqual(values, want) {
		t.Errorf("haomad's outbox states are %v, the warm-up was written for %v — decide whether a new one means still queued", values, want)
	}

	api, err := os.ReadFile("../../../backend/cmd/haomad/api.go")
	if err != nil {
		t.Fatalf("cannot read haomad's API: %v", err)
	}
	for _, want := range []string{`mux.HandleFunc("GET /outbox", d.handleOutboxList)`, `state := r.URL.Query().Get("state")`} {
		if !strings.Contains(string(api), want) {
			t.Errorf("backend/cmd/haomad/api.go no longer holds %s", want)
		}
	}
}

func TestSweepState(t *testing.T) {
	armed := slogLine(triggerArmedLine, "wait=5s", "held_by=debounce")
	fired := slogLine("synctrigger: journal growth started a sweep", "waited=5s")
	declined := slogLine("synctrigger: journal growth started no sweep — one was already running; this growth rides the next one")
	merge := slogLine("gapfill: batch merged", "applied=2")
	other := slogLine("rail: mirror applied — local state changed")
	for _, tc := range []struct {
		name      string
		lines     []string
		wantArmed bool
		wantCount int
	}{
		{"nothing", []string{other}, false, 0},
		{"armed, not fired", []string{fired, merge, armed}, true, 3},
		{"armed then fired", []string{armed, armed, fired}, false, 3},
		{"armed then declined", []string{armed, declined, other}, false, 2},
	} {
		a, c := sweepState(tc.lines)
		if a != tc.wantArmed || c != tc.wantCount {
			t.Errorf("%s: armed=%v count=%d, want armed=%v count=%d", tc.name, a, c, tc.wantArmed, tc.wantCount)
		}
	}
}

func TestSyncFinishedErr(t *testing.T) {
	for _, tc := range []struct {
		p    ipc.DeviceSyncFinishedPush
		want string
	}{
		{ipc.DeviceSyncFinishedPush{Siblings: 1, Answered: 1}, ""},
		{ipc.DeviceSyncFinishedPush{Siblings: 1, Answered: 0, TimedOut: true}, "stopped waiting"},
		{ipc.DeviceSyncFinishedPush{Siblings: 1, Unreachable: 1}, "could not reach"},
		{ipc.DeviceSyncFinishedPush{Siblings: 2, Answered: 1}, "heard from 1 of 2"},
	} {
		var err error
		captureRun(t, func() { err = syncFinishedErr("dev-a", tc.p) })
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%+v: %v, want no error", tc.p, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%+v: %v, want an error saying %q", tc.p, err, tc.want)
		}
	}
}

func TestGapFillCountsFromTheMark(t *testing.T) {
	r := fakeRig(t, t.TempDir(), "dev-b")
	merged := func(n int) string { return slogLine("gapfill: batch merged", "applied="+strconv.Itoa(n)) }
	write := func(lines ...string) {
		if err := os.WriteFile(r.root+"/haoma.log", []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	check := func(since gapFillMark, want string) {
		t.Helper()
		var err error
		captureRun(t, func() { err = assertGapFillDidTheWork(r, since) })
		switch {
		case want == "" && err != nil:
			t.Errorf("since %+v: %v, want a pass", since, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("since %+v: %v, want an error saying %q", since, err, want)
		}
	}

	write(merged(3), merged(0))
	check(gapFillMark{}, "")
	mark, err := gapFillMarkOf(r)
	if err != nil || mark != (gapFillMark{batches: 2, applied: 3}) {
		t.Fatalf("mark = %+v, %v; want 2 batches, 3 rows", mark, err)
	}

	check(mark, "never merged a gap-fill batch since the warm-up")

	write(merged(3), merged(0), merged(0))
	check(mark, "applied 0 rows")

	write(merged(3), merged(0), merged(0), merged(2))
	check(mark, "")

	write(merged(0))
	check(gapFillMark{}, "applied 0 rows")
}
