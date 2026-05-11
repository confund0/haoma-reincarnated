package identity

import (
	"context"
	"os"
	"testing"
	"time"

	"haoma/internal/store"
	"haoma/internal/tor/control"
)

const (
	realTorAddrEnv = "HAOMA_REAL_TOR_CTRL"
	realTorPassEnv = "HAOMA_REAL_TOR_PASS"
)

func requireRealTor(t *testing.T) (addr, pass string) {
	t.Helper()
	addr = os.Getenv(realTorAddrEnv)
	if addr == "" {
		t.Skipf("%s not set — skipping live tor test", realTorAddrEnv)
	}
	pass = os.Getenv(realTorPassEnv)
	return addr, pass
}

func TestRealTor_Identity_RoundTrip(t *testing.T) {
	addr, pass := requireRealTor(t)
	dir := t.TempDir()
	portsPerSlot := [][]control.OnionPort{
		{{VirtPort: 80, Target: "127.0.0.1:54321"}},
		{{VirtPort: 80, Target: "127.0.0.1:54322"}},
	}

	ids1 := roundTripOnce(t, dir, addr, pass, portsPerSlot, true)
	t.Logf("first pass  service IDs: %v", ids1)

	ids2 := roundTripOnce(t, dir, addr, pass, portsPerSlot, false)
	t.Logf("second pass service IDs: %v", ids2)

	if !equalStrings(ids1, ids2) {
		t.Fatalf("ServiceIDs changed across restart: %v vs %v", ids1, ids2)
	}
}

func roundTripOnce(t *testing.T, dir, addr, pass string, portsPerSlot [][]control.OnionPort, isFresh bool) []string {
	t.Helper()
	s, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatalf("store.Unlock: %v", err)
	}
	defer s.Lock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := control.Dial(ctx, addr)
	if err != nil {
		t.Fatalf("control.Dial: %v", err)
	}
	defer c.Close()
	if _, err := c.Authenticate(pass); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	id, err := LoadOrPublish(s, c, portsPerSlot)
	if err != nil {
		t.Fatalf("LoadOrPublish (fresh=%v): %v", isFresh, err)
	}
	if len(id.Active) != SlotCount {
		t.Fatalf("Active length = %d, want %d", len(id.Active), SlotCount)
	}
	ids := id.ServiceIDs()

	for _, sid := range ids {
		if err := c.DelOnion(sid); err != nil {
			t.Logf("DelOnion %s: %v", sid, err)
		}
	}
	return ids
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
