package xport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"golang.org/x/net/proxy"
)

func NewTorHTTPClient(socksAddr, isoTag string) (*http.Client, error) {
	if _, err := socksDialer(socksAddr, isoTag); err != nil {
		return nil, err
	}
	return NewTorHTTPClientFunc(func() string { return socksAddr }, isoTag)
}

func NewTorHTTPClientFunc(socksAddr func() string, isoTag string) (*http.Client, error) {
	if isoTag == "" {
		return nil, errors.New("xport: isoTag must not be empty (circuit isolation)")
	}
	if socksAddr == nil {
		return nil, errors.New("xport: no SOCKS address")
	}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			cd, err := socksDialer(socksAddr(), isoTag)
			if err != nil {
				return nil, err
			}
			return cd.DialContext(ctx, network, addr)
		},
		MaxIdleConns: 4,

		IdleConnTimeout:       10 * time.Minute,
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &http.Client{Transport: tr}, nil
}

func socksDialer(socksAddr, isoTag string) (proxy.ContextDialer, error) {
	if isoTag == "" {
		return nil, errors.New("xport: isoTag must not be empty (circuit isolation)")
	}
	auth := &proxy.Auth{User: isoTag, Password: "x"}
	d, err := proxy.SOCKS5("tcp", socksAddr, auth, proxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("xport: socks5 dialer: %w", err)
	}
	cd, ok := d.(proxy.ContextDialer)
	if !ok {
		return nil, errors.New("xport: socks5 dialer lacks ContextDialer")
	}
	return cd, nil
}
