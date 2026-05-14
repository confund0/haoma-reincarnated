package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"haoma/internal/xport"
)

func (d *daemon) peerIsoTag(peerID string) string {
	return "haomad-peer:" + peerID
}

func (d *daemon) httpClientForPeer(peerID string) (*http.Client, error) {
	hc, err := xport.NewTorHTTPClient(d.cfg.torSocks, d.peerIsoTag(peerID))
	if err != nil {
		return nil, err
	}
	hc.Timeout = 60 * time.Second
	return hc, nil
}

func (d *daemon) peerIDByDest(dest string) (string, bool) {
	onion := onionFromDest(dest)
	if onion == "" {
		return "", false
	}
	peer, err := d.registry.ByAddress(onion)
	if err != nil {
		return "", false
	}
	return peer.ID, true
}

type perPeerSender struct {
	d *daemon
}

func (s *perPeerSender) Send(ctx context.Context, dest string, env xport.Envelope) ([]byte, error) {
	peerID, ok := s.d.peerIDByDest(dest)
	if !ok {
		return nil, fmt.Errorf("outbox: dest %q does not map to a known peer", dest)
	}
	hc, err := s.d.httpClientForPeer(peerID)
	if err != nil {
		return nil, err
	}
	inner := &xport.Client{HTTP: hc}
	return inner.Send(ctx, dest, env)
}
