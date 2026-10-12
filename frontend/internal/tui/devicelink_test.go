package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"haoma-frontend/internal/ipc"
)

func TestExplainLinkFailure_EmptyDeviceRefusal(t *testing.T) {

	for _, token := range []string{
		"device_not_empty",
		"device-not-empty",
		"not_empty",
		"devicelink_joiner_not_empty",
		"has_contacts",
		"has_chats",
		"nonempty",
	} {

		headline, guidance := explainLinkFailure(linkSideJoining, token, "")
		if !strings.Contains(headline, "already has contacts or chats") {
			t.Errorf("%q → headline %q, want the empty-device explanation", token, headline)
		}
		if strings.Contains(headline, token) {
			t.Errorf("%q leaked the raw token into the headline", token)
		}
		joined := strings.Join(guidance, " ")
		if !strings.Contains(joined, "have not set up yet") {
			t.Errorf("%q → guidance %q, want the fresh-device instruction", token, joined)
		}
		if strings.Contains(joined, token) {
			t.Errorf("%q leaked the raw token into the guidance", token)
		}
	}
}

func TestExplainLinkFailure_KnownTokens(t *testing.T) {
	cases := []struct {
		token string
		want  string
	}{
		{"expired", "ran out before"},
		{"rendezvous_timeout", "ran out before"},
		{"cancelled", "withdrawn"},
		{"already_linked", "This device already has a linked device"},
		{"bad_words", "did not open a link offer"},
		{"onion_unreachable", "could not reach"},
	}

	for _, c := range cases {
		headline, _ := explainLinkFailure(linkSideUnknown, c.token, "")
		if !strings.Contains(headline, c.want) {
			t.Errorf("%q → %q, want it to contain %q", c.token, headline, c.want)
		}
	}
}

func TestExplainLinkFailure_NoAnswerDoesNotClaimTheLinkFailed(t *testing.T) {
	for _, token := range []string{ipc.ReasonNoAnswer, "no-answer", "devicelink_no_answer"} {

		headline, guidance := explainLinkFailure(linkSideJoining, token, "")
		if headline != "The link may have gone through." {
			t.Errorf("%q → headline %q, want the may-have-landed wording", token, headline)
		}

		joined := strings.Join(guidance, " ")
		if !strings.Contains(joined, "/linked-devices on this device") {
			t.Errorf("%q → guidance %q, want the check on this device's own linked-device list", token, joined)
		}
		if strings.Contains(headline, token) || strings.Contains(joined, token) {
			t.Errorf("%q leaked the raw token to the screen: %q / %q", token, headline, joined)
		}
	}
}

func TestExplainLinkFailure_NoAnswerIsWhatAbandonPendingSends(t *testing.T) {
	f, err := ipc.NewFrame(ipc.FrameError, "tui-1", ipc.ErrorPayload{
		Code:    ipc.ReasonNoAnswer,
		Message: "the connection to the daemon went down before this was answered",
	})
	if err != nil {
		t.Fatal(err)
	}
	var p ipc.ErrorPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		t.Fatal(err)
	}
	headline, _ := explainLinkFailure(linkSideJoining, p.Code, p.Message)
	if headline == "The device link did not go through." {
		t.Fatalf("the abandoned-request token fell to the default: %q", headline)
	}
	if headline != "The link may have gone through." {
		t.Fatalf("headline = %q", headline)
	}
}

func TestExplainLinkFailure_UnknownTokenSurfacesItself(t *testing.T) {

	headline, guidance := explainLinkFailure(linkSideUnknown, "moon_phase_wrong", "the moon was gibbous")
	if !strings.Contains(headline, "did not go through") {
		t.Fatalf("headline = %q", headline)
	}
	joined := strings.Join(guidance, " ")
	if !strings.Contains(joined, "moon_phase_wrong") || !strings.Contains(joined, "gibbous") {
		t.Fatalf("guidance = %q, want both token and detail", joined)
	}
}

func TestExplainLinkFailure_NoReasonAtAll(t *testing.T) {
	headline, guidance := explainLinkFailure(linkSideUnknown, "", "")
	if !strings.Contains(headline, "did not go through") {
		t.Fatalf("headline = %q", headline)
	}
	if len(guidance) != 0 {
		t.Fatalf("guidance = %v, want none when there is nothing to say", guidance)
	}
}

func TestExplainLinkProbe_AttemptSaysTheRendezvousIsNotUpYet(t *testing.T) {
	line := explainLinkProbe(2, false, "unreachable")
	if !strings.Contains(line, "still publishing") {
		t.Errorf("an unfinished attempt must say the rendezvous is not up yet; got %q", line)
	}
	if !strings.Contains(line, "attempt 2") {
		t.Errorf("the operator needs to see progress, not a static line; got %q", line)
	}
	if strings.Contains(line, "unreachable") {
		t.Errorf("the daemon's token leaked to the screen: %q", line)
	}
}

