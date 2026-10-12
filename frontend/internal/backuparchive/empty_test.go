package backuparchive

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDeviceIsEmpty(t *testing.T) {
	newInstall := func(t *testing.T) string {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "disguise.enc"), "D")
		write(t, filepath.Join(dir, "calc-help.txt"), "K")
		write(t, filepath.Join(dir, "tor", "torrc"), "T")
		write(t, filepath.Join(dir, "haoma-text.log"), "L")
		for _, d := range []string{"backend", "frontend/badger", "textUI"} {
			if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, dir string)
		want  bool
	}{
		{"a new install", func(*testing.T, string) {}, true},
		{"a vault", func(t *testing.T, dir string) { write(t, filepath.Join(dir, "vault.enc"), "V") }, false},
		{"a numbered vault copy", func(t *testing.T, dir string) { write(t, filepath.Join(dir, "vault.enc.1"), "V") }, false},
		{"a file in a store", func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, "frontend", "badger", "000001.vlog"), "S")
		}, false},
		{"a file of haoma-text", func(t *testing.T, dir string) { write(t, filepath.Join(dir, "textUI", "x"), "X") }, false},
		{"a symlink in a store", func(t *testing.T, dir string) {
			if err := os.Symlink("/nowhere", filepath.Join(dir, "backend", "l")); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"a vault and the empty marker", func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, "vault.enc"), "V")
			write(t, filepath.Join(dir, "backend", "badger", "MANIFEST"), "S")
			if err := MarkDeviceEmpty(dir); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"a dir named like the empty marker", func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, "vault.enc"), "V")
			if err := os.Mkdir(filepath.Join(dir, EmptyMarkerFile), 0o700); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"the empty marker and a restore that did not finish", func(t *testing.T, dir string) {
			if err := MarkDeviceEmpty(dir); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(dir, MarkerFile), `{"staging":".staging-20261001-120000"}`)
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := newInstall(t)
			c.setup(t, dir)
			got, err := DeviceIsEmpty(dir)
			if err != nil {
				t.Fatalf("DeviceIsEmpty: %v", err)
			}
			if got != c.want {
				t.Errorf("DeviceIsEmpty = %v, want %v", got, c.want)
			}
		})
	}
}

func TestDeviceIsEmptyFailsClosed(t *testing.T) {
	if empty, err := DeviceIsEmpty(filepath.Join(t.TempDir(), "missing")); err == nil || empty {
		t.Errorf("DeviceIsEmpty of a missing dir = %v, %v; want an error", empty, err)
	}
}

func TestEmptyMarkerWriteAndClear(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "vault.enc"), "V")
	if err := MarkDeviceEmpty(dir); err != nil {
		t.Fatalf("MarkDeviceEmpty: %v", err)
	}
	if empty, err := DeviceIsEmpty(dir); err != nil || !empty {
		t.Fatalf("after MarkDeviceEmpty = %v, %v; want empty", empty, err)
	}
	if _, err := os.Stat(filepath.Join(dir, EmptyMarkerFile+".tmp")); err == nil {
		t.Error("the temp file of the marker is left")
	}
	for i := 0; i < 2; i++ {
		if err := ClearDeviceEmpty(dir); err != nil {
			t.Fatalf("ClearDeviceEmpty %d: %v", i, err)
		}
	}
	if empty, err := DeviceIsEmpty(dir); err != nil || empty {
		t.Errorf("after ClearDeviceEmpty = %v, %v; want not empty", empty, err)
	}
}

func TestCleanRestoreLeftovers(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	write(t, filepath.Join(outside, "secret"), "OUTSIDE")
	write(t, filepath.Join(dir, "vault.enc"), "LIVE")
	write(t, filepath.Join(dir, "backend", ".pre-restore-20200101-000000", "x"), "DEEP")
	write(t, filepath.Join(dir, ".staging-20261001-115900", "vault.enc"), "12345")
	write(t, filepath.Join(dir, ".pre-restore-20260901-120000", "backend", "MANIFEST"), "1234567")
	if err := os.Symlink(outside, filepath.Join(dir, ".pre-restore-20260901-120000", "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, ".staging-20261001-120000")); err != nil {
		t.Fatal(err)
	}
	gone, err := CleanRestoreLeftovers(dir)
	if err != nil {
		t.Fatalf("CleanRestoreLeftovers: %v", err)
	}
	want := map[string]int64{".pre-restore-20260901-120000": 7, ".staging-20261001-115900": 5}
	if len(gone) != len(want) {
		t.Fatalf("deleted %+v, want %v", gone, want)
	}
	for _, d := range gone {
		if want[d.Name] != d.Bytes {
			t.Errorf("%s: bytes=%d, want %d (a symlink followed?)", d.Name, d.Bytes, want[d.Name])
		}
		if _, err := os.Lstat(filepath.Join(dir, d.Name)); err == nil {
			t.Errorf("%s is still there", d.Name)
		}
	}
	if got := read(t, filepath.Join(outside, "secret")); got != "OUTSIDE" {
		t.Errorf("what the symlinks point at = %q", got)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".staging-20261001-120000")); err != nil {
		t.Errorf("a symlink named like a staging dir was removed: %v", err)
	}
	if read(t, filepath.Join(dir, "vault.enc")) != "LIVE" ||
		read(t, filepath.Join(dir, "backend", ".pre-restore-20200101-000000", "x")) != "DEEP" {
		t.Error("an entry that is not a leftover at the top changed")
	}
}

func TestCleanRestoreLeftoversRefusesWhilePending(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, ".staging-20261001-115900", "vault.enc"), "STAGED")
	write(t, filepath.Join(dir, ".pre-restore-20260901-120000", "vault.enc"), "OLD")
	write(t, filepath.Join(dir, MarkerFile), `{"staging":".staging-20261001-115900"}`)
	gone, err := CleanRestoreLeftovers(dir)
	if !errors.Is(err, ErrCommitPending) || len(gone) != 0 {
		t.Errorf("= %+v, %v; want ErrCommitPending and nothing", gone, err)
	}
	for _, kept := range []string{".staging-20261001-115900", ".pre-restore-20260901-120000"} {
		if _, err := os.Stat(filepath.Join(dir, kept)); err != nil {
			t.Errorf("%s went while a restore did not finish", kept)
		}
	}
}

func TestClearDeviceEmptySyncsOnlyWhatItRemoved(t *testing.T) {
	prev := syncDir
	syncs := 0
	syncDir = func(string) error { syncs++; return errors.New("seeded EINVAL") }
	t.Cleanup(func() { syncDir = prev })

	dir := t.TempDir()
	if err := ClearDeviceEmpty(dir); err != nil {
		t.Errorf("ClearDeviceEmpty with no file: %v", err)
	}
	if syncs != 0 {
		t.Errorf("synced %d time(s) with nothing removed", syncs)
	}
	write(t, filepath.Join(dir, EmptyMarkerFile+".tmp"), "HALF")
	if err := ClearDeviceEmpty(dir); err == nil {
		t.Error("a failed sync after a removal was not reported")
	}
	if _, err := os.Stat(filepath.Join(dir, EmptyMarkerFile+".tmp")); err == nil {
		t.Error("the leftover temp file is still there")
	}
}

func TestNotEmptyMessageWording(t *testing.T) {
	want := "This device holds contacts or chats. A backup can only be restored on an empty device. " +
		"Unlock the app and wipe it, or reinstall the app, then restore. " +
		"If anything on this device is valuable, make a backup of it first."
	if NotEmptyMessage != want {
		t.Errorf("NotEmptyMessage = %q, want %q", NotEmptyMessage, want)
	}
}
