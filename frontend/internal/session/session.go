package session

import (
	"context"
	"errors"
	"fmt"

	"go.mau.fi/libsignal/protocol"
	"go.mau.fi/libsignal/serialize"
	libsession "go.mau.fi/libsignal/session"

	"haoma-frontend/internal/signal"
)

const DeviceID uint32 = 1

var ErrShortBlob = errors.New("session: ciphertext blob too short to carry a type tag")

var ErrUnknownType = errors.New("session: unknown libsignal CiphertextMessage type")

var ErrNoSession = errors.New("session: no session record for peer")

type Cipher struct {
	stores *signal.Stores
	ser    *serialize.Serializer
}

func New(stores *signal.Stores) *Cipher {
	return &Cipher{
		stores: stores,
		ser:    serialize.NewJSONSerializer(),
	}
}

func (c *Cipher) Encrypt(ctx context.Context, peerID string, plaintext []byte) ([]byte, error) {
	addr := protocol.NewSignalAddress(peerID, DeviceID)

	contains, err := c.stores.ContainsSession(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("session: contains-session check for %s: %w", peerID, err)
	}
	if !contains {
		return nil, fmt.Errorf("%w: %s", ErrNoSession, peerID)
	}
	cipher := c.cipherFor(addr)

	msg, err := cipher.Encrypt(ctx, plaintext)
	if err != nil {
		return nil, fmt.Errorf("session: encrypt to %s: %w", peerID, err)
	}
	body := msg.Serialize()
	out := make([]byte, 0, 1+len(body))
	out = append(out, byte(msg.Type()))
	out = append(out, body...)
	return out, nil
}

func (c *Cipher) Decrypt(ctx context.Context, peerID string, blob []byte) ([]byte, error) {
	if len(blob) < 1 {
		return nil, ErrShortBlob
	}
	addr := protocol.NewSignalAddress(peerID, DeviceID)
	cipher := c.cipherFor(addr)

	tag := blob[0]
	body := blob[1:]
	switch tag {
	case protocol.WHISPER_TYPE:
		msg, err := protocol.NewSignalMessageFromBytes(body, c.ser.SignalMessage)
		if err != nil {
			return nil, fmt.Errorf("session: parse SignalMessage from %s: %w", peerID, err)
		}
		plain, err := cipher.Decrypt(ctx, msg)
		if err != nil {
			return nil, fmt.Errorf("session: decrypt SignalMessage from %s: %w", peerID, err)
		}
		return plain, nil
	case protocol.PREKEY_TYPE:
		msg, err := protocol.NewPreKeySignalMessageFromBytes(body, c.ser.PreKeySignalMessage, c.ser.SignalMessage)
		if err != nil {
			return nil, fmt.Errorf("session: parse PreKeySignalMessage from %s: %w", peerID, err)
		}
		plain, err := cipher.DecryptMessage(ctx, msg)
		if err != nil {
			return nil, fmt.Errorf("session: decrypt PreKeySignalMessage from %s: %w", peerID, err)
		}
		return plain, nil
	default:
		return nil, fmt.Errorf("%w: tag=%d", ErrUnknownType, tag)
	}
}

func (c *Cipher) cipherFor(addr *protocol.SignalAddress) *libsession.Cipher {
	builder := libsession.NewBuilder(c.stores, c.stores, c.stores, c.stores, addr, c.ser)
	return libsession.NewCipher(builder, addr)
}
