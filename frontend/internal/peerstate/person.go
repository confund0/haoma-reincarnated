package peerstate

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/dgraph-io/badger/v4"

	"haoma-frontend/internal/logid"
	"haoma-frontend/internal/store"
)

const personPrefix = "person:"

func personKey(fingerprint string) []byte {
	return []byte(personPrefix + fingerprint + suffixMeta)
}

func (m *Meta) GetPerson(fingerprint string) (MetaRecord, error) {
	if fingerprint == "" {
		return MetaRecord{}, errors.New("peerstate: empty fingerprint")
	}
	raw, err := m.st.Get(personKey(fingerprint))
	if err != nil {
		return MetaRecord{}, err
	}
	var rec MetaRecord
	if len(raw) == 0 {
		return rec, nil
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		return MetaRecord{}, fmt.Errorf("peerstate: decode person: %w", err)
	}
	return rec, nil
}

func (m *Meta) MergePerson(fingerprint string, in MetaRecord) (changed bool, err error) {
	if fingerprint == "" {
		return false, errors.New("peerstate: empty fingerprint")
	}
	cur, err := m.GetPerson(fingerprint)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	fresh := errors.Is(err, store.ErrNotFound)
	cur.Fingerprint = fingerprint

	if takeString(&cur.Nick, &cur.NickAt, in.Nick, in.NickAt) {
		changed = true
	} else if cur.Nick == "" && in.Nick != "" {
		cur.Nick, changed = in.Nick, true
	}

	if takeString(&cur.Alias, &cur.AliasAt, in.Alias, in.AliasAt) {
		changed = true
	}

	if in.VerifiedAt > cur.VerifiedAt || (fresh && in.Verified) {
		cur.Verified, cur.VerifiedAt, changed = in.Verified, in.VerifiedAt, true
	}
	if in.BlockedAt > cur.BlockedAt || (fresh && in.Blocked) {
		cur.Blocked, cur.BlockedAt, changed = in.Blocked, in.BlockedAt, true
	}
	if fresh {

		changed = true
	}
	if !changed {
		return false, nil
	}
	raw, mErr := json.Marshal(cur)
	if mErr != nil {
		return false, fmt.Errorf("peerstate: encode person: %w", mErr)
	}
	if pErr := m.st.Put(personKey(fingerprint), raw); pErr != nil {
		return false, pErr
	}
	return true, nil
}

type PersonSeed string

const (
	PersonSeedNoFingerprint PersonSeed = "no_fingerprint"

	PersonSeedNothingToSay PersonSeed = "nothing_to_say"

	PersonSeedCreated PersonSeed = "created"

	PersonSeedMerged PersonSeed = "merged"

	PersonSeedUnchanged PersonSeed = "unchanged"
)

func (m *Meta) SeedPersonFromPairing(peerID string) (fingerprint string, outcome PersonSeed, err error) {
	if peerID == "" {
		return "", "", errors.New("peerstate: empty peer id")
	}

	peer, err := m.peerRecord(peerID)
	if err != nil {
		return "", "", err
	}
	if peer.Fingerprint == "" {
		return "", PersonSeedNoFingerprint, nil
	}
	if peer.Nick == "" {

		return peer.Fingerprint, PersonSeedNothingToSay, nil
	}
	_, err = m.GetPerson(peer.Fingerprint)
	existed := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return peer.Fingerprint, "", err
	}
	changed, err := m.MergePerson(peer.Fingerprint, MetaRecord{
		Fingerprint: peer.Fingerprint,
		Nick:        peer.Nick,
		NickAt:      peer.NickAt,
	})
	if err != nil {
		return peer.Fingerprint, "", err
	}
	switch {
	case !existed:
		return peer.Fingerprint, PersonSeedCreated, nil
	case changed:
		return peer.Fingerprint, PersonSeedMerged, nil
	default:
		return peer.Fingerprint, PersonSeedUnchanged, nil
	}
}

func (m *Meta) allPersons() (map[string]MetaRecord, error) {
	out := map[string]MetaRecord{}
	err := m.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(personPrefix)
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.ValidForPrefix([]byte(personPrefix)); it.Next() {
			key := string(it.Item().Key())
			if !strings.HasSuffix(key, suffixMeta) {
				continue
			}
			fp := key[len(personPrefix) : len(key)-len(suffixMeta)]
			if fp == "" {
				continue
			}
			var rec MetaRecord
			if err := it.Item().Value(func(raw []byte) error {
				if len(raw) == 0 {
					return nil
				}
				return json.Unmarshal(raw, &rec)
			}); err != nil {
				return err
			}
			rec.Fingerprint = fp
			out[fp] = rec
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("peerstate: all persons: %w", err)
	}
	return out, nil
}

