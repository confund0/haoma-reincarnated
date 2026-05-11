package pair

import (
	"bytes"
	"testing"
)

func TestSeedToEd25519_Deterministic(t *testing.T) {
	idEnt, err := DecodeWords([]string{"abandon", "ability", "about"}, 33)
	if err != nil {
		t.Fatal(err)
	}
	priv1, pub1, err := seedToEd25519(idEnt)
	if err != nil {
		t.Fatal(err)
	}
	priv2, pub2, err := seedToEd25519(idEnt)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(priv1, priv2) {
		t.Error("same idEntropy produced different private keys")
	}
	if !bytes.Equal(pub1, pub2) {
		t.Error("same idEntropy produced different public keys")
	}
}

func TestSeedToEd25519_DifferentIds_DifferentKeys(t *testing.T) {
	a, _ := DecodeWords([]string{"abandon", "ability", "about"}, 33)
	b, _ := DecodeWords([]string{"abandon", "ability", "above"}, 33)
	_, pubA, err := seedToEd25519(a)
	if err != nil {
		t.Fatal(err)
	}
	_, pubB, err := seedToEd25519(b)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(pubA, pubB) {
		t.Error("different ids collapsed to the same ed25519 pubkey")
	}
}

func TestSeedToEd25519_RejectsEmptyEntropy(t *testing.T) {
	_, _, err := seedToEd25519(nil)
	if err == nil {
		t.Fatal("expected error on empty entropy")
	}
}
