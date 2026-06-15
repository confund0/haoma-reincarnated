package bencode_test

import (
	"bytes"
	"testing"

	"haoma/internal/bencode"
)

func TestRoundTrip_UTF8Bytes(t *testing.T) {
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
		t.Errorf("mismatch:\n  orig: %q\n  got:  %q", original, decoded)
	}
}

func TestRoundTrip_Empty(t *testing.T) {
	encoded, err := bencode.Marshal([]byte(nil))
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	var decoded []byte
	if err := bencode.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal empty: %v", err)
	}
	if len(decoded) != 0 {
		t.Errorf("expected 0 bytes, got %d: %q", len(decoded), decoded)
	}
}

func TestRoundTrip_EveryByteValue(t *testing.T) {

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
		t.Errorf("binary mismatch")
	}
}
