package embedded

import "testing"

func TestParseCportFile(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{
			name: "happy path",
			in:   "PORT=127.0.0.1:23451\n",
			want: "127.0.0.1:23451",
		},
		{
			name: "no trailing newline",
			in:   "PORT=127.0.0.1:23451",
			want: "127.0.0.1:23451",
		},
		{
			name: "ipv6 host",
			in:   "PORT=[::1]:23451\n",
			want: "[::1]:23451",
		},
		{
			name: "trailing whitespace",
			in:   "PORT=127.0.0.1:23451  \n",
			want: "127.0.0.1:23451",
		},
		{
			name: "two PORT lines, first wins",
			in:   "PORT=127.0.0.1:23451\nPORT=127.0.0.1:23452\n",
			want: "127.0.0.1:23451",
		},
		{
			name:    "empty",
			in:      "",
			wantErr: true,
		},
		{
			name:    "no PORT prefix",
			in:      "127.0.0.1:23451\n",
			wantErr: true,
		},
		{
			name:    "non-numeric port",
			in:      "PORT=127.0.0.1:abcd\n",
			wantErr: true,
		},
		{
			name:    "missing port",
			in:      "PORT=127.0.0.1\n",
			wantErr: true,
		},
		{
			name:    "host empty",
			in:      "PORT=:23451\n",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCportFile([]byte(tt.in))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseCportFile(%q): want error, got %q", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCportFile(%q): unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("parseCportFile(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestRenderTorrc_ContainsRequired(t *testing.T) {
	out := renderTorrc(torrcConfig{
		DataDir:         "/tmp/t",
		ControlPortFile: "/tmp/t/control.port",
		CookieAuthFile:  "/tmp/t/control.cookie",
		NoticeLog:       "/tmp/t/tor.log",
	})
	required := []string{
		"DataDirectory /tmp/t",
		"ControlPort auto",
		"ControlPortWriteToFile /tmp/t/control.port",
		"CookieAuthentication 1",
		"CookieAuthFile /tmp/t/control.cookie",
		"SocksPort auto",
		"AvoidDiskWrites 1",
		"Log notice file /tmp/t/tor.log",
		"SafeLogging 1",
	}
	for _, want := range required {
		if !contains(out, want) {
			t.Errorf("rendered torrc missing required directive %q\n--- got ---\n%s", want, out)
		}
	}
}

func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
