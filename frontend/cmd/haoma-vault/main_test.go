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

	payload, _, err := vault.Open(vaultPath, []byte(pass))
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

	got, _, err := vault.Open(vaultPath, []byte(pass))
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
	prev, _, err := vault.Open(backupPath, []byte(pass))
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

	payload, _, err := vault.Open(filepath.Join(cfgDir, "vault.enc"), []byte(pass))
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

	payload, _, err := vault.Open(vaultPath, []byte(pass))
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
	got, _, err := vault.Open(vaultPath, []byte(pass))
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

	payload, _, err := vault.Open(vaultPath, []byte(pass))
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

	got, _, err := vault.Open(vaultPath, []byte(pass))
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

func TestArchiveWriteAndRestoreRoundTrip(t *testing.T) {
	bin := buildBinary(t)
	cfgDir := t.TempDir()
	pass := vault.InsecureDefaultPassphrase
	mintVault(t, bin, cfgDir, pass)

	if err := os.MkdirAll(filepath.Join(cfgDir, "backend"), 0o700); err != nil {
		t.Fatalf("mkdir backend: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "backend", "MANIFEST"), []byte("manifest"), 0o600); err != nil {
		t.Fatalf("seed manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "backend", "haomad.log"), []byte("noisy"), 0o600); err != nil {
		t.Fatalf("seed log: %v", err)
	}

	archive := filepath.Join(t.TempDir(), "out.tar")
	cmd := exec.Command(bin, "--cfg-dir", cfgDir, "--archive-write="+archive)
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("archive-write: %v\n%s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "archive-write ok") {
		t.Errorf("expected ok stderr; got %q", stderr.String())
	}
	if info, err := os.Stat(archive); err != nil || info.Size() == 0 {
		t.Fatalf("archive not present or empty: %v", err)
	}

	dest := t.TempDir()
	if err := os.Chmod(dest, 0o700); err != nil {
		t.Fatalf("chmod dest: %v", err)
	}
	stderr.Reset()
	cmd = exec.Command(bin, "--cfg-dir", dest, "--archive-restore="+archive)
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("archive-restore: %v\n%s", err, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dest, "vault.enc")); err != nil {
		t.Errorf("restored vault.enc missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "backend", "MANIFEST")); err != nil {
		t.Errorf("restored backend manifest missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "backend", "haomad.log")); err == nil {
		t.Errorf("log file should not be in archive")
	}

	stderr.Reset()
	cmd = exec.Command(bin, "--cfg-dir", dest, "--archive-restore="+archive)
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("archive-restore #2: %v\n%s", err, stderr.String())
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var foundPreRestore bool
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".pre-restore-") {
			foundPreRestore = true
			break
		}
	}
	if !foundPreRestore {
		t.Errorf("expected a .pre-restore-* dir after second restore; entries: %v", entries)
	}
}

func stagingPathFromStdout(out []byte) string {
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

func TestArchiveStageCommitRoundTrip(t *testing.T) {
	bin := buildBinary(t)
	pass := vault.InsecureDefaultPassphrase

	src := t.TempDir()
	mintVault(t, bin, src, pass)
	if err := os.MkdirAll(filepath.Join(src, "backend"), 0o700); err != nil {
		t.Fatalf("mkdir backend: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "backend", "MANIFEST"), []byte("manifest"), 0o600); err != nil {
		t.Fatalf("seed manifest: %v", err)
	}

	payload, _, err := vault.Open(filepath.Join(src, "vault.enc"), []byte(pass))
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	payload.TorPassword = "from-source"
	jsonPayload, _ := json.Marshal(payload)
	cmd := exec.Command(bin, "--cfg-dir", src, "-w")
	cmd.Stdin = strings.NewReader(pass + "\n" + string(jsonPayload))
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("seed write: %v\n%s", err, stderr.String())
	}

	archive := filepath.Join(t.TempDir(), "backup.tar.zst")
	stderr.Reset()
	cmd = exec.Command(bin, "--cfg-dir", src, "--archive-write="+archive)
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("archive-write: %v\n%s", err, stderr.String())
	}

	dest := t.TempDir()
	if err := os.Chmod(dest, 0o700); err != nil {
		t.Fatalf("chmod dest: %v", err)
	}
	stderr.Reset()
	cmd = exec.Command(bin, "--cfg-dir", dest, "--archive-stage="+archive)
	cmd.Stderr = stderr
	stageOut, err := cmd.Output()
	if err != nil {
		t.Fatalf("archive-stage: %v\n%s", err, stderr.String())
	}
	stagingPath := stagingPathFromStdout(stageOut)
	if stagingPath == "" {
		t.Fatalf("archive-stage stdout empty; stderr=%s", stderr.String())
	}
	if !strings.HasPrefix(filepath.Base(stagingPath), ".staging-") {
		t.Errorf("staging basename should begin with .staging-, got %q", filepath.Base(stagingPath))
	}
	if _, err := os.Stat(filepath.Join(stagingPath, "vault.enc")); err != nil {
		t.Errorf("expected staged vault.enc: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dest, "vault.enc")); err == nil {
		t.Errorf("dest should not have vault.enc before commit")
	}

	stderr.Reset()
	cmd = exec.Command(bin, "--cfg-dir", dest, "--archive-commit="+stagingPath)
	cmd.Stdin = strings.NewReader(pass)
	cmd.Stderr = stderr
	commitOut, err := cmd.Output()
	if err != nil {
		t.Fatalf("archive-commit: %v\n%s", err, stderr.String())
	}
	if len(commitOut) == 0 {
		t.Errorf("archive-commit stdout empty; stderr=%s", stderr.String())
	}

	if _, err := os.Stat(stagingPath); err == nil {
		t.Errorf("staging dir should be removed after commit")
	}
	if _, err := os.Stat(filepath.Join(dest, "vault.enc")); err != nil {
		t.Errorf("dest vault.enc missing after commit: %v", err)
	}

	got, _, err := vault.Open(filepath.Join(dest, "vault.enc"), []byte(pass))
	if err != nil {
		t.Fatalf("open dest vault: %v", err)
	}
	if got.TorPassword != "from-source" {
		t.Errorf("restored TorPassword = %q, want from-source", got.TorPassword)
	}
}

func TestArchiveCommitWrongPassphraseLeavesStateUntouched(t *testing.T) {
	bin := buildBinary(t)
	pass := vault.InsecureDefaultPassphrase

	src := t.TempDir()
	mintVault(t, bin, src, pass)
	archive := filepath.Join(t.TempDir(), "backup.tar.zst")
	stderr := &bytes.Buffer{}
	cmd := exec.Command(bin, "--cfg-dir", src, "--archive-write="+archive)
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("archive-write: %v\n%s", err, stderr.String())
	}

	dest := t.TempDir()
	if err := os.Chmod(dest, 0o700); err != nil {
		t.Fatalf("chmod dest: %v", err)
	}
	stderr.Reset()
	cmd = exec.Command(bin, "--cfg-dir", dest, "--archive-stage="+archive)
	cmd.Stderr = stderr
	stageOut, err := cmd.Output()
	if err != nil {
		t.Fatalf("archive-stage: %v\n%s", err, stderr.String())
	}
	stagingPath := stagingPathFromStdout(stageOut)

	stderr.Reset()
	cmd = exec.Command(bin, "--cfg-dir", dest, "--archive-commit="+stagingPath)
	cmd.Stdin = strings.NewReader("not-the-passphrase")
	cmd.Stderr = stderr
	if err := cmd.Run(); err == nil {
		t.Fatalf("archive-commit with wrong pass should fail; stderr=%s", stderr.String())
	}

	if _, err := os.Stat(filepath.Join(stagingPath, "vault.enc")); err != nil {
		t.Errorf("staged vault.enc should survive wrong-pass commit: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dest, "vault.enc")); err == nil {
		t.Errorf("dest must not have vault.enc after wrong-pass commit")
	}

	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatalf("readdir dest: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".pre-restore-") {
			t.Errorf("wrong-pass commit must not create .pre-restore-* sidecar; saw %s", e.Name())
		}
	}
}

func TestArchiveDiscardRemovesStaging(t *testing.T) {
	bin := buildBinary(t)
	pass := vault.InsecureDefaultPassphrase

	src := t.TempDir()
	mintVault(t, bin, src, pass)
	archive := filepath.Join(t.TempDir(), "backup.tar.zst")
	stderr := &bytes.Buffer{}
	cmd := exec.Command(bin, "--cfg-dir", src, "--archive-write="+archive)
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("archive-write: %v\n%s", err, stderr.String())
	}

	dest := t.TempDir()
	if err := os.Chmod(dest, 0o700); err != nil {
		t.Fatalf("chmod dest: %v", err)
	}
	stderr.Reset()
	cmd = exec.Command(bin, "--cfg-dir", dest, "--archive-stage="+archive)
	cmd.Stderr = stderr
	stageOut, err := cmd.Output()
	if err != nil {
		t.Fatalf("archive-stage: %v\n%s", err, stderr.String())
	}
	stagingPath := stagingPathFromStdout(stageOut)

	stderr.Reset()
	cmd = exec.Command(bin, "--cfg-dir", dest, "--archive-discard="+stagingPath)
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("archive-discard: %v\n%s", err, stderr.String())
	}
	if _, err := os.Stat(stagingPath); err == nil {
		t.Errorf("staging dir should be removed by --archive-discard")
	}
}

