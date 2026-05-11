package main_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"haoma-frontend/internal/vault"
)

func buildBinary(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "haoma-vault")
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = "."
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build: %v\n%s", err, stderr.String())
	}
	return out
}

func mintVault(t *testing.T, bin, cfgDir, passphrase string) []byte {
	t.Helper()
	if err := os.Chmod(cfgDir, 0o700); err != nil {
		t.Fatalf("chmod %s: %v", cfgDir, err)
	}
	cmd := exec.Command(bin, "--cfg-dir", cfgDir)
	cmd.Stdin = strings.NewReader(passphrase)
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("read mode: %v\n%s", err, stderr.String())
	}
	return out
}

func TestWriteModeRoundTrip(t *testing.T) {
	bin := buildBinary(t)
	cfgDir := t.TempDir()
	pass := vault.InsecureDefaultPassphrase

	mintVault(t, bin, cfgDir, pass)
	vaultPath := filepath.Join(cfgDir, "vault.enc")

	payload, _, err := vault.Open(vaultPath, pass)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if payload.TorPassword != "" {
		t.Fatalf("fresh vault should have empty TorPassword, got %q", payload.TorPassword)
	}

	payload.TorPassword = "swordfish"
	payload.NotifyShellEnabled = !payload.NotifyShellEnabled
	wantNotify := payload.NotifyShellEnabled

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	cmd := exec.Command(bin, "--cfg-dir", cfgDir, "-w")
	cmd.Stdin = strings.NewReader(pass + "\n" + string(jsonPayload))
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("-w: %v\n%s", err, stderr.String())
	}

	got, _, err := vault.Open(vaultPath, pass)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	if got.TorPassword != "swordfish" {
		t.Errorf("after -w: TorPassword = %q, want 'swordfish'", got.TorPassword)
	}
	if got.NotifyShellEnabled != wantNotify {
		t.Errorf("after -w: NotifyShellEnabled = %v, want %v", got.NotifyShellEnabled, wantNotify)
	}

	backupPath := vaultPath + ".1"
	info, err := os.Stat(backupPath)
	if err != nil {
		t.Fatalf("expected %s to exist after -w: %v", backupPath, err)
	}
	if info.Size() == 0 {
		t.Errorf("backup is empty")
	}
	prev, _, err := vault.Open(backupPath, pass)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	if prev.TorPassword != "" {
		t.Errorf("backup should retain pre-write TorPassword \"\", got %q", prev.TorPassword)
	}
}

func TestWriteModeRejectsInvalidEnum(t *testing.T) {
	bin := buildBinary(t)
	cfgDir := t.TempDir()
	pass := vault.InsecureDefaultPassphrase
	mintVault(t, bin, cfgDir, pass)

	payload, _, err := vault.Open(filepath.Join(cfgDir, "vault.enc"), pass)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	payload.IdleAction = "panic-lock"

	jsonPayload, _ := json.Marshal(payload)
	cmd := exec.Command(bin, "--cfg-dir", cfgDir, "-w")
	cmd.Stdin = strings.NewReader(pass + "\n" + string(jsonPayload))
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	err = cmd.Run()
	if err == nil {
		t.Fatalf("-w with invalid enum should fail")
	}
	if !strings.Contains(stderr.String(), "idle_action") {
		t.Errorf("stderr should mention idle_action: %s", stderr.String())
	}
}

func TestWriteModeRejectsUnknownField(t *testing.T) {
	bin := buildBinary(t)
	cfgDir := t.TempDir()
	pass := vault.InsecureDefaultPassphrase
	mintVault(t, bin, cfgDir, pass)

	junk := `{"haomad_store_passphrase":"x","frontend_store_passphrase":"y","haomad_token":"z","mystery_field":"oops"}`
	cmd := exec.Command(bin, "--cfg-dir", cfgDir, "-w")
	cmd.Stdin = strings.NewReader(pass + "\n" + junk)
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	err := cmd.Run()
	if err == nil {
		t.Fatalf("-w with unknown field should fail")
	}
	if !strings.Contains(stderr.String(), "mystery_field") {
		t.Errorf("stderr should name unknown field: %s", stderr.String())
	}
}

