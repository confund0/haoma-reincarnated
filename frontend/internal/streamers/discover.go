package streamers

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func Discover(flagDir string) (mic, spk, cam, vid string, err error) {
	dirs := candidateDirs(flagDir)
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if mic == "" {
			mic = pickIfExecutable(dir, micBinaryName)
		}
		if spk == "" {
			spk = pickIfExecutable(dir, spkBinaryName)
		}
		if cam == "" {
			cam = pickIfExecutable(dir, camBinaryName)
		}
		if vid == "" {
			vid = pickIfExecutable(dir, vidBinaryName)
		}
		if mic != "" && spk != "" && cam != "" && vid != "" {
			return mic, spk, cam, vid, nil
		}
	}

	for name, dst := range map[string]*string{
		micBinaryName: &mic,
		spkBinaryName: &spk,
		camBinaryName: &cam,
		vidBinaryName: &vid,
	} {
		if *dst == "" {
			if p, perr := exec.LookPath(name); perr == nil {
				*dst = p
			}
		}
	}
	if mic == "" || spk == "" {
		return "", "", "", "", fmt.Errorf("streamers: discover: mic=%q spk=%q (set --streamer-dir or $HAOMA_STREAMER_DIR)",
			mic, spk)
	}
	return mic, spk, cam, vid, nil
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
	camBinaryName = "haoma-cam"
	vidBinaryName = "haoma-vid"
)
