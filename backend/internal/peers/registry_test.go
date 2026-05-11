package peers

import (
	"bytes"
	"errors"
	"os"
	"testing"
	"time"

	"haoma/internal/store"
)

func TestMain(m *testing.M) {
	store.DefaultKDFParams = store.KDFParams{
		Time: 1, Memory: 8 * 1024, Threads: 4, KeyLen: 32, SaltLen: 16,
	}
	os.Exit(m.Run())
}

func newTestRegistry(t *testing.T) (*Registry, *store.Store) {
	t.Helper()
	s, err := store.Unlock(t.TempDir(), "pw")
	if err != nil {
		t.Fatalf("store.Unlock: %v", err)
	}
	t.Cleanup(func() { _ = s.Lock() })
	return NewRegistry(s), s
}

func freshPeer(t *testing.T, addrs ...string) Peer {
	t.Helper()
	id, err := NewPeerID()
	if err != nil {
		t.Fatalf("NewPeerID: %v", err)
	}
	secret, err := NewPeerSecret()
	if err != nil {
		t.Fatalf("NewPeerSecret: %v", err)
	}
	return Peer{
		ID:             id,
		KnownAddresses: addrs,
		InboundSecret:  secret,
		OutboundSecret: secret,
	}
}

func TestRegistry_Add_Get_Remove(t *testing.T) {
	r, _ := newTestRegistry(t)
	p := freshPeer(t, "abc123")

	if err := r.Add(p); err != nil {
		t.Fatal(err)
	}

	got, err := r.Get(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != p.ID || !bytes.Equal(got.InboundSecret, p.InboundSecret) || !bytes.Equal(got.OutboundSecret, p.OutboundSecret) {
		t.Errorf("round-trip mismatch: got %+v want %+v", got, p)
	}
	if len(got.KnownAddresses) != 1 || got.KnownAddresses[0] != "abc123" {
		t.Errorf("KnownAddresses = %v", got.KnownAddresses)
	}

	if err := r.Remove(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(p.ID); !errors.Is(err, ErrPeerNotFound) {
		t.Errorf("Get after Remove: err = %v, want ErrPeerNotFound", err)
	}
}

func TestRegistry_Get_Unknown(t *testing.T) {
	r, _ := newTestRegistry(t)
	if _, err := r.Get("deadbeef"); !errors.Is(err, ErrPeerNotFound) {
		t.Errorf("err = %v, want ErrPeerNotFound", err)
	}
}

func TestRegistry_List(t *testing.T) {
	r, _ := newTestRegistry(t)
	for i := 0; i < 3; i++ {
		if err := r.Add(freshPeer(t, "addr"+string(rune('A'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	peers, err := r.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 3 {
		t.Errorf("List returned %d, want 3", len(peers))
	}
}

func TestRegistry_ByAddress(t *testing.T) {
	r, _ := newTestRegistry(t)
	alice := freshPeer(t, "aliceONION56charsbleh", "alice-backup")
	bob := freshPeer(t, "bobONION56chars")
	if err := r.Add(alice); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(bob); err != nil {
		t.Fatal(err)
	}

	got, err := r.ByAddress("aliceONION56charsbleh")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != alice.ID {
		t.Errorf("ByAddress returned %s, want %s", got.ID, alice.ID)
	}

	got, err = r.ByAddress("alice-backup")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != alice.ID {
		t.Errorf("secondary address lookup: got %s, want %s", got.ID, alice.ID)
	}

	if _, err := r.ByAddress("unknown"); !errors.Is(err, ErrPeerNotFound) {
		t.Errorf("unknown address: err = %v, want ErrPeerNotFound", err)
	}
}

func TestRegistry_RecordViolation(t *testing.T) {
	r, _ := newTestRegistry(t)
	p := freshPeer(t, "good-addr")
	if err := r.Add(p); err != nil {
		t.Fatal(err)
	}

	if err := r.RecordViolation(p.ID, "bad-addr-1"); err != nil {
		t.Fatal(err)
	}
	if err := r.RecordViolation(p.ID, "bad-addr-1"); err != nil {
		t.Fatal(err)
	}
	if err := r.RecordViolation(p.ID, "bad-addr-2"); err != nil {
		t.Fatal(err)
	}

	got, err := r.Get(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.IDSCounters["bad-addr-1"] != 2 {
		t.Errorf("IDSCounters[bad-addr-1] = %d, want 2", got.IDSCounters["bad-addr-1"])
	}
	if got.IDSCounters["bad-addr-2"] != 1 {
		t.Errorf("IDSCounters[bad-addr-2] = %d, want 1", got.IDSCounters["bad-addr-2"])
	}
}

func TestRegistry_RecordViolation_UnknownPeer(t *testing.T) {
	r, _ := newTestRegistry(t)
	err := r.RecordViolation("no-such-peer", "whatever")
	if !errors.Is(err, ErrPeerNotFound) {
		t.Errorf("err = %v, want ErrPeerNotFound", err)
	}
}

func TestRegistry_TouchPresence(t *testing.T) {
	r, _ := newTestRegistry(t)
	p := freshPeer(t, "addr")
	if err := r.Add(p); err != nil {
		t.Fatal(err)
	}

	ts1 := time.Unix(1_700_000_000, 0)
	if err := r.TouchPresence(p.ID, ts1, "haomad"); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastPassiveAt != ts1.Unix() {
		t.Errorf("LastPassiveAt = %d, want %d", got.LastPassiveAt, ts1.Unix())
	}
	if got.LastActiveAt != 0 {
		t.Errorf("LastActiveAt = %d, want 0 after haomad-source touch", got.LastActiveAt)
	}

	ts2 := time.Unix(1_700_000_100, 0)
	if err := r.TouchPresence(p.ID, ts2, "haoma"); err != nil {
		t.Fatal(err)
	}
	got, err = r.Get(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastActiveAt != ts2.Unix() {
		t.Errorf("LastActiveAt = %d, want %d", got.LastActiveAt, ts2.Unix())
	}
	if got.LastPassiveAt != ts2.Unix() {
		t.Errorf("LastPassiveAt = %d, want %d", got.LastPassiveAt, ts2.Unix())
	}

	ts3 := time.Unix(1_700_000_200, 0)
	if err := r.TouchPresence(p.ID, ts3, ""); err != nil {
		t.Fatal(err)
	}
	got, err = r.Get(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastPassiveAt != ts3.Unix() {
		t.Errorf("LastPassiveAt = %d, want %d", got.LastPassiveAt, ts3.Unix())
	}
	if got.LastActiveAt != ts2.Unix() {
		t.Errorf("LastActiveAt = %d, want preserved %d", got.LastActiveAt, ts2.Unix())
	}
}

func TestRegistry_RoundTripPersistence(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	r := NewRegistry(s)

	p := freshPeer(t, "first-onion")
	if err := r.Add(p); err != nil {
		t.Fatal(err)
	}
	if err := r.RecordViolation(p.ID, "bad"); err != nil {
		t.Fatal(err)
	}
	if err := s.Lock(); err != nil {
		t.Fatal(err)
	}

	s2, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Lock()
	r2 := NewRegistry(s2)

	got, err := r2.Get(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != p.ID || !bytes.Equal(got.InboundSecret, p.InboundSecret) || !bytes.Equal(got.OutboundSecret, p.OutboundSecret) {
		t.Errorf("peer fields changed across reopen")
	}
	if got.IDSCounters["bad"] != 1 {
		t.Errorf("IDS counter lost across reopen: %d", got.IDSCounters["bad"])
	}
}

func TestNewPeerID_And_Secret_Independence(t *testing.T) {
	id1, err := NewPeerID()
	if err != nil {
		t.Fatal(err)
	}
	id2, err := NewPeerID()
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 {
		t.Errorf("two NewPeerID calls collided: %s", id1)
	}
	if len(id1) != 32 {
		t.Errorf("ID length = %d, want 32 hex chars", len(id1))
	}

	s1, err := NewPeerSecret()
	if err != nil {
		t.Fatal(err)
	}
	s2, err := NewPeerSecret()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(s1, s2) {
		t.Errorf("two NewPeerSecret calls collided")
	}
	if len(s1) != 32 {
		t.Errorf("Secret length = %d, want 32", len(s1))
	}
}

func TestRegistry_Validate_MissingSecret(t *testing.T) {
	r, _ := newTestRegistry(t)
	p := Peer{ID: "abcd", KnownAddresses: []string{"x"}}
	if err := r.Add(p); err == nil {
		t.Fatal("expected validation error for missing inbound secret")
	}
}

func TestRegistry_Validate_MissingOutbound(t *testing.T) {
	r, _ := newTestRegistry(t)
	in, _ := NewPeerSecret()
	p := Peer{ID: "abcd", KnownAddresses: []string{"x"}, InboundSecret: in}
	if err := r.Add(p); err == nil {
		t.Fatal("expected validation error for missing outbound secret")
	}
}

func TestRegistry_Validate_MissingID(t *testing.T) {
	r, _ := newTestRegistry(t)
	secret, _ := NewPeerSecret()
	p := Peer{InboundSecret: secret, OutboundSecret: secret, KnownAddresses: []string{"x"}}
	if err := r.Add(p); err == nil {
		t.Fatal("expected validation error for missing ID")
	}
}

func TestRegistry_Stats_Empty(t *testing.T) {
	r, _ := newTestRegistry(t)
	st, err := r.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if st.Total != 0 || st.TotalViolations != 0 || st.NeverSeen != 0 {
		t.Errorf("Stats on empty registry = %+v", st)
	}
}

func TestRegistry_Stats_CountsAndViolations(t *testing.T) {
	r, _ := newTestRegistry(t)
	alice := freshPeer(t, "alice-addr")
	bob := freshPeer(t, "bob-addr")
	if err := r.Add(alice); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(bob); err != nil {
		t.Fatal(err)
	}

	_ = r.RecordViolation(alice.ID, "bad-1")
	_ = r.RecordViolation(alice.ID, "bad-1")
	_ = r.RecordViolation(alice.ID, "bad-2")
	_ = r.TouchPresence(alice.ID, time.Unix(1_700_000_000, 0), "haoma")

	st, err := r.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if st.Total != 2 {
		t.Errorf("Total = %d, want 2", st.Total)
	}
	if st.TotalViolations != 3 {
		t.Errorf("TotalViolations = %d, want 3", st.TotalViolations)
	}
	if st.PerPeerViolations[alice.ID] != 3 {
		t.Errorf("PerPeerViolations[alice] = %d, want 3", st.PerPeerViolations[alice.ID])
	}
	if st.PerPeerViolations[bob.ID] != 0 {
		t.Errorf("PerPeerViolations[bob] = %d, want 0", st.PerPeerViolations[bob.ID])
	}
	if st.NeverSeen != 1 {
		t.Errorf("NeverSeen = %d, want 1 (Bob)", st.NeverSeen)
	}
}

func TestRegistry_ResetIDS(t *testing.T) {
	r, _ := newTestRegistry(t)
	alice := freshPeer(t, "alice")
	if err := r.Add(alice); err != nil {
		t.Fatal(err)
	}
	_ = r.RecordViolation(alice.ID, "x")
	_ = r.RecordViolation(alice.ID, "y")

	if err := r.ResetIDS(alice.ID); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.IDSCounters) != 0 {
		t.Errorf("IDSCounters after reset = %v, want empty", got.IDSCounters)
	}
}

func TestRegistry_ResetIDS_UnknownPeer(t *testing.T) {
	r, _ := newTestRegistry(t)
	if err := r.ResetIDS("nobody"); !errors.Is(err, ErrPeerNotFound) {
		t.Errorf("ResetIDS on unknown peer: err = %v, want ErrPeerNotFound", err)
	}
}

func TestRegistry_Add_PopulatesAddrIndex(t *testing.T) {
	r, s := newTestRegistry(t)
	p := freshPeer(t, "slot-0-addr", "slot-1-addr")
	if err := r.Add(p); err != nil {
		t.Fatal(err)
	}
	for _, addr := range []string{"slot-0-addr", "slot-1-addr"} {
		got, err := s.GetAddrIndex(addr)
		if err != nil {
			t.Errorf("addr index miss for %q: %v", addr, err)
			continue
		}
		if string(got) != p.ID {
			t.Errorf("addr[%q] = %q, want %q", addr, string(got), p.ID)
		}
	}
}

func TestRegistry_Remove_ClearsAddrIndex(t *testing.T) {
	r, s := newTestRegistry(t)
	p := freshPeer(t, "dead-0", "dead-1")
	if err := r.Add(p); err != nil {
		t.Fatal(err)
	}
	if err := r.Remove(p.ID); err != nil {
		t.Fatal(err)
	}
	for _, addr := range []string{"dead-0", "dead-1"} {
		if _, err := s.GetAddrIndex(addr); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("addr[%q] not cleared on Remove: err = %v", addr, err)
		}
	}

	if _, err := r.Get(p.ID); !errors.Is(err, ErrPeerNotFound) {
		t.Errorf("primary record after Remove: err = %v, want ErrPeerNotFound", err)
	}
}

func TestRegistry_Retire(t *testing.T) {
	r, s := newTestRegistry(t)
	fixed := time.Unix(1_750_000_000, 0)
	r.Now = func() time.Time { return fixed }

	p := freshPeer(t, "onion-a", "onion-b")
	if err := r.Add(p); err != nil {
		t.Fatal(err)
	}

	if err := r.Retire(p.ID); err != nil {
		t.Fatalf("Retire: %v", err)
	}

	got, err := r.Get(p.ID)
	if err != nil {
		t.Fatalf("Get after Retire: %v", err)
	}
	if got.RetiredAt != fixed.Unix() {
		t.Errorf("RetiredAt = %d, want %d", got.RetiredAt, fixed.Unix())
	}
	if got.InboundSecret != nil || got.OutboundSecret != nil {
		t.Errorf("secrets = (in=%x, out=%x), want both nil", got.InboundSecret, got.OutboundSecret)
	}
	if got.KnownAddresses != nil {
		t.Errorf("KnownAddresses = %v, want nil", got.KnownAddresses)
	}

	for _, addr := range []string{"onion-a", "onion-b"} {
		if _, err := s.GetAddrIndex(addr); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("addr index for %q still present: err = %v", addr, err)
		}
	}
}

func TestRegistry_Retire_Idempotent(t *testing.T) {
	r, _ := newTestRegistry(t)

	first := time.Unix(1_750_000_000, 0)
	r.Now = func() time.Time { return first }
	p := freshPeer(t, "onion-a")
	if err := r.Add(p); err != nil {
		t.Fatal(err)
	}
	if err := r.Retire(p.ID); err != nil {
		t.Fatal(err)
	}

	later := time.Unix(1_760_000_000, 0)
	r.Now = func() time.Time { return later }
	if err := r.Retire(p.ID); err != nil {
		t.Fatalf("second Retire: %v", err)
	}
	got, err := r.Get(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RetiredAt != first.Unix() {
		t.Errorf("RetiredAt = %d after second Retire, want %d (pinned)", got.RetiredAt, first.Unix())
	}
}

func TestRegistry_Retire_Unknown(t *testing.T) {
	r, _ := newTestRegistry(t)
	if err := r.Retire("deadbeef"); !errors.Is(err, ErrPeerNotFound) {
		t.Errorf("Retire(unknown): err = %v, want ErrPeerNotFound", err)
	}
}

func TestRegistry_ByAddress_UsesAddrIndex_NotScan(t *testing.T) {

	r, _ := newTestRegistry(t)
	p := freshPeer(t, "stale-addr")
	if err := r.Add(p); err != nil {
		t.Fatal(err)
	}
	if err := r.Retire(p.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := r.ByAddress("stale-addr"); !errors.Is(err, ErrPeerNotFound) {
		t.Errorf("ByAddress found peer via dropped address: err = %v", err)
	}
}

func TestRegistry_AddGet_MyOnion(t *testing.T) {
	r, _ := newTestRegistry(t)
	p := freshPeer(t, "their-onion")
	p.MyOnionAddr = "myonion4567890123456789012345678901234567890123456789abcd"
	p.MyOnionPrivateKey = "QkFTRTY0RVhQQU5ERURLRVk="
	if err := r.Add(p); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MyOnionAddr != p.MyOnionAddr {
		t.Errorf("MyOnionAddr = %q, want %q", got.MyOnionAddr, p.MyOnionAddr)
	}
	if got.MyOnionPrivateKey != p.MyOnionPrivateKey {
		t.Errorf("MyOnionPrivateKey = %q, want %q", got.MyOnionPrivateKey, p.MyOnionPrivateKey)
	}
}

func TestRegistry_OverlayPeerAddress(t *testing.T) {
	r, s := newTestRegistry(t)
	p := freshPeer(t, "old-onion")
	if err := r.Add(p); err != nil {
		t.Fatal(err)
	}
	if err := r.OverlayPeerAddress(p.ID, "new-onion"); err != nil {
		t.Fatalf("OverlayPeerAddress: %v", err)
	}
	got, err := r.Get(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.KnownAddresses) != 2 || got.KnownAddresses[0] != "new-onion" || got.KnownAddresses[1] != "old-onion" {
		t.Errorf("KnownAddresses = %v, want [new-onion old-onion]", got.KnownAddresses)
	}

	for _, addr := range []string{"new-onion", "old-onion"} {
		got, err := s.GetAddrIndex(addr)
		if err != nil {
			t.Errorf("GetAddrIndex(%q): %v", addr, err)
			continue
		}
		if string(got) != p.ID {
			t.Errorf("addr index %q -> %s, want %s", addr, got, p.ID)
		}
	}
}

func TestRegistry_OverlayPeerAddress_Idempotent(t *testing.T) {
	r, _ := newTestRegistry(t)
	p := freshPeer(t, "current-onion")
	if err := r.Add(p); err != nil {
		t.Fatal(err)
	}

	if err := r.OverlayPeerAddress(p.ID, "current-onion"); err != nil {
		t.Fatal(err)
	}
	got, _ := r.Get(p.ID)
	if len(got.KnownAddresses) != 1 || got.KnownAddresses[0] != "current-onion" {
		t.Errorf("KnownAddresses = %v, want [current-onion]", got.KnownAddresses)
	}
}

func TestRegistry_OverlayPeerAddress_DropsDuplicate(t *testing.T) {
	r, _ := newTestRegistry(t)

	p := freshPeer(t, "addr-a", "addr-b")
	if err := r.Add(p); err != nil {
		t.Fatal(err)
	}
	if err := r.OverlayPeerAddress(p.ID, "addr-b"); err != nil {
		t.Fatal(err)
	}
	got, _ := r.Get(p.ID)
	if len(got.KnownAddresses) != 2 || got.KnownAddresses[0] != "addr-b" || got.KnownAddresses[1] != "addr-a" {
		t.Errorf("KnownAddresses = %v, want [addr-b addr-a]", got.KnownAddresses)
	}
}

func TestRegistry_OverlayPeerAddress_RetiredPeerRejected(t *testing.T) {
	r, _ := newTestRegistry(t)
	p := freshPeer(t, "old-onion")
	if err := r.Add(p); err != nil {
		t.Fatal(err)
	}
	if err := r.Retire(p.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.OverlayPeerAddress(p.ID, "new-onion"); err == nil {
		t.Errorf("OverlayPeerAddress on retired peer accepted, want error")
	}
}

func TestRegistry_CollapsePeerAddress(t *testing.T) {
	r, s := newTestRegistry(t)

	r.CollapseGrace = -1
	p := freshPeer(t, "new-onion", "old-onion")
	if err := r.Add(p); err != nil {
		t.Fatal(err)
	}
	if err := r.CollapsePeerAddress(p.ID, "new-onion"); err != nil {
		t.Fatalf("CollapsePeerAddress: %v", err)
	}
	got, _ := r.Get(p.ID)
	if len(got.KnownAddresses) != 1 || got.KnownAddresses[0] != "new-onion" {
		t.Errorf("KnownAddresses = %v, want [new-onion]", got.KnownAddresses)
	}

	if _, err := s.GetAddrIndex("old-onion"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("addr index for old-onion still present: %v", err)
	}
	if _, err := s.GetAddrIndex("new-onion"); err != nil {
		t.Errorf("addr index for new-onion lost: %v", err)
	}
}

func TestRegistry_ByAddress_HitsSoftRetired(t *testing.T) {
	r, _ := newTestRegistry(t)
	r.CollapseGrace = 60 * time.Second
	clock := time.Unix(2_000_000, 0)
	r.Now = func() time.Time { return clock }

	p := freshPeer(t, "new-onion", "old-onion")
	if err := r.Add(p); err != nil {
		t.Fatal(err)
	}
	if err := r.CollapsePeerAddress(p.ID, "new-onion"); err != nil {
		t.Fatalf("CollapsePeerAddress: %v", err)
	}

	got, err := r.ByAddress("old-onion")
	if err != nil {
		t.Fatalf("ByAddress(old-onion) during grace: %v", err)
	}
	if got.ID != p.ID {
		t.Errorf("ByAddress returned peer %q, want %q", got.ID, p.ID)
	}

	if _, err := r.SweepRetiredAddrs(2_000_000 + 60); err != nil {
		t.Fatalf("SweepRetiredAddrs: %v", err)
	}
	if _, err := r.ByAddress("old-onion"); !errors.Is(err, ErrPeerNotFound) {
		t.Errorf("ByAddress(old-onion) after sweep err = %v, want ErrPeerNotFound", err)
	}
}

func TestRegistry_CollapsePeerAddress_GraceAndSweep(t *testing.T) {
	r, s := newTestRegistry(t)
	r.CollapseGrace = 60 * time.Second
	clock := time.Unix(1_000_000, 0)
	r.Now = func() time.Time { return clock }

	p := freshPeer(t, "new-onion", "old-onion")
	if err := r.Add(p); err != nil {
		t.Fatal(err)
	}
	if err := r.CollapsePeerAddress(p.ID, "new-onion"); err != nil {
		t.Fatalf("CollapsePeerAddress: %v", err)
	}

	got, _ := r.Get(p.ID)
	if len(got.KnownAddresses) != 1 || got.KnownAddresses[0] != "new-onion" {
		t.Errorf("KnownAddresses = %v, want [new-onion]", got.KnownAddresses)
	}
	if len(got.RetiredAddrs) != 1 || got.RetiredAddrs[0].Address != "old-onion" {
		t.Errorf("RetiredAddrs = %v, want [{old-onion, 1000060}]", got.RetiredAddrs)
	}
	if got.RetiredAddrs[0].ExpiresAt != 1_000_000+60 {
		t.Errorf("ExpiresAt = %d, want %d", got.RetiredAddrs[0].ExpiresAt, 1_000_000+60)
	}

	if _, err := s.GetAddrIndex("old-onion"); err != nil {
		t.Errorf("addr index for old-onion dropped during grace: %v", err)
	}

	swept, err := r.SweepRetiredAddrs(1_000_000 + 30)
	if err != nil {
		t.Fatalf("SweepRetiredAddrs (pre-expiry): %v", err)
	}
	if swept != 0 {
		t.Errorf("swept = %d, want 0 (still mid-grace)", swept)
	}
	if _, err := s.GetAddrIndex("old-onion"); err != nil {
		t.Errorf("addr index for old-onion dropped pre-expiry: %v", err)
	}

	swept, err = r.SweepRetiredAddrs(1_000_000 + 60)
	if err != nil {
		t.Fatalf("SweepRetiredAddrs (at expiry): %v", err)
	}
	if swept != 1 {
		t.Errorf("swept = %d, want 1", swept)
	}
	if _, err := s.GetAddrIndex("old-onion"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("addr index for old-onion still present after sweep: %v", err)
	}
	if _, err := s.GetAddrIndex("new-onion"); err != nil {
		t.Errorf("addr index for new-onion lost after sweep: %v", err)
	}
	got, _ = r.Get(p.ID)
	if len(got.RetiredAddrs) != 0 {
		t.Errorf("RetiredAddrs after sweep = %v, want empty", got.RetiredAddrs)
	}
}

func TestRegistry_CollapsePeerAddress_RetainNotInList(t *testing.T) {
	r, _ := newTestRegistry(t)
	p := freshPeer(t, "addr-a")
	if err := r.Add(p); err != nil {
		t.Fatal(err)
	}
	if err := r.CollapsePeerAddress(p.ID, "addr-b"); err == nil {
		t.Errorf("CollapsePeerAddress with retain not in list accepted, want error")
	}

	got, _ := r.Get(p.ID)
	if len(got.KnownAddresses) != 1 || got.KnownAddresses[0] != "addr-a" {
		t.Errorf("KnownAddresses = %v, want unchanged [addr-a]", got.KnownAddresses)
	}
}

func TestRegistry_RotateOwnOnion(t *testing.T) {

	r, _ := newTestRegistry(t)
	r.CollapseGrace = -1
	p := freshPeer(t, "their-onion")
	p.MyOnionAddr = "old-my-onion"
	p.MyOnionPrivateKey = "old-priv"
	if err := r.Add(p); err != nil {
		t.Fatal(err)
	}
	old, err := r.RotateOwnOnion(p.ID, "new-my-onion", "new-priv")
	if err != nil {
		t.Fatalf("RotateOwnOnion: %v", err)
	}
	if old != "old-my-onion" {
		t.Errorf("returned old = %q, want %q", old, "old-my-onion")
	}
	got, _ := r.Get(p.ID)
	if got.MyOnionAddr != "new-my-onion" {
		t.Errorf("MyOnionAddr = %q, want new-my-onion", got.MyOnionAddr)
	}
	if got.MyOnionPrivateKey != "new-priv" {
		t.Errorf("MyOnionPrivateKey = %q, want new-priv", got.MyOnionPrivateKey)
	}
	if got.PrevMyOnion != nil {
		t.Errorf("PrevMyOnion = %v, want nil (immediate-drop mode)", got.PrevMyOnion)
	}
}

func TestRegistry_RotateOwnOnion_GraceAndSweep(t *testing.T) {
	r, _ := newTestRegistry(t)
	r.CollapseGrace = 60 * time.Second
	clock := time.Unix(3_000_000, 0)
	r.Now = func() time.Time { return clock }

	p := freshPeer(t, "their-onion")
	p.MyOnionAddr = "old-my-onion"
	p.MyOnionPrivateKey = "old-priv"
	if err := r.Add(p); err != nil {
		t.Fatal(err)
	}
	old, err := r.RotateOwnOnion(p.ID, "new-my-onion", "new-priv")
	if err != nil {
		t.Fatalf("RotateOwnOnion: %v", err)
	}
	if old != "" {
		t.Errorf("RotateOwnOnion returned %q, want empty (steady-state with grace)", old)
	}
	got, _ := r.Get(p.ID)
	if got.MyOnionAddr != "new-my-onion" {
		t.Errorf("MyOnionAddr = %q, want new-my-onion", got.MyOnionAddr)
	}
	if got.PrevMyOnion == nil {
		t.Fatalf("PrevMyOnion is nil, want grace-slot snapshot")
	}
	if got.PrevMyOnion.Address != "old-my-onion" || got.PrevMyOnion.PrivateKey != "old-priv" {
		t.Errorf("PrevMyOnion = %+v, want {old-my-onion, old-priv}", got.PrevMyOnion)
	}
	if got.PrevMyOnion.ExpiresAt != 3_000_000+60 {
		t.Errorf("PrevMyOnion.ExpiresAt = %d, want %d", got.PrevMyOnion.ExpiresAt, 3_000_000+60)
	}

	toDel, err := r.SweepRetiredOwnOnions(3_000_000 + 30)
	if err != nil {
		t.Fatalf("SweepRetiredOwnOnions (pre-expiry): %v", err)
	}
	if len(toDel) != 0 {
		t.Errorf("toDel = %v, want empty (still mid-grace)", toDel)
	}
	got, _ = r.Get(p.ID)
	if got.PrevMyOnion == nil {
		t.Errorf("PrevMyOnion dropped pre-expiry")
	}

	toDel, err = r.SweepRetiredOwnOnions(3_000_000 + 60)
	if err != nil {
		t.Fatalf("SweepRetiredOwnOnions (at expiry): %v", err)
	}
	if len(toDel) != 1 || toDel[0] != "old-my-onion" {
		t.Errorf("toDel = %v, want [old-my-onion]", toDel)
	}
	got, _ = r.Get(p.ID)
	if got.PrevMyOnion != nil {
		t.Errorf("PrevMyOnion = %+v after sweep, want nil", got.PrevMyOnion)
	}
}

func TestRegistry_RotateOwnOnion_DoubleRotation_EvictsPrev(t *testing.T) {
	r, _ := newTestRegistry(t)
	r.CollapseGrace = 60 * time.Second
	clock := time.Unix(4_000_000, 0)
	r.Now = func() time.Time { return clock }

	p := freshPeer(t, "their-onion")
	p.MyOnionAddr = "addr-0"
	p.MyOnionPrivateKey = "priv-0"
	if err := r.Add(p); err != nil {
		t.Fatal(err)
	}

	if old, err := r.RotateOwnOnion(p.ID, "addr-1", "priv-1"); err != nil {
		t.Fatalf("first RotateOwnOnion: %v", err)
	} else if old != "" {
		t.Errorf("first rotation returned %q, want empty", old)
	}

	old, err := r.RotateOwnOnion(p.ID, "addr-2", "priv-2")
	if err != nil {
		t.Fatalf("second RotateOwnOnion: %v", err)
	}
	if old != "addr-0" {
		t.Errorf("second rotation returned %q, want addr-0 (evicted prev)", old)
	}
	got, _ := r.Get(p.ID)
	if got.MyOnionAddr != "addr-2" {
		t.Errorf("MyOnionAddr = %q, want addr-2", got.MyOnionAddr)
	}
	if got.PrevMyOnion == nil || got.PrevMyOnion.Address != "addr-1" {
		t.Errorf("PrevMyOnion = %+v, want {addr-1, ...}", got.PrevMyOnion)
	}
}

func TestRegistry_RotateOwnOnion_NoPrior(t *testing.T) {

	r, _ := newTestRegistry(t)
	p := freshPeer(t, "their-onion")
	if err := r.Add(p); err != nil {
		t.Fatal(err)
	}
	old, err := r.RotateOwnOnion(p.ID, "new-my-onion", "new-priv")
	if err != nil {
		t.Fatalf("RotateOwnOnion: %v", err)
	}
	if old != "" {
		t.Errorf("returned old = %q, want empty (no prior MyOnionAddr)", old)
	}
}

func TestRegistry_RotateOwnOnion_RetiredPeerRejected(t *testing.T) {
	r, _ := newTestRegistry(t)
	p := freshPeer(t, "their-onion")
	if err := r.Add(p); err != nil {
		t.Fatal(err)
	}
	if err := r.Retire(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RotateOwnOnion(p.ID, "new-onion", "new-priv"); err == nil {
		t.Errorf("RotateOwnOnion on retired peer accepted, want error")
	}
}

func TestRegistry_RotateOwnOnion_UnknownPeer(t *testing.T) {
	r, _ := newTestRegistry(t)
	if _, err := r.RotateOwnOnion("deadbeef", "new", "priv"); !errors.Is(err, ErrPeerNotFound) {
		t.Errorf("err = %v, want ErrPeerNotFound", err)
	}
}

func TestRegistry_Retire_ClearsMyOnion(t *testing.T) {
	r, _ := newTestRegistry(t)
	p := freshPeer(t, "their-onion")
	p.MyOnionAddr = "myonion4567890123456789012345678901234567890123456789abcd"
	p.MyOnionPrivateKey = "QkFTRTY0RVhQQU5ERURLRVk="
	if err := r.Add(p); err != nil {
		t.Fatal(err)
	}
	if err := r.Retire(p.ID); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MyOnionAddr != "" {
		t.Errorf("MyOnionAddr = %q after Retire, want empty", got.MyOnionAddr)
	}
	if got.MyOnionPrivateKey != "" {
		t.Errorf("MyOnionPrivateKey = %q after Retire, want empty", got.MyOnionPrivateKey)
	}
}
