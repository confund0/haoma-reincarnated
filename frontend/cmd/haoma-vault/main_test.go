package main_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dgraph-io/badger/v4"

	"haoma-frontend/internal/backuparchive"
	"haoma-frontend/internal/lockfile"
	"haoma-frontend/internal/vault"
)

func buildBinary(t *testing.T) string {
	t.Helper()
	statSources(t, ".")
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

func statSources(t *testing.T, pkg string) {
	t.Helper()
	cmd := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.Dir}}{{end}}", pkg)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list %s: %v\n%s", pkg, err, stderr.String())
	}
	for _, dir := range strings.Fields(string(out)) {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatalf("glob %s: %v", dir, err)
		}
		for _, f := range files {
			if _, err := os.Stat(f); err != nil {
				t.Fatalf("stat %s: %v", f, err)
			}
		}
	}
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

func writeFile(t *testing.T, cfgDir, rel, body string) {
	t.Helper()
	full := filepath.Join(cfgDir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func readFile(t *testing.T, cfgDir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(cfgDir, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func archiveOf(t *testing.T, bin, cfgDir string) string {
	t.Helper()
	archive := filepath.Join(t.TempDir(), "backup.tar.zst")
	cmd := exec.Command(bin, "--cfg-dir", cfgDir, "--archive-write="+archive)
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("archive-write: %v\n%s", err, stderr.String())
	}
	return archive
}

func stage(t *testing.T, bin, cfgDir, archive string) string {
	t.Helper()
	cmd := exec.Command(bin, "--cfg-dir", cfgDir, "--archive-stage="+archive)
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("archive-stage: %v\n%s", err, stderr.String())
	}
	p := stagingPathFromStdout(out)
	if p == "" {
		t.Fatalf("archive-stage stdout empty; stderr=%s", stderr.String())
	}
	return p
}

func commit(t *testing.T, bin, cfgDir, staging, pass string) (int, string) {
	t.Helper()
	cmd := exec.Command(bin, "--cfg-dir", cfgDir, "--archive-commit="+staging)
	cmd.Stdin = strings.NewReader(pass)
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0, stderr.String()
	case errors.As(err, &exitErr):
		return exitErr.ExitCode(), stderr.String()
	default:
		t.Fatalf("archive-commit: %v", err)
		return -1, ""
	}
}

func markEmpty(t *testing.T, cfgDir string) {
	t.Helper()
	if err := backuparchive.MarkDeviceEmpty(cfgDir); err != nil {
		t.Fatalf("MarkDeviceEmpty: %v", err)
	}
}

func TestArchiveRestoreKeepsDeviceLocalState(t *testing.T) {
	bin := buildBinary(t)
	pass := vault.InsecureDefaultPassphrase

	src := t.TempDir()
	mintVault(t, bin, src, pass)
	writeFile(t, src, "backend/MANIFEST", "SRC-MANIFEST")
	writeFile(t, src, "disguise.enc", "SRC-DISGUISE")
	writeFile(t, src, "calc-help.txt", "SRC-KEYS")
	writeFile(t, src, "haomad.log", "noisy")
	srcVault := readFile(t, src, "vault.enc")
	archive := archiveOf(t, bin, src)

	dest := t.TempDir()
	mintVault(t, bin, dest, pass)
	writeFile(t, dest, "backend/MANIFEST", "LIVE-MANIFEST")
	writeFile(t, dest, "calc-help.txt", "LIVE-KEYS")
	writeFile(t, dest, "tor/torrc", "LIVE-TORRC")
	writeFile(t, dest, "tor/torrc.local", "LIVE-BRIDGES")
	writeFile(t, dest, "tor/lock", "")
	writeFile(t, dest, "haoma-text.log", "live log")
	writeFile(t, dest, "haoma-text.log.prev", "older live log")
	writeFile(t, dest, "vault.lock", "")
	markEmpty(t, dest)

	staging := stage(t, bin, dest, archive)
	if code, stderr := commit(t, bin, dest, staging, pass); code != 0 {
		t.Fatalf("archive-commit exit=%d\n%s", code, stderr)
	}
	if readFile(t, dest, "vault.enc") != srcVault {
		t.Error("vault.enc is not the archive's")
	}
	for rel, want := range map[string]string{
		"backend/MANIFEST":    "SRC-MANIFEST",
		"disguise.enc":        "SRC-DISGUISE",
		"calc-help.txt":       "SRC-KEYS",
		"tor/torrc":           "LIVE-TORRC",
		"tor/torrc.local":     "LIVE-BRIDGES",
		"haoma-text.log":      "live log",
		"haoma-text.log.prev": "older live log",
	} {
		if got := readFile(t, dest, rel); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "haomad.log")); err == nil {
		t.Error("haomad.log came with the archive; logs are not carried")
	}
	if _, err := os.Stat(staging); err == nil {
		t.Error("staging dir is still there after the commit")
	}
	if pre, _ := filepath.Glob(filepath.Join(dest, ".pre-restore-*")); len(pre) != 0 {
		t.Errorf(".pre-restore dirs after a restore: %v", pre)
	}
	if _, err := os.Stat(filepath.Join(dest, backuparchive.EmptyMarkerFile)); err == nil {
		t.Errorf("%s is still there after the restore", backuparchive.EmptyMarkerFile)
	}
}

