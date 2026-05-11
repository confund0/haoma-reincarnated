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
