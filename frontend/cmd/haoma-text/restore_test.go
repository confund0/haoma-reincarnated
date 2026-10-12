package main

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"haoma-frontend/internal/backuparchive"
	"haoma-frontend/internal/lockfile"
	"haoma-frontend/internal/supervisor"
	"haoma-frontend/internal/vault"
)

func buildHaomaVault(t *testing.T) string {
	t.Helper()
	statSources(t, "haoma-frontend/cmd/haoma-vault")
	out := filepath.Join(t.TempDir(), "haoma-vault")
	cmd := exec.Command("go", "build", "-o", out, "haoma-frontend/cmd/haoma-vault")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build haoma-vault: %v\n%s", err, stderr.String())
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

func newDataDir(t *testing.T, pass string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "cfg")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	payload, err := vault.MintFreshPayload()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if err := vault.Create(filepath.Join(dir, vaultFileName), []byte(pass), payload, vault.DefaultKDFParams); err != nil {
		t.Fatalf("create vault: %v", err)
	}
	return dir
}

func put(t *testing.T, dir, rel, body string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func get(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func backupOf(t *testing.T, bin, dir string) string {
	t.Helper()
	archive := filepath.Join(t.TempDir(), "backup.tar.zst")
	cmd := exec.Command(bin, "--cfg-dir", dir, "--archive-write="+archive)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("archive-write: %v\n%s", err, stderr.String())
	}
	return archive
}

func fixedPrompt(pass string, calls *int) passphrasePrompt {
	return func(string) ([]byte, error) {
		*calls++
		return []byte(pass), nil
	}
}

func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		if info.IsDir() {
			out[rel+"/"] = ""
			return nil
		}
		if rel == "vault.enc.lock" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return out
}

func sameSnapshot(t *testing.T, got, want map[string]string) {
	t.Helper()
	for k, v := range want {
		if g, ok := got[k]; !ok {
			t.Errorf("%s is missing", k)
		} else if g != v {
			t.Errorf("%s changed", k)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%s is new", k)
		}
	}
}

func markEmpty(t *testing.T, dir string) {
	t.Helper()
	if err := backuparchive.MarkDeviceEmpty(dir); err != nil {
		t.Fatalf("MarkDeviceEmpty: %v", err)
	}
}

func TestRestoreBackupKeepsTorAndLogs(t *testing.T) {
	bin := buildHaomaVault(t)
	src := newDataDir(t, "source-pass")
	put(t, src, "backend/MANIFEST", "SRC-MANIFEST")
	srcVault := get(t, src, vaultFileName)
	srcPayload, _, err := vault.Open(filepath.Join(src, vaultFileName), []byte("source-pass"))
	if err != nil {
		t.Fatalf("open source vault: %v", err)
	}
	archive := backupOf(t, bin, src)

	dest := newDataDir(t, "dest-pass")
	markEmpty(t, dest)
	put(t, dest, "backend/MANIFEST", "LIVE-MANIFEST")
	put(t, dest, "tor/torrc", "LIVE-TORRC")
	put(t, dest, "tor/torrc.local", "LIVE-BRIDGES")
	put(t, dest, "tor/lock", "")
	put(t, dest, "haoma-text.log", "live log")
	put(t, dest, "haoma-text.log.prev", "older live log")
	put(t, dest, "vault.lock", "")

	calls := 0
	got, report, err := runRestoreBackup(dest, bin, archive, fixedPrompt("source-pass", &calls))
	if err != nil {
		t.Fatalf("runRestoreBackup: %v", err)
	}
	if len(report.Lines()) != 0 {
		t.Errorf("report = %v, want nothing to say", report.Lines())
	}
	if pre, _ := filepath.Glob(filepath.Join(dest, ".pre-restore-*")); len(pre) != 0 {
		t.Errorf("a restore kept a copy of the earlier state: %v", pre)
	}
	if _, err := os.Stat(filepath.Join(dest, backuparchive.EmptyMarkerFile)); err == nil {
		t.Errorf("%s is still there after the restore", backuparchive.EmptyMarkerFile)
	}
	if calls != 1 {
		t.Errorf("prompted %d times, want 1", calls)
	}
	if string(got.passphrase) != "source-pass" {
		t.Errorf("returned passphrase is not the backed-up device's")
	}
	if got.payload.HaomadToken != srcPayload.HaomadToken {
		t.Error("returned payload is not the backed-up vault's")
	}
	if get(t, dest, vaultFileName) != srcVault {
		t.Error("vault.enc is not the backed-up one")
	}
	for rel, want := range map[string]string{
		"backend/MANIFEST":    "SRC-MANIFEST",
		"tor/torrc":           "LIVE-TORRC",
		"tor/torrc.local":     "LIVE-BRIDGES",
		"haoma-text.log":      "live log",
		"haoma-text.log.prev": "older live log",
	} {
		if g := get(t, dest, rel); g != want {
			t.Errorf("%s = %q, want %q", rel, g, want)
		}
	}
}

