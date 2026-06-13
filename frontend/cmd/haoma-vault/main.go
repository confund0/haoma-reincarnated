package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"haoma-frontend/internal/backuparchive"
	"haoma-frontend/internal/disguise"
	"haoma-frontend/internal/paths"
	"haoma-frontend/internal/vault"
)

const (
	vaultFileName = "vault.enc"
	lockFileName  = "vault.enc.lock"

	maxPassphraseSize = 4 * 1024

	maxPayloadSize = 64 * 1024

	maxPatternSize = 256

	disguiseSidecarMissingExit = 2
)

func main() {
	cfgDir := flag.String("cfg-dir", "", "data root anchoring vault.enc; required")
	writeMode := flag.Bool("w", false, "write mode: read passphrase + payload JSON on stdin and re-seal vault.enc")
	listBackups := flag.Bool("list-backups", false, "list existing vault.enc.N backups and exit")
	restoreN := flag.Int("restore", 0, "atomically restore vault.enc from vault.enc.N (1..MaxBackups)")
	disguiseInit := flag.Bool("disguise-init", false, "create disguise.enc sealed under stdin-pattern (refuses to overwrite)")
	disguiseVerify := flag.Bool("disguise-verify", false, "verify disguise.enc decrypts under stdin-pattern (exit 0=match, 1=mismatch, 2=missing)")
	disguiseRekey := flag.Bool("disguise-rekey", false, "rekey disguise.enc: stdin line 1 = old pattern, rest = new pattern")
	archiveWrite := flag.String("archive-write", "", "write a full-cfg-dir tar archive to this path; CALLER must quiesce haomad + haoma first")
	archiveRestore := flag.String("archive-restore", "", "restore a full-cfg-dir tar archive from this path; moves existing cfg-dir contents to <cfg-dir>/.pre-restore-<ts>/")
	archiveStage := flag.String("archive-stage", "", "extract a backup archive into <cfg-dir>/.staging-<ts>/ without touching the live cfg-dir; stdout = staging path")
	archiveCommit := flag.String("archive-commit", "", "verify <staging-path>/vault.enc unseals under stdin passphrase, then atomically swap staged contents into <cfg-dir>")
	archiveDiscard := flag.String("archive-discard", "", "remove <staging-path>; abandons a previously-staged restore")
	flag.Parse()

	exit, err := run(*cfgDir, runFlags{
		writeMode:      *writeMode,
		listBackups:    *listBackups,
		restoreN:       *restoreN,
		disguiseInit:   *disguiseInit,
		disguiseVerify: *disguiseVerify,
		disguiseRekey:  *disguiseRekey,
		archiveWrite:   *archiveWrite,
		archiveRestore: *archiveRestore,
		archiveStage:   *archiveStage,
		archiveCommit:  *archiveCommit,
		archiveDiscard: *archiveDiscard,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "haoma-vault:", err)
	}
	if exit != 0 {
		os.Exit(exit)
	}
}

type runFlags struct {
	writeMode      bool
	listBackups    bool
	restoreN       int
	disguiseInit   bool
	disguiseVerify bool
	disguiseRekey  bool
	archiveWrite   string
	archiveRestore string
	archiveStage   string
	archiveCommit  string
	archiveDiscard string
}

