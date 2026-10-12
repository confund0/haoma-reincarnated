package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/destroypin"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/linkeddevice"
	"haoma-frontend/internal/syncrail"
)

func init() {

	destroypin.DefaultParams = destroypin.Params{
		Time: 1, Memory: 8 * 1024, Threads: 4, KeyLen: 32, SaltLen: 16,
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

func storedVerifierFor(t *testing.T, pin, saltHex string) string {
	t.Helper()
	salt, err := hex.DecodeString(saltHex)
	if err != nil {
		t.Fatalf("decode salt: %v", err)
	}
	proof, err := destroypin.Derive(pin, salt)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	return hex.EncodeToString(destroypin.Verifier(proof))
}

func linkASibling(t *testing.T, d *daemon) {
	t.Helper()
	if d.linkedDevices == nil {
		d.linkedDevices = linkeddevice.New(d.store)
	}
	if _, err := d.linkedDevices.Link(railSiblingID, 1); err != nil {
		t.Fatalf("link a sibling: %v", err)
	}
}

func TestDestroyPIN_FirstSetStoresASaltAndAVerifier(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkASibling(t, d)

	out, err := setLocalDestroyPIN(context.Background(), d, ipc.SetDestroyPINRequest{New: "1234"})
	if err != nil {
		t.Fatalf("setLocalDestroyPIN: %v", err)
	}
	if !out.Accepted {
		t.Fatalf("the first set was refused: %q", out.Reason)
	}
	if !stub.DestroyPINGuardSet {
		t.Fatal("haomad holds no guard after a set")
	}
	if stub.DestroyPINSalt == "" || stub.DestroyPINVerifier == "" {
		t.Fatalf("guard is incomplete: salt=%q verifier=%q", stub.DestroyPINSalt, stub.DestroyPINVerifier)
	}
	if want := storedVerifierFor(t, "1234", stub.DestroyPINSalt); stub.DestroyPINVerifier != want {
		t.Fatalf("verifier = %q, want SHA-256 of the derivation %q", stub.DestroyPINVerifier, want)
	}
}

func TestDestroyPIN_TheStoredVerifierIsNotTheProof(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkASibling(t, d)

	if _, err := setLocalDestroyPIN(context.Background(), d, ipc.SetDestroyPINRequest{New: "1234"}); err != nil {
		t.Fatalf("setLocalDestroyPIN: %v", err)
	}
	salt, err := hex.DecodeString(stub.DestroyPINSalt)
	if err != nil {
		t.Fatalf("decode salt: %v", err)
	}
	proof, err := destroypin.Derive("1234", salt)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if stub.DestroyPINVerifier == hex.EncodeToString(proof) {
		t.Fatal("the stored verifier is the proof itself")
	}
	sum := sha256.Sum256(proof)
	if stub.DestroyPINVerifier != hex.EncodeToString(sum[:]) {
		t.Fatal("the stored verifier is not SHA-256 of the proof")
	}
}

func TestDestroyPIN_SetOnceAndNeverAgain(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkASibling(t, d)
	ctx := context.Background()

	if _, err := setLocalDestroyPIN(ctx, d, ipc.SetDestroyPINRequest{New: "1234"}); err != nil {
		t.Fatalf("first set: %v", err)
	}
	firstSalt := stub.DestroyPINSalt
	firstVerifier := stub.DestroyPINVerifier

	for _, pin := range []string{"4321", "1234"} {
		out, err := setLocalDestroyPIN(ctx, d, ipc.SetDestroyPINRequest{New: pin})
		if err != nil {
			t.Fatalf("second set: %v", err)
		}
		if out.Accepted {
			t.Fatal("a second set was accepted")
		}
		if out.Reason != ipc.DestroyPINRefusedAlreadySet {
			t.Fatalf("reason = %q, want %q", out.Reason, ipc.DestroyPINRefusedAlreadySet)
		}
	}
	if stub.DestroyPINSalt != firstSalt || stub.DestroyPINVerifier != firstVerifier {
		t.Fatal("a refused set moved the guard")
	}
}

func TestDestroyPIN_RefusesABadShapeWithoutTouchingTheGuard(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkASibling(t, d)

	for _, pin := range []string{"", "123", "12345", "123456", "abcd", "1234567"} {
		out, err := setLocalDestroyPIN(context.Background(), d, ipc.SetDestroyPINRequest{New: pin})
		if err != nil {
			t.Fatalf("set %q: %v", pin, err)
		}
		if out.Accepted {
			t.Fatalf("PIN of a bad shape was accepted")
		}
		if out.Reason != ipc.DestroyPINRefusedShape {
			t.Fatalf("reason = %q, want %q", out.Reason, ipc.DestroyPINRefusedShape)
		}
	}
	if stub.DestroyPINSetCalls != 0 {
		t.Fatalf("a PIN of a bad shape reached haomad %d time(s)", stub.DestroyPINSetCalls)
	}
}

func TestDestroyPIN_ThePINNeverReachesHaomad(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkASibling(t, d)

	if _, err := setLocalDestroyPIN(context.Background(), d, ipc.SetDestroyPINRequest{New: "2468"}); err != nil {
		t.Fatalf("setLocalDestroyPIN: %v", err)
	}
	if len(stub.DestroyPINSetBodies) == 0 {
		t.Fatal("haomad received no set request")
	}

	for _, body := range stub.DestroyPINSetBodies {
		var got map[string]any
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatalf("decode the request body: %v", err)
		}
		for k := range got {
			switch k {
			case "salt", "verifier", "sibling_peer_id":
			default:
				t.Fatalf("the request body carries %q; only a salt, a verifier and the sibling may cross", k)
			}
		}
	}
}

func TestDestroyPIN_SetPushesTheSaltToASibling(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkASibling(t, d)
	linkSibling(t, d, railSiblingID)

	out, err := setLocalDestroyPIN(context.Background(), d, ipc.SetDestroyPINRequest{New: "1234"})
	if err != nil {
		t.Fatalf("setLocalDestroyPIN: %v", err)
	}
	if out.SaltPushed != 1 || out.Siblings != 1 {
		t.Fatalf("pushed %d of %d siblings, want 1 of 1", out.SaltPushed, out.Siblings)
	}
	if len(stub.SentPeerIDs) != 1 {
		t.Fatalf("%d envelopes sent, want 1", len(stub.SentPeerIDs))
	}
	if stub.SentPeerIDs[0] != railSiblingID {
		t.Fatalf("envelope went to %q, want the sibling %q", stub.SentPeerIDs[0], railSiblingID)
	}

	key, err := d.railKey()
	if err != nil {
		t.Fatalf("railKey: %v", err)
	}
	env, err := syncrail.OpenEnvelope(key, stub.SentPayloads[0])
	if err != nil {
		t.Fatalf("the pushed payload did not open as a rail envelope: %v", err)
	}
	if env.Kind != syncrail.KindDestroySalt {
		t.Fatalf("rail kind = %q, want %q", env.Kind, syncrail.KindDestroySalt)
	}
	_, salt, err := syncrail.ParseDestroySalt(env)
	if err != nil {
		t.Fatalf("ParseDestroySalt: %v", err)
	}
	if hex.EncodeToString(salt) != stub.DestroyPINSalt {
		t.Fatalf("pushed salt %q, but haomad holds %q", hex.EncodeToString(salt), stub.DestroyPINSalt)
	}

	body, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	if strings.Contains(string(body), stub.DestroyPINVerifier) {
		t.Fatal("the rail envelope carries the verifier")
	}
	if strings.Contains(string(body), "1234") {
		t.Fatal("the rail envelope carries the PIN")
	}
}

func TestDestroyPIN_ADeviceWithNoSiblingsIsRefused(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)

	out, err := setLocalDestroyPIN(context.Background(), d, ipc.SetDestroyPINRequest{New: "1234"})
	if err != nil {
		t.Fatalf("setLocalDestroyPIN: %v", err)
	}
	if out.Accepted {
		t.Fatal("a device with no linked device set a destroy PIN")
	}
	if out.Reason != ipc.DestroyPINRefusedNoSibling {
		t.Fatalf("reason = %q, want %q", out.Reason, ipc.DestroyPINRefusedNoSibling)
	}
	if stub.DestroyPINGuardSet {
		t.Fatal("haomad holds a guard after a refused set")
	}
	if len(stub.SentPeerIDs) != 0 {
		t.Fatalf("%d envelopes sent on a device with no siblings, want none", len(stub.SentPeerIDs))
	}
}

