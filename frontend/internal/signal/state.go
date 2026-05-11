package signal

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dgraph-io/badger/v4"
	"go.mau.fi/libsignal/ecc"
	"go.mau.fi/libsignal/keys/identity"
	"go.mau.fi/libsignal/serialize"
	"go.mau.fi/libsignal/state/record"
	"go.mau.fi/libsignal/util/keyhelper"

	"haoma-frontend/internal/store"
)

const (
	keyRegistrationID = "signal:reg_id"
	keyIdentityPub    = "signal:identity_pub"
	keyIdentityPriv   = "signal:identity_priv"
	spkKeyPrefix      = "signal:spk:"
	opkKeyPrefix      = "signal:opk:"
	remoteIdentPrefix = "signal:remote_ident:"
	sessionPrefix     = "signal:session:"
)

const DefaultOPKCount = 100

const LowWaterOPK = 30

var ErrNotInitialized = errors.New("signal: not initialized")

var ErrOPKPoolEmpty = errors.New("signal: opk pool empty")

type State struct {
	RegistrationID  uint32
	IdentityKeyPair *identity.KeyPair
	SignedPreKey    *record.SignedPreKey
	OneTimePreKeys  []*record.PreKey
}

func Bootstrap(opkCount int) (*State, error) {
	if opkCount < 1 {
		opkCount = DefaultOPKCount
	}
	ser := serialize.NewJSONSerializer()

	idPair, err := keyhelper.GenerateIdentityKeyPair()
	if err != nil {
		return nil, fmt.Errorf("signal: generate identity: %w", err)
	}
	regID := keyhelper.GenerateRegistrationID()

	spk, err := keyhelper.GenerateSignedPreKey(idPair, 1, ser.SignedPreKeyRecord)
	if err != nil {
		return nil, fmt.Errorf("signal: generate signed prekey: %w", err)
	}

	opks, err := keyhelper.GeneratePreKeys(1, opkCount, ser.PreKeyRecord)
	if err != nil {
		return nil, fmt.Errorf("signal: generate one-time prekeys: %w", err)
	}

	return &State{
		RegistrationID:  regID,
		IdentityKeyPair: idPair,
		SignedPreKey:    spk,
		OneTimePreKeys:  opks,
	}, nil
}

