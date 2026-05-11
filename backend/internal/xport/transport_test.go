package xport

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestHandler(r Receiver) http.Handler { return NewServer(-1, r, nil, nil) }

func TestRoundTrip_OK(t *testing.T) {
	var got Envelope
	var once sync.Once
	srv := httptest.NewServer(newTestHandler(ReceiverFunc(func(ctx context.Context, e Envelope) error {
		once.Do(func() { got = e })
		return nil
	})))
	defer srv.Close()

	c := &Client{HTTP: srv.Client()}
	want := Envelope{ID: "abc-123", Timestamp: time.Now().Unix(), From: "sender", Payload: []byte("opaque-bytes")}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := c.Send(ctx, srv.URL, want); err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID || got.Timestamp != want.Timestamp || got.From != want.From || !bytes.Equal(got.Payload, want.Payload) {
		t.Errorf("received %+v, want %+v", got, want)
	}
}

func TestServer_RejectsWrongContentType(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(ReceiverFunc(func(ctx context.Context, e Envelope) error {
		t.Error("handler should not be reached")
		return nil
	})))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/message", "text/plain", strings.NewReader("nope"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want 415", resp.StatusCode)
	}
}

func TestServer_RejectsMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(ReceiverFunc(func(ctx context.Context, e Envelope) error {
		t.Error("handler should not be reached")
		return nil
	})))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/message", "application/json", strings.NewReader("{not json"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestServer_RejectsEmptyBody(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(ReceiverFunc(func(ctx context.Context, e Envelope) error {
		return nil
	})))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/message", "application/json", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestServer_RejectsMissingID(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(ReceiverFunc(func(ctx context.Context, e Envelope) error {
		t.Error("handler should not be reached for invalid envelope")
		return nil
	})))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/message", "application/json", strings.NewReader(`{"ts":1,"from":"x","payload":"","mac":""}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestServer_RejectsUnknownFields(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(ReceiverFunc(func(ctx context.Context, e Envelope) error {
		t.Error("handler should not be reached")
		return nil
	})))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/message", "application/json",
		strings.NewReader(`{"id":"x","ts":1,"from":"y","payload":"","mac":"","rogue":"field"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 on unknown field", resp.StatusCode)
	}
}

func TestServer_OversizeRejected(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(ReceiverFunc(func(ctx context.Context, e Envelope) error {
		t.Error("handler should not be reached for oversize body")
		return nil
	})))
	defer srv.Close()

	big := bytes.Repeat([]byte("A"), MaxEnvelopeBytes+1024)
	resp, err := http.Post(srv.URL+"/message", "application/json", bytes.NewReader(big))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge && resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 413 or 400 (oversize)", resp.StatusCode)
	}
}

func TestServer_ReceiverErrorReturns500(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(ReceiverFunc(func(ctx context.Context, e Envelope) error {
		return errors.New("kaboom")
	})))
	defer srv.Close()

	c := &Client{HTTP: srv.Client()}
	_, err := c.Send(context.Background(), srv.URL, Envelope{ID: "x", Timestamp: 1, From: "y", Payload: []byte("")})
	if err == nil {
		t.Fatal("expected error from 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("err = %v, want 500", err)
	}
}

func TestServer_WrongMethod(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(ReceiverFunc(func(ctx context.Context, e Envelope) error {
		return nil
	})))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/message")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

func TestHealth_OK(t *testing.T) {
	srv := httptest.NewServer(newTestHandler(ReceiverFunc(func(ctx context.Context, e Envelope) error {
		return nil
	})))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestClient_PropagatesContextCancel(t *testing.T) {
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-done:
		}
	}))
	defer func() { close(done); srv.Close() }()

	c := &Client{HTTP: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := c.Send(ctx, srv.URL, Envelope{ID: "x"})
	if err == nil {
		t.Fatal("expected error from context deadline")
	}
}
