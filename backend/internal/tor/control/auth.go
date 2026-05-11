package control

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	safeCookieServerKey = "Tor safe cookie authentication server-to-controller hash"
	safeCookieClientKey = "Tor safe cookie authentication controller-to-server hash"
	cookieLen           = 32
)

func (c *Conn) AuthNull() error {
	reply, err := c.cmd("AUTHENTICATE")
	if err != nil {
		return err
	}
	return checkAuthReply(reply)
}

func (c *Conn) AuthPassword(password string) error {
	reply, err := c.cmd("AUTHENTICATE " + hex.EncodeToString([]byte(password)))
	if err != nil {
		return err
	}
	return checkAuthReply(reply)
}

func (c *Conn) AuthCookie(cookiePath string) error {
	cookie, err := os.ReadFile(cookiePath)
	if err != nil {
		return fmt.Errorf("control: read cookie %q: %w", cookiePath, err)
	}
	if len(cookie) != cookieLen {
		return fmt.Errorf("control: cookie %q is %d bytes, want %d", cookiePath, len(cookie), cookieLen)
	}
	reply, err := c.cmd("AUTHENTICATE " + hex.EncodeToString(cookie))
	if err != nil {
		return err
	}
	return checkAuthReply(reply)
}

func (c *Conn) AuthSafeCookie(cookiePath string) error {
	cookie, err := os.ReadFile(cookiePath)
	if err != nil {
		return fmt.Errorf("control: read cookie %q: %w", cookiePath, err)
	}
	if len(cookie) != cookieLen {
		return fmt.Errorf("control: cookie %q is %d bytes, want %d", cookiePath, len(cookie), cookieLen)
	}

	var clientNonce [32]byte
	if _, err := rand.Read(clientNonce[:]); err != nil {
		return err
	}

	reply, err := c.cmd("AUTHCHALLENGE SAFECOOKIE " + hex.EncodeToString(clientNonce[:]))
	if err != nil {
		return err
	}
	if reply.Code != 250 {
		return fmt.Errorf("control: AUTHCHALLENGE: %d %s", reply.Code, strings.Join(reply.Lines, " "))
	}
	if len(reply.Lines) == 0 || !strings.HasPrefix(reply.Lines[0], "AUTHCHALLENGE ") {
		return fmt.Errorf("control: unexpected AUTHCHALLENGE reply: %v", reply.Lines)
	}
	pairs, err := tokenKV(strings.TrimPrefix(reply.Lines[0], "AUTHCHALLENGE "))
	if err != nil {
		return err
	}
	serverHash, err := hex.DecodeString(pairs["SERVERHASH"])
	if err != nil {
		return fmt.Errorf("control: SERVERHASH decode: %w", err)
	}
	serverNonce, err := hex.DecodeString(pairs["SERVERNONCE"])
	if err != nil {
		return fmt.Errorf("control: SERVERNONCE decode: %w", err)
	}

	if !hmac.Equal(serverHash, safeCookieHMAC([]byte(safeCookieServerKey), cookie, clientNonce[:], serverNonce)) {
		return errors.New("control: SAFECOOKIE server hash mismatch")
	}

	clientHash := safeCookieHMAC([]byte(safeCookieClientKey), cookie, clientNonce[:], serverNonce)

	reply, err = c.cmd("AUTHENTICATE " + hex.EncodeToString(clientHash))
	if err != nil {
		return err
	}
	return checkAuthReply(reply)
}

func safeCookieHMAC(key, cookie, clientNonce, serverNonce []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(cookie)
	mac.Write(clientNonce)
	mac.Write(serverNonce)
	return mac.Sum(nil)
}

func checkAuthReply(r Reply) error {
	if r.Code != 250 {
		return fmt.Errorf("control: AUTHENTICATE: %d %s", r.Code, strings.Join(r.Lines, " "))
	}
	return nil
}
