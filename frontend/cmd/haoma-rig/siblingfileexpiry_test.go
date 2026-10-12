package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"haoma-frontend/internal/logid"
)

const (
	haomaRunSource          = "../haoma/run.go"
	haomadFetchWorkerSource = "../../../backend/internal/files/fetch_worker.go"
	haomadFetchSource       = "../../../backend/internal/files/fetch.go"
)

func TestCrossHourNamesMatchHaoma(t *testing.T) {
	if got := stringConst(t, haomaFaultsSource, "holdGapFillFetchEnv"); got != holdGapFillFetchEnv {
		t.Errorf("haoma reads %q, the rig sets %q", got, holdGapFillFetchEnv)
	}
	if got := stringConst(t, haomaServeSource, "gapOfferLifetimeEnv"); got != gapOfferTTLEnv {
		t.Errorf("haoma reads %q, the rig sets %q", got, gapOfferTTLEnv)
	}
	if got := stringConst(t, haomaFaultsSource, "holdGapFillFetchOnLine"); got != holdGapFillFetchOnLine {
		t.Errorf("haoma writes the start line %q, the rig reads %q", got, holdGapFillFetchOnLine)
	}
}

func TestCrossHourLinesAreWhatTheDaemonsLog(t *testing.T) {
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("cannot read %s: %v", p, err)
		}
		return string(b)
	}
	sib, serve, ask := read(haomaSiblingSource), read(haomaServeSource), read(haomaAskSource)
	faults := read(haomaFaultsSource)
	worker, fetch := read(haomadFetchWorkerSource), read(haomadFetchSource)
	for _, c := range []struct{ src, file, want string }{
		{serve, haomaServeSource, strconv.Quote(gapOfferLifetimeLine)},
		{serve, haomaServeSource, `slog.Duration("lifetime", d)`},
		{serve, haomaServeSource, strconv.Quote(keptOfferDroppedLine)},
		{serve, haomaServeSource, `slog.String("staging_id", o.StagingID)`},
		{faults, haomaFaultsSource, strconv.Quote(ignoreSiblingOfferOnLine)},
		{read(haomaRunSource), haomaRunSource, strconv.Quote(ipcListeningLine)},
		{faults, haomaFaultsSource, strconv.Quote(gapFetchHeldLine)},

		{faults, haomaFaultsSource, `slog.String("staging_id", stagingID)`},
		{sib, haomaSiblingSource, `gapFillFetchHeldAtTake(d, logger, offer.StagingID)`},
		{sib, haomaSiblingSource, strconv.Quote(siblingFetchAskLine)},
		{sib, haomaSiblingSource, `slog.String("staging_id", held.StagingID)`},

		{sib, haomaSiblingSource, `slog.String("staging_id", ev.MsgID)`},
		{ask, haomaAskSource, strconv.Quote(gapAskedAgainLine)},
		{ask, haomaAskSource, `slog.String("fail_reason", reason)`},
		{worker, haomadFetchWorkerSource, strconv.Quote(haomadFetchBeginLine)},
		{worker, haomadFetchWorkerSource, strconv.Quote(haomadFetchFailedLine)},
		{worker, haomadFetchWorkerSource, `slog.String("reason", string(reason))`},
		{worker, haomadFetchWorkerSource, `"peer 404 (token unknown)"`},
		{worker, haomadFetchWorkerSource, `"peer 410 (token invalidated)"`},
		{fetch, haomadFetchSource, `FetchFailBlobGone FetchFailReason = ` + strconv.Quote(haomadBlobGone)},
	} {
		if !strings.Contains(c.src, c.want) {
			t.Errorf("%s no longer holds %s", c.file, c.want)
		}
	}
}

func TestHaomaEnvClearsTheCaseSettings(t *testing.T) {
	t.Setenv(holdGapFillFetchEnv, "1")
	t.Setenv(gapOfferTTLEnv, "90s")
	envOf := func(r *rig) map[string]string {
		out := map[string]string{}
		for _, kv := range childEnv(r.haomaEnv()) {
			k, v, _ := strings.Cut(kv, "=")
			out[k] = v
		}
		return out
	}
	off := envOf(&rig{name: "dev-b"})
	if off[holdGapFillFetchEnv] != "" || off[gapOfferTTLEnv] != "" {
		t.Errorf("off: %s=%q %s=%q; want both empty, whatever the shell holds",
			holdGapFillFetchEnv, off[holdGapFillFetchEnv], gapOfferTTLEnv, off[gapOfferTTLEnv])
	}
	on := envOf(&rig{name: "dev-b", holdGapFillFetch: true, gapOfferTTL: "1m30s"})
	if on[holdGapFillFetchEnv] != "1" || on[gapOfferTTLEnv] != "1m30s" {
		t.Errorf("on: %s=%q %s=%q; want 1 and 1m30s",
			holdGapFillFetchEnv, on[holdGapFillFetchEnv], gapOfferTTLEnv, on[gapOfferTTLEnv])
	}
}

