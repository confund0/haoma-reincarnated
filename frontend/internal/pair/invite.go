package pair

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"go.mau.fi/libsignal/ecc"
	"go.mau.fi/libsignal/keys/identity"
	"go.mau.fi/libsignal/keys/prekey"
	"go.mau.fi/libsignal/util/optional"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/signal"
)

const SecretLen = 32

const DeviceID uint32 = 1

type Invite struct {
	PeerID    string       `json:"peer_id"`
	Addresses []string     `json:"addresses"`
	Secret    string       `json:"secret"`
	Frontend  FrontendBlob `json:"frontend"`
}

type FrontendBlob struct {
	Nick   string       `json:"nick,omitempty"`
	Signal SignalBundle `json:"signal"`
}

type MyKeys struct {
	PeerID         string
	OutboundSecret []byte
}

type SignalBundle struct {
	RegistrationID uint32           `json:"registration_id"`
	IdentityKey    string           `json:"identity_key"`
	SignedPreKey   SignedPreKeyInfo `json:"signed_pre_key"`
	OneTimePreKey  PreKeyInfo       `json:"one_time_pre_key"`
}

type SignedPreKeyInfo struct {
	ID        uint32 `json:"id"`
	Public    string `json:"public"`
	Signature string `json:"signature"`
}

type PreKeyInfo struct {
	ID     uint32 `json:"id"`
	Public string `json:"public"`
}

var ErrNoPreKeysAvailable = errors.New("pair: no one-time prekeys available")

type OPKSource interface {
	AvailableOPK() (id uint32, pub []byte, err error)
}

func Build(state *signal.State, opks OPKSource, addresses []string, nickname string, keys *MyKeys) (*Invite, *MyKeys, error) {
	if state == nil {
		return nil, nil, errors.New("pair: nil signal state")
	}
	if opks == nil {
		return nil, nil, errors.New("pair: nil opk source")
	}
	if len(addresses) == 0 {
		return nil, nil, errors.New("pair: no addresses")
	}

	var peerID string
	var secret []byte
	if keys != nil {
		if keys.PeerID == "" {
			return nil, nil, errors.New("pair: MyKeys.PeerID empty")
		}
		if len(keys.OutboundSecret) != SecretLen {
			return nil, nil, fmt.Errorf("pair: MyKeys.OutboundSecret length %d, want %d", len(keys.OutboundSecret), SecretLen)
		}
		peerID = keys.PeerID
		secret = keys.OutboundSecret
	} else {
		var err error
		peerID, err = newPeerID()
		if err != nil {
			return nil, nil, fmt.Errorf("pair: peer id: %w", err)
		}
		secret, err = newSecret()
		if err != nil {
			return nil, nil, fmt.Errorf("pair: secret: %w", err)
		}
	}

	opkID, opkPub, err := opks.AvailableOPK()
	if err != nil {
		if errors.Is(err, signal.ErrOPKPoolEmpty) {
			return nil, nil, ErrNoPreKeysAvailable
		}
		return nil, nil, fmt.Errorf("pair: opk pick: %w", err)
	}

	spkSig := state.SignedPreKey.Signature()
	inv := &Invite{
		PeerID:    peerID,
		Addresses: append([]string(nil), addresses...),
		Secret:    hex.EncodeToString(secret),
		Frontend: FrontendBlob{
			Nick: nickname,
			Signal: SignalBundle{
				RegistrationID: state.RegistrationID,
				IdentityKey:    hex.EncodeToString(state.IdentityKeyPair.PublicKey().Serialize()),
				SignedPreKey: SignedPreKeyInfo{
					ID:        state.SignedPreKey.ID(),
					Public:    hex.EncodeToString(state.SignedPreKey.KeyPair().PublicKey().Serialize()),
					Signature: hex.EncodeToString(spkSig[:]),
				},
				OneTimePreKey: PreKeyInfo{
					ID:     opkID,
					Public: hex.EncodeToString(opkPub),
				},
			},
		},
	}
	mine := &MyKeys{
		PeerID:         peerID,
		OutboundSecret: append([]byte(nil), secret...),
	}
	return inv, mine, nil
}

func (i *Invite) Marshal() ([]byte, error) {
	return json.Marshal(i)
}

func Parse(data []byte) (*Invite, error) {
	var inv Invite
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&inv); err != nil {
		return nil, fmt.Errorf("pair: decode invite: %w", err)
	}
	if err := inv.Validate(); err != nil {
		return nil, err
	}
	return &inv, nil
}

