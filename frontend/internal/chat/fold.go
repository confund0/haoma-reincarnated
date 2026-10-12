package chat

import (
	"errors"
	"fmt"

	"github.com/dgraph-io/badger/v4"
)

func (s *Store) CreateDirectEdgeless(id ChatID, fingerprint string) (*DirectChat, error) {
	if id == "" {
		return nil, errors.New("chat: CreateDirectEdgeless: empty chat id")
	}
	if fingerprint == "" {
		return nil, errors.New("chat: CreateDirectEdgeless: empty fingerprint")
	}
	if err := s.runBeforeGrow(); err != nil {
		return nil, fmt.Errorf("chat: CreateDirectEdgeless: %w", err)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if existing, err := s.Get(id); err == nil {
		dc, ok := existing.(*DirectChat)
		if !ok {
			return nil, fmt.Errorf("chat: CreateDirectEdgeless: %s already exists and is not a direct chat", id)
		}
		if dc.Fingerprint != fingerprint {
			return nil, fmt.Errorf("chat: CreateDirectEdgeless: %s already belongs to a different person", id)
		}
		return dc, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if existing, err := s.GetByFingerprint(fingerprint); err == nil {
		return nil, fmt.Errorf("chat: CreateDirectEdgeless: that person already has chat %s", existing.ID)
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	dc := &DirectChat{
		BaseChat: BaseChat{
			ID:        id,
			CreatedAt: s.now().Unix(),
		},
		MaxMembers:  MaxMembersDirect,
		Fingerprint: fingerprint,
	}

	if err := s.putDirectLocked(dc); err != nil {
		return nil, err
	}
	return dc, nil
}

func (s *Store) GetByFingerprint(fingerprint string) (*DirectChat, error) {
	if fingerprint == "" {
		return nil, errors.New("chat: GetByFingerprint: empty fingerprint")
	}
	var id ChatID
	err := s.st.View(func(txn *badger.Txn) error {
		item, err := txn.Get(byFingerprintKey(fingerprint))
		if errors.Is(err, badger.ErrKeyNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return item.Value(func(v []byte) error {
			id = ChatID(append([]byte(nil), v...))
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	c, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	dc, ok := c.(*DirectChat)
	if !ok {
		return nil, fmt.Errorf("chat: fingerprint index for %s points at a non-direct chat", fingerprint)
	}
	return dc, nil
}

func (s *Store) AttachPeerID(id ChatID, peerID string) error {
	if peerID == "" {
		return errors.New("chat: AttachPeerID: empty peer id")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.st.Update(func(txn *badger.Txn) error {
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
			return fmt.Errorf("chat: AttachPeerID: %s is not a direct chat", id)
		}
		for _, existing := range dc.PeerIDs {
			if existing == peerID {

				return txn.Set(byPeerKey(peerID), []byte(dc.ID))
			}
		}
		dc.PeerIDs = append(dc.PeerIDs, peerID)
		dc.Members = append(dc.Members, peerID)
		if dc.PeerID == "" {

			dc.PeerID = dc.PeerIDs[0]
		}
		encoded, err := encodeDirect(dc)
		if err != nil {
			return err
		}
		if err := txn.Set(chatKey(dc.ID), encoded); err != nil {
			return err
		}
		return txn.Set(byPeerKey(peerID), []byte(dc.ID))
	})
}
