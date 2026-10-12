package backuparchive

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgraph-io/badger/v4"
)

func tree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			out[rel+"/"] = ""
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
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

func sameTree(t *testing.T, label string, got, want map[string]string) {
	t.Helper()
	for k, v := range want {
		g, ok := got[k]
		if !ok {
			t.Errorf("%s: %s is missing", label, k)
			continue
		}
		if g != v {
			t.Errorf("%s: %s = %q, want %q", label, k, g, v)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%s: %s is there and should not be", label, k)
		}
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func stagedFixture(t *testing.T) (live, staging string) {
	t.Helper()
	src := makeCfg(t)
	write(t, filepath.Join(src, "vault.enc"), "SRC-VAULT")
	write(t, filepath.Join(src, "calc-help.txt"), "SRC-KEYS")
	write(t, filepath.Join(src, "backend", "MANIFEST"), "SRC-MANIFEST")
	archive := filepath.Join(t.TempDir(), "out.tar.zst")
	if _, _, err := Create(src, archive); err != nil {
		t.Fatalf("Create: %v", err)
	}

	live = makeCfg(t)
	write(t, filepath.Join(live, "vault.enc"), "LIVE-VAULT")
	write(t, filepath.Join(live, "calc-help.txt"), "LIVE-KEYS")
	write(t, filepath.Join(live, "backend", "MANIFEST"), "LIVE-MANIFEST")
	write(t, filepath.Join(live, "backend", "LIVE-ONLY"), "LIVE-ONLY")
	write(t, filepath.Join(live, "tor", "torrc"), "LIVE-TORRC")
	if err := MarkDeviceEmpty(live); err != nil {
		t.Fatalf("MarkDeviceEmpty: %v", err)
	}
	staging = filepath.Join(live, ".staging-20261001-120000")
	if _, _, err := Extract(archive, staging); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	return live, staging
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func noCopyLeft(t *testing.T, cfgDir string) {
	t.Helper()
	entries, err := os.ReadDir(cfgDir)
	if err != nil {
		t.Fatalf("read %s: %v", cfgDir, err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".pre-restore-") {
			t.Errorf("%s is there; a restore keeps no copy", e.Name())
		}
	}
	if _, err := os.Stat(filepath.Join(cfgDir, "backend", "LIVE-ONLY")); err == nil {
		t.Error("backend/LIVE-ONLY of the earlier state is still there")
	}
}

func TestCommitKeepsDeviceLocalState(t *testing.T) {
	live, staging := stagedFixture(t)
	before := tree(t, live)

	if _, err := Commit(live, staging); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := read(t, filepath.Join(live, "vault.enc")); got != "SRC-VAULT" {
		t.Errorf("vault.enc = %q, want the archive's", got)
	}
	if got := read(t, filepath.Join(live, "calc-help.txt")); got != "SRC-KEYS" {
		t.Errorf("calc-help.txt = %q, want the archive's", got)
	}
	if got := read(t, filepath.Join(live, "backend", "MANIFEST")); got != "SRC-MANIFEST" {
		t.Errorf("backend/MANIFEST = %q, want the archive's", got)
	}
	for _, rel := range []string{
		"tor/torrc", "tor/torrc.local", "tor/cached-certs",
		"haoma-text.log", "haoma-text.log.prev", "vault.lock",
		"vault.enc.lock", "haomad.runtime.json", "scratch/random.tmp",
	} {
		if got := read(t, filepath.Join(live, rel)); got != before[rel] {
			t.Errorf("%s = %q, want it left as it was (%q)", rel, got, before[rel])
		}
	}
	noCopyLeft(t, live)
	if _, err := os.Stat(filepath.Join(live, EmptyMarkerFile)); err == nil {
		t.Errorf("%s is still there after the restore", EmptyMarkerFile)
	}
	if empty, err := DeviceIsEmpty(live); err != nil || empty {
		t.Errorf("DeviceIsEmpty after the restore = %v, %v; want false", empty, err)
	}
}

func TestCommitOntoNewInstall(t *testing.T) {
	_, staging0 := stagedFixture(t)
	live := t.TempDir()
	write(t, filepath.Join(live, "tor", "torrc"), "LIVE-TORRC")
	write(t, filepath.Join(live, "haoma-text.log"), "LOG")
	write(t, filepath.Join(live, "disguise.enc"), "LIVE-DISGUISE")
	write(t, filepath.Join(live, "calc-help.txt"), "LIVE-KEYS")
	if err := os.MkdirAll(filepath.Join(live, "backend"), 0o700); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(live, filepath.Base(staging0))
	if err := os.Rename(staging0, staging); err != nil {
		t.Fatalf("move staging: %v", err)
	}
	if _, err := Commit(live, staging); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := read(t, filepath.Join(live, "vault.enc")); got != "SRC-VAULT" {
		t.Errorf("vault.enc = %q, want the archive's", got)
	}
	noMarker(t, live)
}

func TestCommitRefusesANotEmptyDevice(t *testing.T) {
	live, staging := stagedFixture(t)
	if err := ClearDeviceEmpty(live); err != nil {
		t.Fatalf("ClearDeviceEmpty: %v", err)
	}
	beforeLive := tree(t, live)
	_, err := Commit(live, staging)
	if !errors.Is(err, ErrDeviceNotEmpty) {
		t.Fatalf("Commit err = %v, want ErrDeviceNotEmpty", err)
	}
	sameTree(t, "data dir", tree(t, live), beforeLive)
	noMarker(t, live)
}

func TestCommitArchiveWithoutCalcHelp(t *testing.T) {
	live, staging := stagedFixture(t)
	if err := os.Remove(filepath.Join(staging, "calc-help.txt")); err != nil {
		t.Fatalf("remove staged calc-help.txt: %v", err)
	}
	res, err := Commit(live, staging)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := read(t, filepath.Join(live, "calc-help.txt")); got != "LIVE-KEYS" {
		t.Errorf("calc-help.txt after the restore = %q, want the live one kept", got)
	}
	if strings.Join(res.KeptLive, ",") != "calc-help.txt" {
		t.Errorf("KeptLive = %v, want [calc-help.txt]", res.KeptLive)
	}
	if got := read(t, filepath.Join(live, "vault.enc")); got != "SRC-VAULT" {
		t.Errorf("vault.enc = %q, want the archive's", got)
	}
}

func noMarker(t *testing.T, cfgDir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(cfgDir, MarkerFile)); err == nil {
		t.Errorf("%s is still there", MarkerFile)
	}
}

type crash struct{}

func crashAfter(t *testing.T, n int) {
	t.Helper()
	prevRename, prevRemove := rename, removeAll
	calls := 0
	step := func() {
		if calls == n {
			panic(crash{})
		}
		calls++
	}
	rename = func(src, dst string) error { step(); return prevRename(src, dst) }
	removeAll = func(p string) error { step(); return prevRemove(p) }
	t.Cleanup(func() { rename, removeAll = prevRename, prevRemove })
}

func TestCommitCrashLeavesMarker(t *testing.T) {

	const steps = 16
	for k := 0; k < steps; k++ {
		live, staging := stagedFixture(t)
		crashAfter(t, k)
		func() {
			defer func() {
				if r := recover(); r != nil {
					if _, ok := r.(crash); !ok {
						panic(r)
					}
				}
			}()
			_, _ = Commit(live, staging)
			t.Fatalf("k=%d: Commit returned; the crash did not happen", k)
		}()
		rename, removeAll = os.Rename, os.RemoveAll

		p, err := PendingCommit(live)
		if err != nil || p == nil {
			t.Fatalf("k=%d: PendingCommit = %v, %v; want the marker", k, p, err)
		}
		if p.Staging != filepath.Base(staging) {
			t.Errorf("k=%d: marker names %+v", k, p)
		}

		if _, err := os.Stat(filepath.Join(live, "vault.enc")); k > 0 && err == nil {
			t.Errorf("k=%d: the data dir holds a vault.enc while its stores are half moved", k)
		}
		if empty, _ := DeviceIsEmpty(live); empty {
			t.Errorf("k=%d: a device part way through a restore says it is empty", k)
		}
		entries, _ := os.ReadDir(live)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".pre-restore-") {
				t.Errorf("k=%d: %s is there", k, e.Name())
			}
		}
		_, err = Commit(live, staging)
		if !errors.Is(err, ErrCommitPending) {
			t.Errorf("k=%d: second Commit err = %v, want ErrCommitPending", k, err)
		}
		want := "The restore did not finish. Delete the folder " + live + " and restore your backup again."
		if got := p.Message(live); got != want {
			t.Errorf("k=%d: message = %q, want %q", k, got, want)
		}
	}
}

