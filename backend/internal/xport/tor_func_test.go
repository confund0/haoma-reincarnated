package xport

import (
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func countingListener(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var n atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			n.Add(1)
			_ = c.Close()
		}
	}()
	return ln.Addr().String(), &n
}

func TestNewTorHTTPClientFunc_FollowsTheAddress(t *testing.T) {
	first, nFirst := countingListener(t)
	second, nSecond := countingListener(t)
	var addr atomic.Pointer[string]
	addr.Store(&first)
	hc, err := NewTorHTTPClientFunc(func() string { return *addr.Load() }, "tag")
	if err != nil {
		t.Fatal(err)
	}
	hc.Timeout = 5 * time.Second

	_, _ = hc.Get("http://example.onion/")
	addr.Store(&second)
	_, _ = hc.Get("http://example.onion/")

	deadline := time.Now().Add(2 * time.Second)
	for nSecond.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if nFirst.Load() != 1 || nSecond.Load() != 1 {
		t.Errorf("dials: first=%d second=%d, want 1 and 1", nFirst.Load(), nSecond.Load())
	}
}

func TestNewTorHTTPClientFunc_Refusals(t *testing.T) {
	if _, err := NewTorHTTPClientFunc(func() string { return "127.0.0.1:9" }, ""); err == nil {
		t.Error("an empty isolation tag was accepted")
	}
	if _, err := NewTorHTTPClientFunc(nil, "tag"); err == nil {
		t.Error("a nil address function was accepted")
	}
}
