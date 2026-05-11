package peers

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

type Peer struct {
	ID string `json:"id"`

	KnownAddresses []string `json:"known_addresses"`

	MyOnionAddr string `json:"my_onion_addr,omitempty"`

	MyOnionPrivateKey string `json:"my_onion_private_key,omitempty"`

	InboundSecret []byte `json:"inbound_secret"`

	OutboundSecret []byte `json:"outbound_secret"`

	IDSCounters map[string]int `json:"ids_counters,omitempty"`

	LastActiveAt int64 `json:"last_active_at,omitempty"`

	LastPassiveAt int64 `json:"last_passive_at,omitempty"`

	RetiredAt int64 `json:"retired_at,omitempty"`

	RetiredAddrs []RetiredAddr `json:"retired_addrs,omitempty"`

	PrevMyOnion *PrevMyOnion `json:"prev_my_onion,omitempty"`
}

type RetiredAddr struct {
	Address   string `json:"address"`
	ExpiresAt int64  `json:"expires_at"`
}

type PrevMyOnion struct {
	Address    string `json:"address"`
	PrivateKey string `json:"private_key"`
	ExpiresAt  int64  `json:"expires_at"`
}

func NewPeerID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("contacts: generate peer id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func NewPeerSecret() ([]byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("contacts: generate peer secret: %w", err)
	}
	return b, nil
}

func (p *Peer) hasAddress(addr string) bool {
	for _, a := range p.KnownAddresses {
		if a == addr {
			return true
		}
	}
	return false
}