func run(cfgDir string, f runFlags) (int, error) {
	if cfgDir == "" {
		return 1, errors.New("--cfg-dir is required")
	}
	root, err := paths.RootFromFlag(cfgDir)
	if err != nil {
		return 1, fmt.Errorf("resolve cfg-dir: %w", err)
	}
	if _, err := paths.BootstrapAt(root); err != nil {
		return 1, fmt.Errorf("bootstrap %s: %w", root, err)
	}
	vaultPath := filepath.Join(root, vaultFileName)
	lockPath := filepath.Join(root, lockFileName)
	disguisePath := disguise.Path(root)

	modes := 0
	if f.writeMode {
		modes++
	}
	if f.listBackups {
		modes++
	}
	if f.restoreN != 0 {
		modes++
	}
	if f.disguiseInit {
		modes++
	}
	if f.disguiseVerify {
		modes++
	}
	if f.disguiseRekey {
		modes++
	}
	if f.archiveWrite != "" {
		modes++
	}
	if f.archiveRestore != "" {
		modes++
	}
	if f.archiveStage != "" {
		modes++
	}
	if f.archiveCommit != "" {
		modes++
	}
	if f.archiveDiscard != "" {
		modes++
	}
	if modes > 1 {
		return 1, errors.New("-w / --list-backups / --restore / --disguise-* / --archive-* are mutually exclusive")
	}

	switch {
	case f.listBackups:
		return wrap(runList(vaultPath))
	case f.restoreN != 0:
		return wrap(runRestore(vaultPath, lockPath, f.restoreN))
	case f.writeMode:
		return wrap(runWrite(vaultPath, lockPath))
	case f.disguiseInit:
		return wrap(runDisguiseInit(disguisePath))
	case f.disguiseVerify:
		return runDisguiseVerify(disguisePath)
	case f.disguiseRekey:
		return wrap(runDisguiseRekey(disguisePath))
	case f.archiveWrite != "":
		return wrap(runArchiveWrite(root, f.archiveWrite))
	case f.archiveRestore != "":
		return wrap(runArchiveRestore(root, f.archiveRestore))
	case f.archiveStage != "":
		return wrap(runArchiveStage(root, f.archiveStage))
	case f.archiveCommit != "":
		return wrap(runArchiveCommit(root, f.archiveCommit))
	case f.archiveDiscard != "":
		return wrap(runArchiveDiscard(root, f.archiveDiscard))
	default:
		return wrap(runRead(vaultPath))
	}
}

func wrap(err error) (int, error) {
	if err != nil {
		return 1, err
	}
	return 0, nil
}

func runRead(vaultPath string) error {
	pass, err := readPassphrase(os.Stdin)
	if err != nil {
		return err
	}
	if len(pass) == 0 {
		fmt.Fprintln(os.Stderr, "haoma-vault: empty stdin; using InsecureDefaultPassphrase")
		pass = []byte(vault.InsecureDefaultPassphrase)
	}
	payload, err := openOrMint(vaultPath, pass)
	if err != nil {
		return err
	}

	blob, err := payload.Secrets.Marshal()
	if err != nil {
		return fmt.Errorf("marshal secrets: %w", err)
	}
	if _, err := os.Stdout.Write(blob); err != nil {
		return fmt.Errorf("write stdout: %w", err)
	}
	if _, err := os.Stdout.Write([]byte("\n")); err != nil {
		return fmt.Errorf("write stdout: %w", err)
	}

	full, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	if _, err := os.Stdout.Write(full); err != nil {
		return fmt.Errorf("write stdout: %w", err)
	}
	if _, err := os.Stdout.Write([]byte("\n")); err != nil {
		return fmt.Errorf("write stdout: %w", err)
	}
	return nil
}

func runWrite(vaultPath, lockPath string) error {
	pass, payloadJSON, err := readWriteStdin(os.Stdin)
	if err != nil {
		return err
	}
	if len(pass) == 0 {
		return errors.New("write mode requires a non-empty passphrase on stdin line 1")
	}
	fmt.Fprintf(os.Stderr,
		"haoma-vault: -w enter pass_len=%d payload_bytes=%d\n",
		len(pass), len(payloadJSON),
	)

	dec := json.NewDecoder(bytes.NewReader(payloadJSON))
	dec.DisallowUnknownFields()
	var p vault.Payload
	if err := dec.Decode(&p); err != nil {
		return fmt.Errorf("decode payload: %w", err)
	}
	if dec.More() {
		return errors.New("payload has trailing content after JSON object")
	}
	if err := p.Validate(); err != nil {
		return fmt.Errorf("validate: %w", err)
	}

	lockFd, err := acquireFlock(lockPath)
	if err != nil {
		return err
	}
	defer releaseFlock(lockFd)

	params, hadVault, err := peekParams(vaultPath)
	if err != nil {
		return err
	}
	if !hadVault {
		params = vault.DefaultKDFParams
		fmt.Fprintln(os.Stderr, "haoma-vault: no existing vault.enc — sealing fresh with DefaultKDFParams")
	}

	if err := vault.RotateBeforeWrite(vaultPath); err != nil {
		return err
	}

	start := time.Now()
	if err := vault.Save(vaultPath, pass, p, params); err != nil {
		return fmt.Errorf("seal: %w", err)
	}
	fmt.Fprintf(os.Stderr,
		"haoma-vault: wrote %s (kdf t=%d mem=%dKiB par=%d, %.0fms)\n",
		vaultPath, params.Time, params.Memory, params.Threads,
		time.Since(start).Seconds()*1000,
	)
	return nil
}

