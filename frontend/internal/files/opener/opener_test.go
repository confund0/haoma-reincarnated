package opener

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestDetect_Cached(t *testing.T) {
	Reset()
	t.Cleanup(Reset)

	a := Detect()
	b := Detect()

	if a.Name() != b.Name() || a.bin != b.bin {
		t.Fatalf("Detect cache returned different handles: %+v vs %+v", a, b)
	}
}

func TestDetect_TermuxWinsOverGOOS(t *testing.T) {
	Reset()
	t.Cleanup(Reset)

	dir := t.TempDir()
	stubName := "termux-open"
	if runtime.GOOS == "windows" {
		stubName = "termux-open.bat"
	}
	stub := filepath.Join(dir, stubName)
	writeStub(t, stub)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TERMUX_VERSION", "0.118")

	o := Detect()
	if !o.Available() {
		t.Fatal("expected Termux opener to be detected")
	}
	if o.Name() != "termux-open" {
		t.Errorf("Name = %q, want termux-open", o.Name())
	}
}

func TestDetect_NoCandidate(t *testing.T) {
	Reset()
	t.Cleanup(Reset)

	t.Setenv("PATH", "")
	t.Setenv("TERMUX_VERSION", "")

	o := Detect()
	if o.Available() {
		t.Fatalf("expected no opener, got %+v", o)
	}
	if o.Name() != "" {
		t.Errorf("Name = %q, want empty", o.Name())
	}
	err := o.Open(context.Background(), "/tmp/whatever")
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("Open = %v, want ErrUnavailable", err)
	}
}

func TestOpener_OpenSpawnsAndDetaches(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test stub uses /bin/sh shebang; skip on windows")
	}
	Reset()
	t.Cleanup(Reset)

	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	stub := filepath.Join(dir, "xdg-open")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\necho \"$1\" >"+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("TERMUX_VERSION", "")

	o := Detect()
	if !o.Available() {
		t.Fatalf("expected stub xdg-open to resolve, got %+v", o)
	}

	target := filepath.Join(dir, "victim.txt")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := o.Open(ctx, target); err != nil {
		t.Fatalf("Open: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(marker); err == nil {
			got := string(data)
			want := target + "\n"
			if got != want {
				t.Errorf("stub recorded %q, want %q", got, want)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("stub did not run in time")
}

func TestOpener_OpenEmptyPath(t *testing.T) {
	o := Opener{name: "stub", bin: "/bin/true"}
	err := o.Open(context.Background(), "")
	if err == nil {
		t.Fatal("expected error on empty path")
	}
}

func writeStub(t *testing.T, path string) {
	t.Helper()
	body := "#!/bin/sh\nexit 0\n"
	if runtime.GOOS == "windows" {
		body = "@echo off\r\nexit /b 0\r\n"
	}
	mode := os.FileMode(0o755)
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}
