package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const rootEnv = "HAOMA_ROOT"

const defaultRoot = ".haoma"

const SecureMode os.FileMode = 0o700

const SecureFileMode os.FileMode = 0o600

const (
	BackendSubdir  = "backend"
	FrontendSubdir = "frontend"
	TextSubdir     = "textUI"
)

func BackendDir(root string) string { return filepath.Join(root, BackendSubdir) }

func FrontendDir(root string) string { return filepath.Join(root, FrontendSubdir) }

func TextDir(root string) string { return filepath.Join(root, TextSubdir) }

func Root() (string, error) {
	if v := os.Getenv(rootEnv); v != "" {
		return Expand(v)
	}
	if runtime.GOOS == "windows" {
		cfg, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("paths: resolve config dir: %w", err)
		}
		return filepath.Join(cfg, "haoma"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("paths: resolve home: %w", err)
	}
	return filepath.Join(home, defaultRoot), nil
}

func RootFromFlag(flagValue string) (string, error) {
	if flagValue == "" {
		return Root()
	}
	expanded, err := Expand(flagValue)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", fmt.Errorf("paths: absolute %q: %w", flagValue, err)
	}
	return abs, nil
}

func Expand(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("paths: empty path")
	}
	if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("paths: resolve home: %w", err)
		}
		return home, nil
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("paths: resolve home: %w", err)
		}
		return filepath.Join(home, path[2:]), nil
	}
	if strings.HasPrefix(path, "$HOME/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("paths: resolve home: %w", err)
		}
		return filepath.Join(home, path[len("$HOME/"):]), nil
	}
	return path, nil
}

func Bootstrap(subdir string) (string, error) {
	if subdir == "" || strings.ContainsRune(subdir, os.PathSeparator) {
		return "", fmt.Errorf("paths: subdir must be a single path component, got %q", subdir)
	}
	root, err := Root()
	if err != nil {
		return "", err
	}
	if err := ensureDir(root); err != nil {
		return "", err
	}
	sub := filepath.Join(root, subdir)
	if err := ensureDir(sub); err != nil {
		return "", err
	}
	return sub, nil
}

func BootstrapAt(path string) (string, error) {
	expanded, err := Expand(path)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", fmt.Errorf("paths: absolute %q: %w", expanded, err)
	}
	if err := ensureDir(abs); err != nil {
		return "", err
	}
	return abs, nil
}

func ensureDir(path string) error {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(path, SecureMode); err != nil {
			return fmt.Errorf("paths: create %s: %w", path, err)
		}

		if err := os.Chmod(path, SecureMode); err != nil {
			return fmt.Errorf("paths: chmod %s: %w", path, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("paths: stat %s: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("paths: %s exists and is not a directory", path)
	}
	if perm := info.Mode().Perm(); perm != SecureMode {
		return fmt.Errorf("paths: %s has mode %o, want %o — refusing to run (run: chmod 700 %s)",
			path, perm, SecureMode, path)
	}
	return nil
}

func DefaultDownloadsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("paths: resolve home: %w", err)
	}
	if os.Getenv("TERMUX_VERSION") != "" {
		return filepath.Join(home, "storage", "shared", "Download"), nil
	}
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(home, "Downloads"), nil
	default:
		return filepath.Join(home, "Downloads"), nil
	}
}

func DefaultAttachStartDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("paths: resolve home: %w", err)
	}
	if os.Getenv("TERMUX_VERSION") != "" {
		return filepath.Join(home, "storage", "shared"), nil
	}
	return home, nil
}

func ResolveSaveStartDir(vaultSaveDir string) string {
	if vaultSaveDir != "" {
		if info, err := os.Stat(vaultSaveDir); err == nil && info.IsDir() {
			return vaultSaveDir
		}
	}
	if dl, err := DefaultDownloadsDir(); err == nil && dl != "" {
		if info, err := os.Stat(dl); err == nil && info.IsDir() {
			return dl
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	return "/"
}

func ResolveAttachStartDir(vaultAttachDir string) string {
	if vaultAttachDir != "" {
		if info, err := os.Stat(vaultAttachDir); err == nil && info.IsDir() {
			return vaultAttachDir
		}
	}
	if dl, err := DefaultAttachStartDir(); err == nil && dl != "" {
		if info, err := os.Stat(dl); err == nil && info.IsDir() {
			return dl
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	return "/"
}

func WriteSensitiveFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("paths: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(SecureFileMode); err != nil {
		tmp.Close()
		return fmt.Errorf("paths: chmod temp: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("paths: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("paths: close temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("paths: rename %s: %w", path, err)
	}
	return nil
}
