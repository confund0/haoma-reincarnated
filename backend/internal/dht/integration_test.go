//go:build dhtnet

package dht

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"
)

func TestIntegration_BootstrapPutGetRoundTrip(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	srv, err := NewServer(logger)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	t.Logf("bootstrap starting (local %s, self %s)", srv.LocalAddr(), srv.SelfID())
	if err := srv.Bootstrap(ctx); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Logf("bootstrap done: %d nodes in routing table", srv.NodeCount())
	if srv.NodeCount() < 8 {
		t.Fatalf("routing table too sparse after bootstrap: %d", srv.NodeCount())
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	payload := make([]byte, 200)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	salt := []byte(fmt.Sprintf("haoma-dht-integration-%d", time.Now().UnixNano()))

	item := &MutableItem{
		PrivKey: priv,
		Salt:    salt,
		Seq:     time.Now().Unix(),
		Value:   payload,
	}
	copy(item.PubKey[:], pub)
	if err := item.Sign(); err != nil {
		t.Fatalf("sign: %v", err)
	}
	target := item.Target()
	t.Logf("target=%s seq=%d", target, item.Seq)

	pre, err := srv.Get(ctx, target, &item.PubKey, salt)
	if err != nil {
		t.Fatalf("Get (pre-put): %v", err)
	}
	if pre.Value != nil {
		t.Fatalf("fresh target unexpectedly had a value: %d bytes", len(pre.Value))
	}
	t.Logf("pre-put Get: %d tokens collected", len(pre.Tokens))
	if len(pre.Tokens) < 1 {
		t.Fatalf("expected at least one put-token from pre-put Get")
	}

	stored, err := srv.Put(ctx, item, pre.Tokens)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	t.Logf("Put accepted by %d/%d nodes", stored, len(pre.Tokens))
	if stored == 0 {
		t.Fatal("Put: zero acceptances")
	}

	time.Sleep(3 * time.Second)
	post, err := srv.Get(ctx, target, &item.PubKey, salt)
	if err != nil {
		t.Fatalf("Get (post-put): %v", err)
	}
	if post.Value == nil {
		t.Fatalf("post-put Get returned no value")
	}
	if string(post.Value) != string(payload) {
		t.Fatalf("payload mismatch after round-trip:\n  in:  %x\n  out: %x", payload, post.Value)
	}
	if post.Seq != item.Seq {
		t.Errorf("seq mismatch: got %d want %d", post.Seq, item.Seq)
	}
	t.Logf("post-put Get: %d bytes, seq=%d — byte-equal ✓", len(post.Value), post.Seq)
}