func runList(vaultPath string) error {
	infos, err := vault.ListBackups(vaultPath)
	if err != nil {
		return err
	}
	if _, err := os.Stdout.Write([]byte(vault.FormatBackups(infos))); err != nil {
		return fmt.Errorf("write stdout: %w", err)
	}
	return nil
}

func runRestore(vaultPath, lockPath string, n int) error {
	if n < 1 || n > vault.MaxBackups {
		return fmt.Errorf("--restore=%d out of range [1, %d]", n, vault.MaxBackups)
	}
	lockFd, err := acquireFlock(lockPath)
	if err != nil {
		return err
	}
	defer releaseFlock(lockFd)

	if err := vault.RestoreFromBackup(vaultPath, n); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "haoma-vault: restored %s from .%d\n", vaultPath, n)
	return nil
}

func runDisguiseInit(disguisePath string) error {
	pat, err := readPatternLine(os.Stdin)
	if err != nil {
		return err
	}
	if pat == "" {
		return errors.New("disguise-init: empty pattern on stdin")
	}
	if err := disguise.Init(disguisePath, pat); err != nil {
		return fmt.Errorf("disguise-init: %w", err)
	}
	fmt.Fprintf(os.Stderr, "haoma-vault: minted %s\n", disguisePath)
	return nil
}

func runDisguiseVerify(disguisePath string) (int, error) {
	pat, err := readPatternLine(os.Stdin)
	if err != nil {
		return 1, err
	}
	if pat == "" {
		return 1, errors.New("disguise-verify: empty pattern on stdin")
	}
	switch err := disguise.Verify(disguisePath, pat); {
	case err == nil:
		return 0, nil
	case errors.Is(err, disguise.ErrEmpty):
		return disguiseSidecarMissingExit, nil
	case errors.Is(err, disguise.ErrPatternMismatch),
		errors.Is(err, disguise.ErrBadMagic),
		errors.Is(err, disguise.ErrUnsupportedVersion),
		errors.Is(err, disguise.ErrTruncated):
		return 1, fmt.Errorf("disguise-verify: %w", err)
	default:

		if os.IsNotExist(errors.Unwrap(err)) || os.IsNotExist(err) {
			return disguiseSidecarMissingExit, nil
		}
		return 1, fmt.Errorf("disguise-verify: %w", err)
	}
}

func runDisguiseRekey(disguisePath string) error {
	oldPat, newPat, err := readRekeyStdin(os.Stdin)
	if err != nil {
		return err
	}
	if oldPat == "" || newPat == "" {
		return errors.New("disguise-rekey: stdin must be old\\nnew (both non-empty)")
	}
	if err := disguise.Rekey(disguisePath, oldPat, newPat); err != nil {
		return fmt.Errorf("disguise-rekey: %w", err)
	}
	fmt.Fprintf(os.Stderr, "haoma-vault: rekeyed %s\n", disguisePath)
	return nil
}

func readPatternLine(r io.Reader) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxPatternSize+1))
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	if len(raw) > maxPatternSize {
		return "", fmt.Errorf("stdin exceeds %d bytes", maxPatternSize)
	}
	return strings.TrimRight(string(raw), "\r\n\t "), nil
}

func readRekeyStdin(r io.Reader) (oldPat, newPat string, err error) {
	br := bufio.NewReader(io.LimitReader(r, 2*maxPatternSize+2))
	first, err := br.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", "", fmt.Errorf("read old pattern: %w", err)
	}
	if len(first) > maxPatternSize {
		return "", "", fmt.Errorf("old pattern line exceeds %d bytes", maxPatternSize)
	}
	oldPat = strings.TrimRight(first, "\r\n")
	rest, err := io.ReadAll(io.LimitReader(br, maxPatternSize+1))
	if err != nil {
		return "", "", fmt.Errorf("read new pattern: %w", err)
	}
	if len(rest) > maxPatternSize {
		return "", "", fmt.Errorf("new pattern exceeds %d bytes", maxPatternSize)
	}
	newPat = strings.TrimRight(string(rest), "\r\n\t ")
	return oldPat, newPat, nil
}

func readPassphrase(r io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxPassphraseSize+1))
	if err != nil {
		return nil, fmt.Errorf("read stdin: %w", err)
	}
	if len(raw) > maxPassphraseSize {
		return nil, fmt.Errorf("stdin exceeds %d bytes", maxPassphraseSize)
	}
	return bytes.TrimRight(raw, "\r\n\t "), nil
}

