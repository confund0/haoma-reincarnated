package chat_test

import (
	"errors"
	"testing"

	"haoma-frontend/internal/chat"
)

func TestAttachPeerID_SecondDeviceRoutesToTheSameChat(t *testing.T) {
	s := chat.NewStore(newStore(t))
	dc, err := s.CreateDirect("peer-laptop")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AttachPeerID(dc.ID, "peer-phone"); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetByDirectPeer("peer-phone")
	if err != nil {
		t.Fatalf("the second device does not resolve to a chat: %v", err)
	}
	if got.ID != dc.ID {
		t.Fatalf("second device landed in chat %s, want the existing %s", got.ID, dc.ID)
	}
	if len(got.FanoutPeerIDs()) != 2 {
		t.Fatalf("fan-out set = %v, want both devices", got.FanoutPeerIDs())
	}
}

func TestAttachPeerID_PrimaryStays(t *testing.T) {
	s := chat.NewStore(newStore(t))
	dc, err := s.CreateDirect("peer-laptop")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AttachPeerID(dc.ID, "peer-phone"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetByDirectPeer("peer-laptop")
	if err != nil {
		t.Fatal(err)
	}
	if got.PeerID != "peer-laptop" {
		t.Errorf("primary moved to %q — callers that read PeerID now address a different device", got.PeerID)
	}
	if got.FanoutPeerIDs()[0] != "peer-laptop" {
		t.Errorf("PeerIDs[0] = %q, want the primary", got.FanoutPeerIDs()[0])
	}
}

func TestAttachPeerID_IsIdempotent(t *testing.T) {
	s := chat.NewStore(newStore(t))
	dc, err := s.CreateDirect("peer-laptop")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := s.AttachPeerID(dc.ID, "peer-phone"); err != nil {
			t.Fatalf("attach %d failed: %v", i, err)
		}
	}
	got, err := s.GetByDirectPeer("peer-phone")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.FanoutPeerIDs()) != 2 {
		t.Fatalf("fan-out set = %v after repeated attach, want exactly both devices", got.FanoutPeerIDs())
	}
}

func TestAttachPeerID_RejectsUnknownChatAndEmptyPeer(t *testing.T) {
	s := chat.NewStore(newStore(t))
	if err := s.AttachPeerID(chat.ChatID("nope"), "peer-phone"); err == nil {
		t.Error("attaching to a chat that does not exist should fail")
	}
	dc, err := s.CreateDirect("peer-laptop")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AttachPeerID(dc.ID, ""); err == nil {
		t.Error("attaching an empty peer id should fail")
	}
}

func TestCreateDirectEdgeless_FoundByPersonNotByDevice(t *testing.T) {
	s := chat.NewStore(newStore(t))
	dc, err := s.CreateDirectEdgeless(chat.ChatID("adopted-id"), "fp-alice")
	if err != nil {
		t.Fatal(err)
	}
	if dc.ID != "adopted-id" {
		t.Errorf("chat id = %q — the id must be adopted, both devices have to agree on it", dc.ID)
	}
	if len(dc.FanoutPeerIDs()) != 0 {
		t.Errorf("edge-less chat has routing targets %v — there is no edge to route over", dc.FanoutPeerIDs())
	}
	got, err := s.GetByFingerprint("fp-alice")
	if err != nil {
		t.Fatalf("the chat cannot be found by the person it belongs to: %v", err)
	}
	if got.ID != dc.ID {
		t.Errorf("fingerprint lookup returned %s, want %s", got.ID, dc.ID)
	}
}

