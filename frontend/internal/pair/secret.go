package pair

import (
	"encoding/json"
	"errors"
	"fmt"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/store"
)

const pairMyKeysPrefix = "pair_mykeys:"

func SaveMyKeys(st *store.Store, handle string, keys *MyKeys, minted backendapi.MintedOnion) error {
	if handle == "" {
		return errors.New("pair: empty handle")
	}
	if keys == nil {
		return errors.New("pair: nil MyKeys")
	}
	if keys.PeerID == "" {
		return errors.New("pair: empty MyKeys.PeerID")
	}
	if len(keys.OutboundSecret) != SecretLen {
		return fmt.Errorf("pair: MyKeys.OutboundSecret length %d, want %d", len(keys.OutboundSecret), SecretLen)
	}
	if minted.Address == "" || minted.PrivateKey == "" {
		return errors.New("pair: empty MintedOnion (mint via /onion/mint before SaveMyKeys)")
	}
	raw, err := json.Marshal(persistedMyKeys{
		PeerID:            keys.PeerID,
		OutboundSecret:    keys.OutboundSecret,
		MyOnionAddr:       minted.Address,
		MyOnionPrivateKey: minted.PrivateKey,
	})
	if err != nil {
		return fmt.Errorf("pair: marshal MyKeys: %w", err)
	}
	return st.Put(myKeysKey(handle), raw)
}

func LoadMyKeys(st *store.Store, handle string) (*MyKeys, backendapi.MintedOnion, error) {
	if handle == "" {
		return nil, backendapi.MintedOnion{}, errors.New("pair: empty handle")
	}
	raw, err := st.Get(myKeysKey(handle))
	if errors.Is(err, store.ErrNotFound) {
		return nil, backendapi.MintedOnion{}, ErrMyKeysNotFound
	}
	if err != nil {
		return nil, backendapi.MintedOnion{}, fmt.Errorf("pair: load MyKeys for %s: %w", handle, err)
	}
	var p persistedMyKeys
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, backendapi.MintedOnion{}, fmt.Errorf("pair: decode MyKeys for %s: %w", handle, err)
	}
	if p.PeerID == "" {
		return nil, backendapi.MintedOnion{}, fmt.Errorf("pair: stored MyKeys for %s has empty peer-id", handle)
	}
	if len(p.OutboundSecret) != SecretLen {
		return nil, backendapi.MintedOnion{}, fmt.Errorf("pair: stored MyKeys for %s outbound length %d, want %d", handle, len(p.OutboundSecret), SecretLen)
	}
	return &MyKeys{
			PeerID:         p.PeerID,
			OutboundSecret: p.OutboundSecret,
		}, backendapi.MintedOnion{
			Address:    p.MyOnionAddr,
			PrivateKey: p.MyOnionPrivateKey,
		}, nil
}

func DeleteMyKeys(st *store.Store, handle string) error {
	if handle == "" {
		return errors.New("pair: empty handle")
	}
	if err := st.Delete(myKeysKey(handle)); err != nil {
		return fmt.Errorf("pair: delete MyKeys for %s: %w", handle, err)
	}
	return nil
}

var ErrMyKeysNotFound = errors.New("pair: no stored MyKeys for handle")

type persistedMyKeys struct {
	PeerID            string `json:"peer_id"`
	OutboundSecret    []byte `json:"outbound_secret"`
	MyOnionAddr       string `json:"my_onion_addr,omitempty"`
	MyOnionPrivateKey string `json:"my_onion_private_key,omitempty"`
}

func myKeysKey(handle string) []byte {
	out := make([]byte, 0, len(pairMyKeysPrefix)+len(handle))
	out = append(out, pairMyKeysPrefix...)
	out = append(out, handle...)
	return out
}
