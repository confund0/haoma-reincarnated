package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
)

func TestInvitationLookFor_StateAndDeadline(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	future := now.Add(48 * time.Hour).Unix()
	past := now.Add(-time.Hour).Unix()
	deadline := func(ts int64) string { return time.Unix(ts, 0).Format(invitationDeadlineLayout) }

	cases := []struct {
		name       string
		state      string
		expiresAt  int64
		wantPhrase string
		wantTag    string
		answerable bool
	}{
		{"standing", events.InvitationLive, future, "live - lapses " + deadline(future), StyleInvitationLive, true},
		{"standing with no deadline", events.InvitationLive, 0, "no deadline recorded - it cannot be answered", StyleInvitationDim, false},
		{"past its deadline", events.InvitationLive, past, "lapsed - stood until " + deadline(past), StyleInvitationDim, false},
		{"retired", events.InvitationSuperseded, future, "retired by a newer invitation", StyleInvitationDim, false},
		{"accepted", events.InvitationAccepted, future, "accepted - the pairing completed", StyleInvitationOK, false},
		{"failed", events.InvitationFailed, future, "failed - no pairing came of it", StyleInvitationBad, false},
		{"a state this build does not know", "invented", future, "invented", StyleInvitationDim, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := invitationLookFor(c.state, c.expiresAt, now)
			if got.phrase != c.wantPhrase {
				t.Errorf("phrase = %q, want %q", got.phrase, c.wantPhrase)
			}
			if got.tag != c.wantTag {
				t.Errorf("tag = %q, want %q", got.tag, c.wantTag)
			}
			if got.answerable != c.answerable {
				t.Errorf("answerable = %v, want %v — this gates whether Enter offers to accept", got.answerable, c.answerable)
			}
		})
	}
}

func TestInvitationLookFor_DeadlineBeatsTheStamp(t *testing.T) {
	expires := int64(1_700_000_000)
	look := invitationLookFor(events.InvitationLive, expires, time.Unix(expires, 0))
	if look.answerable {
		t.Fatal("an invitation is answerable at the instant it lapses")
	}
	if !strings.HasPrefix(look.phrase, "lapsed") {
		t.Fatalf("phrase = %q, want it to lead with lapsed", look.phrase)
	}
}

func TestResolveInvitationContact(t *testing.T) {
	chats := []ipc.ChatEntry{
		{ChatID: "c-al", Kind: ipc.ChatKindDirect, Label: "al", Fingerprint: "aaaa1111"},
		{ChatID: "c-alice", Kind: ipc.ChatKindDirect, Label: "alice", Fingerprint: "bbbb2222"},
		{ChatID: "c-alina", Kind: ipc.ChatKindDirect, Label: "alina", Fingerprint: "cccc3333"},
		{ChatID: "c-group", Kind: ipc.ChatKindGroup, GroupName: "alpha team"},
	}

	t.Run("an exact name is never ambiguous with a longer one", func(t *testing.T) {
		got, candidates := resolveInvitationContact(chats, "al")
		if len(candidates) != 0 {
			t.Fatalf("candidates = %v, want the exact match to win outright", candidates)
		}
		if got.ChatID != "c-al" {
			t.Fatalf("chat = %q, want c-al", got.ChatID)
		}
	})

	t.Run("case does not matter", func(t *testing.T) {
		got, _ := resolveInvitationContact(chats, "ALICE")
		if got.ChatID != "c-alice" {
			t.Fatalf("chat = %q, want c-alice", got.ChatID)
		}
	})

	t.Run("an ambiguous prefix names the candidates", func(t *testing.T) {
		got, candidates := resolveInvitationContact(chats, "ali")
		if got.ChatID != "" {
			t.Fatalf("chat = %q, want no match when two contacts qualify", got.ChatID)
		}
		if strings.Join(candidates, ",") != "alice,alina" {
			t.Fatalf("candidates = %v, want alice + alina in name order", candidates)
		}
	})

	t.Run("an identity prefix resolves", func(t *testing.T) {
		got, _ := resolveInvitationContact(chats, "cccc")
		if got.ChatID != "c-alina" {
			t.Fatalf("chat = %q, want c-alina", got.ChatID)
		}
	})

	t.Run("a group is never a candidate", func(t *testing.T) {
		got, candidates := resolveInvitationContact(chats, "alpha team")
		if got.ChatID != "" || len(candidates) != 0 {
			t.Fatalf("chat = %q candidates = %v, want nothing — an invitation is addressed to one identity", got.ChatID, candidates)
		}
	})

	t.Run("nothing matches", func(t *testing.T) {
		got, candidates := resolveInvitationContact(chats, "bob")
		if got.ChatID != "" || len(candidates) != 0 {
			t.Fatalf("chat = %q candidates = %v, want nothing", got.ChatID, candidates)
		}
	})
}

