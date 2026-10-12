package main

import (
	"testing"

	"haoma-frontend/internal/peerstate"
	"haoma-frontend/internal/store"
)

func aliasTestDaemon(t *testing.T) *daemon {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Unlock(dir, "test-pass")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Lock() })
	return &daemon{store: st, peerMeta: peerstate.NewMeta(st)}
}

func TestPeerEntryFor_ShowsAnAliasSetOnAnotherDevice(t *testing.T) {
	d := aliasTestDaemon(t)
	const peerID, fingerprint = "peer-1", "fp-alice"

	if _, _, err := d.peerMeta.SetFingerprint(peerID, fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := d.peerMeta.SetNick(peerID, "alice", 500); err != nil {
		t.Fatal(err)
	}

	if _, err := d.peerMeta.MergePerson(fingerprint, peerstate.MetaRecord{
		Alias: "Boss", AliasAt: 1000,
	}); err != nil {
		t.Fatal(err)
	}

	entry := peerEntryOf(t, d, peerID)
	if entry.Alias != "Boss" {
		t.Errorf("Alias column = %q, want Boss", entry.Alias)
	}
	if entry.Nick != "alice" {
		t.Errorf("Nick column = %q, want alice", entry.Nick)
	}
	if entry.Label != "Boss" {
		t.Errorf("Label = %q, want Boss (alias beats the contact's own name)", entry.Label)
	}
}

func TestPeerEntryFor_ShowsAnAliasSetHere(t *testing.T) {
	d := aliasTestDaemon(t)
	const peerID, fingerprint = "peer-1", "fp-alice"

	if _, _, err := d.peerMeta.SetFingerprint(peerID, fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := d.peerMeta.SetNick(peerID, "alice", 500); err != nil {
		t.Fatal(err)
	}
	if _, err := d.peerMeta.SetAlias(peerID, "Boss", 1000); err != nil {
		t.Fatal(err)
	}

	entry := peerEntryOf(t, d, peerID)
	if entry.Alias != "Boss" || entry.Label != "Boss" {
		t.Errorf("alias=%q label=%q, want Boss/Boss", entry.Alias, entry.Label)
	}
}
