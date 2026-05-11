package pair

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/sha3"
)

const OnionWordCount = 7

const onionRendezvousDST = "haoma-pair-rendezvous-v2/"

const (
	onionPortDST = "haoma-pair-port-v2/"
	onionHMACDST = "haoma-pair-hmac-v2/"
)

const (
	OnionPortMin = 1025
	OnionPortMax = 30000
)

type OnionMaterial struct {
	Words []string

	Seed [32]byte

	PublicKey ed25519.PublicKey

	PrivateKey ed25519.PrivateKey

	TorExpandedKey [64]byte

	TorExpandedKeyB64 string

	OnionAddress string

	Port int

	HandoffHMAC [32]byte
}

func (m *OnionMaterial) Wipe() {
	if m == nil {
		return
	}
	for i := range m.Seed {
		m.Seed[i] = 0
	}
	for i := range m.PrivateKey {
		m.PrivateKey[i] = 0
	}
	for i := range m.TorExpandedKey {
		m.TorExpandedKey[i] = 0
	}
	for i := range m.HandoffHMAC {
		m.HandoffHMAC[i] = 0
	}
	m.TorExpandedKeyB64 = ""
}

func GenerateOnionWords() ([]string, error) {
	out := make([]string, OnionWordCount)
	for i := range out {
		idx, err := uniformIndex(uint32(EFFShortCount))
		if err != nil {
			return nil, fmt.Errorf("pair: random word index: %w", err)
		}
		out[i] = EFFShort[idx]
	}
	return out, nil
}

func OnionDerive(words []string) (*OnionMaterial, error) {
	canonical, err := ValidateEFFShortPhrase(words)
	if err != nil {
		return nil, err
	}
	if len(canonical) != OnionWordCount {
		return nil, fmt.Errorf("pair: onion phrase has %d words, want %d", len(canonical), OnionWordCount)
	}

	h := sha256.New()
	h.Write([]byte(onionRendezvousDST))
	h.Write([]byte(strings.Join(canonical, " ")))
	var seed [32]byte
	copy(seed[:], h.Sum(nil))

	priv := ed25519.NewKeyFromSeed(seed[:])
	pub := priv.Public().(ed25519.PublicKey)

	exp := torExpandedKey(seed)

	addr := v3OnionAddress(pub)

	port := derivePort(seed)

	var hmacKey [32]byte
	copy(hmacKey[:], deriveDST(seed[:], onionHMACDST))

	return &OnionMaterial{
		Words:             canonical,
		Seed:              seed,
		PublicKey:         pub,
		PrivateKey:        priv,
		TorExpandedKey:    exp,
		TorExpandedKeyB64: base64.StdEncoding.EncodeToString(exp[:]),
		OnionAddress:      addr,
		Port:              port,
		HandoffHMAC:       hmacKey,
	}, nil
}

func torExpandedKey(seed [32]byte) [64]byte {
	h := sha512.Sum512(seed[:])

	h[0] &= 248
	h[31] &= 127
	h[31] |= 64
	return h
}

func v3OnionAddress(pub ed25519.PublicKey) string {
	const version byte = 0x03
	h := sha3.New256()
	h.Write([]byte(".onion checksum"))
	h.Write(pub)
	h.Write([]byte{version})
	checksum := h.Sum(nil)[:2]
	buf := make([]byte, 0, 35)
	buf = append(buf, pub...)
	buf = append(buf, checksum...)
	buf = append(buf, version)
	return strings.ToLower(base32.StdEncoding.EncodeToString(buf))
}

func derivePort(seed [32]byte) int {
	raw := deriveDST(seed[:], onionPortDST)
	span := uint32(OnionPortMax - OnionPortMin + 1)
	n := binary.BigEndian.Uint32(raw[:4]) % span
	return OnionPortMin + int(n)
}

func deriveDST(data []byte, dst string) []byte {
	h := sha256.New()
	h.Write([]byte(dst))
	h.Write(data)
	return h.Sum(nil)
}

func uniformIndex(n uint32) (uint32, error) {
	if n == 0 {
		return 0, errors.New("pair: uniformIndex n=0")
	}
	threshold := -n % n
	var buf [4]byte
	for {
		if _, err := rand.Read(buf[:]); err != nil {
			return 0, err
		}
		v := binary.BigEndian.Uint32(buf[:])
		if v >= threshold {
			return v % n, nil
		}
	}
}
