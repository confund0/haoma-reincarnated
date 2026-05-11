package xport

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
)

type Envelope struct {
	ID             string `json:"id"`
	Timestamp      int64  `json:"ts"`
	From           string `json:"from"`
	Kind           string `json:"kind,omitempty"`
	PresenceSource string `json:"presence_source,omitempty"`
	Payload        []byte `json:"payload"`
	Padding        []byte `json:"padding,omitempty"`
	Mac            []byte `json:"mac"`
}

const (
	KindText     = "text"
	KindStatus   = "status"
	KindSentAck  = "sent_ack"
	KindPresence = "presence"
)

const (
	PresenceSourceHaoma  = "haoma"
	PresenceSourceHaomad = "haomad"
)

const MaxPaddingBytes = 2048

func (e Envelope) EffectiveKind() string {
	if e.Kind == "" {
		return KindText
	}
	return e.Kind
}

func RandomPadding(env *Envelope) error {
	var lenBuf [4]byte
	if _, err := rand.Read(lenBuf[:]); err != nil {
		return fmt.Errorf("xport: draw padding length: %w", err)
	}

	n := int(binary.BigEndian.Uint32(lenBuf[:]) % uint32(MaxPaddingBytes+1))
	if n == 0 {
		env.Padding = nil
		return nil
	}
	pad := make([]byte, n)
	if _, err := rand.Read(pad); err != nil {
		return fmt.Errorf("xport: draw padding bytes: %w", err)
	}
	env.Padding = pad
	return nil
}
