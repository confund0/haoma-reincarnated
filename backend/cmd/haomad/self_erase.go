package main

import (
	"encoding/hex"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

func (d *daemon) watchForRemoval(peerID string, hc *http.Client) *http.Client {
	if hc == nil {
		return nil
	}
	next := hc.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	hc.Transport = &removalWatch{d: d, peerID: peerID, next: next}
	return hc
}

type removalWatch struct {
	d      *daemon
	peerID string
	next   http.RoundTripper
}

func (t *removalWatch) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	if err != nil || resp == nil || resp.StatusCode != http.StatusTeapot {
		return resp, err
	}
	t.d.readDestroyAnswer(t.peerID, req.URL.Host, resp.Header)
	return resp, err
}

const railAnswerDebounce = time.Minute

var railAnswerDebounceForTest = railAnswerDebounce

type railAnswerGate struct {
	mu sync.Mutex
	at map[string]time.Time
}

func (g *railAnswerGate) admit(peerID string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if last, ok := g.at[peerID]; ok && now.Sub(last) < railAnswerDebounceForTest {
		return false
	}
	if g.at == nil {
		g.at = make(map[string]time.Time)
	}
	g.at[peerID] = now
	return true
}

func (d *daemon) readDestroyAnswer(peerID, host string, header http.Header) bool {
	proofHex := header.Get(destroyProofHeader)
	if proofHex == "" {

		slog.Debug("self-erase: a peer answered 418 with no proof; ignored",
			slog.String("peer_id", peerID),
		)
		return false
	}
	if id, ok := d.peerIDByDest(host); !ok || id != peerID {

		slog.Debug("self-erase: 418 came from an address this device does not dial for that peer; ignored",
			slog.String("peer_id", peerID),
		)
		return false
	}
	if !d.railAnswers.admit(peerID, time.Now()) {

		slog.Debug("self-erase: that device's answer was checked a moment ago; ignored",
			slog.String("peer_id", peerID),
		)
		return false
	}
	proof, err := hex.DecodeString(proofHex)
	if err != nil {

		proof = nil
	}
	outcome, err := checkDestroyProof(d, "", proofSourceRailAnswer, proof, peerID)
	if err != nil {
		slog.Warn("self-erase: the proof could not be checked",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return false
	}
	if outcome != destroyProofRight {
		return false
	}

	if d.cfg.dataRoot == "" {
		return false
	}

	eraseID := newEraseID()
	slog.Info("self-erase: the other device says this device was removed; erasing",
		slog.String("peer_id", peerID),
		slog.String("erase_id", eraseID),
	)
	go d.eraseAndExit("the other device says this device was removed", eraseID)
	return true
}