func TestArchiveStageOfBrokenArchiveMovesNothing(t *testing.T) {
	bin := buildBinary(t)
	pass := vault.InsecureDefaultPassphrase
	src := t.TempDir()
	mintVault(t, bin, src, pass)
	writeFile(t, src, "backend/MANIFEST", strings.Repeat("M", 64<<10))
	archive := archiveOf(t, bin, src)
	raw, err := os.ReadFile(archive)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	if err := os.WriteFile(archive, raw[:len(raw)/2], 0o600); err != nil {
		t.Fatalf("truncate archive: %v", err)
	}

	dest := t.TempDir()
	mintVault(t, bin, dest, pass)
	writeFile(t, dest, "tor/torrc", "LIVE-TORRC")
	markEmpty(t, dest)
	liveVault := readFile(t, dest, "vault.enc")

	cmd := exec.Command(bin, "--cfg-dir", dest, "--archive-stage="+archive)
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	err = cmd.Run()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("archive-stage of a truncated archive: %v, want exit 1; stderr=%s", err, stderr.String())
	}
	if readFile(t, dest, "vault.enc") != liveVault {
		t.Error("vault.enc changed after a failed stage")
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".staging-") || strings.HasPrefix(e.Name(), ".pre-restore-") {
			t.Errorf("%s left after a failed stage", e.Name())
		}
	}
}

