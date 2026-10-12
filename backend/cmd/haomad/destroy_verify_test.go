package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"haoma/internal/store"
	"haoma/internal/xport"
)

const testSiblingPeerID = "sibling-peer"

func verifyEnvelope(t *testing.T, proofHex string) xport.Envelope {
	t.Helper()
	body, err := json.Marshal(destroyVerifyBody{Proof: proofHex})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return xport.Envelope{ID: "verify-1", Kind: xport.KindDestroyVerify, Payload: body}
}

func proofAccepted(t *testing.T, d *daemon, env xport.Envelope, senderPeerID string) bool {
	t.Helper()
	outcome, err := checkDestroyProofEnvelope(d, env, senderPeerID)
	if err != nil {
		t.Fatalf("checkDestroyProof: %v", err)
	}
	return outcome == destroyProofRight
}

func setGuard(t *testing.T, d *daemon, proofHex string) {
	t.Helper()
	setGuardFor(t, d, proofHex, testSiblingPeerID)
}

func setGuardFor(t *testing.T, d *daemon, proofHex, siblingPeerID string) {
	t.Helper()
	proof, err := hex.DecodeString(proofHex)
	if err != nil {
		t.Fatalf("decode proof: %v", err)
	}
	sum := sha256.Sum256(proof)
	if err := d.store.PutDestroyPIN(store.DestroyPIN{
		Salt: []byte("salt"), Verifier: sum[:], SiblingPeerID: siblingPeerID, SetAt: 1,
	}); err != nil {
		t.Fatalf("PutDestroyPIN: %v", err)
	}
}

func TestDestroyVerify_RightProofAcceptedWrongRefused(t *testing.T) {
	d := newTestDaemon(t)
	setGuard(t, d, "c3d4")

	if !proofAccepted(t, d, verifyEnvelope(t, "c3d4"), testSiblingPeerID) {
		t.Fatal("the right proof was refused")
	}
	if proofAccepted(t, d, verifyEnvelope(t, "dead"), testSiblingPeerID) {
		t.Fatal("a wrong proof was accepted")
	}
}

func TestDestroyVerify_TheStoredVerifierIsNotAProof(t *testing.T) {
	d := newTestDaemon(t)
	setGuard(t, d, "c3d4")

	guard, err := d.store.GetDestroyPIN()
	if err != nil {
		t.Fatalf("GetDestroyPIN: %v", err)
	}
	if proofAccepted(t, d, verifyEnvelope(t, hex.EncodeToString(guard.Verifier)), testSiblingPeerID) {
		t.Fatal("the stored verifier was accepted as a proof")
	}
}

func TestDestroyVerify_ThreeWrongThenAnHourOfNothing(t *testing.T) {
	d := newTestDaemon(t)
	setGuard(t, d, "c3d4")

	for i := 1; i <= destroyAttemptLimit; i++ {
		if proofAccepted(t, d, verifyEnvelope(t, "dead"), testSiblingPeerID) {
			t.Fatalf("attempt %d accepted a wrong proof", i)
		}
	}

	att, err := d.store.GetDestroyAttempts()
	if err != nil {
		t.Fatalf("GetDestroyAttempts: %v", err)
	}
	if att.BlockedUntil <= time.Now().Unix() {
		t.Fatalf("no cool-off after %d wrong proofs: %+v", destroyAttemptLimit, att)
	}
	if want := time.Now().Unix() + destroyCooloffSeconds; att.BlockedUntil > want+5 {
		t.Fatalf("cool-off runs to %d, want about %d", att.BlockedUntil, want)
	}

	if proofAccepted(t, d, verifyEnvelope(t, "c3d4"), testSiblingPeerID) {
		t.Fatal("the right proof was accepted during a cool-off")
	}
}

func TestDestroyVerify_TheCountIsOnDisk(t *testing.T) {
	d := newTestDaemon(t)
	setGuard(t, d, "c3d4")

	if _, err := checkDestroyProofEnvelope(d, verifyEnvelope(t, "dead"), testSiblingPeerID); err != nil {
		t.Fatalf("first wrong proof: %v", err)
	}
	att, err := d.store.GetDestroyAttempts()
	if err != nil {
		t.Fatalf("GetDestroyAttempts: %v", err)
	}
	if att.Failures != 1 {
		t.Fatalf("failures = %d, want 1", att.Failures)
	}
}

