package pair

import (
	"bytes"
	"crypto/rand"
	"strings"
	"testing"
)

func TestHandshakeMAC_RoundTrip(t *testing.T) {
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	body := []byte("opaque inviter payload bytes")
	mac := InviterHandshakeMAC(key, body)
	if !VerifyInviterHandshakeMAC(key, body, mac) {
		t.Errorf("inviter MAC verify failed")
	}
}

func TestHandshakeMAC_DirectionsAreDistinct(t *testing.T) {
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	body := []byte("hello")
	jm := JoinerHandshakeMAC(key, body)
	im := InviterHandshakeMAC(key, body)
	if jm == im {
		t.Errorf("joiner and inviter MACs collided for same body — DST broken")
	}

	if VerifyInviterHandshakeMAC(key, body, jm) {
		t.Errorf("inviter verify accepted joiner-side MAC — replay defence broken")
	}
	if VerifyJoinerHandshakeMAC(key, body, im) {
		t.Errorf("joiner verify accepted inviter-side MAC — replay defence broken")
	}
}

func TestHandshakeMAC_RejectsTamperedBody(t *testing.T) {
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	body := []byte("original payload")
	mac := JoinerHandshakeMAC(key, body)
	tampered := bytes.Repeat([]byte("x"), len(body))
	if VerifyJoinerHandshakeMAC(key, tampered, mac) {
		t.Errorf("verify accepted tampered body")
	}
}

func TestHandshakeMAC_HexCaseInsensitive(t *testing.T) {
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	body := []byte("body")
	mac := JoinerHandshakeMAC(key, body)
	upper := strings.ToUpper(mac)
	if !VerifyJoinerHandshakeMAC(key, body, upper) {
		t.Errorf("verify rejected uppercase hex form")
	}
}

func TestHandshakeMAC_DifferentKeyFails(t *testing.T) {
	var k1, k2 [32]byte
	if _, err := rand.Read(k1[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(k2[:]); err != nil {
		t.Fatal(err)
	}
	body := []byte("payload")
	mac := JoinerHandshakeMAC(k1, body)
	if VerifyJoinerHandshakeMAC(k2, body, mac) {
		t.Errorf("verify accepted MAC under wrong key")
	}
}
