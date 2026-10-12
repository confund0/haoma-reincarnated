package main

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"haoma-frontend/internal/logid"
)

const (
	haomaSaltSyncSource   = "../haoma/destroy_salt_sync.go"
	haomaDestroyPINSource = "../haoma/destroy_pin.go"
)

func TestSaltHealNamesAndLinesMatchHaoma(t *testing.T) {
	for _, c := range []struct{ name, want string }{
		{"dropFirstDestroySaltEnv", dropFirstDestroySaltEnv},
		{"dropFirstDestroySaltOnLine", dropFirstDestroySaltOnLine},
		{"destroySaltDroppedLine", destroySaltDroppedLine},
	} {
		if got := stringConst(t, haomaFaultsSource, c.name); got != c.want {
			t.Errorf("haoma's %s is %q, the rig uses %q", c.name, got, c.want)
		}
	}
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("cannot read %s: %v", p, err)
		}
		return string(b)
	}
	sync, pin, faults := read(haomaSaltSyncSource), read(haomaDestroyPINSource), read(haomaFaultsSource)
	for _, c := range []struct{ src, file, want string }{
		{sync, haomaSaltSyncSource, strconv.Quote(saltSentAgainLine)},
		{sync, haomaSaltSyncSource, strconv.Quote(saltHeldLine)},

		{sync, haomaSaltSyncSource, `logger := slog.With(slog.String("sibling_peer_id", sibling))`},

		{pin, haomaDestroyPINSource, "destroySaltDropped(logger)"},
		{faults, haomaFaultsSource, "logger.Warn(destroySaltDroppedLine,"},
	} {
		if !strings.Contains(c.src, c.want) {
			t.Errorf("%s no longer holds %s", c.file, c.want)
		}
	}
}

func TestHaomaEnvClearsTheSaltFault(t *testing.T) {
	t.Setenv(dropFirstDestroySaltEnv, "1")
	envOf := func(r *rig) string {
		v := ""
		for _, kv := range childEnv(r.haomaEnv()) {
			if k, val, _ := strings.Cut(kv, "="); k == dropFirstDestroySaltEnv {
				v = val
			}
		}
		return v
	}
	if got := envOf(&rig{name: "dev-b"}); got != "" {
		t.Errorf("off: %s=%q, want empty whatever the shell holds", dropFirstDestroySaltEnv, got)
	}
	if got := envOf(&rig{name: "dev-b", dropFirstDestroySalt: true}); got != "1" {
		t.Errorf("on: %s=%q, want 1", dropFirstDestroySaltEnv, got)
	}
	notice := shellCaseSettingsNotice(func(k string) (string, bool) {
		if k == dropFirstDestroySaltEnv {
			return "1", true
		}
		return "", false
	})
	if len(notice) != 1 || !strings.Contains(notice[0], dropFirstDestroySaltEnv) || !strings.Contains(notice[0], "destroy-pin") {
		t.Errorf("notice = %q, want one line naming %s and the group", notice, dropFirstDestroySaltEnv)
	}
}

func TestSaltSentAgainLineMatchesTheSibling(t *testing.T) {
	const sib = "2d655ac4edc76cc32cdecccec9bce372"
	line := `time=2026-10-02T00:00:00Z level=INFO msg="` + saltSentAgainLine + `" service=haoma sibling_peer_id=` +
		logid.Short(sib) + ` other_holds_a_salt=false`
	if !(lineWant{saltSentAgainLine, map[string]string{"sibling_peer_id": logid.Short(sib)}}).matches(line) {
		t.Errorf("the line for %s does not match: %s", sib, line)
	}
	if (lineWant{saltSentAgainLine, map[string]string{"sibling_peer_id": logid.Short("7ae6e41b00000000000000000000beef")}}).matches(line) {
		t.Error("the line matched another sibling")
	}
}

func TestSaltHealHalvesReadTheLogs(t *testing.T) {
	saltHealTimeout = 0
	t.Cleanup(func() { saltHealTimeout = saltCrossTimeout })
	const aSeesB, bSeesA = "0000000000000000000000000000b0b0", "0000000000000000000000000000a0a0"
	at := func(level, msg, rest string) string {
		return "time=2026-10-02T00:00:00Z level=" + level + " msg=" + strconv.Quote(msg) + " service=haoma" + rest
	}
	newRig := func(name string) *rig { return &rig{name: name, root: t.TempDir()} }
	appendLog := func(r *rig, lines ...string) {
		t.Helper()
		f, err := os.OpenFile(r.root+"/haoma.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(strings.Join(lines, "\n") + "\n")
		f.Close()
	}
	dropped := at("WARN", destroySaltDroppedLine, " sibling_peer_id="+logid.Short(bSeesA))
	sentAgain := at("INFO", saltSentAgainLine, " sibling_peer_id="+logid.Short(aSeesB)+" other_holds_a_salt=false")
	sentAgainByB := at("INFO", saltSentAgainLine, " sibling_peer_id="+logid.Short(bSeesA)+" other_holds_a_salt=false")
	held := at("DEBUG", saltHeldLine, " sibling_peer_id="+logid.Short(bSeesA))

	for _, c := range []struct {
		name       string
		onA, onB   []string
		wantErrHas string
	}{
		{"healed", []string{sentAgain}, []string{dropped}, ""},
		{"heal off", nil, []string{dropped}, "never sent its salt again"},
		{"nothing lost", []string{sentAgain}, nil, "never dropped"},
	} {
		t.Run("sent again/"+c.name, func(t *testing.T) {
			devA, devB := newRig("dev-a"), newRig("dev-b")
			markA, markB := haomaLogMark(devA), haomaLogMark(devB)
			appendLog(devA, append([]string{"x"}, c.onA...)...)
			appendLog(devB, append([]string{"x"}, c.onB...)...)
			err := awaitSaltSentAgain(devA, devB, markA, markB, aSeesB)
			switch {
			case c.wantErrHas == "" && err != nil:
				t.Errorf("err = %v, want none", err)
			case c.wantErrHas != "" && (err == nil || !strings.Contains(err.Error(), c.wantErrHas)):
				t.Errorf("err = %v, want one naming %q", err, c.wantErrHas)
			}
		})
	}

	for _, c := range []struct {
		name       string
		onB        []string
		wantErrHas string
	}{
		{"matched", []string{held}, ""},
		{"sent again with nothing lost", []string{held, sentAgainByB}, "sent its salt again 1 time"},
		{"no report reached it", nil, "measured nothing"},
	} {
		t.Run("not sent again/"+c.name, func(t *testing.T) {
			devA, devB := newRig("dev-a"), newRig("dev-b")
			markB := haomaLogMark(devB)
			appendLog(devB, append([]string{"x"}, c.onB...)...)
			err := awaitNoSecondSalt(devA, devB, markB, bSeesA)
			switch {
			case c.wantErrHas == "" && err != nil:
				t.Errorf("err = %v, want none", err)
			case c.wantErrHas != "" && (err == nil || !strings.Contains(err.Error(), c.wantErrHas)):
				t.Errorf("err = %v, want one naming %q", err, c.wantErrHas)
			}
		})
	}
}
