//go:build faults

package main

import (
	"log/slog"
	"net/http"
	"strings"
	"sync"
)

const stallPeerCircuitEnv = "HAOMA_FAULT_STALL_PEER_CIRCUIT"

const (
	stallFaultOnLine      = "seeded fault on: requests to one peer stall on the tag of its first answer"
	stallFaultArmedLine   = "seeded fault armed: requests to the peer under this tag stall from now on"
	stallFaultStalledLine = "seeded fault: a request to the peer stalled"
	stallFaultThroughLine = "seeded fault: a request to the peer under a new tag got through"
)

type stallFault struct {
	peerID string

	mu       sync.Mutex
	armedTag string
	through  bool
}

func readStallFault(getenv func(string) string) *stallFault {
	id := strings.TrimSpace(getenv(stallPeerCircuitEnv))
	if id == "" {
		return nil
	}
	slog.Warn(stallFaultOnLine, slog.String("peer_id", id))
	return &stallFault{peerID: id}
}

func (f *stallFault) wrap(d *daemon, peerID, tag string, hc *http.Client) {
	if f == nil || peerID != f.peerID || hc == nil {
		return
	}
	next := hc.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	hc.Transport = &stallRoundTripper{f: f, d: d, tag: tag, next: next}
}

func (f *stallFault) stalls(tag string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.armedTag != "" && f.armedTag == tag
}

func (f *stallFault) answered(tag string) {
	f.mu.Lock()
	armed, through := false, false
	switch {
	case f.armedTag == "":
		f.armedTag = tag
		armed = true
	case tag != f.armedTag && !f.through:
		f.through = true
		through = true
	}
	f.mu.Unlock()
	if armed {
		slog.Info(stallFaultArmedLine, slog.String("peer_id", f.peerID))
	}
	if through {
		slog.Info(stallFaultThroughLine, slog.String("peer_id", f.peerID))
	}
}

type stallRoundTripper struct {
	f    *stallFault
	d    *daemon
	tag  string
	next http.RoundTripper
}

func (t *stallRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	host := strings.TrimSuffix(req.URL.Hostname(), ".onion")
	if !t.ownAddress(host) {
		return t.next.RoundTrip(req)
	}
	if t.f.stalls(t.tag) {
		slog.Info(stallFaultStalledLine, slog.String("peer_id", t.f.peerID))
		<-req.Context().Done()
		return nil, req.Context().Err()
	}
	resp, err := t.next.RoundTrip(req)
	if err == nil {
		t.f.answered(t.tag)
	}
	return resp, err
}

func (t *stallRoundTripper) ownAddress(host string) bool {
	if t.d == nil || t.d.registry == nil {
		return false
	}
	p, err := t.d.registry.Get(t.f.peerID)
	if err != nil {
		return false
	}
	return hasAddress(p.KnownAddresses, host)
}
