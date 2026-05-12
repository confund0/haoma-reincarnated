package main

import "testing"

func TestOnionFromDest(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"http://abcdefghij.onion", "abcdefghij"},
		{"http://abcdefghij.onion/", "abcdefghij"},
		{"http://abcdefghij.onion/message", "abcdefghij"},
		{"http://abcdefghij.onion:80", "abcdefghij"},
		{"http://abcdefghij.onion:8080/files/abc", "abcdefghij"},
		{"https://abcdefghij.onion", "abcdefghij"},
		{"http://127.0.0.1:8080", ""},
		{"http://example.com", ""},
		{"", ""},
	}
	for _, c := range cases {
		got := onionFromDest(c.in)
		if got != c.want {
			t.Errorf("onionFromDest(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
