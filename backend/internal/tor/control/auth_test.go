package control

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthNull_OK(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {
		got := readLine(t, s)
		if got != "AUTHENTICATE\r\n" {
			t.Errorf("got %q, want AUTHENTICATE\\r\\n", got)
		}
		s.Write([]byte("250 OK\r\n"))
	})
	defer c.Close()
	if err := c.AuthNull(); err != nil {
		t.Fatalf("AuthNull: %v", err)
	}
}

func TestAuthNull_Rejected(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {
		readLine(t, s)
		s.Write([]byte("515 Authentication failed\r\n"))
	})
	defer c.Close()
	if err := c.AuthNull(); err == nil {
		t.Fatal("expected error")
	}
}

func TestAuthPassword_SendsHex(t *testing.T) {
	want := "AUTHENTICATE " + hex.EncodeToString([]byte("swordfish")) + "\r\n"
	c := mockTor(t, func(s net.Conn) {
		got := readLine(t, s)
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		s.Write([]byte("250 OK\r\n"))
	})
	defer c.Close()
	if err := c.AuthPassword("swordfish"); err != nil {
		t.Fatalf("AuthPassword: %v", err)
	}
}

func TestAuthSafeCookie_Success(t *testing.T) {
	cookiePath := filepath.Join(t.TempDir(), "cookie")
	cookie := make([]byte, 32)
	rand.Read(cookie)
	if err := os.WriteFile(cookiePath, cookie, 0o600); err != nil {
		t.Fatal(err)
	}

	serverNonce := make([]byte, 32)
	rand.Read(serverNonce)

	c := mockTor(t, func(s net.Conn) {
		line := readLine(t, s)
		if !strings.HasPrefix(line, "AUTHCHALLENGE SAFECOOKIE ") {
			t.Errorf("expected AUTHCHALLENGE SAFECOOKIE prefix, got %q", line)
			return
		}
		nonceHex := strings.TrimSuffix(strings.TrimPrefix(line, "AUTHCHALLENGE SAFECOOKIE "), "\r\n")
		clientNonce, err := hex.DecodeString(nonceHex)
		if err != nil {
			t.Error(err)
			return
		}

		serverMac := hmac.New(sha256.New, []byte(safeCookieServerKey))
		serverMac.Write(cookie)
		serverMac.Write(clientNonce)
		serverMac.Write(serverNonce)
		serverHash := serverMac.Sum(nil)

		s.Write([]byte("250 AUTHCHALLENGE SERVERHASH=" + hex.EncodeToString(serverHash) +
			" SERVERNONCE=" + hex.EncodeToString(serverNonce) + "\r\n"))

		line = readLine(t, s)
		if !strings.HasPrefix(line, "AUTHENTICATE ") {
			t.Errorf("expected AUTHENTICATE, got %q", line)
			return
		}
		gotHex := strings.TrimSuffix(strings.TrimPrefix(line, "AUTHENTICATE "), "\r\n")
		got, err := hex.DecodeString(gotHex)
		if err != nil {
			t.Error(err)
			return
		}
		expectedMac := hmac.New(sha256.New, []byte(safeCookieClientKey))
		expectedMac.Write(cookie)
		expectedMac.Write(clientNonce)
		expectedMac.Write(serverNonce)
		if !hmac.Equal(got, expectedMac.Sum(nil)) {
			t.Error("client hash mismatch")
			return
		}
		s.Write([]byte("250 OK\r\n"))
	})
	defer c.Close()

	if err := c.AuthSafeCookie(cookiePath); err != nil {
		t.Fatalf("AuthSafeCookie: %v", err)
	}
}

func TestAuthSafeCookie_ServerHashMismatch(t *testing.T) {
	cookiePath := filepath.Join(t.TempDir(), "cookie")
	if err := os.WriteFile(cookiePath, make([]byte, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	c := mockTor(t, func(s net.Conn) {
		readLine(t, s)
		bad := make([]byte, 32)
		s.Write([]byte("250 AUTHCHALLENGE SERVERHASH=" + hex.EncodeToString(bad) +
			" SERVERNONCE=" + hex.EncodeToString(bad) + "\r\n"))
	})
	defer c.Close()

	err := c.AuthSafeCookie(cookiePath)
	if err == nil || !strings.Contains(err.Error(), "server hash mismatch") {
		t.Fatalf("err = %v, want server hash mismatch", err)
	}
}

func TestAuthSafeCookie_BadCookieLength(t *testing.T) {
	cookiePath := filepath.Join(t.TempDir(), "short")
	if err := os.WriteFile(cookiePath, []byte("tooshort"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := mockTor(t, func(s net.Conn) {})
	defer c.Close()
	if err := c.AuthSafeCookie(cookiePath); err == nil || !strings.Contains(err.Error(), "32") {
		t.Fatalf("err = %v, want 32-byte length error", err)
	}
}

func TestAuthSafeCookie_MissingFile(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {})
	defer c.Close()
	if err := c.AuthSafeCookie("/nonexistent-cookie"); err == nil {
		t.Fatal("expected error for missing cookie file")
	}
}