func TestLineWantMatchesWholeFields(t *testing.T) {
	line := "time=2026-10-01T00:00:00Z level=DEBUG msg=\"" + keptOfferDroppedLine + "\" receipt_msg_id=" +
		logid.Short(otherMsgID) + " msg_id=" + logid.Short(gapMsgID) + " staging_id=abcdef01…"
	cases := []struct {
		name   string
		msg    string
		fields map[string]string
		want   bool
	}{
		{"the line", keptOfferDroppedLine, map[string]string{"msg_id": logid.Short(gapMsgID), "staging_id": "abcdef01…"}, true},
		{"another message", fileServedLine, map[string]string{"msg_id": logid.Short(gapMsgID)}, false},
		{"msg_id is not read from receipt_msg_id", keptOfferDroppedLine, map[string]string{"msg_id": logid.Short(otherMsgID)}, false},
		{"a value is compared whole", keptOfferDroppedLine, map[string]string{"staging_id": "abcdef01"}, false},
		{"a field the line lacks", keptOfferDroppedLine, map[string]string{"outcome": "expired"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := (lineWant{c.msg, c.fields}).matches(line); got != c.want {
				t.Errorf("logLine = %t, want %t", got, c.want)
			}
		})
	}
}

func TestStagedAgain(t *testing.T) {
	id := logid.Short(gapMsgID)
	const first, second = "1111aaaa…", "2222bbbb…"
	served := func(msgID, outcome, staging string) string {
		return strings.TrimSuffix(shortIDLine(fileServedLine, "sibling_peer_id", peerDevB, "msg_id", msgID, "outcome", outcome)+"", "\n") + " staging_id=" + staging
	}
	dropped := func(staging string) string {
		return strings.TrimSuffix(shortIDLine(keptOfferDroppedLine, "msg_id", gapMsgID), "\n") + " staging_id=" + staging + " outcome=expired"
	}
	cases := []struct {
		name      string
		lines     []string
		found     bool
		want      string
		failsWith string
	}{
		{"served once, not dropped yet", []string{served(gapMsgID, "served", first)}, false, "", ""},
		{"dropped, not served again yet", []string{served(gapMsgID, "served", first), dropped(first)}, false, "", ""},
		{"a new staging after the drop", []string{served(gapMsgID, "served", first), dropped(first), served(gapMsgID, "served", second)}, true, second, ""},
		{"the kept offer resent after the drop", []string{served(gapMsgID, "served", first), dropped(first), served(gapMsgID, "resent", first)}, true, "", "outlived its hour"},
		{"the first staging again after the drop", []string{served(gapMsgID, "served", first), dropped(first), served(gapMsgID, "served", first)}, true, "", "want a new one"},
		{"another file served after the drop", []string{served(gapMsgID, "served", first), dropped(first), served(otherMsgID, "served", second)}, false, "", ""},
		{"another staging of the file dropped", []string{served(gapMsgID, "served", first), dropped(second), served(gapMsgID, "served", second)}, false, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, found, err := stagedAgain(c.lines, id, first)
			if found != c.found {
				t.Errorf("found = %t, want %t", found, c.found)
			}
			switch {
			case c.failsWith == "" && err != nil:
				t.Errorf("want no failure, got %v", err)
			case c.failsWith != "" && (err == nil || !strings.Contains(err.Error(), c.failsWith)):
				t.Errorf("want a failure naming %q, got %v", c.failsWith, err)
			case got != c.want:
				t.Errorf("staging = %q, want %q", got, c.want)
			}
		})
	}
}

