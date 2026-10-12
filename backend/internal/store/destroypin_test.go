package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

func TestDestroyPIN_UnsetIsTheZeroValue(t *testing.T) {
	dir := t.TempDir()
	s, err := Unlock(dir, "pw")
	if err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	defer s.Lock()

	rec, err := s.GetDestroyPIN()
	if err != nil {
		t.Fatalf("GetDestroyPIN on a fresh store: %v", err)
	}
	if rec.Set {
		t.Fatal("a fresh store reports a destroy PIN is set")
	}
	if len(rec.Salt) != 0 || len(rec.Verifier) != 0 {
		t.Fatalf("a fresh store carries material: salt=%d verifier=%d", len(rec.Salt), len(rec.Verifier))
	}
}

func TestDestroyPIN_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := Unlock(dir, "pw")
	if err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	defer s.Lock()

	want := DestroyPIN{
		Salt:          []byte("salt-bytes"),
		Verifier:      []byte("verifier-bytes"),
		SiblingPeerID: "sib",
		SetAt:         1700000000,
	}
	if err := s.PutDestroyPIN(want); err != nil {
		t.Fatalf("PutDestroyPIN: %v", err)
	}
	got, err := s.GetDestroyPIN()
	if err != nil {
		t.Fatalf("GetDestroyPIN: %v", err)
	}
	if !got.Set {
		t.Fatal("stored record does not report Set")
	}
	if !bytes.Equal(got.Salt, want.Salt) || !bytes.Equal(got.Verifier, want.Verifier) {
		t.Fatalf("material did not round trip: salt=%q verifier=%q", got.Salt, got.Verifier)
	}
	if got.SetAt != want.SetAt {
		t.Fatalf("set_at = %d, want %d", got.SetAt, want.SetAt)
	}
}

func TestDestroyPIN_RefusesToReplaceAGuard(t *testing.T) {
	dir := t.TempDir()
	s, err := Unlock(dir, "pw")
	if err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	defer s.Lock()

	if err := s.PutDestroyPIN(DestroyPIN{Salt: []byte("s1"), Verifier: []byte("v1"), SiblingPeerID: "sib", SetAt: 1}); err != nil {
		t.Fatalf("first PutDestroyPIN: %v", err)
	}
	err = s.PutDestroyPIN(DestroyPIN{Salt: []byte("s2"), Verifier: []byte("v2"), SiblingPeerID: "sib", SetAt: 2})
	if !errors.Is(err, ErrDestroyPINAlreadySet) {
		t.Fatalf("second PutDestroyPIN error = %v, want ErrDestroyPINAlreadySet", err)
	}
	got, err := s.GetDestroyPIN()
	if err != nil {
		t.Fatalf("GetDestroyPIN: %v", err)
	}
	if !bytes.Equal(got.Salt, []byte("s1")) || !bytes.Equal(got.Verifier, []byte("v1")) {
		t.Fatalf("the refused write moved the guard: salt=%q verifier=%q", got.Salt, got.Verifier)
	}
}

func TestDestroyPIN_ClearTakesTheAttemptsWithIt(t *testing.T) {
	dir := t.TempDir()
	s, err := Unlock(dir, "pw")
	if err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	defer s.Lock()

	if err := s.PutDestroyPIN(DestroyPIN{Salt: []byte("s1"), Verifier: []byte("v1"), SiblingPeerID: "sib", SetAt: 1}); err != nil {
		t.Fatalf("PutDestroyPIN: %v", err)
	}
	if err := s.PutDestroyAttempts(DestroyAttempts{Failures: 2, BlockedUntil: 999}); err != nil {
		t.Fatalf("PutDestroyAttempts: %v", err)
	}
	if err := s.ClearDestroyPIN(); err != nil {
		t.Fatalf("ClearDestroyPIN: %v", err)
	}
	if got, err := s.GetDestroyPIN(); err != nil || got.Set {
		t.Fatalf("guard after clear: %+v err=%v", got, err)
	}
	att, err := s.GetDestroyAttempts()
	if err != nil {
		t.Fatalf("GetDestroyAttempts: %v", err)
	}
	if att.Failures != 0 || att.BlockedUntil != 0 {
		t.Fatalf("attempts survived the clear: %+v", att)
	}

	if err := s.PutDestroyPIN(DestroyPIN{Salt: []byte("s2"), Verifier: []byte("v2"), SiblingPeerID: "sib", SetAt: 2}); err != nil {
		t.Fatalf("PutDestroyPIN after clear: %v", err)
	}
}

func TestDestroyPIN_ClearOnAFreshDevice(t *testing.T) {
	dir := t.TempDir()
	s, err := Unlock(dir, "pw")
	if err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	defer s.Lock()

	if err := s.ClearDestroyPIN(); err != nil {
		t.Fatalf("ClearDestroyPIN on a fresh device: %v", err)
	}
}

func TestDestroyPIN_RefusesEmptyMaterial(t *testing.T) {
	dir := t.TempDir()
	s, err := Unlock(dir, "pw")
	if err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	defer s.Lock()

	if err := s.PutDestroyPIN(DestroyPIN{Verifier: []byte("v"), SiblingPeerID: "sib"}); err == nil {
		t.Fatal("a record with no salt was accepted")
	}
	if err := s.PutDestroyPIN(DestroyPIN{Salt: []byte("s"), SiblingPeerID: "sib"}); err == nil {
		t.Fatal("a record with no verifier was accepted")
	}
}

func TestDestroyPIN_RefusesAnUnknownVersion(t *testing.T) {
	dir := t.TempDir()
	s, err := Unlock(dir, "pw")
	if err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	defer s.Lock()

	raw, err := json.Marshal(DestroyPIN{Version: destroyPINVersion + 1, Set: true, Salt: []byte("s"), Verifier: []byte("v")})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := s.PutState(destroyPINStateKey, raw); err != nil {
		t.Fatalf("PutState: %v", err)
	}
	if _, err := s.GetDestroyPIN(); !errors.Is(err, ErrDestroyPINVersion) {
		t.Fatalf("err = %v, want ErrDestroyPINVersion", err)
	}
}

func TestUpdateDestroyAttempts_ConcurrentCallsEachCount(t *testing.T) {
	dir := t.TempDir()
	s, err := Unlock(dir, "pw")
	if err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	defer s.Lock()

	const callers = 32
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.UpdateDestroyAttempts(func(_ DestroyPIN, att DestroyAttempts) (DestroyAttempts, bool, error) {
				att.Failures++
				return att, true, nil
			})
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("UpdateDestroyAttempts: %v", err)
	}

	att, err := s.GetDestroyAttempts()
	if err != nil {
		t.Fatalf("GetDestroyAttempts: %v", err)
	}
	if att.Failures != callers {
		t.Fatalf("failures = %d after %d calls, want %d — attempts were lost",
			att.Failures, callers, callers)
	}
}