func TestListAndRestoreBackups(t *testing.T) {
	bin := buildBinary(t)
	cfgDir := t.TempDir()
	pass := vault.InsecureDefaultPassphrase
	mintVault(t, bin, cfgDir, pass)
	vaultPath := filepath.Join(cfgDir, "vault.enc")

	payload, _, err := vault.Open(vaultPath, pass)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	for _, val := range []string{"v1", "v2", "v3"} {
		payload.TorPassword = val
		jsonPayload, _ := json.Marshal(payload)
		cmd := exec.Command(bin, "--cfg-dir", cfgDir, "-w")
		cmd.Stdin = strings.NewReader(pass + "\n" + string(jsonPayload))
		stderr := &bytes.Buffer{}
		cmd.Stderr = stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("-w %s: %v\n%s", val, err, stderr.String())
		}
	}

	cmd := exec.Command(bin, "--cfg-dir", cfgDir, "--list-backups")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	listing := string(out)
	for _, want := range []string{".1", ".2"} {
		if !strings.Contains(listing, want) {
			t.Errorf("--list-backups missing %s: %s", want, listing)
		}
	}

	cmd = exec.Command(bin, "--cfg-dir", cfgDir, "--restore=1")
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("--restore=1: %v\n%s", err, stderr.String())
	}
	got, _, err := vault.Open(vaultPath, pass)
	if err != nil {
		t.Fatalf("open after restore: %v", err)
	}
	if got.TorPassword != "v2" {
		t.Errorf("after --restore=1: TorPassword = %q, want v2", got.TorPassword)
	}
}

func TestWriteModeFlockSerializesConcurrentWrites(t *testing.T) {
	bin := buildBinary(t)
	cfgDir := t.TempDir()
	pass := vault.InsecureDefaultPassphrase
	mintVault(t, bin, cfgDir, pass)
	vaultPath := filepath.Join(cfgDir, "vault.enc")

	payload, _, err := vault.Open(vaultPath, pass)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	type result struct {
		val    string
		stderr string
		err    error
	}
	done := make(chan result, 2)
	for _, val := range []string{"raceA", "raceB"} {
		val := val
		go func() {
			p := payload
			p.TorPassword = val
			j, _ := json.Marshal(p)
			cmd := exec.Command(bin, "--cfg-dir", cfgDir, "-w")
			cmd.Stdin = strings.NewReader(pass + "\n" + string(j))
			buf := &bytes.Buffer{}
			cmd.Stderr = buf
			err := cmd.Run()
			done <- result{val, buf.String(), err}
		}()
	}
	for i := 0; i < 2; i++ {
		r := <-done
		if r.err != nil {
			t.Errorf("writer %s failed: %v\n%s", r.val, r.err, r.stderr)
		}
	}

	got, _, err := vault.Open(vaultPath, pass)
	if err != nil {
		t.Fatalf("post-race open: %v", err)
	}
	if got.TorPassword != "raceA" && got.TorPassword != "raceB" {
		t.Errorf("post-race TorPassword = %q, want raceA or raceB", got.TorPassword)
	}

	if _, err := os.Stat(vaultPath + ".1"); err != nil {
		t.Errorf("expected .1 backup after concurrent writes: %v", err)
	}
}

func TestModesMutuallyExclusive(t *testing.T) {
	bin := buildBinary(t)
	cfgDir := t.TempDir()
	if err := os.Chmod(cfgDir, 0o700); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	cmd := exec.Command(bin, "--cfg-dir", cfgDir, "-w", "--list-backups")
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	err := cmd.Run()
	if err == nil {
		t.Fatalf("expected error for combined modes")
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Errorf("stderr: %s", stderr.String())
	}
}
