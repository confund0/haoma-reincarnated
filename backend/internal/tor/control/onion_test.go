package control

import (
	"net"
	"strings"
	"testing"
)

const fakeServiceID = "aabbccddeeff00112233445566778899aabbccddeeff00112233445566"
const fakePrivKey = "fakebase64payload=="

func TestAddOnionNew_ParsesReply(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {
		line := readLine(t, s)
		if !strings.HasPrefix(line, "ADD_ONION NEW:ED25519-V3 ") {
			t.Errorf("cmd = %q, want ADD_ONION NEW:ED25519-V3 prefix", line)
		}
		s.Write([]byte("250-ServiceID=" + fakeServiceID + "\r\n" +
			"250-PrivateKey=ED25519-V3:" + fakePrivKey + "\r\n" +
			"250 OK\r\n"))
	})
	defer c.Close()

	o, err := c.AddOnionNew([]OnionPort{{VirtPort: 80, Target: "127.0.0.1:8080"}})
	if err != nil {
		t.Fatal(err)
	}
	if o.ServiceID != fakeServiceID {
		t.Errorf("ServiceID = %q", o.ServiceID)
	}
	if o.PrivateKey != fakePrivKey {
		t.Errorf("PrivateKey = %q", o.PrivateKey)
	}
}

func TestAddOnionNew_WireFormat_WithTarget(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {
		line := readLine(t, s)
		want := "ADD_ONION NEW:ED25519-V3 Port=80,127.0.0.1:8080\r\n"
		if line != want {
			t.Errorf("cmd = %q, want %q", line, want)
		}
		s.Write([]byte("250-ServiceID=" + fakeServiceID + "\r\n250-PrivateKey=ED25519-V3:" + fakePrivKey + "\r\n250 OK\r\n"))
	})
	defer c.Close()
	_, _ = c.AddOnionNew([]OnionPort{{VirtPort: 80, Target: "127.0.0.1:8080"}})
}

func TestAddOnionNew_WireFormat_WithoutTarget(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {
		line := readLine(t, s)
		want := "ADD_ONION NEW:ED25519-V3 Port=443\r\n"
		if line != want {
			t.Errorf("cmd = %q, want %q", line, want)
		}
		s.Write([]byte("250-ServiceID=" + fakeServiceID + "\r\n250-PrivateKey=ED25519-V3:" + fakePrivKey + "\r\n250 OK\r\n"))
	})
	defer c.Close()
	_, _ = c.AddOnionNew([]OnionPort{{VirtPort: 443}})
}

func TestAddOnionNew_Flags(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {
		line := readLine(t, s)
		if !strings.Contains(line, "Flags=Detach") {
			t.Errorf("cmd = %q, want Flags=Detach", line)
		}
		s.Write([]byte("250-ServiceID=" + fakeServiceID + "\r\n250-PrivateKey=ED25519-V3:" + fakePrivKey + "\r\n250 OK\r\n"))
	})
	defer c.Close()
	_, _ = c.AddOnionNew([]OnionPort{{VirtPort: 80}}, "Detach")
}

func TestAddOnion_Republish_NoPrivateKeyInReply(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {
		line := readLine(t, s)
		if !strings.HasPrefix(line, "ADD_ONION ED25519-V3:existingkey Port=80") {
			t.Errorf("cmd = %q", line)
		}
		s.Write([]byte("250-ServiceID=" + fakeServiceID + "\r\n250 OK\r\n"))
	})
	defer c.Close()

	o, err := c.AddOnion("existingkey", []OnionPort{{VirtPort: 80}})
	if err != nil {
		t.Fatal(err)
	}
	if o.ServiceID != fakeServiceID {
		t.Errorf("ServiceID = %q", o.ServiceID)
	}
	if o.PrivateKey != "" {
		t.Errorf("PrivateKey = %q, want empty on republish", o.PrivateKey)
	}
}

func TestAddOnion_EmptyKey(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {})
	defer c.Close()
	if _, err := c.AddOnion("", []OnionPort{{VirtPort: 80}}); err == nil {
		t.Error("expected error for empty private key")
	}
}

func TestAddOnion_NoPorts(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {})
	defer c.Close()
	if _, err := c.AddOnionNew(nil); err == nil {
		t.Error("expected error for missing ports")
	}
}

func TestAddOnion_ErrorReply(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {
		readLine(t, s)
		s.Write([]byte("551 Onion service operation failed\r\n"))
	})
	defer c.Close()
	if _, err := c.AddOnionNew([]OnionPort{{VirtPort: 80}}); err == nil {
		t.Error("expected error from 551 reply")
	}
}

func TestAddOnionNew_MissingPKInReply(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {
		readLine(t, s)
		s.Write([]byte("250-ServiceID=" + fakeServiceID + "\r\n250 OK\r\n"))
	})
	defer c.Close()
	if _, err := c.AddOnionNew([]OnionPort{{VirtPort: 80}}); err == nil {
		t.Error("expected error when NEW ADD_ONION reply lacks PrivateKey")
	}
}

func TestDelOnion_OK(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {
		got := readLine(t, s)
		want := "DEL_ONION " + fakeServiceID + "\r\n"
		if got != want {
			t.Errorf("cmd = %q, want %q", got, want)
		}
		s.Write([]byte("250 OK\r\n"))
	})
	defer c.Close()
	if err := c.DelOnion(fakeServiceID); err != nil {
		t.Fatal(err)
	}
}

func TestDelOnion_EmptyID(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {})
	defer c.Close()
	if err := c.DelOnion(""); err == nil {
		t.Error("expected error for empty ServiceID")
	}
}

func TestDelOnion_NotFound(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {
		readLine(t, s)
		s.Write([]byte("552 Unknown onion service\r\n"))
	})
	defer c.Close()
	if err := c.DelOnion(fakeServiceID); err == nil {
		t.Error("expected error from 552 reply")
	}
}
