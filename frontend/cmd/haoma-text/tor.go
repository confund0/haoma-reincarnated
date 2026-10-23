package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"haoma-frontend/internal/secrets"
	"haoma-frontend/internal/tui"
)

const (
	defaultTorBin = "tor"

	torDirName = "tor"
)

type torSetup struct {
	system bool
	bin    string
	dir    string
}

func (t torSetup) String() string {
	if t.system {
		return "the system tor"
	}
	dir := t.dir
	if dir == "" {
		dir = "haomad's default data dir"
	}
	return fmt.Sprintf("its own tor (%s, data dir %s)", t.bin, dir)
}

func torFlags(systemTor bool, bin string, binSet bool) (torSetup, error) {
	if systemTor {
		if binSet {
			return torSetup{}, errors.New("--tor-bin names the tor haomad starts; --system-tor uses a tor that is already running. Pass one or the other")
		}
		return torSetup{system: true}, nil
	}

	if !strings.ContainsRune(bin, '/') && !strings.ContainsRune(bin, filepath.Separator) {
		found, err := exec.LookPath(bin)
		if err != nil {
			return torSetup{}, fmt.Errorf("no %q found on PATH. Install tor, name its path with --tor-bin, or run with --system-tor to use the system tor", bin)
		}
		bin = found
	}
	abs, err := filepath.Abs(bin)
	if err != nil {
		return torSetup{}, fmt.Errorf("--tor-bin %s: %w", bin, err)
	}
	if err := checkTorBin(abs, runtime.GOOS); err != nil {
		return torSetup{}, err
	}
	return torSetup{bin: abs}, nil
}

func checkTorBin(bin, goos string) error {
	st, err := os.Stat(bin)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("no tor binary at %s. Install tor, name its path with --tor-bin, or run with --system-tor to use the system tor", bin)
		}
		return fmt.Errorf("tor binary %s: %w", bin, err)
	}
	if st.IsDir() || (goos != "windows" && st.Mode().Perm()&0o111 == 0) {
		return fmt.Errorf("tor binary %s is not an executable file. Name another with --tor-bin, or run with --system-tor", bin)
	}
	return nil
}

func (t torSetup) withDir(root string) torSetup {
	if !t.system {
		t.dir = filepath.Join(root, torDirName)
	}
	return t
}

func buildHaomadArgs(root, runtimePath, logLevel, logPath string, tor torSetup, callStats bool) ([]string, error) {
	args := []string{
		"--cfg-dir", root,
		"--secrets-stdin",
		"--api-addr", "127.0.0.1:0",
		"--runtime-file", runtimePath,
		"--log-level", logLevel,
		"--log-file", logPath,
	}
	if callStats {
		args = append(args, "--call-stats")
	}
	if tor.system {
		return args, nil
	}

	if tor.bin == "" || tor.dir == "" {
		return nil, fmt.Errorf("own tor with no binary (%q) or no data dir (%q)", tor.bin, tor.dir)
	}
	return append(args, "--manage-tor", tor.bin, "--tor-data-dir", tor.dir), nil
}

func torSetupFromArgs(args []string) torSetup {
	var t torSetup
	for i := 0; i < len(args); i++ {
		name, value, hasValue := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		if !strings.HasPrefix(args[i], "-") {
			continue
		}
		if name != "manage-tor" && name != "tor-data-dir" {
			continue
		}
		if !hasValue && i+1 < len(args) {
			i++
			value = args[i]
		}
		if name == "manage-tor" {
			t.bin = value
		} else {
			t.dir = value
		}
	}
	if t.bin == "" {

		return torSetup{system: true}
	}
	return t
}

func torSetupFromRuntime(manageTor, torDataDir string) torSetup {
	if manageTor == "" {
		return torSetup{system: true}
	}
	return torSetup{bin: manageTor, dir: torDataDir}
}

func secretsForSpawn(s secrets.Secrets, tor torSetup) secrets.Secrets {
	if !tor.system {
		s.TorPassword = ""
	}
	return s
}

func attachedTorStatus(pid int, running, wanted torSetup) string {
	return fmt.Sprintf(tui.StyleNoticeWarn+"tor:"+tui.StyleReset+" haomad was already running and keeps %s until it restarts. This run asked for %s. To switch now, end haomad (pid %d) and start haoma-text again.", running, wanted, pid)
}
