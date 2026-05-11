package pair

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"

	"golang.org/x/crypto/argon2"
)

var KDFParams = struct {
	Time    uint32
	Memory  uint32
	Threads uint8
	KeyLen  uint32
}{
	Time:    3,
	Memory:  64 * 1024,
	Threads: 2,
	KeyLen:  32,
}

func EncryptBootstrap(plaintext, passphraseEntropy, idEntropy []byte) ([]byte, error) {
	key := deriveKey(passphraseEntropy, idEntropy)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("pair: aes new: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("pair: gcm new: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("pair: gcm nonce: %w", err)
	}
	out := make([]byte, 0, len(nonce)+len(plaintext)+gcm.Overhead())
	out = append(out, nonce...)
	out = gcm.Seal(out, nonce, plaintext, nil)
	return out, nil
}

func DecryptBootstrap(ciphertext, passphraseEntropy, idEntropy []byte) ([]byte, error) {
	key := deriveKey(passphraseEntropy, idEntropy)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("pair: aes new: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("pair: gcm new: %w", err)
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, ErrDecrypt
	}
	nonce, ct := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}

var ErrDecrypt = errors.New("pair: bootstrap decrypt failed (bad passphrase, bad id, or tampered)")

func deriveKey(passphraseEntropy, idEntropy []byte) []byte {
	salt := sha256.Sum256(idEntropy)
	return argon2.IDKey(
		passphraseEntropy,
		salt[:],
		KDFParams.Time,
		KDFParams.Memory,
		KDFParams.Threads,
		KDFParams.KeyLen,
	)
}
