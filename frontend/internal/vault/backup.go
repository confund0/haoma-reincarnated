package vault

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const MaxBackups = 12

func RotateBeforeWrite(path string) error {

	oldest := backupPath(path, MaxBackups)
	if _, err := os.Stat(oldest); err == nil {
		if rmErr := os.Remove(oldest); rmErr != nil {
			return fmt.Errorf("vault: drop oldest backup %s: %w", oldest, rmErr)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("vault: stat oldest backup %s: %w", oldest, err)
	}

	for i := MaxBackups - 1; i >= 1; i-- {
		src := backupPath(path, i)
		dst := backupPath(path, i+1)
		if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return fmt.Errorf("vault: stat backup %s: %w", src, err)
		}
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("vault: rotate %s -> %s: %w", src, dst, err)
		}
	}

	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("vault: stat current %s: %w", path, err)
	}
	if err := os.Rename(path, backupPath(path, 1)); err != nil {
		return fmt.Errorf("vault: rotate current to .1: %w", err)
	}
	return nil
}

type BackupInfo struct {
	N       int
	Path    string
	Size    int64
	ModTime time.Time
}

func ListBackups(path string) ([]BackupInfo, error) {
	var out []BackupInfo
	for i := 1; i <= MaxBackups; i++ {
		bp := backupPath(path, i)
		info, err := os.Stat(bp)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("vault: stat %s: %w", bp, err)
		}
		out = append(out, BackupInfo{
			N:       i,
			Path:    bp,
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}
	return out, nil
}

func RestoreFromBackup(path string, n int) error {
	if n < 1 || n > MaxBackups {
		return fmt.Errorf("vault: backup index %d out of range [1, %d]", n, MaxBackups)
	}
	src := backupPath(path, n)
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("vault: read backup %s: %w", src, err)
	}
	if len(data) == 0 {
		return fmt.Errorf("vault: backup %s is empty", src)
	}
	return writeAtomic(path, data)
}

func backupPath(path string, n int) string {
	return path + "." + strconv.Itoa(n)
}

func FormatBackups(infos []BackupInfo) string {
	if len(infos) == 0 {
		return "(no backups)"
	}
	var b strings.Builder
	for _, info := range infos {
		fmt.Fprintf(&b, "  .%d  %d bytes  %s  %s\n",
			info.N, info.Size, info.ModTime.UTC().Format(time.RFC3339), filepath.Base(info.Path))
	}
	return b.String()
}

func CopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, filepath.Base(dst)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(fileMode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, dst)
}
