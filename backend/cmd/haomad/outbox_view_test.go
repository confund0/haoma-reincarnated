package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPI_Outbox_AnswersNoOnion(t *testing.T) {
	d, _ := daemonWithIdentity(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	inv := newTestBackendInvite(t, []string{"alice-onion-56chars-yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy"})
	if _, err := d.registry.Import(inv); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(sendRequest{PeerID: inv.PeerID, Payload: []byte("ciphertext-bytes")})
	resp, err := http.Post(srv.URL+"/send", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var sent sendResponse
	_ = json.NewDecoder(resp.Body).Decode(&sent)
	resp.Body.Close()
	if sent.EnvelopeID == "" {
		t.Fatal("no envelope id")
	}

	stored, err := d.worker.Load(sent.EnvelopeID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored.Dest, ".onion") || stored.Envelope.From != inv.MyOnionAddr {
		t.Fatalf("fixture: stored dest %q from %q, want both onions", stored.Dest, stored.Envelope.From)
	}

	for _, path := range []string{"/outbox/" + sent.EnvelopeID, "/outbox?state=enqueued"} {
		resp := get(t, srv, path)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d", path, resp.StatusCode)
		}
		s := string(body)
		for _, banned := range []string{".onion", inv.MyOnionAddr, "alice-onion", `"dest"`, `"from"`, `"envelope"`} {
			if strings.Contains(s, banned) {
				t.Errorf("%s answers %q: %s", path, banned, s)
			}
		}
		for _, want := range []string{`"envelope_id":"` + sent.EnvelopeID + `"`, `"state":"enqueued"`} {
			if !strings.Contains(s, want) {
				t.Errorf("%s no longer answers %s: %s", path, want, s)
			}
		}
	}
}
