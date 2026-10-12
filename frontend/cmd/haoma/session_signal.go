package main

import "sync"

type sessionSignal struct {
	mu   sync.Mutex
	next chan struct{}
}

func (s *sessionSignal) wait() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.next == nil {
		s.next = make(chan struct{})
	}
	return s.next
}

func (s *sessionSignal) fire() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.next != nil {
		close(s.next)
		s.next = nil
	}
}
