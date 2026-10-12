package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/logging"
)

const leakOnion = "dummyaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaad"

var leakBody = `pair: dial onion: Post "http://` + leakOnion + `.onion:9999/pair": socks connect tcp 127.0.0.1:9050->` + leakOnion + `.onion:9999: general SOCKS server failure`

func captureDefaultLog(t *testing.T) func() string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "haoma.log")
	logger, closer, err := logging.New(logging.Config{Level: "debug", File: path, Service: "haoma"})
	if err != nil {
		t.Fatal(err)
	}
	prev := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() {
		slog.SetDefault(prev)
		_ = closer()
	})
	return func() string {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read captured log: %v", err)
		}
		return string(b)
	}
}

func leakyPairStub(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /onion/mint", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(backendapi.MintedOnion{
			Address:    "stubmintAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA1",
			PrivateKey: "ZmFrZQ==",
		})
	})
	mux.HandleFunc("POST /pair/onion/accept", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": leakBody})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestPairOnionAccept_DialFailureKeepsTheAddressOnTheDaemon(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	readLog := captureDefaultLog(t)

	d, addr, certPath, token := newTestDaemon(t, startHaomadStub(t, []string{"unused-onion"}, http.StatusCreated))
	d.backendClient = backendapi.New(leakyPairStub(t), "", nil)

	conn := dialTest(t, ctx, addr, certPath, token)
	req, _ := ipc.NewFrame(ipc.FramePairOnionAccept, "req-accept-leak", ipc.PairOnionAcceptRequest{
		Words: []string{"acid", "acorn", "acre", "acts", "afar", "affix", "aged"},
	})
	writeFrame(t, ctx, conn, req)

	resp := readUntil(t, ctx, conn, ipc.FrameError)
	var payload ipc.ErrorPayload
	if err := json.Unmarshal(resp.Payload, &payload); err != nil {
		t.Fatal(err)
	}

	if payload.Code != "onion_accept_failed" {
		t.Fatalf("error code = %q, want the stable token onion_accept_failed", payload.Code)
	}
	if strings.Contains(payload.Message, leakOnion) {
		t.Fatalf("the peer's address reached the client frame: %q", payload.Message)
	}
	if strings.Contains(payload.Message, ".onion") {
		t.Fatalf("an address fragment reached the client frame: %q", payload.Message)
	}
	if payload.Message == "" {
		t.Fatal("the client was given a code with no sentence to show")
	}

	if s := readLog(); strings.Contains(s, leakOnion) {
		t.Fatalf("the peer's address reached haoma's log: %q", s)
	}
}

func TestClientDetail_DropsAnAddressWhole(t *testing.T) {
	readLog := captureDefaultLog(t)

	got := clientDetail("onion_accept_failed", leakBody)
	if got != detailWithheld {
		t.Fatalf("clientDetail = %q, want the fixed sentence %q", got, detailWithheld)
	}
	if s := readLog(); strings.Contains(s, leakOnion) {
		t.Fatalf("the withheld-detail warning logged the address it withheld: %q", s)
	}

	const clean = "peer 404 (token unknown)"
	if got := clientDetail("file_failed_permanent", clean); got != clean {
		t.Fatalf("clientDetail rewrote a detail that named no address: %q", got)
	}
}
