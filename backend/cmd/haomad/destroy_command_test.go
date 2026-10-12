package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"haoma/internal/peers"
	"haoma/internal/xport"
)

func destroyEnvelope(t *testing.T, proofHex string) xport.Envelope {
	t.Helper()
	body, err := json.Marshal(destroyVerifyBody{Proof: proofHex})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return xport.Envelope{ID: "destroy-1", Kind: xport.KindDestroy, Payload: body}
}

func stampedCtx(peerID string) context.Context {
	ctx := xport.ContextWithPeerIDStamp(context.Background())
	xport.StampPeerID(ctx, peerID)
	return ctx
}

func destroyDaemon(t *testing.T, proofHex string) (*daemon, peers.Peer, string) {
	t.Helper()
	d := newTestDaemon(t)

	peer := freshTestPeer(t, "sibling-slot-0")

	peer.MyOnionAddr = "sibling0aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := d.registry.Add(peer); err != nil {
		t.Fatalf("registry.Add: %v", err)
	}
	setGuardFor(t, d, proofHex, peer.ID)

	root := newDataRoot(t)
	d.cfg.dataRoot = root
	return d, peer, root
}

func TestDestroy_RightProofErasesAndAnswers(t *testing.T) {
	withFastWipeGrace(t)
	exited := captureExit(t)

	d, peer, root := destroyDaemon(t, "c3d4")
	withFastEraseShutdownWait(t, 30*time.Second)
	asked, finish := withRunLoop(t, d)

	ctx := stampedCtx(peer.ID)
	resp, err := respondDestroy(d, ctx, destroyEnvelope(t, "c3d4"))
	if err != nil {
		t.Fatalf("respondDestroy: %v", err)
	}
	if resp.Kind != xport.KindDestroyReceipt {
		t.Fatalf("answer kind = %q, want %q", resp.Kind, xport.KindDestroyReceipt)
	}
	var body destroyReceiptBody
	if err := json.Unmarshal(resp.Payload, &body); err != nil {
		t.Fatalf("decode receipt: %v", err)
	}
	if body.DestroyID != "destroy-1" {
		t.Fatalf("receipt names %q, want destroy-1", body.DestroyID)
	}

	if _, err := os.Stat(root); err != nil {
		t.Fatalf("the root went before the answer was built: %v", err)
	}

	select {
	case <-asked:
	case <-time.After(5 * time.Second):
		t.Fatal("the command erased without stopping the daemon")
	}
	finish()

	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the device never erased itself")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("the root still holds %d entries", len(entries))
	}
}

func TestDestroy_WrongProofAnswersNothingAndErasesNothing(t *testing.T) {
	withFastWipeGrace(t)
	exited := captureExit(t)

	d, peer, root := destroyDaemon(t, "c3d4")

	ctx := stampedCtx(peer.ID)
	resp, err := respondDestroy(d, ctx, destroyEnvelope(t, "dead"))
	if err != nil {
		t.Fatalf("respondDestroy: %v", err)
	}
	if resp.ID != "" {
		t.Fatalf("a wrong proof was answered with an envelope: %+v", resp)
	}

	select {
	case <-exited:
		t.Fatal("a wrong proof erased the device")
	case <-time.After(200 * time.Millisecond):
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("the root was touched: %v", err)
	}
}

func TestDestroy_SpendsTheSameAttemptsAsVerify(t *testing.T) {
	withFastWipeGrace(t)
	captureExit(t)

	d, peer, _ := destroyDaemon(t, "c3d4")
	ctx := stampedCtx(peer.ID)

	for i := 0; i < 2; i++ {
		if _, err := checkDestroyProofEnvelope(d, verifyEnvelope(t, "dead"), peer.ID); err != nil {
			t.Fatalf("verify %d: %v", i, err)
		}
	}
	if _, err := respondDestroy(d, ctx, destroyEnvelope(t, "dead")); err != nil {
		t.Fatalf("respondDestroy: %v", err)
	}

	att, err := d.store.GetDestroyAttempts()
	if err != nil {
		t.Fatalf("GetDestroyAttempts: %v", err)
	}
	if att.BlockedUntil == 0 {
		t.Fatalf("two wrong verifies and a wrong destroy did not reach the limit: %+v", att)
	}
}

