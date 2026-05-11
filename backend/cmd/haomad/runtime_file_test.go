package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteRuntimeFile_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "haomad.runtime.json")

	want := RuntimeInfo{
		PID:       4242,
		APIAddr:   "127.0.0.1:7891",
		StartedAt: time.Now().UTC().Truncate(time.Nanosecond),
	}
	if err := writeRuntimeFile(path, want); err != nil {
		t.Fatalf("writeRuntimeFile: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 0600", perm)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got RuntimeInfo
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v (raw=%q)", err, raw)
	}
	if got.PID != want.PID {
		t.Errorf("PID = %d, want %d", got.PID, want.PID)
	}
	if got.APIAddr != want.APIAddr {
		t.Errorf("APIAddr = %q, want %q", got.APIAddr, want.APIAddr)
	}
	if !got.StartedAt.Equal(want.StartedAt) {
		t.Errorf("StartedAt = %v, want %v", got.StartedAt, want.StartedAt)
	}
}

func TestWriteRuntimeFile_SchemaMatchesSupervisor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "haomad.runtime.json")

	if err := writeRuntimeFile(path, RuntimeInfo{
		PID:       1,
		APIAddr:   "127.0.0.1:1",
		StartedAt: time.Unix(0, 0).UTC(),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, key := range []string{`"pid"`, `"api_addr"`, `"started_at"`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("file body %q missing key %s", raw, key)
		}
	}
}

func TestWriteRuntimeFile_AtomicNoTempLeak(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "haomad.runtime.json")
	if err := writeRuntimeFile(path, RuntimeInfo{PID: 1, APIAddr: "127.0.0.1:1"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file leaked: %s", e.Name())
		}
	}
}

func TestWriteRuntimeFile_EmptyPath(t *testing.T) {
	if err := writeRuntimeFile("", RuntimeInfo{PID: 1, APIAddr: "x"}); err == nil {
		t.Fatal("want error on empty path, got nil")
	}
}

func TestWriteRuntimeFile_ParentDoesNotExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope", "haomad.runtime.json")
	if err := writeRuntimeFile(path, RuntimeInfo{PID: 1, APIAddr: "x"}); err == nil {
		t.Fatal("want error when parent dir missing, got nil")
	}
}

func TestRemoveRuntimeFile_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "haomad.runtime.json")
	if err := writeRuntimeFile(path, RuntimeInfo{PID: 1, APIAddr: "x"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	removeRuntimeFile(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file still present after remove: stat err=%v", err)
	}
}

func TestRemoveRuntimeFile_MissingFileSwallows(t *testing.T) {

	removeRuntimeFile(filepath.Join(t.TempDir(), "never-existed"))
}

func TestRemoveRuntimeFile_EmptyPathNoOp(t *testing.T) {
	removeRuntimeFile("")
}
