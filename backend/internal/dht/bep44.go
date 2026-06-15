package dht

import (
	"crypto/ed25519"
	"crypto/sha1"
	"fmt"

	"haoma/internal/bencode"
)

const (
	maxValueBytes = 1000
	maxSaltBytes  = 64
)

type MutableItem struct {
	PrivKey ed25519.PrivateKey
	PubKey  [32]byte
	Salt    []byte
	Seq     int64
	Value   []byte
	Sig     [64]byte
}

func (m *MutableItem) Target() ID {
	h := sha1.New()
	h.Write(m.PubKey[:])
	h.Write(m.Salt)
	var out ID
	copy(out[:], h.Sum(nil))
	return out
}

func (m *MutableItem) Sign() error {
	if m.PrivKey == nil {
		return fmt.Errorf("dht: sign: nil private key")
	}
	bv, err := bencode.Marshal(m.Value)
	if err != nil {
		return fmt.Errorf("dht: sign: bencode value: %w", err)
	}
	if len(bv) > maxValueBytes {
		return fmt.Errorf("dht: sign: bencoded value %d > %d", len(bv), maxValueBytes)
	}
	if len(m.Salt) > maxSaltBytes {
		return fmt.Errorf("dht: sign: salt %d > %d", len(m.Salt), maxSaltBytes)
	}
	sig := ed25519.Sign(m.PrivKey, bufferToSign(m.Salt, bv, m.Seq))
	copy(m.Sig[:], sig)

	if m.PubKey == ([32]byte{}) {
		pub, ok := m.PrivKey.Public().(ed25519.PublicKey)
		if !ok || len(pub) != 32 {
			return fmt.Errorf("dht: sign: bad private key")
		}
		copy(m.PubKey[:], pub)
	}
	return nil
}

func (m *MutableItem) Verify() error {
	bv, err := bencode.Marshal(m.Value)
	if err != nil {
		return fmt.Errorf("dht: verify: bencode value: %w", err)
	}
	if !ed25519.Verify(m.PubKey[:], bufferToSign(m.Salt, bv, m.Seq), m.Sig[:]) {
		return fmt.Errorf("dht: verify: signature mismatch")
	}
	return nil
}

func bufferToSign(salt, bv []byte, seq int64) []byte {
	var out []byte
	if len(salt) != 0 {
		out = append(out, "4:salt"...)
		out = append(out, bencode.MustMarshal(salt)...)
	}
	out = append(out, fmt.Sprintf("3:seqi%de1:v", seq)...)
	out = append(out, bv...)
	return out
}
