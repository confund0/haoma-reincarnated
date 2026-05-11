package calls_test

import (
	"bytes"
	"errors"
	"testing"

	"haoma-frontend/internal/calls"
	"haoma-frontend/internal/chat"
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
