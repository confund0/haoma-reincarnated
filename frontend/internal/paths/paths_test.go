package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func withTempRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "haoma-root")
	t.Setenv(rootEnv, root)
	return root
}

func TestRoot_HAOMARootEnv(t *testing.T) {
	root := withTempRoot(t)
	got, err := Root()
	if err != nil {
		t.Fatal(err)
	}
	if got != root {
		t.Errorf("Root() = %q, want %q", got, root)
	}
}

func TestRoot_PlatformDefault(t *testing.T) {
	t.Setenv(rootEnv, "")
	got, err := Root()
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		cfg, err := os.UserConfigDir()
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(cfg, "haoma")
		if got != want {
			t.Errorf("Root() = %q, want %q (windows %%APPDATA%%\\haoma)", got, want)
		}
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(home, defaultRoot)
		if got != want {
			t.Errorf("Root() = %q, want %q (unix ~/.haoma)", got, want)
		}
	}
}

func TestRootFromFlag_EmptyFallsBackToRoot(t *testing.T) {
	root := withTempRoot(t)
	got, err := RootFromFlag("")
	if err != nil {
		t.Fatal(err)
	}
	if got != root {
		t.Errorf("RootFromFlag(\"\") = %q, want %q (Root())", got, root)
	}
}

func TestRootFromFlag_AbsolutePassesThrough(t *testing.T) {
	dir := t.TempDir()
	got, err := RootFromFlag(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Errorf("RootFromFlag(%q) = %q, want %q", dir, got, dir)
	}
}

func TestRootFromFlag_RelativeResolvedToAbs(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	got, err := RootFromFlag("relative/cfg")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(cwd, "relative", "cfg")
	if got != want {
		t.Errorf("RootFromFlag(relative/cfg) = %q, want %q", got, want)
	}
}

func TestRootFromFlag_TildeExpanded(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	got, err := RootFromFlag("~/haoma-test")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "haoma-test")
	if got != want {
		t.Errorf("RootFromFlag(~/haoma-test) = %q, want %q", got, want)
	}
}

func TestRootFromFlag_HAOMARootIgnoredWhenFlagSet(t *testing.T) {
	t.Setenv(rootEnv, "/should/not/be/used")
	dir := t.TempDir()
	got, err := RootFromFlag(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Errorf("RootFromFlag(%q) = %q, want %q (env should be ignored when flag set)", dir, got, dir)
	}
}

func TestBootstrap_CreatesMissingDirsAt0700(t *testing.T) {
	root := withTempRoot(t)

	sub, err := Bootstrap("frontend")
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if sub != filepath.Join(root, "frontend") {
		t.Errorf("sub = %q, want %q", sub, filepath.Join(root, "frontend"))
	}

	for _, p := range []string{root, sub} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if perm := info.Mode().Perm(); perm != SecureMode {
			t.Errorf("%s mode = %o, want %o", p, perm, SecureMode)
		}
	}
}

func TestBootstrap_IdempotentOnCorrectPerms(t *testing.T) {
	withTempRoot(t)
	if _, err := Bootstrap("frontend"); err != nil {
		t.Fatal(err)
	}

	if _, err := Bootstrap("frontend"); err != nil {
		t.Errorf("second Bootstrap errored: %v", err)
	}
}

func TestBootstrap_RefusesWidePermsOnRoot(t *testing.T) {
	root := withTempRoot(t)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := Bootstrap("frontend")
	if err == nil {
		t.Fatal("Bootstrap accepted mode 0755 root; should have refused")
	}
}

