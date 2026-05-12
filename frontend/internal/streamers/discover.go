package streamers

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func Discover(flagDir string) (mic, spk string, err error) {
	micName := micBinaryName
	spkName := spkBinaryName
	dirs := candidateDirs(flagDir)
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		mic = pickIfExecutable(dir, micName)
		spk = pickIfExecutable(dir, spkName)
		if mic != "" && spk != "" {
			return mic, spk, nil
		}
	}

	if mic == "" {
		if p, perr := exec.LookPath(micName); perr == nil {
			mic = p
		}
	}
	if spk == "" {
		if p, perr := exec.LookPath(spkName); perr == nil {
			spk = p
		}
	}
	if mic == "" || spk == "" {
		return "", "", fmt.Errorf("streamers: discover: mic=%q spk=%q (set --streamer-dir or $HAOMA_STREAMER_DIR)",
			mic, spk)
	}
	return mic, spk, nil
}

func candidateDirs(flagDir string) []string {
	out := []string{
		os.Getenv("HAOMA_STREAMER_DIR"),
		flagDir,
	}
	if exe, err := os.Executable(); err == nil {
		out = append(out, filepath.Dir(exe))
	}
	return out
}

func pickIfExecutable(dir, name string) string {
	p := filepath.Join(dir, name)
	st, err := os.Stat(p)
	if err != nil {
		return ""
	}
	if st.IsDir() {
		return ""
	}
	if st.Mode()&0o111 == 0 {
		return ""
	}
	return p
}

var ErrNoBinary = errors.New("streamers: streamer binary not found")

var (
	micBinaryName = "haoma-mic"
	spkBinaryName = "haoma-spk"
)