func TestDestroy_OnlyTheGuardsOwnPeer(t *testing.T) {
	withFastWipeGrace(t)
	exited := captureExit(t)

	d, _, root := destroyDaemon(t, "c3d4")

	stranger := freshTestPeer(t, "stranger-slot-0")
	stranger.MyOnionAddr = "stranger0aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := d.registry.Add(stranger); err != nil {
		t.Fatalf("registry.Add: %v", err)
	}
	ctx := stampedCtx(stranger.ID)

	resp, err := respondDestroy(d, ctx, destroyEnvelope(t, "c3d4"))
	if err != nil {
		t.Fatalf("respondDestroy: %v", err)
	}
	if resp.ID != "" {
		t.Fatalf("a stranger got an answer: %+v", resp)
	}
	select {
	case <-exited:
		t.Fatal("a stranger erased the device")
	case <-time.After(200 * time.Millisecond):
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("the root was touched: %v", err)
	}
	att, err := d.store.GetDestroyAttempts()
	if err != nil {
		t.Fatalf("GetDestroyAttempts: %v", err)
	}
	if att.Failures != 0 || att.BlockedUntil != 0 {
		t.Fatalf("a stranger moved the count: %+v", att)
	}
}

func destroyServer(d *daemon, senderPeerID string) http.Handler {
	verifier := xport.VerifierFunc(func(ctx context.Context, _ xport.Envelope) error {
		xport.StampPeerID(ctx, senderPeerID)
		return nil
	})
	responder := xport.StatusResponderFunc(func(ctx context.Context, req xport.Envelope) (xport.Envelope, error) {
		switch req.EffectiveKind() {
		case xport.KindDestroy:
			return respondDestroy(d, ctx, req)
		case xport.KindDestroyVerify:
			return respondDestroyVerify(d, ctx, req)
		}
		return xport.Envelope{}, nil
	})
	receiver := xport.ReceiverFunc(func(context.Context, xport.Envelope) error { return nil })
	return xport.NewServer(-1, receiver, verifier, responder)
}

func answerBytes(t *testing.T, h http.Handler, env xport.Envelope) (int, []byte) {
	t.Helper()
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/message", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func TestDestroy_ARightProofThisDaemonCannotActOnAnswersLikeAWrongOne(t *testing.T) {
	withFastWipeGrace(t)
	captureExit(t)

	wrongStatus, wrongBody := func() (int, []byte) {
		d, peer, _ := destroyDaemon(t, "c3d4")
		return answerBytes(t, destroyServer(d, peer.ID), destroyEnvelope(t, "dead"))
	}()

	cases := []struct {
		name   string
		break_ func(d *daemon, peer *peers.Peer)
	}{
		{"no data root", func(d *daemon, _ *peers.Peer) { d.cfg.dataRoot = "" }},
		{"no onion published for that peer", func(d *daemon, peer *peers.Peer) {
			peer.MyOnionAddr = ""
			if err := d.registry.Add(*peer); err != nil {
				t.Fatalf("registry.Add: %v", err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, peer, root := destroyDaemon(t, "c3d4")
			c.break_(d, &peer)

			status, body := answerBytes(t, destroyServer(d, peer.ID), destroyEnvelope(t, "c3d4"))
			if status != wrongStatus {
				t.Fatalf("status = %d, a wrong proof gets %d", status, wrongStatus)
			}
			if !bytes.Equal(body, wrongBody) {
				t.Fatalf("body = %q, a wrong proof gets %q", body, wrongBody)
			}

			if _, err := os.Stat(root); err != nil {
				t.Fatalf("the root was touched: %v", err)
			}
		})
	}
}

func TestDestroyVerify_AStrangerGetsTheSameBytesAsSilence(t *testing.T) {
	d, sibling, _ := destroyDaemon(t, "c3d4")

	stranger := freshTestPeer(t, "stranger0aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	stranger.MyOnionAddr = "stranger0aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := d.registry.Add(stranger); err != nil {
		t.Fatalf("registry.Add: %v", err)
	}

	quiet := newTestDaemon(t)
	quietPeer := freshTestPeer(t, "quiet000aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	quietPeer.MyOnionAddr = "quiet000aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := quiet.registry.Add(quietPeer); err != nil {
		t.Fatalf("registry.Add: %v", err)
	}
	silentStatus, silentBody := answerBytes(t, destroyServer(quiet, quietPeer.ID), verifyEnvelope(t, "c3d4"))

	status, body := answerBytes(t, destroyServer(d, stranger.ID), verifyEnvelope(t, "c3d4"))
	if status != silentStatus {
		t.Fatalf("status = %d, a device with nothing to say gives %d", status, silentStatus)
	}
	if !bytes.Equal(body, silentBody) {
		t.Fatalf("body = %q, a device with nothing to say gives %q", body, silentBody)
	}
	if len(body) != 0 {
		t.Fatalf("a stranger was answered with %q", body)
	}

	_, ownBody := answerBytes(t, destroyServer(d, sibling.ID), verifyEnvelope(t, "c3d4"))
	if len(ownBody) == 0 {
		t.Fatal("the guard did not answer the peer it belongs to")
	}
}
