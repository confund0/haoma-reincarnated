package pair

import (
	"testing"
)

func TestEncodeDecode_3Words_RoundTrip(t *testing.T) {

	for i := 0; i < 100; i++ {
		words, err := RandomWords(33)
		if err != nil {
			t.Fatal(err)
		}
		if len(words) != 3 {
			t.Fatalf("got %d words, want 3", len(words))
		}
		ent, err := DecodeWords(words, 33)
		if err != nil {
			t.Fatalf("decode %v: %v", words, err)
		}
		got, err := EncodeWords(ent, 33)
		if err != nil {
			t.Fatal(err)
		}
		for j := range words {
			if words[j] != got[j] {
				t.Errorf("round-trip drift at index %d: %q -> %q", j, words[j], got[j])
			}
		}
	}
}

func TestEncodeDecode_4Words_RoundTrip(t *testing.T) {

	for i := 0; i < 100; i++ {
		words, err := RandomWords(44)
		if err != nil {
			t.Fatal(err)
		}
		if len(words) != 4 {
			t.Fatalf("got %d words, want 4", len(words))
		}
		ent, err := DecodeWords(words, 44)
		if err != nil {
			t.Fatal(err)
		}
		got, err := EncodeWords(ent, 44)
		if err != nil {
			t.Fatal(err)
		}
		for j := range words {
			if words[j] != got[j] {
				t.Errorf("round-trip drift at index %d: %q -> %q", j, words[j], got[j])
			}
		}
	}
}

func TestDecodeWords_RejectsUnknownWord(t *testing.T) {
	_, err := DecodeWords([]string{"alice", "bob", "notbip39"}, 33)
	if err == nil {
		t.Fatal("expected ErrInvalidWord")
	}
	if _, ok := err.(ErrInvalidWord); !ok {
		t.Errorf("err type = %T, want ErrInvalidWord", err)
	}
}

func TestDecodeWords_RejectsWrongCount(t *testing.T) {
	_, err := DecodeWords([]string{"abandon", "ability"}, 33)
	if err == nil {
		t.Fatal("expected ErrWordCount")
	}
	if _, ok := err.(ErrWordCount); !ok {
		t.Errorf("err type = %T, want ErrWordCount", err)
	}
}

func TestDecodeWords_CaseInsensitive(t *testing.T) {
	words := []string{"abandon", "ability", "about"}
	lowerEnt, err := DecodeWords(words, 33)
	if err != nil {
		t.Fatal(err)
	}
	upper := []string{"Abandon", "ABILITY", "About"}
	upperEnt, err := DecodeWords(upper, 33)
	if err != nil {
		t.Fatalf("uppercase round-trip: %v", err)
	}
	if string(lowerEnt) != string(upperEnt) {
		t.Errorf("case insensitivity broken: %x vs %x", lowerEnt, upperEnt)
	}
}

func TestEncodeWords_AllZeroEntropy(t *testing.T) {
	entropy := []byte{0, 0, 0, 0, 0}
	words, err := EncodeWords(entropy, 33)
	if err != nil {
		t.Fatal(err)
	}
	for i, w := range words {
		if w != "abandon" {
			t.Errorf("word %d = %q, want abandon", i, w)
		}
	}
}

func TestEncodeWords_LastWord(t *testing.T) {

	entropy := []byte{0xff, 0xe0, 0x00, 0x00, 0x00}
	words, err := EncodeWords(entropy, 33)
	if err != nil {
		t.Fatal(err)
	}
	if words[0] != "zoo" {
		t.Errorf("words[0] = %q, want zoo (entropy=%x)", words[0], entropy)
	}
}

func TestParseWords(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"alice bob carol", []string{"alice", "bob", "carol"}},
		{"  alice   bob\tcarol\n", []string{"alice", "bob", "carol"}},
		{"Alice BOB Carol", []string{"alice", "bob", "carol"}},
		{"", nil},
	}
	for _, c := range cases {
		got := ParseWords(c.in)
		if len(got) != len(c.want) {
			t.Errorf("ParseWords(%q) len = %d, want %d", c.in, len(got), len(c.want))
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("ParseWords(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}
