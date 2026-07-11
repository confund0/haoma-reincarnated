package peerstate_test

import (
	"strings"
	"sync"
	"testing"

	"haoma-frontend/internal/peerstate"
	"haoma-frontend/internal/store"
)

func init() {
	store.DefaultKDFParams = store.KDFParams{
		Time: 1, Memory: 8 * 1024, Threads: 2, KeyLen: 32, SaltLen: 16,
	}
}

func newCounters(t *testing.T) (*peerstate.Counters, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Lock() })
	return peerstate.New(st), dir
}

func newMeta(t *testing.T) (*peerstate.Meta, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Lock() })
	return peerstate.NewMeta(st), dir
}

func TestSetAlias_StampsAliasAt(t *testing.T) {
	m, _ := newMeta(t)
	if _, err := m.SetAlias("alice", "Ally", 1000); err != nil {
		t.Fatal(err)
	}
	rec, _ := m.Get("alice")
	if rec.Alias != "Ally" || rec.AliasAt != 1000 {
		t.Fatalf("alias=%q at=%d, want Ally/1000", rec.Alias, rec.AliasAt)
	}

	if _, err := m.SetAlias("alice", "Al", 2000); err != nil {
		t.Fatal(err)
	}
	if rec, _ := m.Get("alice"); rec.AliasAt != 2000 {
		t.Errorf("AliasAt = %d, want 2000", rec.AliasAt)
	}
	if _, err := m.SetAlias("alice", "stale", 500); err != nil {
		t.Fatal(err)
	}
	if rec, _ := m.Get("alice"); rec.AliasAt != 2000 {
		t.Errorf("AliasAt = %d after stale write, want 2000 (no backward move)", rec.AliasAt)
	}
}

func TestSetVerified_LWW(t *testing.T) {
	m, _ := newMeta(t)
	changed, err := m.SetVerified("alice", true, 1000)
	if err != nil || !changed {
		t.Fatalf("first set: changed=%v err=%v, want changed=true", changed, err)
	}

	if changed, _ := m.SetVerified("alice", false, 500); changed {
		t.Error("stale verified write should be rejected")
	}
	if rec, _ := m.Get("alice"); !rec.Verified || rec.VerifiedAt != 1000 {
		t.Errorf("verified=%v at=%d, want true/1000 (stale rejected)", rec.Verified, rec.VerifiedAt)
	}

	if changed, _ := m.SetVerified("alice", false, 2000); !changed {
		t.Error("newer verified write should apply")
	}
	if rec, _ := m.Get("alice"); rec.Verified || rec.VerifiedAt != 2000 {
		t.Errorf("verified=%v at=%d, want false/2000", rec.Verified, rec.VerifiedAt)
	}
}

func TestSetBlocked_LWWAndReportsChange(t *testing.T) {
	m, _ := newMeta(t)
	if changed, _ := m.SetBlocked("alice", true, 1000); !changed {
		t.Error("first block should report changed")
	}
	if changed, _ := m.SetBlocked("alice", true, 1500); changed {
		t.Error("re-blocking (same value) should report changed=false")
	}
	if rec, _ := m.Get("alice"); !rec.Blocked || rec.BlockedAt != 1500 {
		t.Errorf("blocked=%v at=%d, want true/1500 (At bumps even on same-value)", rec.Blocked, rec.BlockedAt)
	}
}

func TestSetVerified_RejectsEmptyPeerID(t *testing.T) {
	m, _ := newMeta(t)
	if _, err := m.SetVerified("", true, 1); err == nil {
		t.Error("empty peer id should error")
	}
}

func TestSetFingerprint_SetsAndReports(t *testing.T) {
	m, _ := newMeta(t)
	changed, prev, err := m.SetFingerprint("alice", "fp-abc")
	if err != nil {
		t.Fatal(err)
	}
	if !changed || prev != "" {
		t.Errorf("first seed: changed=%v prev=%q, want changed=true prev empty", changed, prev)
	}
	rec, err := m.Get("alice")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Fingerprint != "fp-abc" {
		t.Errorf("Fingerprint = %q, want fp-abc", rec.Fingerprint)
	}
}

func TestSetFingerprint_Idempotent(t *testing.T) {
	m, _ := newMeta(t)
	if _, _, err := m.SetFingerprint("alice", "fp-abc"); err != nil {
		t.Fatal(err)
	}
	changed, prev, err := m.SetFingerprint("alice", "fp-abc")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("re-seeding the same fingerprint should report changed=false")
	}
	if prev != "fp-abc" {
		t.Errorf("prev = %q, want fp-abc", prev)
	}
}

func TestSetFingerprint_ChangeReportsPrev(t *testing.T) {
	m, _ := newMeta(t)
	if _, _, err := m.SetFingerprint("alice", "fp-old"); err != nil {
		t.Fatal(err)
	}

	changed, prev, err := m.SetFingerprint("alice", "fp-new")
	if err != nil {
		t.Fatal(err)
	}
	if !changed || prev != "fp-old" {
		t.Errorf("change: changed=%v prev=%q, want changed=true prev=fp-old", changed, prev)
	}
	rec, _ := m.Get("alice")
	if rec.Fingerprint != "fp-new" {
		t.Errorf("Fingerprint = %q, want fp-new (last write wins)", rec.Fingerprint)
	}
}

