package pair

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base32"
	"encoding/base64"
	"strings"
	"testing"
)

var fixedWords = []string{"acid", "acorn", "acre", "acts", "afar", "affix", "aged"}

func TestOnionDerive_Deterministic(t *testing.T) {
	a, err := OnionDerive(fixedWords)
	if err != nil {
		t.Fatalf("first derive: %v", err)
	}
	b, err := OnionDerive(fixedWords)
	if err != nil {
		t.Fatalf("second derive: %v", err)
	}
	if a.OnionAddress != b.OnionAddress {
		t.Errorf("address mismatch: %s vs %s", a.OnionAddress, b.OnionAddress)
	}
	if a.Port != b.Port {
		t.Errorf("port mismatch: %d vs %d", a.Port, b.Port)
	}
	if a.Seed != b.Seed {
		t.Errorf("seed mismatch")
	}
	if a.HandoffHMAC != b.HandoffHMAC {
		t.Errorf("hmac mismatch")
	}
	if a.TorExpandedKey != b.TorExpandedKey {
		t.Errorf("tor expanded key mismatch")
	}
}

func TestOnionDerive_NormalisesInput(t *testing.T) {
	mixed := []string{"  ACID ", "Acorn", "ACRE", "acts", "afar", "affix", "AGED"}
	a, err := OnionDerive(fixedWords)
	if err != nil {
		t.Fatal(err)
	}
	b, err := OnionDerive(mixed)
	if err != nil {
		t.Fatal(err)
	}
	if a.OnionAddress != b.OnionAddress {
		t.Errorf("normalisation broke determinism: %s vs %s", a.OnionAddress, b.OnionAddress)
	}
}

func TestOnionDerive_RejectsWrongWordCount(t *testing.T) {
	if _, err := OnionDerive(fixedWords[:6]); err == nil {
		t.Error("expected err for 6 words")
	}
	tooMany := append([]string{"abandon"}, fixedWords...)
	if _, err := OnionDerive(tooMany); err == nil {
		t.Error("expected err for 8 words")
	}
}

func TestOnionDerive_RejectsUnknownWord(t *testing.T) {
	bad := append([]string{}, fixedWords...)
	bad[3] = "definitelynotonthelist"
	_, err := OnionDerive(bad)
	if err == nil {
		t.Fatal("expected err")
	}
	if _, ok := err.(ErrInvalidEFFShortWord); !ok {
		t.Errorf("expected ErrInvalidEFFShortWord, got %T", err)
	}
}

func TestOnionDerive_OnionAddressShape(t *testing.T) {
	m, err := OnionDerive(fixedWords)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.OnionAddress) != 56 {
		t.Errorf("address length %d, want 56: %s", len(m.OnionAddress), m.OnionAddress)
	}

	for i, c := range m.OnionAddress {
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '2' && c <= '7':
		default:
			t.Errorf("char %d (%q) not lowercase base32", i, c)
		}
	}

	raw, err := base32.StdEncoding.DecodeString(strings.ToUpper(m.OnionAddress))
	if err != nil {
		t.Fatalf("base32 decode: %v", err)
	}
	if len(raw) != 35 {
		t.Fatalf("decoded len %d, want 35", len(raw))
	}
	if raw[34] != 0x03 {
		t.Errorf("version byte %x, want 03", raw[34])
	}

	if !bytes.Equal(raw[:32], m.PublicKey) {
		t.Errorf("decoded pubkey doesn't match m.PublicKey")
	}
}

func TestOnionDerive_PortInRange(t *testing.T) {

	phrases := [][]string{
		fixedWords,
		{"zoom", "zone", "zippy", "yo-yo", "yummy", "yoyo", "yelp"},
		{"acid", "zoom", "acre", "acts", "afar", "affix", "aged"},
	}

	safe := func(words []string) []string {
		out := make([]string, len(words))
		for i, w := range words {
			if !IsEFFShort(w) {
				out[i] = "acid"
			} else {
				out[i] = w
			}
		}
		return out
	}
	for _, ph := range phrases {
		m, err := OnionDerive(safe(ph))
		if err != nil {
			t.Fatalf("derive %v: %v", ph, err)
		}
		if m.Port < OnionPortMin || m.Port > OnionPortMax {
			t.Errorf("port %d out of [%d, %d] for %v", m.Port, OnionPortMin, OnionPortMax, ph)
		}
	}
}