func TestNoFetchYet(t *testing.T) {
	id := logid.Short(gapMsgID)
	const staging = "1111aaaa…"
	ask := strings.TrimSuffix(shortIDLine(siblingFetchAskLine, "msg_id", gapMsgID), "\n") + " staging_id=" + staging
	otherAsk := strings.TrimSuffix(shortIDLine(siblingFetchAskLine, "msg_id", otherMsgID), "\n") + " staging_id=3333cccc…"
	begin := "time=2026-10-01T00:00:00Z level=DEBUG msg=\"" + haomadFetchBeginLine + "\" msg_id=" + staging + " peer_id=" + logid.Short(peerDevA)
	otherBegin := "time=2026-10-01T00:00:00Z level=DEBUG msg=\"" + haomadFetchBeginLine + "\" msg_id=3333cccc… peer_id=" + logid.Short(peerDevA)
	cases := []struct {
		name, haoma, haomad, failsWith string
	}{
		{"nothing fetched", otherAsk + "\n", otherBegin + "\n", ""},
		{"haoma asked haomad for it", ask + "\n", "", "the hold did not hold"},
		{"haomad began a fetch of the staging", "", begin + "\n", "something other than the take"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := writeRigLogs(t, c.haoma, c.haomad)
			err := noFetchYet(r, id, staging)
			switch {
			case c.failsWith == "" && err != nil:
				t.Errorf("want no failure, got %v", err)
			case c.failsWith != "" && (err == nil || !strings.Contains(err.Error(), c.failsWith)):
				t.Errorf("want a failure naming %q, got %v", c.failsWith, err)
			}
		})
	}
}

func TestAssertFileAcrossTheHourReportsEveryVerdict(t *testing.T) {
	devA, devB := &rig{name: "dev-a", logLevel: "info"}, &rig{name: "dev-b", logLevel: "info"}
	got := assertFileAcrossTheHour(devA, devB, &rig{name: "peer"}, "", "")
	if len(got) != len(crossHourVerdicts) {
		t.Fatalf("%d verdicts, want %d", len(got), len(crossHourVerdicts))
	}
	for i, c := range got {
		if c.name != crossHourVerdicts[i] {
			t.Errorf("verdict %d is %q, want %q", i, c.name, crossHourVerdicts[i])
		}
		if c.err == nil || !strings.Contains(c.err.Error(), "-log-level") {
			t.Errorf("verdict %q = %v, want not asserted for the log level", c.name, c.err)
		}
	}
	if devB.holdGapFillFetch || devA.gapOfferTTL != "" {
		t.Error("a case that ran nothing left a setting on a rig")
	}
}

type relaunchCall struct {
	rig    string
	hold   bool
	ignore bool
	ttl    string
}

func fakeRelaunch(calls *[]relaunchCall, fail map[int]bool) relaunchFunc {
	return func(r *rig, set func(*rig)) error {
		set(r)
		*calls = append(*calls, relaunchCall{r.name, r.holdGapFillFetch, r.ignoreSiblingOffer, r.gapOfferTTL})
		if fail[len(*calls)] {
			return errors.New("haoma did not start")
		}
		return nil
	}
}