func TestRestoreBackupBrokenArchiveChangesNothing(t *testing.T) {
	bin := buildHaomaVault(t)
	src := newDataDir(t, "source-pass")
	put(t, src, "backend/MANIFEST", strings.Repeat("M", 64<<10))
	archive := backupOf(t, bin, src)
	raw, err := os.ReadFile(archive)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	if err := os.WriteFile(archive, raw[:len(raw)/2], 0o600); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	dest := newDataDir(t, "dest-pass")
	markEmpty(t, dest)
	put(t, dest, "tor/torrc", "LIVE-TORRC")
	put(t, dest, "haoma-text.log", "live log")
	before := snapshot(t, dest)

	calls := 0
	_, _, err = runRestoreBackup(dest, bin, archive, fixedPrompt("source-pass", &calls))
	if !errors.Is(err, errRestoreNotApplied) {
		t.Fatalf("err = %v, want errRestoreNotApplied", err)
	}
	if calls != 0 {
		t.Errorf("prompted %d times for a broken archive", calls)
	}
	sameSnapshot(t, snapshot(t, dest), before)
}

func TestRestoreBackupWrongPassphraseChangesNothing(t *testing.T) {
	bin := buildHaomaVault(t)
	src := newDataDir(t, "source-pass")
	archive := backupOf(t, bin, src)

	dest := newDataDir(t, "dest-pass")
	markEmpty(t, dest)
	put(t, dest, "tor/torrc", "LIVE-TORRC")
	cert, pemBytes := selfSignedCert(t)
	put(t, dest, "backend/cert.pem", string(pemBytes))
	_, gone := fakeHaomad(t, dest, cert)
	before := snapshot(t, dest)

	calls := 0
	_, _, err := runRestoreBackup(dest, bin, archive, fixedPrompt("not-it", &calls))
	if !errors.Is(err, errRestoreNotApplied) {
		t.Fatalf("err = %v, want errRestoreNotApplied", err)
	}
	if calls != restorePassphraseTries {
		t.Errorf("prompted %d times, want %d", calls, restorePassphraseTries)
	}
	sameSnapshot(t, snapshot(t, dest), before)
	select {
	case <-gone:
		t.Error("haomad was stopped for a restore that never got past the passphrase")
	case <-time.After(200 * time.Millisecond):
	}
}

func selfSignedCert(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		DNSNames:     []string{"haoma-backend"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pemBytes
}

func fakeHaomad(t *testing.T, root string, cert tls.Certificate) (*exec.Cmd, <-chan struct{}) {
	t.Helper()
	return fakeHaomadRunning(t, root, cert, exec.Command("sleep", "60"))
}

func fakeHaomadRunning(t *testing.T, root string, cert tls.Certificate, proc *exec.Cmd) (*exec.Cmd, <-chan struct{}) {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.(*tls.Conn).Handshake()
			_ = c.Close()
		}
	}()
	if proc.Process == nil {
		if err := proc.Start(); err != nil {
			t.Fatalf("start %s: %v", proc.Path, err)
		}
	}
	gone := make(chan struct{})
	go func() { _ = proc.Wait(); close(gone) }()
	t.Cleanup(func() { _ = proc.Process.Kill(); <-gone })

	info := supervisor.RuntimeInfo{PID: proc.Process.Pid, APIAddr: ln.Addr().(*net.TCPAddr).String(), StartedAt: time.Now()}
	raw, _ := json.Marshal(info)
	put(t, root, runtimeFileName, string(raw))
	return proc, gone
}

func TestStopHaomadForRestoreStopsThisHaomad(t *testing.T) {
	root := t.TempDir()
	cert, pemBytes := selfSignedCert(t)
	put(t, root, "backend/cert.pem", string(pemBytes))
	_, gone := fakeHaomad(t, root, cert)

	if err := stopHaomadForRestore(root); err != nil {
		t.Fatalf("stopHaomadForRestore: %v", err)
	}
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("the process named in the runtime file is still running")
	}
}

