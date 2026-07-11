package main

import (
	"net/http"
	"testing"
)

func setTestChatDefaults(d *daemon, retentionSec uint64, sendReceipts bool) {
	d.defaultRetentionCache.Store(&retentionSec)
	d.defaultSendReceiptsCache.Store(&sendReceipts)
}

func TestCreateDirectWithDefaults_FreshChatInheritsDefaults(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)

	setTestChatDefaults(d, 3600, false)

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
		t.Errorf("RetentionTTL = %d, want 3600 (inherited from defaults)", got.RetentionTTL)
	}
	if !got.DisableReadReceipts {
		t.Error("DisableReadReceipts = false, want true (default send-receipts=false)")
	}
	_ = dc
}

func TestCreateDirectWithDefaults_IdempotentPreservesOverrides(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)

	setTestChatDefaults(d, 60, true)
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

	setTestChatDefaults(d, 600, true)

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

func TestCreateDirectWithDefaults_UnconfiguredSeedsShippedFallbacks(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)

	d.defaultRetentionCache.Store(nil)
	d.defaultSendReceiptsCache.Store(nil)

	if _, fresh, err := d.createDirectWithDefaults("peer-fallback"); err != nil {
		t.Fatalf("createDirectWithDefaults: %v", err)
	} else if !fresh {
		t.Error("fresh expected on first create")
	}

	got, err := d.chats.GetByDirectPeer("peer-fallback")
	if err != nil {
		t.Fatal(err)
	}
	if got.RetentionTTL != uint32(defaultRetentionSecFallback) {
		t.Errorf("RetentionTTL = %d, want %d (4w shipped fallback)", got.RetentionTTL, defaultRetentionSecFallback)
	}
	if got.DisableReadReceipts {
		t.Error("DisableReadReceipts should be false — receipts default ON must survive the vault→badger move")
	}
}
