package secrets

import (
	"bytes"
	"strings"
	"testing"
)

func TestParse_RoundTrip(t *testing.T) {
	want := Secrets{
		HaomadStorePassphrase:   "back-pass",
		FrontendStorePassphrase: "front-pass",
		HaomadToken:             "deadbeef",
		TorPassword:             "tor-pw",
		HaomadURL:               "http://127.0.0.1:8731",
		IdleTimeoutSeconds:      1800,
	}
	blob, err := want.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := Parse(bytes.NewReader(blob))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got != want {
		t.Errorf("round-trip drift:\n got  %+v\n want %+v", got, want)
	}
}

func TestParse_EmptyInput(t *testing.T) {
	_, err := Parse(bytes.NewReader(nil))
	if err == nil {
		t.Fatal("expected error on empty input")
	}
}

func TestParse_BadJSON(t *testing.T) {
	_, err := Parse(strings.NewReader("{not json"))
	if err == nil {
		t.Fatal("expected decode error")
	}
}

func TestParse_OversizeRejected(t *testing.T) {
	big := bytes.Repeat([]byte("{"), MaxBlobSize+8)
	_, err := Parse(bytes.NewReader(big))
	if err == nil {
		t.Fatal("expected oversize rejection")
	}
}

func TestValidate(t *testing.T) {
	full := Secrets{
		HaomadStorePassphrase:   "a",
		FrontendStorePassphrase: "b",
		HaomadToken:             "c",
	}
	if err := full.Validate(); err != nil {
		t.Errorf("full Secrets should validate, got %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*Secrets)
	}{
		{"missing haomad_store_passphrase", func(s *Secrets) { s.HaomadStorePassphrase = "" }},
		{"missing frontend_store_passphrase", func(s *Secrets) { s.FrontendStorePassphrase = "" }},
		{"missing haomad_token", func(s *Secrets) { s.HaomadToken = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := full
			tc.mutate(&s)
			if err := s.Validate(); err == nil {
				t.Errorf("expected error for %s", tc.name)
			}
		})
	}
}