func TestArchiveDiscardRejectsNonStagingPath(t *testing.T) {
	bin := buildBinary(t)
	dest := t.TempDir()
	if err := os.Chmod(dest, 0o700); err != nil {
		t.Fatalf("chmod dest: %v", err)
	}

	bogus := filepath.Join(dest, "some-other-dir")
	if err := os.MkdirAll(bogus, 0o700); err != nil {
		t.Fatalf("mkdir bogus: %v", err)
	}
	cmd := exec.Command(bin, "--cfg-dir", dest, "--archive-discard="+bogus)
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Run(); err == nil {
		t.Fatalf("--archive-discard on non-.staging path should fail")
	}

	if _, err := os.Stat(bogus); err != nil {
		t.Errorf("refused-path dir must be left untouched: %v", err)
	}
}

func TestArchiveStageCleansStalePriorStaging(t *testing.T) {
	bin := buildBinary(t)
	pass := vault.InsecureDefaultPassphrase

	src := t.TempDir()
	mintVault(t, bin, src, pass)
	archive := filepath.Join(t.TempDir(), "backup.tar.zst")
	stderr := &bytes.Buffer{}
	cmd := exec.Command(bin, "--cfg-dir", src, "--archive-write="+archive)
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("archive-write: %v\n%s", err, stderr.String())
	}

	dest := t.TempDir()
	if err := os.Chmod(dest, 0o700); err != nil {
		t.Fatalf("chmod dest: %v", err)
	}

	stale := filepath.Join(dest, ".staging-19700101-000000")
	if err := os.MkdirAll(filepath.Join(stale, "old-junk"), 0o700); err != nil {
		t.Fatalf("seed stale: %v", err)
	}
	stderr.Reset()
	cmd = exec.Command(bin, "--cfg-dir", dest, "--archive-stage="+archive)
	cmd.Stderr = stderr
	stageOut, err := cmd.Output()
	if err != nil {
		t.Fatalf("archive-stage with stale: %v\n%s", err, stderr.String())
	}
	if _, err := os.Stat(stale); err == nil {
		t.Errorf("stale staging dir should be removed by next --archive-stage")
	}
	fresh := stagingPathFromStdout(stageOut)
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh staging dir missing: %v", err)
	}
}
