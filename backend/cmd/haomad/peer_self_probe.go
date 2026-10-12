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
	"haoma/internal/logid"
)

const peerSelfProbeCacheTTL = 2 * time.Minute

const peerSelfReachPushInterval = 20 * time.Second

const peerSelfProbeTimeout = 30 * time.Second

type peerSelfReachState struct {
	PeerID string    `json:"peer_id"`
	Onion  string    `json:"-"`
	Ok     bool      `json:"ok"`
	At     time.Time `json:"at"`
	Err    string    `json:"err,omitempty"`
}

type peerSelfReachPayload struct {
	PeerID string `json:"peer_id"`
	Ok     bool   `json:"ok"`
	At     int64  `json:"at"`
}

type peerSelfProbe struct {
	mu sync.Mutex

	cache map[string]peerSelfReachState

	envelope map[string]selfReachEnvelope

	inflight map[string]chan struct{}
}

type selfReachEnvelope struct {
	at    time.Time
	onion string
}

func newPeerSelfProbe() *peerSelfProbe {
	return &peerSelfProbe{
		cache:    make(map[string]peerSelfReachState),
		envelope: make(map[string]selfReachEnvelope),
		inflight: make(map[string]chan struct{}),
	}
}

func (p *peerSelfProbe) markEnvelope(peerID, onion string, now time.Time, publish func(peerSelfReachState)) (peerSelfReachState, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.envelope[peerID] = selfReachEnvelope{at: now, onion: onion}

	if cur, ok := p.cache[peerID]; ok && cur.Ok && cur.Onion == onion && now.Sub(cur.At) < peerSelfReachPushInterval {
		return cur, false
	}
	return p.storeLocked(peerSelfReachState{PeerID: peerID, Onion: onion, Ok: true, At: now}, publish), true
}

func (p *peerSelfProbe) settle(s peerSelfReachState, now time.Time, publish func(peerSelfReachState)) (peerSelfReachState, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cur, have := p.cache[s.PeerID]
	if !s.Ok {
		if e, ok := p.envelope[s.PeerID]; ok && e.onion == s.Onion && now.Sub(e.at) < peerSelfProbeCacheTTL {

			if have && cur.Ok && cur.Onion == e.onion && !e.at.After(cur.At) {
				return cur, false
			}
			return p.storeLocked(peerSelfReachState{PeerID: s.PeerID, Onion: e.onion, Ok: true, At: e.at}, publish), true
		}
	}
	if s.Ok && have && cur.Ok && cur.Onion == s.Onion && cur.At.After(s.At) {
		return cur, false
	}
	return p.storeLocked(s, publish), true
}

func (p *peerSelfProbe) storeLocked(s peerSelfReachState, publish func(peerSelfReachState)) peerSelfReachState {
	p.cache[s.PeerID] = s
	if publish != nil {
		publish(s)
	}
	return s
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

	if d.unlinkRefusesPeer(peerID) {
		return peerSelfReachState{PeerID: peerID, Onion: onion, Err: errUnlinkPeerRemoved.Error()}
	}

	if !force {
		if s, fresh := d.selfProbe.snapshot(peerID); fresh && s.Onion == onion {
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
		if s.Onion != onion {
			return peerSelfReachState{PeerID: peerID, Onion: onion, Err: "the probe in flight was for another onion"}
		}
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

		hc, herr := d.httpClientForPeer(peerID)
		if herr != nil {
			state.Err = herr.Error()
		} else {
			resp, err := hc.Do(req)
			if err != nil {
				state.Err = err.Error()
			} else {
				defer resp.Body.Close()
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<14))
				state.Ok = true
			}
		}
	}
	probe := state
	state, _ = d.selfProbe.settle(probe, time.Now(), d.publishSelfReach)

	slog.Info("peer self-probe",
		slog.String("peer_id", peerID),
		slog.String("onion", onion),
		slog.Bool("ok", probe.Ok),
		slog.String("err", probe.Err),
		slog.Bool("kept_ok", state.Ok && !probe.Ok),
	)
	return state
}

func (d *daemon) noteSelfReachFromEnvelope(peerID, host string) {

	if d.selfProbe == nil || d.registry == nil || peerID == "" {
		return
	}
	host = onionServiceID(host)
	if host == "" {
		return
	}
	peer, err := d.registry.Get(peerID)
	if err != nil || peer == nil || peer.MyOnionAddr == "" || peer.MyOnionAddr != host {
		return
	}

	if d.unlinkRefusesPeer(peerID) {
		return
	}
	if _, published := d.selfProbe.markEnvelope(peerID, peer.MyOnionAddr, time.Now(), d.publishSelfReach); !published {
		return
	}
	slog.Info("peer self-reach ok from a verified envelope",
		slog.String("peer_id", peerID),
		slog.String("onion", logid.Short(peer.MyOnionAddr)),
	)
}

func (d *daemon) publishSelfReach(s peerSelfReachState) {
	if d.bus == nil {
		return
	}
	d.bus.Publish(eventbus.TopicPeerSelfReachChanged, peerSelfReachPayload{
		PeerID: s.PeerID,
		Ok:     s.Ok,
		At:     s.At.Unix(),
	})
}
