package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"haoma/internal/peers"
	"haoma/internal/store"
)

func TestUnlink_NarrowingAcceptsOnlyTheReceipt(t *testing.T) {
	d := newTestDaemon(t)

	if !d.unlinkAcceptsKind("some-peer", "text") {
		t.Fatal("a device with no unlink in flight refused ordinary traffic")
	}

	if _, err := d.beginUnlink("removed-peer"); err != nil {
		t.Fatalf("beginUnlink: %v", err)
	}

	for _, kind := range []string{"text", "status", "destroy", "destroy_verify", "sent_ack"} {
		if d.unlinkAcceptsKind("removed-peer", kind) {
			t.Fatalf("the narrowed rail accepted %q from the device being removed", kind)
		}
	}
	if !d.unlinkAcceptsKind("removed-peer", "destroy_receipt") {
		t.Fatal("the narrowed rail refused the one kind it exists to accept")
	}

	if !d.unlinkAcceptsKind("somebody-else", "text") {
		t.Fatal("the narrowing reached a peer it does not name")
	}
}

func TestUnlink_ARaceBackIsDropped(t *testing.T) {
	d := newTestDaemon(t)
	if _, err := d.beginUnlink("removed-peer"); err != nil {
		t.Fatalf("beginUnlink: %v", err)
	}
	if d.unlinkAcceptsKind("removed-peer", "destroy") {
		t.Fatal("the device being removed could still command this one")
	}
}

func TestUnlink_NarrowingIsOnDisk(t *testing.T) {
	d := newTestDaemon(t)
	if _, err := d.beginUnlink("removed-peer"); err != nil {
		t.Fatalf("beginUnlink: %v", err)
	}

	rec, err := d.store.GetUnlinkPending()
	if err != nil {
		t.Fatalf("GetUnlinkPending: %v", err)
	}
	if !rec.InFlight() || rec.PeerID != "removed-peer" {
		t.Fatalf("the narrowing did not reach the store: %+v", rec)
	}
	if rec.Deadline <= time.Now().Unix() {
		t.Fatalf("the deadline is already past: %+v", rec)
	}
}

func TestUnlink_RefusesASecondAgainstAnotherDevice(t *testing.T) {
	d := newTestDaemon(t)
	if _, err := d.beginUnlink("removed-peer"); err != nil {
		t.Fatalf("beginUnlink: %v", err)
	}
	if _, err := d.beginUnlink("another-peer"); err == nil {
		t.Fatal("a second unlink against another device was accepted")
	}

	if _, err := d.beginUnlink("removed-peer"); err != nil {
		t.Fatalf("the same unlink again was refused: %v", err)
	}
}

func TestUnlink_TheDeadlineClosesTheAddress(t *testing.T) {
	d := newTestDaemon(t)
	peer := addRemovablePeer(t, d, "removed0aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	if err := d.store.PutUnlinkPending(store.UnlinkPending{
		PeerID:    peer.ID,
		Deadline:  time.Now().Add(-time.Minute).Unix(),
		StartedAt: time.Now().Add(-2 * time.Hour).Unix(),
	}); err != nil {
		t.Fatalf("PutUnlinkPending: %v", err)
	}

	d.finishUnlink("no receipt arrived before the deadline")

	rec, err := d.store.GetUnlinkPending()
	if err != nil {
		t.Fatalf("GetUnlinkPending: %v", err)
	}
	if rec.InFlight() {
		t.Fatalf("the narrowing outlived its deadline: %+v", rec)
	}

	if !d.unlinkAcceptsKind(peer.ID, "text") {
		t.Fatal("the narrowing is still in force after it finished")
	}
}

