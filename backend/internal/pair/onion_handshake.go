package pair

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

const PairMACHeader = "X-Haoma-Pair-Mac"

const PairContentType = "application/octet-stream"

const PairHandoffPath = "/pair"

const (
	joinerMACPrefix  = "haoma-pair-joiner-v2/"
	inviterMACPrefix = "haoma-pair-inviter-v2/"
)

func JoinerHandshakeMAC(handoff [32]byte, body []byte) string {
	return computeMAC(handoff, joinerMACPrefix, body)
}

func InviterHandshakeMAC(handoff [32]byte, body []byte) string {
	return computeMAC(handoff, inviterMACPrefix, body)
}

func VerifyJoinerHandshakeMAC(handoff [32]byte, body []byte, gotHex string) bool {
	return verifyMAC(handoff, joinerMACPrefix, body, gotHex)
}

func VerifyInviterHandshakeMAC(handoff [32]byte, body []byte, gotHex string) bool {
	return verifyMAC(handoff, inviterMACPrefix, body, gotHex)
}

func computeMAC(handoff [32]byte, prefix string, body []byte) string {
	h := hmac.New(sha256.New, handoff[:])
	h.Write([]byte(prefix))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

func verifyMAC(handoff [32]byte, prefix string, body []byte, gotHex string) bool {
	want := computeMAC(handoff, prefix, body)

	return hmac.Equal([]byte(want), []byte(toLowerHex(gotHex)))
}

func toLowerHex(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'F' {
			b[i] = c + 32
		}
	}
	return string(b)
}

var ErrEmptyHandshakeBody = errors.New("pair: empty handshake body")
