package identity

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"haoma/internal/store"
	"haoma/internal/tor/control"
)

const (
	stateKey     = "identity"
	stateVersion = 2
)

const SlotCount = 2

type Onion struct {
	ServiceID  string `json:"service_id"`
	PrivateKey string `json:"private_key"`
}

type Identity struct {
	Active    []Onion
	RotatedAt int64
}

func (id *Identity) ServiceIDs() []string {
	out := make([]string, len(id.Active))
	for i, o := range id.Active {
		out[i] = o.ServiceID
	}
	return out
}

type Publisher interface {
	AddOnionNew(ports []control.OnionPort, flags ...string) (*control.Onion, error)
	AddOnion(privateKey string, ports []control.OnionPort, flags ...string) (*control.Onion, error)
	DelOnion(serviceID string) error
}

var ErrUnsupportedVersion = errors.New("identity: unsupported state version")

type envelope struct {
	Version   int     `json:"version"`
	Active    []Onion `json:"active"`
	RotatedAt int64   `json:"rotated_at,omitempty"`
}

func LoadOrPublish(st *store.Store, p Publisher, portsPerSlot [][]control.OnionPort) (*Identity, error) {
	if len(portsPerSlot) != SlotCount {
		return nil, fmt.Errorf("identity: portsPerSlot length %d, want %d", len(portsPerSlot), SlotCount)
	}
	raw, err := st.GetState(stateKey)
	if errors.Is(err, store.ErrNotFound) {
		return generate(st, p, portsPerSlot)
	}
	if err != nil {
		return nil, fmt.Errorf("identity: load state: %w", err)
	}
	return republish(st, p, portsPerSlot, raw)
}

func (id *Identity) Republish(p Publisher, portsPerSlot [][]control.OnionPort) error {
	for i, slot := range id.Active {
		o, err := p.AddOnion(slot.PrivateKey, portsPerSlot[i])
		if err != nil {
			return fmt.Errorf("identity: republish slot %d (%s): %w", i, slot.ServiceID, err)
		}
		if o.ServiceID != slot.ServiceID {
			return fmt.Errorf("identity: republish slot %d: key reproduced %s, expected %s", i, o.ServiceID, slot.ServiceID)
		}
	}
	return nil
}

func generate(st *store.Store, p Publisher, portsPerSlot [][]control.OnionPort) (*Identity, error) {
	id := &Identity{Active: make([]Onion, 0, SlotCount)}
	for slot := 0; slot < SlotCount; slot++ {
		o, err := p.AddOnionNew(portsPerSlot[slot])
		if err != nil {

			for _, existing := range id.Active {
				_ = p.DelOnion(existing.ServiceID)
			}
			return nil, fmt.Errorf("identity: generate slot %d: %w", slot, err)
		}
		id.Active = append(id.Active, Onion{ServiceID: o.ServiceID, PrivateKey: o.PrivateKey})
	}
	id.RotatedAt = time.Now().Unix()
	if err := save(st, id); err != nil {
		for _, existing := range id.Active {
			_ = p.DelOnion(existing.ServiceID)
		}
		return nil, err
	}
	return id, nil
}

func republish(st *store.Store, p Publisher, portsPerSlot [][]control.OnionPort, raw []byte) (*Identity, error) {
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("identity: decode state: %w", err)
	}
	if env.Version != stateVersion && env.Version != 1 {
		return nil, fmt.Errorf("%w: got %d, supports 1 or %d", ErrUnsupportedVersion, env.Version, stateVersion)
	}
	id := &Identity{Active: make([]Onion, 0, SlotCount), RotatedAt: env.RotatedAt}

	rollback := func() {
		for _, existing := range id.Active {
			_ = p.DelOnion(existing.ServiceID)
		}
	}

	for i, entry := range env.Active {
		if i >= SlotCount {
			break
		}
		o, err := p.AddOnion(entry.PrivateKey, portsPerSlot[i])
		if err != nil {
			rollback()
			return nil, fmt.Errorf("identity: republish %s: %w", entry.ServiceID, err)
		}
		if o.ServiceID != entry.ServiceID {
			rollback()
			return nil, fmt.Errorf("identity: republish %s returned different ServiceID %q", entry.ServiceID, o.ServiceID)
		}
		id.Active = append(id.Active, entry)
	}

	for slot := len(id.Active); slot < SlotCount; slot++ {
		o, err := p.AddOnionNew(portsPerSlot[slot])
		if err != nil {
			rollback()
			return nil, fmt.Errorf("identity: top-up slot %d: %w", slot, err)
		}
		id.Active = append(id.Active, Onion{ServiceID: o.ServiceID, PrivateKey: o.PrivateKey})
	}

	if len(env.Active) < SlotCount || env.Version != stateVersion {
		if id.RotatedAt == 0 {
			id.RotatedAt = time.Now().Unix()
		}
		if err := save(st, id); err != nil {
			rollback()
			return nil, err
		}
	}
	return id, nil
}

func save(st *store.Store, id *Identity) error {
	raw, err := json.Marshal(envelope{
		Version:   stateVersion,
		Active:    id.Active,
		RotatedAt: id.RotatedAt,
	})
	if err != nil {
		return fmt.Errorf("identity: encode state: %w", err)
	}
	if err := st.PutState(stateKey, raw); err != nil {
		return fmt.Errorf("identity: save state: %w", err)
	}
	return nil
}
