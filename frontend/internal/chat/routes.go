package chat

import (
	"errors"
	"fmt"
	"slices"

	"github.com/dgraph-io/badger/v4"

	"haoma-frontend/internal/logid"
)

var ErrRoutesChanged = errors.New("chat: the routing set changed since it was read")

func (s *Store) DropRoutes(id ChatID, primary string, drop []string) (*DirectChat, bool, error) {
	if primary == "" {
		return nil, false, errors.New("chat: DropRoutes: empty primary")
	}
	dropSet := make(map[string]struct{}, len(drop))
	for _, pid := range drop {
		dropSet[pid] = struct{}{}
	}
	if _, ok := dropSet[primary]; ok {
		return nil, false, fmt.Errorf("chat: DropRoutes: %w: the primary %s is one of the pairings to drop", ErrRoutesChanged, logid.Short(primary))
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var (
		out     *DirectChat
		changed bool
	)
	err := s.st.Update(func(txn *badger.Txn) error {
		item, err := txn.Get(chatKey(id))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		var raw []byte
		if err := item.Value(func(v []byte) error {
			raw = append([]byte(nil), v...)
			return nil
		}); err != nil {
			return err
		}
		c, err := decodeRecord(raw)
		if err != nil {
			return err
		}
		dc, ok := c.(*DirectChat)
		if !ok {
			return fmt.Errorf("chat: DropRoutes: %s is not a direct chat", logid.Short(string(id)))
		}
		routes := dc.FanoutPeerIDs()
		found := false
		for _, pid := range routes {
			if pid == primary {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("chat: DropRoutes: %w: %s is not in the routing set of %s", ErrRoutesChanged, logid.Short(primary), logid.Short(string(id)))
		}
		next := make([]string, 0, len(routes))
		next = append(next, primary)
		for _, pid := range routes {
			if pid == primary {
				continue
			}
			if _, gone := dropSet[pid]; gone {
				continue
			}
			next = append(next, pid)
		}
		members := make([]string, 0, len(dc.Members))
		for _, pid := range dc.Members {
			if _, gone := dropSet[pid]; gone {
				continue
			}
			members = append(members, pid)
		}
		if dc.PeerID == primary && slices.Equal(routes, next) && len(members) == len(dc.Members) {
			out = dc
			return nil
		}
		dc.PeerID = primary
		dc.PeerIDs = next
		dc.Members = members
		encoded, err := encodeDirect(dc)
		if err != nil {
			return err
		}
		if err := txn.Set(chatKey(dc.ID), encoded); err != nil {
			return err
		}

		if err := txn.Set(byPeerKey(primary), []byte(dc.ID)); err != nil {
			return err
		}
		out = dc
		changed = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return out, changed, nil
}

func (s *Store) DropPairing(id ChatID, peerID string) (*DirectChat, error) {
	if peerID == "" {
		return nil, errors.New("chat: DropPairing: empty peer id")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var out *DirectChat
	err := s.st.Update(func(txn *badger.Txn) error {
		item, err := txn.Get(chatKey(id))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		var raw []byte
		if err := item.Value(func(v []byte) error {
			raw = append([]byte(nil), v...)
			return nil
		}); err != nil {
			return err
		}
		c, err := decodeRecord(raw)
		if err != nil {
			return err
		}
		dc, ok := c.(*DirectChat)
		if !ok {
			return fmt.Errorf("chat: DropPairing: %s is not a direct chat", logid.Short(string(id)))
		}
		if dc.PeerID == peerID {
			return fmt.Errorf("chat: DropPairing: %w: %s is the primary of %s", ErrRoutesChanged, logid.Short(peerID), logid.Short(string(id)))
		}
		dc.PeerIDs = slices.DeleteFunc(slices.Clone(dc.FanoutPeerIDs()), func(p string) bool { return p == peerID })
		dc.Members = slices.DeleteFunc(slices.Clone(dc.Members), func(p string) bool { return p == peerID })
		encoded, err := encodeDirect(dc)
		if err != nil {
			return err
		}
		if err := txn.Set(chatKey(dc.ID), encoded); err != nil {
			return err
		}

		pi, err := txn.Get(byPeerKey(peerID))
		if err == nil {
			var names bool
			if err := pi.Value(func(v []byte) error {
				names = string(v) == string(dc.ID)
				return nil
			}); err != nil {
				return err
			}
			if names {
				if err := txn.Delete(byPeerKey(peerID)); err != nil {
					return err
				}
			}
		} else if !errors.Is(err, badger.ErrKeyNotFound) {
			return err
		}
		out = dc
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func peerIndexEntriesFor(txn *badger.Txn, id ChatID) ([][]byte, error) {
	opts := badger.DefaultIteratorOptions
	opts.Prefix = []byte(prefixChatByPeer)
	it := txn.NewIterator(opts)
	defer it.Close()
	var keys [][]byte
	for it.Rewind(); it.Valid(); it.Next() {
		item := it.Item()
		var names bool
		if err := item.Value(func(v []byte) error {
			names = string(v) == string(id)
			return nil
		}); err != nil {
			return nil, err
		}
		if names {
			keys = append(keys, item.KeyCopy(nil))
		}
	}
	return keys, nil
}
