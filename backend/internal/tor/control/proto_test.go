package control

import "testing"

func TestTokenKV_Simple(t *testing.T) {
	pairs, err := tokenKV("A=1 B=2 C=3")
	if err != nil {
		t.Fatal(err)
	}
	if pairs["A"] != "1" || pairs["B"] != "2" || pairs["C"] != "3" {
		t.Errorf("pairs = %v", pairs)
	}
}

func TestTokenKV_QuotedValues(t *testing.T) {
	pairs, err := tokenKV(`NAME="hello world" OTHER=x EMPTY=""`)
	if err != nil {
		t.Fatal(err)
	}
	if pairs["NAME"] != "hello world" {
		t.Errorf("NAME = %q", pairs["NAME"])
	}
	if pairs["OTHER"] != "x" {
		t.Errorf("OTHER = %q", pairs["OTHER"])
	}
	if pairs["EMPTY"] != "" {
		t.Errorf("EMPTY = %q", pairs["EMPTY"])
	}
}

func TestTokenKV_NoEquals(t *testing.T) {
	if _, err := tokenKV("NOEQUALS"); err == nil {
		t.Error("expected error for token without =")
	}
}

func TestTokenKV_UnterminatedQuote(t *testing.T) {
	if _, err := tokenKV(`K="unclosed`); err == nil {
		t.Error("expected error for unterminated quote")
	}
}

func TestParseProtocolInfo_Standard(t *testing.T) {
	lines := []string{
		"PROTOCOLINFO 1",
		`AUTH METHODS=COOKIE,SAFECOOKIE,HASHEDPASSWORD COOKIEFILE="/var/lib/tor/control_auth_cookie"`,
		`VERSION Tor="0.4.9.6"`,
		"OK",
	}
	p, err := parseProtocolInfo(lines)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Methods) != 3 {
		t.Errorf("methods = %v", p.Methods)
	}
	for _, m := range []string{"COOKIE", "SAFECOOKIE", "HASHEDPASSWORD"} {
		if !p.Has(m) {
			t.Errorf("missing method %q", m)
		}
	}
	if p.CookieFile != "/var/lib/tor/control_auth_cookie" {
		t.Errorf("cookie = %q", p.CookieFile)
	}
	if p.Version != "0.4.9.6" {
		t.Errorf("version = %q", p.Version)
	}
}

func TestParseProtocolInfo_NullOnly(t *testing.T) {
	lines := []string{"PROTOCOLINFO 1", "AUTH METHODS=NULL", `VERSION Tor="0.4.0.0"`, "OK"}
	p, err := parseProtocolInfo(lines)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Has("NULL") {
		t.Errorf("NULL missing")
	}
	if p.CookieFile != "" {
		t.Errorf("cookie = %q, want empty", p.CookieFile)
	}
}

func TestParseProtocolInfo_MissingAuth(t *testing.T) {
	if _, err := parseProtocolInfo([]string{"PROTOCOLINFO 1", `VERSION Tor="0.4"`, "OK"}); err == nil {
		t.Error("expected error for missing AUTH line")
	}
}
