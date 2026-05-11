package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"go.mau.fi/libsignal/protocol"

	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/pair"
)

func TestDispatch_GetPeerFingerprint_HappyPath(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000000a"
	preEstablishSession(t, d, peerID)

	want := ""
	{
		key, err := d.stores.GetRemoteIdentity(protocol.NewSignalAddress(peerID, pair.DeviceID))
		if err != nil {
			t.Fatalf("GetRemoteIdentity: %v", err)
		}
		want = key.Fingerprint()
		if len(want) != 66 {
			t.Fatalf("Fingerprint() len = %d, want 66 (33 bytes hex)", len(want))
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameGetPeerFingerprint, "fp-1", ipc.GetPeerFingerprintRequest{
		PeerID: peerID,
	}))
	resp := readUntil(t, ctx, conn, ipc.FramePeerFingerprint)
	if resp.ID != "fp-1" {
		t.Errorf("corr-id = %q, want fp-1", resp.ID)
	}
	var p ipc.PeerFingerprintPayload
	if err := json.Unmarshal(resp.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.PeerID != peerID {
		t.Errorf("peer_id = %q, want %q", p.PeerID, peerID)
	}
	if p.Fingerprint != want {
		t.Errorf("fingerprint = %q, want %q", p.Fingerprint, want)
	}
}

func TestDispatch_GetPeerFingerprint_NoSessionYet(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	_, addr, certPath, token := newTestDaemon(t, stub)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameGetPeerFingerprint, "fp-2", ipc.GetPeerFingerprintRequest{
		PeerID: "00000000000000000000000000000fff",
	}))
	resp := readUntil(t, ctx, conn, ipc.FramePeerFingerprint)
	if resp.ID != "fp-2" {
		t.Errorf("corr-id = %q, want fp-2", resp.ID)
	}
	var p ipc.PeerFingerprintPayload
	if err := json.Unmarshal(resp.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Fingerprint != "" {
		t.Errorf("fingerprint = %q, want empty (no session)", p.Fingerprint)
	}
}

func TestDispatch_GetPeerFingerprint_EmptyPeerIDRejected(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	_, addr, certPath, token := newTestDaemon(t, stub)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameGetPeerFingerprint, "fp-3", ipc.GetPeerFingerprintRequest{}))
	resp := readUntil(t, ctx, conn, ipc.FrameError)
	if resp.ID != "fp-3" {
		t.Errorf("corr-id = %q, want fp-3", resp.ID)
	}
	var e ipc.ErrorPayload
	if err := json.Unmarshal(resp.Payload, &e); err != nil {
		t.Fatal(err)
	}
	if e.Code != "bad_request" {
		t.Errorf("code = %q, want bad_request", e.Code)
	}
}
