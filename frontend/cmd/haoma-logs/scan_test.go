package main

import (
	"strings"
	"testing"
)

func TestParseLineReadsQuotedAndBareValues(t *testing.T) {
	raw := `time=2026-08-15T10:12:35.893+01:00 level=DEBUG msg="inbox decrypted" ` +
		`service=haoma envelope_id=bbd889ae… peer_id=1b3f1f84… ` +
		`mime="text/plain; charset=utf-8" image_mode="" ` +
		`err="backendapi: {\"error\":\"fetch worker not running\"}"`
	e := parseLine(raw)
	if !e.hasTS {
		t.Fatal("the time was not read")
	}
	if e.level != "DEBUG" {
		t.Errorf("level = %q, want DEBUG", e.level)
	}
	if e.msg != "inbox decrypted" {
		t.Errorf("msg = %q, want %q", e.msg, "inbox decrypted")
	}
	if got := e.get("mime"); got != "text/plain; charset=utf-8" {
		t.Errorf("mime = %q, want the whole quoted value", got)
	}
	if got := e.get("image_mode"); got != "" {
		t.Errorf("image_mode = %q, want the empty value", got)
	}
	if got := e.get("err"); !strings.Contains(got, `{"error":"fetch worker not running"}`) {
		t.Errorf("err = %q, want the escaped quotes read back", got)
	}

	if e.get("charset") != "" {
		t.Error("a key inside a quoted value was read as an attribute")
	}
	if e.get("peer_id") != "1b3f1f84…" {
		t.Errorf("peer_id = %q", e.get("peer_id"))
	}
}

func TestParseLineKeepsALineThatIsNotInLogShape(t *testing.T) {
	raw := "goroutine 1 [running]:"
	e := parseLine(raw)
	if e.raw != raw {
		t.Errorf("raw = %q, want the line unchanged", e.raw)
	}
	if e.hasTS {
		t.Error("a line with no time reported one")
	}
}

func TestMergeOrderAcrossFiles(t *testing.T) {
	root := writeRun(t, map[string]string{
		"alice-vault/haomad.log": lines(
			`time=2026-08-15T10:00:01.000+01:00 level=INFO msg="one" service=haomad`,
			`time=2026-08-15T10:00:04.000+01:00 level=INFO msg="four" service=haomad`,
			`time=2026-08-15T10:00:06.000+01:00 level=INFO msg="six" service=haomad`,
		),
		"alice-vault/haoma.log": lines(
			`time=2026-08-15T10:00:02.000+01:00 level=INFO msg="two" service=haoma`,
			`time=2026-08-15T10:00:05.000+01:00 level=INFO msg="five" service=haoma`,
		),
		"eddie-vault/haomad.log": lines(
			`time=2026-08-15T10:00:03.000+01:00 level=INFO msg="three" service=haomad`,
			`time=2026-08-15T10:00:07.000+01:00 level=INFO msg="seven" service=haomad`,
		),
	})
	got := collect(t, sourcesFor(t, root))

	wantMsg := []string{"one", "two", "three", "four", "five", "six", "seven"}
	wantFrom := []string{
		"alice/haomad", "alice/haoma", "eddie/haomad",
		"alice/haomad", "alice/haoma", "alice/haomad", "eddie/haomad",
	}
	if len(got) != len(wantMsg) {
		t.Fatalf("read %d lines, want %d", len(got), len(wantMsg))
	}
	for i := range got {
		if got[i].msg != wantMsg[i] {
			t.Errorf("line %d is %q, want %q", i, got[i].msg, wantMsg[i])
		}
		from := got[i].account + "/" + got[i].process
		if from != wantFrom[i] {
			t.Errorf("line %d came from %q, want %q", i, from, wantFrom[i])
		}
	}
}

func TestLineWithNoTimeStaysWithTheLineBefore(t *testing.T) {
	root := writeRun(t, map[string]string{
		"alice-vault/haomad.log": lines(
			`time=2026-08-15T10:00:01.000+01:00 level=INFO msg="before" service=haomad`,
			`time=2026-08-15T10:00:03.000+01:00 level=ERROR msg="panic: bad" service=haomad`,
			`goroutine 1 [running]:`,
			`	main.go:1 +0x1`,
		),
		"eddie-vault/haomad.log": lines(
			`time=2026-08-15T10:00:02.000+01:00 level=INFO msg="between" service=haomad`,
			`time=2026-08-15T10:00:04.000+01:00 level=INFO msg="after" service=haomad`,
		),
	})
	got := collect(t, sourcesFor(t, root))
	want := []string{"before", "between", "panic: bad", "goroutine 1 [running]:", "\tmain.go:1 +0x1", "after"}
	if len(got) != len(want) {
		t.Fatalf("read %d lines, want %d", len(got), len(want))
	}
	for i := range got {
		text := got[i].msg
		if text == "" {
			text = got[i].raw
		}
		if text != want[i] {
			t.Errorf("line %d is %q, want %q", i, text, want[i])
		}
	}
}

