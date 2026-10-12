package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"haoma-frontend/internal/secrets"
)

func fakeTorBin(t *testing.T, mode os.FileMode) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "tor")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), mode); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestBuildHaomadArgsOwnTor(t *testing.T) {
	root := "/home/u/.haoma"
	tor := torSetup{bin: "/usr/bin/tor"}.withDir(root)
	args, err := buildHaomadArgs(root, "/rt", "warn", "/log", tor)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.Index(args, "--manage-tor")
	if i < 0 || i+1 >= len(args) || args[i+1] != "/usr/bin/tor" {
		t.Fatalf("no --manage-tor /usr/bin/tor in %v", args)
	}
	j := slices.Index(args, "--tor-data-dir")
	if j < 0 || j+1 >= len(args) || args[j+1] != filepath.Join(root, "tor") {
		t.Fatalf("no --tor-data-dir %s in %v", filepath.Join(root, "tor"), args)
	}
	for _, a := range args {
		if strings.HasPrefix(a, "--tor-control") || strings.HasPrefix(a, "--tor-socks") {
			t.Fatalf("own tor, but a shared tor address passed: %v", args)
		}
	}
	for _, want := range []string{"--cfg-dir", "--secrets-stdin", "--runtime-file", "--log-file"} {
		if !slices.Contains(args, want) {
			t.Fatalf("%s missing from %v", want, args)
		}
	}
}

func TestBuildHaomadArgsSystemTor(t *testing.T) {
	root := "/home/u/.haoma"
	args, err := buildHaomadArgs(root, "/rt", "warn", "/log", torSetup{system: true}.withDir(root))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--cfg-dir", root,
		"--secrets-stdin",
		"--api-addr", "127.0.0.1:0",
		"--runtime-file", "/rt",
		"--log-level", "warn",
		"--log-file", "/log",
	}
	if !slices.Equal(args, want) {
		t.Fatalf("system tor args = %v, want %v", args, want)
	}
}

func TestBuildHaomadArgsRefusesAnEmptyOwnTor(t *testing.T) {
	for _, tor := range []torSetup{{}, {bin: "/usr/bin/tor"}, {dir: "/d"}} {
		if args, err := buildHaomadArgs("/r", "/rt", "warn", "/log", tor); err == nil {
			t.Fatalf("%+v: no error, args %v", tor, args)
		}
	}
}

func TestTorFlags(t *testing.T) {
	good := fakeTorBin(t, 0o755)
	notExec := fakeTorBin(t, 0o644)
	missing := filepath.Join(t.TempDir(), "no-tor")
	for _, tc := range []struct {
		name    string
		system  bool
		bin     string
		binSet  bool
		wantErr string
		want    torSetup
	}{
		{"default, tor present", false, good, false, "", torSetup{bin: good}},
		{"tor missing", false, missing, false, "no tor binary at " + missing, torSetup{}},
		{"tor not executable", false, notExec, true, "not an executable file", torSetup{}},
		{"a dir", false, filepath.Dir(good), true, "not an executable file", torSetup{}},
		{"system tor", true, missing, false, "", torSetup{system: true}},
		{"system tor and --tor-bin", true, good, true, "--tor-bin", torSetup{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := torFlags(tc.system, tc.bin, tc.binSet)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one naming %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestTorFlagsMakesTheBinaryAbsolute(t *testing.T) {
	good := fakeTorBin(t, 0o755)
	t.Chdir(filepath.Dir(good))
	got, err := torFlags(false, "./tor", true)
	if err != nil {
		t.Fatal(err)
	}
	if got.bin != good {
		t.Fatalf("bin = %q, want %q", got.bin, good)
	}
}

func TestTorSetupFromArgs(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want torSetup
	}{
		{"no tor flags", []string{"--cfg-dir", "/r", "--secrets-stdin"}, torSetup{system: true}},
		{"own tor", []string{"--cfg-dir", "/r", "--manage-tor", "/usr/bin/tor", "--tor-data-dir", "/r/tor"}, torSetup{bin: "/usr/bin/tor", dir: "/r/tor"}},
		{"= form, one dash", []string{"-manage-tor=/t", "-tor-data-dir=/d"}, torSetup{bin: "/t", dir: "/d"}},
		{"own tor, default dir", []string{"--manage-tor", "/t"}, torSetup{bin: "/t"}},
		{"empty --manage-tor", []string{"--manage-tor=", "--tor-data-dir", "/d"}, torSetup{system: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := torSetupFromArgs(tc.args); got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestAttachedTorStatus(t *testing.T) {
	own := torSetup{bin: "/usr/bin/tor", dir: "/r/tor"}
	status := attachedTorStatus(42, torSetup{system: true}, own)
	for _, want := range []string{"keeps the system tor", "until it restarts", "asked for its own tor (/usr/bin/tor", "pid 42"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status %q does not name %q", status, want)
		}
	}
}

func TestTorFlagsLooksTorUpOnPath(t *testing.T) {
	good := fakeTorBin(t, 0o755)
	t.Setenv("PATH", filepath.Dir(good))
	got, err := torFlags(false, defaultTorBin, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.bin != good {
		t.Fatalf("bin = %q, want %q", got.bin, good)
	}

	t.Setenv("PATH", t.TempDir())
	_, err = torFlags(false, defaultTorBin, false)
	if err == nil || !strings.Contains(err.Error(), "PATH") || !strings.Contains(err.Error(), "--system-tor") {
		t.Fatalf("no tor on PATH: err = %v, want one naming PATH and --system-tor", err)
	}
}

func TestCheckTorBinOnWindows(t *testing.T) {
	notExec := fakeTorBin(t, 0o644)
	if err := checkTorBin(notExec, "windows"); err != nil {
		t.Fatalf("windows: %v", err)
	}
	if err := checkTorBin(notExec, "linux"); err == nil {
		t.Fatal("linux: a file without exec bits was taken")
	}
	if err := checkTorBin(filepath.Dir(notExec), "windows"); err == nil {
		t.Fatal("windows: a directory was taken")
	}
}

func TestTorSetupFromRuntime(t *testing.T) {
	if got := torSetupFromRuntime("", ""); got != (torSetup{system: true}) {
		t.Fatalf("no field: %+v", got)
	}
	if got := torSetupFromRuntime("/usr/bin/tor", "/r/tor"); got != (torSetup{bin: "/usr/bin/tor", dir: "/r/tor"}) {
		t.Fatalf("own tor: %+v", got)
	}
}

func TestSecretsForSpawn(t *testing.T) {
	s := secrets.Secrets{HaomadStorePassphrase: "a", FrontendStorePassphrase: "b", HaomadToken: "c", TorPassword: "tor-pw"}
	if got := secretsForSpawn(s, torSetup{bin: "/t", dir: "/d"}); got.TorPassword != "" || got.HaomadToken != "c" || got.HaomadStorePassphrase != "a" {
		t.Fatalf("own tor: %+v", got)
	}
	if got := secretsForSpawn(s, torSetup{system: true}); got.TorPassword != "tor-pw" {
		t.Fatalf("system tor: tor password %q, want tor-pw", got.TorPassword)
	}
}