func TestStopHaomadForRestoreLeavesAStranger(t *testing.T) {
	root := t.TempDir()
	_, ownPEM := selfSignedCert(t)
	put(t, root, "backend/cert.pem", string(ownPEM))
	otherCert, _ := selfSignedCert(t)
	proc, gone := fakeHaomad(t, root, otherCert)

	if err := stopHaomadForRestore(root); err != nil {
		t.Fatalf("stopHaomadForRestore: %v", err)
	}
	select {
	case <-gone:
		t.Fatal("a process that is not this haomad was stopped")
	case <-time.After(300 * time.Millisecond):
	}
	if err := proc.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("the stranger is gone: %v", err)
	}
}

const holdDirEnv = "HAOMA_TEST_HOLD_STORE_DIR"

func TestHelperHoldsStoreLock(t *testing.T) {
	dir := os.Getenv(holdDirEnv)
	if dir == "" {
		t.Skip("helper process only")
	}
	if _, err := lockfile.TryLockDir(dir); err != nil {
		t.Fatalf("lock: %v", err)
	}
	os.Stdout.WriteString("held\n")
	time.Sleep(60 * time.Second)
}

func TestRestoreBackupStopsARunningHaomad(t *testing.T) {
	bin := buildHaomaVault(t)
	src := newDataDir(t, "source-pass")
	srcVault := get(t, src, vaultFileName)
	archive := backupOf(t, bin, src)

	dest := newDataDir(t, "dest-pass")
	markEmpty(t, dest)
	store := filepath.Join(dest, "backend", "badger")
	if err := os.MkdirAll(store, 0o700); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
	cert, pemBytes := selfSignedCert(t)
	put(t, dest, "backend/cert.pem", string(pemBytes))

	_, gone := fakeHaomadRunning(t, dest, cert, startStoreHolder(t, store))

	calls := 0
	if _, _, err := runRestoreBackup(dest, bin, archive, fixedPrompt("source-pass", &calls)); err != nil {
		t.Fatalf("runRestoreBackup: %v", err)
	}
	select {
	case <-gone:
	case <-time.After(2 * time.Second):
		t.Error("the haomad stand-in is still running after the restore")
	}
	if get(t, dest, vaultFileName) != srcVault {
		t.Error("vault.enc is not the backed-up one")
	}
}

func startStoreHolder(t *testing.T, store string) *exec.Cmd {
	t.Helper()
	helper := exec.Command(os.Args[0], "-test.run=^TestHelperHoldsStoreLock$")
	helper.Env = append(os.Environ(), holdDirEnv+"="+store)
	out, err := helper.StdoutPipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if err := helper.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	if line, err := bufio.NewReader(out).ReadString('\n'); err != nil || line != "held\n" {
		_ = helper.Process.Kill()
		t.Fatalf("helper said %q (err %v), want held", line, err)
	}
	if err := os.WriteFile(filepath.Join(store, "LOCK"), []byte(fmt.Sprintf("%d\n", helper.Process.Pid)), 0o600); err != nil {
		t.Fatalf("write LOCK: %v", err)
	}
	return helper
}

func TestRestoreBackupStrangerHoldsTheStore(t *testing.T) {
	bin := buildHaomaVault(t)
	src := newDataDir(t, "source-pass")
	archive := backupOf(t, bin, src)

	dest := newDataDir(t, "dest-pass")
	markEmpty(t, dest)
	store := filepath.Join(dest, "backend", "badger")
	if err := os.MkdirAll(store, 0o700); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
	_, ownPEM := selfSignedCert(t)
	put(t, dest, "backend/cert.pem", string(ownPEM))
	otherCert, _ := selfSignedCert(t)
	holder := startStoreHolder(t, store)
	_, gone := fakeHaomadRunning(t, dest, otherCert, holder)
	before := snapshot(t, dest)

	calls := 0
	_, _, err := runRestoreBackup(dest, bin, archive, fixedPrompt("source-pass", &calls))
	if !errors.Is(err, errRestoreNotApplied) {
		t.Fatalf("err = %v, want errRestoreNotApplied", err)
	}
	if want := fmt.Sprintf("pid %d", holder.Process.Pid); !strings.Contains(err.Error(), want) {
		t.Errorf("error does not name %s: %v", want, err)
	}
	sameSnapshot(t, snapshot(t, dest), before)
	select {
	case <-gone:
		t.Error("a process that is not this haomad was stopped")
	default:
	}
}