func TestDestroyPIN_InboundSaltIsStoredAndThenReplaced(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	ctx := context.Background()

	first := []byte("first-salt-abcdef")
	applyDestroySalt(ctx, d, railSiblingID, syncrail.NewDestroySaltBody(first, 100), first)

	rec, held, err := d.linkedDevices.SiblingSalt(railSiblingID)
	if err != nil {
		t.Fatalf("SiblingSalt: %v", err)
	}
	if !held {
		t.Fatal("the sibling's salt was not stored")
	}
	if string(rec.Salt) != string(first) {
		t.Fatalf("salt = %q, want %q", rec.Salt, first)
	}
	if rec.SetAt != 100 {
		t.Fatalf("set_at = %d, want 100", rec.SetAt)
	}
	if rec.ReceivedAt == 0 {
		t.Fatal("received_at was not stamped")
	}

	second := []byte("second-salt-abcde")
	applyDestroySalt(ctx, d, railSiblingID, syncrail.NewDestroySaltBody(second, 200), second)
	rec, _, err = d.linkedDevices.SiblingSalt(railSiblingID)
	if err != nil {
		t.Fatalf("SiblingSalt: %v", err)
	}
	if string(rec.Salt) != string(second) {
		t.Fatalf("salt = %q, want the replacement %q", rec.Salt, second)
	}
}

