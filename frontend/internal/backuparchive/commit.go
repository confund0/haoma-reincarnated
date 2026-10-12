package backuparchive

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"haoma-frontend/internal/lockfile"
)

const MarkerFile = ".restore-commit"

var ErrStoreInUse = errors.New("backuparchive: a store in the data dir is open in another process")

var ErrCommitBroken = errors.New("backuparchive: the restore failed part way")

var ErrCommitPending = errors.New("backuparchive: a restore did not finish")

var storeDirs = []string{
	filepath.Join("backend", "badger"),
	filepath.Join("frontend", "badger"),
}

var (
	rename    = os.Rename
	removeAll = os.RemoveAll
)

type CommitResult struct {
	Moved int

	Skipped []string

	KeptLive []string
}

var keptWhenMissing = []string{"calc-help.txt"}

type Pending struct {
	Staging string `json:"staging"`
}

func (p *Pending) Message(cfgDir string) string {
	return fmt.Sprintf("The restore did not finish. Delete the folder %s and restore your backup again.", cfgDir)
}

func PendingCommit(cfgDir string) (*Pending, error) {
	raw, err := os.ReadFile(filepath.Join(cfgDir, MarkerFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", MarkerFile, err)
	}
	var p Pending
	if err := json.Unmarshal(raw, &p); err != nil || p.Staging == "" {
		return &Pending{Staging: "?"}, nil
	}
	return &p, nil
}

func Commit(cfgDir, stagingDir string) (CommitResult, error) {
	var res CommitResult
	if p, err := PendingCommit(cfgDir); err != nil {
		return res, err
	} else if p != nil {
		return res, fmt.Errorf("%w: %s", ErrCommitPending, p.Message(cfgDir))
	}
	locks, err := lockStores(cfgDir)
	if err != nil {
		return res, err
	}
	defer func() {
		for _, lk := range locks {
			_ = lk.Release()
		}
	}()
	empty, err := DeviceIsEmpty(cfgDir)
	if err != nil {
		return res, fmt.Errorf("check the device is empty: %w", err)
	}
	if !empty {
		return res, ErrDeviceNotEmpty
	}

	staged, skipped, err := carriedEntries(stagingDir)
	if err != nil {
		return res, fmt.Errorf("read staging: %w", err)
	}
	if !contains(staged, "vault.enc") {
		return res, fmt.Errorf("staging %s holds no vault.enc", stagingDir)
	}
	carriedLive, _, err := carriedEntries(cfgDir)
	if err != nil {
		return res, fmt.Errorf("read cfg-dir: %w", err)
	}
	var live []string
	for _, name := range carriedLive {
		if contains(keptWhenMissing, name) && !contains(staged, name) {
			res.KeptLive = append(res.KeptLive, name)
			continue
		}
		live = append(live, name)
	}

	if err := ClearDeviceEmpty(cfgDir); err != nil {
		return res, fmt.Errorf("remove the empty-device file: %w", err)
	}
	if err := writeMarker(cfgDir, stagingDir); err != nil {
		return res, err
	}

	for _, name := range vaultFirst(live) {
		if err := removeAll(filepath.Join(cfgDir, name)); err != nil {
			return res, fmt.Errorf("%w: delete %s: %v", ErrCommitBroken, name, err)
		}
	}
	var in []string
	for _, name := range vaultLast(staged) {
		if err := rename(filepath.Join(stagingDir, name), filepath.Join(cfgDir, name)); err != nil {
			return res, fmt.Errorf("%w: move in %s: %v", ErrCommitBroken, name, err)
		}
		in = append(in, name)
	}

	_ = syncDir(cfgDir)
	removeMarker(cfgDir)

	res.Moved = len(in)
	res.Skipped = skipped
	return res, nil
}

func writeMarker(cfgDir, stagingDir string) error {
	p := Pending{Staging: filepath.Base(stagingDir)}
	raw, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marker: %w", err)
	}
	path := filepath.Join(cfgDir, MarkerFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("marker: %w", err)
	}
	_, werr := f.Write(append(raw, '\n'))
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("marker: %w", err)
	}
	if err := syncDir(cfgDir); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("marker: sync %s: %w", cfgDir, err)
	}
	return nil
}

func removeMarker(cfgDir string) {
	_ = os.Remove(filepath.Join(cfgDir, MarkerFile))
	_ = syncDir(cfgDir)
}

var syncDir = func(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = f.Sync()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func lockStores(cfgDir string) ([]*lockfile.Lock, error) {
	var locks []*lockfile.Lock
	release := func() {
		for _, lk := range locks {
			_ = lk.Release()
		}
	}
	for _, rel := range storeDirs {
		dir := filepath.Join(cfgDir, rel)
		info, err := os.Stat(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			release()
			return nil, fmt.Errorf("stat %s: %w", rel, err)
		}
		if !info.IsDir() {
			continue
		}
		lk, err := lockfile.TryLockDir(dir)
		if errors.Is(err, lockfile.ErrInUse) {
			release()
			if pid := lockPID(dir); pid != 0 {
				return nil, fmt.Errorf("%w: %s (pid %d, from %s)", ErrStoreInUse, rel, pid, filepath.Join(rel, "LOCK"))
			}
			return nil, fmt.Errorf("%w: %s", ErrStoreInUse, rel)
		}
		if err != nil {
			release()
			return nil, fmt.Errorf("lock %s: %w", rel, err)
		}
		locks = append(locks, lk)
	}
	return locks, nil
}

func lockPID(dir string) int {
	raw, err := os.ReadFile(filepath.Join(dir, "LOCK"))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

func carriedEntries(dir string) (carried, rest []string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	for _, d := range entries {
		if IsCarried(d.Name(), d.IsDir()) {
			carried = append(carried, d.Name())
		} else {
			rest = append(rest, d.Name())
		}
	}
	sort.Strings(carried)
	sort.Strings(rest)
	return carried, rest, nil
}

func vaultFirst(names []string) []string {
	out := make([]string, 0, len(names))
	if contains(names, "vault.enc") {
		out = append(out, "vault.enc")
	}
	for _, n := range names {
		if n != "vault.enc" {
			out = append(out, n)
		}
	}
	return out
}

func vaultLast(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n != "vault.enc" {
			out = append(out, n)
		}
	}
	if contains(names, "vault.enc") {
		out = append(out, "vault.enc")
	}
	return out
}

func contains(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}
