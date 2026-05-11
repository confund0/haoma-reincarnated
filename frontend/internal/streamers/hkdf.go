package streamers

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

const StreamKeyBytes = 32

func DeriveStreamKey(outboundKey []byte, streamID string) ([]byte, error) {
	if len(outboundKey) != StreamKeyBytes {
		return nil, fmt.Errorf("streamers: outbound_key must be %d bytes, got %d", StreamKeyBytes, len(outboundKey))
	}
	if streamID == "" {
		return nil, errors.New("streamers: empty stream_id")
	}
	r := hkdf.New(sha256.New, outboundKey, nil, []byte(streamID))
	out := make([]byte, StreamKeyBytes)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, fmt.Errorf("streamers: hkdf: %w", err)
	}
	return out, nil
}
