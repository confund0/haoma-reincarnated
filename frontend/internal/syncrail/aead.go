package syncrail

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

const HKDFInfo = "haoma/v1/sync"

var AAD = []byte("haoma/v1/sync/aad")

const KeySize = chacha20poly1305.KeySize

const NonceSize = chacha20poly1305.NonceSizeX

var ErrShortCiphertext = errors.New("syncrail: ciphertext shorter than nonce+tag")

var ErrEmptyIdentity = errors.New("syncrail: empty identity private key")

func DeriveKey(identityPriv []byte) ([]byte, error) {
	if len(identityPriv) == 0 {
		return nil, ErrEmptyIdentity
	}
	r := hkdf.New(sha256.New, identityPriv, nil, []byte(HKDFInfo))
	out := make([]byte, KeySize)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, fmt.Errorf("syncrail: hkdf: %w", err)
	}
	return out, nil
}

func Seal(key, plaintext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("syncrail: aead init: %w", err)
	}
	nonce := make([]byte, NonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("syncrail: draw nonce: %w", err)
	}
	out := make([]byte, 0, NonceSize+len(plaintext)+aead.Overhead())
	out = append(out, nonce...)
	return aead.Seal(out, nonce, plaintext, AAD), nil
}

func Open(key, ciphertext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("syncrail: aead init: %w", err)
	}
	if len(ciphertext) < NonceSize+aead.Overhead() {
		return nil, ErrShortCiphertext
	}
	nonce := ciphertext[:NonceSize]
	body := ciphertext[NonceSize:]
	plain, err := aead.Open(nil, nonce, body, AAD)
	if err != nil {
		return nil, fmt.Errorf("syncrail: aead open: %w", err)
	}
	return plain, nil
}
