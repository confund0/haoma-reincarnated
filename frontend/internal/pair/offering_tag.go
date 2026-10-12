package pair

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/dgraph-io/badger/v4"

	"haoma-frontend/internal/store"
)

const offeringTagDST = "haoma-offering-tag-v1/"

const offeringTagKeyRow = "pair_offering_tag_key"

const offeringTagKeyLen = 32

const OfferingTagLen = 16

var ErrOfferingTagKey = errors.New("pair: offering-tag key unavailable")

func OfferingDeviceTag(st *store.Store, fingerprint string) (string, error) {
	if st == nil {
		return "", errors.New("pair: nil store")
	}
	if fingerprint == "" {
		return "", errors.New("pair: empty fingerprint")
	}
	key, err := offeringTagKey(st)
	if err != nil {
		return "", err
	}
	defer func() {
		for i := range key {
			key[i] = 0
		}
	}()
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(offeringTagDST))
	mac.Write([]byte(fingerprint))
	return hex.EncodeToString(mac.Sum(nil)[:OfferingTagLen]), nil
}

func offeringTagKey(st *store.Store) ([]byte, error) {
	var key []byte
	err := st.Update(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(offeringTagKeyRow))
		switch {
		case err == nil:
			raw, err := item.ValueCopy(nil)
			if err != nil {
				return err
			}
			if len(raw) != offeringTagKeyLen {
				return fmt.Errorf("stored key is %d bytes, want %d", len(raw), offeringTagKeyLen)
			}
			key = raw
			return nil
		case errors.Is(err, badger.ErrKeyNotFound):
			fresh := make([]byte, offeringTagKeyLen)
			if _, err := rand.Read(fresh); err != nil {
				return err
			}
			if err := txn.Set([]byte(offeringTagKeyRow), fresh); err != nil {
				return err
			}
			key = fresh
			return nil
		default:
			return err
		}
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrOfferingTagKey, err)
	}
	return key, nil
}
