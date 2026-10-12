package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/ipc"
)

const unreachablePeerID = "0f3a9c27d41e48b6a5c2e8f19d07b364"

func TestPeerUnreachable_FlipPushesPeerUpdatedWithTheMark(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	stub.Peer = backendapi.Peer{ID: unreachablePeerID, Unreachable: true}
	d, addr, certPath, token := newTestDaemon(t, stub)
	client := dialTest(t, ctx, addr, certPath, token)
	onFlip := peerUnreachableHandler(ctx, d)

	onFlip(backendapi.UnreachableObservation{PeerID: unreachablePeerID, Unreachable: true})
	if got := readPeerUpdated(t, ctx, client); got.ID != unreachablePeerID || !got.Unreachable {
		t.Fatalf("peer.updated after the mark was set = %+v, want peer %s unreachable", got, unreachablePeerID)
	}

	stub.Peer = backendapi.Peer{ID: unreachablePeerID, Unreachable: false, LastPassiveAt: 1754903102}
	onFlip(backendapi.UnreachableObservation{PeerID: unreachablePeerID, Unreachable: false})
	if got := readPeerUpdated(t, ctx, client); got.ID != unreachablePeerID || got.Unreachable {
		t.Fatalf("peer.updated after the mark cleared = %+v, want peer %s not unreachable", got, unreachablePeerID)
	}
}

func TestPeerUnreachable_NoPushWhenHaomadDoesNotAnswer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	client := dialTest(t, ctx, addr, certPath, token)

	stub.close()

	peerUnreachableHandler(ctx, d)(backendapi.UnreachableObservation{PeerID: unreachablePeerID, Unreachable: false})

	readCtx, readCancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer readCancel()
	for {
		_, data, err := client.Read(readCtx)
		if err != nil {
			return
		}
		f, err := ipc.Decode(data)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if f.Type == ipc.FramePeerUpdated {
			t.Fatalf("peer.updated was pushed although haomad did not answer for the row: %s", f.Payload)
		}
	}
}

func TestPeerUnreachable_PeerListCarriesTheMark(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	live := "7c1e0b5a92d34f68b0a4c3e2d1f09876"
	stub.PeerList = []backendapi.Peer{
		{ID: unreachablePeerID, Unreachable: true},
		{ID: live},
	}
	_, addr, certPath, token := newTestDaemon(t, stub)
	client := dialTest(t, ctx, addr, certPath, token)

	list, err := ipc.NewFrame(ipc.FrameListPeers, "c-unreach-1", struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	writeFrame(t, ctx, client, list)
	f := readUntil(t, ctx, client, ipc.FramePeersListed)
	var resp ipc.PeersListedResponse
	if err := json.Unmarshal(f.Payload, &resp); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, p := range resp.Peers {
		got[p.ID] = p.Unreachable
	}
	if len(got) != 2 {
		t.Fatalf("peer list holds %d rows, want 2: %+v", len(got), resp.Peers)
	}
	if !got[unreachablePeerID] {
		t.Errorf("the marked pairing is listed without the mark")
	}
	if got[live] {
		t.Errorf("the live pairing is listed with the mark")
	}
}

func TestPeerUnreachable_EventsStreamCallsTheHandler(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("event: peer.unreachable-changed\n" +
			`data: {"peer_id":"` + unreachablePeerID + `","unreachable":true}` + "\n\n"))
		flusher.Flush()
		<-r.Context().Done()
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	got := make(chan backendapi.UnreachableObservation, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		_ = backendapi.New(srv.URL, "", nil).Events(ctx, backendapi.EventsOpts{
			OnUnreachable: func(o backendapi.UnreachableObservation) { got <- o },
		})
	}()
	select {
	case o := <-got:
		if o.PeerID != unreachablePeerID || !o.Unreachable {
			t.Fatalf("observation = %+v, want peer %s unreachable", o, unreachablePeerID)
		}
	case <-ctx.Done():
		t.Fatal("peer.unreachable-changed never reached OnUnreachable")
	}
}

func readPeerUpdated(t *testing.T, ctx context.Context, conn *websocket.Conn) ipc.PeerEntry {
	t.Helper()
	f := readUntil(t, ctx, conn, ipc.FramePeerUpdated)
	var p ipc.PeerUpdatedPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		t.Fatalf("decode peer.updated: %v", err)
	}
	return p.Peer
}