func Save(st *store.Store, state *State) error {
	if state == nil {
		return errors.New("signal: nil state")
	}
	pubBytes := state.IdentityKeyPair.PublicKey().Serialize()
	privArr := state.IdentityKeyPair.PrivateKey().Serialize()
	privBytes := privArr[:]

	regIDBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(regIDBytes, state.RegistrationID)

	spkBytes := state.SignedPreKey.Serialize()

	return st.Update(func(txn *badger.Txn) error {
		if err := txn.Set([]byte(keyRegistrationID), regIDBytes); err != nil {
			return err
		}
		if err := txn.Set([]byte(keyIdentityPub), pubBytes); err != nil {
			return err
		}
		if err := txn.Set([]byte(keyIdentityPriv), privBytes); err != nil {
			return err
		}
		if err := txn.Set(spkKey(state.SignedPreKey.ID()), spkBytes); err != nil {
			return err
		}
		for _, opk := range state.OneTimePreKeys {
			if !opk.ID().IsEmpty {
				if err := txn.Set(opkKey(opk.ID().Value), opk.Serialize()); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func Load(st *store.Store) (*State, error) {
	ser := serialize.NewJSONSerializer()
	state := &State{}

	pubBytes, err := st.Get([]byte(keyIdentityPub))
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotInitialized
	}
	if err != nil {
		return nil, fmt.Errorf("signal: load identity pub: %w", err)
	}
	privBytes, err := st.Get([]byte(keyIdentityPriv))
	if err != nil {
		return nil, fmt.Errorf("signal: load identity priv: %w", err)
	}
	if len(privBytes) != 32 {
		return nil, fmt.Errorf("signal: identity priv length = %d, want 32", len(privBytes))
	}
	var priv32 [32]byte
	copy(priv32[:], privBytes)

	pub, err := ecc.DecodePoint(pubBytes, 0)
	if err != nil {
		return nil, fmt.Errorf("signal: decode identity pub: %w", err)
	}
	state.IdentityKeyPair = identity.NewKeyPair(identity.NewKey(pub), ecc.NewDjbECPrivateKey(priv32))

	regBytes, err := st.Get([]byte(keyRegistrationID))
	if err != nil {
		return nil, fmt.Errorf("signal: load reg id: %w", err)
	}
	if len(regBytes) != 4 {
		return nil, fmt.Errorf("signal: reg id length = %d, want 4", len(regBytes))
	}
	state.RegistrationID = binary.BigEndian.Uint32(regBytes)

	var (
		currentSPK   *record.SignedPreKey
		currentSPKID uint32
	)
	if err := st.View(func(txn *badger.Txn) error {
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
			candidate, err := decodeSignedPreKey(raw, ser.SignedPreKeyRecord)
			if err != nil {
				return fmt.Errorf("decode spk: %w", err)
			}
			if currentSPK == nil || candidate.ID() > currentSPKID {
				currentSPK = candidate
				currentSPKID = candidate.ID()
			}
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("signal: iterate spks: %w", err)
	}
	if currentSPK == nil {
		return nil, fmt.Errorf("signal: no signed prekey present after identity (corrupted state)")
	}
	state.SignedPreKey = currentSPK

	if err := st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(opkKeyPrefix)
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
			opk, err := decodePreKey(raw, ser.PreKeyRecord)
			if err != nil {
				return fmt.Errorf("decode opk: %w", err)
			}
			state.OneTimePreKeys = append(state.OneTimePreKeys, opk)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("signal: iterate opks: %w", err)
	}

	return state, nil
}

func (s *State) AvailableOPK() (uint32, []byte, error) {
	for _, k := range s.OneTimePreKeys {
		id := k.ID()
		if id.IsEmpty {
			continue
		}
		return id.Value, k.KeyPair().PublicKey().Serialize(), nil
	}
	return 0, nil, ErrOPKPoolEmpty
}

func decodeSignedPreKey(raw []byte, ser record.SignedPreKeySerializer) (*record.SignedPreKey, error) {
	st, err := ser.Deserialize(raw)
	if err != nil {
		return nil, err
	}
	st.PublicKey = stripDjbPrefix(st.PublicKey)
	return record.NewSignedPreKeyFromStruct(st, ser)
}

func decodePreKey(raw []byte, ser record.PreKeySerializer) (*record.PreKey, error) {
	st, err := ser.Deserialize(raw)
	if err != nil {
		return nil, err
	}
	st.PublicKey = stripDjbPrefix(st.PublicKey)
	return record.NewPreKeyFromStruct(st, ser)
}

func stripDjbPrefix(b []byte) []byte {
	if len(b) == 33 && b[0] == 0x05 {
		return b[1:]
	}
	return b
}

func LoadOrBootstrap(st *store.Store, opkCount int) (state *State, created bool, err error) {
	state, err = Load(st)
	if err == nil {
		return state, false, nil
	}
	if !errors.Is(err, ErrNotInitialized) {
		return nil, false, err
	}
	state, err = Bootstrap(opkCount)
	if err != nil {
		return nil, false, err
	}
	if err := Save(st, state); err != nil {
		return nil, false, fmt.Errorf("signal: save bootstrapped state: %w", err)
	}
	return state, true, nil
}

func ConsumeOneTimePreKey(st *store.Store, id uint32) error {
	return st.Delete(opkKey(id))
}

func opkKey(id uint32) []byte { return prefixedIDKey(opkKeyPrefix, id) }
func spkKey(id uint32) []byte { return prefixedIDKey(spkKeyPrefix, id) }

func prefixedIDKey(prefix string, id uint32) []byte {
	b := make([]byte, 0, len(prefix)+4)
	b = append(b, prefix...)
	var idBE [4]byte
	binary.BigEndian.PutUint32(idBE[:], id)
	b = append(b, idBE[:]...)
	return b
}

func remoteIdentKey(addr string) []byte {
	return []byte(remoteIdentPrefix + addr)
}

func sessionKey(addr string) []byte {
	return []byte(sessionPrefix + addr)
}

type PublicSummary struct {
	RegistrationID       uint32 `json:"registration_id"`
	IdentityFingerprint  string `json:"identity_fingerprint"`
	SignedPreKeyID       uint32 `json:"signed_pre_key_id"`
	OneTimePreKeyCount   int    `json:"one_time_pre_key_count"`
	OneTimePreKeyLowest  uint32 `json:"one_time_pre_key_lowest,omitempty"`
	OneTimePreKeyHighest uint32 `json:"one_time_pre_key_highest,omitempty"`
}

func (s *State) Summary() PublicSummary {
	out := PublicSummary{
		RegistrationID:      s.RegistrationID,
		IdentityFingerprint: s.IdentityKeyPair.PublicKey().Fingerprint(),
		SignedPreKeyID:      s.SignedPreKey.ID(),
		OneTimePreKeyCount:  len(s.OneTimePreKeys),
	}
	for i, opk := range s.OneTimePreKeys {
		if opk.ID().IsEmpty {
			continue
		}
		v := opk.ID().Value
		if i == 0 || v < out.OneTimePreKeyLowest {
			out.OneTimePreKeyLowest = v
		}
		if v > out.OneTimePreKeyHighest {
			out.OneTimePreKeyHighest = v
		}
	}
	return out
}

func (s *State) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.Summary())
}
