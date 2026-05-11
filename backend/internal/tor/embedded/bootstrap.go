package embedded

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"haoma/internal/tor/control"
)

type Config struct {
	BinPath string

	DataDir string

	CportTimeout time.Duration

	Logger *slog.Logger
}

const DefaultCportTimeout = 30 * time.Second

type Instance struct {
	ControlAddr string

	SocksAddr string

	cmd     *exec.Cmd
	logger  *slog.Logger
	dataDir string
}

func Bootstrap(ctx context.Context, cfg Config) (*Instance, error) {
	if cfg.BinPath == "" {
		return nil, errors.New("embedded: Bootstrap requires BinPath")
	}
	if cfg.DataDir == "" {
		return nil, errors.New("embedded: Bootstrap requires DataDir")
	}
	timeout := cfg.CportTimeout
	if timeout <= 0 {
		timeout = DefaultCportTimeout
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	if _, err := os.Stat(cfg.BinPath); err != nil {
		return nil, fmt.Errorf("embedded: tor binary at %s: %w", cfg.BinPath, err)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("embedded: mkdir %s: %w", cfg.DataDir, err)
	}

	torrcPath := defaultTorrcPath(cfg.DataDir)
	cportPath := defaultControlFile(cfg.DataDir)
	cookiePath := defaultCookieFile(cfg.DataDir)
	noticePath := defaultNoticeLog(cfg.DataDir)

	_ = os.Remove(cportPath)
	_ = os.Remove(cookiePath)

	torrcBody := renderTorrc(torrcConfig{
		DataDir:         cfg.DataDir,
		ControlPortFile: cportPath,
		CookieAuthFile:  cookiePath,
		NoticeLog:       noticePath,
	})
	if err := os.WriteFile(torrcPath, []byte(torrcBody), 0o600); err != nil {
		return nil, fmt.Errorf("embedded: write torrc %s: %w", torrcPath, err)
	}

	cmd := exec.Command(cfg.BinPath, "-f", torrcPath, "--quiet")

	cmd.Stdout = nil
	cmd.Stderr = nil

	logger.Info("embedded tor: starting",
		slog.String("bin", cfg.BinPath),
		slog.String("torrc", torrcPath),
		slog.String("data_dir", cfg.DataDir),
	)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("embedded: start tor: %w", err)
	}

	inst := &Instance{
		cmd:     cmd,
		logger:  logger,
		dataDir: cfg.DataDir,
	}

	if err := inst.resolveAddrs(ctx, cportPath, timeout); err != nil {

		_ = inst.Stop(2 * time.Second)
		return nil, err
	}

	logger.Info("embedded tor: ready",
		slog.String("control_addr", inst.ControlAddr),
		slog.String("socks_addr", inst.SocksAddr),
	)
	return inst, nil
}

func (inst *Instance) resolveAddrs(ctx context.Context, cportPath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("embedded: cport file %s did not appear within %s", cportPath, timeout)
		}

		if err := inst.cmd.Process.Signal(syscall.Signal(0)); err != nil {
			return fmt.Errorf("embedded: tor exited before publishing control port: %w", err)
		}
		raw, err := os.ReadFile(cportPath)
		if err == nil && len(raw) > 0 {
			ctrlAddr, err := parseCportFile(raw)
			if err != nil {
				return fmt.Errorf("embedded: parse cport file: %w", err)
			}
			inst.ControlAddr = ctrlAddr
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}

	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := control.Dial(dialCtx, inst.ControlAddr)
	if err != nil {
		return fmt.Errorf("embedded: dial control %s: %w", inst.ControlAddr, err)
	}
	defer conn.Close()
	if _, err := conn.Authenticate(""); err != nil {
		return fmt.Errorf("embedded: authenticate control: %w", err)
	}
	socks, err := conn.GetInfo("net/listeners/socks")
	if err != nil {
		return fmt.Errorf("embedded: GETINFO net/listeners/socks: %w", err)
	}
	inst.SocksAddr = strings.Trim(socks, "\"")
	if inst.SocksAddr == "" {
		return errors.New("embedded: GETINFO returned empty SOCKS listener")
	}
	return nil
}

func parseCportFile(raw []byte) (string, error) {
	text := strings.TrimSpace(string(raw))
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "PORT=") {
			continue
		}
		addr := strings.TrimPrefix(line, "PORT=")

		host, port, ok := splitHostPort(addr)
		if !ok {
			return "", fmt.Errorf("malformed PORT line: %q", line)
		}
		if host == "" {
			return "", fmt.Errorf("PORT line missing host: %q", line)
		}
		return host + ":" + port, nil
	}
	return "", fmt.Errorf("no PORT= line in cport file: %q", text)
}

func splitHostPort(s string) (host, port string, ok bool) {
	colon := strings.LastIndexByte(s, ':')
	if colon <= 0 || colon == len(s)-1 {
		return "", "", false
	}
	host = s[:colon]
	port = s[colon+1:]
	if _, err := strconv.Atoi(port); err != nil {
		return "", "", false
	}
	return host, port, true
}

func (inst *Instance) Stop(grace time.Duration) error {
	if inst == nil || inst.cmd == nil || inst.cmd.Process == nil {
		return nil
	}

	if inst.cmd.ProcessState != nil {
		return nil
	}
	if grace <= 0 {
		grace = 5 * time.Second
	}

	inst.logger.Info("embedded tor: stopping (SIGTERM)")
	if err := inst.cmd.Process.Signal(syscall.SIGTERM); err != nil {

		inst.logger.Debug("embedded tor: SIGTERM error", slog.Any("err", err))
	}

	done := make(chan error, 1)
	go func() { done <- inst.cmd.Wait() }()

	select {
	case err := <-done:
		return err
	case <-time.After(grace):
		inst.logger.Warn("embedded tor: SIGTERM grace expired, escalating to SIGKILL")
		_ = inst.cmd.Process.Kill()
		return <-done
	}
}

func (inst *Instance) TorrcPath() string {
	if inst == nil {
		return ""
	}
	return filepath.Join(inst.dataDir, "torrc")
}