func TestDestroyPIN_RailRoutesTheSaltEnvelope(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)

	salt := []byte("sealed-salt-abcd")
	sealed := sealRailEnvelope(t, d, syncrail.Envelope{
		V:    syncrail.Version,
		Kind: syncrail.KindDestroySalt,
		Ts:   time.Now().Unix(),
		Body: mustJSON(t, syncrail.NewDestroySaltBody(salt, 300)),
	})

	processSiblingRailEnvelope(context.Background(), d, backendapi.InboxEntry{
		ArrivalAt: time.Now().UnixNano(),
		PeerID:    railSiblingID,
		Envelope: backendapi.RawEnvelope{
			ID: "env-destroy-salt-1", Timestamp: time.Now().Unix(),
			From: "sibling-onion", Payload: sealed, Mac: []byte{1, 2, 3, 4},
		},
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rec, held, err := d.linkedDevices.SiblingSalt(railSiblingID)
		if err != nil {
			t.Fatalf("SiblingSalt: %v", err)
		}
		if held {
			if string(rec.Salt) != string(salt) {
				t.Fatalf("stored salt = %q, want %q", rec.Salt, salt)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the sealed salt envelope never reached the store")
}

func TestDestroyPIN_StateFrameAnswersSetAndSiblingSalts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	conn := dialTest(t, ctx, addr, certPath, token)

	req, _ := ipc.NewFrame(ipc.FrameGetDestroyPIN, "req-state-1", ipc.GetDestroyPINRequest{})
	writeFrame(t, ctx, conn, req)
	f := readUntil(t, ctx, conn, ipc.FrameDestroyPINListed)
	var listed ipc.DestroyPINListedResponse
	if err := json.Unmarshal(f.Payload, &listed); err != nil {
		t.Fatal(err)
	}
	if listed.Set {
		t.Fatal("a device with no destroy PIN reports one is set")
	}
	if len(listed.Siblings) != 1 {
		t.Fatalf("%d siblings listed, want 1", len(listed.Siblings))
	}
	if listed.Siblings[0].SaltHeld {
		t.Fatal("a salt is reported held before any arrived")
	}

	if _, err := setLocalDestroyPIN(ctx, d, ipc.SetDestroyPINRequest{New: "1234"}); err != nil {
		t.Fatalf("setLocalDestroyPIN: %v", err)
	}
	salt := []byte("their-salt-abcde")
	applyDestroySalt(ctx, d, railSiblingID, syncrail.NewDestroySaltBody(salt, 400), salt)

	req, _ = ipc.NewFrame(ipc.FrameGetDestroyPIN, "req-state-2", ipc.GetDestroyPINRequest{})
	writeFrame(t, ctx, conn, req)
	f = readUntil(t, ctx, conn, ipc.FrameDestroyPINListed)
	if f.ID != "req-state-2" {
		t.Fatalf("correlation id = %q, want req-state-2", f.ID)
	}
	listed = ipc.DestroyPINListedResponse{}
	if err := json.Unmarshal(f.Payload, &listed); err != nil {
		t.Fatal(err)
	}
	if !listed.Set {
		t.Fatal("the state frame does not report the destroy PIN set here")
	}
	if !listed.Siblings[0].SaltHeld {
		t.Fatal("the state frame does not report the sibling's salt held")
	}
	if listed.Siblings[0].ReceivedAt == 0 {
		t.Fatal("the state frame carries no received_at for a held salt")
	}

	if strings.Contains(string(f.Payload), hex.EncodeToString(salt)) {
		t.Fatalf("the state frame carries a salt: %s", f.Payload)
	}
	if strings.Contains(string(f.Payload), stub.DestroyPINVerifier) {
		t.Fatalf("the state frame carries the verifier: %s", f.Payload)
	}
}

func TestDestroyPIN_SetFrameRoundTrips(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	linkASibling(t, d)
	_ = d
	conn := dialTest(t, ctx, addr, certPath, token)

	req, _ := ipc.NewFrame(ipc.FrameSetDestroyPIN, "req-set-1", ipc.SetDestroyPINRequest{New: "1357"})
	writeFrame(t, ctx, conn, req)
	f := readUntil(t, ctx, conn, ipc.FrameDestroyPINSet)
	if f.ID != "req-set-1" {
		t.Fatalf("correlation id = %q, want req-set-1", f.ID)
	}
	var out ipc.DestroyPINSetResponse
	if err := json.Unmarshal(f.Payload, &out); err != nil {
		t.Fatal(err)
	}
	if !out.Accepted {
		t.Fatalf("the set frame was refused: %q", out.Reason)
	}
	if !stub.DestroyPINGuardSet {
		t.Fatal("haomad holds no guard after a set over the wire")
	}
	if want := storedVerifierFor(t, "1357", stub.DestroyPINSalt); stub.DestroyPINVerifier != want {
		t.Fatal("the guard stored over the wire does not match the PIN")
	}

	if strings.Contains(string(f.Payload), "1357") {
		t.Fatalf("the reply carries the PIN: %s", f.Payload)
	}
	if strings.Contains(string(f.Payload), stub.DestroyPINVerifier) {
		t.Fatalf("the reply carries the verifier: %s", f.Payload)
	}
}

func TestDestroyPINVerify_DerivesAgainstTheSiblingSalt(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	stub.DestroyVerifyAnswer = backendapi.VerifyDestroyPINResponse{Answered: true, Verified: true}

	siblingSalt := []byte("sibling-salt-16b")
	if _, err := d.linkedDevices.PutSiblingSalt(railSiblingID, linkeddevice.SiblingSalt{Salt: siblingSalt, SetAt: 1, ReceivedAt: 2}); err != nil {
		t.Fatalf("PutSiblingSalt: %v", err)
	}

	out, err := verifySiblingDestroyPIN(context.Background(), d, ipc.VerifyDestroyPINRequest{
		PeerID: railSiblingID, PIN: "1234",
	})
	if err != nil {
		t.Fatalf("verifySiblingDestroyPIN: %v", err)
	}
	if !out.Answered || !out.Verified {
		t.Fatalf("answer = %+v, want answered and verified", out)
	}
	if len(stub.DestroyVerifyBodies) != 1 {
		t.Fatalf("%d verify requests reached haomad, want 1", len(stub.DestroyVerifyBodies))
	}

	var sent backendapi.VerifyDestroyPINRequest
	if err := json.Unmarshal([]byte(stub.DestroyVerifyBodies[0]), &sent); err != nil {
		t.Fatalf("decode the verify request: %v", err)
	}
	want, err := destroypin.Derive("1234", siblingSalt)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if sent.Proof != hex.EncodeToString(want) {
		t.Fatal("the proof was not derived against the sibling's salt")
	}
	if sent.PeerID != railSiblingID {
		t.Fatalf("asked %q, want the sibling %q", sent.PeerID, railSiblingID)
	}
}

func TestDestroyPINVerify_ThePINNeverLeavesThisProcess(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	if _, err := d.linkedDevices.PutSiblingSalt(railSiblingID, linkeddevice.SiblingSalt{Salt: []byte("sibling-salt-16b"), SetAt: 1, ReceivedAt: 2}); err != nil {
		t.Fatalf("PutSiblingSalt: %v", err)
	}

	if _, err := verifySiblingDestroyPIN(context.Background(), d, ipc.VerifyDestroyPINRequest{
		PeerID: railSiblingID, PIN: "2468",
	}); err != nil {
		t.Fatalf("verifySiblingDestroyPIN: %v", err)
	}

	for _, body := range stub.DestroyVerifyBodies {
		var got map[string]any
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatalf("decode the request body: %v", err)
		}
		for k := range got {
			if k != "peer_id" && k != "proof" {
				t.Fatalf("the request body carries %q; only a peer id and a proof may cross", k)
			}
		}
	}
}

func TestDestroyPINVerify_LocalRefusalsSpendNoAttempt(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)

	out, err := verifySiblingDestroyPIN(context.Background(), d, ipc.VerifyDestroyPINRequest{
		PeerID: railSiblingID, PIN: "1234",
	})
	if err != nil {
		t.Fatalf("verify with no salt: %v", err)
	}
	if out.Answered || out.Reason != ipc.DestroyPINVerifyNoSalt {
		t.Fatalf("answer = %+v, want a no-salt refusal", out)
	}

	if _, err := d.linkedDevices.PutSiblingSalt(railSiblingID, linkeddevice.SiblingSalt{Salt: []byte("sibling-salt-16b"), SetAt: 1, ReceivedAt: 2}); err != nil {
		t.Fatalf("PutSiblingSalt: %v", err)
	}
	out, err = verifySiblingDestroyPIN(context.Background(), d, ipc.VerifyDestroyPINRequest{
		PeerID: railSiblingID, PIN: "12345",
	})
	if err != nil {
		t.Fatalf("verify with a bad shape: %v", err)
	}
	if out.Answered || out.Reason != ipc.DestroyPINRefusedShape {
		t.Fatalf("answer = %+v, want a shape refusal", out)
	}

	out, err = verifySiblingDestroyPIN(context.Background(), d, ipc.VerifyDestroyPINRequest{
		PeerID: "not-a-device-of-mine", PIN: "1234",
	})
	if err != nil {
		t.Fatalf("verify against a stranger: %v", err)
	}
	if out.Answered || out.Reason != ipc.DestroyPINVerifyUnknownSibling {
		t.Fatalf("answer = %+v, want an unknown-sibling refusal", out)
	}

	if len(stub.DestroyVerifyBodies) != 0 {
		t.Fatalf("%d refusals reached the sibling; none should", len(stub.DestroyVerifyBodies))
	}
}

