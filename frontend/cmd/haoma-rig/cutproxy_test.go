package main

import (
	"errors"
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

func startEcho(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	return ln.Addr().String()
}

func socksConnect(proxy, target string, wait time.Duration) (net.Conn, error) {
	c, err := net.DialTimeout("tcp", proxy, 2*time.Second)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (net.Conn, error) { c.Close(); return nil, err }
	_ = c.SetDeadline(time.Now().Add(wait))
	if _, err := c.Write([]byte{5, 1, 0}); err != nil {
		return fail(err)
	}
	var greet [2]byte
	if _, err := io.ReadFull(c, greet[:]); err != nil {
		return fail(err)
	}
	host, portS, _ := net.SplitHostPort(target)
	port, _ := strconv.Atoi(portS)
	ip := net.ParseIP(host).To4()
	req := append([]byte{5, 1, 0, 1}, ip...)
	req = append(req, byte(port>>8), byte(port))
	if _, err := c.Write(req); err != nil {
		return fail(err)
	}
	var rep [10]byte
	if _, err := io.ReadFull(c, rep[:]); err != nil {
		return fail(err)
	}
	if rep[1] != 0 {
		return fail(errors.New("socks reply " + strconv.Itoa(int(rep[1]))))
	}
	_ = c.SetDeadline(time.Time{})
	return c, nil
}

func echoOnce(t *testing.T, c net.Conn) {
	t.Helper()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatalf("write through the proxy: %v", err)
	}
	var b [4]byte
	if _, err := io.ReadFull(c, b[:]); err != nil || string(b[:]) != "ping" {
		t.Fatalf("read through the proxy: %q %v", b, err)
	}
	_ = c.SetDeadline(time.Time{})
}

func readEnds(c net.Conn, wait time.Duration) bool {
	_ = c.SetReadDeadline(time.Now().Add(wait))
	var b [1]byte
	_, err := c.Read(b[:])
	var ne net.Error
	return err != nil && !(errors.As(err, &ne) && ne.Timeout())
}

func TestCutProxy_PassCutPass(t *testing.T) {
	echo := startEcho(t)
	p, err := startCutProxy()
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()

	open, err := socksConnect(p.addr(), echo, 2*time.Second)
	if err != nil {
		t.Fatalf("connect while passing: %v", err)
	}
	defer open.Close()
	echoOnce(t, open)

	if n := p.setCut(true); n != 1 {
		t.Fatalf("the cut closed %d links, want 1", n)
	}
	if !readEnds(open, 2*time.Second) {
		t.Fatal("the open link is still up after the cut")
	}

	held, err := net.DialTimeout("tcp", p.addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("the proxy's listener must stay open while cut: %v", err)
	}
	defer held.Close()
	if _, err := held.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	if readEnds(held, 300*time.Millisecond) {
		t.Fatal("a connection made while cut was answered or closed; it must be held")
	}
	if _, err := socksConnect(p.addr(), echo, 300*time.Millisecond); err == nil {
		t.Fatal("a SOCKS connect went through while cut")
	}

	deadline := time.Now().Add(2 * time.Second)
	for p.statsNow().Held < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	st := p.statsNow()
	if st.Links != 1 || st.ClosedAtCut != 1 || st.Held != 2 || st.Cuts != 1 {
		t.Fatalf("stats while cut: %+v, want Links=1 ClosedAtCut=1 Held=2 Cuts=1", st)
	}

	if n := p.setCut(false); n != 2 {
		t.Fatalf("the pass closed %d held connections, want 2", n)
	}
	if !readEnds(held, 2*time.Second) {
		t.Fatal("the held connection is still open after the pass")
	}
	again, err := socksConnect(p.addr(), echo, 2*time.Second)
	if err != nil {
		t.Fatalf("connect after the pass: %v", err)
	}
	defer again.Close()
	echoOnce(t, again)
	if st := p.statsNow(); st.Links != 2 {
		t.Fatalf("links after the pass: %d, want 2", st.Links)
	}
}

func TestCutProxy_CutEndsAHandshake(t *testing.T) {
	p, err := startCutProxy()
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	c, err := net.DialTimeout("tcp", p.addr(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if _, err := c.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	var greet [2]byte
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(c, greet[:]); err != nil {
		t.Fatal(err)
	}
	if n := p.setCut(true); n != 1 {
		t.Fatalf("the cut closed %d links, want 1 (the one in its handshake)", n)
	}
	if !readEnds(c, 2*time.Second) {
		t.Fatal("the link in its handshake is still up after the cut")
	}
	if st := p.statsNow(); st.Links != 0 || st.ClosedAtCut != 1 {
		t.Fatalf("stats: %+v, want Links=0 ClosedAtCut=1", st)
	}
}

func TestCutProxy_Close(t *testing.T) {
	echo := startEcho(t)
	p, err := startCutProxy()
	if err != nil {
		t.Fatal(err)
	}
	open, err := socksConnect(p.addr(), echo, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()
	done := make(chan struct{})
	go func() { p.close(); p.close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("close did not return")
	}
	if !readEnds(open, 2*time.Second) {
		t.Fatal("the open link is still up after close")
	}
	if c, err := net.DialTimeout("tcp", p.addr(), time.Second); err == nil {
		c.Close()
		t.Fatal("the listener still accepts after close")
	}
}