func TestOpenOrCreateVaultRefusesWhileRestorePending(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, ".restore-commit", `{"staging":".staging-20261001-115900"}`)
	_, err := openOrCreateVault(filepath.Join(dir, vaultFileName))
	if err == nil {
		t.Fatal("openOrCreateVault went on while a restore is pending")
	}
	want := "The restore did not finish. Delete the folder " + dir + " and restore your backup again."
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
	if _, err := os.Stat(filepath.Join(dir, vaultFileName)); err == nil {
		t.Error("a vault was minted")
	}
}

func TestRestoreBackupRefusedOnANotEmptyDevice(t *testing.T) {
	bin := buildHaomaVault(t)
	src := newDataDir(t, "source-pass")
	archive := backupOf(t, bin, src)

	dest := newDataDir(t, "dest-pass")
	put(t, dest, "frontend/badger/MANIFEST", "LIVE-STORE")
	cert, pemBytes := selfSignedCert(t)
	put(t, dest, "backend/cert.pem", string(pemBytes))
	_, gone := fakeHaomad(t, dest, cert)
	before := snapshot(t, dest)

	calls := 0
	_, _, err := runRestoreBackup(dest, bin, archive, fixedPrompt("source-pass", &calls))
	if !errors.Is(err, errRestoreNotEmpty) {
		t.Fatalf("err = %v, want errRestoreNotEmpty", err)
	}
	if err.Error() != backuparchive.NotEmptyMessage {
		t.Errorf("error = %q, want only %q", err, backuparchive.NotEmptyMessage)
	}
	if calls != 0 {
		t.Errorf("prompted %d times on a device that is not empty", calls)
	}
	sameSnapshot(t, snapshot(t, dest), before)
	select {
	case <-gone:
		t.Error("haomad was stopped for a restore that was refused")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestRestoreBackupAfterABrokenRestore(t *testing.T) {
	bin := buildHaomaVault(t)
	src := newDataDir(t, "source-pass")
	archive := backupOf(t, bin, src)
	dest := t.TempDir()
	if err := os.Chmod(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	put(t, dest, ".restore-commit", `{"staging":".staging-20261001-115900"}`)

	calls := 0
	_, _, err := runRestoreBackup(dest, bin, archive, fixedPrompt("source-pass", &calls))
	if !errors.Is(err, errRestoreBroken) {
		t.Fatalf("err = %v, want errRestoreBroken", err)
	}
	want := "The restore did not finish. Delete the folder " + dest + " and restore your backup again."
	if err.Error() != want {
		t.Errorf("error = %q, want only %q", err, want)
	}
}

func TestCleanRestoreLeftovers(t *testing.T) {
	bin := buildHaomaVault(t)
	dest := t.TempDir()
	if err := os.Chmod(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	put(t, outside, "keep/secret", "OUTSIDE")
	put(t, dest, "vault.enc", "LIVE")
	put(t, dest, "tor/torrc", "TORRC")
	put(t, dest, ".staging-20261001-115900/vault.enc", "STAGED")
	put(t, dest, ".pre-restore-20260901-120000/vault.enc", "OLD")
	put(t, dest, ".pre-restore-20260901-120000/backend/MANIFEST", "OLD")
	if err := os.Symlink(filepath.Join(outside, "keep"), filepath.Join(dest, ".pre-restore-20260902-120000")); err != nil {
		t.Fatal(err)
	}
	cleanRestoreLeftovers(bin, dest)
	for _, gone := range []string{".staging-20261001-115900", ".pre-restore-20260901-120000"} {
		if _, err := os.Lstat(filepath.Join(dest, gone)); err == nil {
			t.Errorf("%s is still there after the start", gone)
		}
	}
	if _, err := os.Lstat(filepath.Join(dest, ".pre-restore-20260902-120000")); err != nil {
		t.Errorf("the symlink was removed: %v", err)
	}
	if got := get(t, outside, "keep/secret"); got != "OUTSIDE" {
		t.Errorf("what the symlink points at = %q", got)
	}
	if get(t, dest, "vault.enc") != "LIVE" || get(t, dest, "tor/torrc") != "TORRC" {
		t.Error("a live entry changed")
	}

	put(t, dest, ".staging-20261001-130000/vault.enc", "STAGED")
	put(t, dest, ".pre-restore-20260903-120000/vault.enc", "OLD")
	put(t, dest, ".restore-commit", `{"staging":".staging-20261001-130000"}`)
	cleanRestoreLeftovers(bin, dest)
	for _, kept := range []string{".staging-20261001-130000", ".pre-restore-20260903-120000"} {
		if _, err := os.Lstat(filepath.Join(dest, kept)); err != nil {
			t.Errorf("%s went while a restore did not finish: %v", kept, err)
		}
	}
}