func TestBootstrap_RefusesWidePermsOnSubdir(t *testing.T) {
	root := withTempRoot(t)
	if err := os.MkdirAll(filepath.Join(root, "frontend"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "frontend"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := Bootstrap("frontend")
	if err == nil {
		t.Fatal("Bootstrap accepted mode 0755 subdir; should have refused")
	}
}

func TestBootstrap_RejectsNestedSubdir(t *testing.T) {
	withTempRoot(t)
	if _, err := Bootstrap("frontend/keys"); err == nil {
		t.Fatal("Bootstrap accepted nested subdir; should have refused")
	}
}

func TestExpand_Tilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		in, want string
	}{
		{"~", home},
		{"~/haoma", filepath.Join(home, "haoma")},
		{"$HOME/x", filepath.Join(home, "x")},
		{"/absolute", "/absolute"},
		{"relative/path", "relative/path"},
	}
	for _, c := range cases {
		got, err := Expand(c.in)
		if err != nil {
			t.Errorf("Expand(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("Expand(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDefaultDownloadsDir_DesktopFallsBackToHomeDownloads(t *testing.T) {
	t.Setenv("TERMUX_VERSION", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DefaultDownloadsDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "Downloads")
	if got != want {
		t.Errorf("DefaultDownloadsDir() = %q, want %q", got, want)
	}
}

func TestDefaultDownloadsDir_TermuxBranch(t *testing.T) {
	t.Setenv("TERMUX_VERSION", "0.118.0")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DefaultDownloadsDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "storage", "shared", "Download")
	if got != want {
		t.Errorf("DefaultDownloadsDir() = %q, want %q (Termux)", got, want)
	}
}

func TestDefaultAttachStartDir_DesktopFallsBackToHome(t *testing.T) {
	t.Setenv("TERMUX_VERSION", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DefaultAttachStartDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != home {
		t.Errorf("DefaultAttachStartDir() = %q, want %q", got, home)
	}
}

func TestDefaultAttachStartDir_TermuxBranch(t *testing.T) {
	t.Setenv("TERMUX_VERSION", "0.118.0")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DefaultAttachStartDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "storage", "shared")
	if got != want {
		t.Errorf("DefaultAttachStartDir() = %q, want %q (Termux)", got, want)
	}
}

func TestWriteSensitiveFile_CreatesWith0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	if err := WriteSensitiveFile(path, []byte("shhh")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != SecureFileMode {
		t.Errorf("mode = %o, want %o", perm, SecureFileMode)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "shhh" {
		t.Errorf("content = %q, want shhh", got)
	}
}

func TestWriteSensitiveFile_ReplaceAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	if err := WriteSensitiveFile(path, []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if err := WriteSensitiveFile(path, []byte("v2-longer")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "v2-longer" {
		t.Errorf("content after replace = %q, want v2-longer", got)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("dir has %d entries (want 1): %v", len(entries), entries)
	}
}

func TestResolveSaveStartDir_VaultDirWins(t *testing.T) {
	dir := t.TempDir()
	got := ResolveSaveStartDir(dir)
	if got != dir {
		t.Errorf("ResolveSaveStartDir(%q) = %q, want %q", dir, got, dir)
	}
}

func TestResolveSaveStartDir_VaultDirMissingFallsThrough(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	got := ResolveSaveStartDir(dir)
	if got == dir {
		t.Errorf("ResolveSaveStartDir(%q) = %q, expected fall-through", dir, got)
	}
	if got == "" {
		t.Errorf("ResolveSaveStartDir fell through to empty string")
	}
}

func TestResolveSaveStartDir_VaultDirIsFileFallsThrough(t *testing.T) {
	dir := t.TempDir()
	regularFile := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(regularFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed regular file: %v", err)
	}
	got := ResolveSaveStartDir(regularFile)
	if got == regularFile {
		t.Errorf("ResolveSaveStartDir(%q) = %q, expected fall-through", regularFile, got)
	}
}

func TestResolveSaveStartDir_EmptyVaultDirFallsThrough(t *testing.T) {
	got := ResolveSaveStartDir("")
	if got == "" {
		t.Errorf("ResolveSaveStartDir fell through to empty string")
	}
}

func TestResolveAttachStartDir_VaultDirWins(t *testing.T) {
	dir := t.TempDir()
	got := ResolveAttachStartDir(dir)
	if got != dir {
		t.Errorf("ResolveAttachStartDir(%q) = %q, want %q", dir, got, dir)
	}
}

func TestResolveAttachStartDir_VaultDirMissingFallsThrough(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	got := ResolveAttachStartDir(dir)
	if got == dir {
		t.Errorf("ResolveAttachStartDir(%q) = %q, expected fall-through", dir, got)
	}
	if got == "" {
		t.Errorf("ResolveAttachStartDir fell through to empty string")
	}
}

func TestResolveAttachStartDir_EmptyVaultDirFallsThrough(t *testing.T) {
	got := ResolveAttachStartDir("")
	if got == "" {
		t.Errorf("ResolveAttachStartDir fell through to empty string")
	}
}
