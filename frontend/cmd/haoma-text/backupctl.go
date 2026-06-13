package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"haoma-frontend/internal/backuparchive"
	"haoma-frontend/internal/ipcclient"
	"haoma-frontend/internal/supervisor"
)

type backupController struct {
	mu sync.Mutex

	root          string
	haomaVaultBin string
	client        *ipcclient.Client
	haomad        *supervisor.Detached
	haoma         *supervisor.Child

	used bool
}

func newBackupController(root, haomaVaultBin string, client *ipcclient.Client, haomad *supervisor.Detached, haoma *supervisor.Child) *backupController {
	return &backupController{
		root:          root,
		haomaVaultBin: haomaVaultBin,
		client:        client,
		haomad:        haomad,
		haoma:         haoma,
	}
}

var ErrBackupUsed = errors.New("backup: already ran in this supervisor session — restart haoma-text")

func (bc *backupController) Backup(destPath string) (string, int, int64, error) {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	if bc.used {
		return "", 0, 0, ErrBackupUsed
	}
	bc.used = true

	resolved, err := resolveBackupPath(destPath)
	if err != nil {
		return "", 0, 0, fmt.Errorf("resolve dest: %w", err)
	}

	slog.Info("backup: starting")
	slog.Debug("backup: resolved dest", slog.String("dest", resolved))

	if bc.client != nil {
		bc.client.Close()
	}

	{
		ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		err := bc.haoma.Stop(ctx)
		cancel()
		if err != nil {
			slog.Warn("backup: haoma stop", slog.Any("err", err))
		}
	}

	{
		ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		err := bc.haomad.Stop(ctx)
		cancel()
		if err != nil {
			return resolved, 0, 0, fmt.Errorf("haomad stop: %w", err)
		}
	}

	time.Sleep(200 * time.Millisecond)

	bin := bc.haomaVaultBin
	if bin == "" {
		bin = "haoma-vault"
	}
	cmd := exec.Command(bin, "--cfg-dir", bc.root, "--archive-write="+resolved)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return resolved, 0, 0, fmt.Errorf("haoma-vault --archive-write: %w (stderr: %s)",
			err, strings.TrimSpace(stderr.String()))
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return resolved, 0, 0, fmt.Errorf("stat archive: %w", err)
	}
	tail := strings.TrimSpace(stderr.String())
	files, byteCount := parseArchiveStderr(tail)
	if byteCount == 0 {

		byteCount = info.Size()
	}
	slog.Info("backup: archive-write ok",
		slog.Int("files", files),
		slog.Int64("bytes", byteCount),
	)
	slog.Debug("backup: archive-write dest", slog.String("dest", resolved))
	return resolved, files, byteCount, nil
}

func resolveBackupPath(dest string) (string, error) {
	dest = strings.TrimSpace(dest)
	if dest == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home: %w", err)
		}
		return filepath.Join(home, backuparchive.DefaultFileName(time.Now())), nil
	}
	if strings.HasPrefix(dest, "~/") || dest == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home: %w", err)
		}
		dest = filepath.Join(home, strings.TrimPrefix(dest, "~"))
	}
	abs, err := filepath.Abs(dest)
	if err != nil {
		return "", fmt.Errorf("absolutize: %w", err)
	}

	if info, err := os.Stat(abs); err == nil && info.IsDir() {
		abs = filepath.Join(abs, backuparchive.DefaultFileName(time.Now()))
	}
	return abs, nil
}

func parseArchiveStderr(tail string) (files int, byteCount int64) {
	for _, line := range strings.Split(tail, "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "archive-write ok") {
			continue
		}
		for _, tok := range strings.Fields(line) {
			switch {
			case strings.HasPrefix(tok, "files="):
				fmt.Sscanf(tok, "files=%d", &files)
			case strings.HasPrefix(tok, "bytes="):
				fmt.Sscanf(tok, "bytes=%d", &byteCount)
			}
		}
	}
	return files, byteCount
}