func readWriteStdin(r io.Reader) (passphrase []byte, payload []byte, err error) {
	br := bufio.NewReader(io.LimitReader(r, maxPassphraseSize+maxPayloadSize+2))
	passLine, err := br.ReadBytes('\n')
	if err != nil && err != io.EOF {
		return nil, nil, fmt.Errorf("read passphrase: %w", err)
	}
	if len(passLine) > maxPassphraseSize {
		return nil, nil, fmt.Errorf("passphrase line exceeds %d bytes", maxPassphraseSize)
	}
	passphrase = bytes.TrimRight(passLine, "\r\n")
	rest, err := io.ReadAll(io.LimitReader(br, maxPayloadSize+1))
	if err != nil {
		return nil, nil, fmt.Errorf("read payload: %w", err)
	}
	if len(rest) > maxPayloadSize {
		return nil, nil, fmt.Errorf("payload exceeds %d bytes", maxPayloadSize)
	}
	return passphrase, rest, nil
}

func peekParams(path string) (vault.KDFParams, bool, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return vault.KDFParams{}, false, nil
	} else if err != nil {
		return vault.KDFParams{}, false, fmt.Errorf("stat %s: %w", path, err)
	}
	params, err := vault.PeekParams(path)
	if err != nil {
		return vault.KDFParams{}, false, fmt.Errorf("peek existing: %w", err)
	}
	return params, true, nil
}

func acquireFlock(lockPath string) (int, error) {
	fd, err := unix.Open(lockPath, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return -1, fmt.Errorf("open lock %s: %w", lockPath, err)
	}
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("flock %s: %w", lockPath, err)
	}
	return fd, nil
}

func releaseFlock(lockFd int) {
	if err := unix.Flock(lockFd, unix.LOCK_UN); err != nil {
		fmt.Fprintln(os.Stderr, "haoma-vault: flock unlock:", err)
	}
	if err := unix.Close(lockFd); err != nil {
		fmt.Fprintln(os.Stderr, "haoma-vault: close lock fd:", err)
	}
}

func runArchiveWrite(cfgDir, destPath string) error {
	if destPath == "" {
		return errors.New("--archive-write requires a destination path")
	}
	abs, err := filepath.Abs(destPath)
	if err != nil {
		return fmt.Errorf("resolve dest: %w", err)
	}
	files, byteCount, err := backuparchive.Create(cfgDir, abs)
	if err != nil {
		return fmt.Errorf("archive write: %w", err)
	}
	fmt.Fprintf(os.Stderr, "haoma-vault: archive-write ok dest=%s files=%d bytes=%d\n",
		abs, files, byteCount)
	return nil
}

func runArchiveRestore(cfgDir, srcPath string) error {
	if srcPath == "" {
		return errors.New("--archive-restore requires a source path")
	}
	srcAbs, err := filepath.Abs(srcPath)
	if err != nil {
		return fmt.Errorf("resolve src: %w", err)
	}
	if _, err := os.Stat(srcAbs); err != nil {
		return fmt.Errorf("source archive: %w", err)
	}
	preRestoreDir, err := moveAsideExisting(cfgDir)
	if err != nil {
		return fmt.Errorf("move-aside existing cfg-dir: %w", err)
	}
	if preRestoreDir != "" {
		fmt.Fprintf(os.Stderr, "haoma-vault: archive-restore moved existing state to %s\n", preRestoreDir)
	}
	files, byteCount, err := backuparchive.Extract(srcAbs, cfgDir)
	if err != nil {
		return fmt.Errorf("archive extract: %w", err)
	}
	fmt.Fprintf(os.Stderr, "haoma-vault: archive-restore ok src=%s files=%d bytes=%d\n",
		srcAbs, files, byteCount)
	return nil
}

