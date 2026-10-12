package session_test

import (
	"bytes"
	"context"
	"log/slog"
	"regexp"
	"strings"
	"testing"

	"haoma-frontend/internal/devicetag"
	"haoma-frontend/internal/logid"
	"haoma-frontend/internal/session"
)

func TestForkDiagSmoke(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx := context.Background()
	aState, alice := newStateAndStores(t)
	bState, bob := newStateAndStores(t)
	processBundleFor(t, alice, bobID, bState)
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

	for _, full := range []string{aliceID, bobID} {
		if strings.Contains(out, full) {
			t.Errorf("a full peer id reached the line: %q in\n%s", full, out)
		}
	}
	if !strings.Contains(out, "peer="+logid.Short(aliceID)) {
		t.Errorf("no short peer id on the line:\n%s", out)
	}
	regs := regexp.MustCompile(`rem_reg=(\S*)`).FindAllStringSubmatch(out, -1)
	if len(regs) == 0 {
		t.Fatalf("no rem_reg field:\n%s", out)
	}
	tags := map[string]bool{
		devicetag.Tag(aState.RegistrationID): true,
		devicetag.Tag(bState.RegistrationID): true,
	}
	for _, m := range regs {
		if !tags[m[1]] {
			t.Errorf("rem_reg=%q is not the device tag of either side:\n%s", m[1], out)
		}
	}
	t.Logf("ring output:\n%s", out)
}
