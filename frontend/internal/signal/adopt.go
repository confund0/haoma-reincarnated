package signal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"

	"github.com/dgraph-io/badger/v4"
	"go.mau.fi/libsignal/ecc"
	"go.mau.fi/libsignal/keys/identity"
	"go.mau.fi/libsignal/serialize"
	"go.mau.fi/libsignal/state/record"
	"go.mau.fi/libsignal/util/keyhelper"

	"haoma-frontend/internal/devicetag"
	"haoma-frontend/internal/store"
)

var ErrAdoptKeyMismatch = errors.New("signal: adopted identity public key does not derive from the private key")

var ErrAdoptKeyShape = errors.New("signal: adopted identity key wrong length")

const opkErasedThroughKey = "signal:opk_erased_through"

func AdoptIdentity(st *store.Store, state *State, pub, priv []byte) error {
	if st == nil {
		return errors.New("signal: nil store")
	}
	if state == nil {
		return errors.New("signal: nil state")
	}
	if len(pub) != 33 {
		return fmt.Errorf("%w: public is %d bytes, want 33", ErrAdoptKeyShape, len(pub))
	}
	if len(priv) != 32 {
		return fmt.Errorf("%w: private is %d bytes, want 32", ErrAdoptKeyShape, len(priv))
	}

	derived := ecc.CreateKeyPair(priv)
	derivedPub := derived.PublicKey().Serialize()
	if len(derivedPub) != len(pub) {
		return ErrAdoptKeyMismatch
	}
	for i := range pub {
		if pub[i] != derivedPub[i] {
			return ErrAdoptKeyMismatch
		}
	}

	pubPoint, err := ecc.DecodePoint(pub, 0)
	if err != nil {
		return fmt.Errorf("signal: decode adopted identity pub: %w", err)
	}
	var priv32 [32]byte
	copy(priv32[:], priv)
	adopted := identity.NewKeyPair(identity.NewKey(pubPoint), ecc.NewDjbECPrivateKey(priv32))

	opkCount := len(state.OneTimePreKeys)
	if opkCount < 1 {
		opkCount = DefaultOPKCount
	}

	ser := serialize.NewJSONSerializer()

	var (
		newSPK        *record.SignedPreKey
		newOPKs       []*record.PreKey
		erasedThrough uint32
	)
	err = st.Update(func(txn *badger.Txn) error {
		spkVictims, maxSPKID, err := collectPrefix(txn, spkKeyPrefix)
		if err != nil {
			return fmt.Errorf("scan signed prekeys: %w", err)
		}
		opkVictims, maxOPKID, err := collectPrefix(txn, opkKeyPrefix)
		if err != nil {
			return fmt.Errorf("scan one-time prekeys: %w", err)
		}

		newSPK, err = keyhelper.GenerateSignedPreKey(adopted, maxSPKID+1, ser.SignedPreKeyRecord)
		if err != nil {
			return fmt.Errorf("generate signed prekey: %w", err)
		}
		opkStart := int(maxOPKID) + 1
		newOPKs, err = keyhelper.GeneratePreKeys(opkStart, opkStart+opkCount-1, ser.PreKeyRecord)
		if err != nil {
			return fmt.Errorf("generate one-time prekeys: %w", err)
		}

		privArr := adopted.PrivateKey().Serialize()
		if err := txn.Set([]byte(keyIdentityPub), adopted.PublicKey().Serialize()); err != nil {
			return err
		}
		if err := txn.Set([]byte(keyIdentityPriv), privArr[:]); err != nil {
			return err
		}

		for _, k := range append(spkVictims, opkVictims...) {
			if err := txn.Delete(k); err != nil {
				return err
			}
		}
		if err := txn.Set(spkKey(newSPK.ID()), newSPK.Serialize()); err != nil {
			return err
		}
		for _, opk := range newOPKs {
			id := opk.ID()
			if id.IsEmpty {
				continue
			}
			if err := txn.Set(opkKey(id.Value), opk.Serialize()); err != nil {
				return err
			}
		}

		if maxOPKID > 0 {
			erasedThrough, err = recordErasedThrough(txn, maxOPKID)
			if err != nil {
				return fmt.Errorf("record the erased one-time prekey boundary: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("signal: adopt identity: %w", err)
	}

	state.IdentityKeyPair = adopted
	state.SignedPreKey = newSPK
	state.OneTimePreKeys = newOPKs

	slog.Debug("signal: identity adopted",
		slog.String("fingerprint", adopted.PublicKey().Fingerprint()),
		slog.Uint64("signed_pre_key_id", uint64(newSPK.ID())),
		slog.Int("one_time_pre_keys", len(newOPKs)),
		slog.Uint64("one_time_pre_keys_erased_through", uint64(erasedThrough)),
		slog.String("registration_id_kept", devicetag.Tag(state.RegistrationID)),
	)
	return nil
}

func recordErasedThrough(txn *badger.Txn, id uint32) (uint32, error) {
	prev, err := readErasedThrough(txn)
	if err != nil {
		return 0, err
	}
	if prev >= id {
		return prev, nil
	}
	var be [4]byte
	binary.BigEndian.PutUint32(be[:], id)
	if err := txn.Set([]byte(opkErasedThroughKey), be[:]); err != nil {
		return 0, err
	}
	return id, nil
}

func readErasedThrough(txn *badger.Txn) (uint32, error) {
	item, err := txn.Get([]byte(opkErasedThroughKey))
	if errors.Is(err, badger.ErrKeyNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var out uint32
	err = item.Value(func(v []byte) error {
		if len(v) != 4 {
			return fmt.Errorf("adoption boundary is %d bytes, want 4", len(v))
		}
		out = binary.BigEndian.Uint32(v)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return out, nil
}

func collectPrefix(txn *badger.Txn, prefix string) (keys [][]byte, maxID uint32, err error) {
	opts := badger.DefaultIteratorOptions
	opts.Prefix = []byte(prefix)
	opts.PrefetchValues = false
	it := txn.NewIterator(opts)
	defer it.Close()
	for it.Rewind(); it.Valid(); it.Next() {
		k := it.Item().KeyCopy(nil)
		keys = append(keys, k)
		if len(k) == len(prefix)+4 {
			if id := binary.BigEndian.Uint32(k[len(prefix):]); id > maxID {
				maxID = id
			}
		}
	}
	return keys, maxID, nil
}