func TestExplainInvitationFailure_EveryTokenIsWorded(t *testing.T) {
	surfaces := []struct {
		name    string
		tokens  []string
		explain func(string) (string, []string)
	}{
		{"offer", ipc.InvitationOfferReasons, explainInvitationOfferFailure},
		{"accept", ipc.InvitationAcceptReasons, explainInvitationAcceptFailure},
		{"list", ipc.InvitationsListReasons, explainInvitationsListFailure},
		{"inherited-contacts", ipc.InheritedContactsListReasons, explainInheritedContactsListFailure},
	}

	for i := range surfaces {
		surfaces[i].tokens = append(append([]string{}, surfaces[i].tokens...), ipc.ClientMintedReasons...)
	}
	for _, s := range surfaces {
		if len(s.tokens) == 0 {
			t.Fatalf("%s: no tokens to check — the daemon's set is empty", s.name)
		}
		for _, tok := range s.tokens {
			t.Run(s.name+"/"+tok, func(t *testing.T) {
				headline, guidance := s.explain(tok)
				assertWorded(t, s.name, tok, headline, guidance)
			})
		}
	}
}

func assertWorded(t *testing.T, surface, token, headline string, guidance []string) {
	t.Helper()
	if headline == "" {
		t.Fatalf("%s/%s produced no headline", surface, token)
	}
	if strings.Contains(headline, token) {
		t.Fatalf("%s/%s: the headline shows the raw token: %q", surface, token, headline)
	}
	if token == "encode_frame" && surface != "list" {
		return
	}
	if len(guidance) == 0 {
		t.Fatalf("%s/%s: no follow-up line telling the operator what to do", surface, token)
	}
	for _, g := range guidance {
		if strings.Contains(g, token) {
			t.Fatalf("%s/%s: a guidance line shows the raw token: %q", surface, token, g)
		}
	}
}

func TestExplainInvitationFailure_UnknownTokenSurvives(t *testing.T) {
	for name, explain := range map[string]func(string) (string, []string){
		"offer":  explainInvitationOfferFailure,
		"accept": explainInvitationAcceptFailure,
		"list":   explainInvitationsListFailure,
	} {
		t.Run(name, func(t *testing.T) {
			_, guidance := explain("some_future_reason")
			if len(guidance) != 1 || !strings.Contains(guidance[0], "some_future_reason") {
				t.Fatalf("guidance = %v, want the unrecognised token carried through", guidance)
			}
		})
	}
}

func TestExplainInvitationFailure_TakesTheTokenAndNothingElse(t *testing.T) {
	var _ func(string) (string, []string) = explainInvitationOfferFailure
	var _ func(string) (string, []string) = explainInvitationAcceptFailure
	var _ func(string) (string, []string) = explainInvitationsListFailure
	var _ func(string) (string, []string) = explainInheritedContactsListFailure
}

func TestInvitationWording_KeepsTheVocabulary(t *testing.T) {
	banned := []string{"edge", "leg", "holder", "forwarding device", "node"}
	var lines []string
	tokens := append(append(append([]string{},
		ipc.InvitationOfferReasons...),
		ipc.InvitationAcceptReasons...),
		ipc.InvitationsListReasons...)
	tokens = append(tokens, ipc.InheritedContactsListReasons...)
	tokens = append(tokens, ipc.ClientMintedReasons...)
	for _, tok := range tokens {
		for _, explain := range []func(string) (string, []string){
			explainInvitationOfferFailure,
			explainInvitationAcceptFailure,
			explainInvitationsListFailure,
			explainInheritedContactsListFailure,
		} {
			h, g := explain(tok)
			lines = append(append(lines, h), g...)
		}
	}
	for _, state := range []string{
		events.InvitationLive, events.InvitationSuperseded,
		events.InvitationAccepted, events.InvitationFailed,
	} {
		lines = append(lines, invitationLookFor(state, 1_700_000_000, time.Unix(1, 0)).phrase)
	}

	lines = append(lines,
		inheritedFrameTitle, inheritedFrameEmpty, inheritedFrameAll,
		pairStateWaiting, pairStateOffering, pairStateOffered, pairStateFailed, pairStateUnknown,
		hintContacts, hintInherited, hintChats, hintChat,
	)
	for _, line := range lines {
		lower := strings.ToLower(line)
		for _, word := range banned {
			if strings.Contains(lower, word) {
				t.Errorf("banned word %q in operator string: %q", word, line)
			}
		}
	}
}

