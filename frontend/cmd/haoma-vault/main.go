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
	"strings"
	"time"

	"haoma-frontend/internal/backuparchive"
	"haoma-frontend/internal/disguise"
	"haoma-frontend/internal/lockfile"
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

	commitNotAppliedExit = 3

	commitBrokenExit = 4

	commitStoreInUseExit = 5

	commitNoBundleExit = 6

	restorePendingExit = 7

	notEmptyExit = 8
)

var errRestorePending = errors.New("refusing to open or mint a vault")

func main() {
	cfgDir := flag.String("cfg-dir", "", "data root anchoring vault.enc; required")
	writeMode := flag.Bool("w", false, "write mode: read passphrase + payload JSON on stdin and re-seal vault.enc")
	listBackups := flag.Bool("list-backups", false, "list existing vault.enc.N backups and exit")
	restoreN := flag.Int("restore", 0, "atomically restore vault.enc from vault.enc.N (1..MaxBackups)")
	disguiseInit := flag.Bool("disguise-init", false, "create disguise.enc sealed under stdin-pattern (refuses to overwrite)")
	disguiseVerify := flag.Bool("disguise-verify", false, "verify disguise.enc decrypts under stdin-pattern (exit 0=match, 1=mismatch, 2=missing)")
	disguiseRekey := flag.Bool("disguise-rekey", false, "rekey disguise.enc: stdin line 1 = old pattern, rest = new pattern")
	archiveWrite := flag.String("archive-write", "", "write a full-cfg-dir tar archive to this path; CALLER must quiesce haomad + haoma first")
	archiveStage := flag.String("archive-stage", "", "extract a backup archive into <cfg-dir>/.staging-<ts>/ without touching the live cfg-dir; stdout = staging path")
	archiveCommit := flag.String("archive-commit", "", "verify <staging-path>/vault.enc unseals under stdin passphrase, then atomically swap staged contents into <cfg-dir>")
	archiveDiscard := flag.String("archive-discard", "", "remove <staging-path>; abandons a previously-staged restore")
	checkEmpty := flag.Bool("check-empty", false, "exit 0 when a restore may run onto this device, 8 when it is not empty")
	cleanLeftovers := flag.Bool("clean-restore-leftovers", false, "delete the .staging-* dirs of unfinished staged restores and the .pre-restore-* dirs of earlier builds")
	flag.Parse()

	exit, err := run(*cfgDir, runFlags{
		writeMode:      *writeMode,
		listBackups:    *listBackups,
		restoreN:       *restoreN,
		disguiseInit:   *disguiseInit,
		disguiseVerify: *disguiseVerify,
		disguiseRekey:  *disguiseRekey,
		archiveWrite:   *archiveWrite,
		archiveStage:   *archiveStage,
		archiveCommit:  *archiveCommit,
		archiveDiscard: *archiveDiscard,
		checkEmpty:     *checkEmpty,
		cleanLeftovers: *cleanLeftovers,
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
	archiveStage   string
	archiveCommit  string
	archiveDiscard string
	checkEmpty     bool
	cleanLeftovers bool
}

func run(cfgDir string, f runFlags) (int, error) {

	early := 1
	if f.archiveCommit != "" {
		early = commitNotAppliedExit
	}
	if cfgDir == "" {
		return early, errors.New("--cfg-dir is required")
	}
	root, err := paths.RootFromFlag(cfgDir)
	if err != nil {
		return early, fmt.Errorf("resolve cfg-dir: %w", err)
	}
	if _, err := paths.BootstrapAt(root); err != nil {
		return early, fmt.Errorf("bootstrap %s: %w", root, err)
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
	if f.archiveStage != "" {
		modes++
	}
	if f.archiveCommit != "" {
		modes++
	}
	if f.archiveDiscard != "" {
		modes++
	}
	if f.checkEmpty {
		modes++
	}
	if f.cleanLeftovers {
		modes++
	}
	if modes > 1 {
		return early, errors.New("-w / --list-backups / --restore / --disguise-* / --archive-* are mutually exclusive")
	}

	if !f.listBackups && !f.disguiseInit && !f.disguiseVerify && !f.disguiseRekey {
		pending, err := backuparchive.PendingCommit(root)
		if err != nil {
			return restorePendingExit, err
		}
		if pending != nil {
			return restorePendingExit, errors.New(pending.Message(root))
		}
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
	case f.archiveStage != "":

		lk, err := acquireVaultLock(lockPath)
		if err != nil {
			return 1, err
		}
		defer releaseVaultLock(lk)
		if exit, err := checkDeviceEmpty(root); exit != 0 {
			return exit, err
		}
		return wrap(runArchiveStage(root, f.archiveStage))
	case f.archiveCommit != "":
		return runArchiveCommit(root, lockPath, f.archiveCommit)
	case f.archiveDiscard != "":
		return wrap(runArchiveDiscard(root, f.archiveDiscard))
	case f.checkEmpty:
		return checkDeviceEmpty(root)
	case f.cleanLeftovers:
		return runCleanRestoreLeftovers(root, lockPath)
	default:
		err := runRead(vaultPath)
		if errors.Is(err, errRestorePending) {
			return restorePendingExit, err
		}
		return wrap(err)
	}
}

func checkDeviceEmpty(root string) (int, error) {
	empty, err := backuparchive.DeviceIsEmpty(root)
	if err != nil {
		return 1, fmt.Errorf("check the device is empty: %w", err)
	}
	if !empty {
		return notEmptyExit, fmt.Errorf("%w: %s", backuparchive.ErrDeviceNotEmpty, backuparchive.NotEmptyMessage)
	}
	return 0, nil
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
		"haoma-vault: -w enter payload_bytes=%d\n",
		len(payloadJSON),
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

	lk, err := acquireVaultLock(lockPath)
	if err != nil {
		return err
	}
	defer releaseVaultLock(lk)

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
	lk, err := acquireVaultLock(lockPath)
	if err != nil {
		return err
	}
	defer releaseVaultLock(lk)

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

func readCommitPassphrases(r io.Reader) ([][]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxPassphraseSize+1))
	if err != nil {
		return nil, fmt.Errorf("read stdin: %w", err)
	}
	if len(raw) > maxPassphraseSize {
		return nil, fmt.Errorf("stdin exceeds %d bytes", maxPassphraseSize)
	}
	lineCut := bytes.TrimRight(raw, "\r\n")
	allCut := bytes.TrimRight(raw, "\r\n\t ")
	if len(allCut) == 0 {
		return nil, errors.New("archive-commit requires a non-empty passphrase on stdin")
	}
	if bytes.Equal(lineCut, allCut) {
		return [][]byte{lineCut}, nil
	}
	return [][]byte{lineCut, allCut}, nil
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

func acquireVaultLock(lockPath string) (*lockfile.Lock, error) {
	lk, err := lockfile.AcquireBlocking(lockPath)
	if err != nil {
		return nil, fmt.Errorf("acquire vault lock: %w", err)
	}
	return lk, nil
}

func releaseVaultLock(lk *lockfile.Lock) {
	if err := lk.Release(); err != nil {
		fmt.Fprintln(os.Stderr, "haoma-vault: release lock:", err)
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

func runArchiveCommit(cfgDir, lockPath, stagingPath string) (int, error) {
	if stagingPath == "" {
		return commitNotAppliedExit, errors.New("--archive-commit requires a staging path")
	}
	stagingAbs, err := validateStagingPath(cfgDir, stagingPath)
	if err != nil {
		return commitNotAppliedExit, err
	}
	candidates, err := readCommitPassphrases(os.Stdin)
	if err != nil {
		return commitNotAppliedExit, err
	}
	stagedVault := filepath.Join(stagingAbs, vaultFileName)
	if _, err := os.Stat(stagedVault); err != nil {
		return commitNotAppliedExit, fmt.Errorf("staged vault.enc: %w", err)
	}

	var payload vault.Payload
	for _, pass := range candidates {
		payload, _, err = vault.Open(stagedVault, pass)
		if !errors.Is(err, vault.ErrUnseal) {
			break
		}
	}
	if errors.Is(err, vault.ErrUnseal) {
		return 1, fmt.Errorf("open staged vault: %w", err)
	}
	if err != nil {
		return commitNotAppliedExit, fmt.Errorf("open staged vault: %w", err)
	}

	blob, err := payload.Secrets.Marshal()
	if err != nil {
		return commitNotAppliedExit, fmt.Errorf("marshal secrets: %w", err)
	}
	full, err := json.Marshal(payload)
	if err != nil {
		return commitNotAppliedExit, fmt.Errorf("marshal payload: %w", err)
	}

	lk, err := acquireVaultLock(lockPath)
	if err != nil {
		return commitNotAppliedExit, err
	}
	res, err := backuparchive.Commit(cfgDir, stagingAbs)
	releaseVaultLock(lk)
	switch {
	case errors.Is(err, backuparchive.ErrCommitBroken):
		return commitBrokenExit, fmt.Errorf("archive-commit: %w", err)
	case errors.Is(err, backuparchive.ErrDeviceNotEmpty):
		return notEmptyExit, fmt.Errorf("%w: %s", backuparchive.ErrDeviceNotEmpty, backuparchive.NotEmptyMessage)
	case errors.Is(err, backuparchive.ErrStoreInUse):
		return commitStoreInUseExit, fmt.Errorf("archive-commit: haomad or haoma is still running; stop it first: %w", err)
	case errors.Is(err, backuparchive.ErrCommitPending):
		return restorePendingExit, fmt.Errorf("archive-commit: %w", err)
	case err != nil:
		return commitNotAppliedExit, fmt.Errorf("archive-commit: %w", err)
	}
	for _, name := range res.KeptLive {
		fmt.Fprintf(os.Stderr, "haoma-vault: archive-commit kept the live %s: the backup has none\n", name)
	}
	if len(res.Skipped) > 0 {
		fmt.Fprintf(os.Stderr, "haoma-vault: archive-commit left out entries this build does not carry: %s\n",
			strings.Join(res.Skipped, " "))
	}

	if err := os.RemoveAll(stagingAbs); err != nil {
		fmt.Fprintf(os.Stderr, "haoma-vault: archive-commit staging dir cleanup: %v\n", err)
	}
	fmt.Fprintf(os.Stderr, "haoma-vault: archive-commit ok staging=%s moved=%d\n",
		stagingAbs, res.Moved)

	for _, line := range [][]byte{blob, full} {
		if _, err := os.Stdout.Write(append(line, '\n')); err != nil {
			return commitNoBundleExit, fmt.Errorf("archive-commit swapped, but stdout failed: %w", err)
		}
	}
	return 0, nil
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

func runCleanRestoreLeftovers(cfgDir, lockPath string) (int, error) {
	lk, err := acquireVaultLock(lockPath)
	if err != nil {
		return 1, err
	}
	gone, err := backuparchive.CleanRestoreLeftovers(cfgDir)
	releaseVaultLock(lk)
	for _, d := range gone {
		fmt.Fprintf(os.Stderr, "haoma-vault: clean-restore-leftovers deleted %s bytes=%d\n", d.Name, d.Bytes)
	}
	switch {
	case errors.Is(err, backuparchive.ErrCommitPending):
		return restorePendingExit, fmt.Errorf("clean-restore-leftovers: %w", err)
	case err != nil:
		return 1, fmt.Errorf("clean-restore-leftovers: %w", err)
	}
	return 0, nil
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

func openOrMint(vaultPath string, passphrase []byte) (vault.Payload, error) {
	root := filepath.Dir(vaultPath)
	if pending, err := backuparchive.PendingCommit(root); err != nil {
		return vault.Payload{}, err
	} else if pending != nil {
		return vault.Payload{}, fmt.Errorf("%w: %s", errRestorePending, pending.Message(root))
	}
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