func TestCommitRemovesMarker(t *testing.T) {
	live, staging := stagedFixture(t)
	if _, err := Commit(live, staging); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	noMarker(t, live)
}

func failRename(t *testing.T, match func(src, dst string) bool) {
	t.Helper()
	prev := rename
	rename = func(src, dst string) error {
		if match(src, dst) {
			return &os.LinkError{Op: "rename", Old: src, New: dst, Err: errors.New("seeded failure")}
		}
		return prev(src, dst)
	}
	t.Cleanup(func() { rename = prev })
}

func TestCommitFailedMoveLeavesMarker(t *testing.T) {
	live, staging := stagedFixture(t)
	failRename(t, func(src, dst string) bool {
		return strings.HasPrefix(src, staging) && filepath.Base(dst) == "textUI"
	})
	_, err := Commit(live, staging)
	if !errors.Is(err, ErrCommitBroken) {
		t.Fatalf("Commit err = %v, want ErrCommitBroken", err)
	}
	if p, _ := PendingCommit(live); p == nil {
		t.Error("the marker is gone after a failed move")
	}
	noCopyLeft(t, live)
}

func TestCommitRefusesWhileStoreOpen(t *testing.T) {
	live, staging := stagedFixture(t)
	db, err := badger.Open(badger.DefaultOptions(filepath.Join(live, "backend", "badger")).
		WithLoggingLevel(badger.ERROR))
	if err != nil {
		t.Fatalf("open badger: %v", err)
	}
	beforeLive := tree(t, live)
	_, err = Commit(live, staging)
	if !errors.Is(err, ErrStoreInUse) {
		_ = db.Close()
		t.Fatalf("Commit err = %v, want ErrStoreInUse", err)
	}

	if want := fmt.Sprintf("pid %d", os.Getpid()); !strings.Contains(err.Error(), want) {
		t.Errorf("error does not name %s: %v", want, err)
	}
	sameTree(t, "data dir", tree(t, live), beforeLive)
	if err := db.Close(); err != nil {
		t.Fatalf("close badger: %v", err)
	}
	if _, err := Commit(live, staging); err != nil {
		t.Fatalf("Commit after the store closed: %v", err)
	}
}