func moveAsideExisting(cfgDir string) (string, error) {
	entries, err := os.ReadDir(cfgDir)
	if err != nil {
		return "", fmt.Errorf("read cfg-dir: %w", err)
	}
	var movable []string
	for _, d := range entries {
		name := d.Name()
		switch {
		case name == "vault.enc" || strings.HasPrefix(name, "vault.enc."):
			movable = append(movable, name)
		case name == "disguise.enc":
			movable = append(movable, name)
		case d.IsDir() && (name == "backend" || name == "frontend" || name == "textUI"):
			movable = append(movable, name)
		}
	}
	if len(movable) == 0 {
		return "", nil
	}
	preName := ".pre-restore-" + time.Now().UTC().Format("20060102-150405")
	preDir := filepath.Join(cfgDir, preName)

	for i := 1; ; i++ {
		if _, err := os.Stat(preDir); errors.Is(err, os.ErrNotExist) {
			break
		}
		preDir = filepath.Join(cfgDir, fmt.Sprintf("%s-%d", preName, i))
	}
	if err := os.MkdirAll(preDir, 0o700); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", preDir, err)
	}
	for _, name := range movable {
		src := filepath.Join(cfgDir, name)
		dst := filepath.Join(preDir, name)
		if err := os.Rename(src, dst); err != nil {
			return "", fmt.Errorf("move %s: %w", name, err)
		}
	}
	return preDir, nil
}

func runArchiveStage(cfgDir, archivePath string) error {
	if archivePath == "" {
		return errors.New("--archive-stage requires an archive path")
	}
	srcAbs, err := filepath.Abs(archivePath)
	if err != nil {
		return fmt.Errorf("resolve src: %w", err)
	}
	if _, err := os.Stat(srcAbs); err != nil {
		return fmt.Errorf("source archive: %w", err)
	}
	if err := cleanStaleStagingDirs(cfgDir); err != nil {
		return fmt.Errorf("clean stale staging: %w", err)
	}
	ts := time.Now().UTC().Format("20060102-150405")
	stagingDir := filepath.Join(cfgDir, ".staging-"+ts)

	for i := 1; ; i++ {
		if _, err := os.Stat(stagingDir); errors.Is(err, os.ErrNotExist) {
			break
		}
		stagingDir = filepath.Join(cfgDir, fmt.Sprintf(".staging-%s-%d", ts, i))
	}
	files, byteCount, err := backuparchive.Extract(srcAbs, stagingDir)
	if err != nil {

		_ = os.RemoveAll(stagingDir)
		return fmt.Errorf("archive stage: %w", err)
	}
	fmt.Fprintf(os.Stderr, "haoma-vault: archive-stage ok staging=%s files=%d bytes=%d\n",
		stagingDir, files, byteCount)
	if _, err := fmt.Fprintln(os.Stdout, stagingDir); err != nil {
		return fmt.Errorf("write stdout: %w", err)
	}
	return nil
}

func runArchiveCommit(cfgDir, stagingPath string) error {
	if stagingPath == "" {
		return errors.New("--archive-commit requires a staging path")
	}
	stagingAbs, err := validateStagingPath(cfgDir, stagingPath)
	if err != nil {
		return err
	}
	pass, err := readPassphrase(os.Stdin)
	if err != nil {
		return err
	}
	if len(pass) == 0 {
		return errors.New("archive-commit requires a non-empty passphrase on stdin")
	}
	stagedVault := filepath.Join(stagingAbs, vaultFileName)
	if _, err := os.Stat(stagedVault); err != nil {
		return fmt.Errorf("staged vault.enc: %w", err)
	}

	payload, _, err := vault.Open(stagedVault, pass)
	if err != nil {
		return fmt.Errorf("open staged vault: %w", err)
	}
	preDir, err := moveAsideExisting(cfgDir)
	if err != nil {
		return fmt.Errorf("move-aside existing cfg-dir: %w", err)
	}
	if preDir != "" {
		fmt.Fprintf(os.Stderr, "haoma-vault: archive-commit moved existing state to %s\n", preDir)
	}
	moved, err := moveStagedToCfgDir(stagingAbs, cfgDir)
	if err != nil {
		return fmt.Errorf("move staged: %w", err)
	}

	if err := os.Remove(stagingAbs); err != nil {
		fmt.Fprintf(os.Stderr, "haoma-vault: archive-commit staging dir cleanup: %v\n", err)
	}
	fmt.Fprintf(os.Stderr, "haoma-vault: archive-commit ok staging=%s moved=%d\n",
		stagingAbs, moved)

	blob, err := payload.Secrets.Marshal()
	if err != nil {
		return fmt.Errorf("marshal secrets: %w", err)
	}
	if _, err := os.Stdout.Write(blob); err != nil {
		return fmt.Errorf("write stdout: %w", err)
	}
	if _, err := os.Stdout.Write([]byte("\n")); err != nil {
		return fmt.Errorf("write stdout: %w", err)
	}
	full, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	if _, err := os.Stdout.Write(full); err != nil {
		return fmt.Errorf("write stdout: %w", err)
	}
	if _, err := os.Stdout.Write([]byte("\n")); err != nil {
		return fmt.Errorf("write stdout: %w", err)
	}
	return nil
}

