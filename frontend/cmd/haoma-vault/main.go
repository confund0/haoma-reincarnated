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

	"golang.org/x/sys/unix"

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
	flag.Parse()

	exit, err := run(*cfgDir, runFlags{
		writeMode:      *writeMode,
		listBackups:    *listBackups,
		restoreN:       *restoreN,
		disguiseInit:   *disguiseInit,
		disguiseVerify: *disguiseVerify,
		disguiseRekey:  *disguiseRekey,
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
	if modes > 1 {
		return 1, errors.New("-w / --list-backups / --restore / --disguise-* are mutually exclusive")
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
	if pass == "" {
		fmt.Fprintln(os.Stderr, "haoma-vault: empty stdin; using InsecureDefaultPassphrase")
		pass = vault.InsecureDefaultPassphrase
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
	if pass == "" {
		return errors.New("write mode requires a non-empty passphrase on stdin line 1")
	}

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

func readPassphrase(r io.Reader) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxPassphraseSize+1))
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	if len(raw) > maxPassphraseSize {
		return "", fmt.Errorf("stdin exceeds %d bytes", maxPassphraseSize)
	}
	return strings.TrimRight(string(raw), "\r\n\t "), nil
}

func readWriteStdin(r io.Reader) (passphrase string, payload []byte, err error) {
	br := bufio.NewReader(io.LimitReader(r, maxPassphraseSize+maxPayloadSize+2))
	passLine, err := br.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", nil, fmt.Errorf("read passphrase: %w", err)
	}
	if len(passLine) > maxPassphraseSize {
		return "", nil, fmt.Errorf("passphrase line exceeds %d bytes", maxPassphraseSize)
	}
	passphrase = strings.TrimRight(passLine, "\r\n")
	rest, err := io.ReadAll(io.LimitReader(br, maxPayloadSize+1))
	if err != nil {
		return "", nil, fmt.Errorf("read payload: %w", err)
	}
	if len(rest) > maxPayloadSize {
		return "", nil, fmt.Errorf("payload exceeds %d bytes", maxPayloadSize)
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

func openOrMint(vaultPath, passphrase string) (vault.Payload, error) {
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
