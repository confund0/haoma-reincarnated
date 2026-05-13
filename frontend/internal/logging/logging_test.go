package logging_test

import (
	"context"
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
	if !logger.Enabled(context.TODO(), slog.LevelWarn) {
		t.Error("default config should enable WARN")
	}
	if logger.Enabled(context.TODO(), slog.LevelInfo) {
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
		if !logger.Enabled(context.TODO(), lvl) {
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

func TestNew_RedactsSensitiveFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	logger, closer, err := logging.New(logging.Config{Level: "info", File: path})
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("shipped",
		slog.String("peer_id", "5c7b2897f04bdf9ebcd91c769cf84344"),
		slog.String("service_id", "uhrvvclzwwdfrxm6f336w6qiidgxniw6vbzij7j6rdh2scpxlzgppwqd"),
		slog.String("chat_id", "4d64c624d111baed132a40abcb196fdd"),
		slog.String("msg_id", "168dfbe1a59160a0cea1c58fefe85d01"),
		slog.String("envelope_id", "b9828febb119e2db7467e7480d07a76e"),
		slog.String("receipt_msg_id", "e15e2452a4dafed889dcbd4a1ffb7ea3"),
		slog.String("rotation_id", "fadc34b9c7f0e58a1234abcd5678ef00"),
		slog.String("dest", "http://s3zsmprypzrpuwwppb3isi4aeuexquidnnuiboj2qmmmot3ykffx7zyd.onion"),
		slog.String("call_id", "Y061TApJGdgndoE_TyGfu_FKEbUhZEeE_W5v3VzrFjs"),
		slog.String("token", "zAB4Q4L62pyYMGXFqUIVTo5PLEFezNWXsRF0UjkeaiY"),
		slog.String("peer_url", "http://s3zsmprypzrpuwwppb3isi4aeuexquidnnuiboj2qmmmot3ykffx7zyd.onion/audio/zAB4Q4L62pyYMGXFqUIVTo5PLEFezNWXsRF0UjkeaiY"),
		slog.String("nick", "n9i-dbg"),
		slog.String("sender_nick", "alice"),
		slog.String("port", "127.0.0.1:1234"),
	)
	if err := closer(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	for _, leak := range []string{
		"5c7b2897f04bdf9ebcd91c769cf84344",
		"uhrvvclzwwdfrxm6f336w6qiidgxniw6vbzij7j6rdh2scpxlzgppwqd",
		"4d64c624d111baed132a40abcb196fdd",
		"168dfbe1a59160a0cea1c58fefe85d01",
		"b9828febb119e2db7467e7480d07a76e",
		"Y061TApJGdgndoE_TyGfu_FKEbUhZEeE_W5v3VzrFjs",
		"zAB4Q4L62pyYMGXFqUIVTo5PLEFezNWXsRF0UjkeaiY",
		"s3zsmprypzrpuwwppb3isi4aeuexquidnnuiboj2qmmmot3ykffx7zyd",
		"e15e2452a4dafed889dcbd4a1ffb7ea3",
		"fadc34b9c7f0e58a1234abcd5678ef00",
	} {
		if strings.Contains(s, leak) {
			t.Errorf("redact handler let identifier slip through: %q in %q", leak, s)
		}
	}
	for _, kept := range []string{"5c7b2897…", "uhrvvclz…", "4d64c624…", "168dfbe1…", "b9828feb…", "Y061TApJ…", "zAB4Q4L6…", "e15e2452…", "fadc34b9…", "127.0.0.1:1234"} {
		if !strings.Contains(s, kept) {
			t.Errorf("expected %q in redacted output: %q", kept, s)
		}
	}

	if !strings.Contains(s, "s3zsmpry….onion/audio/zAB4") {
		t.Errorf("peer_url onion not redacted in place (path should survive): %q", s)
	}

	for _, leak := range []string{"n9i-dbg", "alice"} {
		if strings.Contains(s, leak) {
			t.Errorf("nick handle leaked: %q in %q", leak, s)
		}
	}
	if !strings.Contains(s, "nick=h:") || !strings.Contains(s, "sender_nick=h:") {
		t.Errorf("expected hashed nick / sender_nick fields, got %q", s)
	}
}

func TestNew_RedactsOnionInErrField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	logger, closer, err := logging.New(logging.Config{Level: "info", File: path})
	if err != nil {
		t.Fatal(err)
	}
	wrappedErr := &textErr{
		msg: `Post "http://s3zsmprypzrpuwwppb3isi4aeuexquidnnuiboj2qmmmot3ykffx7zyd.onion/message": socks connect failed`,
	}
	logger.Warn("send failed", slog.Any("err", wrappedErr))
	_ = closer()
	got, _ := os.ReadFile(path)
	s := string(got)
	if strings.Contains(s, "s3zsmprypzrpuwwppb3isi4aeuexquidnnuiboj2qmmmot3ykffx7zyd") {
		t.Errorf("onion not stripped from err field: %q", s)
	}
	if !strings.Contains(s, "s3zsmpry….onion") {
		t.Errorf("expected redacted onion form in err: %q", s)
	}
}

type textErr struct{ msg string }

func (e *textErr) Error() string { return e.msg }

func TestNew_RotatesAtMaxBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	logger, closer, err := logging.New(logging.Config{
		Level: "info", File: path, Service: "rot", MaxBytes: 256,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		logger.Info("filler", slog.Int("i", i), slog.String("pad", "xxxxxxxxxxxxxxxxxxxxxxxx"))
	}
	if err := closer(); err != nil {
		t.Fatal(err)
	}
	prev := path + ".prev"
	if _, err := os.Stat(prev); err != nil {
		t.Fatalf(".prev not created after 50 log lines past 256-byte cap: %v", err)
	}
	cur, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(cur)) > 1024 {
		t.Errorf("post-rotation .log = %d bytes, expected something modest after the most recent rotation", len(cur))
	}
}
