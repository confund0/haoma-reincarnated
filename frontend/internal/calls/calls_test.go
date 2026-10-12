package calls_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/dgraph-io/badger/v4"

	"haoma-frontend/internal/calls"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/store"
)

func init() {
	store.DefaultKDFParams = store.KDFParams{
		Time: 1, Memory: 8 * 1024, Threads: 2, KeyLen: 32, SaltLen: 16,
	}
}

func newManager(t *testing.T) *calls.Manager {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Lock() })
	mgr, err := calls.NewManager(st)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return mgr
}

const (
	testChatID = chat.ChatID("aabbccddeeff00112233445566778899")
	testPeer   = "peer-1234"
)

func seedOutgoing(t *testing.T, mgr *calls.Manager, callID string) calls.State {
	t.Helper()
	s := calls.State{
		CallID:     callID,
		ChatID:     testChatID,
		PeerID:     testPeer,
		Direction:  calls.DirOut,
		Status:     calls.StatusOffered,
		Modalities: []string{"audio"},
		StartedAt:  100,
	}
	if err := mgr.PutState(s); err != nil {
		t.Fatalf("PutState: %v", err)
	}
	return s
}

func TestNewCallID_LengthAndUniqueness(t *testing.T) {
	a, err := calls.NewCallID()
	if err != nil {
		t.Fatal(err)
	}
	b, err := calls.NewCallID()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Errorf("NewCallID returned identical ids: %q", a)
	}

	if len(a) != 43 {
		t.Errorf("call id length = %d, want 43", len(a))
	}
}

func TestPutState_GetState_RoundTrip(t *testing.T) {
	mgr := newManager(t)
	s := seedOutgoing(t, mgr, "call-A")
	got, err := mgr.GetState("call-A")
	if err != nil {
		t.Fatal(err)
	}
	if got.CallID != s.CallID || got.ChatID != s.ChatID || got.PeerID != s.PeerID {
		t.Errorf("round-trip identity drift: %+v", got)
	}
	if got.Direction != calls.DirOut || got.Status != calls.StatusOffered {
		t.Errorf("round-trip status drift: dir=%q status=%q", got.Direction, got.Status)
	}
	if got.StartedAt != 100 {
		t.Errorf("StartedAt = %d, want 100", got.StartedAt)
	}
}

func TestGetState_MissingReturnsTypedError(t *testing.T) {
	mgr := newManager(t)
	if _, err := mgr.GetState("absent"); !errors.Is(err, calls.ErrCallNotFound) {
		t.Errorf("err = %v, want ErrCallNotFound", err)
	}
}

func TestPutState_RejectsEmptyFields(t *testing.T) {
	mgr := newManager(t)
	if err := mgr.PutState(calls.State{ChatID: testChatID, PeerID: testPeer, Direction: calls.DirOut}); err == nil {
		t.Errorf("empty CallID accepted")
	}
	if err := mgr.PutState(calls.State{CallID: "x", PeerID: testPeer, Direction: calls.DirOut}); err == nil {
		t.Errorf("empty ChatID accepted")
	}
	if err := mgr.PutState(calls.State{CallID: "x", ChatID: testChatID, Direction: calls.DirOut}); err == nil {
		t.Errorf("empty PeerID accepted")
	}
	if err := mgr.PutState(calls.State{CallID: "x", ChatID: testChatID, PeerID: testPeer, Direction: "sideways"}); err == nil {
		t.Errorf("invalid Direction accepted")
	}
}

