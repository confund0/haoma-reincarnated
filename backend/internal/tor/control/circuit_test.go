package control

import (
	"net"
	"strings"
	"testing"
)

func TestParseCircuitLine_BuiltHSClientRend(t *testing.T) {
	line := "42 BUILT $AAA=Relay1,$BBB=Relay2,$CCC=Relay3,$DDD=Relay4 BUILD_FLAGS=NEED_UPTIME,NEED_CAPACITY PURPOSE=HS_CLIENT_REND HS_STATE=HSCR_JOINED REND_QUERY=abcdef0123456789abcdef0123456789abcdef0123456789abcdef01 TIME_CREATED=2026-05-12T10:00:00.000000"
	ci, err := parseCircuitLine(line)
	if err != nil {
		t.Fatal(err)
	}
	if ci.ID != "42" || ci.Status != "BUILT" {
		t.Errorf("header = %q %q", ci.ID, ci.Status)
	}
	if ci.Purpose != "HS_CLIENT_REND" {
		t.Errorf("Purpose = %q", ci.Purpose)
	}
	if ci.HSAddress != "abcdef0123456789abcdef0123456789abcdef0123456789abcdef01" {
		t.Errorf("HSAddress = %q", ci.HSAddress)
	}
	if !strings.HasPrefix(ci.Path, "$AAA=") {
		t.Errorf("Path = %q", ci.Path)
	}
}

func TestParseCircuitLine_FailedNoPath(t *testing.T) {
	line := "7 FAILED PURPOSE=GENERAL REASON=TIMEOUT"
	ci, err := parseCircuitLine(line)
	if err != nil {
		t.Fatal(err)
	}
	if ci.ID != "7" || ci.Status != "FAILED" {
		t.Errorf("header = %q %q", ci.ID, ci.Status)
	}
	if ci.Path != "" {
		t.Errorf("Path = %q, want empty", ci.Path)
	}
	if ci.Purpose != "GENERAL" {
		t.Errorf("Purpose = %q", ci.Purpose)
	}
}

func TestCircuitStatus_MultiLineParse(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {
		readLine(t, s)
		s.Write([]byte("250+circuit-status=\r\n" +
			"1 BUILT $AA=a,$BB=b PURPOSE=GENERAL\r\n" +
			"2 BUILT $AA=a,$BB=b,$CC=c,$DD=d PURPOSE=HS_CLIENT_REND REND_QUERY=onion1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\r\n" +
			"3 BUILT $AA=a,$BB=b,$CC=c,$DD=d PURPOSE=HS_CLIENT_REND REND_QUERY=onion2bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\r\n" +
			".\r\n" +
			"250 OK\r\n"))
	})
	defer c.Close()
	got, err := c.CircuitStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[1].HSAddress != "onion1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("row 1 HSAddress = %q", got[1].HSAddress)
	}
	if got[2].HSAddress != "onion2bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Errorf("row 2 HSAddress = %q", got[2].HSAddress)
	}
}

func TestCloseCircuit_WireAndOK(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {
		line := readLine(t, s)
		if line != "CLOSECIRCUIT 42\r\n" {
			t.Errorf("cmd = %q", line)
		}
		s.Write([]byte("250 OK\r\n"))
	})
	defer c.Close()
	if err := c.CloseCircuit("42"); err != nil {
		t.Fatal(err)
	}
}

func TestCloseCircuit_UnknownCircuitTreatedAsSuccess(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {
		readLine(t, s)
		s.Write([]byte("552 Unknown circuit \"99\"\r\n"))
	})
	defer c.Close()
	if err := c.CloseCircuit("99"); err != nil {
		t.Errorf("err = %v, want nil", err)
	}
}

func TestHsFetch_WireAndOK(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {
		line := readLine(t, s)
		if line != "HSFETCH abcdefonion\r\n" {
			t.Errorf("cmd = %q", line)
		}
		s.Write([]byte("250 OK\r\n"))
	})
	defer c.Close()
	if err := c.HsFetch("abcdefonion"); err != nil {
		t.Fatal(err)
	}
}

func TestHsFetch_EmptyID(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {})
	defer c.Close()
	if err := c.HsFetch(""); err == nil {
		t.Errorf("want error on empty service id")
	}
}

func TestCloseCircuit_EmptyID(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {})
	defer c.Close()
	if err := c.CloseCircuit(""); err == nil {
		t.Errorf("want error on empty id")
	}
}
