package backuparchive

import (
	"archive/tar"
	stdbytes "bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func makeCfg(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mk := func(rel string, body []byte) {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(full, body, 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	mk("vault.enc", []byte("VAULT-CIPHERTEXT"))
	mk("vault.enc.1", []byte("OLDER"))
	mk("vault.enc.2", []byte("OLDEST"))
	mk("vault.lock", []byte("flock-sentinel"))
	mk("disguise.enc", []byte("DISGUISE"))
	mk("haoma-text.log", []byte("a log line\n"))
	mk("haomad.runtime.json", []byte(`{"pid":1}`))

	mk("backend/MANIFEST", []byte("manifest"))
	mk("backend/000001.sst", []byte("sst-data"))
	mk("backend/cert.pem", []byte("CERT"))
	mk("backend/cert.key", []byte("KEY"))
	mk("backend/haomad.log", []byte("ignored"))
	mk("backend/haomad.pid", []byte("12345"))
	mk("backend/api.sock", []byte("socket-bytes"))

	mk("frontend/MANIFEST", []byte("frontend-manifest"))
	mk("frontend/haoma-token", []byte("token-bytes"))
	mk("frontend/haoma.log", []byte("ignored"))

	mk("textUI/last-invite.json", []byte(`{"v":1}`))

	mk("scratch/random.tmp", []byte("nope"))
	return root
}

func TestCreateAndExtractRoundTrip(t *testing.T) {
	src := makeCfg(t)
	archive := filepath.Join(t.TempDir(), "out.tar")
	files, bytes, err := Create(src, archive)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if files == 0 || bytes == 0 {
		t.Fatalf("Create returned %d files / %d bytes; want > 0", files, bytes)
	}

	dest := filepath.Join(t.TempDir(), "restored")
	if _, _, err := Extract(archive, dest); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	expected := []string{
		"vault.enc", "vault.enc.1", "vault.enc.2",
		"disguise.enc",
		"backend/MANIFEST", "backend/000001.sst",
		"backend/cert.pem", "backend/cert.key",
		"frontend/MANIFEST", "frontend/haoma-token",
		"textUI/last-invite.json",
	}
	for _, rel := range expected {
		full := filepath.Join(dest, rel)
		if _, err := os.Stat(full); err != nil {
			t.Errorf("expected %s in archive: %v", rel, err)
		}
	}

	excluded := []string{
		"vault.lock",
		"haoma-text.log",
		"haomad.runtime.json",
		"backend/haomad.log",
		"backend/haomad.pid",
		"backend/api.sock",
		"frontend/haoma.log",
		"scratch/random.tmp",
	}
	for _, rel := range excluded {
		full := filepath.Join(dest, rel)
		if _, err := os.Stat(full); err == nil {
			t.Errorf("unexpected %s in archive (should be excluded)", rel)
		}
	}
}

func TestExtractRefusesPopulatedDest(t *testing.T) {
	src := makeCfg(t)
	archive := filepath.Join(t.TempDir(), "out.tar")
	if _, _, err := Create(src, archive); err != nil {
		t.Fatalf("Create: %v", err)
	}
	dest := t.TempDir()
	if err := os.WriteFile(filepath.Join(dest, "existing"), []byte("x"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, _, err := Extract(archive, dest); err == nil {
		t.Errorf("expected error when extracting into a non-empty dir")
	}
}

func TestExtractTolerates_OperationalArtifacts(t *testing.T) {

	src := makeCfg(t)
	archive := filepath.Join(t.TempDir(), "out.tar")
	if _, _, err := Create(src, archive); err != nil {
		t.Fatalf("Create: %v", err)
	}
	dest := t.TempDir()
	for _, name := range []string{"haoma-text.log", "vault.lock", "haomad.runtime.json"} {
		if err := os.WriteFile(filepath.Join(dest, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	if _, _, err := Extract(archive, dest); err != nil {
		t.Errorf("Extract should tolerate operational artifacts: %v", err)
	}

	for _, name := range []string{"haoma-text.log", "vault.lock"} {
		if _, err := os.Stat(filepath.Join(dest, name)); err != nil {
			t.Errorf("expected %s preserved through extract: %v", name, err)
		}
	}
}

func TestExtractTolerates_PreRestoreSidecar(t *testing.T) {
	src := makeCfg(t)
	archive := filepath.Join(t.TempDir(), "out.tar")
	if _, _, err := Create(src, archive); err != nil {
		t.Fatalf("Create: %v", err)
	}
	dest := t.TempDir()

	sidecar := filepath.Join(dest, ".pre-restore-20260611-150405")
	if err := os.MkdirAll(sidecar, 0o700); err != nil {
		t.Fatalf("seed sidecar: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sidecar, "vault.enc"), []byte("OLD"), 0o600); err != nil {
		t.Fatalf("seed sidecar vault: %v", err)
	}
	if _, _, err := Extract(archive, dest); err != nil {
		t.Errorf("Extract should tolerate .pre-restore-* sidecar: %v", err)
	}
}

func TestDefaultFileNameTimestamp(t *testing.T) {
	got := DefaultFileName(time.Date(2026, 6, 11, 13, 4, 5, 0, time.UTC))
	want := "haoma-backup-20260611-130405.tar.zst"
	if got != want {
		t.Errorf("DefaultFileName: got %q want %q", got, want)
	}
}

func TestCreateEmitsZstdMagic(t *testing.T) {
	src := makeCfg(t)
	archive := filepath.Join(t.TempDir(), "out.tar.zst")
	if _, _, err := Create(src, archive); err != nil {
		t.Fatalf("Create: %v", err)
	}
	head, err := os.ReadFile(archive)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	if len(head) < 4 || !stdbytes.Equal(head[:4], zstdMagic) {
		t.Errorf("archive does not begin with zstd magic: %x", head[:min(4, len(head))])
	}
}

func TestExtractAcceptsLegacyPlainTar(t *testing.T) {
	src := makeCfg(t)
	archive := filepath.Join(t.TempDir(), "legacy.tar")

	f, err := os.OpenFile(archive, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("open legacy archive: %v", err)
	}
	tw := tar.NewWriter(f)
	for _, rel := range []string{"vault.enc", "disguise.enc", "backend/MANIFEST"} {
		full := filepath.Join(src, rel)
		info, err := os.Stat(full)
		if err != nil {
			t.Fatalf("stat seed %s: %v", rel, err)
		}
		hdr := &tar.Header{Name: rel, Mode: 0o600, Size: info.Size(), Typeflag: tar.TypeReg, ModTime: info.ModTime()}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write hdr %s: %v", rel, err)
		}
		body, err := os.ReadFile(full)
		if err != nil {
			t.Fatalf("read seed %s: %v", rel, err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatalf("write body %s: %v", rel, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "restored")
	if _, _, err := Extract(archive, dest); err != nil {
		t.Fatalf("Extract legacy plain tar: %v", err)
	}
	for _, rel := range []string{"vault.enc", "disguise.enc", "backend/MANIFEST"} {
		if _, err := os.Stat(filepath.Join(dest, rel)); err != nil {
			t.Errorf("legacy extract missing %s: %v", rel, err)
		}
	}
}

func TestCreateMissingCfgDir(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "out.tar")
	if _, _, err := Create(filepath.Join(t.TempDir(), "nope"), dest); err == nil {
		t.Errorf("expected error for missing cfg-dir")
	}
}
