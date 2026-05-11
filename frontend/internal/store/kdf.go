package store

import (
	"crypto/rand"

	"golang.org/x/crypto/argon2"
)

type KDFParams struct {
	Time    uint32 `json:"time"`
	Memory  uint32 `json:"memory_kib"`
	Threads uint8  `json:"threads"`
	KeyLen  uint32 `json:"key_len"`
	SaltLen int    `json:"salt_len"`
}

var DefaultKDFParams = KDFParams{
	Time:    4,
	Memory:  256 * 1024,
	Threads: 4,
	KeyLen:  32,
	SaltLen: 16,
}

func deriveKey(passphrase string, salt []byte, p KDFParams) []byte {
	return argon2.IDKey([]byte(passphrase), salt, p.Time, p.Memory, p.Threads, p.KeyLen)
}

func newSalt(p KDFParams) ([]byte, error) {
	b := make([]byte, p.SaltLen)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}