func TestUnlink_WithNoProofFinishesAtOnceAndHoldsNoNarrowing(t *testing.T) {
	d := newTestDaemon(t)
	peer := addRemovablePeer(t, d, "removed1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	rec := httptest.NewRecorder()
	d.handleUnlink(rec, unlinkRequestFor(peer.ID, ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("plain unlink answered %d, want 200: %s", rec.Code, rec.Body.String())
	}
	pending, err := d.store.GetUnlinkPending()
	if err != nil {
		t.Fatalf("GetUnlinkPending: %v", err)
	}
	if pending.InFlight() {
		t.Fatalf("a plain unlink left a narrowing waiting for a receipt that cannot come: %+v", pending)
	}
}

func TestUnlink_APlainUnlinkDoesNotBlockRemovingTheNextDevice(t *testing.T) {
	d := newTestDaemon(t)
	first := addRemovablePeer(t, d, "removed2aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	second := addRemovablePeer(t, d, "removed3aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	firstRec := httptest.NewRecorder()
	d.handleUnlink(firstRec, unlinkRequestFor(first.ID, ""))
	if firstRec.Code != http.StatusOK {
		t.Fatalf("first unlink answered %d, want 200", firstRec.Code)
	}

	secondRec := httptest.NewRecorder()
	d.handleUnlink(secondRec, unlinkRequestFor(second.ID, ""))
	if secondRec.Code == http.StatusConflict {
		t.Fatalf("removing a second device was refused because the first was still "+
			"'in flight': %s", secondRec.Body.String())
	}
	if secondRec.Code != http.StatusOK {
		t.Fatalf("second unlink answered %d, want 200: %s", secondRec.Code, secondRec.Body.String())
	}
}

func TestUnlink_NarrowOnlyHoldsTheNarrowingAndKeepsTheAddress(t *testing.T) {
	d := newTestDaemon(t)
	peer := addRemovablePeer(t, d, "removed4aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	rec := httptest.NewRecorder()
	d.handleUnlink(rec, unlinkNarrowOnlyRequestFor(peer.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("narrow-only answered %d, want 200: %s", rec.Code, rec.Body.String())
	}

	pending, err := d.store.GetUnlinkPending()
	if err != nil {
		t.Fatalf("GetUnlinkPending: %v", err)
	}
	if !pending.InFlight() {
		t.Fatal("narrow-only did not hold the narrowing, so the PIN check would run on an open rail")
	}

	got, err := d.registry.Get(peer.ID)
	if err != nil {
		t.Fatalf("the peer was retired by a call that only meant to narrow: %v", err)
	}
	if got.MyOnionAddr == "" || len(got.KnownAddresses) == 0 {
		t.Fatal("the address was dropped by a call that only meant to narrow; " +
			"the PIN check in step two has nothing left to dial")
	}
}

func unlinkNarrowOnlyRequestFor(peerID string) *http.Request {
	body := `{"peer_id":"` + peerID + `","narrow_only":true}`
	req := httptest.NewRequest(http.MethodPost, "/unlink", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func unlinkRequestFor(peerID, proof string) *http.Request {
	body := `{"peer_id":"` + peerID + `"`
	if proof != "" {
		body += `,"proof":"` + proof + `"`
	}
	body += `}`
	req := httptest.NewRequest(http.MethodPost, "/unlink", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func addRemovablePeer(t *testing.T, d *daemon, addr string) peers.Peer {
	t.Helper()
	peer := freshTestPeer(t, addr)
	peer.MyOnionAddr = publishedAddrFor(addr)
	if err := d.registry.Add(peer); err != nil {
		t.Fatalf("registry.Add: %v", err)
	}
	return peer
}

func publishedAddrFor(addr string) string {
	if len(addr) < 4 {
		return addr + "-published"
	}
	return addr[:len(addr)-4] + "pub0"
}

func TestUnlink_AFailedRetireKeepsTheRailNarrowed(t *testing.T) {
	d := newTestDaemon(t)

	if _, err := d.beginUnlink("never-registered"); err != nil {
		t.Fatalf("beginUnlink: %v", err)
	}

	d.finishUnlink("the receipt arrived")

	rec, err := d.store.GetUnlinkPending()
	if err != nil {
		t.Fatalf("GetUnlinkPending: %v", err)
	}
	if !rec.InFlight() {
		t.Fatal("a failed retire widened the rail")
	}
	if d.unlinkAcceptsKind("never-registered", "destroy") {
		t.Fatal("the removed device could command this one after a failed retire")
	}
}

func TestUnlink_TheNarrowingCoversEveryInboundPath(t *testing.T) {
	d := newTestDaemon(t)
	removed := addRemovablePeer(t, d, "removed0aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	other := addRemovablePeer(t, d, "other000aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	reached := false
	gated := d.unlinkGate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	paths := []string{"/pair", "/pair/return", "/files/tok-1", "/audio/tok-1", "/message"}

	for _, path := range paths {
		reached = false
		gated.ServeHTTP(httptest.NewRecorder(), onionRequest(removed.MyOnionAddr, path))
		if !reached {
			t.Fatalf("%s was refused with no unlink in flight", path)
		}
	}

	if _, err := d.beginUnlink(removed.ID); err != nil {
		t.Fatalf("beginUnlink: %v", err)
	}

	for _, path := range paths {
		reached = false
		rec := httptest.NewRecorder()
		gated.ServeHTTP(rec, onionRequest(removed.MyOnionAddr, path))
		if reached {
			t.Fatalf("%s stayed open to the device being removed", path)
		}
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s answered %d; a refusal must look like an unknown path", path, rec.Code)
		}
	}

	for _, path := range paths {
		reached = false
		gated.ServeHTTP(httptest.NewRecorder(), onionRequest(other.MyOnionAddr, path))
		if !reached {
			t.Fatalf("%s was refused for a contact the narrowing does not name", path)
		}
	}
}

func TestUnlink_AnArmedAddressTellsThatDeviceItIsGone(t *testing.T) {
	d := newTestDaemon(t)
	removed := addRemovablePeer(t, d, "removed0aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	other := addRemovablePeer(t, d, "other000aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	reached := false
	gated := d.unlinkGate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	if _, err := d.beginUnlink(removed.ID); err != nil {
		t.Fatalf("beginUnlink: %v", err)
	}
	d.armSelfErase(removed.ID, "deadbeef")

	for _, path := range []string{"/pair", "/pair/return", "/files/tok-1", "/audio/tok-1", "/message", "/health"} {
		reached = false
		rec := httptest.NewRecorder()
		gated.ServeHTTP(rec, onionRequest(removed.MyOnionAddr, path))
		if reached {
			t.Fatalf("%s stayed open to the device being removed", path)
		}
		if rec.Code != http.StatusTeapot {
			t.Fatalf("%s answered %d, want 418", path, rec.Code)
		}
		if got := rec.Header().Get(destroyProofHeader); got != "deadbeef" {
			t.Fatalf("%s answered with proof %q, want the armed one", path, got)
		}
	}

	reached = false
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, onionRequest(other.MyOnionAddr, "/message"))
	if !reached {
		t.Fatal("a contact the narrowing does not name was refused")
	}
	if got := rec.Header().Get(destroyProofHeader); got != "" {
		t.Fatalf("a contact was handed the destroy proof: %q", got)
	}
}

func TestUnlink_AnUnarmedNarrowingSaysNothing(t *testing.T) {
	d := newTestDaemon(t)
	removed := addRemovablePeer(t, d, "removed0aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	gated := d.unlinkGate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	if _, err := d.beginUnlink(removed.ID); err != nil {
		t.Fatalf("beginUnlink: %v", err)
	}

	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, onionRequest(removed.MyOnionAddr, "/message"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("an unarmed narrowing answered %d, want 404", rec.Code)
	}
	if got := rec.Header().Get(destroyProofHeader); got != "" {
		t.Fatalf("an unarmed narrowing answered with a proof: %q", got)
	}
}

func TestUnlink_TheAnswerStandsForTheWholeWindow(t *testing.T) {
	d := newTestDaemon(t)
	removed := addRemovablePeer(t, d, "removed0aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	gated := d.unlinkGate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	if _, err := d.beginUnlink(removed.ID); err != nil {
		t.Fatalf("beginUnlink: %v", err)
	}
	d.armSelfErase(removed.ID, "deadbeef")

	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		gated.ServeHTTP(rec, onionRequest(removed.MyOnionAddr, "/message"))
		if rec.Code != http.StatusTeapot {
			t.Fatalf("answer %d was %d, want 418 every time", i+1, rec.Code)
		}
	}

	d.finishUnlink("no receipt arrived before the deadline")
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, onionRequest(removed.MyOnionAddr, "/message"))
	if rec.Code == http.StatusTeapot {
		t.Fatal("the address still answered after the window closed")
	}
	pending, err := d.store.GetUnlinkPending()
	if err != nil {
		t.Fatalf("GetUnlinkPending: %v", err)
	}
	if pending.Proof != "" {
		t.Fatal("the proof outlived the window")
	}
}

func TestUnlink_ThePreviousAddressAnswersWhileItIsStillPublished(t *testing.T) {
	d := newTestDaemon(t)
	removed := addRemovablePeer(t, d, "removed0aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	prevAddr := removed.MyOnionAddr
	if _, err := d.registry.RotateOwnOnion(removed.ID,
		"rotated0aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "privkey"); err != nil {
		t.Fatalf("RotateOwnOnion: %v", err)
	}

	gated := d.unlinkGate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	if _, err := d.beginUnlink(removed.ID); err != nil {
		t.Fatalf("beginUnlink: %v", err)
	}
	d.armSelfErase(removed.ID, "deadbeef")

	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, onionRequest(prevAddr, "/message"))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("the previous address answered %d, want 418", rec.Code)
	}
}

func TestUnlink_AnExpiredPreviousAddressAnswersNobody(t *testing.T) {
	d := newTestDaemon(t)

	d.registry.CollapseGrace = time.Nanosecond
	removed := addRemovablePeer(t, d, "removed0aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	prevAddr := removed.MyOnionAddr
	if _, err := d.registry.RotateOwnOnion(removed.ID,
		"rotated0aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "privkey"); err != nil {
		t.Fatalf("RotateOwnOnion: %v", err)
	}

	gated := d.unlinkGate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	if _, err := d.beginUnlink(removed.ID); err != nil {
		t.Fatalf("beginUnlink: %v", err)
	}
	d.armSelfErase(removed.ID, "deadbeef")

	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, onionRequest(prevAddr, "/message"))
	if rec.Code == http.StatusTeapot {
		t.Fatal("an expired address still answered 418")
	}
}

func TestUnlink_AnEmptyAddressMatchesNobody(t *testing.T) {
	d := newTestDaemon(t)
	unpublished := freshTestPeer(t, "dialled0aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err := d.registry.Add(unpublished); err != nil {
		t.Fatalf("registry.Add: %v", err)
	}
	other := addRemovablePeer(t, d, "other000aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	if _, err := d.beginUnlink(unpublished.ID); err != nil {
		t.Fatalf("beginUnlink: %v", err)
	}
	d.armSelfErase(unpublished.ID, "deadbeef")

	if d.unlinkOwnsInboundHost(unpublished.ID, "") {
		t.Fatal("a request with no host matched the device being removed")
	}
	if d.unlinkOwnsInboundHost(unpublished.ID, other.MyOnionAddr+".onion") {
		t.Fatal("a contact's address matched the device being removed")
	}

	reached := false
	gated := d.unlinkGate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	gated.ServeHTTP(rec, onionRequest(other.MyOnionAddr, "/message"))
	if !reached || rec.Code == http.StatusTeapot {
		t.Fatalf("a contact was answered %d while another device was being removed", rec.Code)
	}
}

func TestUnlink_OnlyAnUndeliveredCommandArms(t *testing.T) {
	d := newTestDaemon(t)

	unreachable := freshTestPeer(t)
	unreachable.MyOnionAddr = "published0aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := d.registry.Add(unreachable); err != nil {
		t.Fatalf("registry.Add: %v", err)
	}
	d.handleUnlink(httptest.NewRecorder(), unlinkRequestFor(unreachable.ID, "deadbeef"))
	pending, err := d.store.GetUnlinkPending()
	if err != nil {
		t.Fatalf("GetUnlinkPending: %v", err)
	}
	if !pending.Armed() {
		t.Fatalf("a command with nowhere to go left no answer behind: %+v", pending)
	}
	if pending.Proof != "deadbeef" {
		t.Fatalf("armed with proof %q", pending.Proof)
	}

	d2 := newTestDaemon(t)
	plain := addRemovablePeer(t, d2, "removed1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	d2.handleUnlink(httptest.NewRecorder(), unlinkRequestFor(plain.ID, ""))
	pending, err = d2.store.GetUnlinkPending()
	if err != nil {
		t.Fatalf("GetUnlinkPending: %v", err)
	}
	if pending.Armed() {
		t.Fatalf("a plain unlink armed a self-erase: %+v", pending)
	}

	d3 := newTestDaemon(t)
	narrowed := addRemovablePeer(t, d3, "removed2aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	d3.handleUnlink(httptest.NewRecorder(), unlinkNarrowOnlyRequestFor(narrowed.ID))
	pending, err = d3.store.GetUnlinkPending()
	if err != nil {
		t.Fatalf("GetUnlinkPending: %v", err)
	}
	if !pending.InFlight() {
		t.Fatal("the first call left no narrowing")
	}
	if pending.Armed() {
		t.Fatalf("narrowing alone armed a self-erase: %+v", pending)
	}
}

func onionRequest(addr, path string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Host = addr + ".onion"
	return r
}

func TestUnlink_AFileTokenMintedForTheRemovedDeviceIsRefused(t *testing.T) {
	d := newFilesTestDaemon(t)
	removed := addRemovablePeer(t, d, "removed0aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	other := addRemovablePeer(t, d, "other000aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	tokens, err := d.files.StageBlob("msg-1", []byte("ciphertext"), []string{removed.ID, other.ID}, 0)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if len(tokens) != 2 {
		t.Fatalf("%d tokens minted, want 2", len(tokens))
	}

	reached := false
	gated := d.unlinkGate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	if _, err := d.beginUnlink(removed.ID); err != nil {
		t.Fatalf("beginUnlink: %v", err)
	}

	reached = false
	gated.ServeHTTP(httptest.NewRecorder(), onionRequest(other.MyOnionAddr, "/files/"+tokens[0]))
	if reached {
		t.Fatal("the removed device pulled its attachment through another onion")
	}

	reached = false
	gated.ServeHTTP(httptest.NewRecorder(), onionRequest(other.MyOnionAddr, "/files/"+tokens[1]))
	if !reached {
		t.Fatal("a token minted for a contact that is staying was refused")
	}
}

func TestUnlink_TheOutboxHoldsEverythingForTheRemovedDevice(t *testing.T) {
	d := newTestDaemon(t)
	removed := addRemovablePeer(t, d, "removed0aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	other := addRemovablePeer(t, d, "other000aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	removedDest := "http://" + removed.KnownAddresses[0] + ".onion"
	otherDest := "http://" + other.KnownAddresses[0] + ".onion"

	if d.unlinkRefusesDest(removedDest) {
		t.Fatal("the outbox was held with no unlink in flight")
	}

	if _, err := d.beginUnlink(removed.ID); err != nil {
		t.Fatalf("beginUnlink: %v", err)
	}
	if !d.unlinkRefusesDest(removedDest) {
		t.Fatal("the outbox kept delivering to the device being removed")
	}
	if d.unlinkRefusesDest(otherDest) {
		t.Fatal("the narrowing held the outbox for a contact it does not name")
	}
}

func TestUnlink_NothingReachesTheDeviceBeingRemoved(t *testing.T) {
	d := newTestDaemon(t)
	removed := addRemovablePeer(t, d, "removed0aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	other := addRemovablePeer(t, d, "other000aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	if d.unlinkRefusesPeer(removed.ID) {
		t.Fatal("a peer was refused with no removal in flight")
	}

	if _, err := d.beginUnlink(removed.ID); err != nil {
		t.Fatalf("beginUnlink: %v", err)
	}
	if !d.unlinkRefusesPeer(removed.ID) {
		t.Fatal("this device would still reach the one being removed")
	}

	if d.unlinkRefusesPeer(other.ID) {
		t.Fatal("the window reached a contact it does not name")
	}

	state := d.probePeerSelf(context.Background(), removed.ID, removed.MyOnionAddr, true)
	if state.Ok {
		t.Fatal("the device being removed was probed")
	}
	if state.Err != errUnlinkPeerRemoved.Error() {
		t.Fatalf("probe err = %q, want %q", state.Err, errUnlinkPeerRemoved.Error())
	}
}

func TestUnlink_TheDeadlineIsSetOnceHoweverManyCallsArrive(t *testing.T) {
	d := newTestDaemon(t)

	first := store.UnlinkPending{
		PeerID:    "removed-peer",
		Deadline:  time.Now().Add(unlinkReceiptWindow - time.Hour).Unix(),
		StartedAt: time.Now().Add(-time.Hour).Unix(),
	}
	if err := d.store.PutUnlinkPending(first); err != nil {
		t.Fatalf("PutUnlinkPending: %v", err)
	}

	for i := 0; i < 3; i++ {
		again, err := d.beginUnlink("removed-peer")
		if err != nil {
			t.Fatalf("beginUnlink again: %v", err)
		}
		if again.Deadline != first.Deadline {
			t.Fatalf("the deadline moved from %d to %d on call %d",
				first.Deadline, again.Deadline, i+2)
		}
		if again.StartedAt != first.StartedAt {
			t.Fatalf("the window restarted on call %d", i+2)
		}
	}
}

func TestUnlink_PlainAndNarrowOnlyLeaveDifferentState(t *testing.T) {
	d := newTestDaemon(t)
	plain := addRemovablePeer(t, d, "removed5aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	d.handleUnlink(httptest.NewRecorder(), unlinkRequestFor(plain.ID, ""))
	pending, err := d.store.GetUnlinkPending()
	if err != nil {
		t.Fatalf("GetUnlinkPending: %v", err)
	}
	if pending.InFlight() {
		t.Fatalf("a plain unlink left a window standing: %+v", pending)
	}

	narrowed := addRemovablePeer(t, d, "removed6aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	d.handleUnlink(httptest.NewRecorder(), unlinkNarrowOnlyRequestFor(narrowed.ID))
	pending, err = d.store.GetUnlinkPending()
	if err != nil {
		t.Fatalf("GetUnlinkPending: %v", err)
	}
	if !pending.InFlight() || pending.PeerID != narrowed.ID {
		t.Fatalf("narrow-only did not hold the window: %+v", pending)
	}
}
