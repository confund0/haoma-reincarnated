package logging_test

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"haoma-frontend/internal/logging"
)

func TestNew_DefaultsToWarn(t *testing.T) {
	logger, closer, err := logging.New(logging.Config{Service: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer closer()
	if !logger.Enabled(nil, slog.LevelWarn) {
		t.Error("default config should enable WARN")
	}
	if logger.Enabled(nil, slog.LevelInfo) {
		t.Error("default config should NOT enable INFO")
	}
}

func TestNew_LevelDebugEnablesAll(t *testing.T) {
	logger, closer, err := logging.New(logging.Config{Level: "debug"})
	if err != nil {
		t.Fatal(err)
	}
	defer closer()
	for _, lvl := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
		if !logger.Enabled(nil, lvl) {
			t.Errorf("debug config should enable %s", lvl)
		}
	}
}

func TestNew_RejectsUnknownLevel(t *testing.T) {
	_, _, err := logging.New(logging.Config{Level: "trace"})
	if err == nil {
		t.Error("expected error on unknown level")
	}
}

func TestNew_RejectsUnknownFormat(t *testing.T) {
	_, _, err := logging.New(logging.Config{Format: "xml"})
	if err == nil {
		t.Error("expected error on unknown format")
	}
}

func TestNew_FileWritten_0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	logger, closer, err := logging.New(logging.Config{Level: "info", File: path, Service: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("hello", slog.String("kind", "text"))
	if err := closer(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if !strings.Contains(s, "hello") || !strings.Contains(s, `service=tester`) || !strings.Contains(s, `kind=text`) {
		t.Errorf("log line missing expected fields: %q", s)
	}
	st, _ := os.Stat(path)
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Errorf("file mode = %o, want 0600 (logs are sensitive)", mode)
	}
}

func TestNew_DiscardWhenFileEmpty(t *testing.T) {
	logger, closer, err := logging.New(logging.Config{Level: "debug", File: ""})
	if err != nil {
		t.Fatal(err)
	}
	defer closer()
	logger.Info("if you see this, discard isn't working")
}
