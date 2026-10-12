package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func sha256Hex(t *testing.T, proofHex string) string {
	t.Helper()
	proof, err := hex.DecodeString(proofHex)
	if err != nil {
		t.Fatalf("decode proof: %v", err)
	}
	sum := sha256.Sum256(proof)
	return hex.EncodeToString(sum[:])
}

func destroyPINState(t *testing.T, d *daemon) destroyPINStateResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	d.apiHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/destroy-pin", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /destroy-pin: status %d, body %s", rec.Code, rec.Body.String())
	}
	var out destroyPINStateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	return out
}

func registerTestSibling(t *testing.T, d *daemon) string {
	t.Helper()
	peer := freshTestPeer(t, "sibling-slot-0")
	if err := d.registry.Add(peer); err != nil {
		t.Fatalf("registry.Add: %v", err)
	}
	return peer.ID
}

func destroyPINSet(t *testing.T, d *daemon, body string) destroyPINSetResponse {
	t.Helper()
	body = `{"sibling_peer_id":"` + registerTestSibling(t, d) + `",` + strings.TrimPrefix(body, "{")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/destroy-pin", strings.NewReader(body))
	d.apiHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /destroy-pin: status %d, body %s", rec.Code, rec.Body.String())
	}
	var out destroyPINSetResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode set response: %v", err)
	}
	return out
}

func TestDestroyPINAPI_FreshDeviceHasNoGuard(t *testing.T) {
	d := newTestDaemon(t)
	got := destroyPINState(t, d)
	if got.Set {
		t.Fatal("a fresh device reports a destroy PIN is set")
	}
	if got.Salt != "" {
		t.Fatalf("a fresh device handed out a salt: %q", got.Salt)
	}
}

func TestDestroyPINAPI_FirstSetIsAccepted(t *testing.T) {
	d := newTestDaemon(t)
	resp := destroyPINSet(t, d, `{"salt":"a1b2","verifier":"c3d4"}`)
	if !resp.Accepted {
		t.Fatalf("first set refused: %q", resp.Reason)
	}
	state := destroyPINState(t, d)
	if !state.Set {
		t.Fatal("after a set the device still reports no destroy PIN")
	}
	if state.Salt != "a1b2" {
		t.Fatalf("salt = %q, want a1b2", state.Salt)
	}
	if state.SetAt == 0 {
		t.Fatal("set_at was not stamped")
	}
}

func TestDestroyPINAPI_SetOnceAndNeverAgain(t *testing.T) {
	d := newTestDaemon(t)

	const proof = "c3d4"
	verifier := sha256Hex(t, proof)
	if resp := destroyPINSet(t, d, `{"salt":"a1b2","verifier":"`+verifier+`"}`); !resp.Accepted {
		t.Fatalf("first set refused: %q", resp.Reason)
	}

	resp := destroyPINSet(t, d, `{"salt":"eeff","verifier":"9999"}`)
	if resp.Accepted {
		t.Fatal("a second set was accepted")
	}
	if resp.Reason != destroyPINRefusalAlreadySet {
		t.Fatalf("reason = %q, want %q", resp.Reason, destroyPINRefusalAlreadySet)
	}

	resp = destroyPINSet(t, d, `{"salt":"eeff","verifier":"`+sha256Hex(t, proof)+`"}`)
	if resp.Accepted {
		t.Fatal("a second set was accepted from a caller that knows the PIN")
	}

	if state := destroyPINState(t, d); state.Salt != "a1b2" {
		t.Fatalf("a refused set moved the guard: salt = %q", state.Salt)
	}
}

func TestDestroyPINAPI_ClearAllowsAFreshGuard(t *testing.T) {
	d := newTestDaemon(t)
	if resp := destroyPINSet(t, d, `{"salt":"a1b2","verifier":"c3d4"}`); !resp.Accepted {
		t.Fatalf("first set refused: %q", resp.Reason)
	}
	if err := d.store.ClearDestroyPIN(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if state := destroyPINState(t, d); state.Set {
		t.Fatal("the guard survived a clear")
	}
	if resp := destroyPINSet(t, d, `{"salt":"eeff","verifier":"9999"}`); !resp.Accepted {
		t.Fatalf("a set after a clear was refused: %q", resp.Reason)
	}
	if state := destroyPINState(t, d); state.Salt != "eeff" {
		t.Fatalf("salt after the fresh set = %q, want eeff", state.Salt)
	}
}

func TestDestroyPINAPI_NeverHandsOutTheVerifier(t *testing.T) {
	d := newTestDaemon(t)
	verifier := sha256Hex(t, "c3d4")
	if resp := destroyPINSet(t, d, `{"salt":"a1b2","verifier":"`+verifier+`"}`); !resp.Accepted {
		t.Fatalf("set refused: %q", resp.Reason)
	}
	rec := httptest.NewRecorder()
	d.apiHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/destroy-pin", nil))
	if strings.Contains(rec.Body.String(), verifier) {
		t.Fatalf("the state answer carries the verifier: %s", rec.Body.String())
	}
}

func TestDestroyPINAPI_RefusesUnusableMaterial(t *testing.T) {
	d := newTestDaemon(t)
	for _, body := range []string{
		`{"salt":"","verifier":"c3d4"}`,
		`{"salt":"a1b2","verifier":""}`,
		`{"salt":"nothex","verifier":"c3d4"}`,
		`{"salt":"a1b2","verifier":"nothex"}`,
	} {
		rec := httptest.NewRecorder()
		d.apiHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/destroy-pin", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status %d, want 400", body, rec.Code)
		}
	}
	if got := destroyPINState(t, d); got.Set {
		t.Fatal("an unusable request set a guard")
	}
}

func TestDestroyPINAPI_StateNamesTheGuardsSibling(t *testing.T) {
	d := newTestDaemon(t)
	if got := destroyPINState(t, d).SiblingPeerID; got != "" {
		t.Fatalf("no guard, and the state names %q", got)
	}
	sib := registerTestSibling(t, d)
	body := `{"sibling_peer_id":"` + sib + `","salt":"a1b2","verifier":"` + sha256Hex(t, "c3d4") + `"}`
	rec := httptest.NewRecorder()
	d.apiHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/destroy-pin", strings.NewReader(body)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"accepted":true`) {
		t.Fatalf("set: status %d, body %s", rec.Code, rec.Body.String())
	}
	if got := destroyPINState(t, d).SiblingPeerID; got != sib {
		t.Fatalf("state names %q, want the guard's sibling %q", got, sib)
	}
}
