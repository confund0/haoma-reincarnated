package main

import (
	"sync"
	"testing"
	"time"
)

func TestOnionInviteRegistry_PutGetDrop(t *testing.T) {
	t.Parallel()
	r := newOnionInviteRegistry()
	if _, ok := r.get("missing"); ok {
		t.Error("get on empty registry returned ok=true")
	}
	e := &onionInviteEntry{
		HandleID:  "abc123",
		Words:     []string{"acid", "acorn", "acre", "acts", "afar", "affix", "aged"},
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
		CreatedAt: time.Now(),
	}
	r.put(e)

	got, ok := r.get("abc123")
	if !ok || got != e {
		t.Errorf("get after put: ok=%v, equal=%v", ok, got == e)
	}

	r.drop("abc123")
	if _, ok := r.get("abc123"); ok {
		t.Error("get after drop still returns ok=true")
	}

	r.drop("never-existed")
}

func TestOnionInviteRegistry_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	r := newOnionInviteRegistry()
	const N = 200
	var wg sync.WaitGroup
	wg.Add(N * 3)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			r.put(&onionInviteEntry{HandleID: handleKey(i)})
		}()
		go func() {
			defer wg.Done()
			_, _ = r.get(handleKey(i))
		}()
		go func() {
			defer wg.Done()
			r.drop(handleKey(i))
		}()
	}
	wg.Wait()

}

func handleKey(i int) string {
	const charset = "abcdef0123456789"
	out := make([]byte, 8)
	for j := range out {
		out[j] = charset[(i+j)%len(charset)]
	}
	return string(out)
}