func (i *Invite) Validate() error {
	if i.PeerID == "" {
		return errors.New("pair: peer_id empty")
	}
	if _, err := hex.DecodeString(i.PeerID); err != nil {
		return fmt.Errorf("pair: peer_id not hex: %w", err)
	}
	if len(i.Addresses) == 0 {
		return errors.New("pair: addresses empty")
	}
	secret, err := hex.DecodeString(i.Secret)
	if err != nil {
		return fmt.Errorf("pair: secret not hex: %w", err)
	}
	if len(secret) != SecretLen {
		return fmt.Errorf("pair: secret length %d, want %d", len(secret), SecretLen)
	}
	if i.Frontend.Signal.RegistrationID == 0 {
		return errors.New("pair: registration_id zero")
	}
	idKey, err := hex.DecodeString(i.Frontend.Signal.IdentityKey)
	if err != nil {
		return fmt.Errorf("pair: identity_key not hex: %w", err)
	}
	if len(idKey) != 33 {
		return fmt.Errorf("pair: identity_key length %d, want 33", len(idKey))
	}
	if i.Frontend.Signal.SignedPreKey.ID == 0 {
		return errors.New("pair: signed_pre_key.id zero")
	}
	spkPub, err := hex.DecodeString(i.Frontend.Signal.SignedPreKey.Public)
	if err != nil {
		return fmt.Errorf("pair: signed_pre_key.public not hex: %w", err)
	}
	if len(spkPub) != 33 {
		return fmt.Errorf("pair: signed_pre_key.public length %d, want 33", len(spkPub))
	}
	spkSig, err := hex.DecodeString(i.Frontend.Signal.SignedPreKey.Signature)
	if err != nil {
		return fmt.Errorf("pair: signed_pre_key.signature not hex: %w", err)
	}
	if len(spkSig) != 64 {
		return fmt.Errorf("pair: signed_pre_key.signature length %d, want 64", len(spkSig))
	}
	if i.Frontend.Signal.OneTimePreKey.ID == 0 {
		return errors.New("pair: one_time_pre_key.id zero")
	}
	opkPub, err := hex.DecodeString(i.Frontend.Signal.OneTimePreKey.Public)
	if err != nil {
		return fmt.Errorf("pair: one_time_pre_key.public not hex: %w", err)
	}
	if len(opkPub) != 33 {
		return fmt.Errorf("pair: one_time_pre_key.public length %d, want 33", len(opkPub))
	}
	return nil
}

func (i *Invite) ToBundle() (*prekey.Bundle, error) {
	idKeyBytes, _ := hex.DecodeString(i.Frontend.Signal.IdentityKey)
	idPub, err := ecc.DecodePoint(idKeyBytes, 0)
	if err != nil {
		return nil, fmt.Errorf("pair: decode identity key: %w", err)
	}
	idKey := identity.NewKey(idPub)

	spkPubBytes, _ := hex.DecodeString(i.Frontend.Signal.SignedPreKey.Public)
	spkPub, err := ecc.DecodePoint(spkPubBytes, 0)
	if err != nil {
		return nil, fmt.Errorf("pair: decode signed prekey pub: %w", err)
	}

	spkSigBytes, _ := hex.DecodeString(i.Frontend.Signal.SignedPreKey.Signature)
	var spkSig [64]byte
	copy(spkSig[:], spkSigBytes)

	opkPubBytes, _ := hex.DecodeString(i.Frontend.Signal.OneTimePreKey.Public)
	opkPub, err := ecc.DecodePoint(opkPubBytes, 0)
	if err != nil {
		return nil, fmt.Errorf("pair: decode one-time prekey pub: %w", err)
	}

	return prekey.NewBundle(
		i.Frontend.Signal.RegistrationID,
		DeviceID,
		optional.NewOptionalUint32(i.Frontend.Signal.OneTimePreKey.ID),
		i.Frontend.Signal.SignedPreKey.ID,
		opkPub,
		spkPub,
		spkSig,
		idKey,
	), nil
}

type BackendInvite struct {
	PeerID            string   `json:"peer_id"`
	Addresses         []string `json:"addresses"`
	InboundSecret     string   `json:"inbound_secret"`
	OutboundSecret    string   `json:"outbound_secret"`
	MyOnionAddr       string   `json:"my_onion_addr"`
	MyOnionPrivateKey string   `json:"my_onion_private_key"`
}

func (i *Invite) Backend(myKeys *MyKeys, minted backendapi.MintedOnion) BackendInvite {
	return BackendInvite{
		PeerID:            i.PeerID,
		Addresses:         append([]string(nil), i.Addresses...),
		InboundSecret:     i.Secret,
		OutboundSecret:    hex.EncodeToString(myKeys.OutboundSecret),
		MyOnionAddr:       minted.Address,
		MyOnionPrivateKey: minted.PrivateKey,
	}
}

func newPeerID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func newSecret() ([]byte, error) {
	b := make([]byte, SecretLen)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}
