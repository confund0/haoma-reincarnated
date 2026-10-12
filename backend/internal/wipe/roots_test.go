package wipe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type phone struct {
	data, files, cache, cfg, libTarget string
}

func newPhone(t *testing.T) phone {
	t.Helper()
	base := t.TempDir()
	p := phone{data: filepath.Join(base, "data", "user", "0", "io.haoma.calculator")}
	p.files = filepath.Join(p.data, "files")
	p.cache = filepath.Join(p.data, "cache")
	p.cfg = filepath.Join(p.files, "haoma")
	p.libTarget = filepath.Join(base, "app", "lib", "arm64")
	for _, dir := range []string{p.cfg, p.cache, p.libTarget} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(p.libTarget, "libhaomad.so"))
	if err := os.Symlink(p.libTarget, filepath.Join(p.data, "lib")); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeFile(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func (p phone) erase(t *testing.T) {
	t.Helper()
	roots, err := Roots(p.cfg, "android")
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	for _, r := range roots {
		if err := Device(r.Path, r.Keep...); err != nil {
			t.Fatalf("Device(%s): %v", r.Path, err)
		}
	}
}

func TestRoots_ADesktopIsTheConfiguredDirectoryAlone(t *testing.T) {
	roots, err := Roots("/home/someone/.haoma", "linux")
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 || roots[0].Path != "/home/someone/.haoma" || len(roots[0].Keep) != 0 {
		t.Fatalf("roots = %+v", roots)
	}
}

func TestRoots_APhoneIsThreeDirectoriesFromTheOneItWasGiven(t *testing.T) {
	roots, err := Roots("/data/user/10/io.haoma.calculator/files/haoma", "android")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/data/user/10/io.haoma.calculator/files",
		"/data/user/10/io.haoma.calculator/cache",
		"/data/user/10/io.haoma.calculator",
	}
	if len(roots) != len(want) {
		t.Fatalf("roots = %+v", roots)
	}
	for i, w := range want {
		if roots[i].Path != w {
			t.Fatalf("root %d = %s, want %s", i, roots[i].Path, w)
		}
	}
}

func TestRoots_APhonePathOfTheWrongShapeIsRefused(t *testing.T) {
	if _, err := Roots("/data/user/0/io.haoma.calculator/haoma", "android"); err == nil {
		t.Fatal("a path outside a files directory was accepted")
	}
}

func TestPhone_TheUnlockKeysAreErased(t *testing.T) {
	p := newPhone(t)
	keys := writeFile(t, filepath.Join(p.files, "unlock_keys.json"))
	halfWritten := writeFile(t, filepath.Join(p.files, "unlock_keys.json.tmp"))
	p.erase(t)
	if exists(keys) || exists(halfWritten) {
		t.Fatal("the unlock keys survived")
	}
}

func TestPhone_TheIdentityIsErased(t *testing.T) {
	p := newPhone(t)
	vault := writeFile(t, filepath.Join(p.cfg, "vault.enc"))
	blob := writeFile(t, filepath.Join(p.cfg, "backend", "files", "blob"))
	p.erase(t)
	if exists(vault) || exists(blob) || exists(p.cfg) {
		t.Fatal("the daemons' directory survived")
	}
}

func TestPhone_TheFilesSentAndTheFilesWrittenOutAreErased(t *testing.T) {
	p := newPhone(t)
	sent := writeFile(t, filepath.Join(p.cache, "saf-in", "photo.jpg"))
	writtenOut := writeFile(t, filepath.Join(p.files, "saf-out", "report.pdf"))
	p.erase(t)
	if exists(sent) || exists(writtenOut) {
		t.Fatal("an attachment copy survived")
	}
}

func TestPhone_WhatARunLeftInTheCacheIsErased(t *testing.T) {
	p := newPhone(t)
	archive := writeFile(t, filepath.Join(p.cache, "haoma-backup-20260923-210000.tar.zst"))
	bundle := writeFile(t, filepath.Join(p.cache, "log-export", "haoma-logs-1.zip"))
	p.erase(t)
	if exists(archive) || exists(bundle) {
		t.Fatal("something a run left in the cache survived")
	}
}

func TestPhone_ThePreferencesAreErased(t *testing.T) {
	p := newPhone(t)
	prefs := writeFile(t, filepath.Join(p.data, "shared_prefs", "MainActivity.xml"))
	p.erase(t)
	if exists(prefs) {
		t.Fatal("the preferences survived")
	}
}

func TestPhone_AFileNobodyNamedIsErased(t *testing.T) {
	p := newPhone(t)
	inFiles := writeFile(t, filepath.Join(p.files, "some-later-slice", "state.json"))
	inData := writeFile(t, filepath.Join(p.data, "databases", "later.db"))
	p.erase(t)
	if exists(inFiles) || exists(inData) {
		t.Fatal("a file nobody named survived")
	}
}

func TestPhone_AReleaseBuildsLogsAreErased(t *testing.T) {
	p := newPhone(t)
	log := writeFile(t, filepath.Join(p.files, "logs", "haomad.log"))
	p.erase(t)
	if exists(log) {
		t.Fatal("the release log survived")
	}
}

func TestPhone_TheNativeLibraryLinkIsLeftAlone(t *testing.T) {
	p := newPhone(t)
	p.erase(t)
	if !exists(filepath.Join(p.data, "lib")) {
		t.Fatal("the lib link was removed")
	}
	if !exists(filepath.Join(p.libTarget, "libhaomad.so")) {
		t.Fatal("the native binaries were removed")
	}
}

func TestPhone_TheFilesAndCacheDirectoriesStay(t *testing.T) {
	p := newPhone(t)
	writeFile(t, filepath.Join(p.files, "x"))
	writeFile(t, filepath.Join(p.cache, "y"))
	p.erase(t)
	for _, dir := range []string{p.files, p.cache, p.data} {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Fatalf("%s is gone", dir)
		}
	}
	for _, dir := range []string{p.files, p.cache} {
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Fatalf("%s still holds %d entries", dir, len(entries))
		}
	}
}

func TestDevice_ALinkIsRemovedAndWhatItPointsAtStays(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	photo := writeFile(t, filepath.Join(outside, "family-album", "photo.jpg"))
	link := filepath.Join(root, "shortcut")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	if err := Device(root); err != nil {
		t.Fatalf("Device: %v", err)
	}
	if exists(link) {
		t.Fatal("the link survived")
	}
	if !exists(photo) {
		t.Fatal("the link was followed and its target erased")
	}
}

func TestDevice_AnEntryThatWillNotGoIsCountedAndNeverNamed(t *testing.T) {
	root := t.TempDir()
	stuck := filepath.Join(root, "saf-in")
	writeFile(t, filepath.Join(stuck, "holiday-photo.jpg"))
	other := writeFile(t, filepath.Join(root, "other"))
	if err := os.Chmod(stuck, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stuck, 0o700) })
	if f, err := os.Create(filepath.Join(stuck, "probe")); err == nil {
		_ = f.Close()
		t.Skip("this process ignores directory permissions")
	}

	err := Device(root)
	if err == nil {
		t.Fatal("a failed removal was reported as success")
	}
	if exists(other) {
		t.Fatal("the failure stopped the rest of the erasure")
	}
	msg := err.Error()
	if !strings.Contains(msg, "1 failed") {
		t.Fatalf("the failure is not counted: %s", msg)
	}
	if strings.Contains(msg, "holiday-photo") || strings.Contains(msg, "saf-in") {
		t.Fatalf("the error names an entry: %s", msg)
	}
}