func TestExplainInvitationAcceptFailure_AnUnansweredDialIsTorSlow(t *testing.T) {
	headline, _ := explainInvitationAcceptFailure(ipc.ReasonRendezvousUnreachable)
	if headline != torSlowLine {
		t.Fatalf("headline %q, want %q", headline, torSlowLine)
	}
	if got := refusalStyle(ipc.ReasonRendezvousUnreachable); got != StyleTorSlow {
		t.Fatalf("drawn in %q, want %q", got, StyleTorSlow)
	}
	if got := refusalStyle(ipc.ReasonSelfCheckFailed); got != StyleTorSlow {
		t.Fatalf("the offering side's failed self-check is drawn in %q, want %q", got, StyleTorSlow)
	}
	for _, tok := range []string{ipc.ReasonRendezvousRefused, ipc.ReasonRailSendFailed, ipc.ReasonNoAnswer, ""} {
		if got := refusalStyle(tok); got != StyleLinkBad {
			t.Fatalf("%q drawn in %q, want %q", tok, got, StyleLinkBad)
		}
	}
}

func TestPairStateLines_OneSentencePerLine(t *testing.T) {
	got := pairStateLines(pairStateOffering)
	if len(got) != 2 || got[0] != "Preparing the invitation." || got[1] != "Keep this device on." {
		t.Fatalf("lines %q", got)
	}

	room := inheritedFrameWidth - 2 - inheritedFramePrefix - 1 - 1
	for _, st := range []string{pairStateWaiting, pairStateOffering, pairStateOffered, pairStateFailed, pairStateUnknown} {
		for _, line := range pairStateLines(st) {
			if n := len([]rune(line)); n > room {
				t.Fatalf("%q is %d wide, the frame has room for %d", line, n, room)
			}
		}
	}
}

func TestInheritedFrame_AStateLineKeepsItsColour(t *testing.T) {
	_, tbl, rows := buildInheritedFrame([]inheritedFrameEntry{
		{fingerprint: "fp-a", label: "Alice", state: pairStateOffered},
		{fingerprint: "fp-b", label: "Bob", state: pairStateFailed},
	}, "linked device")
	if tbl == nil {
		t.Fatal("no table for a frame that holds contacts")
	}
	want := map[string]tcell.Color{pairStateOffered: ColorContactsOK, pairStateFailed: ColorContactsBad}
	seen := 0
	for i, r := range rows {
		if r.state == "" {
			continue
		}
		seen++
		fg, _, _ := tbl.GetCell(i, 1).Style.Decompose()
		if fg != want[r.state] {
			t.Fatalf("the line for %q is drawn in %v, want %v", r.state, fg, want[r.state])
		}
	}
	if seen != 2 {
		t.Fatalf("%d state lines, want 2", seen)
	}
}

func TestInheritedFrame_APartialAndAWaitingRowSaySo(t *testing.T) {
	_, tbl, rows := buildInheritedFrame([]inheritedFrameEntry{
		{fingerprint: "fp-a", label: "Alice", devices: 2, paired: 1},
		{fingerprint: "fp-b", label: "Bartholomew-the-long", devices: 2, paired: 1, waiting: true},
		{fingerprint: "fp-c", label: "Carol"},
		{fingerprint: "fp-d", label: "Dan", devices: 2, paired: 1, waiting: true, invited: true},
	}, "linked device")
	if tbl == nil {
		t.Fatal("no table for a frame that holds contacts")
	}
	var lines []string
	for i, r := range rows {
		if r.state == "" {
			continue
		}
		text := strings.TrimSpace(tbl.GetCell(i, 1).Text)
		if n := len([]rune(text)); n > inheritedFrameTextWidth {
			t.Fatalf("%q is %d wide, the frame has room for %d", text, n, inheritedFrameTextWidth)
		}
		lines = append(lines, r.fingerprint+": "+text)
	}
	got := strings.Join(lines, "\n")
	want := "fp-a: 1 of 2 devices\nfp-b: waiting for\nfp-b: Bartholomew-the-long's\nfp-b: device to offer\nfp-d: accept Dan's invitation"
	if got != want {
		t.Fatalf("lines under the names:\n%s\nwant:\n%s", got, want)
	}
}
