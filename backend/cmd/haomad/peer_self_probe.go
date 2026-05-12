package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"haoma/internal/eventbus"
)

const peerSelfProbeCacheTTL = 2 * time.Minute

const peerSelfProbeTimeout = 30 * time.Second

type peerSelfReachState struct {
	PeerID string    `json:"peer_id"`
	Onion  string    `json:"onion"`
	Ok     bool      `json:"ok"`
	At     time.Time `json:"at"`
	Err    string    `json:"err,omitempty"`
}

type peerSelfReachPayload struct {
	PeerID string `json:"peer_id"`
	Onion  string `json:"onion,omitempty"`
	Ok     bool   `json:"ok"`
	At     int64  `json:"at"`
}

type peerSelfProbe struct {
	mu    sync.Mutex
	cache map[string]peerSelfReachState

	inflight map[string]chan struct{}
}

func newPeerSelfProbe() *peerSelfProbe {
	return &peerSelfProbe{
		cache:    make(map[string]peerSelfReachState),
		inflight: make(map[string]chan struct{}),
	}
}

func (p *peerSelfProbe) snapshot(peerID string) (peerSelfReachState, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.cache[peerID]
	if !ok {
		return peerSelfReachState{}, false
	}
	return s, time.Since(s.At) < peerSelfProbeCacheTTL
}

func (p *peerSelfProbe) store(s peerSelfReachState) {
	p.mu.Lock()
	p.cache[s.PeerID] = s
	p.mu.Unlock()
}

func (d *daemon) probePeerSelf(ctx context.Context, peerID, onion string, force bool) peerSelfReachState {
	if d.selfProbe == nil {
		d.selfProbe = newPeerSelfProbe()
	}
	if onion == "" {
		return peerSelfReachState{PeerID: peerID, Err: "no onion"}
	}
	if !force {
		if s, fresh := d.selfProbe.snapshot(peerID); fresh {
			return s
		}
	}

	d.selfProbe.mu.Lock()
	if existing, ok := d.selfProbe.inflight[peerID]; ok {
		d.selfProbe.mu.Unlock()
		select {
		case <-ctx.Done():
			return peerSelfReachState{PeerID: peerID, Onion: onion, Err: ctx.Err().Error()}
		case <-existing:
		}
		s, _ := d.selfProbe.snapshot(peerID)
		return s
	}
	done := make(chan struct{})
	d.selfProbe.inflight[peerID] = done
	d.selfProbe.mu.Unlock()
	defer func() {
		d.selfProbe.mu.Lock()
		delete(d.selfProbe.inflight, peerID)
		d.selfProbe.mu.Unlock()
		close(done)
	}()

	state := peerSelfReachState{PeerID: peerID, Onion: onion, At: time.Now()}
	url := fmt.Sprintf("http://%s.onion/", onion)
	reqCtx, cancel := context.WithTimeout(ctx, peerSelfProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		state.Err = err.Error()
	} else {
		resp, err := d.torHTTP.Do(req)
		if err != nil {
			state.Err = err.Error()
		} else {
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<14))
			state.Ok = true
		}
	}
	d.selfProbe.store(state)
	slog.Info("peer self-probe",
		slog.String("peer_id", peerID),
		slog.String("onion", onion),
		slog.Bool("ok", state.Ok),
		slog.String("err", state.Err),
	)
	if d.bus != nil {
		d.bus.Publish(eventbus.TopicPeerSelfReachChanged, peerSelfReachPayload{
			PeerID: state.PeerID,
			Onion:  state.Onion,
			Ok:     state.Ok,
			At:     state.At.Unix(),
		})
	}
	return state
}
