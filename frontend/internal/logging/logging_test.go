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
		slog.String("service_id", "dummybbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbd"),
		slog.String("chat_id", "4d64c624d111baed132a40abcb196fdd"),
		slog.String("msg_id", "168dfbe1a59160a0cea1c58fefe85d01"),
		slog.String("envelope_id", "b9828febb119e2db7467e7480d07a76e"),
		slog.String("receipt_msg_id", "e15e2452a4dafed889dcbd4a1ffb7ea3"),
		slog.String("rotation_id", "fadc34b9c7f0e58a1234abcd5678ef00"),
		slog.String("dest", "http://dummyaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaad.onion"),
		slog.String("call_id", "Y061TApJGdgndoE_TyGfu_FKEbUhZEeE_W5v3VzrFjs"),
		slog.String("token", "zAB4Q4L62pyYMGXFqUIVTo5PLEFezNWXsRF0UjkeaiY"),
		slog.String("peer_url", "http://dummyaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaad.onion/audio/zAB4Q4L62pyYMGXFqUIVTo5PLEFezNWXsRF0UjkeaiY"),
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
		"dummybbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbd",
		"4d64c624d111baed132a40abcb196fdd",
		"168dfbe1a59160a0cea1c58fefe85d01",
		"b9828febb119e2db7467e7480d07a76e",
		"Y061TApJGdgndoE_TyGfu_FKEbUhZEeE_W5v3VzrFjs",
		"zAB4Q4L62pyYMGXFqUIVTo5PLEFezNWXsRF0UjkeaiY",
		"dummyaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaad",
		"e15e2452a4dafed889dcbd4a1ffb7ea3",
		"fadc34b9c7f0e58a1234abcd5678ef00",
	} {
		if strings.Contains(s, leak) {
			t.Errorf("redact handler let identifier slip through: %q in %q", leak, s)
		}
	}
	for _, kept := range []string{"5c7b2897…", "dummybbb…", "4d64c624…", "168dfbe1…", "b9828feb…", "Y061TApJ…", "zAB4Q4L6…", "e15e2452…", "fadc34b9…", "127.0.0.1:1234"} {
		if !strings.Contains(s, kept) {
			t.Errorf("expected %q in redacted output: %q", kept, s)
		}
	}

	if !strings.Contains(s, "dummyaaa….onion/audio/zAB4") {
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

func TestNew_RedactsFromPeerAndNames(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	logger, closer, err := logging.New(logging.Config{Level: "info", File: path})
	if err != nil {
		t.Fatal(err)
	}
	const (
		onion  = "dummybbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbd"
		peerID = "6fee9137002cabf79411888e48e670c4"
	)
	logger.Info("received",
		slog.String("from", onion),
		slog.String("peer", peerID),
		slog.String("label", "Aunt Petra"),
		slog.String("alias", "Uncle Jonas"),
		slog.String("inviter_nick", "inviterbob"),
		slog.String("joiner_nick", "joinercarol"),
	)
	if err := closer(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	for _, leak := range []string{onion, peerID, "Aunt Petra", "Uncle Jonas", "inviterbob", "joinercarol"} {
		if strings.Contains(s, leak) {
			t.Errorf("value reached the log in clear: %q in %q", leak, s)
		}
	}
	for _, want := range []string{"from=dummybbb…", "peer=6fee9137…", "label=h:", "alias=h:", "inviter_nick=h:", "joiner_nick=h:"} {
		if !strings.Contains(s, want) {
			t.Errorf("expected %q in redacted output: %q", want, s)
		}
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
		msg: `Post "http://dummyaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaad.onion/message": socks connect failed`,
	}
	logger.Warn("send failed", slog.Any("err", wrappedErr))
	_ = closer()
	got, _ := os.ReadFile(path)
	s := string(got)
	if strings.Contains(s, "dummyaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaad") {
		t.Errorf("onion not stripped from err field: %q", s)
	}
	if !strings.Contains(s, "dummyaaa….onion") {
		t.Errorf("expected redacted onion form in err: %q", s)
	}
}

func TestNew_RedactsOnionUnderAnyAttrName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	logger, closer, err := logging.New(logging.Config{Level: "info", File: path})
	if err != nil {
		t.Fatal(err)
	}
	const onion = "dummyaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaad"
	logger.Info("ipc send error",
		slog.String("message", `pair: dial onion: Post "http://`+onion+`.onion:9999/pair": socks connect failed`),
		slog.String("detail", "reached "+onion+".onion"),
		slog.String("raw_err", "probe "+onion+".onion failed"),
		slog.Any("probe_err", &textErr{msg: "GET http://" + onion + ".onion/: timeout"}),
	)
	_ = closer()
	got, _ := os.ReadFile(path)
	s := string(got)
	if strings.Contains(s, onion) {
		t.Errorf("an address reached the log under a non-err attribute: %q", s)
	}
	for _, want := range []string{"message=", "detail=", "raw_err=", "probe_err="} {
		if !strings.Contains(s, want) {
			t.Errorf("expected %q to survive redaction: %q", want, s)
		}
	}
	if strings.Count(s, "dummyaaa….onion") != 4 {
		t.Errorf("expected every attribute redacted in place: %q", s)
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
