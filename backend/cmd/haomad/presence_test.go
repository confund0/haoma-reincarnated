package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"haoma/internal/peers"
	"haoma/internal/eventbus"
	"haoma/internal/ids"
	"haoma/internal/xport"
)

func freshTestPeer(t *testing.T, addrs ...string) peers.Peer {
	t.Helper()
	id, err := peers.NewPeerID()
	if err != nil {
		t.Fatal(err)
	}
	in, err := peers.NewPeerSecret()
	if err != nil {
		t.Fatal(err)
	}
	out, err := peers.NewPeerSecret()
	if err != nil {
		t.Fatal(err)
	}
	return peers.Peer{
		ID:             id,
		KnownAddresses: addrs,
		InboundSecret:  in,
		OutboundSecret: out,
	}
}

func signedAck(t *testing.T, peer peers.Peer, source string) []byte {
	t.Helper()
	env := xport.Envelope{
		ID:             "ack-1",
		Timestamp:      time.Now().Unix(),
		From:           peer.KnownAddresses[0],
		Kind:           xport.KindSentAck,
		PresenceSource: source,
		Payload:        []byte(`{"acked_id":"src-1"}`),
	}
	if err := xport.RandomPadding(&env); err != nil {
		t.Fatal(err)
	}
	signed := xport.Sign(env, peer.InboundSecret)
	body, err := json.Marshal(signed)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func subscribePresence(t *testing.T, d *daemon) (<-chan peerPresenceObservation, func()) {
	t.Helper()
	raw, cancel := d.bus.Subscribe(eventbus.TopicPeerPresenceChanged, 4)
	out := make(chan peerPresenceObservation, 4)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case ev, ok := <-raw:
				if !ok {
					return
				}
				obs, ok := ev.Payload.(peerPresenceObservation)
				if !ok {
					t.Errorf("payload type = %T, want peerPresenceObservation", ev.Payload)
					continue
				}
				out <- obs
			}
		}
	}()
	return out, func() { close(done); cancel() }
}

func TestVerifyAckBody_PublishesObservation_OnPresenceSource(t *testing.T) {
	d := newTestDaemon(t)

	peer := freshTestPeer(t, "alice-slot-0")
	if err := d.registry.Add(peer); err != nil {
		t.Fatal(err)
	}

	ch, cancel := subscribePresence(t, d)
	defer cancel()

	body := signedAck(t, peer, xport.PresenceSourceHaomad)
	if err := verifyAckBody(d, body, "http://alice-slot-0.onion"); err != nil {
		t.Fatalf("verifyAckBody: %v", err)
	}

	select {
	case obs := <-ch:
		if obs.PeerID != peer.ID {
			t.Errorf("peer_id = %q, want %q", obs.PeerID, peer.ID)
		}
		if obs.Source != xport.PresenceSourceHaomad {
			t.Errorf("source = %q, want %q", obs.Source, xport.PresenceSourceHaomad)
		}
		if obs.At == 0 {
			t.Errorf("at = 0, want non-zero")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("no observation published within 200ms")
	}
}

func TestVerifyAckBody_NoPublish_WhenSourceEmpty(t *testing.T) {
	d := newTestDaemon(t)

	peer := freshTestPeer(t, "alice-slot-0")
	if err := d.registry.Add(peer); err != nil {
		t.Fatal(err)
	}

	ch, cancel := subscribePresence(t, d)
	defer cancel()

	body := signedAck(t, peer, "")
	if err := verifyAckBody(d, body, "http://alice-slot-0.onion"); err != nil {
		t.Fatalf("verifyAckBody: %v", err)
	}

	select {
	case obs := <-ch:
		t.Fatalf("unexpected observation: %+v", obs)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestVerifyAckBody_NoPublish_OnMACFailure(t *testing.T) {
	d := newTestDaemon(t)

	peer := freshTestPeer(t, "alice-slot-0")
	if err := d.registry.Add(peer); err != nil {
		t.Fatal(err)
	}

	ch, cancel := subscribePresence(t, d)
	defer cancel()

	body := signedAck(t, peer, xport.PresenceSourceHaoma)
	tampered := strings.Replace(string(body), `"id":"ack-1"`, `"id":"ack-2"`, 1)
	if tampered == string(body) {
		t.Fatal("tamper did not change the body")
	}

	if err := verifyAckBody(d, []byte(tampered), "http://alice-slot-0.onion"); err == nil {
		t.Fatal("verifyAckBody accepted a tampered ack")
	}

	select {
	case obs := <-ch:
		t.Fatalf("unexpected observation after MAC failure: %+v", obs)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestAPI_Events_StreamsPresenceObserved(t *testing.T) {
	d := newTestDaemon(t)
	d.ids = ids.New()

	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	time.Sleep(50 * time.Millisecond)
	d.bus.Publish(eventbus.TopicPeerPresenceChanged, peerPresenceObservation{
		PeerID: "peer-x",
		Source: xport.PresenceSourceHaoma,
		At:     1700000000,
	})

	scanner := bufio.NewScanner(resp.Body)
	deadline := time.After(1 * time.Second)
	type frame struct {
		event string
		data  string
	}
	got := make(chan frame, 1)
	go func() {
		var ev string
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				ev = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				if ev == "peer.presence-changed" {
					got <- frame{event: ev, data: strings.TrimPrefix(line, "data: ")}
					return
				}
			}
		}
	}()

	select {
	case f := <-got:
		var obs peerPresenceObservation
		if err := json.Unmarshal([]byte(f.data), &obs); err != nil {
			t.Errorf("unmarshal SSE payload: %v (raw: %s)", err, f.data)
		}
		if obs.PeerID != "peer-x" {
			t.Errorf("peer_id = %q, want peer-x", obs.PeerID)
		}
		if obs.Source != xport.PresenceSourceHaoma {
			t.Errorf("source = %q, want %q", obs.Source, xport.PresenceSourceHaoma)
		}
	case <-deadline:
		t.Fatal("no presence_observed SSE frame within 1s")
	}
}
