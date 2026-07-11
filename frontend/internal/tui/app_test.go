package tui

import (
	"sort"
	"strings"
	"testing"
)

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

func TestLongestCommonPrefix(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{"/invite-tor"}, "/invite-tor"},
		{[]string{"/invite-tor", "/invite-dht", "/invite-file"}, "/invite-"},
		{[]string{"/call", "/close"}, "/c"},
		{[]string{"/nick", "/msg"}, "/"},
		{[]string{"nick", "msg"}, ""},
	}
	for _, c := range cases {
		if got := longestCommonPrefix(c.in); got != c.want {
			t.Errorf("longestCommonPrefix(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCompleteCommandToken(t *testing.T) {
	cmds := []string{"/msg", "/nick", "/invite-tor", "/invite-dht", "/invite-file", "/call", "/close"}
	cases := []struct {
		name        string
		in          string
		wantReplace string
		wantList    []string
	}{

		{"empty", "", "", nil},
		{"plain text", "hello", "", nil},
		{"past the token", "/msg alice", "", nil},
		{"trailing space commits the token", "/msg ", "", nil},

		{"unique", "/ni", "/nick ", nil},
		{"unique full", "/call", "/call ", nil},

		{"extend to common", "/i", "/invite-", nil},

		{"list at dead end", "/invite-", "", []string{"/invite-dht", "/invite-file", "/invite-tor"}},

		{"no match", "/zzz", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := completeCommandToken(c.in, cmds)
			if got.replace != c.wantReplace {
				t.Errorf("replace = %q, want %q", got.replace, c.wantReplace)
			}
			if strings.Join(got.list, " ") != strings.Join(c.wantList, " ") {
				t.Errorf("list = %v, want %v", got.list, c.wantList)
			}
		})
	}
}

func TestSlashCommandsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range slashCommands {
		if !strings.HasPrefix(c, "/") {
			t.Errorf("%q missing leading slash", c)
		}
		if strings.ContainsAny(c, " \t") {
			t.Errorf("%q contains whitespace", c)
		}
		if seen[c] {
			t.Errorf("%q duplicated", c)
		}
		seen[c] = true
	}
	if !sort.StringsAreSorted(slashCommands) {

		t.Errorf("slashCommands is not sorted")
	}

	for c := range chatOnlyCommands {
		if !seen[c] {
			t.Errorf("chatOnlyCommands has %q not in slashCommands", c)
		}
		if statusOnlyCommands[c] {
			t.Errorf("%q is in both chatOnlyCommands and statusOnlyCommands", c)
		}
	}
	for c := range statusOnlyCommands {
		if !seen[c] {
			t.Errorf("statusOnlyCommands has %q not in slashCommands", c)
		}
	}
}

func setOf(ss []string) map[string]bool {
	m := map[string]bool{}
	for _, s := range ss {
		m[s] = true
	}
	return m
}

func TestCommandsForContext(t *testing.T) {
	chat := setOf(commandsForContext(scopeChat))
	status := setOf(commandsForContext(scopeStatus))
	other := setOf(commandsForContext(scopeOther))

	for _, c := range []string{"/edit", "/react", "/new-circuit", "/rotate-tor"} {
		if !chat[c] {
			t.Errorf("chat scope missing chat-only %q", c)
		}
	}
	for _, c := range []string{"/invite-tor", "/accept-tor", "/about", "/set-tor-password"} {
		if chat[c] {
			t.Errorf("chat scope offered status-only %q", c)
		}
	}

	for _, c := range []string{"/invite-tor", "/accept-tor", "/about", "/set-tor-password"} {
		if !status[c] {
			t.Errorf("status scope missing status-only %q", c)
		}
	}
	for _, c := range []string{"/edit", "/react", "/rotate-tor"} {
		if status[c] {
			t.Errorf("status scope offered chat-only %q", c)
		}
	}

	for _, c := range []string{"/edit", "/invite-tor", "/about"} {
		if other[c] {
			t.Errorf("other scope offered scoped %q", c)
		}
	}
	for _, c := range []string{"/help", "/quit", "/peers", "/chats"} {
		if !other[c] {
			t.Errorf("other scope missing global %q", c)
		}
	}
}