func TestTransition_LegalMoves(t *testing.T) {
	cases := []struct {
		from, to calls.Status
		reason   string
	}{
		{calls.StatusOffered, calls.StatusAccepted, ""},
		{calls.StatusOffered, calls.StatusRejected, "user_declined"},
		{calls.StatusOffered, calls.StatusEnded, ""},
		{calls.StatusOffered, calls.StatusFailed, "boom"},
		{calls.StatusRinging, calls.StatusAccepted, ""},
		{calls.StatusRinging, calls.StatusRejected, "busy"},
		{calls.StatusRinging, calls.StatusEnded, ""},
		{calls.StatusAccepted, calls.StatusEnded, ""},
		{calls.StatusAccepted, calls.StatusFailed, "lost-streamer"},
	}
	for _, tc := range cases {
		mgr := newManager(t)
		seedOutgoing(t, mgr, "call-X")

		if tc.from != calls.StatusOffered {
			_, _ = mgr.Transition("call-X", tc.from, "", 200)
			s, _ := mgr.GetState("call-X")
			s.Status = tc.from
			if err := mgr.PutState(s); err != nil {
				t.Fatalf("seed reset: %v", err)
			}
		}
		got, err := mgr.Transition("call-X", tc.to, tc.reason, 300)
		if err != nil {
			t.Errorf("legal transition %s→%s: %v", tc.from, tc.to, err)
			continue
		}
		if got.Status != tc.to {
			t.Errorf("post-state status = %q, want %q", got.Status, tc.to)
		}
		if got.UpdatedAt != 300 {
			t.Errorf("UpdatedAt = %d, want 300", got.UpdatedAt)
		}
		if (tc.to == calls.StatusRejected || tc.to == calls.StatusEnded || tc.to == calls.StatusFailed) && got.EndedAt != 300 {
			t.Errorf("EndedAt = %d, want 300 on terminal", got.EndedAt)
		}
		if (tc.to == calls.StatusRejected || tc.to == calls.StatusFailed) && got.FailReason != tc.reason {
			t.Errorf("FailReason = %q, want %q", got.FailReason, tc.reason)
		}
	}
}

func TestTransition_RejectsIllegalMoves(t *testing.T) {
	cases := []struct{ from, to calls.Status }{
		{calls.StatusRejected, calls.StatusEnded},
		{calls.StatusEnded, calls.StatusAccepted},
		{calls.StatusFailed, calls.StatusEnded},
		{calls.StatusAccepted, calls.StatusRejected},
	}
	for _, tc := range cases {
		mgr := newManager(t)
		seedOutgoing(t, mgr, "call-X")
		s, _ := mgr.GetState("call-X")
		s.Status = tc.from
		if err := mgr.PutState(s); err != nil {
			t.Fatalf("seed reset: %v", err)
		}
		if _, err := mgr.Transition("call-X", tc.to, "", 300); !errors.Is(err, calls.ErrIllegalTransition) {
			t.Errorf("illegal %s→%s err = %v, want ErrIllegalTransition", tc.from, tc.to, err)
		}
	}
}

func TestTransition_MissingRow(t *testing.T) {
	mgr := newManager(t)
	if _, err := mgr.Transition("absent", calls.StatusEnded, "", 1); !errors.Is(err, calls.ErrCallNotFound) {
		t.Errorf("err = %v, want ErrCallNotFound", err)
	}
}