func TestCommitSkipsEntriesThisBuildDoesNotCarry(t *testing.T) {
	live, staging := stagedFixture(t)
	write(t, filepath.Join(staging, "tor", "torrc"), "STAGED-TORRC")
	write(t, filepath.Join(staging, "newthing", "x"), "X")
	res, err := Commit(live, staging)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := read(t, filepath.Join(live, "tor", "torrc")); got != "LIVE-TORRC" {
		t.Errorf("tor/torrc = %q, want the live one", got)
	}
	if _, err := os.Stat(filepath.Join(live, "newthing")); err == nil {
		t.Error("newthing/ was moved in; this build does not carry it")
	}
	if strings.Join(res.Skipped, ",") != "newthing,tor" {
		t.Errorf("Skipped = %v, want [newthing tor]", res.Skipped)
	}
}

func TestIsCarried(t *testing.T) {
	cases := []struct {
		name  string
		isDir bool
		want  bool
	}{
		{"vault.enc", false, true},
		{"vault.enc.1", false, true},
		{"vault.enc.12", false, true},
		{"vault.enc.lock", false, false},
		{"vault.enc.tmp-123", false, false},
		{"vault.enc.", false, false},
		{"disguise.enc", false, true},
		{"calc-help.txt", false, true},
		{"calc-help.txt.tmp", false, false},
		{"calc-help.txt", true, false},
		{"backend", true, true},
		{"backend", false, false},
		{"frontend", true, true},
		{"textUI", true, true},
		{"tor", true, false},
		{"haomad.log", false, false},
		{"haomad.log.prev", false, false},
		{"vault.lock", false, false},
		{".pre-restore-20261001-120000", true, false},
		{".staging-20261001-120000", true, false},
		{"device-empty", false, false},
	}
	for _, c := range cases {
		if got := IsCarried(c.name, c.isDir); got != c.want {
			t.Errorf("IsCarried(%q, dir=%v) = %v, want %v", c.name, c.isDir, got, c.want)
		}
	}
}

func TestCommitEmptyFileNotRemovedIsNotApplied(t *testing.T) {
	live, staging := stagedFixture(t)
	write(t, filepath.Join(live, EmptyMarkerFile+".tmp", "x"), "X")
	before := read(t, filepath.Join(live, "vault.enc"))
	_, err := Commit(live, staging)
	if err == nil || errors.Is(err, ErrCommitBroken) {
		t.Fatalf("Commit err = %v, want a not-applied error", err)
	}
	noMarker(t, live)
	if got := read(t, filepath.Join(live, "vault.enc")); got != before {
		t.Errorf("vault.enc = %q, want it as it was", got)
	}
}
