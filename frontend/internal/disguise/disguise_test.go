package disguise

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestInitAndVerifyRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := Init(path, "78963"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := Verify(path, "78963"); err != nil {
		t.Fatalf("Verify with correct pattern: %v", err)
	}
}

func TestVerifyWrongPattern(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := Init(path, "78963"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := Verify(path, "12345"); !errors.Is(err, ErrPatternMismatch) {
		t.Fatalf("Verify with wrong pattern: want ErrPatternMismatch, got %v", err)
	}
}

func TestInitRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := Init(path, "78963"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := Init(path, "12345"); err == nil {
		t.Fatalf("Init twice should fail")
	}

	if err := Verify(path, "78963"); err != nil {
		t.Fatalf("Verify after refused Init: %v", err)
	}
}

func TestRekeyHappy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := Init(path, "78963"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := Rekey(path, "78963", "44321"); err != nil {
		t.Fatalf("Rekey: %v", err)
	}
	if err := Verify(path, "44321"); err != nil {
		t.Fatalf("Verify with new pattern: %v", err)
	}
	if err := Verify(path, "78963"); !errors.Is(err, ErrPatternMismatch) {
		t.Fatalf("Verify with old pattern after rekey: want ErrPatternMismatch, got %v", err)
	}
}

func TestRekeyWrongOldLeavesFileIntact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := Init(path, "78963"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}
	if err := Rekey(path, "wrong", "44321"); !errors.Is(err, ErrPatternMismatch) {
		t.Fatalf("Rekey wrong-old: want ErrPatternMismatch, got %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("Rekey with wrong old pattern mutated the file")
	}
	if err := Verify(path, "78963"); err != nil {
		t.Fatalf("original pattern still works: %v", err)
	}
}

func TestVerifyTamperedSidecar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := Init(path, "78963"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	raw[headerLen+2] ^= 0x01
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write tampered: %v", err)
	}
	if err := Verify(path, "78963"); !errors.Is(err, ErrPatternMismatch) {
		t.Fatalf("Verify tampered ciphertext: want ErrPatternMismatch, got %v", err)
	}
}

func TestVerifyHeaderTamperFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := Init(path, "78963"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	raw[8] = 0x99
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write tampered: %v", err)
	}
	if err := Verify(path, "78963"); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("Verify with bad version: want ErrUnsupportedVersion, got %v", err)
	}
}

func TestVerifyMissingFile(t *testing.T) {
	if err := Verify(filepath.Join(t.TempDir(), "nope.enc"), "78963"); err == nil {
		t.Fatalf("Verify missing file: want error, got nil")
	}
}

func TestVerifyEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write empty: %v", err)
	}
	if err := Verify(path, "78963"); !errors.Is(err, ErrEmpty) {
		t.Fatalf("Verify empty file: want ErrEmpty, got %v", err)
	}
}

func TestInitEmptyPattern(t *testing.T) {
	if err := Init(filepath.Join(t.TempDir(), FileName), ""); err == nil {
		t.Fatalf("Init with empty pattern: want error, got nil")
	}
}

func TestRekeyEmptyNewPattern(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := Init(path, "78963"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := Rekey(path, "78963", ""); err == nil {
		t.Fatalf("Rekey with empty new pattern: want error, got nil")
	}
}

func TestPathHelper(t *testing.T) {
	got := Path("/data/data/io.haoma/files")
	want := "/data/data/io.haoma/files/disguise.enc"
	if got != want {
		t.Fatalf("Path: got %q, want %q", got, want)
	}
}