func TestDestroyPINVerify_SilenceIsNotARefusal(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	if _, err := d.linkedDevices.PutSiblingSalt(railSiblingID, linkeddevice.SiblingSalt{Salt: []byte("sibling-salt-16b"), SetAt: 1, ReceivedAt: 2}); err != nil {
		t.Fatalf("PutSiblingSalt: %v", err)
	}

	stub.DestroyVerifyAnswer = backendapi.VerifyDestroyPINResponse{Answered: true, Verified: false}
	out, err := verifySiblingDestroyPIN(context.Background(), d, ipc.VerifyDestroyPINRequest{
		PeerID: railSiblingID, PIN: "1234",
	})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !out.Answered || out.Verified {
		t.Fatalf("answer = %+v, want answered and not verified", out)
	}

	stub.DestroyVerifyAnswer = backendapi.VerifyDestroyPINResponse{}
	out, err = verifySiblingDestroyPIN(context.Background(), d, ipc.VerifyDestroyPINRequest{
		PeerID: railSiblingID, PIN: "1234",
	})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if out.Answered || out.Verified {
		t.Fatalf("answer = %+v, want no answer at all", out)
	}
}

func TestDestroyPINVerify_AReplyThatDidNotCheckOutKeepsItsReason(t *testing.T) {
	if ipc.DestroyPINVerifyAnswerBad != backendapi.DestroyVerifyAnswerBad {
		t.Fatalf("ipc says %q and backendapi says %q; one token, change both", ipc.DestroyPINVerifyAnswerBad, backendapi.DestroyVerifyAnswerBad)
	}
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	if _, err := d.linkedDevices.PutSiblingSalt(railSiblingID, linkeddevice.SiblingSalt{Salt: []byte("sibling-salt-16b"), SetAt: 1, ReceivedAt: 2}); err != nil {
		t.Fatalf("PutSiblingSalt: %v", err)
	}

	stub.DestroyVerifyAnswer = backendapi.VerifyDestroyPINResponse{Reason: backendapi.DestroyVerifyAnswerBad}
	out, err := verifySiblingDestroyPIN(context.Background(), d, ipc.VerifyDestroyPINRequest{
		PeerID: railSiblingID, PIN: "1234",
	})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if out.Answered || out.Verified || out.Reason != ipc.DestroyPINVerifyAnswerBad {
		t.Fatalf("answer = %+v, want reason %q", out, ipc.DestroyPINVerifyAnswerBad)
	}
}