func TestListByChat_FindsAllAndIgnoresOtherChats(t *testing.T) {
	mgr := newManager(t)
	seedOutgoing(t, mgr, "call-A")
	seedOutgoing(t, mgr, "call-B")
	other := calls.State{
		CallID: "call-C", ChatID: chat.ChatID("ffffffffffffffffffffffffffffffff"),
		PeerID: testPeer, Direction: calls.DirIn, Status: calls.StatusRinging,
		StartedAt: 1,
	}
	if err := mgr.PutState(other); err != nil {
		t.Fatal(err)
	}
	got, err := mgr.ListByChat(testChatID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	for _, s := range got {
		if s.ChatID != testChatID {
			t.Errorf("got out-of-chat row: %+v", s)
		}
	}
}

func TestDeleteState_RemovesPrimaryAndIndex(t *testing.T) {
	mgr := newManager(t)
	seedOutgoing(t, mgr, "call-A")
	if err := mgr.DeleteState("call-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.GetState("call-A"); !errors.Is(err, calls.ErrCallNotFound) {
		t.Errorf("post-delete Get err = %v, want ErrCallNotFound", err)
	}
	got, err := mgr.ListByChat(testChatID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("ListByChat post-delete len = %d, want 0 (index leaked)", len(got))
	}
}

func TestSetRemoteMaterial_StampsAndCopies(t *testing.T) {
	mgr := newManager(t)
	seedOutgoing(t, mgr, "call-A")
	key := bytes.Repeat([]byte{0x42}, 32)
	tokens := map[string]string{"audio": "tok-peer"}
	if err := mgr.SetRemoteMaterial("call-A", key, tokens); err != nil {
		t.Fatal(err)
	}

	key[0] = 0xFF
	tokens["audio"] = "wrong"
	got, err := mgr.GetState("call-A")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.RemoteOutboundKey, bytes.Repeat([]byte{0x42}, 32)) {
		t.Errorf("RemoteOutboundKey = %x", got.RemoteOutboundKey)
	}
	if got.RemoteTokens["audio"] != "tok-peer" {
		t.Errorf("RemoteTokens[audio] = %q, want %q", got.RemoteTokens["audio"], "tok-peer")
	}
}

func TestTransition_TerminalWipesRemoteKey(t *testing.T) {
	mgr := newManager(t)
	seedOutgoing(t, mgr, "call-A")
	if err := mgr.SetRemoteMaterial("call-A", bytes.Repeat([]byte{0xAA}, 32), map[string]string{"audio": "tok"}); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Transition("call-A", calls.StatusEnded, "", 500); err != nil {
		t.Fatal(err)
	}
	got, _ := mgr.GetState("call-A")
	if len(got.RemoteOutboundKey) != 0 {
		t.Errorf("RemoteOutboundKey not wiped on terminal: %x", got.RemoteOutboundKey)
	}
	if got.RemoteTokens["audio"] != "tok" {
		t.Errorf("RemoteTokens lost (only key should wipe): %v", got.RemoteTokens)
	}
}

func TestSetRemoteMaterial_RefusesTerminal(t *testing.T) {
	mgr := newManager(t)
	seedOutgoing(t, mgr, "call-A")
	if _, err := mgr.Transition("call-A", calls.StatusEnded, "", 1); err != nil {
		t.Fatal(err)
	}
	if err := mgr.SetRemoteMaterial("call-A", bytes.Repeat([]byte{1}, 32), nil); err == nil {
		t.Errorf("SetRemoteMaterial on terminal row succeeded")
	}
}

func TestSweepNonTerminal_FlipsLiveRowsToFailed(t *testing.T) {
	mgr := newManager(t)

	seedOutgoing(t, mgr, "call-live-1")
	seedOutgoing(t, mgr, "call-live-2")
	if _, err := mgr.Transition("call-live-2", calls.StatusAccepted, "", 10); err != nil {
		t.Fatal(err)
	}
	seedOutgoing(t, mgr, "call-dead")
	if _, err := mgr.Transition("call-dead", calls.StatusEnded, "", 5); err != nil {
		t.Fatal(err)
	}
	n, err := mgr.SweepNonTerminal("daemon_restart", 999)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("swept = %d, want 2", n)
	}
	for _, id := range []string{"call-live-1", "call-live-2"} {
		s, _ := mgr.GetState(id)
		if s.Status != calls.StatusFailed || s.FailReason != "daemon_restart" {
			t.Errorf("%s post-sweep status=%s reason=%q", id, s.Status, s.FailReason)
		}
	}
	dead, _ := mgr.GetState("call-dead")
	if dead.Status != calls.StatusEnded {
		t.Errorf("call-dead got rewritten: %s", dead.Status)
	}
}

