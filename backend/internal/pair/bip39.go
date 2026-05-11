package pair

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"

	"github.com/tyler-smith/go-bip39/wordlists"
)

var English = wordlists.English

var wordIndex = func() map[string]int {
	m := make(map[string]int, len(English))
	for i, w := range English {
		m[w] = i
	}
	return m
}()

type ErrInvalidWord struct{ Word string }

func (e ErrInvalidWord) Error() string { return "pair: invalid bip39 word: " + e.Word }

type ErrWordCount struct{ Got, Want int }

func (e ErrWordCount) Error() string {
	return fmt.Sprintf("pair: word count mismatch: got %d, want %d", e.Got, e.Want)
}

func EncodeWords(entropy []byte, bits int) ([]string, error) {
	if bits <= 0 {
		return nil, errors.New("pair: bits must be positive")
	}
	if bits > 8*len(entropy) {
		return nil, fmt.Errorf("pair: bits=%d exceeds available entropy of %d bytes", bits, len(entropy))
	}
	words := (bits + 10) / 11
	out := make([]string, words)
	for i := 0; i < words; i++ {

		idx := readBits(entropy, i*11, 11)
		out[i] = English[idx]
	}
	return out, nil
}

func DecodeWords(words []string, bits int) ([]byte, error) {
	if bits <= 0 {
		return nil, errors.New("pair: bits must be positive")
	}
	want := (bits + 10) / 11
	if len(words) != want {
		return nil, ErrWordCount{Got: len(words), Want: want}
	}
	byteLen := (bits + 7) / 8
	out := make([]byte, byteLen)
	for i, w := range words {
		idx, ok := wordIndex[strings.ToLower(w)]
		if !ok {
			return nil, ErrInvalidWord{Word: w}
		}
		writeBits(out, i*11, 11, idx)
	}
	return out, nil
}

func RandomWords(bits int) ([]string, error) {
	if bits <= 0 {
		return nil, errors.New("pair: bits must be positive")
	}
	byteLen := (bits + 7) / 8
	ent := make([]byte, byteLen)
	if _, err := rand.Read(ent); err != nil {
		return nil, fmt.Errorf("pair: random entropy: %w", err)
	}

	if rem := 8*byteLen - bits; rem > 0 {
		ent[byteLen-1] &^= byte((1 << rem) - 1)
	}
	return EncodeWords(ent, bits)
}

func ParseWords(s string) []string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return nil
	}
	out := make([]string, len(f))
	for i, w := range f {
		out[i] = strings.ToLower(w)
	}
	return out
}

func readBits(b []byte, off, n int) int {
	var v int
	for i := 0; i < n; i++ {
		bitPos := off + i
		byteIdx := bitPos / 8
		bitIdx := 7 - (bitPos % 8)
		bit := int((b[byteIdx] >> bitIdx) & 1)
		v = (v << 1) | bit
	}
	return v
}

func writeBits(b []byte, off, n, v int) {
	for i := 0; i < n; i++ {
		bitPos := off + i
		byteIdx := bitPos / 8
		bitIdx := 7 - (bitPos % 8)
		bit := byte((v >> (n - 1 - i)) & 1)
		b[byteIdx] &^= 1 << bitIdx
		b[byteIdx] |= bit << bitIdx
	}
}
