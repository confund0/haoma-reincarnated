package signal

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/dgraph-io/badger/v4"
	"go.mau.fi/libsignal/ecc"
	"go.mau.fi/libsignal/keys/identity"
	"go.mau.fi/libsignal/protocol"
	"go.mau.fi/libsignal/serialize"
	"go.mau.fi/libsignal/state/record"
	upstreamstore "go.mau.fi/libsignal/state/store"
	"go.mau.fi/libsignal/util/keyhelper"

	"haoma-frontend/internal/store"
)

var (
	_ upstreamstore.IdentityKey  = (*Stores)(nil)
	_ upstreamstore.PreKey       = (*Stores)(nil)
	_ upstreamstore.SignedPreKey = (*Stores)(nil)
	_ upstreamstore.Session      = (*Stores)(nil)
)

type Stores struct {
	store      *store.Store
	state      *State
	serializer *serialize.Serializer
	refillMu   sync.Mutex
}

func NewStores(st *store.Store, state *State) *Stores {
	return &Stores{
		store:      st,
		state:      state,
		serializer: serialize.NewJSONSerializer(),
	}
}

func (s *Stores) Store() *store.Store {
	return s.store
}

func (s *Stores) GetIdentityKeyPair() *identity.KeyPair {
	return s.state.IdentityKeyPair
}

func (s *Stores) GetLocalRegistrationID() uint32 {
	return s.state.RegistrationID
}

func (s *Stores) SaveIdentity(_ context.Context, address *protocol.SignalAddress, key *identity.Key) error {
	return s.store.Put(remoteIdentKey(address.String()), key.Serialize())
}

func (s *Stores) IsTrustedIdentity(_ context.Context, address *protocol.SignalAddress, key *identity.Key) (bool, error) {
	stored, err := s.store.Get(remoteIdentKey(address.String()))
	if errors.Is(err, store.ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("signal: is-trusted load: %w", err)
	}
	return bytes.Equal(stored, key.Serialize()), nil
}

func (s *Stores) GetRemoteIdentity(address *protocol.SignalAddress) (*identity.Key, error) {
	raw, err := s.store.Get(remoteIdentKey(address.String()))
	if err != nil {
		return nil, err
	}
	pub, err := ecc.DecodePoint(raw, 0)
	if err != nil {
		return nil, fmt.Errorf("signal: decode remote ident: %w", err)
	}
	return identity.NewKey(pub), nil
}

func (s *Stores) LoadPreKey(_ context.Context, id uint32) (*record.PreKey, error) {
	raw, err := s.store.Get(opkKey(id))
	if err != nil {
		return nil, err
	}
	return record.NewPreKeyFromBytes(raw, s.serializer.PreKeyRecord)
}

func (s *Stores) StorePreKey(_ context.Context, id uint32, rec *record.PreKey) error {
	return s.store.Put(opkKey(id), rec.Serialize())
}

func (s *Stores) ContainsPreKey(_ context.Context, id uint32) (bool, error) {
	_, err := s.store.Get(opkKey(id))
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Stores) RemovePreKey(_ context.Context, id uint32) error {
	if err := s.store.Delete(opkKey(id)); err != nil {
		return err
	}
	slog.Debug("opk consumed", slog.Uint64("id", uint64(id)))
	minted, err := s.maybeRefill(LowWaterOPK, DefaultOPKCount)
	if err != nil {

		slog.Debug("opk refill failed", slog.Any("err", err))
	} else if minted > 0 {
		slog.Debug("opk pool refilled", slog.Int("minted", minted), slog.Int("target", DefaultOPKCount))
	}
	return nil
}

func (s *Stores) maybeRefill(lowWater, target int) (minted int, err error) {
	s.refillMu.Lock()
	defer s.refillMu.Unlock()

	err = s.store.Update(func(txn *badger.Txn) error {
		count, maxID, cerr := countAndMaxOPK(txn)
		if cerr != nil {
			return cerr
		}
		if count >= lowWater {
			return nil
		}
		need := target - count
		if need <= 0 {
			return nil
		}
		startID := int(maxID) + 1
		endID := startID + need - 1
		fresh, gerr := keyhelper.GeneratePreKeys(startID, endID, s.serializer.PreKeyRecord)
		if gerr != nil {
			return fmt.Errorf("generate prekeys: %w", gerr)
		}
		for _, opk := range fresh {
			id := opk.ID()
			if id.IsEmpty {
				continue
			}
			if serr := txn.Set(opkKey(id.Value), opk.Serialize()); serr != nil {
				return serr
			}
		}
		minted = len(fresh)
		return nil
	})
	return minted, err
}

