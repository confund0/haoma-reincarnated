package main

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func runRestoreBackup(root, haomaVaultBin, srcPath string) error {
	abs, err := filepath.Abs(srcPath)
	if err != nil {
		return fmt.Errorf("resolve src: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("source archive: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("source archive %s is not a regular file", abs)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve cfg-dir: %w", err)
	}

	if rel, err := filepath.Rel(rootAbs, abs); err == nil &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
		return fmt.Errorf("source archive %s lives inside cfg-dir %s — move it elsewhere first", abs, rootAbs)
	}

	bin := haomaVaultBin
	if bin == "" {
		bin = "haoma-vault"
	}
	slog.Info("restore-backup: invoking haoma-vault")
	slog.Debug("restore-backup: paths",
		slog.String("bin", bin),
		slog.String("src", abs),
		slog.String("cfg_dir", rootAbs),
	)
	cmd := exec.Command(bin, "--cfg-dir", rootAbs, "--archive-restore="+abs)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("haoma-vault --archive-restore: %w (stderr: %s)",
			err, strings.TrimSpace(stderr.String()))
	}
	if tail := strings.TrimSpace(stderr.String()); tail != "" {
		slog.Debug("restore-backup: haoma-vault ok", slog.String("tail", tail))
	}
	slog.Info("restore-backup: archive extracted")
	return nil
}
