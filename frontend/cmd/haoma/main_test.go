package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func TestSecretsStdin_ReadyLine(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping binary spawn under -short")
	}
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM smoke is Unix-only; Windows path lands later")
	}

	bin := buildHaoma(t)
	dataDir := filepath.Join(t.TempDir(), "frontend")

	hsp := randB64(t, 32)
	fsp := randB64(t, 32)
	tok := randHex(t, 32)
	blob, err := json.Marshal(map[string]any{
		"haomad_store_passphrase":   hsp,
		"frontend_store_passphrase": fsp,
		"haomad_token":              tok,
		"tor_password":              "",
	})
	if err != nil {
		t.Fatalf("marshal secrets: %v", err)
	}

	cmd := exec.Command(bin,
		"--secrets-stdin",
		"--data-dir", dataDir,
		"--addr", "127.0.0.1:0",
		"--log-file", "-",
		"--log-level", "error",
	)
	cmd.Stdin = bytes.NewReader(blob)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	addr := readReadyLine(t, stdout, 5*time.Second, stderr)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v\nstderr:\n%s", addr, err, stderr.String())
	}
	conn.Close()

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("did not exit within 5s of SIGTERM. stderr:\n%s", stderr.String())
	}
}

func TestSecretsStdin_MutualExclusion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping binary spawn under -short")
	}
	bin := buildHaoma(t)
	cases := []struct {
		name string
		flag string
	}{
		{"passphrase-file", "--passphrase-file"},
		{"haomad-token-file", "--haomad-token-file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(bin,
				"--secrets-stdin",
				"--data-dir", t.TempDir(),
				tc.flag, "/dev/null",
			)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("expected non-zero exit; got success. output:\n%s", out)
			}
			if !bytes.Contains(out, []byte("mutually exclusive")) {
				t.Fatalf("expected 'mutually exclusive' in stderr, got:\n%s", out)
			}
		})
	}
}

func buildHaoma(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "haoma")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func readReadyLine(t *testing.T, r io.Reader, timeout time.Duration, stderr *bytes.Buffer) string {
	t.Helper()
	type readyLine struct {
		Status  string `json:"status"`
		APIAddr string `json:"api_addr"`
	}
	lineCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(r)
		if sc.Scan() {
			lineCh <- sc.Text()
			return
		}
		if err := sc.Err(); err != nil {
			errCh <- err
			return
		}
		errCh <- errors.New("no ready line (EOF)")
	}()
	select {
	case s := <-lineCh:
		var rl readyLine
		if err := json.Unmarshal([]byte(s), &rl); err != nil {
			t.Fatalf("ready line JSON: %v (line=%q)", err, s)
		}
		if rl.Status != "ready" {
			t.Fatalf("ready line status = %q; want ready", rl.Status)
		}
		if _, _, err := net.SplitHostPort(rl.APIAddr); err != nil {
			t.Fatalf("ready line api_addr %q: %v", rl.APIAddr, err)
		}
		return rl.APIAddr
	case err := <-errCh:
		t.Fatalf("ready line: %v\nstderr:\n%s", err, stderr.String())
	case <-time.After(timeout):
		t.Fatalf("ready line: timeout after %v\nstderr:\n%s", timeout, stderr.String())
	}
	return ""
}

func randB64(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func randHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}
