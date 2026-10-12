package wipe

import (
	"os"
	"path/filepath"
	"testing"
)

func newRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"backend", "frontend", "textUI"} {
		if err := os.MkdirAll(filepath.Join(root, dir, "nested"), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, "nested", "data"), []byte("x"), 0o600); err != nil {
			t.Fatalf("write in %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "vault.enc"), []byte("HAOMAVLT"), 0o600); err != nil {
		t.Fatalf("write vault: %v", err)
	}
	return root
}

func TestDevice_LeavesTheRootEmpty(t *testing.T) {
	root := newRoot(t)

	if err := Device(root); err != nil {
		t.Fatalf("Device: %v", err)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("the root still holds %v", names)
	}
}

func TestDevice_KeepsTheRootItself(t *testing.T) {
	root := newRoot(t)

	if err := Device(root); err != nil {
		t.Fatalf("Device: %v", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("the root itself was removed: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("the root is no longer a directory")
	}
}

func TestDevice_TheRootCanBeUsedAgain(t *testing.T) {
	root := newRoot(t)
	if err := Device(root); err != nil {
		t.Fatalf("Device: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "backend"), 0o700); err != nil {
		t.Fatalf("the root cannot be used again: %v", err)
	}
}

func TestDevice_RefusesAPathThatCannotBeTheConfiguredDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		root string
	}{
		{"nothing at all", ""},
		{"a relative path", "some/where"},
		{"the filesystem root", string(filepath.Separator)},
		{"a path that does not exist", filepath.Join(t.TempDir(), "missing")},
		{"a file", file},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := Device(c.root); err == nil {
				t.Fatalf("Device(%q) was accepted", c.root)
			}
		})
	}
}

func TestDevice_ErasesTheConfiguredDirectoryWhateverIsInIt(t *testing.T) {
	cases := []struct {
		name  string
		build func(t *testing.T, root string)
	}{
		{"a daemon store and no vault", func(t *testing.T, root string) {
			if err := os.MkdirAll(filepath.Join(root, "backend", "nested"), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{"a vault and nothing else", func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "vault.enc"), []byte("HAOMAVLT"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"nothing this package recognises", func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "something-else"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"an empty directory", func(t *testing.T, _ string) {}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			c.build(t, root)
			if err := Device(root); err != nil {
				t.Fatalf("the configured directory was refused: %v", err)
			}
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatalf("ReadDir: %v", err)
			}
			if len(entries) != 0 {
				t.Fatalf("the root still holds %d entries", len(entries))
			}
		})
	}
}

func TestDevice_RefusesTheHomeDirectory(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory on this system")
	}
	if err := Device(home); err == nil {
		t.Fatal("the home directory was accepted")
	}
}
