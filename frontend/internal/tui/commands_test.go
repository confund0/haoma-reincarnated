package tui

import "testing"

func TestParseCommand(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantName string
		wantRest string
		wantOK   bool
	}{
		{"not a command", "hello", "", "", false},
		{"empty", "", "", "", false},
		{"only whitespace", "   ", "", "", false},
		{"bare command", "/quit", "/quit", "", true},
		{"command with single-word arg", "/invite-paste alice", "/invite-paste", "alice", true},
		{"command with multi-word arg", "/accept-paste {\"peer_id\":\"abc\",\"x\":1}", "/accept-paste", `{"peer_id":"abc","x":1}`, true},
		{"leading whitespace tolerated", "   /invite-paste alice", "/invite-paste", "alice", true},
		{"tabs tolerated", "/invite-paste\talice", "/invite-paste", "alice", true},
		{"multiple inter-token spaces collapsed", "/invite-paste    alice bob", "/invite-paste", "alice bob", true},
		{"slash in middle", "foo /bar", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotName, gotRest, gotOK := parseCommand(c.in)
			if gotOK != c.wantOK || gotName != c.wantName || gotRest != c.wantRest {
				t.Errorf("parseCommand(%q) = (%q, %q, %v); want (%q, %q, %v)",
					c.in, gotName, gotRest, gotOK, c.wantName, c.wantRest, c.wantOK)
			}
		})
	}
}

func TestShortID(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"short", "short"},
		{"exactly10x", "exactly10x"},
		{"0123456789abcdef", "01234567…"},
	}
	for _, c := range cases {
		if got := shortID(c.in); got != c.want {
			t.Errorf("shortID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIfElse(t *testing.T) {
	if got := ifElse(true, "a", "b"); got != "a" {
		t.Errorf("true → %q, want a", got)
	}
	if got := ifElse(false, "a", "b"); got != "b" {
		t.Errorf("false → %q, want b", got)
	}
}

func TestWinOrderAfterClose(t *testing.T) {
	cases := []struct {
		name      string
		order     []string
		idx       int
		wantOrder []string
		wantNext  string
	}{
		{
			name:      "close tail chat, fall back to previous",
			order:     []string{"status", "contacts", "chat:alice"},
			idx:       2,
			wantOrder: []string{"status", "contacts"},
			wantNext:  "contacts",
		},
		{
			name:      "close middle chat, successor slides into slot",
			order:     []string{"status", "contacts", "chat:alice", "chat:bob"},
			idx:       2,
			wantOrder: []string{"status", "contacts", "chat:bob"},
			wantNext:  "chat:bob",
		},
		{
			name:      "close final chat among many, fall back",
			order:     []string{"status", "contacts", "chat:alice", "chat:bob"},
			idx:       3,
			wantOrder: []string{"status", "contacts", "chat:alice"},
			wantNext:  "chat:alice",
		},
		{
			name:      "out-of-range idx: no-op",
			order:     []string{"status", "contacts"},
			idx:       5,
			wantOrder: []string{"status", "contacts"},
			wantNext:  "",
		},
		{
			name:      "negative idx: no-op",
			order:     []string{"status", "contacts"},
			idx:       -1,
			wantOrder: []string{"status", "contacts"},
			wantNext:  "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotOrder, gotNext := winOrderAfterClose(c.order, c.idx)
			if len(gotOrder) != len(c.wantOrder) {
				t.Fatalf("order len = %d, want %d (%v vs %v)", len(gotOrder), len(c.wantOrder), gotOrder, c.wantOrder)
			}
			for i := range gotOrder {
				if gotOrder[i] != c.wantOrder[i] {
					t.Errorf("order[%d] = %q, want %q", i, gotOrder[i], c.wantOrder[i])
				}
			}
			if gotNext != c.wantNext {
				t.Errorf("next = %q, want %q", gotNext, c.wantNext)
			}
		})
	}
}

func TestSplitPeerArg(t *testing.T) {
	cases := []struct {
		name             string
		in               string
		wantID, wantText string
		wantOK           bool
	}{
		{"empty", "", "", "", false},
		{"whitespace only", "  ", "", "", false},
		{"only peer id, no text", "alice", "", "", false},
		{"only peer id with trailing space", "alice ", "", "", false},
		{"happy path", "alice hello", "alice", "hello", true},
		{"text contains spaces", "alice hello there friend", "alice", "hello there friend", true},
		{"text contains tabs", "alice\thello\tworld", "alice", "hello\tworld", true},
		{"leading whitespace tolerated", "   alice hello", "alice", "hello", true},
		{"runs of spaces in text preserved", "alice hi   bob", "alice", "hi   bob", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id, text, ok := splitPeerArg(c.in)
			if ok != c.wantOK || id != c.wantID || text != c.wantText {
				t.Errorf("splitPeerArg(%q) = (%q, %q, %v); want (%q, %q, %v)",
					c.in, id, text, ok, c.wantID, c.wantText, c.wantOK)
			}
		})
	}
}
