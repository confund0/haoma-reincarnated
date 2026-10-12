package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"haoma-frontend/internal/backuparchive"
)

func markerThere(t *testing.T, root string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(root, backuparchive.EmptyMarkerFile))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("stat: %v", err)
	}
	return err == nil
}

func passes(context.Context) (string, string, bool) { return "", "", true }

func TestEmptyMarkerStartRemovesAnOldFile(t *testing.T) {
	root := t.TempDir()
	if err := backuparchive.MarkDeviceEmpty(root); err != nil {
		t.Fatal(err)
	}
	if _, err := newEmptyMarker(root, ""); err != nil {
		t.Fatalf("newEmptyMarker: %v", err)
	}
	if markerThere(t, root) {
		t.Error("the file of an earlier run is still there after the start")
	}
}

func TestEmptyMarkerWrittenWhenTheRuleHolds(t *testing.T) {
	root := t.TempDir()
	m := mustMarker(t, root)
	m.watch(context.Background(), passes)
	if !markerThere(t, root) {
		t.Fatal("no file after the rule held")
	}
	if err := m.grow("a contact"); err != nil {
		t.Fatalf("grow: %v", err)
	}
	if markerThere(t, root) {
		t.Error("the file is still there after a write that adds a contact began")
	}
}

func TestEmptyMarkerNotWrittenAfterAGrow(t *testing.T) {
	root := t.TempDir()
	m := mustMarker(t, root)
	m.markIfEmpty(context.Background(), func(context.Context) (string, string, bool) {
		_ = m.grow("a chat")
		return "", "", true
	})
	if markerThere(t, root) {
		t.Error("the file was written after a grow")
	}
}

func TestEmptyMarkerNotWrittenWhenNotEmpty(t *testing.T) {
	root := t.TempDir()
	m := mustMarker(t, root)
	if done := m.markIfEmpty(context.Background(), func(context.Context) (string, string, bool) {
		return reasonDeviceNotEmpty, "1 chat", false
	}); !done {
		t.Error("a device that is not empty was tried again")
	}
	if done := m.markIfEmpty(context.Background(), func(context.Context) (string, string, bool) {
		return reasonPeerCheckFailed, "haomad down", false
	}); done {
		t.Error("a rule that could not be read counted as an answer")
	}
	if markerThere(t, root) {
		t.Error("the file was written for a device that is not proved empty")
	}
}

func TestEmptyMarkerWithoutRoot(t *testing.T) {
	m := mustMarker(t, "")
	if err := m.grow("a contact"); err != nil {
		t.Errorf("grow without a root: %v", err)
	}
	m.watch(context.Background(), passes)
	var nilMarker *emptyMarker
	if err := nilMarker.grow("a chat"); err != nil {
		t.Errorf("grow on nil: %v", err)
	}
}

func mustMarker(t *testing.T, root string) *emptyMarker {
	t.Helper()
	m, err := newEmptyMarker(root, "")
	if err != nil {
		t.Fatalf("newEmptyMarker: %v", err)
	}
	return m
}

func lockedRoot(t *testing.T) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("a read-only dir does not stop root")
	}
	root := t.TempDir()
	if err := backuparchive.MarkDeviceEmpty(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
	return root
}

func TestEmptyMarkerStartFailsClosed(t *testing.T) {
	root := lockedRoot(t)
	if _, err := newEmptyMarker(root, ""); err == nil {
		t.Error("newEmptyMarker went on with a file it could not remove")
	}
}

func TestEmptyMarkerGrowFailsClosed(t *testing.T) {
	root := t.TempDir()
	m := mustMarker(t, root)
	m.watch(context.Background(), passes)
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		_ = os.Chmod(root, 0o700)
		t.Skip("a read-only dir does not stop root")
	}
	err := m.grow("a chat")
	_ = os.Chmod(root, 0o700)
	if err == nil {
		t.Fatal("grow said nothing while the file could not be removed")
	}
	if !markerThere(t, root) {
		t.Fatal("the test did not keep the file")
	}
	if err := m.grow("a chat"); err != nil {
		t.Fatalf("second grow: %v", err)
	}
	if markerThere(t, root) {
		t.Error("the second grow did not remove the file")
	}
}

func TestEmptyMarkerExplicitDataDir(t *testing.T) {
	parent := t.TempDir()
	if err := backuparchive.MarkDeviceEmpty(parent); err != nil {
		t.Fatal(err)
	}
	m, err := newEmptyMarker("", filepath.Join(parent, "frontend"))
	if err != nil {
		t.Fatalf("newEmptyMarker: %v", err)
	}
	if markerThere(t, parent) {
		t.Error("the file beside an explicit data dir is still there")
	}
	m.watch(context.Background(), passes)
	if markerThere(t, parent) {
		t.Error("a run with an explicit data dir wrote the file")
	}
}

func TestEmptyMarkerStartRemovesALeftoverTemp(t *testing.T) {
	root := t.TempDir()
	tmp := filepath.Join(root, backuparchive.EmptyMarkerFile+".tmp")
	if err := os.WriteFile(tmp, []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustMarker(t, root)
	if _, err := os.Stat(tmp); err == nil {
		t.Error("the leftover temp file is still there after the start")
	}
}
