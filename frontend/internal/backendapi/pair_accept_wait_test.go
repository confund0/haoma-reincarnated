package backendapi

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestPairOnionAccept_OutwaitsTheShortClient(t *testing.T) {
	const shortLimit = 50 * time.Millisecond
	const answerAfter = 300 * time.Millisecond

	fb := newFakeBackend(t)
	fb.on(http.MethodPost, "/pair/onion/accept", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(answerAfter):
		case <-r.Context().Done():
			return
		}
		jsonReply(w, http.StatusOK, PairOnionAcceptResponse{InviterPayload: []byte("inviter")})
	})
	c := New(fb.URL, "", &http.Client{Timeout: shortLimit})

	t.Run("words", func(t *testing.T) {
		out, err := c.PairOnionAccept(context.Background(), []string{"acid", "acorn", "acre"}, "", []byte("joiner"))
		if err != nil {
			t.Fatalf("PairOnionAccept gave up before the daemon answered: %v", err)
		}
		if string(out.InviterPayload) != "inviter" {
			t.Fatalf("inviter payload = %q", out.InviterPayload)
		}
	})

	t.Run("secret", func(t *testing.T) {
		out, err := c.PairOnionAcceptSecret(context.Background(), "a-secret", []byte("joiner"))
		if err != nil {
			t.Fatalf("PairOnionAcceptSecret gave up before the daemon answered: %v", err)
		}
		if string(out.InviterPayload) != "inviter" {
			t.Fatalf("inviter payload = %q", out.InviterPayload)
		}
	})
}

func TestPairOnionAccept_StopsWithTheContext(t *testing.T) {
	fb := newFakeBackend(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	fb.on(http.MethodPost, "/pair/onion/accept", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	c := New(fb.URL, "", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.PairOnionAccept(ctx, []string{"acid", "acorn", "acre"}, "", []byte("joiner")); err == nil {
		t.Fatal("PairOnionAccept returned no error for a context that ended")
	}
	if _, err := c.PairOnionAcceptSecret(ctx, "a-secret", []byte("joiner")); err == nil {
		t.Fatal("PairOnionAcceptSecret returned no error for a context that ended")
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("the calls ran %v past a context that ended after 100ms", waited)
	}
}
