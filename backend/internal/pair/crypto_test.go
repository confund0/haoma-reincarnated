package pair

import (
	"bytes"
	"testing"
)

func init() {

	KDFParams.Time = 1
	KDFParams.Memory = 8 * 1024
}

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	idEnt, err := DecodeWords([]string{"abandon", "ability", "about"}, 33)
	if err != nil {
		t.Fatal(err)
	}
	passEnt, err := DecodeWords([]string{"above", "absent", "absorb", "abstract"}, 44)
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte(`{"onion_url":"http://bob.onion","guid":"abc123","expires_at":1900000000}`)
	ct, err := EncryptBootstrap(plaintext, passEnt, idEnt)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := DecryptBootstrap(ct, passEnt, idEnt)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pt, plaintext) {
		t.Errorf("round-trip mismatch: got %q want %q", pt, plaintext)
	}
}

func TestDecrypt_WrongPassphrase_Fails(t *testing.T) {
	idEnt, _ := DecodeWords([]string{"abandon", "ability", "about"}, 33)
	passEnt, _ := DecodeWords([]string{"above", "absent", "absorb", "abstract"}, 44)
	wrongPass, _ := DecodeWords([]string{"above", "absent", "absorb", "abuse"}, 44)
	ct, err := EncryptBootstrap([]byte("secret"), passEnt, idEnt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecryptBootstrap(ct, wrongPass, idEnt); err != ErrDecrypt {
		t.Errorf("err = %v, want ErrDecrypt", err)
	}
}

func TestDecrypt_WrongID_Fails(t *testing.T) {
	idEnt, _ := DecodeWords([]string{"abandon", "ability", "about"}, 33)
	wrongID, _ := DecodeWords([]string{"abandon", "ability", "above"}, 33)
	passEnt, _ := DecodeWords([]string{"above", "absent", "absorb", "abstract"}, 44)
	ct, err := EncryptBootstrap([]byte("secret"), passEnt, idEnt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecryptBootstrap(ct, passEnt, wrongID); err != ErrDecrypt {
		t.Errorf("err = %v, want ErrDecrypt", err)
	}
}

func TestDecrypt_TamperedCiphertext_Fails(t *testing.T) {
	idEnt, _ := DecodeWords([]string{"abandon", "ability", "about"}, 33)
	passEnt, _ := DecodeWords([]string{"above", "absent", "absorb", "abstract"}, 44)
	ct, err := EncryptBootstrap([]byte("secret"), passEnt, idEnt)
	if err != nil {
		t.Fatal(err)
	}
	ct[len(ct)-1] ^= 0x01
	if _, err := DecryptBootstrap(ct, passEnt, idEnt); err != ErrDecrypt {
		t.Errorf("err = %v, want ErrDecrypt", err)
	}
}

func TestDecrypt_TooShort_Fails(t *testing.T) {
	passEnt, _ := DecodeWords([]string{"above", "absent", "absorb", "abstract"}, 44)
	idEnt, _ := DecodeWords([]string{"abandon", "ability", "about"}, 33)
	if _, err := DecryptBootstrap([]byte{1, 2, 3}, passEnt, idEnt); err != ErrDecrypt {
		t.Errorf("err = %v, want ErrDecrypt", err)
	}
}