func TestUnlink_ForgetsTheOtherDeviceEvenWhenItNeverAnswers(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkASibling(t, d)
	if _, err := d.linkedDevices.PutSiblingSalt(railSiblingID, linkeddevice.SiblingSalt{
		Salt: []byte("sibling-salt-16b"), SetAt: 1, ReceivedAt: 2,
	}); err != nil {
		t.Fatalf("PutSiblingSalt: %v", err)
	}

	out, err := unlinkDevice(context.Background(), d, ipc.UnlinkDeviceRequest{
		PeerID: railSiblingID,
		PIN:    "1379",
	})
	if err != nil {
		t.Fatalf("unlinkDevice: %v", err)
	}
	if !out.Unlinked {
		t.Fatalf("the near half did not run: %+v", out)
	}

	members, err := d.linkedDevices.Members()
	if err != nil {
		t.Fatalf("Members: %v", err)
	}
	for _, m := range members {
		if m == railSiblingID {
			t.Fatal("the linked-device entry survived the unlink")
		}
	}
	if _, held, err := d.linkedDevices.SiblingSalt(railSiblingID); err != nil {
		t.Fatalf("SiblingSalt: %v", err)
	} else if held {
		t.Fatal("the other device's salt survived — it is the ingredient for a proof against it")
	}
	if stub.DestroyPINGuardSet {
		t.Fatal("this device's own guard survived, so it could never be given a new one")
	}
}

func TestUnlink_RefusesABadPINWithoutForgettingAnything(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkASibling(t, d)

	out, err := unlinkDevice(context.Background(), d, ipc.UnlinkDeviceRequest{
		PeerID: railSiblingID,
		PIN:    "12",
	})
	if err != nil {
		t.Fatalf("unlinkDevice: %v", err)
	}
	if out.Unlinked {
		t.Fatal("a three-digit PIN unlinked the device")
	}
	if out.Reason != ipc.DestroyPINRefusedShape {
		t.Fatalf("reason = %q, want %q", out.Reason, ipc.DestroyPINRefusedShape)
	}
	members, err := d.linkedDevices.Members()
	if err != nil {
		t.Fatalf("Members: %v", err)
	}
	if len(members) != 1 {
		t.Fatalf("the linked-device set changed on a refused unlink: %v", members)
	}
}

func TestUnlink_RefusesAPeerThatIsNotMyDevice(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkASibling(t, d)

	out, err := unlinkDevice(context.Background(), d, ipc.UnlinkDeviceRequest{
		PeerID: "somebody-else",
		PIN:    "1379",
	})
	if err != nil {
		t.Fatalf("unlinkDevice: %v", err)
	}
	if out.Unlinked || out.Reason != ipc.DestroyPINVerifyUnknownSibling {
		t.Fatalf("a stranger was unlinked: %+v", out)
	}
}

func TestUnlink_NoPINStillForgetsTheDevice(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkASibling(t, d)

	out, err := unlinkDevice(context.Background(), d, ipc.UnlinkDeviceRequest{PeerID: railSiblingID})
	if err != nil {
		t.Fatalf("unlinkDevice: %v", err)
	}
	if !out.Unlinked {
		t.Fatalf("an unlink with no PIN did nothing: %+v", out)
	}
	if out.Reason != "" {
		t.Fatalf("an unlink with no PIN was refused: %q", out.Reason)
	}
	members, err := d.linkedDevices.Members()
	if err != nil {
		t.Fatalf("Members: %v", err)
	}
	for _, m := range members {
		if m == railSiblingID {
			t.Fatal("the linked-device entry survived")
		}
	}

	for _, body := range stub.UnlinkBodies {
		if strings.Contains(body, `"proof":"`) && !strings.Contains(body, `"proof":""`) {
			t.Fatalf("an unlink with no PIN carried a proof: %s", body)
		}
	}
}

