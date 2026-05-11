package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPI_OverlayPeerAddress(t *testing.T) {
	d := newTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	inv := newTestBackendInvite(t, []string{"alice-onion"})
	raw, _ := json.Marshal(inv)
	r1, err := http.Post(srv.URL+"/peers", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	r1.Body.Close()

	body := strings.NewReader(`{"address":"alice-onion-NEW"}`)
	resp, err := http.Post(srv.URL+"/peers/"+inv.PeerID+"/overlay-address", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	got, err := d.registry.Get(inv.PeerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.KnownAddresses) != 2 || got.KnownAddresses[0] != "alice-onion-NEW" || got.KnownAddresses[1] != "alice-onion" {
		t.Errorf("KnownAddresses = %v, want [alice-onion-NEW alice-onion]", got.KnownAddresses)
	}
}

func TestAPI_OverlayPeerAddress_PeerNotFound(t *testing.T) {
	d := newTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	body := strings.NewReader(`{"address":"new-onion"}`)
	resp, err := http.Post(srv.URL+"/peers/deadbeef/overlay-address", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestAPI_CollapsePeerAddress(t *testing.T) {
	d := newTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	inv := newTestBackendInvite(t, []string{"alice-onion"})
	raw, _ := json.Marshal(inv)
	r1, _ := http.Post(srv.URL+"/peers", "application/json", bytes.NewReader(raw))
	r1.Body.Close()

	body := strings.NewReader(`{"address":"alice-onion-NEW"}`)
	r2, _ := http.Post(srv.URL+"/peers/"+inv.PeerID+"/overlay-address", "application/json", body)
	r2.Body.Close()

	collapseBody := strings.NewReader(`{"retain":"alice-onion-NEW"}`)
	resp, err := http.Post(srv.URL+"/peers/"+inv.PeerID+"/collapse-address", "application/json", collapseBody)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	got, _ := d.registry.Get(inv.PeerID)
	if len(got.KnownAddresses) != 1 || got.KnownAddresses[0] != "alice-onion-NEW" {
		t.Errorf("KnownAddresses = %v, want [alice-onion-NEW]", got.KnownAddresses)
	}
}

func TestAPI_CollapsePeerAddress_RetainNotInList(t *testing.T) {
	d := newTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	inv := newTestBackendInvite(t, []string{"alice-onion"})
	raw, _ := json.Marshal(inv)
	r1, _ := http.Post(srv.URL+"/peers", "application/json", bytes.NewReader(raw))
	r1.Body.Close()

	body := strings.NewReader(`{"retain":"some-other-onion"}`)
	resp, err := http.Post(srv.URL+"/peers/"+inv.PeerID+"/collapse-address", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestAPI_RotateOwnOnion(t *testing.T) {
	d := newTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	inv := newTestBackendInvite(t, []string{"alice-onion"})
	raw, _ := json.Marshal(inv)
	r1, _ := http.Post(srv.URL+"/peers", "application/json", bytes.NewReader(raw))
	r1.Body.Close()

	body := strings.NewReader(`{"address":"NEW-myonion-addr-56chars","private_key":"NEW-priv-base64"}`)
	resp, err := http.Post(srv.URL+"/peers/"+inv.PeerID+"/rotate-own-onion", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}

	if got["old_address"] != "" {
		t.Errorf("old_address = %q, want empty (steady-state with grace)", got["old_address"])
	}

	regGot, _ := d.registry.Get(inv.PeerID)
	if regGot.MyOnionAddr != "NEW-myonion-addr-56chars" {
		t.Errorf("MyOnionAddr = %q, want NEW-myonion-addr-56chars", regGot.MyOnionAddr)
	}
	if regGot.MyOnionPrivateKey != "NEW-priv-base64" {
		t.Errorf("MyOnionPrivateKey = %q, want NEW-priv-base64", regGot.MyOnionPrivateKey)
	}

	if regGot.PrevMyOnion == nil {
		t.Fatalf("PrevMyOnion is nil, want grace-slot snapshot of the prior onion")
	}
	if regGot.PrevMyOnion.Address != inv.MyOnionAddr {
		t.Errorf("PrevMyOnion.Address = %q, want %q", regGot.PrevMyOnion.Address, inv.MyOnionAddr)
	}
	if regGot.PrevMyOnion.PrivateKey != inv.MyOnionPrivateKey {
		t.Errorf("PrevMyOnion.PrivateKey = %q, want %q", regGot.PrevMyOnion.PrivateKey, inv.MyOnionPrivateKey)
	}
	if regGot.PrevMyOnion.ExpiresAt == 0 {
		t.Errorf("PrevMyOnion.ExpiresAt = 0, want non-zero grace expiry")
	}
}

func TestAPI_RotateOwnOnion_DoubleRotation_EvictsPrev(t *testing.T) {
	d := newTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	inv := newTestBackendInvite(t, []string{"alice-onion"})
	raw, _ := json.Marshal(inv)
	r1, _ := http.Post(srv.URL+"/peers", "application/json", bytes.NewReader(raw))
	r1.Body.Close()
	originalMy := inv.MyOnionAddr

	body1 := strings.NewReader(`{"address":"NEW1","private_key":"PRIV1"}`)
	r2, err := http.Post(srv.URL+"/peers/"+inv.PeerID+"/rotate-own-onion", "application/json", body1)
	if err != nil {
		t.Fatalf("first rotation POST: %v", err)
	}
	defer r2.Body.Close()
	var got1 map[string]string
	json.NewDecoder(r2.Body).Decode(&got1)
	if got1["old_address"] != "" {
		t.Errorf("first rotation old_address = %q, want empty", got1["old_address"])
	}

	body2 := strings.NewReader(`{"address":"NEW2","private_key":"PRIV2"}`)
	r3, err := http.Post(srv.URL+"/peers/"+inv.PeerID+"/rotate-own-onion", "application/json", body2)
	if err != nil {
		t.Fatalf("second rotation POST: %v", err)
	}
	defer r3.Body.Close()
	var got2 map[string]string
	json.NewDecoder(r3.Body).Decode(&got2)
	if got2["old_address"] != originalMy {
		t.Errorf("second rotation old_address = %q, want %q (evicted prev)", got2["old_address"], originalMy)
	}

	regGot, _ := d.registry.Get(inv.PeerID)
	if regGot.MyOnionAddr != "NEW2" {
		t.Errorf("MyOnionAddr = %q, want NEW2", regGot.MyOnionAddr)
	}
	if regGot.PrevMyOnion == nil || regGot.PrevMyOnion.Address != "NEW1" {
		t.Errorf("PrevMyOnion = %v, want {Address: NEW1, ...}", regGot.PrevMyOnion)
	}
}

func TestAPI_RotateOwnOnion_BadBody(t *testing.T) {
	d := newTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	inv := newTestBackendInvite(t, []string{"alice-onion"})
	raw, _ := json.Marshal(inv)
	r1, _ := http.Post(srv.URL+"/peers", "application/json", bytes.NewReader(raw))
	r1.Body.Close()

	body := strings.NewReader(`{"address":"NEW"}`)
	resp, err := http.Post(srv.URL+"/peers/"+inv.PeerID+"/rotate-own-onion", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestAPI_OnionDel_NoControlConn(t *testing.T) {

	d := newTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	body := strings.NewReader(`{"address":"some-addr"}`)
	resp, err := http.Post(srv.URL+"/onion/del", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
}
