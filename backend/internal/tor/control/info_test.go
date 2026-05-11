package control

import (
	"net"
	"testing"
)

func TestGetInfo_SingleLine(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {
		readLine(t, s)
		s.Write([]byte("250-version=0.4.9.6\r\n250 OK\r\n"))
	})
	defer c.Close()

	v, err := c.GetInfo("version")
	if err != nil {
		t.Fatal(err)
	}
	if v != "0.4.9.6" {
		t.Errorf("value = %q", v)
	}
}

func TestGetInfo_DataPayload(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {
		readLine(t, s)
		s.Write([]byte("250+onions/current=\r\nabc.onion\r\ndef.onion\r\n.\r\n250 OK\r\n"))
	})
	defer c.Close()

	v, err := c.GetInfo("onions/current")
	if err != nil {
		t.Fatal(err)
	}
	want := "abc.onion\ndef.onion"
	if v != want {
		t.Errorf("value = %q, want %q", v, want)
	}
}

func TestGetInfo_ErrorReply(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {
		readLine(t, s)
		s.Write([]byte("552 Unrecognized key\r\n"))
	})
	defer c.Close()
	if _, err := c.GetInfo("bogus"); err == nil {
		t.Error("expected error from 552 reply")
	}
}
