package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/peerstate"
	"haoma-frontend/internal/presence"
	"haoma-frontend/internal/session"
	"haoma-frontend/internal/signal"
	"haoma-frontend/internal/store"
)

func minimalDaemon(t *testing.T, backendURL string) *daemon {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Lock() })
	sigState, _, err := signal.LoadOrBootstrap(st, 3)
	if err != nil {
		t.Fatal(err)
	}
	stores := signal.NewStores(st, sigState)
	bus := events.NewBus()
	return &daemon{
		dataDir:       dir,
		store:         st,
		signalState:   sigState,
		stores:        stores,
		cipher:        session.New(stores),
		peerSeq:       peerstate.New(st),
		chats:         chat.NewStore(st),
		events:        events.New(st, bus, nil),
		eventBus:      bus,
		backendClient: backendapi.New(backendURL, "", nil),
		ipcSrv:        ipc.NewServer("t"),
		presenceCache: presence.New(),
	}
}

func TestNotificationToStatusEvent(t *testing.T) {
	n := backendapi.Notification{
		Event: backendapi.Event{
			Kind: "bad_mac", At: "2026-04-21T19:00:00Z",
			PeerID: "p-1", SourceAddr: "attacker", SlotIdx: 1,
		},
		Actions: []json.RawMessage{json.RawMessage(`{"type":"counter_bump"}`)},
	}
	got := notificationToStatusEvent(n)
	if len(got.Event) == 0 {
		t.Fatal("Event bytes empty")
	}
	var roundTrip backendapi.Event
	if err := json.Unmarshal(got.Event, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip.Kind != "bad_mac" || roundTrip.SlotIdx != 1 {
		t.Errorf("round-trip = %+v", roundTrip)
	}
}

func TestSweepInbox_ProcessesAndAcks(t *testing.T) {
	var (
		deletes sync.Map
		polls   atomic.Int32
		entry   = backendapi.InboxEntry{ArrivalAt: 42, PeerID: "p1", Envelope: backendapi.RawEnvelope{ID: "env-1", From: "alice", Kind: "text", Payload: []byte("hi")}}
	)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /inbox", func(w http.ResponseWriter, _ *http.Request) {
		polls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(backendapi.InboxResponse{Entries: []backendapi.InboxEntry{entry}})
	})
	mux.HandleFunc("DELETE /inbox/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/inbox/")
		deletes.Store(id, true)
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	d := minimalDaemon(t, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	sweepInbox(ctx, d)

	got, _ := deletes.Load("env-1")
	if got != true {
		t.Error("inbox entry was never DELETEd; ack path missing")
	}
	if polls.Load() != 1 {
		t.Errorf("inbox polls = %d, want 1", polls.Load())
	}
}

func TestSweepInbox_SurvivesBackendError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "backend broken", http.StatusInternalServerError)
	}))
	defer srv.Close()

	d := minimalDaemon(t, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	sweepInbox(ctx, d)
}

func TestEventsLoop_OnPresence_FeedsCache(t *testing.T) {

	mux := http.NewServeMux()
	mux.HandleFunc("GET /inbox", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(backendapi.InboxResponse{})
	})
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		_, _ = w.Write([]byte("event: peer.presence-changed\n" +
			`data: {"peer_id":"alice","source":"haomad","at":1700000000}` + "\n\n"))
		flusher.Flush()
		<-r.Context().Done()
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	d := minimalDaemon(t, srv.URL)

	ch, cancel := d.presenceCache.Subscribe(4)
	defer cancel()

	ctx, cancelCtx := context.WithCancel(context.Background())
	defer cancelCtx()

	go eventsLoop(ctx, d)

	select {
	case ev := <-ch:
		if ev.PeerID != "alice" {
			t.Errorf("peer_id = %q, want alice", ev.PeerID)
		}
		if !ev.Snapshot.Accepting {
			t.Errorf("accepting = false, want true after technical signal")
		}

		if ev.Snapshot.Effective != "accepting" {
			t.Errorf("effective = %q, want accepting", ev.Snapshot.Effective)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cache never observed the SSE presence_observed signal")
	}
}
