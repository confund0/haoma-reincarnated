package chat

import (
	"encoding/json"
	"strings"
	"testing"

	"haoma-frontend/internal/store"
)

func init() {
	store.DefaultKDFParams = store.KDFParams{
		Time: 1, Memory: 8 * 1024, Threads: 2, KeyLen: 32, SaltLen: 16,
	}
}

func wbStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Unlock(t.TempDir(), "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Lock() })
	return st
}

func legacyDirectRaw(t *testing.T, id ChatID, peerID string) []byte {
	t.Helper()
	dc := &DirectChat{
		BaseChat:   BaseChat{ID: id, Members: []string{peerID}},
		MaxMembers: MaxMembersDirect,
		PeerID:     peerID,
	}
	data, err := json.Marshal(dc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "peer_ids") {
		t.Fatalf("legacy fixture unexpectedly carries peer_ids: %s", data)
	}
	raw, err := json.Marshal(record{Kind: KindDirect, Data: data})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestDecodeRecord_LegacyDirectNormalisesPeerIDs(t *testing.T) {
	raw := legacyDirectRaw(t, "chat-1", "peer-bob")

	c, err := decodeRecord(raw)
	if err != nil {
		t.Fatal(err)
	}
	dc, ok := c.(*DirectChat)
	if !ok {
		t.Fatalf("decodeRecord returned %T, want *DirectChat", c)
	}
	if len(dc.PeerIDs) != 1 || dc.PeerIDs[0] != "peer-bob" {
		t.Fatalf("legacy row normalised to PeerIDs=%v, want [peer-bob]", dc.PeerIDs)
	}
	if dc.PeerID != dc.PeerIDs[0] {
		t.Errorf("invariant broken: PeerID=%q != PeerIDs[0]=%q", dc.PeerID, dc.PeerIDs[0])
	}
}

func TestDecodeRecord_PreservesExistingPeerIDs(t *testing.T) {

	dc := &DirectChat{
		BaseChat:   BaseChat{ID: "chat-2", Members: []string{"peer-bob"}},
		MaxMembers: MaxMembersDirect,
		PeerID:     "peer-bob",
		PeerIDs:    []string{"peer-bob", "peer-bob-tablet"},
	}
	data, _ := json.Marshal(dc)
	raw, _ := json.Marshal(record{Kind: KindDirect, Data: data})

	c, err := decodeRecord(raw)
	if err != nil {
		t.Fatal(err)
	}
	got := c.(*DirectChat)
	if len(got.PeerIDs) != 2 || got.PeerIDs[0] != "peer-bob" || got.PeerIDs[1] != "peer-bob-tablet" {
		t.Fatalf("normalise clobbered an existing set: %v", got.PeerIDs)
	}
}

func TestSelfHeal_PeerIDsPersistOnNextWrite(t *testing.T) {
	st := wbStore(t)
	s := NewStore(st)

	id := ChatID("chat-legacy")
	if err := st.Put(chatKey(id), legacyDirectRaw(t, id, "peer-bob")); err != nil {
		t.Fatal(err)
	}
	if err := st.Put(byPeerKey("peer-bob"), []byte(id)); err != nil {
		t.Fatal(err)
	}

	c, err := s.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.(*DirectChat).PeerIDs; len(got) != 1 || got[0] != "peer-bob" {
		t.Fatalf("Get did not normalise legacy row: PeerIDs=%v", got)
	}

	before, err := st.Get(chatKey(id))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(before), "peer_ids") {
		t.Fatalf("legacy row already carries peer_ids before any write: %s", before)
	}

	if err := s.SetRetentionTTL(id, 3600); err != nil {
		t.Fatal(err)
	}
	after, err := st.Get(chatKey(id))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "peer_ids") {
		t.Fatalf("write did not self-heal the row to carry peer_ids: %s", after)
	}
}
