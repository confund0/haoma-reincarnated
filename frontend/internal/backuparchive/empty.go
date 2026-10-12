package backuparchive

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const EmptyMarkerFile = "device-empty"

const NotEmptyMessage = "This device holds contacts or chats. A backup can only be restored on an empty device. " +
	"Unlock the app and wipe it, or reinstall the app, then restore. " +
	"If anything on this device is valuable, make a backup of it first."

var ErrDeviceNotEmpty = errors.New("backuparchive: the device is not empty")

var deviceLocalCarried = []string{"disguise.enc", "calc-help.txt"}

func DeviceIsEmpty(cfgDir string) (bool, error) {

	if p, err := PendingCommit(cfgDir); err != nil {
		return false, err
	} else if p != nil {
		return false, nil
	}
	info, err := os.Lstat(filepath.Join(cfgDir, EmptyMarkerFile))
	switch {
	case err == nil && info.Mode().IsRegular():
		return true, nil
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return false, fmt.Errorf("stat %s: %w", EmptyMarkerFile, err)
	}
	entries, err := os.ReadDir(cfgDir)
	if err != nil {
		return false, fmt.Errorf("read cfg-dir: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		if !IsCarried(name, e.IsDir()) || contains(deviceLocalCarried, name) {
			continue
		}
		if !e.IsDir() {

			return false, nil
		}
		held, err := holdsFile(filepath.Join(cfgDir, name))
		if err != nil {
			return false, err
		}
		if held {
			return false, nil
		}
	}
	return true, nil
}

func holdsFile(dir string) (bool, error) {
	held := false
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			held = true
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("read %s: %w", filepath.Base(dir), err)
	}
	return held, nil
}

func MarkDeviceEmpty(cfgDir string) error {
	path := filepath.Join(cfgDir, EmptyMarkerFile)
	tmp := path + ".tmp"
	body := []byte("this device holds no contact, no chat and no linked device\n")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("write %s: %w", EmptyMarkerFile, err)
	}
	_, werr := f.Write(body)
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write %s: %w", EmptyMarkerFile, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write %s: %w", EmptyMarkerFile, err)
	}
	return syncDir(cfgDir)
}

func ClearDeviceEmpty(cfgDir string) error {
	removed := false
	for _, name := range []string{EmptyMarkerFile, EmptyMarkerFile + ".tmp"} {
		err := os.Remove(filepath.Join(cfgDir, name))
		switch {
		case err == nil:
			removed = true
		case !errors.Is(err, os.ErrNotExist):
			return fmt.Errorf("remove %s: %w", name, err)
		}
	}
	if !removed {
		return nil
	}
	if err := syncDir(cfgDir); err != nil {
		return fmt.Errorf("remove %s: sync: %w", EmptyMarkerFile, err)
	}
	return nil
}

type LeftDir struct {
	Name  string
	Bytes int64
}

func CleanRestoreLeftovers(cfgDir string) ([]LeftDir, error) {
	if p, err := PendingCommit(cfgDir); err != nil {
		return nil, err
	} else if p != nil {
		return nil, fmt.Errorf("%w: %s", ErrCommitPending, p.Message(cfgDir))
	}
	entries, err := os.ReadDir(cfgDir)
	if err != nil {
		return nil, fmt.Errorf("read cfg-dir: %w", err)
	}
	var gone []LeftDir
	var errs []error
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, ".staging-") && !strings.HasPrefix(name, ".pre-restore-") {
			continue
		}

		if !e.IsDir() {
			continue
		}
		full := filepath.Join(cfgDir, name)
		size := dirBytes(full)
		if err := removeAll(full); err != nil {
			errs = append(errs, fmt.Errorf("delete %s: %w", name, err))
			continue
		}
		gone = append(gone, LeftDir{Name: name, Bytes: size})
	}
	if len(gone) > 0 {
		_ = syncDir(cfgDir)
	}
	return gone, errors.Join(errs...)
}

func dirBytes(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}
