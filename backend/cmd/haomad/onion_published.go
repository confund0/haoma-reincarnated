package main

import (
	"errors"
	"sync"

	"haoma/internal/tor/control"
)

type publishedOnions struct {
	mu   sync.Mutex
	conn *control.Conn
	ids  map[string]struct{}
}

func (s *publishedOnions) publishOnce(conn *control.Conn, serviceID string, add func() (string, error)) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if serviceID != "" && s.conn == conn {
		if _, ok := s.ids[serviceID]; ok {
			return false, nil
		}
	}
	published, err := add()
	if published != "" {
		s.recordLocked(conn, published)
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *publishedOnions) record(conn *control.Conn, serviceID string) {
	if serviceID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordLocked(conn, serviceID)
}

func (s *publishedOnions) recordLocked(conn *control.Conn, serviceID string) {
	if s.conn != conn || s.ids == nil {
		s.conn = conn
		s.ids = make(map[string]struct{})
	}
	s.ids[serviceID] = struct{}{}
}

func (s *publishedOnions) has(conn *control.Conn, serviceID string) bool {
	if serviceID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != conn {
		return false
	}
	_, ok := s.ids[serviceID]
	return ok
}

func (s *publishedOnions) forget(conn *control.Conn, serviceID string, delErr error) {
	if serviceID == "" || !torDroppedOnion(delErr) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != conn {
		return
	}
	delete(s.ids, serviceID)
}

func torDroppedOnion(err error) bool {
	return err == nil || errors.Is(err, control.ErrUnknownOnionService)
}
