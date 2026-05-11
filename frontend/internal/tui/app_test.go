package tui

import "testing"

func TestHexPreview_Empty(t *testing.T) {
	if got := hexPreview(nil, 32); got != "empty" {
		t.Errorf("nil = %q, want empty", got)
	}
	if got := hexPreview([]byte{}, 32); got != "empty" {
		t.Errorf("empty = %q, want empty", got)
	}
}

func TestHexPreview_ShortFits(t *testing.T) {
	got := hexPreview([]byte{0x00, 0xab, 0xff}, 32)
	if got != "00abff" {
		t.Errorf("got %q, want 00abff", got)
	}
}

func TestHexPreview_TruncatedWithEllipsis(t *testing.T) {
	got := hexPreview([]byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55}, 3)
	want := "001122" + "…"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCoalesce(t *testing.T) {
	if got := coalesce("", "fallback"); got != "fallback" {
		t.Errorf("empty → %q, want fallback", got)
	}
	if got := coalesce("value", "fallback"); got != "value" {
		t.Errorf("value → %q, want value", got)
	}
}

func TestDraftKey(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"status", "status"},
		{"contacts", "contacts"},
		{"chats", "chats"},
		{"settings", "settings"},
		{"chat:abc123", "abc123"},
		{"chat:", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := draftKey(c.in); got != c.want {
			t.Errorf("draftKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIsSensitiveHistoryInput(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{

		{"/set-tor-password", true},
		{"/change-pass", true},
		{"/change-pin", true},

		{"/set-tor-password hunter2", true},
		{"/change-pass new-passphrase here", true},
		{"/change-pin 4242", true},
		{"/change-pass\thunter2", true},

		{"/change-passport", false},
		{"/change-pinball", false},
		{"/set-tor-password-leak", false},

		{"/help", false},
		{"/nick alice", false},
		{"hello world", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isSensitiveHistoryInput(c.in); got != c.want {
			t.Errorf("isSensitiveHistoryInput(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
