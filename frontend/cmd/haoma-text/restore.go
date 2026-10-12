package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/backuparchive"
	"haoma-frontend/internal/paths"
	"haoma-frontend/internal/supervisor"
	"haoma-frontend/internal/vault"
)

type passphrasePrompt func(label string) ([]byte, error)

const restorePassphraseTries = 3

const restorePassphraseLabel = "Passphrase of the backed-up device: "

var errRestoreNotApplied = errors.New("the restore was not applied; this device is unchanged")

var errRestoreBroken = errors.New("the restore failed part way")

var errRestoreNotEmpty = errors.New("the device is not empty")

const (
	notEmptyExit = 8
	pendingExit  = 7
)

type restoreReport struct {
	KeptLive []string

	Skipped []string
}

func (r restoreReport) Lines() []string {
	var out []string
	for _, name := range r.KeptLive {
		out = append(out, "kept this device's "+name+": the backup has none.")
	}
	if len(r.Skipped) > 0 {
		out = append(out, "left out what this build does not know: "+strings.Join(r.Skipped, " ")+".")
	}
	return out
}

const (
	commitKeptPrefix    = "haoma-vault: archive-commit kept the live "
	commitSkippedPrefix = "haoma-vault: archive-commit left out entries this build does not carry: "
)

func parseCommitReport(stderr string) restoreReport {
	var r restoreReport
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, commitKeptPrefix):
			name, _, _ := strings.Cut(strings.TrimPrefix(line, commitKeptPrefix), ":")
			r.KeptLive = append(r.KeptLive, name)
		case strings.HasPrefix(line, commitSkippedPrefix):
			r.Skipped = strings.Fields(strings.TrimPrefix(line, commitSkippedPrefix))
		}
	}
	return r
}

func runRestoreBackup(root, haomaVaultBin, srcPath string, prompt passphrasePrompt) (vaultUnlock, restoreReport, error) {
	var none restoreReport
	abs, err := filepath.Abs(srcPath)
	if err != nil {
		return vaultUnlock{}, none, fmt.Errorf("resolve src: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return vaultUnlock{}, none, fmt.Errorf("source archive: %w", err)
	}
	if !info.Mode().IsRegular() {
		return vaultUnlock{}, none, fmt.Errorf("source archive %s is not a regular file", abs)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return vaultUnlock{}, none, fmt.Errorf("resolve cfg-dir: %w", err)
	}
	if rel, err := filepath.Rel(rootAbs, abs); err == nil &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
		return vaultUnlock{}, none, fmt.Errorf("source archive %s lives inside cfg-dir %s — move it elsewhere first", abs, rootAbs)
	}

	bin := haomaVaultBin
	if bin == "" {
		bin = "haoma-vault"
	}
	slog.Info("restore-backup: starting")
	slog.Debug("restore-backup: paths",
		slog.String("bin", bin),
		slog.String("src", abs),
		slog.String("cfg_dir", rootAbs),
	)

	staging, err := stageArchive(bin, rootAbs, abs)
	if err != nil {
		if errors.Is(err, errRestoreNotEmpty) || errors.Is(err, errRestoreBroken) {
			return vaultUnlock{}, none, err
		}
		return vaultUnlock{}, none, fmt.Errorf("%w: the backup could not be read: %v", errRestoreNotApplied, err)
	}
	slog.Info("restore-backup: archive staged")

	unlocked, err := openStagedVault(staging, prompt)
	if err != nil {
		discardStaging(bin, rootAbs, staging)
		return vaultUnlock{}, none, fmt.Errorf("%w: %v", errRestoreNotApplied, err)
	}

	if err := stopHaomadForRestore(rootAbs); err != nil {
		clear(unlocked.passphrase)
		discardStaging(bin, rootAbs, staging)
		return vaultUnlock{}, none, fmt.Errorf("%w: %v", errRestoreNotApplied, err)
	}

	report, err := commitStaging(bin, rootAbs, staging, unlocked.passphrase)
	if err != nil {
		clear(unlocked.passphrase)
		if !errors.Is(err, errRestoreBroken) {
			discardStaging(bin, rootAbs, staging)
		}
		return vaultUnlock{}, none, err
	}
	slog.Info("restore-backup: archive swapped in")
	return unlocked, report, nil
}

func stageArchive(bin, root, src string) (string, error) {
	cmd := exec.Command(bin, "--cfg-dir", root, "--archive-stage="+src)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			switch exitErr.ExitCode() {
			case notEmptyExit:
				return "", notEmptyError()
			case pendingExit:
				return "", brokenError(root)
			}
		}
		return "", fmt.Errorf("haoma-vault --archive-stage: %w (stderr: %s)",
			err, strings.TrimSpace(stderr.String()))
	}
	staging := strings.TrimSpace(stdout.String())
	if staging == "" {
		return "", errors.New("haoma-vault --archive-stage printed no staging dir")
	}
	return staging, nil
}

