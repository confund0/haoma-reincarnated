package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"haoma/internal/peers"
)

func railAnswer(proofHex string) http.Header {
	h := http.Header{}
	h.Set(destroyProofHeader, proofHex)
	return h
}

func dialledHost(t *testing.T, p peers.Peer) string {
	t.Helper()
	if len(p.KnownAddresses) == 0 {
		t.Fatal("fixture peer has no dialled address")
	}
	return p.KnownAddresses[0] + ".onion"
}

func removedDevice(t *testing.T, d *daemon, proofHex string) peers.Peer {
	t.Helper()
	sib := addRemovablePeer(t, d, "sibling0aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	setGuardFor(t, d, proofHex, sib.ID)
	d.cfg.dataRoot = newDataRoot(t)
	return sib
}

func withoutRailDebounce(t *testing.T) {
	t.Helper()
	prev := railAnswerDebounceForTest
	railAnswerDebounceForTest = 0
	t.Cleanup(func() { railAnswerDebounceForTest = prev })
}

func TestSelfErase_TheRightProofOnADialledAddressErases(t *testing.T) {
	withFastWipeGrace(t)
	exited := captureExit(t)
	d := newTestDaemon(t)
	sib := removedDevice(t, d, "c3d4")

	if !d.readDestroyAnswer(sib.ID, dialledHost(t, sib), railAnswer("c3d4")) {
		t.Fatal("the right proof did not start the erasure")
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the erasure did not finish")
	}
}

func TestSelfErase_ThisDevicesOwnPublishedAddressErasesNothing(t *testing.T) {
	withFastWipeGrace(t)
	exited := captureExit(t)
	d := newTestDaemon(t)
	sib := removedDevice(t, d, "c3d4")

	if d.readDestroyAnswer(sib.ID, sib.MyOnionAddr+".onion", railAnswer("c3d4")) {
		t.Fatal("a 418 on this device's own published address erased it")
	}
	select {
	case <-exited:
		t.Fatal("the erasure ran")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestSelfErase_A418WithNoProofCostsNoAttempt(t *testing.T) {
	d := newTestDaemon(t)
	sib := removedDevice(t, d, "c3d4")

	if d.readDestroyAnswer(sib.ID, dialledHost(t, sib), http.Header{}) {
		t.Fatal("a 418 with no proof started an erasure")
	}
	att, err := d.store.GetDestroyAttempts()
	if err != nil {
		t.Fatalf("GetDestroyAttempts: %v", err)
	}
	if att.Failures != 0 || att.Unseen != 0 {
		t.Fatalf("a bare 418 was counted: failures=%d unseen=%d", att.Failures, att.Unseen)
	}
}

func TestSelfErase_AWrongProofSpendsTheSameAttempts(t *testing.T) {
	withoutRailDebounce(t)
	d := newTestDaemon(t)
	sib := removedDevice(t, d, "c3d4")

	for i := 0; i < destroyAttemptLimit; i++ {
		if d.readDestroyAnswer(sib.ID, dialledHost(t, sib), railAnswer("dead")) {
			t.Fatalf("wrong proof %d erased this device", i+1)
		}
	}
	att, err := d.store.GetDestroyAttempts()
	if err != nil {
		t.Fatalf("GetDestroyAttempts: %v", err)
	}
	if att.BlockedUntil <= time.Now().Unix() {
		t.Fatal("three wrong rail answers did not start the cool-off")
	}

	if proofAccepted(t, d, verifyEnvelope(t, "c3d4"), sib.ID) {
		t.Fatal("the envelope path accepted a proof during a cool-off the rail answer started")
	}
}

func TestSelfErase_OnlyThePeerTheGuardBelongsTo(t *testing.T) {
	d := newTestDaemon(t)
	removedDevice(t, d, "c3d4")
	contact := addRemovablePeer(t, d, "contact0aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	if d.readDestroyAnswer(contact.ID, dialledHost(t, contact), railAnswer("c3d4")) {
		t.Fatal("a contact's 418 erased this device")
	}
	att, err := d.store.GetDestroyAttempts()
	if err != nil {
		t.Fatalf("GetDestroyAttempts: %v", err)
	}
	if att.Failures != 0 || att.Unseen != 0 {
		t.Fatalf("a contact's 418 was counted: failures=%d unseen=%d", att.Failures, att.Unseen)
	}
}

type stubTransport struct {
	status int
	header http.Header
	body   string
	calls  int
}

func (s *stubTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	s.calls++
	h := s.header
	if h == nil {
		h = http.Header{}
	}
	return &http.Response{
		StatusCode: s.status,
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(s.body)),
	}, nil
}

func TestSelfErase_TheWatchLeavesTheResponseAlone(t *testing.T) {
	withFastWipeGrace(t)
	exited := captureExit(t)
	d := newTestDaemon(t)
	sib := removedDevice(t, d, "c3d4")

	stub := &stubTransport{status: http.StatusTeapot, header: railAnswer("c3d4"), body: "gone"}
	hc := d.watchForRemoval(sib.ID, &http.Client{Transport: stub})

	resp, err := hc.Get("http://" + dialledHost(t, sib) + "/message")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot {
		t.Fatalf("the caller saw %d, want 418", resp.StatusCode)
	}
	if got := resp.Header.Get(destroyProofHeader); got != "c3d4" {
		t.Fatalf("the caller saw proof %q, want the one that was sent", got)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != "gone" {
		t.Fatalf("the caller read %q, want the body the peer sent", body)
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the erasure did not finish")
	}
}

func TestSelfErase_TheWatchIgnoresEveryOtherStatus(t *testing.T) {
	d := newTestDaemon(t)
	sib := removedDevice(t, d, "c3d4")

	stub := &stubTransport{status: http.StatusOK, header: railAnswer("c3d4"), body: "ok"}
	hc := d.watchForRemoval(sib.ID, &http.Client{Transport: stub})

	resp, err := hc.Get("http://" + dialledHost(t, sib) + "/message")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the caller saw %d, want 200", resp.StatusCode)
	}
	att, err := d.store.GetDestroyAttempts()
	if err != nil {
		t.Fatalf("GetDestroyAttempts: %v", err)
	}
	if att.Unseen != 0 {
		t.Fatal("a 200 with the header on it was read as a removal")
	}
}

func TestSelfErase_NoDataRootErasesNothing(t *testing.T) {
	withFastWipeGrace(t)
	exited := captureExit(t)
	d := newTestDaemon(t)
	sib := removedDevice(t, d, "c3d4")
	d.cfg.dataRoot = ""

	if d.readDestroyAnswer(sib.ID, dialledHost(t, sib), railAnswer("c3d4")) {
		t.Fatal("a daemon with no data root started an erasure")
	}
	select {
	case <-exited:
		t.Fatal("the erasure ran")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestSelfErase_AnAnswerIsNotCheckedTwiceInAGap(t *testing.T) {
	d := newTestDaemon(t)
	sib := removedDevice(t, d, "c3d4")

	for i := 0; i < 6; i++ {
		d.readDestroyAnswer(sib.ID, dialledHost(t, sib), railAnswer("dead"))
	}
	att, err := d.store.GetDestroyAttempts()
	if err != nil {
		t.Fatalf("GetDestroyAttempts: %v", err)
	}
	if att.Unseen != 1 {
		t.Fatalf("six dials inside the gap were counted %d times, want 1", att.Unseen)
	}
}

func TestSelfErase_TheGapDoesNotDelayARightProof(t *testing.T) {
	withFastWipeGrace(t)
	exited := captureExit(t)
	d := newTestDaemon(t)
	sib := removedDevice(t, d, "c3d4")

	if !d.readDestroyAnswer(sib.ID, dialledHost(t, sib), railAnswer("c3d4")) {
		t.Fatal("the first answer was not acted on")
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the erasure did not finish")
	}
}
