package tui

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"haoma-frontend/internal/ipc"
)

func TestJoinSecondsLeft(t *testing.T) {
	full := int(ipc.JoinerDialTimeout / time.Second)
	for _, c := range []struct {
		elapsed time.Duration
		want    int
	}{
		{0, full},
		{time.Millisecond, full},
		{time.Second, full - 1},
		{ipc.JoinerDialTimeout - 500*time.Millisecond, 1},
		{ipc.JoinerDialTimeout, 0},
		{ipc.JoinerDialTimeout + time.Hour, 0},
	} {
		if got := joinSecondsLeft(c.elapsed); got != c.want {
			t.Errorf("joinSecondsLeft(%v) = %d, want %d", c.elapsed, got, c.want)
		}
	}
}

func TestJoinWaitLine(t *testing.T) {
	if got, want := joinWaitLine(0), "Waiting for Tor: 120 seconds left."; got != want {
		t.Errorf("at the start: %q, want %q", got, want)
	}
	if got, want := joinWaitLine(ipc.JoinerDialTimeout-time.Second), "Waiting for Tor: 1 second left."; got != want {
		t.Errorf("in the last second: %q, want %q", got, want)
	}
	if got := joinWaitLine(ipc.JoinerDialTimeout); got != joinWaitNoAnswerLine {
		t.Errorf("at zero: %q, want %q", got, joinWaitNoAnswerLine)
	}
}

func dialingJoin(now func() time.Time) *linkFlow {
	f := &linkFlow{mode: flowJoin, phase: phaseRun, now: now,
		ownPIN: "1379", otherPIN: "2468", words: "acid acorn acre acts afar affix aged"}
	f.Say("contacting your other device")
	f.StartDialWait()
	return f
}

func TestLinkFlow_TheJoinCountsDown(t *testing.T) {
	start := time.Unix(1_000_000, 0)
	now := start
	f := dialingJoin(func() time.Time { return now })

	if log := f.Log(); !strings.Contains(log, "Waiting for Tor: 120 seconds left.") {
		t.Fatalf("no countdown at the start of the dial:\n%s", log)
	}
	now = start.Add(33 * time.Second)
	if log := f.Log(); !strings.Contains(log, "Waiting for Tor: 87 seconds left.") {
		t.Fatalf("the countdown did not follow the clock:\n%s", log)
	}
	f.EndDialWait()
	if log := f.Log(); strings.Contains(log, "Waiting for Tor") {
		t.Fatalf("the countdown outlived the answer:\n%s", log)
	}
}

func TestLinkFlow_RetryLaterKeepsTheForm(t *testing.T) {
	f := dialingJoin(nil)
	f.RetryLater(joinTorSlowLine, true)

	st := f.state()
	if st.phase != phaseCollect {
		t.Fatalf("phase = %v, want the collecting phase, which draws Proceed", st.phase)
	}
	if st.ownPIN != "1379" || st.otherPIN != "2468" || st.words != "acid acorn acre acts afar affix aged" {
		t.Fatalf("the form lost what was typed: own=%q other=%q words=%q", st.ownPIN, st.otherPIN, st.words)
	}
	if f.Failed() {
		t.Fatal("a run that can be tried again from its form was marked as ended badly")
	}
	if !strings.Contains(st.log, StyleTorSlow+joinTorSlowLine) {
		t.Fatalf("the log does not say why, in orange:\n%s", st.log)
	}
	if !strings.Contains(st.log, outcomeFailed) {
		t.Fatalf("the step that dialled is not marked as failed:\n%s", st.log)
	}
	if strings.Contains(st.log, "Waiting for Tor") {
		t.Fatalf("the countdown is still drawn on a run that is not dialling:\n%s", st.log)
	}
	for _, r := range spinnerFrames {
		if strings.ContainsRune(st.log, r) {
			t.Fatalf("the marker still turns on a run that waits for the person:\n%s", st.log)
		}
	}
}

func TestLinkFlow_RetryLaterLeavesOtherRunsAlone(t *testing.T) {
	offer := &linkFlow{mode: flowOffer, phase: phaseRun}
	offer.Say("publishing the invitation")
	offer.RetryLater(joinTorSlowLine, true)
	if _, phase := offer.snapshot(); phase != phaseRun {
		t.Fatalf("an offer was moved to phase %v", phase)
	}

	done := &linkFlow{mode: flowJoin, phase: phaseDone}
	done.RetryLater(joinTorSlowLine, true)
	if _, phase := done.snapshot(); phase != phaseDone {
		t.Fatalf("a finished join was moved to phase %v", phase)
	}
}

