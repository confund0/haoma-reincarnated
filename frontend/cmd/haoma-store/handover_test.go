package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"haoma-frontend/internal/events"
)

func TestHandover_TheIDFindPrintsIsAcceptedByTheLogTool(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the log tool")
	}

	const msgID = "a1b2c3d4e5f60718293a4b5c6d7e8f90"

	dir := oneDeviceWithHistory(t)
	s := openOne(t, dir)
	res, report := runFindInto(t, []*side{s}, findFilter{text: "I love you"}, true)
	if len(res.hits) != 1 {
		t.Fatalf("%d hits, want 1", len(res.hits))
	}
	printed := strings.TrimSpace(report)
	if printed != msgID {
		t.Fatalf("find printed %q, want the full message id", printed)
	}

	logDir := writeLogFixture(t, msgID)
	tool := buildLogTool(t)

	cmd := exec.Command(tool, "-root", logDir, "-all", "-msg", printed)
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the log tool refused the id find printed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "the line about the message") {
		t.Fatalf("the log tool accepted the id but returned no line about it\n%s", out)
	}

	cmd = exec.Command(tool, "-root", logDir, "-all", "-msg", shortID(msgID))
	cmd.Dir = t.TempDir()
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the log tool refused the shortened form: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "the line about the message") {
		t.Fatalf("the shortened form returned no line\n%s", out)
	}
}

func TestHandover_AnUnrelatedIDMatchesNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the log tool")
	}
	logDir := writeLogFixture(t, "a1b2c3d4e5f60718293a4b5c6d7e8f90")
	tool := buildLogTool(t)

	cmd := exec.Command(tool, "-root", logDir, "-all", "-msg", "ffffffffffffffffffffffffffffffff")
	cmd.Dir = t.TempDir()
	out, _ := cmd.CombinedOutput()
	if strings.Contains(string(out), "the line about the message") {
		t.Fatalf("an unrelated message id matched a line\n%s", out)
	}
}

func buildLogTool(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "haoma-logs")
	cmd := exec.Command("go", "build", "-o", out, "haoma-frontend/cmd/haoma-logs")
	if built, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build the log tool: %v\n%s", err, built)
	}
	return out
}

func writeLogFixture(t *testing.T, msgID string) string {
	t.Helper()
	root := t.TempDir()
	account := filepath.Join(root, "alice")
	if err := os.MkdirAll(account, 0o700); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-time.Minute).Format(time.RFC3339Nano)
	line := fmt.Sprintf("time=%s level=INFO msg=\"the line about the message\" msg_id=%s kind=%s\n",
		when, msgID, events.KindText)
	if err := os.WriteFile(filepath.Join(account, "haomad.log"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}
