package bencode_test

import (
	"bytes"
	"testing"

	"haoma/internal/bencode"
)

var fuzzSeeds = [][]byte{
	{},
	[]byte("i0e"),
	[]byte("i-1e"),
	[]byte("0:"),
	[]byte("4:spam"),
	[]byte("l4:spam4:eggse"),
	[]byte("d3:cow3:moo4:spam4:eggse"),
	[]byte("hello, world — with non-ascii: αβγ ✓"),
}

func FuzzUnmarshal(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var v any
		_ = bencode.Unmarshal(data, &v)
	})
}

func FuzzRoundTrip(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		encoded, err := bencode.Marshal(data)
		if err != nil {
			return
		}
		var decoded []byte
		if err := bencode.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("unmarshal after successful marshal: %v", err)
		}
		if !bytes.Equal(decoded, data) {
			t.Fatalf("round-trip mismatch:\n  in:  %q\n  out: %q", data, decoded)
		}
	})
}
