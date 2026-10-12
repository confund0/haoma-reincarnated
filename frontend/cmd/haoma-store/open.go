package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"haoma-frontend/internal/store"
	"haoma-frontend/internal/vault"
)

type side struct {
	name string

	slot string

	dataDir string
	st      *store.Store
}

func (s *side) close() {
	if s != nil && s.st != nil {
		_ = s.st.Lock()
	}
}

var repoMarkers = []string{
	filepath.Join("backend", "go.mod"),
	filepath.Join("frontend", "go.mod"),
}

var defaultRunDir = filepath.Join("tmp", "test")

func findRepoRoot(start string) string {
	dir, err := filepath.Abs(start)
	if err != nil {
		return ""
	}
	for {
		ok := true
		for _, m := range repoMarkers {
			if _, err := os.Stat(filepath.Join(dir, m)); err != nil {
				ok = false
				break
			}
		}
		if ok {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func resolveDataDir(given, cwd string) (dataDir, accountName string, err error) {
	if given == "" {
		return "", "", errors.New("give a store directory")
	}

	var tried []string
	candidates := []string{}
	if filepath.IsAbs(given) {
		candidates = append(candidates, given)
	} else {
		candidates = append(candidates, filepath.Join(cwd, given))
		if repo := findRepoRoot(cwd); repo != "" {
			candidates = append(candidates,
				filepath.Join(repo, given),
				filepath.Join(repo, defaultRunDir, given))
		}
	}

	for _, c := range candidates {
		tried = append(tried, c)
		if !isDir(c) {
			continue
		}

		if _, statErr := os.Stat(filepath.Join(c, "meta.json")); statErr == nil {
			return c, accountFromPath(c), nil
		}

		inner := filepath.Join(c, "frontend")
		if _, statErr := os.Stat(filepath.Join(inner, "meta.json")); statErr == nil {
			return inner, filepath.Base(filepath.Clean(c)), nil
		}
	}

	return "", "", fmt.Errorf(
		"no store found for %q\nlooked in: %v\na store directory holds meta.json and badger/; an account directory holds frontend/",
		given, tried)
}

func accountFromPath(dataDir string) string {
	clean := filepath.Clean(dataDir)
	if filepath.Base(clean) == "frontend" {
		return filepath.Base(filepath.Dir(clean))
	}
	return filepath.Base(clean)
}

func openSide(given, slot, vaultPath, cwd string) (*side, error) {
	dataDir, name, err := resolveDataDir(given, cwd)
	if err != nil {
		return nil, err
	}
	pw, err := resolvePassphrase(slot, vaultPath)
	if err != nil {
		return nil, err
	}
	st, err := store.UnlockReadOnly(dataDir, pw)
	if err != nil {
		return nil, explainOpenFailure(name, dataDir, err)
	}
	return &side{name: name, slot: slot, dataDir: dataDir, st: st}, nil
}

func explainOpenFailure(name, dataDir string, err error) error {
	switch {
	case errors.Is(err, store.ErrStoreInUse):
		return fmt.Errorf(
			"refusing to read %s at %s: its daemon is running and holds the store\n"+
				"stop the daemons for this account first, in the order the client, then haoma, then haomad\n"+
				"there is no flag to override this: reading a store a daemon is writing can damage it",
			name, dataDir)
	case errors.Is(err, store.ErrNeedsCleanClose):
		return fmt.Errorf(
			"cannot read %s at %s: the last daemon did not close the store\n"+
				"finishing the write log needs a write, and this tool never writes\n"+
				"start that account's daemon once and stop it normally, then run this again",
			name, dataDir)
	case errors.Is(err, store.ErrNoMeta):
		return fmt.Errorf("%s at %s is not a store: no meta.json", name, dataDir)
	}
	return fmt.Errorf("cannot open %s at %s: %w", name, dataDir, err)
}

const (
	envPassphrase = "HAOMA_FRONTEND_PASSPHRASE"
	envVaultPass  = "HAOMA_VAULT_PASSPHRASE"
)

func resolvePassphrase(slot, vaultPath string) (string, error) {
	if vaultPath != "" {
		master := os.Getenv(envVaultPass)
		if master == "" {
			return "", fmt.Errorf("%s is empty, and it is needed to open a vault", envVaultPass)
		}
		payload, _, err := vault.Open(vaultPath, []byte(master))
		if err != nil {
			return "", fmt.Errorf("open vault %s: %w", vaultPath, err)
		}
		if payload.FrontendStorePassphrase == "" {
			return "", fmt.Errorf("vault %s holds no frontend store passphrase", vaultPath)
		}
		return payload.FrontendStorePassphrase, nil
	}
	perSlot := envPassphrase + "_" + upper(slot)
	if pw := os.Getenv(perSlot); pw != "" {
		return pw, nil
	}
	if pw := os.Getenv(envPassphrase); pw != "" {
		return pw, nil
	}
	return "", fmt.Errorf(
		"no passphrase for store %s: set %s, or %s for both stores, or point -vault-%s at the account's vault.enc with %s set",
		slot, perSlot, envPassphrase, slot, envVaultPass)
}

func upper(s string) string {
	out := []byte(s)
	for i := range out {
		if out[i] >= 'a' && out[i] <= 'z' {
			out[i] -= 'a' - 'A'
		}
	}
	return string(out)
}