func countAndMaxOPK(txn *badger.Txn) (count int, maxID uint32, err error) {
	opts := badger.DefaultIteratorOptions
	opts.Prefix = []byte(opkKeyPrefix)
	opts.PrefetchValues = false
	it := txn.NewIterator(opts)
	defer it.Close()
	prefixLen := len(opkKeyPrefix)
	for it.Rewind(); it.Valid(); it.Next() {
		count++
		k := it.Item().Key()
		if len(k) == prefixLen+4 {
			id := binary.BigEndian.Uint32(k[prefixLen:])
			if id > maxID {
				maxID = id
			}
		}
	}
	return count, maxID, nil
}

func (s *Stores) AvailableOPK() (uint32, []byte, error) {
	var chosen *record.PreKey
	err := s.store.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(opkKeyPrefix)
		it := txn.NewIterator(opts)
		defer it.Close()

		it.Rewind()
		if !it.Valid() {
			return nil
		}
		var raw []byte
		if err := it.Item().Value(func(v []byte) error {
			raw = append([]byte(nil), v...)
			return nil
		}); err != nil {
			return err
		}
		opk, err := decodePreKey(raw, s.serializer.PreKeyRecord)
		if err != nil {
			return fmt.Errorf("decode opk: %w", err)
		}
		chosen = opk
		return nil
	})
	if err != nil {
		return 0, nil, fmt.Errorf("signal: available opk: %w", err)
	}
	if chosen == nil {
		return 0, nil, ErrOPKPoolEmpty
	}
	id := chosen.ID()
	if id.IsEmpty {
		return 0, nil, ErrOPKPoolEmpty
	}
	return id.Value, chosen.KeyPair().PublicKey().Serialize(), nil
}

func (s *Stores) LoadSignedPreKey(_ context.Context, id uint32) (*record.SignedPreKey, error) {
	raw, err := s.store.Get(spkKey(id))
	if err != nil {
		return nil, err
	}
	return record.NewSignedPreKeyFromBytes(raw, s.serializer.SignedPreKeyRecord)
}

func (s *Stores) LoadSignedPreKeys(_ context.Context) ([]*record.SignedPreKey, error) {
	var out []*record.SignedPreKey
	err := s.store.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(spkKeyPrefix)
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			var raw []byte
			if err := it.Item().Value(func(v []byte) error {
				raw = append([]byte(nil), v...)
				return nil
			}); err != nil {
				return err
			}
			spk, err := record.NewSignedPreKeyFromBytes(raw, s.serializer.SignedPreKeyRecord)
			if err != nil {
				return fmt.Errorf("decode spk: %w", err)
			}
			out = append(out, spk)
		}
		return nil
	})
	return out, err
}

func (s *Stores) StoreSignedPreKey(_ context.Context, id uint32, rec *record.SignedPreKey) error {
	return s.store.Put(spkKey(id), rec.Serialize())
}

func (s *Stores) ContainsSignedPreKey(_ context.Context, id uint32) (bool, error) {
	_, err := s.store.Get(spkKey(id))
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Stores) RemoveSignedPreKey(_ context.Context, id uint32) error {
	return s.store.Delete(spkKey(id))
}

func (s *Stores) LoadSession(_ context.Context, address *protocol.SignalAddress) (*record.Session, error) {
	raw, err := s.store.Get(sessionKey(address.String()))
	if errors.Is(err, store.ErrNotFound) {
		return record.NewSession(s.serializer.Session, s.serializer.State), nil
	}
	if err != nil {
		return nil, err
	}
	return record.NewSessionFromBytes(raw, s.serializer.Session, s.serializer.State)
}

func (s *Stores) GetSubDeviceSessions(_ context.Context, _ string) ([]uint32, error) {
	return nil, nil
}

func (s *Stores) StoreSession(_ context.Context, address *protocol.SignalAddress, rec *record.Session) error {
	return s.store.Put(sessionKey(address.String()), rec.Serialize())
}

func (s *Stores) ContainsSession(_ context.Context, address *protocol.SignalAddress) (bool, error) {
	_, err := s.store.Get(sessionKey(address.String()))
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Stores) DeleteSession(_ context.Context, address *protocol.SignalAddress) error {
	return s.store.Delete(sessionKey(address.String()))
}

func (s *Stores) DeleteAllSessions(_ context.Context) error {
	return s.store.Update(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(sessionPrefix)
		it := txn.NewIterator(opts)
		defer it.Close()
		var victims [][]byte
		for it.Rewind(); it.Valid(); it.Next() {
			victims = append(victims, it.Item().KeyCopy(nil))
		}

		for _, k := range victims {
			if err := txn.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}
