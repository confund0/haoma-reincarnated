package syncrail

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

const maxSaltBytes = 64

type DestroySaltBody struct {
	Salt string `json:"salt"`

	SetAt int64 `json:"set_at,omitempty"`
}

func ParseDestroySalt(env Envelope) (DestroySaltBody, []byte, error) {
	if env.Kind != KindDestroySalt {
		return DestroySaltBody{}, nil, fmt.Errorf("%w: %q", ErrUnknownKind, env.Kind)
	}
	var body DestroySaltBody
	if err := json.Unmarshal(env.Body, &body); err != nil {
		return DestroySaltBody{}, nil, fmt.Errorf("syncrail: parse destroy salt: %w", err)
	}
	if body.Salt == "" {
		return DestroySaltBody{}, nil, errors.New("syncrail: destroy salt is empty")
	}
	salt, err := hex.DecodeString(body.Salt)
	if err != nil {
		return DestroySaltBody{}, nil, errors.New("syncrail: destroy salt is not hex")
	}
	if len(salt) == 0 {
		return DestroySaltBody{}, nil, errors.New("syncrail: destroy salt is empty")
	}
	if len(salt) > maxSaltBytes {
		return DestroySaltBody{}, nil, fmt.Errorf("syncrail: destroy salt is %d bytes, over the %d limit", len(salt), maxSaltBytes)
	}
	return body, salt, nil
}

func NewDestroySaltBody(salt []byte, setAt int64) DestroySaltBody {
	return DestroySaltBody{Salt: hex.EncodeToString(salt), SetAt: setAt}
}

type DestroySaltReport struct {
	Held bool `json:"held"`

	SHA256 string `json:"sha256,omitempty"`
}

func DestroySaltDigest(salt []byte) string {
	sum := sha256.Sum256(salt)
	return hex.EncodeToString(sum[:])
}
