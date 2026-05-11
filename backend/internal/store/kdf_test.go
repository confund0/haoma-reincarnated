package store

import (
	"bytes"
	"testing"
)

func testParams() KDFParams {
	p := DefaultKDFParams
	p.Memory = 8 * 1024
	p.Time = 1
	return p
}

func TestDeriveKey_Deterministic(t *testing.T) {
	salt := []byte("0123456789abcdef")
	k1 := deriveKey("correct horse battery staple", salt, testParams())
	k2 := deriveKey("correct horse battery staple", salt, testParams())
	if !bytes.Equal(k1, k2) {
		t.Fatalf("same inputs produced different keys")
	}
}

func TestDeriveKey_KeyLength(t *testing.T) {
	salt := []byte("0123456789abcdef")
	k := deriveKey("pw", salt, testParams())
	if len(k) != 32 {
		t.Fatalf("key length = %d, want 32", len(k))
	}
}

func TestDeriveKey_DifferentPassphrase(t *testing.T) {
	salt := []byte("0123456789abcdef")
	k1 := deriveKey("alpha", salt, testParams())
	k2 := deriveKey("beta", salt, testParams())
	if bytes.Equal(k1, k2) {
		t.Fatalf("different passphrases produced identical keys")
	}
}

func TestDeriveKey_DifferentSalt(t *testing.T) {
	k1 := deriveKey("pw", []byte("AAAAAAAAAAAAAAAA"), testParams())
	k2 := deriveKey("pw", []byte("BBBBBBBBBBBBBBBB"), testParams())
	if bytes.Equal(k1, k2) {
		t.Fatalf("different salts produced identical keys")
	}
}

func TestDefaultKDFParams_Sane(t *testing.T) {
	p := DefaultKDFParams
	if p.Time == 0 || p.Memory == 0 || p.Threads == 0 {
		t.Fatalf("default params have zero fields: %+v", p)
	}
	if p.KeyLen != 32 {
		t.Fatalf("KeyLen = %d, want 32 (AES-256)", p.KeyLen)
	}
	if p.SaltLen < 16 {
		t.Fatalf("SaltLen = %d, want >= 16", p.SaltLen)
	}
}

func TestNewSalt_LengthAndEntropy(t *testing.T) {
	s1, err := newSalt(testParams())
	if err != nil {
		t.Fatalf("newSalt: %v", err)
	}
	if len(s1) != testParams().SaltLen {
		t.Fatalf("salt length = %d, want %d", len(s1), testParams().SaltLen)
	}
	s2, err := newSalt(testParams())
	if err != nil {
		t.Fatalf("newSalt: %v", err)
	}
	if bytes.Equal(s1, s2) {
		t.Fatalf("two newSalt calls returned identical bytes (entropy failure)")
	}
}