func TestExplainLinkProbe_ReadyTellsTheOperatorToGoAhead(t *testing.T) {
	line := explainLinkProbe(3, true, "")
	if !strings.Contains(line, "reachable") {
		t.Errorf("ready must name the state that changed; got %q", line)
	}
	if !strings.Contains(line, "Enter the words") {
		t.Errorf("ready is the cue to act, so it must say what to do; got %q", line)
	}
}

func TestExplainLinkProbe_GiveUpStaysHonestWithoutBlocking(t *testing.T) {
	line := explainLinkProbe(30, true, "probe-timeout")
	if !strings.Contains(line, "could not confirm") {
		t.Errorf("a give-up must not read as success; got %q", line)
	}
	if !strings.Contains(line, "still live") {
		t.Errorf("a give-up must not read as failure either; got %q", line)
	}
	if strings.Contains(line, "probe-timeout") {
		t.Errorf("the daemon's token leaked to the screen: %q", line)
	}
}

func TestDeviceLinkSurface_UsesNoContactVocabulary(t *testing.T) {
	banned := []string{"pair", "contact us", "friend"}
	surfaces := []string{
		linkOfferWarning,
		linkOfferHoldNotice,
		explainLinkProbe(1, false, ""),
		explainLinkProbe(2, false, "unreachable"),
		explainLinkProbe(3, true, ""),
		explainLinkProbe(30, true, "probe-timeout"),
	}
	for _, token := range []string{"device_not_empty", "expired", "cancelled", "bad_words", "whatever"} {
		h, g := explainLinkFailure(linkSideUnknown, token, "")
		surfaces = append(surfaces, h)
		surfaces = append(surfaces, g...)
	}
	for _, s := range surfaces {
		low := strings.ToLower(s)
		for _, b := range banned {
			if strings.Contains(low, b) {
				t.Errorf("device-link surface uses reserved contact vocabulary %q: %q", b, s)
			}
		}
	}
}

func TestLinkCommands_AreCompletable(t *testing.T) {
	want := []string{"/link-device", "/link-join", "/link-cancel", "/linked-devices"}
	have := map[string]bool{}
	for _, c := range slashCommands {
		have[c] = true
	}
	for _, c := range want {
		if !have[c] {
			t.Errorf("%s missing from the completion vocabulary", c)
		}
		if !statusOnlyCommands[c] {
			t.Errorf("%s should be scoped to the status window like the other setup verbs", c)
		}
	}
}

func TestSyncOutstanding_OnlyASyncThisClientStartedIsWrittenUp(t *testing.T) {
	var w syncOutstanding
	now := time.Unix(1754900000, 0)
	if w.take(now) {
		t.Fatal("a push with no /sync open was written up")
	}
	w.start(now)
	if !w.take(now.Add(3 * time.Minute)) {
		t.Fatal("the push for the open /sync was not written up")
	}
	if w.take(now.Add(3 * time.Minute)) {
		t.Fatal("a second push was written up for the same /sync")
	}
	w.start(now)
	if w.take(now.Add(syncFinishedWait + time.Second)) {
		t.Fatal("a push later than the daemon ever sends one was written up")
	}
}

func TestSyncFinishedLine(t *testing.T) {
	cases := []struct {
		p    ipc.DeviceSyncFinishedPush
		want []string
		not  []string
	}{
		{ipc.DeviceSyncFinishedPush{Siblings: 1, Answered: 1, RowsApplied: 3, SnapshotApplied: 2},
			[]string{"sync finished", "1 linked device(s) answered", "5 change(s) here"}, []string{"sync all", "synced", "wait for a contact"}},
		{ipc.DeviceSyncFinishedPush{Full: true, Siblings: 1, Answered: 1},
			[]string{"sync all finished", "nothing was missing"}, []string{"synced"}},
		{ipc.DeviceSyncFinishedPush{Full: true, Siblings: 1, Answered: 1, Held: 4},
			[]string{"nothing was missing", "4 row(s) wait for a contact"}, []string{"synced"}},
		{ipc.DeviceSyncFinishedPush{Siblings: 2, Answered: 1, TimedOut: true},
			[]string{"sync stopped waiting", "1 of 2 linked device(s) answered", "no change here"}, []string{"finished", "nothing was missing"}},
		{ipc.DeviceSyncFinishedPush{Siblings: 1, Unreachable: 1},
			[]string{"sync failed", "did not reach 1 linked device(s)"}, []string{"finished", "nothing was missing"}},
		{ipc.DeviceSyncFinishedPush{Siblings: 2, Answered: 1, Unreachable: 1, SnapshotApplied: 1},
			[]string{"sync finished", "1 of 2 linked device(s) answered", "1 could not be reached", "1 change(s) here"}, []string{"stopped"}},
	}
	for _, c := range cases {
		got := syncFinishedLine(c.p)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%+v → %q, want it to contain %q", c.p, got, w)
			}
		}
		for _, n := range c.not {
			if strings.Contains(got, n) {
				t.Errorf("%+v → %q, must not contain %q", c.p, got, n)
			}
		}
	}
}
