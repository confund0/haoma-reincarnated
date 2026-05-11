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

	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return cd.DialContext(ctx, network, addr)
		},
		MaxIdleConns: 4,

		IdleConnTimeout:       10 * time.Minute,
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &http.Client{Transport: tr}, nil
}