func TestArchiveCommitRefusedWhileStoreOpen(t *testing.T) {
	bin := buildBinary(t)
	pass := vault.InsecureDefaultPassphrase
	src := t.TempDir()
	mintVault(t, bin, src, pass)
	archive := archiveOf(t, bin, src)

	dest := t.TempDir()
	mintVault(t, bin, dest, pass)
	markEmpty(t, dest)
	liveVault := readFile(t, dest, "vault.enc")
	db, err := badger.Open(badger.DefaultOptions(filepath.Join(dest, "backend", "badger")).
		WithLoggingLevel(badger.ERROR))
	if err != nil {
		t.Fatalf("open badger: %v", err)
	}
	defer db.Close()

	staging := stage(t, bin, dest, archive)
	code, stderr := commit(t, bin, dest, staging, pass)
	if code != 5 {
		t.Fatalf("archive-commit exit=%d, want 5\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "still running") {
		t.Errorf("stderr does not say a daemon is still running: %s", stderr)
	}

	if want := fmt.Sprintf("pid %d", os.Getpid()); !strings.Contains(stderr, want) {
		t.Errorf("stderr does not name %s: %s", want, stderr)
	}
	if readFile(t, dest, "vault.enc") != liveVault {
		t.Error("vault.enc changed after a refused commit")
	}
	if _, err := os.Stat(filepath.Join(staging, "vault.enc")); err != nil {
		t.Errorf("staged vault.enc gone after a refused commit: %v", err)
	}
	if pre, _ := filepath.Glob(filepath.Join(dest, ".pre-restore-*")); len(pre) != 0 {
		t.Errorf(".pre-restore dirs after a refused commit: %v", pre)
	}
}

func TestArchiveRestoreModeIsGone(t *testing.T) {
	bin := buildBinary(t)
	dest := t.TempDir()
	if err := os.Chmod(dest, 0o700); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	cmd := exec.Command(bin, "--cfg-dir", dest, "--archive-restore=/nonexistent")
	if err := cmd.Run(); err == nil {
		t.Fatal("--archive-restore is accepted")
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
	err = cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("archive-commit with wrong pass: err=%v, want exit 1; stderr=%s", err, stderr.String())
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

func sealedDataDir(t *testing.T, pass string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "cfg")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	payload, err := vault.MintFreshPayload()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if err := vault.Create(filepath.Join(dir, "vault.enc"), []byte(pass), payload, vault.DefaultKDFParams); err != nil {
		t.Fatalf("create: %v", err)
	}
	return dir
}

func TestArchiveCommitPassphraseTrailingSpace(t *testing.T) {
	bin := buildBinary(t)
	for _, c := range []struct{ sealed, typed string }{
		{"pass ", "pass "},
		{"pass ", "pass \n"},
		{"pass", "pass \n"},
	} {
		src := sealedDataDir(t, c.sealed)
		archive := archiveOf(t, bin, src)
		dest := sealedDataDir(t, "dest")
		markEmpty(t, dest)
		staging := stage(t, bin, dest, archive)
		if code, stderr := commit(t, bin, dest, staging, c.typed); code != 0 {
			t.Errorf("sealed %q, typed %q: exit=%d\n%s", c.sealed, c.typed, code, stderr)
		}
	}
}

func writeMarker(t *testing.T, cfgDir string) {
	t.Helper()
	writeFile(t, cfgDir, ".restore-commit", `{"staging":".staging-20261001-115900"}`)
}

func TestPendingMarkerBlocks(t *testing.T) {
	bin := buildBinary(t)
	dest := sealedDataDir(t, "pass")
	markEmpty(t, dest)
	src := sealedDataDir(t, "pass")
	archive := archiveOf(t, bin, src)
	staging := stage(t, bin, dest, archive)
	if err := os.Remove(filepath.Join(dest, "vault.enc")); err != nil {
		t.Fatalf("remove vault: %v", err)
	}
	writeMarker(t, dest)

	run := func(stdin string, args ...string) (int, string) {
		cmd := exec.Command(bin, append([]string{"--cfg-dir", dest}, args...)...)
		cmd.Stdin = strings.NewReader(stdin)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err := cmd.Run()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), stderr.String()
		}
		if err != nil {
			t.Fatalf("run %v: %v", args, err)
		}
		return 0, stderr.String()
	}
	for _, c := range []struct {
		stdin string
		args  []string
	}{
		{"pass", nil},
		{"pass\n{}", []string{"-w"}},
		{"", []string{"--archive-stage=" + archive}},
		{"pass", []string{"--archive-commit=" + staging}},
		{"", []string{"--archive-discard=" + staging}},
		{"", []string{"--archive-write=" + filepath.Join(t.TempDir(), "b.tar.zst")}},
		{"", []string{"--check-empty"}},
		{"", []string{"--clean-restore-leftovers"}},
	} {
		code, stderr := run(c.stdin, c.args...)
		if code != 7 {
			t.Errorf("%v: exit=%d, want 7\n%s", c.args, code, stderr)
		}
		want := "The restore did not finish. Delete the folder " + dest + " and restore your backup again."
		if !strings.Contains(stderr, want) {
			t.Errorf("%v: stderr does not say %q: %s", c.args, want, stderr)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "vault.enc")); err == nil {
		t.Error("a vault was minted while the marker is there")
	}
	if _, err := os.Stat(staging); err != nil {
		t.Errorf("the staging dir named by the marker is gone: %v", err)
	}
	if code, stderr := run("", "--list-backups"); code != 0 {
		t.Errorf("--list-backups exit=%d\n%s", code, stderr)
	}
	if code, stderr := run("5", "--disguise-verify"); code != 2 {
		t.Errorf("--disguise-verify exit=%d, want 2 (no sidecar)\n%s", code, stderr)
	}
}

