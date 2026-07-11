package session

import (
	"context"
	"errors"
	"fmt"
	"sync"

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

	muPeers sync.Mutex
	perPeer map[string]*sync.Mutex
}

func New(stores *signal.Stores) *Cipher {
	return &Cipher{
		stores:  stores,
		ser:     serialize.NewJSONSerializer(),
		perPeer: make(map[string]*sync.Mutex),
	}
}

func (c *Cipher) peerGate(peerID string) *sync.Mutex {
	c.muPeers.Lock()
	defer c.muPeers.Unlock()
	if c.perPeer == nil {
		c.perPeer = make(map[string]*sync.Mutex)
	}
	m, ok := c.perPeer[peerID]
	if !ok {
		m = &sync.Mutex{}
		c.perPeer[peerID] = m
	}
	return m
}

func (c *Cipher) Encrypt(ctx context.Context, peerID string, plaintext []byte) ([]byte, error) {

	gate := c.peerGate(peerID)
	gate.Lock()
	defer gate.Unlock()

	addr := protocol.NewSignalAddress(peerID, DeviceID)

	contains, err := c.stores.ContainsSession(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("session: contains-session check for %s: %w", peerID, err)
	}
	if !contains {
		return nil, fmt.Errorf("%w: %s", ErrNoSession, peerID)
	}
	cipher := c.cipherFor(addr)

	before := c.snapshot(ctx, addr)
	msg, err := cipher.Encrypt(ctx, plaintext)
	c.logRatchetOp(ctx, "encrypt", peerID, peekKind(plaintext), 0, before, c.snapshot(ctx, addr), err)
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

	gate := c.peerGate(peerID)
	gate.Lock()
	defer gate.Unlock()

	addr := protocol.NewSignalAddress(peerID, DeviceID)
	cipher := c.cipherFor(addr)

	tag := blob[0]
	body := blob[1:]
	before := c.snapshot(ctx, addr)
	switch tag {
	case protocol.WHISPER_TYPE:
		msg, err := protocol.NewSignalMessageFromBytes(body, c.ser.SignalMessage)
		if err != nil {
			return nil, fmt.Errorf("session: parse SignalMessage from %s: %w", peerID, err)
		}
		plain, err := cipher.Decrypt(ctx, msg)
		c.logRatchetOp(ctx, "decrypt", peerID, peekKind(plain), tag, before, c.snapshot(ctx, addr), err)
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
		c.logRatchetOp(ctx, "decrypt", peerID, peekKind(plain), tag, before, c.snapshot(ctx, addr), err)
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