func unlinkedAtKey(fingerprint string) []byte {
	return []byte(personPrefix + fingerprint + suffixUnlinkedAt)
}

func countAtKey(fingerprint string) []byte {
	return []byte(personPrefix + fingerprint + suffixCountAt)
}

func (m *Meta) ObserveUnlinkNotice(fingerprint string, ts int64) (fresh bool, err error) {
	fresh, err = m.observeWatermark(fingerprint, unlinkedAtKey, ts)
	if err != nil {
		return false, fmt.Errorf("peerstate: observe unlink notice for %s: %w", logid.Short(fingerprint), err)
	}
	return fresh, nil
}

func (m *Meta) ObserveDeviceCount(fingerprint string, ts int64) (fresh bool, err error) {
	fresh, err = m.observeWatermark(fingerprint, countAtKey, ts)
	if err != nil {
		return false, fmt.Errorf("peerstate: observe device count for %s: %w", logid.Short(fingerprint), err)
	}
	return fresh, nil
}

func (m *Meta) observeWatermark(fingerprint string, keyOf func(string) []byte, ts int64) (fresh bool, err error) {
	if fingerprint == "" {
		return false, errors.New("peerstate: empty fingerprint")
	}
	if ts <= 0 {
		return false, nil
	}
	key := keyOf(fingerprint)
	err = m.st.Update(func(txn *badger.Txn) error {
		seen, lErr := loadInt64(txn, key)
		if lErr != nil {
			return lErr
		}
		if ts < seen {
			fresh = false
			return nil
		}
		fresh = true
		if ts == seen {
			return nil
		}
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], uint64(ts))
		return txn.Set(key, buf[:])
	})
	if err != nil {
		return false, err
	}
	return fresh, nil
}

func loadInt64(txn *badger.Txn, key []byte) (int64, error) {
	v, err := loadUint64(txn, key)
	return int64(v), err
}

const suffixSiblingDevices = ":sibling_devices"

func siblingDevicesKey(fingerprint string) []byte {
	return []byte(personPrefix + fingerprint + suffixSiblingDevices)
}

type SiblingDevices struct {
	Sibling string   `json:"sibling"`
	Devices []uint32 `json:"devices,omitempty"`
	AtMs    int64    `json:"at_ms"`
}

func (m *Meta) ObserveSiblingDevices(fingerprint, sibling string, devices []uint32, atMs int64) (changed bool, err error) {
	if fingerprint == "" || sibling == "" {
		return false, errors.New("peerstate: empty fingerprint or sibling")
	}
	if atMs <= 0 {
		return false, nil
	}
	in := SiblingDevices{Sibling: sibling, Devices: append([]uint32(nil), devices...), AtMs: atMs}
	key := siblingDevicesKey(fingerprint)
	err = m.st.Update(func(txn *badger.Txn) error {
		held, found, lErr := loadSiblingDevices(txn, key)
		if lErr != nil {
			return lErr
		}
		if found && held.Sibling == sibling {
			if atMs < held.AtMs {
				return nil
			}
			if sameDevices(held.Devices, in.Devices) {
				if atMs == held.AtMs {
					return nil
				}

				raw, mErr := json.Marshal(in)
				if mErr != nil {
					return mErr
				}
				return txn.Set(key, raw)
			}
		}
		raw, mErr := json.Marshal(in)
		if mErr != nil {
			return mErr
		}
		changed = true
		return txn.Set(key, raw)
	})
	if err != nil {
		return false, fmt.Errorf("peerstate: observe sibling devices for %s: %w", logid.Short(fingerprint), err)
	}
	return changed, nil
}

func (m *Meta) SiblingDevicesFor(fingerprint string) (rep SiblingDevices, found bool, err error) {
	if fingerprint == "" {
		return SiblingDevices{}, false, errors.New("peerstate: empty fingerprint")
	}
	err = m.st.View(func(txn *badger.Txn) error {
		rep, found, err = loadSiblingDevices(txn, siblingDevicesKey(fingerprint))
		return err
	})
	if err != nil {
		return SiblingDevices{}, false, fmt.Errorf("peerstate: sibling devices for %s: %w", logid.Short(fingerprint), err)
	}
	return rep, found, nil
}

func loadSiblingDevices(txn *badger.Txn, key []byte) (SiblingDevices, bool, error) {
	item, err := txn.Get(key)
	if errors.Is(err, badger.ErrKeyNotFound) {
		return SiblingDevices{}, false, nil
	}
	if err != nil {
		return SiblingDevices{}, false, err
	}
	var rep SiblingDevices
	if err := item.Value(func(v []byte) error { return json.Unmarshal(v, &rep) }); err != nil {
		return SiblingDevices{}, false, err
	}
	return rep, true, nil
}

func sameDevices(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
