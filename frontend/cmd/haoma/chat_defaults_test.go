package main

import (
	"net/http"
	"testing"

	"haoma-frontend/internal/ipc"
)

func TestCreateDirectWithDefaults_FreshChatInheritsSnapshot(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)

	d.settingsSnapshot.Store(&ipc.Settings{
		DefaultRetentionSec: 3600,
		DefaultSendReceipts: false,
	})

	dc, fresh, err := d.createDirectWithDefaults("peer-fresh")
	if err != nil {
		t.Fatalf("createDirectWithDefaults: %v", err)
	}
	if !fresh {
		t.Error("expected fresh=true on first create")
	}
	got, err := d.chats.GetByDirectPeer("peer-fresh")
	if err != nil {
		t.Fatal(err)
	}
	if got.RetentionTTL != 3600 {
		t.Errorf("RetentionTTL = %d, want 3600 (inherited from snapshot)", got.RetentionTTL)
	}
	if !got.DisableReadReceipts {
		t.Error("DisableReadReceipts = false, want true (snapshot DefaultSendReceipts=false)")
	}
	_ = dc
}

func TestCreateDirectWithDefaults_IdempotentPreservesOverrides(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)

	d.settingsSnapshot.Store(&ipc.Settings{
		DefaultRetentionSec: 60,
		DefaultSendReceipts: true,
	})
	if _, _, err := d.createDirectWithDefaults("peer-stable"); err != nil {
		t.Fatal(err)
	}

	chatID, _ := d.chats.GetByDirectPeer("peer-stable")
	if err := d.chats.SetRetentionTTL(chatID.ID, 86400); err != nil {
		t.Fatal(err)
	}
	if err := d.chats.SetDisableReadReceipts(chatID.ID, true); err != nil {
		t.Fatal(err)
	}

	d.settingsSnapshot.Store(&ipc.Settings{
		DefaultRetentionSec: 600,
		DefaultSendReceipts: true,
	})

	dc, fresh, err := d.createDirectWithDefaults("peer-stable")
	if err != nil {
		t.Fatal(err)
	}
	if fresh {
		t.Error("expected fresh=false on re-entry to existing chat")
	}
	if dc.RetentionTTL != 86400 {
		t.Errorf("RetentionTTL = %d, want 86400 (per-chat override preserved)", dc.RetentionTTL)
	}
	if !dc.DisableReadReceipts {
		t.Error("DisableReadReceipts should remain true (per-chat override)")
	}
}

func TestCreateDirectWithDefaults_NilSnapshotIsGraceful(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)

	d.settingsSnapshot.Store(nil)

	dc, fresh, err := d.createDirectWithDefaults("peer-nilcase")
	if err != nil {
		t.Fatalf("createDirectWithDefaults with nil snapshot: %v", err)
	}
	if !fresh {
		t.Error("fresh expected on first create")
	}
	if dc.RetentionTTL != 0 {
		t.Errorf("RetentionTTL = %d, want 0 (zero-value with nil snapshot)", dc.RetentionTTL)
	}
	if dc.DisableReadReceipts {
		t.Error("DisableReadReceipts should remain false-default with nil snapshot")
	}
}
