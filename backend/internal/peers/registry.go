package peers

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/dgraph-io/badger/v4"

	"haoma/internal/store"
)

var ErrPeerNotFound = errors.New("contacts: peer not found")

type Registry struct {
	st *store.Store

	Now func() time.Time

	CollapseGrace time.Duration
}

const DefaultCollapseGrace = 2 * time.Minute

func NewRegistry(st *store.Store) *Registry {
	return &Registry{st: st}
}

func (r *Registry) collapseGrace() time.Duration {
	if r.CollapseGrace == 0 {
		return DefaultCollapseGrace
	}
	if r.CollapseGrace < 0 {
		return 0
	}
	return r.CollapseGrace
}

func (r *Registry) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Registry) Add(p Peer) error {
	if err := validate(&p); err != nil {
		return err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("contacts: marshal peer: %w", err)
	}
	return r.st.Update(func(txn *badger.Txn) error {
		if err := txn.Set(store.ContactKey(store.ContactID(p.ID)), raw); err != nil {
			return err
		}
		for _, addr := range p.KnownAddresses {
			if err := txn.Set(store.AddrKey(addr), []byte(p.ID)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *Registry) Get(id string) (*Peer, error) {
	raw, err := r.st.GetContact(store.ContactID(id))
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrPeerNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("contacts: read peer: %w", err)
	}
	var p Peer
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("contacts: decode peer %q: %w", id, err)
	}
	return &p, nil
}

func (r *Registry) Remove(id string) error {
	return r.st.Update(func(txn *badger.Txn) error {

		key := store.ContactKey(store.ContactID(id))
		item, err := txn.Get(key)
		if errors.Is(err, badger.ErrKeyNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		var p Peer
		if err := item.Value(func(v []byte) error {
			return json.Unmarshal(v, &p)
		}); err != nil {
			return err
		}
		if err := txn.Delete(key); err != nil {
			return err
		}
		for _, addr := range p.KnownAddresses {
			if err := txn.Delete(store.AddrKey(addr)); err != nil {
				return err
			}
		}

		for _, ra := range p.RetiredAddrs {
			if err := txn.Delete(store.AddrKey(ra.Address)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *Registry) List() ([]Peer, error) {
	ids, err := r.st.ListContacts()
	if err != nil {
		return nil, fmt.Errorf("contacts: list peer ids: %w", err)
	}
	out := make([]Peer, 0, len(ids))
	for _, idBytes := range ids {
		p, err := r.Get(string(idBytes))
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, nil
}

func (r *Registry) ByAddress(serviceID string) (*Peer, error) {
	id, err := r.st.GetAddrIndex(serviceID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrPeerNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("contacts: addr index lookup: %w", err)
	}
	return r.Get(string(id))
}

func (r *Registry) RecordViolation(id string, sourceAddr string) error {
	return r.mutate(id, func(p *Peer) {
		if p.IDSCounters == nil {
			p.IDSCounters = map[string]int{}
		}
		p.IDSCounters[sourceAddr]++
	})
}

func (r *Registry) TouchPresence(id string, t time.Time, source string) error {
	return r.mutate(id, func(p *Peer) {
		ts := t.Unix()
		p.LastPassiveAt = ts
		if source == "haoma" {
			p.LastActiveAt = ts
		}
	})
}

func (r *Registry) mutate(id string, fn func(*Peer)) error {
	return r.st.Update(func(txn *badger.Txn) error {
		key := store.ContactKey(store.ContactID(id))
		item, err := txn.Get(key)
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrPeerNotFound
		}
		if err != nil {
			return err
		}
		var p Peer
		if err := item.Value(func(v []byte) error {
			return json.Unmarshal(v, &p)
		}); err != nil {
			return err
		}
		fn(&p)
		raw, err := json.Marshal(&p)
		if err != nil {
			return err
		}
		return txn.Set(key, raw)
	})
}

func validate(p *Peer) error {
	if p.ID == "" {
		return errors.New("contacts: peer ID required")
	}
	if len(p.InboundSecret) != 32 {
		return fmt.Errorf("contacts: peer inbound secret must be 32 bytes, got %d", len(p.InboundSecret))
	}
	if len(p.OutboundSecret) != 32 {
		return fmt.Errorf("contacts: peer outbound secret must be 32 bytes, got %d", len(p.OutboundSecret))
	}
	return nil
}

type Stats struct {
	Total             int            `json:"total"`
	TotalViolations   int            `json:"total_violations"`
	PerPeerViolations map[string]int `json:"per_peer_violations"`
	NeverSeen         int            `json:"never_seen"`
}

func (r *Registry) Stats() (Stats, error) {
	peers, err := r.List()
	if err != nil {
		return Stats{}, err
	}
	out := Stats{
		PerPeerViolations: make(map[string]int, len(peers)),
	}
	for _, p := range peers {
		out.Total++
		if p.LastPassiveAt == 0 {
			out.NeverSeen++
		}
		sum := 0
		for _, c := range p.IDSCounters {
			sum += c
		}
		out.PerPeerViolations[p.ID] = sum
		out.TotalViolations += sum
	}
	return out, nil
}

func (r *Registry) ResetIDS(peerID string) error {
	return r.mutate(peerID, func(p *Peer) {
		p.IDSCounters = nil
	})
}

func (r *Registry) OverlayPeerAddress(peerID, address string) error {
	if address == "" {
		return errors.New("contacts: overlay address required")
	}
	return r.st.Update(func(txn *badger.Txn) error {
		key := store.ContactKey(store.ContactID(peerID))
		item, err := txn.Get(key)
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrPeerNotFound
		}
		if err != nil {
			return err
		}
		var p Peer
		if err := item.Value(func(v []byte) error {
			return json.Unmarshal(v, &p)
		}); err != nil {
			return err
		}
		if p.RetiredAt != 0 {
			return errors.New("contacts: cannot overlay retired peer")
		}

		if len(p.KnownAddresses) > 0 && p.KnownAddresses[0] == address {
			return nil
		}

		filtered := p.KnownAddresses[:0:0]
		for _, a := range p.KnownAddresses {
			if a != address {
				filtered = append(filtered, a)
			}
		}
		p.KnownAddresses = append([]string{address}, filtered...)
		raw, err := json.Marshal(&p)
		if err != nil {
			return err
		}
		if err := txn.Set(key, raw); err != nil {
			return err
		}
		return txn.Set(store.AddrKey(address), []byte(peerID))
	})
}

func (r *Registry) CollapsePeerAddress(peerID, retain string) error {
	if retain == "" {
		return errors.New("contacts: collapse retain address required")
	}
	grace := r.collapseGrace()
	now := r.now().Unix()
	expiresAt := now + int64(grace.Seconds())
	return r.st.Update(func(txn *badger.Txn) error {
		key := store.ContactKey(store.ContactID(peerID))
		item, err := txn.Get(key)
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrPeerNotFound
		}
		if err != nil {
			return err
		}
		var p Peer
		if err := item.Value(func(v []byte) error {
			return json.Unmarshal(v, &p)
		}); err != nil {
			return err
		}
		if !p.hasAddress(retain) {
			return fmt.Errorf("contacts: collapse retain %q not in KnownAddresses", retain)
		}
		toDrop := make([]string, 0, len(p.KnownAddresses))
		for _, a := range p.KnownAddresses {
			if a != retain {
				toDrop = append(toDrop, a)
			}
		}
		p.KnownAddresses = []string{retain}

		if grace > 0 {
			for _, a := range toDrop {
				p.RetiredAddrs = append(p.RetiredAddrs, RetiredAddr{
					Address:   a,
					ExpiresAt: expiresAt,
				})
			}
		}
		raw, err := json.Marshal(&p)
		if err != nil {
			return err
		}
		if err := txn.Set(key, raw); err != nil {
			return err
		}
		if grace == 0 {
			for _, a := range toDrop {
				if err := txn.Delete(store.AddrKey(a)); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (r *Registry) SweepRetiredAddrs(nowUnix int64) (int, error) {
	ids, err := r.st.ListContacts()
	if err != nil {
		return 0, fmt.Errorf("contacts: sweep list: %w", err)
	}
	swept := 0
	for _, idBytes := range ids {
		peerID := string(idBytes)
		err := r.st.Update(func(txn *badger.Txn) error {
			key := store.ContactKey(store.ContactID(peerID))
			item, err := txn.Get(key)
			if errors.Is(err, badger.ErrKeyNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			var p Peer
			if err := item.Value(func(v []byte) error {
				return json.Unmarshal(v, &p)
			}); err != nil {
				return err
			}
			if len(p.RetiredAddrs) == 0 {
				return nil
			}
			kept := p.RetiredAddrs[:0:0]
			toDrop := make([]string, 0)
			for _, ra := range p.RetiredAddrs {
				if ra.ExpiresAt <= nowUnix {
					toDrop = append(toDrop, ra.Address)
				} else {
					kept = append(kept, ra)
				}
			}
			if len(toDrop) == 0 {
				return nil
			}
			p.RetiredAddrs = kept
			raw, err := json.Marshal(&p)
			if err != nil {
				return err
			}
			if err := txn.Set(key, raw); err != nil {
				return err
			}
			for _, a := range toDrop {
				if err := txn.Delete(store.AddrKey(a)); err != nil {
					return err
				}
			}
			swept += len(toDrop)
			return nil
		})
		if err != nil {
			return swept, fmt.Errorf("contacts: sweep peer %s: %w", peerID, err)
		}
	}
	return swept, nil
}

func (r *Registry) SweepRetiredOwnOnions(nowUnix int64) ([]string, error) {
	ids, err := r.st.ListContacts()
	if err != nil {
		return nil, fmt.Errorf("contacts: sweep prev-onions list: %w", err)
	}
	var toDelete []string
	for _, idBytes := range ids {
		peerID := string(idBytes)
		err := r.st.Update(func(txn *badger.Txn) error {
			key := store.ContactKey(store.ContactID(peerID))
			item, err := txn.Get(key)
			if errors.Is(err, badger.ErrKeyNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			var p Peer
			if err := item.Value(func(v []byte) error {
				return json.Unmarshal(v, &p)
			}); err != nil {
				return err
			}
			if p.PrevMyOnion == nil {
				return nil
			}
			if p.PrevMyOnion.ExpiresAt > nowUnix {
				return nil
			}
			toDelete = append(toDelete, p.PrevMyOnion.Address)
			p.PrevMyOnion = nil
			raw, err := json.Marshal(&p)
			if err != nil {
				return err
			}
			return txn.Set(key, raw)
		})
		if err != nil {
			return toDelete, fmt.Errorf("contacts: sweep prev-onion for peer %s: %w", peerID, err)
		}
	}
	return toDelete, nil
}

func (r *Registry) RotateOwnOnion(peerID, address, privateKey string) (immediateDelete string, err error) {
	if address == "" {
		return "", errors.New("contacts: rotate own onion: address required")
	}
	if privateKey == "" {
		return "", errors.New("contacts: rotate own onion: private_key required")
	}
	grace := r.collapseGrace()
	now := r.now().Unix()
	expiresAt := now + int64(grace.Seconds())
	err = r.st.Update(func(txn *badger.Txn) error {
		key := store.ContactKey(store.ContactID(peerID))
		item, err := txn.Get(key)
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrPeerNotFound
		}
		if err != nil {
			return err
		}
		var p Peer
		if err := item.Value(func(v []byte) error {
			return json.Unmarshal(v, &p)
		}); err != nil {
			return err
		}
		if p.RetiredAt != 0 {
			return errors.New("contacts: cannot rotate retired peer")
		}
		oldAddr := p.MyOnionAddr
		oldPriv := p.MyOnionPrivateKey
		if grace <= 0 {
			immediateDelete = oldAddr
			p.PrevMyOnion = nil
		} else {

			if p.PrevMyOnion != nil {
				immediateDelete = p.PrevMyOnion.Address
			}
			if oldAddr != "" {
				p.PrevMyOnion = &PrevMyOnion{
					Address:    oldAddr,
					PrivateKey: oldPriv,
					ExpiresAt:  expiresAt,
				}
			} else {
				p.PrevMyOnion = nil
			}
		}
		p.MyOnionAddr = address
		p.MyOnionPrivateKey = privateKey
		raw, err := json.Marshal(&p)
		if err != nil {
			return err
		}
		return txn.Set(key, raw)
	})
	return immediateDelete, err
}

func (r *Registry) Retire(id string) error {
	return r.st.Update(func(txn *badger.Txn) error {
		return r.retireInTxn(txn, id, r.now().Unix())
	})
}

func (r *Registry) retireInTxn(txn *badger.Txn, id string, retiredAt int64) error {
	key := store.ContactKey(store.ContactID(id))
	item, err := txn.Get(key)
	if errors.Is(err, badger.ErrKeyNotFound) {
		return ErrPeerNotFound
	}
	if err != nil {
		return err
	}
	var p Peer
	if err := item.Value(func(v []byte) error {
		return json.Unmarshal(v, &p)
	}); err != nil {
		return err
	}
	if p.RetiredAt != 0 {
		return nil
	}
	oldAddrs := p.KnownAddresses
	retiredAddrs := p.RetiredAddrs
	p.RetiredAt = retiredAt
	p.InboundSecret = nil
	p.OutboundSecret = nil
	p.KnownAddresses = nil
	p.MyOnionAddr = ""
	p.MyOnionPrivateKey = ""
	p.RetiredAddrs = nil
	p.PrevMyOnion = nil
	raw, err := json.Marshal(&p)
	if err != nil {
		return err
	}
	if err := txn.Set(key, raw); err != nil {
		return err
	}
	for _, a := range oldAddrs {
		if err := txn.Delete(store.AddrKey(a)); err != nil {
			return err
		}
	}

	for _, ra := range retiredAddrs {
		if err := txn.Delete(store.AddrKey(ra.Address)); err != nil {
			return err
		}
	}
	return nil
}