func TestCleanupHook_CascadesOnSweepExpired(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Lock() })

	mgr, err := calls.NewManager(st)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	chatID := testChatID
	now := int64(10_000)
	bus := events.NewBus()
	log := events.New(st, bus, func() time.Time { return time.Unix(now, 0) })

	seedOutgoing(t, mgr, "call-A")
	if _, err := mgr.GetState("call-A"); err != nil {
		t.Fatalf("seed visible: %v", err)
	}

	body, err := json.Marshal(events.CallSummaryBody{
		CallID:    "call-A",
		Direction: "out",
		Outcome:   events.CallOutcomeCompleted,
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	if _, err := log.AppendLocal(events.LocalParams{
		ChatID:        chatID,
		Kind:          events.KindCallSummary,
		Direction:     events.DirOut,
		DisplayTs:     now,
		ExpireSeconds: 1,
		Body:          body,
	}); err != nil {
		t.Fatalf("AppendLocal: %v", err)
	}

	now += 60
	n, _, err := log.SweepExpired(now)
	if err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}
	if n < 1 {
		t.Fatalf("SweepExpired removed %d rows, want >=1", n)
	}

	if _, err := mgr.GetState("call-A"); !errors.Is(err, calls.ErrCallNotFound) {
		t.Errorf("call:<id> not cascaded by SweepExpired hook: err=%v", err)
	}
	rest, err := mgr.ListByChat(chatID)
	if err != nil {
		t.Fatalf("ListByChat: %v", err)
	}
	if len(rest) != 0 {
		t.Errorf("call-by-chat survivors after sweep: %d", len(rest))
	}
}

func TestCleanupHook_CascadesOnDeleteByChat(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Lock() })

	mgr, err := calls.NewManager(st)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	chatID := testChatID
	now := int64(10_000)
	bus := events.NewBus()
	log := events.New(st, bus, func() time.Time { return time.Unix(now, 0) })

	seedOutgoing(t, mgr, "call-A")
	seedOutgoing(t, mgr, "call-B")
	for _, id := range []string{"call-A", "call-B"} {
		body, err := json.Marshal(events.CallSummaryBody{CallID: id, Direction: "out", Outcome: events.CallOutcomeCompleted})
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		if _, err := log.AppendLocal(events.LocalParams{
			ChatID:    chatID,
			Kind:      events.KindCallSummary,
			Direction: events.DirOut,
			DisplayTs: now,
			Body:      body,
		}); err != nil {
			t.Fatalf("AppendLocal %s: %v", id, err)
		}
	}

	n, _, err := log.DeleteByChat(chatID)
	if err != nil {
		t.Fatalf("DeleteByChat: %v", err)
	}
	if n != 2 {
		t.Errorf("breadcrumbs deleted=%d, want 2", n)
	}
	for _, id := range []string{"call-A", "call-B"} {
		if _, err := mgr.GetState(id); !errors.Is(err, calls.ErrCallNotFound) {
			t.Errorf("%s not cascaded by DeleteByChat hook: err=%v", id, err)
		}
	}
}

func TestState_IsTerminal(t *testing.T) {
	cases := map[calls.Status]bool{
		calls.StatusOffered:  false,
		calls.StatusRinging:  false,
		calls.StatusAccepted: false,
		calls.StatusRejected: true,
		calls.StatusEnded:    true,
		calls.StatusFailed:   true,
	}
	for st, want := range cases {
		got := calls.State{Status: st}.IsTerminal()
		if got != want {
			t.Errorf("Status %q IsTerminal = %v, want %v", st, got, want)
		}
	}
}

func seedFanout(t *testing.T, mgr *calls.Manager, callID string) {
	t.Helper()
	s := calls.State{
		CallID:     callID,
		ChatID:     testChatID,
		PeerID:     "dev-a",
		Direction:  calls.DirOut,
		Status:     calls.StatusOffered,
		Modalities: []string{"audio"},
		StartedAt:  100,
		Targets:    []string{"dev-a", "dev-b", "dev-c"},
	}
	if err := mgr.PutState(s); err != nil {
		t.Fatalf("PutState: %v", err)
	}
}

