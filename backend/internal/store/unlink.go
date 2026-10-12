package store

import (
	"encoding/json"
	"errors"
	"fmt"
)

const unlinkStateKey = "unlink-pending"

type UnlinkPending struct {
	PeerID string `json:"peer_id"`

	Deadline int64 `json:"deadline"`

	StartedAt int64 `json:"started_at"`

	Proof string `json:"proof,omitempty"`
}

func (u UnlinkPending) Armed() bool { return u.InFlight() && u.Proof != "" }

func (u UnlinkPending) InFlight() bool { return u.PeerID != "" }

func (s *Store) GetUnlinkPending() (UnlinkPending, error) {
	raw, err := s.GetState(unlinkStateKey)
	if errors.Is(err, ErrNotFound) {
		return UnlinkPending{}, nil
	}
	if err != nil {
		return UnlinkPending{}, fmt.Errorf("store: read unlink state: %w", err)
	}
	var rec UnlinkPending
	if err := json.Unmarshal(raw, &rec); err != nil {
		return UnlinkPending{}, fmt.Errorf("store: parse unlink state: %w", err)
	}
	return rec, nil
}

var ErrUnlinkAlreadyPending = errors.New("store: an unlink is already in flight")

func (s *Store) PutUnlinkPending(rec UnlinkPending) error {
	if rec.PeerID == "" {
		return errors.New("store: unlink record names no peer")
	}
	if rec.Deadline == 0 {
		return errors.New("store: unlink record has no deadline")
	}

	s.destroyMu.Lock()
	defer s.destroyMu.Unlock()

	existing, err := s.GetUnlinkPending()
	if err != nil {
		return err
	}
	if existing.InFlight() && existing.PeerID != rec.PeerID {
		return ErrUnlinkAlreadyPending
	}

	raw, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("store: encode unlink state: %w", err)
	}
	return s.PutState(unlinkStateKey, raw)
}

func (s *Store) ArmUnlinkSelfErase(peerID, proof string) error {
	if peerID == "" {
		return errors.New("store: arm names no peer")
	}
	if proof == "" {
		return errors.New("store: arm states no proof")
	}

	s.destroyMu.Lock()
	defer s.destroyMu.Unlock()

	rec, err := s.GetUnlinkPending()
	if err != nil {
		return err
	}
	if !rec.InFlight() {
		return errors.New("store: no unlink is in flight to arm")
	}
	if rec.PeerID != peerID {
		return ErrUnlinkAlreadyPending
	}
	rec.Proof = proof
	raw, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("store: encode unlink state: %w", err)
	}
	return s.PutState(unlinkStateKey, raw)
}

func (s *Store) ClearUnlinkPending() error {
	s.destroyMu.Lock()
	defer s.destroyMu.Unlock()

	if err := s.DeleteState(unlinkStateKey); err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("store: clear unlink state: %w", err)
	}
	return nil
}
