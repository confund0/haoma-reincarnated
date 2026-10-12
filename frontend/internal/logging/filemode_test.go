package logging

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestRotatingWriter_KeepsTheRequestedModeAcrossRotation(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)

	for _, mode := range []os.FileMode{0o600, 0o660} {
		path := filepath.Join(t.TempDir(), "x.log")
		w, err := newRotatingWriter(path, 64, mode)
		if err != nil {
			t.Fatal(err)
		}
		assertMode(t, path, mode, "at create")
		for i := 0; i < 4; i++ {
			if _, err := w.Write([]byte("0123456789012345678901234567890123456789\n")); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := os.Stat(path + ".prev"); err != nil {
			t.Fatalf("mode %o: no rotation happened: %v", mode, err)
		}
		assertMode(t, path, mode, "after rotation")
		_ = w.Close()
	}
}

func TestRotatingWriter_SetsTheModeOfAnExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.log")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := newRotatingWriter(path, 0, 0o660)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	assertMode(t, path, 0o660, "on an existing file")
}

func TestParseFileMode_RefusesModesOthersCouldRead(t *testing.T) {
	for _, s := range []string{"0644", "0666", "0700", "0770", "abc"} {
		if _, err := ParseFileMode(s); err == nil {
			t.Errorf("ParseFileMode(%q) accepted, want refused", s)
		}
	}
	for s, want := range map[string]os.FileMode{"": 0o600, "0600": 0o600, "0640": 0o640, "0660": 0o660} {
		got, err := ParseFileMode(s)
		if err != nil || got != want {
			t.Errorf("ParseFileMode(%q) = %o, %v; want %o", s, got, err, want)
		}
	}
}

func assertMode(t *testing.T, path string, want os.FileMode, when string) {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != want {
		t.Fatalf("mode %s = %o, want %o", when, got, want)
	}
}
