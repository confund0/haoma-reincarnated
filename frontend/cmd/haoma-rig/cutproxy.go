package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

const cutProxyHandshakeTimeout = 30 * time.Second

const cutProxyDialTimeout = 30 * time.Second

type cutProxyStats struct {
	Links       int
	ClosedAtCut int
	Held        int
	Cuts        int
}

type cutProxyLink struct {
	client net.Conn
	target net.Conn
	cancel context.CancelFunc
	dead   bool
}

type cutProxy struct {
	ln net.Listener

	mu     sync.Mutex
	cut    bool
	closed bool
	links  map[*cutProxyLink]struct{}
	held   map[net.Conn]struct{}
	stats  cutProxyStats
	wg     sync.WaitGroup
	dialer net.Dialer
}

func startCutProxy() (*cutProxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("cut proxy: listen: %w", err)
	}
	p := &cutProxy{
		ln:     ln,
		links:  map[*cutProxyLink]struct{}{},
		held:   map[net.Conn]struct{}{},
		dialer: net.Dialer{Timeout: cutProxyDialTimeout},
	}
	p.wg.Add(1)
	go p.accept()
	return p, nil
}

func (p *cutProxy) addr() string { return p.ln.Addr().String() }

func (p *cutProxy) statsNow() cutProxyStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stats
}

func (p *cutProxy) accept() {
	defer p.wg.Done()
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		switch {
		case p.closed:
			p.mu.Unlock()
			_ = c.Close()
			continue
		case p.cut:
			p.held[c] = struct{}{}
			p.stats.Held++
			p.mu.Unlock()
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		l := &cutProxyLink{client: c, cancel: cancel}
		p.links[l] = struct{}{}
		p.wg.Add(1)
		p.mu.Unlock()
		go p.serve(ctx, l)
	}
}

func (p *cutProxy) setCut(cut bool) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.cut == cut {
		return 0
	}
	p.cut = cut
	n := 0
	if cut {
		p.stats.Cuts++
		for l := range p.links {
			p.endLocked(l)
			n++
		}
		p.stats.ClosedAtCut += n
		return n
	}
	for c := range p.held {
		_ = c.Close()
		delete(p.held, c)
		n++
	}
	return n
}

func (p *cutProxy) endLocked(l *cutProxyLink) {
	l.dead = true
	l.cancel()
	_ = l.client.Close()
	if l.target != nil {
		_ = l.target.Close()
	}
	delete(p.links, l)
}

func (p *cutProxy) close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	_ = p.ln.Close()
	for l := range p.links {
		p.endLocked(l)
	}
	for c := range p.held {
		_ = c.Close()
		delete(p.held, c)
	}
	p.mu.Unlock()
	p.wg.Wait()
}

func (p *cutProxy) serve(ctx context.Context, l *cutProxyLink) {
	defer p.wg.Done()
	defer func() {
		p.mu.Lock()
		if !l.dead {
			p.endLocked(l)
		}
		p.mu.Unlock()
	}()
	_ = l.client.SetDeadline(time.Now().Add(cutProxyHandshakeTimeout))
	target, err := readSocks5Request(l.client)
	if err != nil {
		return
	}
	t, err := p.dialer.DialContext(ctx, "tcp", target)
	if err != nil {

		_, _ = l.client.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	p.mu.Lock()
	if l.dead {
		p.mu.Unlock()
		_ = t.Close()
		return
	}
	l.target = t
	p.mu.Unlock()
	if _, err := l.client.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	_ = l.client.SetDeadline(time.Time{})
	p.mu.Lock()
	if !l.dead {
		p.stats.Links++
	}
	p.mu.Unlock()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(t, l.client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(l.client, t); done <- struct{}{} }()

	<-done
}

func readSocks5Request(c net.Conn) (string, error) {
	var head [2]byte
	if _, err := io.ReadFull(c, head[:]); err != nil {
		return "", err
	}
	if head[0] != 5 {
		return "", fmt.Errorf("socks version %d", head[0])
	}
	methods := make([]byte, head[1])
	if _, err := io.ReadFull(c, methods); err != nil {
		return "", err
	}
	noAuth := false
	for _, m := range methods {
		if m == 0 {
			noAuth = true
		}
	}
	if !noAuth {
		_, _ = c.Write([]byte{5, 0xff})
		return "", errors.New("socks client offers no 'no authentication' method")
	}
	if _, err := c.Write([]byte{5, 0}); err != nil {
		return "", err
	}
	var req [4]byte
	if _, err := io.ReadFull(c, req[:]); err != nil {
		return "", err
	}
	if req[0] != 5 || req[1] != 1 {
		_, _ = c.Write([]byte{5, 7, 0, 1, 0, 0, 0, 0, 0, 0})
		return "", fmt.Errorf("socks request version %d command %d, want 5 and CONNECT", req[0], req[1])
	}
	var host string
	switch req[3] {
	case 1:
		var a [4]byte
		if _, err := io.ReadFull(c, a[:]); err != nil {
			return "", err
		}
		host = net.IP(a[:]).String()
	case 4:
		var a [16]byte
		if _, err := io.ReadFull(c, a[:]); err != nil {
			return "", err
		}
		host = net.IP(a[:]).String()
	case 3:
		var n [1]byte
		if _, err := io.ReadFull(c, n[:]); err != nil {
			return "", err
		}
		name := make([]byte, n[0])
		if _, err := io.ReadFull(c, name); err != nil {
			return "", err
		}
		host = string(name)
	default:
		_, _ = c.Write([]byte{5, 8, 0, 1, 0, 0, 0, 0, 0, 0})
		return "", fmt.Errorf("socks address type %d", req[3])
	}
	var port [2]byte
	if _, err := io.ReadFull(c, port[:]); err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port[:])))), nil
}
