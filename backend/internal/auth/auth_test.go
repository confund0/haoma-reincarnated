package auth

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOrCreateToken_MintsFreshToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "haomad-token")

	tok, err := LoadOrCreateToken(path)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if len(tok) != 64 {
		t.Fatalf("token length = %d, want 64", len(tok))
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != SecureFileMode {
		t.Fatalf("perm = %o, want %o", perm, SecureFileMode)
	}

	tok2, err := LoadOrCreateToken(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if tok2 != tok {
		t.Fatalf("reload mismatch: got %q want %q", tok2, tok)
	}
}

func TestLoadOrCreateToken_RejectsWideMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "haomad-token")

	if err := os.WriteFile(path, []byte("deadbeef"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := LoadOrCreateToken(path)
	if err == nil || !strings.Contains(err.Error(), "refusing to run") {
		t.Fatalf("expected perm-refusal error, got %v", err)
	}
}

func TestLoadOrCreateToken_RejectsEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "haomad-token")
	if err := os.WriteFile(path, []byte("   \n"), SecureFileMode); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := LoadOrCreateToken(path)
	if err == nil || !strings.Contains(err.Error(), "is empty") {
		t.Fatalf("expected empty-file error, got %v", err)
	}
}

func TestReadSensitive_StripsWhitespace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "haomad-token")
	if err := os.WriteFile(path, []byte("  abc123\n"), SecureFileMode); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tok, err := ReadSensitive(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if tok != "abc123" {
		t.Fatalf("token = %q, want %q", tok, "abc123")
	}
}

func TestReadSensitive_RejectsMissing(t *testing.T) {
	dir := t.TempDir()
	_, err := ReadSensitive(filepath.Join(dir, "missing"))
	if err == nil {
		t.Fatal("expected error for missing token file")
	}
}

func TestCheckToken(t *testing.T) {
	if err := CheckToken("abc", "abc"); err != nil {
		t.Fatalf("match should pass, got %v", err)
	}
	if err := CheckToken("abc", "xyz"); err == nil {
		t.Fatal("mismatch should fail")
	}
	if err := CheckToken("abc", ""); err == nil {
		t.Fatal("empty should fail")
	}
}

func TestMiddleware_AllowsValidBearer(t *testing.T) {
	const tok = "secret-token"
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})
	srv := httptest.NewServer(Middleware(tok, nil, inner))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestMiddleware_RejectsMissingHeader(t *testing.T) {
	srv := httptest.NewServer(Middleware("secret", nil, http.NotFoundHandler()))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/anything")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestMiddleware_RejectsBadScheme(t *testing.T) {
	srv := httptest.NewServer(Middleware("secret", nil, http.NotFoundHandler()))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.Header.Set("Authorization", "Basic abc")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestMiddleware_RejectsBadToken(t *testing.T) {
	srv := httptest.NewServer(Middleware("secret", nil, http.NotFoundHandler()))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestMiddleware_SkipsExemptPath(t *testing.T) {
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(Middleware("secret", []string{"/health"}, inner))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !called {
		t.Fatal("inner handler not invoked for exempt path")
	}
}