func TestArchiveCommitEarlyErrorIsNotApplied(t *testing.T) {
	bin := buildBinary(t)
	dir := filepath.Join(t.TempDir(), "cfg")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	code, stderr := commit(t, bin, dir, filepath.Join(dir, ".staging-x"), "pass")
	if code != 3 {
		t.Errorf("exit=%d, want 3\n%s", code, stderr)
	}
}

func runVault(t *testing.T, bin, cfgDir, stdin string, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(bin, append([]string{"--cfg-dir", cfgDir}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), stderr.String()
	}
	if err != nil {
		t.Fatalf("run %v: %v", args, err)
	}
	return 0, stderr.String()
}

func TestRestoreRefusedOnANotEmptyDevice(t *testing.T) {
	bin := buildBinary(t)
	src := sealedDataDir(t, "pass")
	archive := archiveOf(t, bin, src)
	dest := sealedDataDir(t, "pass")
	writeFile(t, dest, "frontend/badger/MANIFEST", "LIVE-STORE")
	liveVault := readFile(t, dest, "vault.enc")

	checkRefused := func(label string, code int, stderr string) {
		t.Helper()
		if code != 8 {
			t.Errorf("%s: exit=%d, want 8\n%s", label, code, stderr)
		}
		if !strings.Contains(stderr, backuparchive.NotEmptyMessage) {
			t.Errorf("%s: stderr does not say %q: %s", label, backuparchive.NotEmptyMessage, stderr)
		}
	}
	code, stderr := runVault(t, bin, dest, "", "--check-empty")
	checkRefused("--check-empty", code, stderr)
	code, stderr = runVault(t, bin, dest, "", "--archive-stage="+archive)
	checkRefused("--archive-stage", code, stderr)
	if st, _ := filepath.Glob(filepath.Join(dest, ".staging-*")); len(st) != 0 {
		t.Errorf("a refused stage left %v", st)
	}

	markEmpty(t, dest)
	staging := stage(t, bin, dest, archive)
	if err := backuparchive.ClearDeviceEmpty(dest); err != nil {
		t.Fatal(err)
	}
	code, stderr = commit(t, bin, dest, staging, "pass")
	checkRefused("--archive-commit", code, stderr)
	if readFile(t, dest, "vault.enc") != liveVault || readFile(t, dest, "frontend/badger/MANIFEST") != "LIVE-STORE" {
		t.Error("a refused commit changed the live state")
	}
	if _, err := os.Stat(filepath.Join(dest, ".restore-commit")); err == nil {
		t.Error("a refused commit left the marker")
	}
}

func TestCheckEmptyOnANewInstall(t *testing.T) {
	bin := buildBinary(t)
	dest := filepath.Join(t.TempDir(), "cfg")
	if err := os.Mkdir(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dest, "disguise.enc", "D")
	if code, stderr := runVault(t, bin, dest, "", "--check-empty"); code != 0 {
		t.Errorf("--check-empty exit=%d, want 0\n%s", code, stderr)
	}
}

func TestArchiveStageWaitsForTheVaultLock(t *testing.T) {
	bin := buildBinary(t)
	src := sealedDataDir(t, "pass")
	archive := archiveOf(t, bin, src)
	dest := filepath.Join(t.TempDir(), "cfg")
	if err := os.Mkdir(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	lk, err := lockfile.Acquire(filepath.Join(dest, "vault.enc.lock"))
	if err != nil {
		t.Fatalf("take the vault lock: %v", err)
	}
	cmd := exec.Command(bin, "--cfg-dir", dest, "--archive-stage="+archive)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		_ = lk.Release()
		t.Fatalf("the stage ran while the vault lock was held: %v\n%s", err, stderr.String())
	case <-time.After(500 * time.Millisecond):
	}
	if st, _ := filepath.Glob(filepath.Join(dest, ".staging-*")); len(st) != 0 {
		t.Errorf("a staging dir appeared while the vault lock was held: %v", st)
	}
	if err := lk.Release(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stage after the release: %v\n%s", err, stderr.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the stage did not finish after the lock was released")
	}
	if st, _ := filepath.Glob(filepath.Join(dest, ".staging-*")); len(st) != 1 {
		t.Errorf("staging dirs after the stage = %v, want one", st)
	}
}