func TestUnlink_TellsTheClientsTheLinkedSetChanged(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	linkASibling(t, d)
	if linked, err := d.readHasSibling(); err != nil || !linked {
		t.Fatalf("fixture: the linked set reads %v (%v), want a linked device", linked, err)
	}
	conn := dialTest(t, ctx, addr, certPath, token)

	out, err := unlinkDevice(ctx, d, ipc.UnlinkDeviceRequest{PeerID: railSiblingID})
	if err != nil || !out.Unlinked {
		t.Fatalf("unlinkDevice: %+v (%v)", out, err)
	}

	f := readUntil(t, ctx, conn, ipc.FrameInheritedContactsChanged)
	var p ipc.InheritedContactsChangedPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Linked {
		t.Fatal("the push after the removal still says this device has a linked device")
	}
	if p.Contacts == nil {
		t.Fatal("the push carries no contacts list; a client would read it as a missing field")
	}
}

func TestUnlink_NarrowsBeforeAnythingIsSent(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	stub.UnlinkAnswer = backendapi.UnlinkResponse{Narrowed: true, Sent: true, Receipted: true}
	d, _, _, _ := newTestDaemon(t, stub)
	linkASibling(t, d)
	salt := siblingSaltFor(t, d)
	stub.DestroyVerifyAccepts = proofHexFor(t, "1379", salt)

	before := len(stub.CallOrder)
	out, err := unlinkDevice(context.Background(), d, ipc.UnlinkDeviceRequest{
		PeerID: railSiblingID,
		PIN:    "1379",
	})
	if err != nil {
		t.Fatalf("unlinkDevice: %v", err)
	}
	if !out.Unlinked {
		t.Fatalf("the near half did not run: %+v", out)
	}

	calls := stub.CallOrder[before:]
	want := []string{"/unlink", "/destroy-pin/verify", "/unlink"}
	if len(calls) != len(want) {
		t.Fatalf("call order %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("call order %v, want %v", calls, want)
		}
	}

	if len(stub.UnlinkBodies) != 2 {
		t.Fatalf("%d unlink calls, want 2", len(stub.UnlinkBodies))
	}
	if !strings.Contains(stub.UnlinkBodies[0], `"proof":""`) {
		t.Fatalf("the narrowing carried a proof: %s", stub.UnlinkBodies[0])
	}
	if strings.Contains(stub.UnlinkBodies[1], `"proof":""`) {
		t.Fatalf("the command carried no proof: %s", stub.UnlinkBodies[1])
	}
	if !out.Receipted {
		t.Fatalf("the receipt was not reported: %+v", out)
	}
}

func TestUnlink_AWrongPINForgetsNothingAndCanBeRetyped(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	stub.UnlinkAnswer = backendapi.UnlinkResponse{Narrowed: true, Sent: true, Receipted: true}
	d, _, _, _ := newTestDaemon(t, stub)
	linkASibling(t, d)
	salt := siblingSaltFor(t, d)
	stub.DestroyVerifyAccepts = proofHexFor(t, "1379", salt)

	if _, err := setLocalDestroyPIN(context.Background(), d, ipc.SetDestroyPINRequest{New: "2468"}); err != nil {
		t.Fatalf("setLocalDestroyPIN: %v", err)
	}
	if !stub.DestroyPINGuardSet {
		t.Fatal("the fixture did not set this device's own guard")
	}

	out, err := unlinkDevice(context.Background(), d, ipc.UnlinkDeviceRequest{
		PeerID: railSiblingID,
		PIN:    "9999",
	})
	if err != nil {
		t.Fatalf("unlinkDevice: %v", err)
	}
	if out.Unlinked {
		t.Fatal("a wrong PIN removed the device")
	}
	if out.Reason != ipc.DestroyPINRefusedWrong {
		t.Fatalf("reason = %q, want %q", out.Reason, ipc.DestroyPINRefusedWrong)
	}

	members, err := d.linkedDevices.Members()
	if err != nil {
		t.Fatalf("Members: %v", err)
	}
	if len(members) != 1 || members[0] != railSiblingID {
		t.Fatalf("the linked-device set changed on a wrong PIN: %v", members)
	}
	if _, held, err := d.linkedDevices.SiblingSalt(railSiblingID); err != nil || !held {
		t.Fatalf("the other device's salt was dropped on a wrong PIN (held=%v err=%v)", held, err)
	}
	if !stub.DestroyPINGuardSet {
		t.Fatal("this device's own guard was cleared on a wrong PIN")
	}

	for _, body := range stub.UnlinkBodies {
		if !strings.Contains(body, `"proof":""`) {
			t.Fatalf("a wrong PIN sent a destroy command: %s", body)
		}
	}

	out, err = unlinkDevice(context.Background(), d, ipc.UnlinkDeviceRequest{
		PeerID: railSiblingID,
		PIN:    "1379",
	})
	if err != nil {
		t.Fatalf("retype: %v", err)
	}
	if !out.Unlinked {
		t.Fatalf("the retype did not remove the device: %+v", out)
	}
	if !out.Receipted {
		t.Fatalf("the retype did not command the erasure: %+v", out)
	}
	members, err = d.linkedDevices.Members()
	if err != nil {
		t.Fatalf("Members: %v", err)
	}
	for _, m := range members {
		if m == railSiblingID {
			t.Fatal("the linked-device entry survived the retype")
		}
	}
}