func TestClaimAnswer_ExactlyOneWinsUnderRace(t *testing.T) {
	for round := 0; round < 20; round++ {
		mgr := newManager(t)
		seedFanout(t, mgr, "race")
		devs := []string{"dev-a", "dev-b", "dev-c"}
		type result struct {
			dev string
			won bool
			err error
		}
		out := make(chan result, len(devs))
		start := make(chan struct{})
		for _, d := range devs {
			go func(d string) {
				<-start
				_, won, err := mgr.ClaimAnswer("race", d)
				out <- result{d, won, err}
			}(d)
		}
		close(start)
		winner := ""
		for range devs {
			r := <-out
			if r.err != nil {
				t.Fatalf("round %d: ClaimAnswer(%s): %v", round, r.dev, r.err)
			}
			if r.won {
				if winner != "" {
					t.Fatalf("round %d: %s and %s both won", round, winner, r.dev)
				}
				winner = r.dev
			}
		}
		if winner == "" {
			t.Fatalf("round %d: nobody won", round)
		}
		got, err := mgr.GetState("race")
		if err != nil {
			t.Fatal(err)
		}
		if got.AnsweredBy != winner {
			t.Fatalf("round %d: row answered by %q, winner was %q", round, got.AnsweredBy, winner)
		}
	}
}

func TestClaimAnswer_RefusesNonTargetAndClosedCall(t *testing.T) {
	mgr := newManager(t)
	seedFanout(t, mgr, "c1")
	if _, won, err := mgr.ClaimAnswer("c1", "dev-eve"); err != nil || won {
		t.Fatalf("non-target: won=%v err=%v, want a refusal", won, err)
	}
	if _, err := mgr.Transition("c1", calls.StatusEnded, "", 200); err != nil {
		t.Fatal(err)
	}
	if _, won, err := mgr.ClaimAnswer("c1", "dev-a"); err != nil || won {
		t.Fatalf("ended call: won=%v err=%v, want a refusal", won, err)
	}
}

func TestMarkStopped_RingingShrinks(t *testing.T) {
	mgr := newManager(t)
	seedFanout(t, mgr, "c2")
	s, err := mgr.MarkStopped("c2", "dev-b", "dev-eve", "dev-b")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Stopped) != 1 || s.Stopped[0] != "dev-b" {
		t.Fatalf("stopped = %v, want [dev-b]", s.Stopped)
	}
	if r := s.Ringing(); len(r) != 2 || r[0] != "dev-a" || r[1] != "dev-c" {
		t.Fatalf("ringing = %v, want [dev-a dev-c]", r)
	}
	if r := s.RingingExcept("dev-a"); len(r) != 1 || r[0] != "dev-c" {
		t.Fatalf("ringing except dev-a = %v, want [dev-c]", r)
	}
}

func TestCallTargets_LegacyRowFallsBackToPeer(t *testing.T) {
	s := calls.State{PeerID: "dev-a", Direction: calls.DirOut}
	if !s.IsTarget("dev-a") || s.IsTarget("dev-b") || s.IsTarget("") {
		t.Fatalf("legacy row targets wrong: %v", s.CallTargets())
	}
	if s.MediaPeer() != "dev-a" {
		t.Fatalf("media peer = %q, want dev-a", s.MediaPeer())
	}
	s.AnsweredBy = "dev-b"
	if s.MediaPeer() != "dev-b" || s.Ringing() != nil {
		t.Fatalf("answered row: media %q ringing %v", s.MediaPeer(), s.Ringing())
	}
}

