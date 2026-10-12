package backendapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

const inboxID = "196d2fa679205436444893960a08e45c"

func TestClient_TransportErrorNamesTheIDShort(t *testing.T) {
	fb := newFakeBackend(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := New(fb.URL, "", nil).DeleteInboxEntry(ctx, inboxID)
	if err == nil {
		t.Fatal("a call on a cancelled context succeeded")
	}
	if strings.Contains(err.Error(), inboxID) {
		t.Errorf("the full id reached the error: %v", err)
	}
	if !strings.Contains(err.Error(), "/inbox/196d2fa6…") {
		t.Errorf("the error does not name the short id: %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(err, context.Canceled) = false for %v", err)
	}
}

func TestClient_StatusErrorNamesTheIDShort(t *testing.T) {
	fb := newFakeBackend(t)
	fb.on("DELETE", "/inbox/"+inboxID, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusInternalServerError)
	})
	err := New(fb.URL, "", nil).DeleteInboxEntry(context.Background(), inboxID)
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want a StatusError", err)
	}
	if strings.Contains(err.Error(), inboxID) || !strings.Contains(err.Error(), "/inbox/196d2fa6…") {
		t.Errorf("err = %v, want the short id and not the full one", err)
	}
}