func TestSetFingerprint_EmptyIsNoOp(t *testing.T) {
	m, _ := newMeta(t)
	if _, _, err := m.SetFingerprint("alice", "fp-abc"); err != nil {
		t.Fatal(err)
	}
	changed, _, err := m.SetFingerprint("alice", "")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("empty fingerprint must be a no-op, not clear the record")
	}
	rec, _ := m.Get("alice")
	if rec.Fingerprint != "fp-abc" {
		t.Errorf("Fingerprint = %q, want fp-abc (empty must not blank it)", rec.Fingerprint)
	}
}

func TestSetFingerprint_PersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := peerstate.NewMeta(st).SetFingerprint("alice", "fp-persist"); err != nil {
		t.Fatal(err)
	}
	if err := st.Lock(); err != nil {
		t.Fatal(err)
	}
	st2, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st2.Lock() })
	rec, err := peerstate.NewMeta(st2).Get("alice")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Fingerprint != "fp-persist" {
		t.Errorf("Fingerprint after reopen = %q, want fp-persist", rec.Fingerprint)
	}
}

func TestSetFingerprint_RejectsEmptyPeerID(t *testing.T) {
	m, _ := newMeta(t)
	if _, _, err := m.SetFingerprint("", "fp"); err == nil {
		t.Error("empty peer id should error")
	}
}

func TestNextSendSeq_StartsAtOne(t *testing.T) {
	c, _ := newCounters(t)
	got, err := c.NextSendSeq("alice")
	if err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Errorf("first NextSendSeq = %d, want 1", got)
	}
}

func TestNextSendSeq_Monotonic(t *testing.T) {
	c, _ := newCounters(t)
	for want := uint64(1); want <= 10; want++ {
		got, err := c.NextSendSeq("alice")
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("call %d returned %d, want %d", want, got, want)
		}
	}
}

func TestNextSendSeq_PerPeerSeparate(t *testing.T) {
	c, _ := newCounters(t)
	if _, err := c.NextSendSeq("alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.NextSendSeq("alice"); err != nil {
		t.Fatal(err)
	}
	bob, err := c.NextSendSeq("bob")
	if err != nil {
		t.Fatal(err)
	}
	if bob != 1 {
		t.Errorf("bob's first seq = %d, want 1 (per-peer counters)", bob)
	}
	alice, err := c.NextSendSeq("alice")
	if err != nil {
		t.Fatal(err)
	}
	if alice != 3 {
		t.Errorf("alice's third seq = %d, want 3", alice)
	}
}

func TestNextSendSeq_PersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	c := peerstate.New(st)
	for i := 0; i < 5; i++ {
		if _, err := c.NextSendSeq("alice"); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Lock(); err != nil {
		t.Fatal(err)
	}

	st2, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st2.Lock() })
	c2 := peerstate.New(st2)
	got, err := c2.NextSendSeq("alice")
	if err != nil {
		t.Fatal(err)
	}
	if got != 6 {
		t.Errorf("after reopen, NextSendSeq = %d, want 6", got)
	}
}

func TestPeekSendSeq_NoSendYet_ReturnsZero(t *testing.T) {
	c, _ := newCounters(t)
	got, err := c.PeekSendSeq("alice")
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Errorf("peek before any send = %d, want 0", got)
	}
}

func TestPeekSendSeq_AfterNext_ReturnsLast(t *testing.T) {
	c, _ := newCounters(t)
	if _, err := c.NextSendSeq("alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.NextSendSeq("alice"); err != nil {
		t.Fatal(err)
	}
	got, err := c.PeekSendSeq("alice")
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Errorf("peek after 2 sends = %d, want 2", got)
	}
}

func TestNextSendSeq_RejectsEmptyPeerID(t *testing.T) {
	c, _ := newCounters(t)
	_, err := c.NextSendSeq("")
	if err == nil || !strings.Contains(err.Error(), "empty peer id") {
		t.Fatalf("err = %v, want empty-peer-id error", err)
	}
}

func TestNextSendSeq_Concurrent(t *testing.T) {
	c, _ := newCounters(t)
	const N = 50
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		seen = make(map[uint64]int, N)
	)
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			v, err := c.NextSendSeq("alice")
			if err != nil {
				t.Errorf("NextSendSeq: %v", err)
				return
			}
			mu.Lock()
			seen[v]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(seen) != N {
		t.Fatalf("got %d distinct seqs, want %d", len(seen), N)
	}
	for v := uint64(1); v <= N; v++ {
		if seen[v] != 1 {
			t.Errorf("seq %d appeared %d times, want 1", v, seen[v])
		}
	}
	last, err := c.PeekSendSeq("alice")
	if err != nil {
		t.Fatal(err)
	}
	if last != N {
		t.Errorf("peek after %d sends = %d, want %d", N, last, N)
	}
}
