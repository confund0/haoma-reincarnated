package opener

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync"
)

type Opener struct {
	name string

	bin string

	preArgs []string
}

var ErrUnavailable = errors.New("opener: no platform handler available")

var (
	once   sync.Once
	cached Opener
)

func Detect() Opener {
	once.Do(func() {
		cached = discover()
	})
	return cached
}

func Reset() {
	once = sync.Once{}
	cached = Opener{}
}

func (o Opener) Available() bool { return o.name != "" }

func (o Opener) Name() string { return o.name }

func (o Opener) Open(ctx context.Context, path string) error {
	if !o.Available() {
		return ErrUnavailable
	}
	if path == "" {
		return errors.New("opener: empty path")
	}
	args := append([]string(nil), o.preArgs...)
	args = append(args, path)
	cmd := exec.CommandContext(ctx, o.bin, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("opener: spawn %s: %w", o.name, err)
	}

	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("opener: release %s: %w", o.name, err)
	}
	return nil
}

func discover() Opener {
	for _, c := range candidates() {
		bin, err := exec.LookPath(c.exe)
		if err != nil {
			continue
		}
		return Opener{name: c.name, bin: bin, preArgs: c.preArgs}
	}
	return Opener{}
}

type candidate struct {
	name    string
	exe     string
	preArgs []string
}

func candidates() []candidate {
	var out []candidate
	if os.Getenv("TERMUX_VERSION") != "" {
		out = append(out, candidate{name: "termux-open", exe: "termux-open"})
	}
	switch runtime.GOOS {
	case "darwin":
		out = append(out, candidate{name: "open", exe: "open"})
	case "windows":

		out = append(out, candidate{name: "cmd /c start", exe: "cmd", preArgs: []string{"/c", "start", ""}})
	default:
		out = append(out, candidate{name: "xdg-open", exe: "xdg-open"})
	}
	return out
}