func TestCrossHourSettings(t *testing.T) {
	ttl := crossHourOfferTTL.String()
	cases := []struct {
		name      string
		runIgnore bool
		fail      map[int]bool
		drive     func(s *crossHourSettings)
		want      []relaunchCall
		restoreOK bool
	}{
		{"nothing on, nothing put back", false, nil,
			func(s *crossHourSettings) {},
			nil, true},
		{"both on, then both back, dev-b first", false, nil,
			func(s *crossHourSettings) { _ = s.shortHourOnA(); _ = s.holdOnB() },
			[]relaunchCall{{"dev-a", false, false, ttl}, {"dev-b", true, true, ""}, {"dev-b", false, false, ""}, {"dev-a", false, false, ""}}, true},
		{"the run's ignore setting is kept", true, nil,
			func(s *crossHourSettings) { _ = s.shortHourOnA(); _ = s.holdOnB() },
			[]relaunchCall{{"dev-a", false, true, ttl}, {"dev-b", true, true, ""}, {"dev-b", false, true, ""}, {"dev-a", false, true, ""}}, true},
		{"dev-b failed half way into the hold, and is put back", false, map[int]bool{2: true},
			func(s *crossHourSettings) { _ = s.shortHourOnA(); _ = s.holdOnB() },
			[]relaunchCall{{"dev-a", false, false, ttl}, {"dev-b", true, true, ""}, {"dev-b", false, false, ""}, {"dev-a", false, false, ""}}, true},
		{"dev-a back in the middle is not put back twice", false, nil,
			func(s *crossHourSettings) { _ = s.shortHourOnA(); _ = s.holdOnB(); _ = s.restoreA() },
			[]relaunchCall{{"dev-a", false, false, ttl}, {"dev-b", true, true, ""}, {"dev-a", false, false, ""}, {"dev-b", false, false, ""}}, true},
		{"a restore that failed in the middle is tried again", false, map[int]bool{3: true},
			func(s *crossHourSettings) { _ = s.shortHourOnA(); _ = s.holdOnB(); _ = s.restoreB() },
			[]relaunchCall{{"dev-a", false, false, ttl}, {"dev-b", true, true, ""}, {"dev-b", false, false, ""}, {"dev-b", false, false, ""}, {"dev-a", false, false, ""}}, true},
		{"a failed restore of dev-b still puts back dev-a, and says so", false, map[int]bool{3: true},
			func(s *crossHourSettings) { _ = s.shortHourOnA(); _ = s.holdOnB() },
			[]relaunchCall{{"dev-a", false, false, ttl}, {"dev-b", true, true, ""}, {"dev-b", false, false, ""}, {"dev-a", false, false, ""}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			devA := &rig{name: "dev-a", ignoreSiblingOffer: c.runIgnore}
			devB := &rig{name: "dev-b", ignoreSiblingOffer: c.runIgnore}
			var calls []relaunchCall
			s := newCrossHourSettings(devA, devB, fakeRelaunch(&calls, c.fail))
			c.drive(s)
			err := s.restoreAll()
			if (err == nil) != c.restoreOK {
				t.Errorf("restoreAll err = %v, want ok=%t", err, c.restoreOK)
			}
			if len(calls) != len(c.want) {
				t.Fatalf("relaunches %+v, want %+v", calls, c.want)
			}
			for i := range calls {
				if calls[i] != c.want[i] {
					t.Errorf("relaunch %d = %+v, want %+v", i+1, calls[i], c.want[i])
				}
			}
			if devA.gapOfferTTL != "" || devB.holdGapFillFetch || devB.ignoreSiblingOffer != c.runIgnore {
				t.Errorf("after restore: ttl %q hold %t ignore %t; want none, false, %t", devA.gapOfferTTL, devB.holdGapFillFetch, devB.ignoreSiblingOffer, c.runIgnore)
			}
		})
	}
}

func TestLogMarkSeesANewLogOnTheSameInode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "haoma.log")
	rewrite := func(text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rewrite("2026-10-11T13:52:09Z before 1\n2026-10-11T13:52:09Z before 2\n")
	m := markLog(path)

	rewrite("short\n")
	if got, err := m.lines(); err != nil || strings.Join(got, "|") != "short" {
		t.Errorf("shorter new log: lines = %q, %v; want [short]", got, err)
	}
	rewrite("2026-10-11T14:00:00Z a new log, longer than the offset the mark holds\n")
	if got, err := m.lines(); err != nil || len(got) != 1 || !strings.Contains(got[0], "a new log") {
		t.Errorf("longer new log: lines = %q, %v; want the whole new log", got, err)
	}
}

func TestLogMarkReadsOnlyLinesAfterIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "haoma.log")
	write := func(p, text string, flag int) {
		t.Helper()
		f, err := os.OpenFile(p, flag|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(text); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	rotate := func() {
		t.Helper()
		if err := os.Rename(path, path+".prev"); err != nil {
			t.Fatal(err)
		}
	}
	check := func(m logMark, want ...string) {
		t.Helper()
		got, err := m.lines()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("lines = %q, want %q", got, want)
		}
	}

	write(path, "before 1\nbefore 2\n", os.O_TRUNC)
	m := markLog(path)
	check(m)
	write(path, "after 1\n", os.O_APPEND)
	check(m, "after 1")

	rotate()
	write(path, "new log line one, longer than the marked offset\n", os.O_TRUNC)
	check(m, "after 1", "new log line one, longer than the marked offset")

	rotate()
	write(path, "third file\n", os.O_TRUNC)
	check(m, "third file")

	other := filepath.Join(dir, "later.log")
	m2 := markLog(other)
	check(m2)
	write(other, "first\n", os.O_TRUNC)
	check(m2, "first")
}

