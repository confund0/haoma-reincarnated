package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var errTest = errors.New("relaunch failed in the test")

const haomaContactDeleteSource = "../haoma/contact_delete.go"

func TestContactDeleteNamesAndLinesMatchHaoma(t *testing.T) {
	for _, c := range []struct{ src, name, want string }{
		{haomaFaultsSource, "dropContactDeletePushEnv", dropContactDeletePushEnv},
		{haomaFaultsSource, "dropContactDeletePushOnLine", dropContactDeletePushOnLine},
		{haomaContactDeleteSource, "personDeletedLine", personDeletedLine},
		{haomaContactDeleteSource, "contactDeletePushedLine", contactDeletePushedLine},
		{haomaFaultsSource, "contactDeletePushDroppedLine", contactDeletePushDroppedLine},
		{"../haoma/backfill.go", "backfillContactsAppliedLine", backfillContactsAppliedLine},
	} {
		if got := stringConst(t, c.src, c.name); got != c.want {
			t.Errorf("haoma's %s is %q, the rig uses %q", c.name, got, c.want)
		}
	}
}

func TestHaomaEnvClearsTheContactDeleteFault(t *testing.T) {
	t.Setenv(dropContactDeletePushEnv, "1")
	envOf := func(r *rig) string {
		v := ""
		for _, kv := range childEnv(r.haomaEnv()) {
			if k, val, _ := strings.Cut(kv, "="); k == dropContactDeletePushEnv {
				v = val
			}
		}
		return v
	}
	if got := envOf(&rig{name: "dev-a"}); got != "" {
		t.Errorf("off: %s=%q, want empty whatever the shell holds", dropContactDeletePushEnv, got)
	}
	if got := envOf(&rig{name: "dev-a", dropContactDeletePush: true}); got != "1" {
		t.Errorf("on: %s=%q, want 1", dropContactDeletePushEnv, got)
	}
	notice := shellCaseSettingsNotice(func(k string) (string, bool) {
		if k == dropContactDeletePushEnv {
			return "1", true
		}
		return "", false
	})
	if len(notice) != 1 || !strings.Contains(notice[0], "contact-delete") {
		t.Errorf("notice = %q, want one line naming the group", notice)
	}
}

func TestContactDeleteFaultRestore(t *testing.T) {
	var calls []bool
	devA := &rig{name: "dev-a"}
	s := &contactDeleteFault{devA: devA, relaunch: func(r *rig, set func(*rig)) error {
		set(r)
		calls = append(calls, r.dropContactDeletePush)
		return errTest
	}}
	if err := s.restore(); err != nil || len(calls) != 0 {
		t.Fatalf("restore before turnOn: err=%v calls=%v", err, calls)
	}
	s.on = true
	if err := s.restore(); err == nil || !s.on {
		t.Fatalf("a failed restore cleared the mark: err=%v on=%v", err, s.on)
	}
	if len(calls) != 1 || calls[0] {
		t.Fatalf("restore relaunched with %v, want the fault off", calls)
	}
}

func cdLine(msg string, fields string) string {
	return `time=2026-10-02T11:12:26.307Z level=DEBUG msg="` + msg + `" service=haoma ` + fields
}

func TestContactDeleteSweepRoundVerdicts(t *testing.T) {
	refused := cdLine(backfillContactsAppliedLine, "sibling_peer_id=1412459c… received=1 merged=0 refused_deleted=1")
	merged := cdLine(backfillContactsAppliedLine, "sibling_peer_id=1412459c… received=1 merged=1 refused_deleted=0")
	bySweep := cdLine(personDeletedLine, "fingerprint=050d4109… origin=sibling_sweep deleted_at=1")
	byPush := cdLine(personDeletedLine, "fingerprint=050d4109… origin=sibling_push deleted_at=1")

	for _, c := range []struct {
		name        string
		aLines, b   []string
		wantRefusal roundVerdict
		wantCarry   roundVerdict
	}{
		{"dev-a refused, dev-b still holds", []string{refused}, nil, verdictPass, verdictPending},
		{"dev-a refused, then dev-b took it by a sweep", []string{refused}, []string{bySweep}, verdictPass, verdictPass},
		{"dev-b's own sweep came first", []string{merged}, []string{bySweep}, verdictNotReached, verdictPass},
		{"nothing yet", nil, nil, verdictPending, verdictPending},
		{"a batch with no refusal while dev-b still holds", []string{merged}, nil, verdictPending, verdictPending},
		{"the push was not dropped", nil, []string{byPush}, verdictNotReached, verdictFail},
	} {
		if got, why := refusalVerdict(c.aLines, c.b); got != c.wantRefusal {
			t.Errorf("%s: refusal verdict %d (%s), want %d", c.name, got, why, c.wantRefusal)
		}
		if got, why := carryVerdict(c.b); got != c.wantCarry {
			t.Errorf("%s: carry verdict %d (%s), want %d", c.name, got, why, c.wantCarry)
		}
	}
}

func TestAwaitVerdict(t *testing.T) {
	if err := awaitVerdict(time.Second, func() (roundVerdict, string, error) { return verdictPass, "", nil }); err != nil {
		t.Fatalf("pass: %v", err)
	}
	err := awaitVerdict(time.Second, func() (roundVerdict, string, error) { return verdictNotReached, "dev-b went first", nil })
	if err == nil || !strings.HasPrefix(err.Error(), "not reached: dev-b went first") {
		t.Fatalf("not reached: %v", err)
	}
	if err := awaitVerdict(0, func() (roundVerdict, string, error) { return verdictPending, "still waiting", nil }); err == nil || !strings.Contains(err.Error(), "still waiting") {
		t.Fatalf("timeout: %v", err)
	}
}
