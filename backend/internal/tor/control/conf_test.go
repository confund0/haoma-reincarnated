package control

import (
	"net"
	"reflect"
	"testing"
)

func TestGetConf(t *testing.T) {
	for _, tc := range []struct {
		reply string
		want  []string
	}{
		{"250 SocksPort=auto IsolateDestAddr\r\n", []string{"auto IsolateDestAddr"}},
		{"250-SocksPort=auto\r\n250 SocksPort=auto IsolateSOCKSAuth\r\n", []string{"auto", "auto IsolateSOCKSAuth"}},
		{"250 SocksPort\r\n", []string{""}},
		{"250 SocksPort=\"unix:/a b\"\r\n", []string{"unix:/a b"}},
	} {
		got := make(chan string, 1)
		c := mockTor(t, func(s net.Conn) {
			got <- readLine(t, s)
			s.Write([]byte(tc.reply))
		})
		v, err := c.GetConf("SocksPort")
		if err != nil {
			t.Fatalf("%q: %v", tc.reply, err)
		}
		if line := <-got; line != "GETCONF SocksPort\r\n" {
			t.Errorf("sent %q", line)
		}
		if !reflect.DeepEqual(v, tc.want) {
			t.Errorf("%q: values = %q, want %q", tc.reply, v, tc.want)
		}
		c.Close()
	}
}

func TestGetConf_ErrorReply(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {
		readLine(t, s)
		s.Write([]byte("552 Unrecognized configuration key \"Bogus\"\r\n"))
	})
	defer c.Close()
	if _, err := c.GetConf("Bogus"); err == nil {
		t.Error("a 552 reply gave no error")
	}
}

func TestSetConf_QuotesTheValue(t *testing.T) {
	got := make(chan string, 1)
	c := mockTor(t, func(s net.Conn) {
		got <- readLine(t, s)
		s.Write([]byte("250 OK\r\n"))
	})
	defer c.Close()
	if err := c.SetConf("SocksPort", `127.0.0.1:9050 IsolateDestAddr`); err != nil {
		t.Fatal(err)
	}
	if line, want := <-got, "SETCONF SocksPort=\"127.0.0.1:9050 IsolateDestAddr\"\r\n"; line != want {
		t.Errorf("sent %q, want %q", line, want)
	}
}

func TestSetConf_Refusals(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {
		readLine(t, s)
		s.Write([]byte("513 Unacceptable option value\r\n"))
	})
	defer c.Close()
	if err := c.SetConf("SocksPort", "nonsense"); err == nil {
		t.Error("a 513 reply gave no error")
	}
	if err := c.SetConf("SocksPort", "a\r\nSIGNAL HALT"); err == nil {
		t.Error("a value with a line break was sent")
	}
	if err := c.SetConf("Socks Port", "auto"); err == nil {
		t.Error("a key with a space was sent")
	}
}

func TestToggleNetwork_OnRefusedTwiceReportsOff(t *testing.T) {
	got := make(chan string, 3)
	c := mockTor(t, func(s net.Conn) {
		got <- readLine(t, s)
		s.Write([]byte("250 OK\r\n"))
		for range 2 {
			got <- readLine(t, s)
			s.Write([]byte("553 Unable to set option: Failed to bind one of the listener ports.\r\n"))
		}
	})
	defer c.Close()
	off, err := c.ToggleNetwork()
	if err == nil || !off {
		t.Fatalf("ToggleNetwork = off %v, err %v; want off with an error", off, err)
	}
	want := []string{"SETCONF DisableNetwork=\"1\"\r\n", "SETCONF DisableNetwork=\"0\"\r\n", "SETCONF DisableNetwork=\"0\"\r\n"}
	for i, w := range want {
		if line := <-got; line != w {
			t.Errorf("command %d = %q, want %q", i, line, w)
		}
	}
}

func TestToggleNetwork_OffRefusedReportsOn(t *testing.T) {
	c := mockTor(t, func(s net.Conn) {
		readLine(t, s)
		s.Write([]byte("552 Unrecognized option\r\n"))
	})
	defer c.Close()
	if off, err := c.ToggleNetwork(); err == nil || off {
		t.Fatalf("ToggleNetwork = off %v, err %v; want an error with the network on", off, err)
	}
}

func TestEnableNetworkUnpinned(t *testing.T) {
	got := make(chan string, 1)
	c := mockTor(t, func(s net.Conn) {
		got <- readLine(t, s)
		s.Write([]byte("250 OK\r\n"))
	})
	defer c.Close()
	if err := c.EnableNetworkUnpinned("auto IsolateDestAddr"); err != nil {
		t.Fatal(err)
	}
	if line, want := <-got, "SETCONF SocksPort=\"auto IsolateDestAddr\" DisableNetwork=\"0\"\r\n"; line != want {
		t.Errorf("sent %q, want %q", line, want)
	}
	if err := c.EnableNetworkUnpinned("auto\r\nSIGNAL HALT"); err == nil {
		t.Error("a line with a line break was sent")
	}
}
