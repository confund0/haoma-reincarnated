package xport

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestServer_StampsTheHostTheRequestWasSentTo(t *testing.T) {
	const host = "myonionaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.onion"
	var verified, received string
	v := VerifierFunc(func(ctx context.Context, _ Envelope) error {
		verified = HostFromContext(ctx)
		return nil
	})
	r := ReceiverFunc(func(ctx context.Context, _ Envelope) error {
		received = HostFromContext(ctx)
		return nil
	})
	body, err := json.Marshal(Envelope{ID: "host-1", From: "sender", Payload: []byte("p")})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "http://"+host+"/message", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	NewServer(-1, r, v, nil).ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if verified != host || received != host {
		t.Fatalf("host seen by the verifier = %q, by the receiver = %q, want %q", verified, received, host)
	}
}

func TestHostFromContext_EmptyWithoutStamp(t *testing.T) {
	if h := HostFromContext(context.Background()); h != "" {
		t.Fatalf("host = %q, want empty", h)
	}
}
