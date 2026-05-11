package xport

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
)

var ErrMacMismatch = errors.New("xport: envelope MAC mismatch")

func Sign(env Envelope, secret []byte) Envelope {
	env.Mac = computeMac(env, secret)
	return env
}

func Verify(env Envelope, secret []byte) error {
	want := computeMac(env, secret)
	if subtle.ConstantTimeCompare(want, env.Mac) != 1 {
		return ErrMacMismatch
	}
	return nil
}

func computeMac(env Envelope, secret []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	var lenBuf [4]byte
	var tsBuf [8]byte

	writeLP := func(b []byte) {
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(b)))
		mac.Write(lenBuf[:])
		mac.Write(b)
	}

	writeLP([]byte(env.ID))

	binary.BigEndian.PutUint64(tsBuf[:], uint64(env.Timestamp))
	mac.Write(tsBuf[:])

	writeLP([]byte(env.From))

	writeLP([]byte(env.Kind))

	writeLP([]byte(env.PresenceSource))

	writeLP(env.Payload)

	writeLP(env.Padding)

	return mac.Sum(nil)
}