func TestOnionDerive_TorExpandedKey(t *testing.T) {
	m, err := OnionDerive(fixedWords)
	if err != nil {
		t.Fatal(err)
	}

	if m.TorExpandedKey[0]&0x07 != 0 {
		t.Errorf("byte 0 low 3 bits not cleared: %02x", m.TorExpandedKey[0])
	}
	if m.TorExpandedKey[31]&0x80 != 0 {
		t.Errorf("byte 31 high bit not cleared: %02x", m.TorExpandedKey[31])
	}
	if m.TorExpandedKey[31]&0x40 == 0 {
		t.Errorf("byte 31 bit 6 not set: %02x", m.TorExpandedKey[31])
	}

	round, err := base64.StdEncoding.DecodeString(m.TorExpandedKeyB64)
	if err != nil {
		t.Fatalf("b64 decode: %v", err)
	}
	if !bytes.Equal(round, m.TorExpandedKey[:]) {
		t.Errorf("base64 round-trip mismatch")
	}
}

func TestOnionDerive_Ed25519PrivateKeyCanSign(t *testing.T) {
	m, err := OnionDerive(fixedWords)
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte("haoma onion derive smoke test")
	sig := ed25519.Sign(m.PrivateKey, msg)
	if !ed25519.Verify(m.PublicKey, msg, sig) {
		t.Error("public key didn't verify signature from private key")
	}
}

func TestGenerateOnionWords(t *testing.T) {
	a, err := GenerateOnionWords()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != OnionWordCount {
		t.Fatalf("got %d words, want %d", len(a), OnionWordCount)
	}
	for i, w := range a {
		if !IsEFFShort(w) {
			t.Errorf("word %d (%q) not in EFF-short", i, w)
		}
	}

	b, err := GenerateOnionWords()
	if err != nil {
		t.Fatal(err)
	}
	allEqual := true
	for i := range a {
		if a[i] != b[i] {
			allEqual = false
			break
		}
	}
	if allEqual {
		t.Errorf("two consecutive GenerateOnionWords draws produced identical phrases — RNG?")
	}
}

func TestOnionDerive_DifferentPhrasesProduceDifferentAddresses(t *testing.T) {
	a, err := OnionDerive(fixedWords)
	if err != nil {
		t.Fatal(err)
	}
	other := []string{"acid", "acorn", "acre", "acts", "afar", "affix", "agent"}
	b, err := OnionDerive(other)
	if err != nil {
		t.Fatal(err)
	}
	if a.OnionAddress == b.OnionAddress {
		t.Errorf("addresses collided across distinct phrases — DST broken?")
	}
	if a.Seed == b.Seed {
		t.Errorf("seeds collided")
	}
}

func TestOnionMaterial_Wipe(t *testing.T) {
	m, err := OnionDerive(fixedWords)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	addr := m.OnionAddress
	port := m.Port
	pubLen := len(m.PublicKey)

	m.Wipe()

	var zeroSeed [32]byte
	if m.Seed != zeroSeed {
		t.Errorf("Seed not wiped")
	}
	var zeroExp [64]byte
	if m.TorExpandedKey != zeroExp {
		t.Errorf("TorExpandedKey not wiped")
	}
	var zeroHM [32]byte
	if m.HandoffHMAC != zeroHM {
		t.Errorf("HandoffHMAC not wiped")
	}
	for i, b := range m.PrivateKey {
		if b != 0 {
			t.Errorf("PrivateKey[%d] = %#x, want 0", i, b)
			break
		}
	}
	if m.TorExpandedKeyB64 != "" {
		t.Errorf("TorExpandedKeyB64 = %q, want empty", m.TorExpandedKeyB64)
	}

	if m.OnionAddress != addr {
		t.Errorf("OnionAddress mutated: %q vs %q", m.OnionAddress, addr)
	}
	if m.Port != port {
		t.Errorf("Port mutated: %d vs %d", m.Port, port)
	}
	if len(m.PublicKey) != pubLen {
		t.Errorf("PublicKey length changed: %d vs %d", len(m.PublicKey), pubLen)
	}

	m.Wipe()
	(*OnionMaterial)(nil).Wipe()
}
