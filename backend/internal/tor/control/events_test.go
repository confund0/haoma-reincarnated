package control

import (
	"net"
	"testing"
	"time"
)

func TestSetEvents_WireFormat(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {
		got := readLine(t, s)
		want := "SETEVENTS CIRC HS_DESC\r\n"
		if got != want {
			t.Errorf("cmd = %q, want %q", got, want)
		}
		s.Write([]byte("250 OK\r\n"))
	})
	defer c.Close()
	if err := c.SetEvents("CIRC", "HS_DESC"); err != nil {
		t.Fatalf("SetEvents: %v", err)
	}
}

func TestSetEvents_Unsubscribe(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {
		got := readLine(t, s)
		if got != "SETEVENTS\r\n" {
			t.Errorf("cmd = %q, want SETEVENTS\\r\\n", got)
		}
		s.Write([]byte("250 OK\r\n"))
	})
	defer c.Close()
	if err := c.SetEvents(); err != nil {
		t.Fatalf("SetEvents: %v", err)
	}
}

func TestSetEvents_ErrorReply(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {
		readLine(t, s)
		s.Write([]byte("552 Unrecognized event type\r\n"))
	})
	defer c.Close()
	if err := c.SetEvents("BOGUS"); err == nil {
		t.Fatal("expected error from 552 reply")
	}
}

func TestSetEvents_EventDelivery(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {
		readLine(t, s)
		s.Write([]byte("250 OK\r\n"))
		s.Write([]byte("650 HS_DESC UPLOAD abc UNKNOWN $fp desc-id\r\n"))
	})
	defer c.Close()
	if err := c.SetEvents("HS_DESC"); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-c.Events():
		if ev.Type != "HS_DESC" {
			t.Errorf("type = %q", ev.Type)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("event not delivered within 500ms")
	}
}
