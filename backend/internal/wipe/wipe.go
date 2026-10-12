package wipe

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func Device(root string, keep ...string) error {
	if err := checkRoot(root); err != nil {
		return err
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("wipe: read %s: %w", root, withoutPath(err))
	}

	kept := make(map[string]bool, len(keep))
	for _, name := range keep {
		kept[name] = true
	}

	var firstErr error
	removed, failed := 0, 0
	for _, e := range entries {
		if kept[e.Name()] {
			continue
		}
		if rmErr := os.RemoveAll(filepath.Join(root, e.Name())); rmErr != nil {
			if firstErr == nil {
				firstErr = withoutPath(rmErr)
			}
			failed++
			continue
		}
		removed++
	}
	if firstErr != nil {
		return fmt.Errorf("wipe: under %s removed %d entries, %d failed, the first with: %w",
			root, removed, failed, firstErr)
	}
	return nil
}

func withoutPath(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return fmt.Errorf("%s: %w", pe.Op, pe.Err)
	}
	return err
}

func checkRoot(root string) error {
	if root == "" {
		return errors.New("wipe: no data root was given")
	}
	if !filepath.IsAbs(root) {
		return fmt.Errorf("wipe: data root %q is not an absolute path", root)
	}

	clean := filepath.Clean(root)
	if clean == string(filepath.Separator) {
		return errors.New("wipe: refusing to erase the filesystem root")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if clean == filepath.Clean(home) {
			return errors.New("wipe: refusing to erase the home directory itself")
		}
	}

	info, err := os.Stat(clean)
	if err != nil {
		return fmt.Errorf("wipe: data root %s: %w", clean, withoutPath(err))
	}
	if !info.IsDir() {
		return fmt.Errorf("wipe: data root %s is not a directory", clean)
	}

	return nil
}
