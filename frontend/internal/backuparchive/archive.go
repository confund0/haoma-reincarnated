package backuparchive

import (
	"archive/tar"
	stdbytes "bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

var zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}

var excludedSuffixes = []string{
	".log",
	".pid",
	".sock",
	".lock",
}

var excludedBasenames = []string{
	"runtime.json",
	"haomad.runtime.json",
}

var includedTopLevelDirs = []string{
	"backend",
	"frontend",
	"textUI",
}

func isExcluded(name string) bool {
	base := filepath.Base(name)
	for _, b := range excludedBasenames {
		if base == b {
			return true
		}
	}
	for _, s := range excludedSuffixes {
		if strings.HasSuffix(base, s) {
			return true
		}
	}
	return false
}

func Create(cfgDir, destPath string) (files int, bytes int64, err error) {
	cfgDir = filepath.Clean(cfgDir)
	info, err := os.Stat(cfgDir)
	if err != nil {
		return 0, 0, fmt.Errorf("stat cfg-dir: %w", err)
	}
	if !info.IsDir() {
		return 0, 0, fmt.Errorf("cfg-dir %s is not a directory", cfgDir)
	}

	f, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, 0, fmt.Errorf("create archive: %w", err)
	}
	defer f.Close()

	zw, err := zstd.NewWriter(f, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return 0, 0, fmt.Errorf("zstd writer: %w", err)
	}
	tw := tar.NewWriter(zw)

	entries, err := topLevelEntries(cfgDir)
	if err != nil {
		_ = tw.Close()
		_ = zw.Close()
		return 0, 0, err
	}
	for _, e := range entries {
		full := filepath.Join(cfgDir, e)
		n, b, err := addPath(tw, cfgDir, full)
		if err != nil {
			_ = tw.Close()
			_ = zw.Close()
			return files, bytes, fmt.Errorf("add %s: %w", e, err)
		}
		files += n
		bytes += b
	}

	if err := tw.Close(); err != nil {
		_ = zw.Close()
		return files, bytes, fmt.Errorf("close tar: %w", err)
	}
	if err := zw.Close(); err != nil {
		return files, bytes, fmt.Errorf("close zstd: %w", err)
	}
	if err := f.Sync(); err != nil {
		return files, bytes, fmt.Errorf("sync archive: %w", err)
	}
	return files, bytes, nil
}

func topLevelEntries(cfgDir string) ([]string, error) {
	all, err := os.ReadDir(cfgDir)
	if err != nil {
		return nil, fmt.Errorf("read cfg-dir: %w", err)
	}
	var keep []string
	for _, d := range all {
		name := d.Name()
		switch {
		case name == "vault.enc" || strings.HasPrefix(name, "vault.enc."):

			if !isExcluded(name) {
				keep = append(keep, name)
			}
		case name == "disguise.enc":
			keep = append(keep, name)
		default:
			if d.IsDir() {
				for _, top := range includedTopLevelDirs {
					if name == top {
						keep = append(keep, name)
						break
					}
				}
			}
		}
	}
	sort.Strings(keep)
	return keep, nil
}

func addPath(tw *tar.Writer, root, path string) (files int, bytes int64, err error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return 0, 0, fmt.Errorf("rel: %w", err)
	}
	if isExcluded(rel) {
		return 0, 0, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, 0, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() {
		return addDir(tw, root, path)
	}
	if !info.Mode().IsRegular() {

		return 0, 0, nil
	}
	if err := writeFile(tw, rel, path, info); err != nil {
		return 0, 0, err
	}
	return 1, info.Size(), nil
}

func addDir(tw *tar.Writer, root, dir string) (files int, bytes int64, err error) {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return 0, 0, fmt.Errorf("rel: %w", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return 0, 0, fmt.Errorf("stat %s: %w", dir, err)
	}
	hdr := &tar.Header{
		Name:     rel + "/",
		Mode:     int64(info.Mode().Perm()),
		Typeflag: tar.TypeDir,
		ModTime:  info.ModTime(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return 0, 0, fmt.Errorf("write dir header %s: %w", rel, err)
	}
	children, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0, fmt.Errorf("read %s: %w", dir, err)
	}
	sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
	for _, c := range children {
		n, b, err := addPath(tw, root, filepath.Join(dir, c.Name()))
		if err != nil {
			return files, bytes, err
		}
		files += n
		bytes += b
	}
	return files, bytes, nil
}

