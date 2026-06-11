package main

import (
	"strings"
	"testing"
)

func TestValidateChatNickOverride(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantOut string
		wantErr bool
	}{
		{"empty allowed", "", "", false},
		{"whitespace collapses to empty", "   ", "", false},
		{"trims surrounding whitespace", "  ZX42  ", "ZX42", false},
		{"passes through normal nick", "Alice", "Alice", false},
		{"unicode allowed", "Αλίκη", "Αλίκη", false},
		{"max len ok", strings.Repeat("a", selfNickMaxLen), strings.Repeat("a", selfNickMaxLen), false},
		{"over max rejected", strings.Repeat("a", selfNickMaxLen+1), "", true},
		{"newline rejected", "Alice\nfoo", "", true},
		{"tab rejected", "Alice\tfoo", "", true},
		{"del rejected", "Alice\x7f", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateChatNickOverride(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if got != tc.wantOut {
				t.Errorf("out=%q want %q", got, tc.wantOut)
			}
		})
	}
}