func TestLinkFlow_FinishLinkedIsNotAFailure(t *testing.T) {
	f := &linkFlow{mode: flowJoin, phase: phaseRun}
	f.Say("contacting your other device")
	f.Step(outcomeOK, "waiting for your other device")
	f.FinishLinked(linkedDetailsNotReceivedLine)

	if f.Failed() {
		t.Fatal("a run whose link came up was marked as ended badly")
	}
	st := f.state()
	if st.phase != phaseDone {
		t.Fatalf("phase = %v, want done", st.phase)
	}
	if strings.Contains(st.log, outcomeFailed) {
		t.Fatalf("the log marks a step FAILED on a run that linked:\n%s", st.log)
	}
	if !strings.Contains(st.log, "details not received yet") {
		t.Fatalf("the log does not say what is missing:\n%s", st.log)
	}
}

func TestJoinWording_MatchesThePhone(t *testing.T) {
	const phone = "../../../mobile/Android/app/src/main/java/io/haoma/calculator/messenger/JoinWait.kt"
	src, err := os.ReadFile(phone)
	if err != nil {
		t.Fatalf("read %s: %v", phone, err)
	}
	kt := string(src)
	for _, want := range []string{
		`"` + torSlowLine + `"`,
		`"` + joinTorSlowLine + `"`,
		`"` + joinWordsWrongLine + `"`,
		`"` + joinInvitationSpentLine + `"`,
		`"` + pairJoinNoAnswerLine + `"`,
		`"` + joinWaitNoAnswerLine + `"`,
		`" Cancel does not stop a join that was already sent."`,
		`"Waiting for Tor: 1 second left."`,
		`"Waiting for Tor: $left seconds left."`,
	} {
		if !strings.Contains(kt, want) {
			t.Errorf("%s does not hold %s", phone, want)
		}
	}

	const linkStore = "../../../mobile/Android/app/src/main/java/io/haoma/calculator/messenger/MessengerStore_DeviceLink.kt"
	src, err = os.ReadFile(linkStore)
	if err != nil {
		t.Fatalf("read %s: %v", linkStore, err)
	}

	phoneLine := strings.ReplaceAll(linkedDetailsNotReceivedLine, "this page", "this screen")
	joined := regexp.MustCompile(`"\s*\+\s*"`).ReplaceAllString(string(src), "")
	if !strings.Contains(joined, `"`+phoneLine+`"`) {
		t.Errorf("%s does not hold %q", linkStore, phoneLine)
	}
}

func TestJoinFailureLine(t *testing.T) {
	for _, c := range []struct {
		code string
		line string
	}{
		{ipc.ReasonOnionAcceptFailed, joinTorSlowLine},
		{ipc.ReasonBackendDown, joinTorSlowLine},
		{ipc.ReasonJoinWordsInvalid, joinWordsWrongLine},
		{ipc.ReasonJoinInvitationSpent, joinInvitationSpentLine},
	} {
		line, keep, ok := joinFailureLine(c.code)
		if !ok || !keep || line != c.line {
			t.Errorf("%s: (%q, %t, %t), want (%q, true, true)", c.code, line, keep, ok, c.line)
		}
	}
	for _, code := range []string{"", ipc.ReasonNoAnswer, "device_not_empty", "bad_invite"} {
		if _, _, ok := joinFailureLine(code); ok {
			t.Errorf("%q was given a join sentence it does not mean", code)
		}
	}
}

func TestLinkFlow_RetryLaterRefusesOnceLinked(t *testing.T) {
	f := dialingJoin(nil)
	f.MarkLinked()
	f.RetryLater(joinTorSlowLine, true)
	if _, phase := f.snapshot(); phase != phaseRun {
		t.Fatalf("a linked run was moved to phase %v", phase)
	}
}

func TestJoinWaitLinkLine(t *testing.T) {
	if got := joinWaitLinkLine(ipc.JoinerDialTimeout); got != joinWaitLinkNoAnswerLine {
		t.Errorf("at zero: %q", got)
	}
	if got := joinWaitLinkLine(0); got != joinWaitLine(0) {
		t.Errorf("before zero the link says %q, the pairing %q", got, joinWaitLine(0))
	}
}
