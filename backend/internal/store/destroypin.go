package store

import (
	"encoding/json"
	"errors"
	"fmt"
)

const destroyPINStateKey = "destroy-pin"

const destroyPINVersion = 1

var ErrDestroyPINVersion = errors.New("store: unsupported destroy pin record version")

type DestroyPIN struct {
	Version int `json:"version"`

	Set bool `json:"set"`

	Salt []byte `json:"salt"`

	Verifier []byte `json:"verifier"`

	SiblingPeerID string `json:"sibling_peer_id"`

	SetAt int64 `json:"set_at"`
}

const destroyAttemptsStateKey = "destroy-pin-attempts"

type DestroyAttempts struct {
	Failures int `json:"failures"`

	BlockedUntil int64 `json:"blocked_until,omitempty"`

	Unseen int   `json:"unseen,omitempty"`
	LastAt int64 `json:"last_at,omitempty"`
}

func (s *Store) GetDestroyAttempts() (DestroyAttempts, error) {
	raw, err := s.GetState(destroyAttemptsStateKey)
	if errors.Is(err, ErrNotFound) {
		return DestroyAttempts{}, nil
	}
	if err != nil {
		return DestroyAttempts{}, fmt.Errorf("store: read destroy attempts: %w", err)
	}
	var rec DestroyAttempts
	if err := json.Unmarshal(raw, &rec); err != nil {
		return DestroyAttempts{}, fmt.Errorf("store: parse destroy attempts: %w", err)
	}
	return rec, nil
}

func (s *Store) PutDestroyAttempts(rec DestroyAttempts) error {
	raw, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("store: encode destroy attempts: %w", err)
	}
	return s.PutState(destroyAttemptsStateKey, raw)
}

func (s *Store) UpdateDestroyAttempts(fn func(DestroyPIN, DestroyAttempts) (DestroyAttempts, bool, error)) error {
	s.destroyMu.Lock()
	defer s.destroyMu.Unlock()

	guard, err := s.GetDestroyPIN()
	if err != nil {
		return err
	}
	att, err := s.GetDestroyAttempts()
	if err != nil {
		return err
	}
	next, write, err := fn(guard, att)
	if err != nil || !write {
		return err
	}
	return s.PutDestroyAttempts(next)
}

func (s *Store) DrainDestroyAttemptNotices() (count int, lastAt int64, err error) {
	s.destroyMu.Lock()
	defer s.destroyMu.Unlock()

	att, err := s.GetDestroyAttempts()
	if err != nil {
		return 0, 0, err
	}
	if att.Unseen == 0 {
		return 0, 0, nil
	}
	count, lastAt = att.Unseen, att.LastAt
	att.Unseen, att.LastAt = 0, 0
	if err := s.PutDestroyAttempts(att); err != nil {
		return 0, 0, err
	}
	return count, lastAt, nil
}

func (s *Store) GetDestroyPIN() (DestroyPIN, error) {
	raw, err := s.GetState(destroyPINStateKey)
	if errors.Is(err, ErrNotFound) {
		return DestroyPIN{}, nil
	}
	if err != nil {
		return DestroyPIN{}, fmt.Errorf("store: read destroy pin: %w", err)
	}
	var rec DestroyPIN
	if err := json.Unmarshal(raw, &rec); err != nil {
		return DestroyPIN{}, fmt.Errorf("store: parse destroy pin: %w", err)
	}
	if rec.Version != destroyPINVersion {
		return DestroyPIN{}, fmt.Errorf("%w: %d", ErrDestroyPINVersion, rec.Version)
	}
	return rec, nil
}

var ErrDestroyPINAlreadySet = errors.New("store: destroy pin is already set")

func (s *Store) PutDestroyPIN(rec DestroyPIN) error {
	if len(rec.Salt) == 0 {
		return errors.New("store: destroy pin record has no salt")
	}
	if len(rec.Verifier) == 0 {
		return errors.New("store: destroy pin record has no verifier")
	}
	if rec.SiblingPeerID == "" {

		return errors.New("store: destroy pin record has no sibling peer id")
	}

	s.destroyMu.Lock()
	defer s.destroyMu.Unlock()

	existing, err := s.GetDestroyPIN()
	if err != nil {
		return err
	}
	if existing.Set {
		return ErrDestroyPINAlreadySet
	}
	rec.Version = destroyPINVersion
	rec.Set = true
	raw, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("store: encode destroy pin: %w", err)
	}
	return s.PutState(destroyPINStateKey, raw)
}

func (s *Store) ClearDestroyPIN() error {
	s.destroyMu.Lock()
	defer s.destroyMu.Unlock()

	if err := s.DeleteState(destroyPINStateKey); err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("store: clear destroy pin: %w", err)
	}
	if err := s.DeleteState(destroyAttemptsStateKey); err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("store: clear destroy attempts: %w", err)
	}
	return nil
}
