package backendapi

import (
	"context"
	"net/http"
	"testing"
)

func TestWipeDevice_PostsToTheWipeRoute(t *testing.T) {
	fb := newFakeBackend(t)
	called := false
	fb.on(http.MethodPost, "/wipe", func(w http.ResponseWriter, _ *http.Request) {
		called = true
		jsonReply(w, http.StatusOK, WipeResponse{Started: true})
	})

	out, err := New(fb.URL, "", nil).WipeDevice(context.Background())
	if err != nil {
		t.Fatalf("WipeDevice: %v", err)
	}
	if !called {
		t.Error("the daemon was never asked to erase this device")
	}
	if !out.Started {
		t.Error("the answer says the erasure did not start")
	}
}

func TestWipeDevice_ARefusalIsAnError(t *testing.T) {
	fb := newFakeBackend(t)
	fb.on(http.MethodPost, "/wipe", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "this daemon was not started with a data root", http.StatusPreconditionFailed)
	})

	out, err := New(fb.URL, "", nil).WipeDevice(context.Background())
	if err == nil {
		t.Fatalf("a refused erasure came back as success: %+v", out)
	}
	if out.Started {
		t.Error("a refused erasure reported that it started")
	}
}
