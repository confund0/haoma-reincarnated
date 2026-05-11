package health

import "testing"

func TestParseBootstrap(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"NOTICE BOOTSTRAP PROGRESS=100 TAG=done SUMMARY=Done", 100},
		{"NOTICE BOOTSTRAP PROGRESS=42 TAG=loading_descriptors SUMMARY=...", 42},
		{"NOTICE BOOTSTRAP PROGRESS=0 TAG=starting SUMMARY=Starting", 0},
		{"", 0},
		{"PROGRESS=notanint", 0},
		{"no progress field here", 0},
	}
	for _, c := range cases {
		got := parseBootstrap(c.in)
		if got != c.want {
			t.Errorf("parseBootstrap(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