func TestDiscoverNamesAccountsAndProcesses(t *testing.T) {
	root := writeRun(t, map[string]string{
		"alice-vault/haomad.log":       "",
		"alice-vault/haomad.log.prev":  "",
		"alice-vault/haoma-text.log":   "",
		"alice-vault/haoma.stderr":     "",
		"alice-vault/backend/data.sst": "",
		"alice-vault/vault.enc":        "",
		"eddie-vault/haoma.log":        "",
	})
	srcs := sourcesFor(t, root)
	var got []string
	for _, s := range srcs {
		tag := s.account + "/" + s.process
		if s.rotated {
			tag += " rotated"
		}
		if s.stderr {
			tag += " stderr"
		}
		got = append(got, tag)
	}
	want := []string{
		"alice/haoma stderr",
		"alice/haoma-text",
		"alice/haomad",
		"alice/haomad rotated",
		"eddie/haoma",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("found %v, want %v", got, want)
	}
}

func TestPickLeavesOutRotatedAndStderrUnlessAsked(t *testing.T) {
	srcs := []source{
		{account: "a", process: "haomad", file: "haomad.log"},
		{account: "a", process: "haomad", file: "haomad.log.prev", rotated: true},
		{account: "a", process: "haomad", file: "haomad.stderr", stderr: true},
	}
	if got := pick(srcs, false, false); len(got) != 1 {
		t.Errorf("default picked %d files, want 1", len(got))
	}
	if got := pick(srcs, true, false); len(got) != 2 {
		t.Errorf("with rotated picked %d files, want 2", len(got))
	}
	if got := pick(srcs, true, true); len(got) != 3 {
		t.Errorf("with rotated and stderr picked %d files, want 3", len(got))
	}
	if got := logsOnly(srcs); len(got) != 2 {
		t.Errorf("logsOnly picked %d files, want 2", len(got))
	}
}

func lines(s ...string) string { return strings.Join(s, "\n") + "\n" }

func TestHarnessRunShapeIsReadTheSameWay(t *testing.T) {
	root := writeRun(t, map[string]string{
		"dev-a/haomad.log":    "",
		"dev-a/haoma.log":     "",
		"dev-a/haomad.stderr": "",
		"dev-b/haomad.log":    "",
		"dev-b/haoma.log":     "",
		"peer/haomad.log":     "",
		"peer/haoma.log":      "",
	})
	srcs := sourcesFor(t, root)
	got := countAccounts(srcs)
	if strings.Join(got, ",") != "dev-a,dev-b,peer" {
		t.Errorf("accounts = %v, want [dev-a dev-b peer]", got)
	}
}

func TestARunOfItsOwnIsNotReadFromTheDirectoryAboveIt(t *testing.T) {
	root := writeRun(t, map[string]string{
		"dev-a/haomad.log":                      "",
		"dev-b/haomad.log":                      "",
		"fail-20260814-181303/dev-a/haomad.log": "",
		"fail-20260814-181303/peer/haomad.log":  "",
		"fail-20260813-084741/dev-a/haomad.log": "",
	})
	srcs := sourcesFor(t, root)
	got := countAccounts(srcs)
	if strings.Join(got, ",") != "dev-a,dev-b" {
		t.Errorf("accounts = %v, want only the live ones [dev-a dev-b]", got)
	}
	for _, s := range srcs {
		if strings.Contains(s.path, "fail-") {
			t.Errorf("read %s, which belongs to a run of its own", s.path)
		}
	}

	nested := nestedRunDirs(root)
	if len(nested) != 2 {
		t.Fatalf("found %d directories holding runs of their own, want 2: %v", len(nested), nested)
	}
	for _, want := range []string{"fail-20260813-084741", "fail-20260814-181303"} {
		var seen bool
		for _, n := range nested {
			if strings.HasSuffix(n, want) {
				seen = true
			}
		}
		if !seen {
			t.Errorf("%s was not named", want)
		}
	}
}

func TestNothingBelowTheAccountLevelIsRead(t *testing.T) {
	root := writeRun(t, map[string]string{
		"alice-vault/haomad.log":           "",
		"alice-vault/backend/badger.log":   "",
		"alice-vault/frontend/inner/x.log": "",
	})
	srcs := sourcesFor(t, root)
	if len(srcs) != 1 {
		t.Fatalf("read %d files, want only the account's own log", len(srcs))
	}
	if !strings.HasSuffix(srcs[0].path, "alice-vault/haomad.log") {
		t.Errorf("read %s", srcs[0].path)
	}
}

func TestALooseLogBesideAccountDirectoriesIsNotAnAccount(t *testing.T) {
	root := writeRun(t, map[string]string{
		"dev-a/haomad.log":          "",
		"dev-b/haomad.log":          "",
		"trace-20260814-181303.log": "",
		"trace-20260813-084741.log": "",
	})
	got := countAccounts(sourcesFor(t, root))
	if strings.Join(got, ",") != "dev-a,dev-b" {
		t.Errorf("accounts = %v, want [dev-a dev-b] — a merged trace is not an account", got)
	}
}

func TestOneAccountsOwnDirectoryIsRead(t *testing.T) {
	root := writeRun(t, map[string]string{
		"haomad.log": "",
		"haoma.log":  "",
	})
	srcs := sourcesFor(t, root)
	if len(srcs) != 2 {
		t.Fatalf("read %d files, want 2", len(srcs))
	}
	for _, s := range srcs {
		if !s.loose {
			t.Errorf("%s was not read as lying directly in the directory", s.file)
		}
	}
}