func TestStartChecksReadOnlyTheNewProcess(t *testing.T) {
	startLineTimeout = 0
	t.Cleanup(func() { startLineTimeout = pathLineTimeout })
	at := func(msg string) string { return "time=2026-10-01T00:00:00Z level=WARN msg=\"" + msg + "\"" }
	listening := at(ipcListeningLine)
	r := &rig{name: "dev-b", root: t.TempDir()}
	log := filepath.Join(r.root, "haoma.log")
	writeLog := func(lines ...string) {
		t.Helper()
		f, err := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(strings.Join(lines, "\n") + "\n")
		f.Close()
	}

	writeLog(at(holdGapFillFetchOnLine), listening)
	m := haomaLogMark(r)
	if err := awaitStartedWithout(r, m, holdGapFillFetchOnLine); err == nil || !strings.Contains(err.Error(), ipcListeningLine) {
		t.Errorf("no new start yet: err = %v, want the listening line named", err)
	}
	if err := awaitStartLines(r, m, startLine{holdGapFillFetchOnLine, ""}); err == nil {
		t.Error("the old process's start line counted for the new one")
	}
	writeLog(at("data dir"), listening)
	if err := awaitStartedWithout(r, m, holdGapFillFetchOnLine); err != nil {
		t.Errorf("a start without the fault: %v", err)
	}

	m = haomaLogMark(r)
	writeLog(at(holdGapFillFetchOnLine), listening)
	if err := awaitStartedWithout(r, m, holdGapFillFetchOnLine); err == nil || !strings.Contains(err.Error(), "took off") {
		t.Errorf("a start with the fault: err = %v, want it named", err)
	}
	if err := awaitStartLines(r, m, startLine{holdGapFillFetchOnLine, ""}); err != nil {
		t.Errorf("the new process's start line: %v", err)
	}
}

func TestShellCaseSettingsNotice(t *testing.T) {
	env := map[string]string{holdGapFillFetchEnv: "1", gapOfferTTLEnv: ""}
	got := shellCaseSettingsNotice(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if len(got) != 1 || !strings.Contains(got[0], holdGapFillFetchEnv) || !strings.Contains(got[0], "file-gapfill-expiry") {
		t.Errorf("notice = %q, want one line naming %s and the case", got, holdGapFillFetchEnv)
	}
	if got := shellCaseSettingsNotice(func(string) (string, bool) { return "", false }); len(got) != 0 {
		t.Errorf("notice with nothing in the shell = %q", got)
	}
}

func TestAwaitStagedAgainFindsAServeWrittenAtTheRestart(t *testing.T) {
	r := &rig{name: "dev-a", root: t.TempDir()}
	path := filepath.Join(r.root, "haoma.log")
	appendLines := func(lines ...string) {
		t.Helper()
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	id := logid.Short(gapMsgID)
	const first, second = "fdd6e6f9…", "4d835e35…"
	served := func(staging string) string {
		return strings.TrimSuffix(shortIDLine(fileServedLine, "sibling_peer_id", peerDevB, "msg_id", gapMsgID, "outcome", "served"), "\n") + " staging_id=" + staging
	}
	dropped := strings.TrimSuffix(shortIDLine(keptOfferDroppedLine, "msg_id", gapMsgID), "\n") + " staging_id=" + first + " outcome=expired"
	listening := "time=2026-10-02T02:24:22.482Z level=INFO msg=\"" + ipcListeningLine + "\""

	appendLines("time=2026-10-02T02:22:41.500Z level=INFO msg=\"an earlier line\"")
	beforeSend := haomaLogMark(r)
	appendLines(served(first), dropped)
	afterDrop := haomaLogMark(r)

	appendLines(listening, served(second))

	got, err := awaitStagedAgain(r, beforeSend, id, first, 0)
	if err != nil || got != second {
		t.Fatalf("from the mark before the send: %q, %v; want %q", got, err, second)
	}
	if _, err := awaitStagedAgain(r, afterDrop, id, first, 0); err == nil {
		t.Fatal("from a mark after the drop the serve was found; the drop line is not in what it reads")
	}

	r2 := &rig{name: "dev-a", root: t.TempDir()}
	path = filepath.Join(r2.root, "haoma.log")
	appendLines("time=2026-10-02T02:22:41.500Z level=INFO msg=\"an earlier line\"")
	m := haomaLogMark(r2)
	resent := strings.Replace(served(first), "outcome=served", "outcome=resent", 1)
	appendLines(served(first), dropped, listening, resent)
	if _, err := awaitStagedAgain(r2, m, id, first, 0); err == nil || !strings.Contains(err.Error(), "outlived its hour") {
		t.Fatalf("a resend after the drop: err = %v, want it named", err)
	}
}