func TestUnlink_ACooloffDoesNotBlockTheOperatorsOwnHalf(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkASibling(t, d)
	siblingSaltFor(t, d)

	stub.DestroyVerifyAccepts = "no-proof-matches-this"

	out, err := unlinkDevice(context.Background(), d, ipc.UnlinkDeviceRequest{
		PeerID: railSiblingID, PIN: "1379",
	})
	if err != nil {
		t.Fatalf("unlinkDevice: %v", err)
	}
	if out.Unlinked {
		t.Fatal("a refused proof removed the device")
	}

	before := len(stub.DestroyVerifyBodies)
	out, err = unlinkDevice(context.Background(), d, ipc.UnlinkDeviceRequest{PeerID: railSiblingID})
	if err != nil {
		t.Fatalf("unlink with no PIN: %v", err)
	}
	if !out.Unlinked {
		t.Fatalf("the operator could not take their own device off: %+v", out)
	}
	if len(stub.DestroyVerifyBodies) != before {
		t.Fatal("an unlink with no PIN still asked the guard")
	}
	members, err := d.linkedDevices.Members()
	if err != nil {
		t.Fatalf("Members: %v", err)
	}
	for _, m := range members {
		if m == railSiblingID {
			t.Fatal("the linked-device entry survived")
		}
	}
}

func siblingSaltFor(t *testing.T, d *daemon) []byte {
	t.Helper()
	salt := []byte("sibling-salt-16b")
	if _, err := d.linkedDevices.PutSiblingSalt(railSiblingID, linkeddevice.SiblingSalt{
		Salt: salt, SetAt: 1, ReceivedAt: 2,
	}); err != nil {
		t.Fatalf("PutSiblingSalt: %v", err)
	}
	return salt
}

func proofHexFor(t *testing.T, pin string, salt []byte) string {
	t.Helper()
	proof, err := destroypin.Derive(pin, salt)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	return hex.EncodeToString(proof)
}

func TestUnlink_AnUnreachableDeviceIsStillRemoved(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)

	stub.UnlinkAnswer = backendapi.UnlinkResponse{Narrowed: true}
	d, _, _, _ := newTestDaemon(t, stub)
	linkASibling(t, d)
	if _, err := d.linkedDevices.PutSiblingSalt(railSiblingID, linkeddevice.SiblingSalt{
		Salt: []byte("sibling-salt-16b"), SetAt: 1, ReceivedAt: 2,
	}); err != nil {
		t.Fatalf("PutSiblingSalt: %v", err)
	}

	out, err := unlinkDevice(context.Background(), d, ipc.UnlinkDeviceRequest{
		PeerID: railSiblingID,
		PIN:    "1379",
	})
	if err != nil {
		t.Fatalf("unlinkDevice: %v", err)
	}
	if !out.Unlinked {
		t.Fatalf("an unreachable device was not removed: %+v", out)
	}

	if out.Sent || out.Receipted {
		t.Fatalf("an unreachable device was reported as commanded: %+v", out)
	}
}

func TestDestroyDevice_StopsWithoutClosingTheStore(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)

	exited := make(chan int, 1)
	prev := exitForDestroy
	exitForDestroy = func(code int) { exited <- code }
	t.Cleanup(func() { exitForDestroy = prev })

	stopForDestroy(d, "the other device commanded it", "erase-store")

	select {
	case code := <-exited:
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
	default:
		t.Fatal("the instruction to stop did not end the process")
	}

	linkASibling(t, d)
	if _, err := d.linkedDevices.Members(); err != nil {
		t.Fatalf("the store was closed on the way out: %v", err)
	}
}

