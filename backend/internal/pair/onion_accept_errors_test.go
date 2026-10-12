package pair

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOnionDriver_AcceptInvite_ErrorsSayWhatWentWrong(t *testing.T) {
	t.Parallel()
	words := []string{"acid", "acorn", "acre", "acts", "afar", "affix", "aged"}

	t.Run("a word not in the list", func(t *testing.T) {
		d := NewOnionDriver(nil, &directDialer{target: "127.0.0.1:0"})
		_, err := d.AcceptInvite(context.Background(), AcceptRequest{
			Blob:    InviteBlob{Words: []string{"acid", "acorn", "definitelynotaword", "acts", "afar", "affix", "aged"}},
			Payload: []byte("x"),
		})
		var wordErr ErrInvalidEFFShortWord
		if !errors.As(err, &wordErr) {
			t.Fatalf("err = %v, want one that unwraps to ErrInvalidEFFShortWord", err)
		}
	})

	t.Run("the wrong number of words", func(t *testing.T) {
		d := NewOnionDriver(nil, &directDialer{target: "127.0.0.1:0"})
		_, err := d.AcceptInvite(context.Background(), AcceptRequest{
			Blob:    InviteBlob{Words: words[:6]},
			Payload: []byte("x"),
		})
		if !errors.Is(err, ErrOnionWordCount) {
			t.Fatalf("err = %v, want ErrOnionWordCount", err)
		}
	})

	t.Run("a rendezvous already answered", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "rendezvous already consumed", http.StatusGone)
		}))
		defer srv.Close()
		d := NewOnionDriver(nil, &directDialer{target: strings.TrimPrefix(srv.URL, "http://")})
		_, err := d.AcceptInvite(context.Background(), AcceptRequest{
			Blob:    InviteBlob{Words: words},
			Payload: []byte("x"),
		})
		if !errors.Is(err, ErrRendezvousSpent) {
			t.Fatalf("err = %v, want ErrRendezvousSpent", err)
		}
	})
}
