package logid_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"haoma-frontend/internal/logid"
)

func TestShort(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"abc", "abc"},
		{"12345678", "12345678"},
		{"123456789", "12345678…"},
		{"5c7b2897f04bdf9ebcd91c769cf84344", "5c7b2897…"},
	}
	for _, c := range cases {
		if got := logid.Short(c.in); got != c.want {
			t.Errorf("Short(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRedactOnions(t *testing.T) {
	in := `Post "http://dummyaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaad.onion/message": socks connect tcp 127.0.0.1:43039->dummyaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaad.onion:80: unknown error host unreachable`
	got := logid.RedactOnions(in)
	if strings.Contains(got, "dummyaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaad") {
		t.Errorf("full onion still present after redaction: %q", got)
	}
	if !strings.Contains(got, "dummyaaa….onion") {
		t.Errorf("expected short-id .onion form, got: %q", got)
	}

	if strings.Count(got, "dummyaaa….onion") != 2 {
		t.Errorf("expected 2 redacted occurrences, got %d in %q", strings.Count(got, "dummyaaa….onion"), got)
	}
}

func TestRedactOnions_NoMatch(t *testing.T) {
	in := "boring error with no onion"
	if got := logid.RedactOnions(in); got != in {
		t.Errorf("untouched string changed: %q -> %q", in, got)
	}
}

func TestHash(t *testing.T) {
	if got := logid.Hash(""); got != "" {
		t.Errorf("Hash(\"\") = %q, want empty", got)
	}
	a := logid.Hash("alice")
	b := logid.Hash("alice")
	if a != b {
		t.Errorf("Hash is not deterministic: %q vs %q", a, b)
	}
	c := logid.Hash("bob")
	if a == c {
		t.Error("Hash collided two distinct nicks")
	}
	if !strings.HasPrefix(a, "h:") {
		t.Errorf("Hash output should start with h:, got %q", a)
	}
	if strings.Contains(a, "alice") {
		t.Errorf("Hash leaked plaintext: %q", a)
	}
}

func TestRedactURLTokens(t *testing.T) {
	in := "http://dummyaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaad.onion/audio/zAB4Q4L62pyYMGXFqUIVTo5PLEFezNWXsRF0UjkeaiY"
	got := logid.RedactURLTokens(in)
	if strings.Contains(got, "dummyaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaad") {
		t.Errorf("onion not redacted: %q", got)
	}
	if strings.Contains(got, "zAB4Q4L62pyYMGXFqUIVTo5PLEFezNWXsRF0UjkeaiY") {
		t.Errorf("path token not redacted: %q", got)
	}
	if !strings.Contains(got, "/audio/") {
		t.Errorf("path structure should survive (so operator sees the modality): %q", got)
	}
	if !strings.Contains(got, "zAB4Q4L6…") {
		t.Errorf("expected short-token form in path: %q", got)
	}
}

func TestHasOnion(t *testing.T) {
	if logid.HasOnion("nothing here") {
		t.Error("HasOnion returned true on a plain string")
	}
	if !logid.HasOnion("http://dummybbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbd.onion/") {
		t.Error("HasOnion returned false on a URL containing a v3 onion")
	}
}

func TestRedactURLError(t *testing.T) {
	const (
		id    = "196d2fa679205436444893960a08e45c"
		onion = "dummyaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaad"
	)
	for _, u := range []string{"https://127.0.0.1:42123/inbox/" + id, "http://" + onion + ".onion/files/" + id} {
		got := fmt.Errorf("wrapped: %w", logid.RedactURLError(&url.Error{Op: "Delete", URL: u, Err: context.Canceled}))
		if strings.Contains(got.Error(), id) || strings.Contains(got.Error(), onion) {
			t.Errorf("a full value reached the error: %v", got)
		}
		if !strings.Contains(got.Error(), "/196d2fa6…") {
			t.Errorf("the error does not name the short id: %v", got)
		}
		if !errors.Is(got, context.Canceled) {
			t.Errorf("errors.Is(err, context.Canceled) = false for %v", got)
		}
	}
	plain := errors.New("plain " + id)
	if logid.RedactURLError(plain) != plain {
		t.Error("an error without a URL was changed")
	}
	if logid.RedactURLError(nil) != nil {
		t.Error("nil came back as an error")
	}
}
