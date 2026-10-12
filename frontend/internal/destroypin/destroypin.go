package destroypin

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"

	"golang.org/x/crypto/argon2"
)

type Params struct {
	Time    uint32
	Memory  uint32
	Threads uint8
	KeyLen  uint32
	SaltLen int
}

var DefaultParams = Params{
	Time:    4,
	Memory:  256 * 1024,
	Threads: 4,
	KeyLen:  32,
	SaltLen: 16,
}

var ErrPINShape = errors.New("destroypin: a destroy PIN is four digits")

const pinLength = 4

func ValidatePIN(pin string) error {
	if len(pin) != pinLength {
		return ErrPINShape
	}
	for i := 0; i < len(pin); i++ {
		if pin[i] < '0' || pin[i] > '9' {
			return ErrPINShape
		}
	}
	return nil
}

func NewSalt() ([]byte, error) {
	return NewSaltWith(DefaultParams)
}

func NewSaltWith(p Params) ([]byte, error) {
	b := make([]byte, p.SaltLen)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("destroypin: salt: %w", err)
	}
	return b, nil
}

func Derive(pin string, salt []byte) ([]byte, error) {
	return DeriveWith(DefaultParams, pin, salt)
}

func DeriveWith(p Params, pin string, salt []byte) ([]byte, error) {
	if err := ValidatePIN(pin); err != nil {
		return nil, err
	}
	if len(salt) == 0 {
		return nil, errors.New("destroypin: no salt")
	}
	return argon2.IDKey([]byte(pin), salt, p.Time, p.Memory, p.Threads, p.KeyLen), nil
}

func Verifier(proof []byte) []byte {
	sum := sha256.Sum256(proof)
	return sum[:]
}

func Zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
