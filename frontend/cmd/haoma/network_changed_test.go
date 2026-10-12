package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/ipc"
)

func networkChangedBackend(t *testing.T, status int, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /network/changed", func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestHandleNetworkChanged_ReachesHaomad(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	backend, calls := networkChangedBackend(t, http.StatusOK, `{"dropped":false,"rows":3,"destinations":2,"fetches":1}`)
	d.backendClient = backendapi.New(backend.URL, "", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)
	f, _ := ipc.NewFrame(ipc.FrameNetworkChanged, "nc-1", ipc.NetworkChangedRequest{})
	writeFrame(t, ctx, conn, f)

	got := readUntil(t, ctx, conn, ipc.FrameNetworkChangeHandled)
	if got.ID != "nc-1" {
		t.Errorf("reply id = %q, want nc-1", got.ID)
	}
	var resp ipc.NetworkChangeHandledResponse
	if err := json.Unmarshal(got.Payload, &resp); err != nil {
		t.Fatal(err)
	}
	if want := (ipc.NetworkChangeHandledResponse{Rows: 3, Destinations: 2, Fetches: 1}); resp != want {
		t.Errorf("reply = %+v, want %+v", resp, want)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("POST /network/changed called %d times, want 1", n)
	}
}

func TestHandleNetworkChanged_RefusalIsAnError(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	backend, _ := networkChangedBackend(t, http.StatusInternalServerError, `{"error":"outbox kick failed"}`)
	d.backendClient = backendapi.New(backend.URL, "", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)
	f, _ := ipc.NewFrame(ipc.FrameNetworkChanged, "nc-2", ipc.NetworkChangedRequest{})
	writeFrame(t, ctx, conn, f)

	got := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	if err := json.Unmarshal(got.Payload, &ep); err != nil {
		t.Fatal(err)
	}
	if got.ID != "nc-2" || ep.Code != "network_changed_failed" {
		t.Errorf("error frame id=%q code=%q, want nc-2 network_changed_failed", got.ID, ep.Code)
	}
}
