package peerstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/dgraph-io/badger/v4"
)

func (m *Meta) PeerIDsByFingerprint(fingerprint string) ([]string, error) {
	if fingerprint == "" {
		return nil, errors.New("peerstate: empty fingerprint")
	}
	var out []string
	err := m.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefix)
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.ValidForPrefix([]byte(prefix)); it.Next() {
			item := it.Item()
			peerID, ok := peerIDFromMetaKey(string(item.Key()))
			if !ok {

				continue
			}
			var rec MetaRecord
			if err := item.Value(func(raw []byte) error {
				if len(raw) == 0 {
					return nil
				}
				return json.Unmarshal(raw, &rec)
			}); err != nil {
				return err
			}
			if rec.Fingerprint == fingerprint {
				out = append(out, peerID)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("peerstate: peers by fingerprint: %w", err)
	}
	sort.Strings(out)
	return out, nil
}

func peerIDFromMetaKey(key string) (string, bool) {
	if !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, suffixMeta) {
		return "", false
	}
	id := key[len(prefix) : len(key)-len(suffixMeta)]
	if id == "" {
		return "", false
	}
	return id, true
}

type Person struct {
	Fingerprint string
	PeerIDs     []string
	Meta        MetaRecord
}

func (m *Meta) Persons() ([]Person, error) {
	byFP := map[string]*Person{}
	err := m.st.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(prefix)
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.ValidForPrefix([]byte(prefix)); it.Next() {
			item := it.Item()
			peerID, ok := peerIDFromMetaKey(string(item.Key()))
			if !ok {
				continue
			}
			var rec MetaRecord
			if err := item.Value(func(raw []byte) error {
				if len(raw) == 0 {
					return nil
				}
				return json.Unmarshal(raw, &rec)
			}); err != nil {
				return err
			}
			if rec.Fingerprint == "" {
				continue
			}
			p, seen := byFP[rec.Fingerprint]
			if !seen {
				p = &Person{Fingerprint: rec.Fingerprint}
				byFP[rec.Fingerprint] = p
			}
			p.PeerIDs = append(p.PeerIDs, peerID)
			foldMeta(&p.Meta, rec)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("peerstate: persons: %w", err)
	}

	inherited, err := m.allPersons()
	if err != nil {
		return nil, err
	}
	for fp, rec := range inherited {
		p, seen := byFP[fp]
		if !seen {
			p = &Person{Fingerprint: fp}
			byFP[fp] = p
		}
		foldMeta(&p.Meta, rec)
	}
	out := make([]Person, 0, len(byFP))
	for _, p := range byFP {
		sort.Strings(p.PeerIDs)
		out = append(out, *p)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Fingerprint < out[j].Fingerprint })
	return out, nil
}

func foldMeta(into *MetaRecord, rec MetaRecord) {
	if into.Fingerprint == "" {
		into.Fingerprint = rec.Fingerprint
	}

	if !takeString(&into.Nick, &into.NickAt, rec.Nick, rec.NickAt) {
		if into.Nick == "" && rec.Nick != "" {
			into.Nick = rec.Nick
		}
	}
	takeString(&into.Alias, &into.AliasAt, rec.Alias, rec.AliasAt)
	if rec.VerifiedAt > into.VerifiedAt || (into.VerifiedAt == 0 && rec.Verified) {
		into.Verified, into.VerifiedAt = rec.Verified, rec.VerifiedAt
	}
	if rec.BlockedAt > into.BlockedAt || (into.BlockedAt == 0 && rec.Blocked) {
		into.Blocked, into.BlockedAt = rec.Blocked, rec.BlockedAt
	}
}