func TestUnlink_ADestroyAgainstAnOfflineDeviceArmsTheAnswer(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	stub.DestroyPINGuardSet = true
	stub.DestroyPINSalt = "0011223344556677"

	stub.UnlinkAnswer = backendapi.UnlinkResponse{Narrowed: true, Deadline: 1_790_000_000}

	stub.DestroyVerifyStatus = http.StatusNotFound

	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)

	if _, err := d.linkedDevices.PutSiblingSalt(railSiblingID, linkeddevice.SiblingSalt{
		Salt: []byte("sibling-salt-16b"), SetAt: 1, ReceivedAt: 2,
	}); err != nil {
		t.Fatalf("PutSiblingSalt: %v", err)
	}

	out, err := unlinkDevice(context.Background(), d, ipc.UnlinkDeviceRequest{
		PeerID: railSiblingID,
		PIN:    "4321",
	})
	if err != nil {
		t.Fatalf("the removal reported an error instead of completing: %v", err)
	}
	if !out.Unlinked {
		t.Fatalf("the device was not removed: %+v", out)
	}
	if out.Sent {
		t.Error("a destroy was reported as sent to a device with no address")
	}
	if out.Reason != "" {
		t.Errorf("reason = %q, want none — the removal completed", out.Reason)
	}

	if out.Deadline != stub.UnlinkAnswer.Deadline {
		t.Errorf("deadline = %d, want %d — the screen would show no window",
			out.Deadline, stub.UnlinkAnswer.Deadline)
	}

	if len(stub.UnlinkBodies) != 2 {
		t.Fatalf("/unlink called %d times, want 2: narrow, then command", len(stub.UnlinkBodies))
	}
	var first, second backendapi.UnlinkRequest
	if err := json.Unmarshal([]byte(stub.UnlinkBodies[0]), &first); err != nil {
		t.Fatalf("first /unlink body: %v", err)
	}
	if err := json.Unmarshal([]byte(stub.UnlinkBodies[1]), &second); err != nil {
		t.Fatalf("second /unlink body: %v", err)
	}
	if !first.NarrowOnly || first.Proof != "" {
		t.Errorf("the first call was %+v, want narrow-only with no proof", first)
	}
	if second.Proof == "" {
		t.Error("the second call sent no proof, so haomad would finish the removal " +
			"and drop the address instead of arming the answer")
	}
	if second.NarrowOnly {
		t.Error("the second call asked to narrow again instead of commanding")
	}
}

func TestDestroyDevice_StopsTheStreamersBeforeEnding(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)

	stopped := make(chan struct{})
	prevShutdown := shutdownStreamers
	shutdownStreamers = func(*daemon) { close(stopped) }
	t.Cleanup(func() { shutdownStreamers = prevShutdown })

	exitedWithStreamersStopped := make(chan bool, 1)
	prevExit := exitForDestroy
	exitForDestroy = func(int) {
		select {
		case <-stopped:
			exitedWithStreamersStopped <- true
		default:
			exitedWithStreamersStopped <- false
		}
	}
	t.Cleanup(func() { exitForDestroy = prevExit })

	stopForDestroy(d, "the other device commanded it", "erase-streamers")

	select {
	case ok := <-exitedWithStreamersStopped:
		if !ok {
			t.Fatal("the process ended with the native streamers still running")
		}
	default:
		t.Fatal("the instruction to stop did not end the process")
	}
}

func TestDestroyDevice_AStreamerThatWillNotStopDoesNotHoldTheExit(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)

	release := make(chan struct{})
	finished := make(chan struct{})
	prevShutdown := shutdownStreamers
	shutdownStreamers = func(*daemon) {
		<-release
		close(finished)
	}
	t.Cleanup(func() {
		close(release)
		<-finished
		shutdownStreamers = prevShutdown
	})

	prevGrace := destroyStreamerGraceForTest
	destroyStreamerGraceForTest = 50 * time.Millisecond
	t.Cleanup(func() { destroyStreamerGraceForTest = prevGrace })

	exited := make(chan int, 1)
	prevExit := exitForDestroy
	exitForDestroy = func(code int) { exited <- code }
	t.Cleanup(func() { exitForDestroy = prevExit })

	stopForDestroy(d, "the other device commanded it", "erase-wedged")

	select {
	case code := <-exited:
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
	default:
		t.Fatal("a streamer that would not stop held up the exit")
	}
}

func TestDestroyDevice_ThePushCarriesTheEraseID(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	conn := dialTest(t, ctx, addr, certPath, token)

	exited := make(chan int, 1)
	prevExit := exitForDestroy
	exitForDestroy = func(code int) { exited <- code }
	t.Cleanup(func() { exitForDestroy = prevExit })

	go stopForDestroy(d, "the other device commanded it", "erase-9f2c1d")

	got := readUntil(t, ctx, conn, ipc.FrameDestroyDevice)
	var p ipc.DestroyDevicePush
	if err := json.Unmarshal(got.Payload, &p); err != nil {
		t.Fatalf("decode the push: %v", err)
	}
	if p.EraseID != "erase-9f2c1d" {
		t.Fatalf("erase id on the push = %q, want %q", p.EraseID, "erase-9f2c1d")
	}
	if p.Reason != "the other device commanded it" {
		t.Fatalf("reason on the push = %q", p.Reason)
	}
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		t.Fatal("the instruction to stop never ended the process")
	}
}

func TestDestroyDevice_AnErasureWithNoIDStillStops(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	conn := dialTest(t, ctx, addr, certPath, token)

	exited := make(chan int, 1)
	prevExit := exitForDestroy
	exitForDestroy = func(code int) { exited <- code }
	t.Cleanup(func() { exitForDestroy = prevExit })

	go stopForDestroy(d, "the other device commanded it", "")

	got := readUntil(t, ctx, conn, ipc.FrameDestroyDevice)
	var p ipc.DestroyDevicePush
	if err := json.Unmarshal(got.Payload, &p); err != nil {
		t.Fatalf("decode the push: %v", err)
	}
	if p.EraseID != "" {
		t.Fatalf("an unnamed erasure grew an id: %q", p.EraseID)
	}
	if p.Reason != "the other device commanded it" {
		t.Fatalf("reason on the push = %q", p.Reason)
	}
	select {
	case code := <-exited:
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("an erasure with no id never ended the process")
	}
}
