package pair

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"haoma-frontend/internal/backendapi"
)

func TestBeforeRegisterRunsBeforeThePeerIsPosted(t *testing.T) {
	for _, name := range []string{"Import", "ImportResponder"} {
		t.Run(name, func(t *testing.T) {
			stores := newLocalStores(t)
			var cap captured
			backend := backendapi.New(stubBackend(t, http.StatusCreated, &cap), "", nil)
			inv, _ := buildFullInvite(t)
			mine := freshLocalKeys(t)

			calls := 0
			SetBeforeRegister(func() error {
				calls++
				if cap.called != 0 {
					t.Errorf("the hook ran after the peer was posted")
				}
				return nil
			})
			t.Cleanup(func() { SetBeforeRegister(nil) })

			var err error
			if name == "Import" {
				err = Import(context.Background(), stores, backend, inv, mine, fakeMintedOnion())
			} else {
				err = ImportResponder(context.Background(), stores, backend, inv, mine, fakeMintedOnion())
			}
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if calls != 1 || cap.called != 1 {
				t.Errorf("hook ran %d times, peer posted %d times; want 1 and 1", calls, cap.called)
			}
		})
	}
}

func TestBeforeRegisterErrorRefusesTheImport(t *testing.T) {
	stores := newLocalStores(t)
	var cap captured
	backend := backendapi.New(stubBackend(t, http.StatusCreated, &cap), "", nil)
	inv, _ := buildFullInvite(t)
	SetBeforeRegister(func() error { return errors.New("seeded") })
	t.Cleanup(func() { SetBeforeRegister(nil) })
	if err := Import(context.Background(), stores, backend, inv, freshLocalKeys(t), fakeMintedOnion()); err == nil {
		t.Error("Import went on after the hook failed")
	}
	if cap.called != 0 {
		t.Errorf("the peer was posted %d time(s) after the hook failed", cap.called)
	}
}
