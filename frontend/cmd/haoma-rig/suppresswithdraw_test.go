package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	swMsgUnderTest  = "aa11bb2200000000000000000000cafe"
	swMsgOther      = "cc33dd4400000000000000000000beef"
	swChatUnderTest = "1122334400000000000000000000feed"
	swChatOther     = "5566778800000000000000000000face"
)

func writeRigLog(t *testing.T, name string, lines ...string) *rig {
	t.Helper()
	dir := t.TempDir()
	body := ""
	for _, l := range lines {
		body += l
	}
	if err := os.WriteFile(filepath.Join(dir, "haoma.log"), []byte(body), 0o600); err != nil {
		t.Fatalf("write haoma.log: %v", err)
	}
	return &rig{name: name, root: dir}
}

func TestCountInboundClaimObservedSeparatesTheThreeStates(t *testing.T) {
	const marker = "gapfill: inbound receipt claim observed"
	r := writeRigLog(t, "dev-a",
		shortIDLine(marker, "msg_id", swMsgUnderTest, "claimed", "false", "generation", "0"),
		shortIDLine(marker, "msg_id", swMsgUnderTest, "claimed", "false", "generation", "1"),

		shortIDLine(marker, "msg_id", swMsgOther, "claimed", "true", "generation", "0"),
	)

	claimed, unclaimed, err := countInboundClaimObserved(r, swMsgUnderTest)
	if err != nil {
		t.Fatalf("countInboundClaimObserved: %v", err)
	}
	if claimed != 0 {
		t.Errorf("claimed = %d, want 0 — another row's claim was counted against this one", claimed)
	}
	if unclaimed != 2 {
		t.Errorf("unclaimed = %d, want 2", unclaimed)
	}

	claimed, unclaimed, err = countInboundClaimObserved(r, swMsgOther)
	if err != nil {
		t.Fatalf("countInboundClaimObserved: %v", err)
	}
	if claimed != 1 || unclaimed != 0 {
		t.Errorf("claimed/unclaimed = %d/%d for the claiming row, want 1/0", claimed, unclaimed)
	}
}

func TestCountInboundClaimObservedRowNeverCarried(t *testing.T) {
	r := writeRigLog(t, "dev-a",
		shortIDLine("gapfill: inbound receipt claim observed", "msg_id", swMsgOther, "claimed", "false", "generation", "0"),
	)
	claimed, unclaimed, err := countInboundClaimObserved(r, swMsgUnderTest)
	if err != nil {
		t.Fatalf("countInboundClaimObserved: %v", err)
	}
	if claimed != 0 || unclaimed != 0 {
		t.Fatalf("claimed/unclaimed = %d/%d for a row no batch carried, want 0/0", claimed, unclaimed)
	}
}

func TestAwaitCountGrewIgnoresWhatWasAlreadyThere(t *testing.T) {
	r := writeRigLog(t, "dev-a",
		shortIDLine("gapfill: inbound receipt claim observed", "msg_id", swMsgUnderTest, "claimed", "false", "generation", "0"),
	)
	count := func() (int, error) { return totalInboundClaimObserved(r, swMsgUnderTest) }

	if _, err := awaitCountGrew("observed", 1, 100*time.Millisecond, count); err == nil {
		t.Error("the wait returned nil on a count that never moved — a sweep that carried nothing would pass this gate")
	}
	got, err := awaitCountGrew("observed", 0, 100*time.Millisecond, count)
	if err != nil {
		t.Errorf("the wait rejected a count that did grow: %v", err)
	}
	if got != 1 {
		t.Errorf("the wait reported %d, want the count it actually read (1)", got)
	}
}

func TestAwaitCountGrewSurfacesACountingError(t *testing.T) {
	missing := &rig{name: "dev-a", root: t.TempDir()}
	_, err := awaitCountGrew("observed", 0, 5*time.Second, func() (int, error) {
		return totalInboundClaimObserved(missing, swMsgUnderTest)
	})
	if err == nil {
		t.Fatal("a counter that cannot read the log must fail immediately, not time out")
	}
	if strings.Contains(err.Error(), "did not move") {
		t.Errorf("a read failure was reported as a convergence failure: %v", err)
	}
}

