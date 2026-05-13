package logid_test

import (
	"strings"
	"testing"

	"haoma/internal/logid"
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
	in := `Post "http://s3zsmprypzrpuwwppb3isi4aeuexquidnnuiboj2qmmmot3ykffx7zyd.onion/message": socks connect tcp 127.0.0.1:43039->s3zsmprypzrpuwwppb3isi4aeuexquidnnuiboj2qmmmot3ykffx7zyd.onion:80: unknown error host unreachable`
	got := logid.RedactOnions(in)
	if strings.Contains(got, "s3zsmprypzrpuwwppb3isi4aeuexquidnnuiboj2qmmmot3ykffx7zyd") {
		t.Errorf("full onion still present after redaction: %q", got)
	}
	if !strings.Contains(got, "s3zsmpry….onion") {
		t.Errorf("expected short-id .onion form, got: %q", got)
	}
	if strings.Count(got, "s3zsmpry….onion") != 2 {
		t.Errorf("expected 2 redacted occurrences, got %d in %q", strings.Count(got, "s3zsmpry….onion"), got)
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
	in := "http://s3zsmprypzrpuwwppb3isi4aeuexquidnnuiboj2qmmmot3ykffx7zyd.onion/audio/zAB4Q4L62pyYMGXFqUIVTo5PLEFezNWXsRF0UjkeaiY"
	got := logid.RedactURLTokens(in)
	if strings.Contains(got, "s3zsmprypzrpuwwppb3isi4aeuexquidnnuiboj2qmmmot3ykffx7zyd") {
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
	if !logid.HasOnion("http://uhrvvclzwwdfrxm6f336w6qiidgxniw6vbzij7j6rdh2scpxlzgppwqd.onion/") {
		t.Error("HasOnion returned false on a URL containing a v3 onion")
	}
}