func TestDestroyVerify_ARightProofClearsTheCount(t *testing.T) {
	d := newTestDaemon(t)
	setGuard(t, d, "c3d4")

	for i := 0; i < destroyAttemptLimit-1; i++ {
		if _, err := checkDestroyProofEnvelope(d, verifyEnvelope(t, "dead"), testSiblingPeerID); err != nil {
			t.Fatalf("wrong proof %d: %v", i, err)
		}
	}
	if _, err := checkDestroyProofEnvelope(d, verifyEnvelope(t, "c3d4"), testSiblingPeerID); err != nil {
		t.Fatalf("right proof: %v", err)
	}
	att, err := d.store.GetDestroyAttempts()
	if err != nil {
		t.Fatalf("GetDestroyAttempts: %v", err)
	}
	if att.Failures != 0 || att.BlockedUntil != 0 {
		t.Fatalf("the count survived a right proof: %+v", att)
	}
}

func TestDestroyVerify_ADeviceWithNoGuardDropsAndRecordsNothing(t *testing.T) {
	d := newTestDaemon(t)

	for i := 0; i < destroyAttemptLimit*3; i++ {
		outcome, err := checkDestroyProofEnvelope(d, verifyEnvelope(t, "c3d4"), "any-paired-contact")
		if err != nil {
			t.Fatalf("proof %d: %v", i, err)
		}
		if outcome != destroyProofDropped {
			t.Fatalf("a device with no guard answered proof %d: %v", i, outcome)
		}
	}

	att, err := d.store.GetDestroyAttempts()
	if err != nil {
		t.Fatalf("GetDestroyAttempts: %v", err)
	}
	if att.Failures != 0 || att.BlockedUntil != 0 || att.Unseen != 0 || att.LastAt != 0 {
		t.Fatalf("a device with no guard recorded something: %+v", att)
	}
}

func TestDestroyVerify_AStrangerIsNotAnswered(t *testing.T) {
	d := newTestDaemon(t)
	setGuard(t, d, "c3d4")

	for _, proof := range []string{"c3d4", "dead"} {
		outcome, err := checkDestroyProofEnvelope(d, verifyEnvelope(t, proof), "some-other-peer")
		if err != nil {
			t.Fatalf("stranger proof %q: %v", proof, err)
		}
		if outcome != destroyProofDropped {
			t.Fatalf("a stranger sending %q was answered: %v", proof, outcome)
		}
	}
}

func TestDestroyVerify_TheCooloffHourIsStillRecorded(t *testing.T) {
	d := newTestDaemon(t)
	setGuard(t, d, "c3d4")

	for i := 0; i < destroyAttemptLimit; i++ {
		if proofAccepted(t, d, verifyEnvelope(t, "dead"), testSiblingPeerID) {
			t.Fatalf("wrong proof %d was accepted", i)
		}
	}
	att, err := d.store.GetDestroyAttempts()
	if err != nil {
		t.Fatalf("GetDestroyAttempts: %v", err)
	}
	if att.BlockedUntil == 0 {
		t.Fatalf("no cool-off after %d wrong proofs: %+v", destroyAttemptLimit, att)
	}
	blockedUntil := att.BlockedUntil
	unseenBefore := att.Unseen

	const during = 20
	for i := 0; i < during; i++ {
		if proofAccepted(t, d, verifyEnvelope(t, "dead"), testSiblingPeerID) {
			t.Fatalf("cool-off proof %d was accepted", i)
		}
	}

	att, err = d.store.GetDestroyAttempts()
	if err != nil {
		t.Fatalf("GetDestroyAttempts: %v", err)
	}
	if want := unseenBefore + during; att.Unseen != want {
		t.Fatalf("unseen = %d after %d proofs in the cool-off, want %d", att.Unseen, during, want)
	}
	if att.LastAt == 0 {
		t.Fatal("a proof in the cool-off was not stamped")
	}

	if att.Failures != 0 {
		t.Fatalf("failures = %d, want 0: a proof in the cool-off spent an attempt", att.Failures)
	}
	if att.BlockedUntil != blockedUntil {
		t.Fatalf("blocked_until moved from %d to %d during the cool-off", blockedUntil, att.BlockedUntil)
	}
}

