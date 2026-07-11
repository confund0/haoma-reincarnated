package session_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"haoma-frontend/internal/session"
)

func TestForkDiagSmoke(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx := context.Background()
	alice, bob := pairAlice(t)
	aCipher := session.New(alice)
	bCipher := session.New(bob)

	blob, err := aCipher.Encrypt(ctx, bobID, []byte(`{"kind":"presence"}`))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := bCipher.Decrypt(ctx, aliceID, blob); err != nil {
		t.Fatalf("decrypt: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "ratchet.op") {
		t.Fatalf("no ratchet.op line emitted:\n%s", out)
	}
	if !strings.Contains(out, "root_changed=") {
		t.Fatalf("ring missing root_changed field:\n%s", out)
	}
	t.Logf("ring output:\n%s", out)
}
