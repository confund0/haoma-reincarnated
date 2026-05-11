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
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"haoma/internal/auth"
)

func TestVersionSet(t *testing.T) {
	if version == "" {
		t.Fatal("version must not be empty")
	}
}

func TestSecretsStdin_EndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping binary spawn under -short")
	}
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM smoke is Unix-only; Windows path lands later")
	}

	bin := buildHaomad(t)
	storeDir := filepath.Join(t.TempDir(), "store")

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
		"--store", storeDir,
		"--api-addr", "127.0.0.1:0",
		"--tor-control", "127.0.0.1:1",
		"--tor-socks", "127.0.0.1:1",
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

	client := pinnedClient(t, storeDir)

	body := httpGetTLS(t, client, "https://"+addr+"/health", "")
	if !bytes.Contains(body, []byte(`"status":"ok"`)) {
		t.Fatalf("/health body = %q; want status:ok", body)
	}

	if code := httpStatusTLS(t, client, "https://"+addr+"/peers", ""); code != http.StatusUnauthorized {
		t.Fatalf("/peers no-bearer: got %d, want 401", code)
	}

	if code := httpStatusTLS(t, client, "https://"+addr+"/peers", tok); code == http.StatusUnauthorized {
		t.Fatalf("/peers with bearer: got 401, want not-401")
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:

		_ = err
	case <-time.After(5 * time.Second):
		t.Fatalf("did not exit within 5s of SIGTERM. stderr:\n%s", stderr.String())
	}
}

func TestRuntimeFile_EndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping binary spawn under -short")
	}
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM smoke is Unix-only; Windows path lands later")
	}

	bin := buildHaomad(t)
	tmp := t.TempDir()
	storeDir := filepath.Join(tmp, "store")
	runtimePath := filepath.Join(tmp, "haomad.runtime.json")

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
		"--store", storeDir,
		"--runtime-file", runtimePath,
		"--api-addr", "127.0.0.1:0",
		"--tor-control", "127.0.0.1:1",
		"--tor-socks", "127.0.0.1:1",
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

	raw, err := os.ReadFile(runtimePath)
	if err != nil {
		t.Fatalf("read runtime file: %v", err)
	}
	var info struct {
		PID       int    `json:"pid"`
		APIAddr   string `json:"api_addr"`
		StartedAt string `json:"started_at"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatalf("parse runtime file: %v (raw=%q)", err, raw)
	}
	if info.PID != cmd.Process.Pid {
		t.Errorf("runtime PID = %d, want spawned child PID %d", info.PID, cmd.Process.Pid)
	}
	if info.APIAddr != addr {
		t.Errorf("runtime api_addr = %q, want ready-line addr %q", info.APIAddr, addr)
	}
	if info.StartedAt == "" {
		t.Errorf("runtime started_at empty")
	}
	if perm := mustStatPerm(t, runtimePath); perm != 0o600 {
		t.Errorf("runtime file mode = %o, want 0600", perm)
	}

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

	if _, err := os.Stat(runtimePath); !os.IsNotExist(err) {
		t.Errorf("runtime file present after graceful shutdown: stat err=%v", err)
	}
}

func TestSecretsStdin_MutualExclusion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping binary spawn under -short")
	}
	bin := buildHaomad(t)
	cases := []struct {
		name string
		flag string
	}{
		{"passphrase-file", "--passphrase-file"},
		{"tor-password-file", "--tor-password-file"},
		{"token-file", "--token-file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(bin,
				"--secrets-stdin",
				"--store", t.TempDir(),
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

func buildHaomad(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "haomad")
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

func pinnedClient(t *testing.T, certDir string) *http.Client {
	t.Helper()

	cfg, err := auth.PinnedClientTLSConfig(auth.CertPath(certDir))
	if err != nil {
		t.Fatalf("pinnedClient: %v", err)
	}
	return &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: cfg},
	}
}

func httpGetTLS(t *testing.T, client *http.Client, url, bearer string) []byte {
	t.Helper()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("http get %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("http get %s: status %d, body %q", url, resp.StatusCode, body)
	}
	return body
}

func httpStatusTLS(t *testing.T, client *http.Client, url, bearer string) int {
	t.Helper()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("http get %s: %v", url, err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func randB64(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func mustStatPerm(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

func randHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}
