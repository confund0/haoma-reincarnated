package control

import (
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func mockTor(t *testing.T, script func(server net.Conn)) *Conn {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})
	go script(server)
	return newConn(client)
}

func readLine(t *testing.T, c net.Conn) string {
	t.Helper()
	buf := make([]byte, 512)
	n, err := c.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("server read: %v", err)
	}
	return string(buf[:n])
}

func TestConn_CommandReply(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {
		got := readLine(t, s)
		if !strings.HasPrefix(got, "GETINFO version") {
			t.Errorf("server got %q, want GETINFO version prefix", got)
		}
		s.Write([]byte("250 version=0.4.8.x\r\n"))
	})
	defer c.Close()

	reply, err := c.cmd("GETINFO version")
	if err != nil {
		t.Fatal(err)
	}
	if reply.Code != 250 || len(reply.Lines) != 1 || reply.Lines[0] != "version=0.4.8.x" {
		t.Errorf("reply = %+v", reply)
	}
}

func TestConn_EventInterleavedBeforeReply(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {
		readLine(t, s)
		s.Write([]byte("650 CIRC 1 BUILT\r\n250 OK\r\n"))
	})
	defer c.Close()

	reply, err := c.cmd("SOMECMD")
	if err != nil {
		t.Fatal(err)
	}
	if reply.Code != 250 {
		t.Errorf("reply code = %d", reply.Code)
	}
	select {
	case ev := <-c.Events():
		if ev.Type != "CIRC" {
			t.Errorf("event type = %q", ev.Type)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("event not delivered within 100ms")
	}
}

func TestConn_CmdAfterClose(t *testing.T) {
	c := mockTor(t, func(s net.Conn) { <-time.After(50 * time.Millisecond); s.Close() })
	c.Close()
	if _, err := c.cmd("FOO"); err == nil {
		t.Error("cmd after Close should error")
	}
}

func TestConn_Close_Idempotent(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {})
	_ = c.Close()
	_ = c.Close()
}

func TestConn_RemoteCloseSurfacesAsCmdError(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {
		readLine(t, s)
		s.Close()
	})
	defer c.Close()
	if _, err := c.cmd("FOO"); err == nil {
		t.Error("expected error after remote close")
	}
}
