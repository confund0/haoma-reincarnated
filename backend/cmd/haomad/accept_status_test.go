package main

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"haoma/internal/pair"
)

func TestAcceptFailureStatus(t *testing.T) {
	wrap := func(err error) error { return fmt.Errorf("pair: derive: %w", err) }
	for _, c := range []struct {
		name string
		err  error
		want int
	}{
		{"a word not in the list, wrapped", wrap(pair.ErrInvalidEFFShortWord{Word: "nope"}), http.StatusUnprocessableEntity},
		{"the wrong number of words, wrapped", wrap(fmt.Errorf("%w: 6, want 7", pair.ErrOnionWordCount)), http.StatusUnprocessableEntity},
		{"a rendezvous already answered", pair.ErrRendezvousSpent, http.StatusGone},
		{"a rejected handshake", pair.ErrMACMismatch, http.StatusUnauthorized},
		{"a dial that timed out", fmt.Errorf("pair: dial onion: %w", errors.New("context deadline exceeded")), http.StatusBadGateway},
	} {
		if got := acceptFailureStatus(c.err); got != c.want {
			t.Errorf("%s: status %d, want %d", c.name, got, c.want)
		}
	}
}
