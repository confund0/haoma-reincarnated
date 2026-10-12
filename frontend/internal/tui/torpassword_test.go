package tui

import (
	"strings"
	"testing"
)

type recordingTorVault struct {
	stubVaultInPackage
	got *string
}

func (v recordingTorVault) SetTorPassword(s string) error { *v.got = s; return nil }

func TestSetTorPasswordOwnTorSavesAndSendsNothing(t *testing.T) {
	var saved string
	a := New(nil)
	a.VaultCtl = recordingTorVault{got: &saved}
	a.SystemTor = false
	a.winMu.Lock()
	a.haomaConnected = true
	a.winMu.Unlock()

	var panicked any
	func() {
		defer func() { panicked = recover() }()
		a.cmdSetTorPassword("hunter2")
	}()
	if panicked != nil {
		t.Fatalf("own tor: the password went to haomad (the send hit the test's nil client: %v)", panicked)
	}
	if saved != "hunter2" {
		t.Fatalf("vault got %q, want hunter2", saved)
	}
	a.pendingMu.Lock()
	pending := len(a.pending)
	a.pendingMu.Unlock()
	if pending != 0 {
		t.Fatalf("own tor: %d request(s) pending, want none", pending)
	}
	if text := a.statusView.GetText(true); !strings.Contains(text, "--system-tor only") {
		t.Fatalf("status does not say the password is for --system-tor only: %q", text)
	}
}