func TestAttachPeerID_EdgelessChatGainsItsFirstRoute(t *testing.T) {
	s := chat.NewStore(newStore(t))
	dc, err := s.CreateDirectEdgeless(chat.ChatID("adopted-id"), "fp-alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AttachPeerID(dc.ID, "alice-phone"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetByDirectPeer("alice-phone")
	if err != nil {
		t.Fatalf("the newly paired device does not resolve to the chat: %v", err)
	}
	if got.ID != dc.ID {
		t.Fatalf("pairing started a second chat %s instead of filling in %s", got.ID, dc.ID)
	}
	if got.PeerID != "alice-phone" {
		t.Errorf("primary = %q, want the first attached route", got.PeerID)
	}
	if len(got.FanoutPeerIDs()) != 1 || got.FanoutPeerIDs()[0] != "alice-phone" {
		t.Errorf("routing set = %v, want exactly the new edge", got.FanoutPeerIDs())
	}

	if got.Fingerprint != "fp-alice" {
		t.Errorf("fingerprint lost on attach: %q", got.Fingerprint)
	}
}

func TestCreateDirectEdgeless_RejectsEmptyArguments(t *testing.T) {
	s := chat.NewStore(newStore(t))
	if _, err := s.CreateDirectEdgeless("", "fp-alice"); err == nil {
		t.Error("an edge-less chat with no id should fail — the id must be adopted")
	}
	if _, err := s.CreateDirectEdgeless(chat.ChatID("x"), ""); err == nil {
		t.Error("an edge-less chat with no fingerprint should fail — it would be unfindable")
	}
}

func TestCreateDirectEdgeless_IsIdempotentAndPreservesWhatWasWon(t *testing.T) {
	s := chat.NewStore(newStore(t))
	dc, err := s.CreateDirectEdgeless(chat.ChatID("adopted-id"), "fp-alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AttachPeerID(dc.ID, "alice-phone"); err != nil {
		t.Fatal(err)
	}
	again, err := s.CreateDirectEdgeless(chat.ChatID("adopted-id"), "fp-alice")
	if err != nil {
		t.Fatalf("a repeated backfill must not fail: %v", err)
	}
	if len(again.FanoutPeerIDs()) != 1 || again.PeerID != "alice-phone" {
		t.Fatalf("the second pass wiped the edge: peer=%q set=%v", again.PeerID, again.FanoutPeerIDs())
	}
	if got, err := s.GetByDirectPeer("alice-phone"); err != nil || got.ID != dc.ID {
		t.Errorf("by-peer index no longer resolves after a repeat pass: %v", err)
	}
}

func TestCreateDirectEdgeless_RefusesConflictingClaims(t *testing.T) {
	s := chat.NewStore(newStore(t))
	if _, err := s.CreateDirectEdgeless(chat.ChatID("id-1"), "fp-alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDirectEdgeless(chat.ChatID("id-1"), "fp-bob"); err == nil {
		t.Error("an adopted id already held by a different person should be refused")
	}
	if _, err := s.CreateDirectEdgeless(chat.ChatID("id-2"), "fp-alice"); err == nil {
		t.Error("a person who already has a chat should not get a second one")
	}

	ordinary, err := s.CreateDirect("bob-laptop")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDirectEdgeless(ordinary.ID, "fp-carol"); err == nil {
		t.Error("an existing ordinary chat should not be overwritten by a backfill")
	}
}

func TestDelete_ClearsEveryIndex(t *testing.T) {
	s := chat.NewStore(newStore(t))
	dc, err := s.CreateDirectEdgeless(chat.ChatID("adopted-id"), "fp-alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AttachPeerID(dc.ID, "alice-phone"); err != nil {
		t.Fatal(err)
	}
	if err := s.AttachPeerID(dc.ID, "alice-laptop"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(dc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetByFingerprint("fp-alice"); !errors.Is(err, chat.ErrNotFound) {
		t.Errorf("fingerprint index survived the delete: %v", err)
	}
	for _, pid := range []string{"alice-phone", "alice-laptop"} {
		if _, err := s.GetByDirectPeer(pid); !errors.Is(err, chat.ErrNotFound) {
			t.Errorf("by-peer index for %s survived the delete: %v", pid, err)
		}
	}
}

func TestCreateDirectEdgeless_StaysEdgelessThroughDecode(t *testing.T) {
	s := chat.NewStore(newStore(t))
	dc, err := s.CreateDirectEdgeless(chat.ChatID("adopted-id"), "fp-alice")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetByFingerprint("fp-alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.FanoutPeerIDs()) != 0 {
		t.Errorf("decode invented routes %v for a chat with no edge", got.FanoutPeerIDs())
	}
	if got.PeerID != "" {
		t.Errorf("decode invented a primary %q", got.PeerID)
	}
	_ = dc
}