func TestCountSuppressionLinesIsScopedToOneChat(t *testing.T) {
	const marker = "read receipts disabled for this chat — suppressed (sentinel stamped)"
	r := writeRigLog(t, "dev-a",
		shortIDLine(marker, "chat_id", swChatUnderTest, "suppressed", "1"),
		shortIDLine(marker, "chat_id", swChatOther, "suppressed", "4"),
		shortIDLine(marker, "chat_id", swChatUnderTest, "suppressed", "2"),
	)
	n, err := countSuppressionLines(r, swChatUnderTest)
	if err != nil {
		t.Fatalf("countSuppressionLines: %v", err)
	}
	if n != 2 {
		t.Fatalf("counted %d suppression lines for the chat under test, want 2", n)
	}
}

func TestSuppressionCountersRefuseAMissingLog(t *testing.T) {
	r := &rig{name: "dev-a", root: t.TempDir()}
	if _, err := countSuppressionLines(r, swChatUnderTest); err == nil {
		t.Error("countSuppressionLines returned no error on a rig with no log")
	}
	if _, _, err := countInboundClaimObserved(r, swMsgUnderTest); err == nil {
		t.Error("countInboundClaimObserved returned no error on a rig with no log")
	}
}

func TestCompleteSuppressWithdrawVerdicts(t *testing.T) {
	out := completeSuppressWithdrawVerdicts(map[string]error{})
	if len(out) != len(suppressWithdrawVerdicts) {
		t.Fatalf("got %d verdicts, want %d", len(out), len(suppressWithdrawVerdicts))
	}
	for _, n := range suppressWithdrawVerdicts {
		if out[n] == nil {
			t.Errorf("verdict %q came back nil — an unset verdict must report itself as a driver bug", n)
		}
	}
}

const (
	gapfillSource      = "../haoma/gapfill.go"
	chatSettingsSource = "../haoma/chat_settings.go"
)

func TestMarkersMatchTheDaemonSource(t *testing.T) {
	for _, tc := range []struct {
		path   string
		needle []string
	}{
		{gapfillSource, []string{inboundClaimMarker, `slog.Bool("` + inboundClaimField + `"`}},
		{chatSettingsSource, []string{suppressionMarker}},
	} {
		src, err := os.ReadFile(tc.path)
		if err != nil {
			t.Fatalf("cannot read %s: %v\nthe daemon source moved. Point this test at its new home, or the marker and its reader drift apart in silence.", tc.path, err)
		}
		for _, n := range tc.needle {
			if !strings.Contains(string(src), n) {
				t.Errorf("%s no longer contains %q — this driver would count zero of them and report the rule as held", tc.path, n)
			}
		}
	}
}

func TestWithdrawalVerdictSeparatesCorrectTreeFromReflectionLoop(t *testing.T) {
	const observed = "gapfill: inbound receipt claim observed"

	for _, tc := range []struct {
		name        string
		claimedLine string
		wantClaimed int
	}{
		{
			name:        "correct tree: dev-b strips the claim it inherited",
			claimedLine: "false",
			wantClaimed: 0,
		},
		{
			name:        "reflection loop: dev-b re-exports what it inherited",
			claimedLine: "true",
			wantClaimed: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := writeRigLog(t, "dev-a",
				shortIDLine(observed, "msg_id", swMsgUnderTest, "claimed", tc.claimedLine, "generation", "0"),
			)

			total, err := awaitCountGrew("observed", 0, 100*time.Millisecond,
				func() (int, error) { return totalInboundClaimObserved(r, swMsgUnderTest) })
			if err != nil {
				t.Fatalf("the row was logged but the wait did not see it: %v", err)
			}
			if total != 1 {
				t.Fatalf("observed %d times, want 1", total)
			}
			claimed, _, err := countInboundClaimObserved(r, swMsgUnderTest)
			if err != nil {
				t.Fatalf("countInboundClaimObserved: %v", err)
			}
			if claimed != tc.wantClaimed {
				t.Fatalf("claimed = %d, want %d — the verdict cannot tell a stripped claim from a re-exported one", claimed, tc.wantClaimed)
			}
		})
	}
}