func openStagedVault(staging string, prompt passphrasePrompt) (vaultUnlock, error) {
	path := filepath.Join(staging, vaultFileName)
	for try := 1; ; try++ {
		pass, err := prompt(restorePassphraseLabel)
		if err != nil {
			return vaultUnlock{}, err
		}
		payload, params, err := vault.Open(path, pass)
		if err == nil {
			return vaultUnlock{payload: payload, passphrase: pass, params: params}, nil
		}
		clear(pass)
		if !errors.Is(err, vault.ErrUnseal) {
			return vaultUnlock{}, fmt.Errorf("open the vault of the backup: %w", err)
		}
		if try >= restorePassphraseTries {
			return vaultUnlock{}, errors.New("the passphrase does not open the vault of the backup")
		}
		fmt.Fprintln(os.Stderr, "haoma-text: that passphrase does not open the vault of the backup.")
	}
}

func commitStaging(bin, root, staging string, pass []byte) (restoreReport, error) {
	cmd := exec.Command(bin, "--cfg-dir", root, "--archive-commit="+staging)
	cmd.Stdin = bytes.NewReader(pass)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err := cmd.Run()
	tail := strings.TrimSpace(stderr.String())
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		return restoreReport{}, fmt.Errorf("%w: haoma-vault --archive-commit: %v", errRestoreNotApplied, err)
	}
	switch code {
	case 0, 6:

		slog.Debug("restore-backup: haoma-vault commit ok", slog.String("tail", tail))
		return parseCommitReport(tail), nil
	case 4, pendingExit:

		slog.Error("restore-backup: the restore failed part way", slog.String("stderr", tail))
		return restoreReport{}, brokenError(root)
	case notEmptyExit:
		return restoreReport{}, notEmptyError()
	default:
		return restoreReport{}, fmt.Errorf("%w: haoma-vault --archive-commit: exit %d (stderr: %s)", errRestoreNotApplied, code, tail)
	}
}

func notEmptyError() error {
	return sentenceError{kind: errRestoreNotEmpty, text: backuparchive.NotEmptyMessage}
}

type sentenceError struct {
	kind error
	text string
}

func (e sentenceError) Error() string { return e.text }
func (e sentenceError) Unwrap() error { return e.kind }

func brokenError(root string) error {
	return sentenceError{kind: errRestoreBroken, text: (&backuparchive.Pending{}).Message(root)}
}

func cleanRestoreLeftovers(bin, root string) {
	if p, err := backuparchive.PendingCommit(root); err != nil || p != nil {
		return
	}
	if bin == "" {
		bin = "haoma-vault"
	}
	cmd := exec.Command(bin, "--cfg-dir", root, "--clean-restore-leftovers")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	for _, line := range strings.Split(strings.TrimSpace(stderr.String()), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			slog.Info("restore-backup: " + strings.TrimPrefix(line, "haoma-vault: "))
		}
	}
	if err != nil {
		slog.Warn("restore-backup: the leftovers of an earlier restore could not all be deleted", slog.Any("err", err))
	}
}

func discardStaging(bin, root, staging string) {
	cmd := exec.Command(bin, "--cfg-dir", root, "--archive-discard="+staging)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		slog.Warn("restore-backup: discard of the staging dir failed",
			slog.Any("err", err), slog.String("stderr", strings.TrimSpace(stderr.String())))
	}
}

const haomadStopTimeout = stopTimeout

func stopHaomadForRestore(root string) error {
	runtimePath := filepath.Join(root, runtimeFileName)
	d, err := supervisor.FindRunning("haomad", runtimePath)
	if err != nil {
		slog.Warn("restore-backup: the runtime file of haomad cannot be read; no haomad is stopped",
			slog.Any("err", err))
		return nil
	}
	if d == nil {
		slog.Debug("restore-backup: no haomad running")
		return nil
	}
	certPath := filepath.Join(paths.BackendDir(root), "cert.pem")
	if err := haomadAnswers(d.APIAddr, certPath); err != nil {
		slog.Warn("restore-backup: the process in the runtime file of haomad does not answer as this haomad; it is not stopped",
			slog.Int("pid", d.PID), slog.Any("err", err))
		return nil
	}
	fmt.Fprintf(os.Stderr, "haoma-text: stopping haomad (pid %d) for the restore.\n", d.PID)
	ctx, cancel := context.WithTimeout(context.Background(), haomadStopTimeout)
	defer cancel()
	if err := d.Stop(ctx); err != nil {
		return fmt.Errorf("haomad (pid %d) did not stop: %w", d.PID, err)
	}
	slog.Info("restore-backup: haomad stopped", slog.Int("pid", d.PID))
	return nil
}

func haomadAnswers(addr, certPath string) error {
	cfg, err := backendapi.PinnedTLSConfig(certPath)
	if err != nil {
		return err
	}
	dialer := &net.Dialer{Timeout: healthDial}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, cfg)
	if err != nil {
		return err
	}
	return conn.Close()
}
