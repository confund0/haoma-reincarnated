package vault_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"haoma-frontend/internal/vault"
)

func helperWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func helperRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestRotateBeforeWriteShiftsExistingBackups(t *testing.T) {
	dir := t.TempDir()
	vaultPath := filepath.Join(dir, "vault.enc")

	helperWrite(t, vaultPath, "current")
	helperWrite(t, vaultPath+".1", "old1")
	helperWrite(t, vaultPath+".2", "old2")

	if err := vault.RotateBeforeWrite(vaultPath); err != nil {
		t.Fatalf("rotate: %v", err)
	}

	if _, err := os.Stat(vaultPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("vault.enc should be gone (rotated); err=%v", err)
	}
	if got := helperRead(t, vaultPath+".1"); got != "current" {
		t.Errorf(".1 = %q, want %q", got, "current")
	}
	if got := helperRead(t, vaultPath+".2"); got != "old1" {
		t.Errorf(".2 = %q, want %q", got, "old1")
	}
	if got := helperRead(t, vaultPath+".3"); got != "old2" {
		t.Errorf(".3 = %q, want %q", got, "old2")
	}
}

func TestRotateBeforeWriteDropsOldestPastMax(t *testing.T) {
	dir := t.TempDir()
	vaultPath := filepath.Join(dir, "vault.enc")
	helperWrite(t, vaultPath, "current")
	for i := 1; i <= vault.MaxBackups; i++ {
		helperWrite(t, vaultPath+"."+itoa(i), "backup"+itoa(i))
	}

	if err := vault.RotateBeforeWrite(vaultPath); err != nil {
		t.Fatalf("rotate: %v", err)
	}

	if got := helperRead(t, vaultPath+"."+itoa(vault.MaxBackups)); got != "backup"+itoa(vault.MaxBackups-1) {
		t.Errorf(".%d after rotate = %q", vault.MaxBackups, got)
	}

	if got := helperRead(t, vaultPath+".1"); got != "current" {
		t.Errorf(".1 after rotate = %q", got)
	}

	if got := strings.Count(helperReadAll(t, dir), "backup"+itoa(vault.MaxBackups)); got != 0 {
		t.Errorf("oldest backup not dropped, count=%d", got)
	}
}

func TestRotateBeforeWriteNoCurrent(t *testing.T) {
	dir := t.TempDir()
	vaultPath := filepath.Join(dir, "vault.enc")

	if err := vault.RotateBeforeWrite(vaultPath); err != nil {
		t.Fatalf("rotate empty: %v", err)
	}
}

func TestListBackupsEmpty(t *testing.T) {
	dir := t.TempDir()
	infos, err := vault.ListBackups(filepath.Join(dir, "vault.enc"))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(infos) != 0 {
		t.Errorf("expected 0 backups, got %d", len(infos))
	}
}

func TestListBackupsLists(t *testing.T) {
	dir := t.TempDir()
	vaultPath := filepath.Join(dir, "vault.enc")
	helperWrite(t, vaultPath+".1", "one")
	helperWrite(t, vaultPath+".3", "three")
	infos, err := vault.ListBackups(vaultPath)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(infos))
	}
	if infos[0].N != 1 || infos[1].N != 3 {
		t.Errorf("infos[0].N=%d, infos[1].N=%d", infos[0].N, infos[1].N)
	}
	if infos[0].Size != 3 {
		t.Errorf("infos[0].Size = %d, want 3", infos[0].Size)
	}
}

func TestRestoreFromBackup(t *testing.T) {
	dir := t.TempDir()
	vaultPath := filepath.Join(dir, "vault.enc")
	helperWrite(t, vaultPath, "corrupt")
	helperWrite(t, vaultPath+".2", "good")

	if err := vault.RestoreFromBackup(vaultPath, 2); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := helperRead(t, vaultPath); got != "good" {
		t.Errorf("after restore: %q, want \"good\"", got)
	}

	if got := helperRead(t, vaultPath+".2"); got != "good" {
		t.Errorf(".2 after restore: %q (should still be readable)", got)
	}
}

func TestRestoreFromBackupOutOfRange(t *testing.T) {
	dir := t.TempDir()
	vaultPath := filepath.Join(dir, "vault.enc")
	for _, n := range []int{0, -1, vault.MaxBackups + 1, 99} {
		err := vault.RestoreFromBackup(vaultPath, n)
		if err == nil {
			t.Errorf("restore %d: expected error", n)
		}
	}
}

func TestRestoreFromBackupMissing(t *testing.T) {
	dir := t.TempDir()
	vaultPath := filepath.Join(dir, "vault.enc")
	err := vault.RestoreFromBackup(vaultPath, 1)
	if err == nil {
		t.Errorf("missing backup: expected error")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if negative {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func helperReadAll(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		b.WriteString(helperRead(t, filepath.Join(dir, e.Name())))
		b.WriteByte('\n')
	}
	return b.String()
}
