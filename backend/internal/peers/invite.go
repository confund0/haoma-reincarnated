package peers

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dgraph-io/badger/v4"

	"haoma/internal/store"
)

type Invite struct {
	PeerID    string   `json:"peer_id"`
	Addresses []string `json:"addresses"`
	Secret    string   `json:"secret"`
}

func NewInvite(addresses []string, outboundSecret []byte) (*Invite, error) {
	if len(addresses) == 0 {
		return nil, errors.New("contacts: invite needs at least one address")
	}
	if outboundSecret == nil {
		var err error
		outboundSecret, err = NewPeerSecret()
		if err != nil {
			return nil, err
		}
	}
	if len(outboundSecret) != 32 {
		return nil, fmt.Errorf("contacts: invite secret must be 32 bytes, got %d", len(outboundSecret))
	}
	peerID, err := NewPeerID()
	if err != nil {
		return nil, err
	}
	return &Invite{
		PeerID:    peerID,
		Addresses: addresses,
		Secret:    hex.EncodeToString(outboundSecret),
	}, nil
}

func (inv *Invite) DecodeSecret() ([]byte, error) {
	b, err := hex.DecodeString(inv.Secret)
	if err != nil {
		return nil, fmt.Errorf("contacts: invite secret is not valid hex: %w", err)
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("contacts: invite secret decodes to %d bytes, want 32", len(b))
	}
	return b, nil
}

type BackendInvite struct {
	PeerID         string   `json:"peer_id"`
	Addresses      []string `json:"addresses"`
	InboundSecret  string   `json:"inbound_secret"`
	OutboundSecret string   `json:"outbound_secret"`

	MyOnionAddr string `json:"my_onion_addr"`

	MyOnionPrivateKey string `json:"my_onion_private_key"`
}

func (b *BackendInvite) DecodeKeys() (inbound, outbound []byte, err error) {
	inbound, err = hex.DecodeString(b.InboundSecret)
	if err != nil {
		return nil, nil, fmt.Errorf("contacts: backend invite inbound_secret not hex: %w", err)
	}
	if len(inbound) != 32 {
		return nil, nil, fmt.Errorf("contacts: backend invite inbound_secret must be 32 bytes, got %d", len(inbound))
	}
	outbound, err = hex.DecodeString(b.OutboundSecret)
	if err != nil {
		return nil, nil, fmt.Errorf("contacts: backend invite outbound_secret not hex: %w", err)
	}
	if len(outbound) != 32 {
		return nil, nil, fmt.Errorf("contacts: backend invite outbound_secret must be 32 bytes, got %d", len(outbound))
	}
	return inbound, outbound, nil
}

func (r *Registry) Import(inv *BackendInvite) ([]string, error) {
	if inv == nil {
		return nil, errors.New("contacts: nil invite")
	}
	inbound, outbound, err := inv.DecodeKeys()
	if err != nil {
		return nil, err
	}
	newPeer := Peer{
		ID:                inv.PeerID,
		KnownAddresses:    inv.Addresses,
		InboundSecret:     inbound,
		OutboundSecret:    outbound,
		MyOnionAddr:       inv.MyOnionAddr,
		MyOnionPrivateKey: inv.MyOnionPrivateKey,
	}
	if err := validate(&newPeer); err != nil {
		return nil, err
	}

	retiredAt := r.now().Unix()
	var retired []string

	err = r.st.Update(func(txn *badger.Txn) error {

		displacedSet := make(map[string]struct{})
		for _, addr := range newPeer.KnownAddresses {
			item, getErr := txn.Get(store.AddrKey(addr))
			if errors.Is(getErr, badger.ErrKeyNotFound) {
				continue
			}
			if getErr != nil {
				return getErr
			}
			var idBytes []byte
			if err := item.Value(func(v []byte) error {
				idBytes = append(idBytes[:0], v...)
				return nil
			}); err != nil {
				return err
			}
			displacedID := string(idBytes)
			if displacedID == newPeer.ID {
				continue
			}
			displacedSet[displacedID] = struct{}{}
		}

		for displacedID := range displacedSet {
			err := r.retireInTxn(txn, displacedID, retiredAt)
			if errors.Is(err, ErrPeerNotFound) {

				continue
			}
			if err != nil {
				return err
			}
			retired = append(retired, displacedID)
		}

		raw, err := json.Marshal(&newPeer)
		if err != nil {
			return err
		}
		if err := txn.Set(store.ContactKey(store.ContactID(newPeer.ID)), raw); err != nil {
			return err
		}
		for _, addr := range newPeer.KnownAddresses {
			if err := txn.Set(store.AddrKey(addr), []byte(newPeer.ID)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return retired, nil
}
