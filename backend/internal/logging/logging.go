package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

type Config struct {
	Level   string
	File    string
	Format  string
	Service string
}

func New(cfg Config) (*slog.Logger, func() error, error) {
	level, err := parseLevel(cfg.Level)
	if err != nil {
		return nil, noopCloser, err
	}
	w, closer, err := openWriter(cfg.File)
	if err != nil {
		return nil, noopCloser, err
	}

	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	switch strings.ToLower(strings.TrimSpace(cfg.Format)) {
	case "", "text":
		handler = slog.NewTextHandler(w, opts)
	case "json":
		handler = slog.NewJSONHandler(w, opts)
	default:
		_ = closer()
		return nil, noopCloser, fmt.Errorf("logging: unknown format %q (want text|json)", cfg.Format)
	}

	logger := slog.New(handler)
	if cfg.Service != "" {
		logger = logger.With(slog.String("service", cfg.Service))
	}
	return logger, closer, nil
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "warn", "warning":
		return slog.LevelWarn, nil
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("logging: unknown level %q (want debug|info|warn|error)", s)
}

func openWriter(path string) (io.Writer, func() error, error) {
	if path == "" {
		return io.Discard, noopCloser, nil
	}
	if path == "-" {
		return os.Stderr, noopCloser, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, noopCloser, fmt.Errorf("logging: open %s: %w", path, err)
	}
	return f, f.Close, nil
}

func noopCloser() error { return nil }
