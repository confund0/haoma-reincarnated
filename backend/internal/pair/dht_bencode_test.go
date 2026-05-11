package pair

import (
	"bytes"
	"testing"

	"github.com/anacrolix/torrent/bencode"
)

func TestBencodeRoundTrip_Bytes(t *testing.T) {
	original := []byte("hello, world — with non-ascii: αβγ ✓")

	encoded, err := bencode.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded []byte
	if err := bencode.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !bytes.Equal(decoded, original) {
		t.Errorf("round-trip mismatch:\n  orig: %q\n  got:  %q", original, decoded)
	}
}

func TestBencodeRoundTrip_Empty(t *testing.T) {
	encoded, err := bencode.Marshal([]byte(nil))
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	var decoded []byte
	if err := bencode.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal empty: %v", err)
	}
	if len(decoded) != 0 {
		t.Errorf("expected 0-byte round-trip, got %d bytes: %q", len(decoded), decoded)
	}
}

func TestBencodeRoundTrip_AESGCMCiphertext(t *testing.T) {

	original := make([]byte, 256)
	for i := range original {
		original[i] = byte(i)
	}
	encoded, err := bencode.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded []byte
	if err := bencode.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !bytes.Equal(decoded, original) {
		t.Errorf("binary round-trip mismatch at first diff byte")
	}
}
