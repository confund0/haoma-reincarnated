package ipc

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateToken_GeneratesWith0600(t *testing.T) {
	dir := t.TempDir()
	tok, err := LoadOrCreateToken(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(tok) != 64 {
		t.Errorf("token length = %d, want 64 hex chars", len(tok))
	}
	path := filepath.Join(dir, tokenFilename)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 0600", perm)
	}
}

func TestLoadOrCreateToken_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	tok1, err := LoadOrCreateToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	tok2, err := LoadOrCreateToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	if tok1 != tok2 {
		t.Errorf("second LoadOrCreateToken regenerated the token")
	}
}

func TestLoadOrCreateToken_RejectsWideMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, tokenFilename)
	if err := os.WriteFile(path, []byte("abc123"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadOrCreateToken(dir)
	if err == nil {
		t.Fatal("expected perm-refusal on world-readable token file")
	}
}

func TestReadToken_TrimsWhitespace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, tokenFilename)
	if err := os.WriteFile(path, []byte("abc123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "abc123" {
		t.Errorf("got %q, want abc123", got)
	}
}

func TestReadToken_EmptyFile_Errors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, tokenFilename)
	if err := os.WriteFile(path, []byte("\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ReadToken(path)
	if err == nil {
		t.Error("expected error on empty token file")
	}
}

func TestReadSensitive_ReturnsTrimmedContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	if err := os.WriteFile(path, []byte("  hunter2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSensitive(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "hunter2" {
		t.Errorf("got %q, want hunter2", got)
	}
}

func TestReadSensitive_RejectsWideMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	if err := os.WriteFile(path, []byte("hunter2"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ReadSensitive(path)
	if err == nil {
		t.Fatal("expected perm-refusal on world-readable file")
	}
}

func TestReadSensitive_RejectsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	if err := os.WriteFile(path, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ReadSensitive(path)
	if err == nil {
		t.Fatal("expected error on empty secret file")
	}
}

func TestReadSensitive_RejectsMissing(t *testing.T) {
	dir := t.TempDir()
	_, err := ReadSensitive(filepath.Join(dir, "nope"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestCheckToken_AcceptsMatch(t *testing.T) {
	if err := CheckToken("hello", "hello"); err != nil {
		t.Errorf("CheckToken match: %v", err)
	}
}

func TestCheckToken_RejectsMismatch(t *testing.T) {
	err := CheckToken("hello", "world")
	if !errors.Is(err, ErrBadToken) {
		t.Errorf("err = %v, want ErrBadToken", err)
	}
}

func TestCheckToken_RejectsLengthDiff(t *testing.T) {
	err := CheckToken("hello", "hell")
	if !errors.Is(err, ErrBadToken) {
		t.Errorf("err = %v, want ErrBadToken", err)
	}
}