func runArchiveDiscard(cfgDir, stagingPath string) error {
	if stagingPath == "" {
		return errors.New("--archive-discard requires a staging path")
	}
	stagingAbs, err := validateStagingPath(cfgDir, stagingPath)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(stagingAbs); err != nil {
		return fmt.Errorf("discard staging: %w", err)
	}
	fmt.Fprintf(os.Stderr, "haoma-vault: archive-discard ok staging=%s\n", stagingAbs)
	return nil
}

func cleanStaleStagingDirs(cfgDir string) error {
	entries, err := os.ReadDir(cfgDir)
	if err != nil {
		return fmt.Errorf("read cfg-dir: %w", err)
	}
	for _, d := range entries {
		if !d.IsDir() {
			continue
		}
		name := d.Name()
		if !strings.HasPrefix(name, ".staging-") {
			continue
		}
		full := filepath.Join(cfgDir, name)
		if err := os.RemoveAll(full); err != nil {
			return fmt.Errorf("remove stale %s: %w", name, err)
		}
		fmt.Fprintf(os.Stderr, "haoma-vault: archive-stage cleaned stale %s\n", full)
	}
	return nil
}

func validateStagingPath(cfgDir, stagingPath string) (string, error) {
	abs, err := filepath.Abs(stagingPath)
	if err != nil {
		return "", fmt.Errorf("resolve staging: %w", err)
	}
	cfgAbs, err := filepath.Abs(cfgDir)
	if err != nil {
		return "", fmt.Errorf("resolve cfg-dir: %w", err)
	}
	rel, err := filepath.Rel(cfgAbs, abs)
	if err != nil {
		return "", fmt.Errorf("rel staging: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("staging path %s is not under cfg-dir %s", abs, cfgAbs)
	}
	if !strings.HasPrefix(filepath.Base(abs), ".staging-") {
		return "", fmt.Errorf("staging path basename %q does not begin with .staging-",
			filepath.Base(abs))
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("stat staging: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("staging %s is not a directory", abs)
	}
	return abs, nil
}

func moveStagedToCfgDir(stagingDir, cfgDir string) (int, error) {
	entries, err := os.ReadDir(stagingDir)
	if err != nil {
		return 0, fmt.Errorf("read staging: %w", err)
	}
	var rest []string
	haveVault := false
	for _, d := range entries {
		if d.Name() == vaultFileName {
			haveVault = true
			continue
		}
		rest = append(rest, d.Name())
	}
	sort.Strings(rest)
	moved := 0
	for _, name := range rest {
		src := filepath.Join(stagingDir, name)
		dst := filepath.Join(cfgDir, name)
		if err := os.Rename(src, dst); err != nil {
			return moved, fmt.Errorf("move %s: %w", name, err)
		}
		moved++
	}
	if haveVault {
		if err := os.Rename(
			filepath.Join(stagingDir, vaultFileName),
			filepath.Join(cfgDir, vaultFileName),
		); err != nil {
			return moved, fmt.Errorf("move vault.enc: %w", err)
		}
		moved++
	}
	return moved, nil
}

func openOrMint(vaultPath string, passphrase []byte) (vault.Payload, error) {
	_, statErr := os.Stat(vaultPath)
	switch {
	case statErr == nil:
		payload, _, err := vault.Open(vaultPath, passphrase)
		if err != nil {
			return vault.Payload{}, fmt.Errorf("open %s: %w", vaultPath, err)
		}
		return payload, nil
	case os.IsNotExist(statErr):
		fmt.Fprintln(os.Stderr, "haoma-vault: minting fresh vault at", vaultPath)
		payload, err := vault.MintFreshPayload()
		if err != nil {
			return vault.Payload{}, fmt.Errorf("mint payload: %w", err)
		}
		if err := vault.Create(vaultPath, passphrase, payload, vault.DefaultKDFParams); err != nil {
			return vault.Payload{}, fmt.Errorf("create %s: %w", vaultPath, err)
		}
		return payload, nil
	default:
		return vault.Payload{}, fmt.Errorf("stat %s: %w", vaultPath, statErr)
	}
}