func conflictTimes(t *testing.T, mgr *calls.Manager, n int) func() int {
	t.Helper()
	caused := 0
	restore := calls.SetBeforeCommitForTest(func(callID string) {
		if n >= 0 && caused >= n {
			return
		}
		caused++
		s, err := mgr.GetState(callID)
		if err != nil {
			t.Errorf("conflict write: %v", err)
			return
		}
		if err := mgr.PutState(s); err != nil {
			t.Errorf("conflict write: %v", err)
		}
	})
	t.Cleanup(restore)
	return func() int { return caused }
}

func TestTransition_RetriesOnConflict(t *testing.T) {
	mgr := newManager(t)
	seedFanout(t, mgr, "c3")
	caused := conflictTimes(t, mgr, 2)
	got, err := mgr.Transition("c3", calls.StatusEnded, "", 200)
	if err != nil {
		t.Fatalf("Transition after 2 conflicts: %v", err)
	}
	if got.Status != calls.StatusEnded || caused() != 2 {
		t.Fatalf("status %s after %d conflicts, want ended after 2", got.Status, caused())
	}
	if s, _ := mgr.GetState("c3"); s.Status != calls.StatusEnded {
		t.Fatalf("stored status = %s, want ended", s.Status)
	}
}

func TestClaimAnswer_RetriesOnConflict(t *testing.T) {
	mgr := newManager(t)
	seedFanout(t, mgr, "c4")
	conflictTimes(t, mgr, 2)
	if _, won, err := mgr.ClaimAnswer("c4", "dev-b"); err != nil || !won {
		t.Fatalf("ClaimAnswer after 2 conflicts: won=%v err=%v", won, err)
	}
	if s, _ := mgr.GetState("c4"); s.AnsweredBy != "dev-b" {
		t.Fatalf("answered by %q, want dev-b", s.AnsweredBy)
	}
}

func TestTransition_ConflictEveryTimeIsReported(t *testing.T) {
	mgr := newManager(t)
	seedFanout(t, mgr, "c5")
	caused := conflictTimes(t, mgr, -1)
	if _, err := mgr.Transition("c5", calls.StatusEnded, "", 200); !errors.Is(err, badger.ErrConflict) {
		t.Fatalf("err = %v, want badger.ErrConflict", err)
	}
	if caused() != calls.UpdateRetries {
		t.Fatalf("tried %d times, want %d", caused(), calls.UpdateRetries)
	}
}

func TestFailUnanswered_LosesToAClaimInBetween(t *testing.T) {
	mgr := newManager(t)
	seedFanout(t, mgr, "c6")
	claimed := false
	inHook := false
	restore := calls.SetBeforeCommitForTest(func(callID string) {
		if claimed || inHook {
			return
		}
		inHook = true
		if _, won, err := mgr.ClaimAnswer(callID, "dev-b"); err != nil || !won {
			t.Errorf("claim in between: won=%v err=%v", won, err)
		}
		inHook = false
		claimed = true
	})
	t.Cleanup(restore)

	_, err := mgr.FailUnanswered("c6", calls.FailReasonNoAnswer, 200)
	if !errors.Is(err, calls.ErrIllegalTransition) {
		t.Fatalf("err = %v, want ErrIllegalTransition (answered)", err)
	}
	s, _ := mgr.GetState("c6")
	if s.Status != calls.StatusOffered || s.AnsweredBy != "dev-b" {
		t.Fatalf("row = %s answered by %q, want offered, answered by dev-b", s.Status, s.AnsweredBy)
	}
}

func TestFailUnanswered_FailsAnUnansweredCall(t *testing.T) {
	mgr := newManager(t)
	seedFanout(t, mgr, "c7")
	s, err := mgr.FailUnanswered("c7", calls.FailReasonNoAnswer, 200)
	if err != nil || s.Status != calls.StatusFailed || s.FailReason != calls.FailReasonNoAnswer || s.EndedAt != 200 {
		t.Fatalf("row = %+v err=%v, want failed (no_answer) at 200", s, err)
	}
}
