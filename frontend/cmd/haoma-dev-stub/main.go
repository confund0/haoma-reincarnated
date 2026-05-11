package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18780", "listen addr")
	eventEvery := flag.Duration("event-every", 3*time.Second, "emit a fake IDS event on this interval")
	inboxEvery := flag.Duration("inbox-every", 5*time.Second, "append a fake inbox entry on this interval")
	flag.Parse()

	s := &stub{
		eventSubs: map[chan struct{}]struct{}{},
		inbox:     []inboxEntry{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("POST /onion/mint", s.onionMint)
	mux.HandleFunc("GET /peers", s.peers)
	mux.HandleFunc("POST /peers", s.addPeer)
	mux.HandleFunc("POST /send", s.send)
	mux.HandleFunc("GET /inbox", s.inboxList)
	mux.HandleFunc("DELETE /inbox/{id}", s.inboxDelete)
	mux.HandleFunc("GET /events", s.events)

	go s.runEventGenerator(*eventEvery)
	go s.runInboxGenerator(*inboxEvery)

	log.Printf("haoma-dev-stub listening on %s", *addr)
	log.Printf("  event every %s, inbox every %s", *eventEvery, *inboxEvery)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

type stub struct {
	mu        sync.Mutex
	inbox     []inboxEntry
	eventSeq  int
	inboxSeq  int
	eventSubs map[chan struct{}]struct{}
	lastEvent string
}

type inboxEntry struct {
	ArrivalAt int64          `json:"arrival_at"`
	PeerID    string         `json:"peer_id"`
	Envelope  map[string]any `json:"envelope"`
}

func (s *stub) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]string{"status": "ok", "version": "dev-stub"})
}

func (s *stub) onionMint(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"address":     "stubmintAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA1",
		"private_key": "ZmFrZS1iYXNlNjQta2V5",
	})
}

func (s *stub) peers(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"peers": []map[string]any{
		{"id": "stub-peer-1", "nickname": "alice-stub", "known_addresses": []string{"alice-stub-addr"}},
	}})
}

func (s *stub) addPeer(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		http.Error(w, "want application/json", http.StatusUnsupportedMediaType)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *stub) send(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		http.Error(w, "want application/json", http.StatusUnsupportedMediaType)
		return
	}
	s.mu.Lock()
	s.inboxSeq++
	id := fmt.Sprintf("stub-env-out-%d", s.inboxSeq)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{"envelope_id": id, "queued": false})
}

func (s *stub) inboxList(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	entries := append([]inboxEntry{}, s.inbox...)
	s.mu.Unlock()
	writeJSON(w, map[string]any{"entries": entries})
}

func (s *stub) inboxDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	kept := s.inbox[:0]
	for _, e := range s.inbox {
		if e.Envelope["id"] != id {
			kept = append(kept, e)
		}
	}
	s.inbox = kept
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *stub) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	sub := make(chan struct{}, 4)
	s.mu.Lock()
	s.eventSubs[sub] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.eventSubs, sub)
		s.mu.Unlock()
	}()

	keep := time.NewTicker(10 * time.Second)
	defer keep.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-keep.C:
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case <-sub:
			s.mu.Lock()
			frame := s.lastEvent
			s.mu.Unlock()
			if frame == "" {
				continue
			}
			if _, err := w.Write([]byte(frame)); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *stub) runEventGenerator(every time.Duration) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	kinds := []string{"bad_mac", "unknown_source", "connection_accepted", "probe_all_dead"}
	for range tick.C {
		s.mu.Lock()
		s.eventSeq++
		kind := kinds[s.eventSeq%len(kinds)]
		n := map[string]any{
			"event": map[string]any{
				"kind":        kind,
				"at":          time.Now().Format(time.RFC3339),
				"peer_id":     fmt.Sprintf("stub-peer-%d", s.eventSeq%3),
				"source_addr": fmt.Sprintf("src-%d", s.eventSeq),
				"slot_idx":    s.eventSeq % 2,
			},
			"actions": []map[string]any{
				{"type": "counter_bump", "count": 1},
			},
		}
		data, _ := json.Marshal(n)
		frame := "event: system.ids-event\ndata: " + string(data) + "\n\n"
		s.lastEvent = frame
		for sub := range s.eventSubs {
			select {
			case sub <- struct{}{}:
			default:
			}
		}
		s.mu.Unlock()
	}
}

func (s *stub) runInboxGenerator(every time.Duration) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for range tick.C {
		s.mu.Lock()
		s.inboxSeq++
		id := fmt.Sprintf("stub-env-%d", s.inboxSeq)
		payload := []byte(fmt.Sprintf("stub message #%d at %s", s.inboxSeq, time.Now().Format(time.RFC3339)))
		s.inbox = append(s.inbox, inboxEntry{
			ArrivalAt: time.Now().UnixNano(),
			PeerID:    "stub-peer-1",
			Envelope: map[string]any{
				"id":      id,
				"ts":      time.Now().Unix(),
				"from":    "alice-stub-addr",
				"kind":    "text",
				"payload": payload,
				"mac":     []byte(strings.Repeat("\x00", 32)),
			},
		})
		s.mu.Unlock()
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