func TestDestroyVerify_RubbishPayloadIsJustWrong(t *testing.T) {
	d := newTestDaemon(t)
	setGuard(t, d, "c3d4")

	for _, payload := range [][]byte{nil, []byte("not json"), []byte(`{"proof":"zz"}`), []byte(`{"proof":""}`)} {
		env := xport.Envelope{ID: "verify-junk", Kind: xport.KindDestroyVerify, Payload: payload}
		if proofAccepted(t, d, env, testSiblingPeerID) {
			t.Fatalf("payload %q was accepted", payload)
		}
	}
}

func TestDestroyVerify_OnlyTheGuardsOwnPeerMaySpendAnAttempt(t *testing.T) {
	d := newTestDaemon(t)
	setGuard(t, d, "c3d4")

	for i := 0; i < destroyAttemptLimit*3; i++ {
		outcome, err := checkDestroyProofEnvelope(d, verifyEnvelope(t, "dead"), "some-other-peer")
		if err != nil {
			t.Fatalf("stranger attempt %d: %v", i, err)
		}
		if outcome != destroyProofDropped {
			t.Fatalf("stranger attempt %d was answered: %v", i, outcome)
		}
	}

	att, err := d.store.GetDestroyAttempts()
	if err != nil {
		t.Fatalf("GetDestroyAttempts: %v", err)
	}
	if att.Failures != 0 || att.BlockedUntil != 0 {
		t.Fatalf("a stranger moved the count: %+v", att)
	}

	if !proofAccepted(t, d, verifyEnvelope(t, "c3d4"), testSiblingPeerID) {
		t.Fatal("the guard refused the peer it belongs to")
	}
}

func TestDestroyVerify_WrongProofsAreKeptForTheOperator(t *testing.T) {
	d := newTestDaemon(t)
	setGuard(t, d, "c3d4")

	for i := 0; i < 2; i++ {
		if _, err := checkDestroyProofEnvelope(d, verifyEnvelope(t, "dead"), testSiblingPeerID); err != nil {
			t.Fatalf("wrong proof %d: %v", i, err)
		}
	}
	att, err := d.store.GetDestroyAttempts()
	if err != nil {
		t.Fatalf("GetDestroyAttempts: %v", err)
	}
	if att.Unseen != 2 {
		t.Fatalf("unseen = %d after two wrong proofs, want 2", att.Unseen)
	}
	if att.LastAt == 0 {
		t.Fatal("the wrong proof was not stamped")
	}

	if _, err := checkDestroyProofEnvelope(d, verifyEnvelope(t, "c3d4"), testSiblingPeerID); err != nil {
		t.Fatalf("right proof: %v", err)
	}
	att, err = d.store.GetDestroyAttempts()
	if err != nil {
		t.Fatalf("GetDestroyAttempts: %v", err)
	}
	if att.Failures != 0 {
		t.Fatalf("failures = %d after a right proof, want 0", att.Failures)
	}
	if att.Unseen != 2 {
		t.Fatalf("a right proof erased the record of %d wrong ones", 2-att.Unseen)
	}

	count, lastAt, err := d.store.DrainDestroyAttemptNotices()
	if err != nil {
		t.Fatalf("DrainDestroyAttemptNotices: %v", err)
	}
	if count != 2 || lastAt == 0 {
		t.Fatalf("drained count=%d last_at=%d, want 2 and a stamp", count, lastAt)
	}
	count, _, err = d.store.DrainDestroyAttemptNotices()
	if err != nil {
		t.Fatalf("second drain: %v", err)
	}
	if count != 0 {
		t.Fatalf("a second drain handed over %d attempts again", count)
	}
}

func TestDestroyVerify_AStrangerLeavesNoNotice(t *testing.T) {
	d := newTestDaemon(t)
	setGuard(t, d, "c3d4")

	if _, err := checkDestroyProofEnvelope(d, verifyEnvelope(t, "dead"), "some-other-peer"); err != nil {
		t.Fatalf("stranger proof: %v", err)
	}
	att, err := d.store.GetDestroyAttempts()
	if err != nil {
		t.Fatalf("GetDestroyAttempts: %v", err)
	}
	if att.Unseen != 0 {
		t.Fatalf("a stranger left %d notices", att.Unseen)
	}
}