func writeFile(tw *tar.Writer, rel, full string, info fs.FileInfo) error {
	hdr := &tar.Header{
		Name:     rel,
		Mode:     int64(info.Mode().Perm()),
		Size:     info.Size(),
		Typeflag: tar.TypeReg,
		ModTime:  info.ModTime(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write file header %s: %w", rel, err)
	}
	f, err := os.Open(full)
	if err != nil {
		return fmt.Errorf("open %s: %w", full, err)
	}
	defer f.Close()
	if _, err := io.Copy(tw, f); err != nil {
		return fmt.Errorf("copy %s: %w", full, err)
	}
	return nil
}

func DefaultFileName(now time.Time) string {
	return "haoma-backup-" + now.Format("20060102-150405") + ".tar.zst"
}

var ErrSuspiciousTarPath = errors.New("backuparchive: tar entry escapes destination")

func Extract(srcPath, destDir string) (files int, bytes int64, err error) {
	destDir = filepath.Clean(destDir)
	if err := requireEmptyDir(destDir); err != nil {
		return 0, 0, err
	}
	f, err := os.Open(srcPath)
	if err != nil {
		return 0, 0, fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

	var head [4]byte
	n, err := io.ReadFull(f, head[:])
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return 0, 0, fmt.Errorf("sniff magic: %w", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, 0, fmt.Errorf("rewind: %w", err)
	}

	var src io.Reader = f
	if n == 4 && stdbytes.Equal(head[:], zstdMagic) {
		zr, err := zstd.NewReader(f)
		if err != nil {
			return 0, 0, fmt.Errorf("zstd reader: %w", err)
		}
		defer zr.Close()
		src = zr
	}
	tr := tar.NewReader(src)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return files, bytes, fmt.Errorf("read tar: %w", err)
		}
		clean := filepath.Clean(hdr.Name)
		if filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
			return files, bytes, fmt.Errorf("%w: %q", ErrSuspiciousTarPath, hdr.Name)
		}
		dest := filepath.Join(destDir, clean)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, fs.FileMode(hdr.Mode)&0o700); err != nil {
				return files, bytes, fmt.Errorf("mkdir %s: %w", clean, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
				return files, bytes, fmt.Errorf("mkdir parent %s: %w", clean, err)
			}
			out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fs.FileMode(hdr.Mode)&0o600)
			if err != nil {
				return files, bytes, fmt.Errorf("create %s: %w", clean, err)
			}
			n, err := io.Copy(out, tr)
			closeErr := out.Close()
			if err != nil {
				return files, bytes, fmt.Errorf("copy %s: %w", clean, err)
			}
			if closeErr != nil {
				return files, bytes, fmt.Errorf("close %s: %w", clean, closeErr)
			}
			files++
			bytes += n
		default:

		}
	}
	return files, bytes, nil
}

func requireEmptyDir(destDir string) error {
	info, err := os.Stat(destDir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return os.MkdirAll(destDir, 0o700)
	case err != nil:
		return fmt.Errorf("stat dest: %w", err)
	case !info.IsDir():
		return fmt.Errorf("dest %s is not a directory", destDir)
	}
	entries, err := os.ReadDir(destDir)
	if err != nil {
		return fmt.Errorf("read dest: %w", err)
	}
	var blockers []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".pre-restore-") {
			continue
		}
		if isExcluded(name) {
			continue
		}
		blockers = append(blockers, name)
	}
	if len(blockers) > 0 {
		return fmt.Errorf("dest %s is not empty (%d unexpected entries: %v) — move existing state aside first",
			destDir, len(blockers), blockers)
	}
	return nil
}
