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

type step struct {
	expectPrefix string
	reply        string
}

func scriptedTor(t *testing.T, steps []step) *Conn {
	t.Helper()
	return mockTor(t, func(s net.Conn) {
		for _, st := range steps {
			line := readLine(t, s)
			if !strings.HasPrefix(line, st.expectPrefix) {
				t.Errorf("got %q, want prefix %q", line, st.expectPrefix)
				return
			}
			if _, err := s.Write([]byte(st.reply)); err != nil {
				return
			}
		}
	})
}

func protocolInfoLine(methods string, cookieFile string) string {
	auth := "250-AUTH METHODS=" + methods
	if cookieFile != "" {
		auth += ` COOKIEFILE="` + cookieFile + `"`
	}
	return "250-PROTOCOLINFO 1\r\n" +
		auth + "\r\n" +
		`250-VERSION Tor="0.4.8.10"` + "\r\n" +
		"250 OK\r\n"
}

func writeCookie(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cookie")
	cookie := make([]byte, 32)
	rand.Read(cookie)
	if err := os.WriteFile(path, cookie, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAuthenticate_PrefersSafeCookie(t *testing.T) {
	cookiePath := writeCookie(t)
	cookie, _ := os.ReadFile(cookiePath)
	serverNonce := make([]byte, 32)
	rand.Read(serverNonce)

	c := mockTor(t, func(s net.Conn) {

		readLine(t, s)
		s.Write([]byte(protocolInfoLine("SAFECOOKIE,HASHEDPASSWORD", cookiePath)))

		line := readLine(t, s)
		nonceHex := strings.TrimSuffix(strings.TrimPrefix(line, "AUTHCHALLENGE SAFECOOKIE "), "\r\n")
		clientNonce, err := hex.DecodeString(nonceHex)
		if err != nil {
			t.Error(err)
			return
		}
		mac := hmac.New(sha256.New, []byte(safeCookieServerKey))
		mac.Write(cookie)
		mac.Write(clientNonce)
		mac.Write(serverNonce)
		s.Write([]byte("250 AUTHCHALLENGE SERVERHASH=" + hex.EncodeToString(mac.Sum(nil)) +
			" SERVERNONCE=" + hex.EncodeToString(serverNonce) + "\r\n"))

		readLine(t, s)
		s.Write([]byte("250 OK\r\n"))
	})
	defer c.Close()

	got, err := c.Authenticate("ignored-because-safecookie-wins")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got != MethodSafeCookie {
		t.Errorf("method = %q, want %q", got, MethodSafeCookie)
	}
}

func TestAuthenticate_FallsBackToPasswordOnCookieReadError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-cookie")

	c := scriptedTor(t, []step{
		{"PROTOCOLINFO", protocolInfoLine("SAFECOOKIE,HASHEDPASSWORD", missing)},
		{"AUTHENTICATE " + hex.EncodeToString([]byte("swordfish")), "250 OK\r\n"},
	})
	defer c.Close()

	got, err := c.Authenticate("swordfish")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got != MethodHashedPassword {
		t.Errorf("method = %q, want %q", got, MethodHashedPassword)
	}
}

func TestAuthenticate_PasswordOnly(t *testing.T) {
	c := scriptedTor(t, []step{
		{"PROTOCOLINFO", protocolInfoLine("HASHEDPASSWORD", "")},
		{"AUTHENTICATE " + hex.EncodeToString([]byte("p4ss")), "250 OK\r\n"},
	})
	defer c.Close()

	got, err := c.Authenticate("p4ss")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got != MethodHashedPassword {
		t.Errorf("method = %q, want %q", got, MethodHashedPassword)
	}
}

func TestAuthenticate_PasswordRequiredButEmpty(t *testing.T) {
	c := scriptedTor(t, []step{
		{"PROTOCOLINFO", protocolInfoLine("HASHEDPASSWORD", "")},
	})
	defer c.Close()

	_, err := c.Authenticate("")
	if err == nil || !strings.Contains(err.Error(), "no password is configured") {
		t.Fatalf("err = %v, want password-required", err)
	}
}

func TestAuthenticate_NullWhenAllowed(t *testing.T) {
	c := scriptedTor(t, []step{
		{"PROTOCOLINFO", protocolInfoLine("NULL", "")},
		{"AUTHENTICATE\r\n", "250 OK\r\n"},
	})
	defer c.Close()

	got, err := c.Authenticate("")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got != MethodNull {
		t.Errorf("method = %q, want %q", got, MethodNull)
	}
}

func TestAuthenticate_PlainCookie(t *testing.T) {
	cookiePath := writeCookie(t)
	cookie, _ := os.ReadFile(cookiePath)

	c := scriptedTor(t, []step{
		{"PROTOCOLINFO", protocolInfoLine("COOKIE", cookiePath)},
		{"AUTHENTICATE " + hex.EncodeToString(cookie), "250 OK\r\n"},
	})
	defer c.Close()

	got, err := c.Authenticate("")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got != MethodCookie {
		t.Errorf("method = %q, want %q", got, MethodCookie)
	}
}

func TestAuthenticate_NoUsableMethod(t *testing.T) {
	c := scriptedTor(t, []step{
		{"PROTOCOLINFO", protocolInfoLine("SOMETHING-FUTURE", "")},
	})
	defer c.Close()

	_, err := c.Authenticate("")
	if err == nil || !strings.Contains(err.Error(), "no usable auth method") {
		t.Fatalf("err = %v, want no-usable-method", err)
	}
}

func TestAuthenticate_SafeCookieHashMismatchSurfacesWhenNoFallback(t *testing.T) {
	cookiePath := writeCookie(t)
	c := mockTor(t, func(s net.Conn) {
		readLine(t, s)
		s.Write([]byte(protocolInfoLine("SAFECOOKIE", cookiePath)))
		readLine(t, s)

		bad := make([]byte, 32)
		s.Write([]byte("250 AUTHCHALLENGE SERVERHASH=" + hex.EncodeToString(bad) +
			" SERVERNONCE=" + hex.EncodeToString(bad) + "\r\n"))
	})
	defer c.Close()

	_, err := c.Authenticate("")
	if err == nil || !strings.Contains(err.Error(), "SAFECOOKIE") {
		t.Fatalf("err = %v, want SAFECOOKIE error", err)
	}
}
