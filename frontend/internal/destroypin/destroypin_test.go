package destroypin_test

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"

	"haoma-frontend/internal/destroypin"
)

var cheap = destroypin.Params{Time: 1, Memory: 8 * 1024, Threads: 4, KeyLen: 32, SaltLen: 16}

func TestValidatePIN_ShapeRule(t *testing.T) {
	for _, ok := range []string{"1234", "0000", "9876"} {
		if err := destroypin.ValidatePIN(ok); err != nil {
			t.Errorf("ValidatePIN(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "123", "12345", "123456", "1234567", "12a4", "12 4", "-1234"} {
		if err := destroypin.ValidatePIN(bad); !errors.Is(err, destroypin.ErrPINShape) {
			t.Errorf("ValidatePIN(%q) = %v, want ErrPINShape", bad, err)
		}
	}
}

func TestValidatePIN_ErrorNeverCarriesThePIN(t *testing.T) {
	const pin = "8675309"
	err := destroypin.ValidatePIN(pin)
	if err == nil {
		t.Fatal("a 7-digit PIN was accepted")
	}
	if strings.Contains(err.Error(), pin) {
		t.Fatalf("the error carries the PIN: %v", err)
	}
}

func TestDerive_SameInputsSameValue(t *testing.T) {
	salt := []byte("0123456789abcdef")
	a, err := destroypin.DeriveWith(cheap, "1234", salt)
	if err != nil {
		t.Fatalf("DeriveWith: %v", err)
	}
	b, err := destroypin.DeriveWith(cheap, "1234", salt)
	if err != nil {
		t.Fatalf("DeriveWith: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("the same PIN and salt derived two different values")
	}
	if len(a) != int(cheap.KeyLen) {
		t.Fatalf("derived %d bytes, want %d", len(a), cheap.KeyLen)
	}
}

func TestDerive_SamePINDifferentSaltsDiffer(t *testing.T) {
	a, err := destroypin.DeriveWith(cheap, "1234", []byte("0123456789abcdef"))
	if err != nil {
		t.Fatalf("DeriveWith: %v", err)
	}
	b, err := destroypin.DeriveWith(cheap, "1234", []byte("fedcba9876543210"))
	if err != nil {
		t.Fatalf("DeriveWith: %v", err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("the same PIN under two salts derived the same value")
	}
}

func TestDerive_DifferentPINsDiffer(t *testing.T) {
	salt := []byte("0123456789abcdef")
	a, _ := destroypin.DeriveWith(cheap, "1234", salt)
	b, _ := destroypin.DeriveWith(cheap, "1235", salt)
	if bytes.Equal(a, b) {
		t.Fatal("two PINs derived the same value")
	}
}

func TestDerive_RefusesABadShapeAndAMissingSalt(t *testing.T) {
	if _, err := destroypin.DeriveWith(cheap, "abc", []byte("0123456789abcdef")); !errors.Is(err, destroypin.ErrPINShape) {
		t.Fatalf("err = %v, want ErrPINShape", err)
	}
	if _, err := destroypin.DeriveWith(cheap, "1234", nil); err == nil {
		t.Fatal("a derivation with no salt was accepted")
	}
}

func TestVerifier_IsNotTheProof(t *testing.T) {
	proof, err := destroypin.DeriveWith(cheap, "1234", []byte("0123456789abcdef"))
	if err != nil {
		t.Fatalf("DeriveWith: %v", err)
	}
	verifier := destroypin.Verifier(proof)
	if bytes.Equal(proof, verifier) {
		t.Fatal("the verifier equals the proof")
	}
	if len(verifier) != sha256.Size {
		t.Fatalf("verifier is %d bytes, want %d", len(verifier), sha256.Size)
	}

	if bytes.Equal(destroypin.Verifier(verifier), verifier) {
		t.Fatal("the verifier is a fixed point")
	}
}

func TestVerifier_IsWhatTheCheckCompares(t *testing.T) {
	salt := []byte("0123456789abcdef")
	stored, err := destroypin.DeriveWith(cheap, "1234", salt)
	if err != nil {
		t.Fatalf("DeriveWith: %v", err)
	}
	verifier := destroypin.Verifier(stored)

	right, err := destroypin.DeriveWith(cheap, "1234", salt)
	if err != nil {
		t.Fatalf("DeriveWith: %v", err)
	}
	if !bytes.Equal(destroypin.Verifier(right), verifier) {
		t.Fatal("the right PIN did not verify")
	}
	wrong, err := destroypin.DeriveWith(cheap, "1235", salt)
	if err != nil {
		t.Fatalf("DeriveWith: %v", err)
	}
	if bytes.Equal(destroypin.Verifier(wrong), verifier) {
		t.Fatal("a wrong PIN verified")
	}
}

func TestZero_WipesTheDerivedValue(t *testing.T) {
	k, err := destroypin.DeriveWith(cheap, "1234", []byte("0123456789abcdef"))
	if err != nil {
		t.Fatalf("DeriveWith: %v", err)
	}
	destroypin.Zero(k)
	for i, b := range k {
		if b != 0 {
			t.Fatalf("byte %d survived the wipe: %d", i, b)
		}
	}
}

func TestNewSalt_LengthAndFreshness(t *testing.T) {
	a, err := destroypin.NewSaltWith(cheap)
	if err != nil {
		t.Fatalf("NewSaltWith: %v", err)
	}
	if len(a) != cheap.SaltLen {
		t.Fatalf("salt is %d bytes, want %d", len(a), cheap.SaltLen)
	}
	b, err := destroypin.NewSaltWith(cheap)
	if err != nil {
		t.Fatalf("NewSaltWith: %v", err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("two salts came out identical")
	}
}
