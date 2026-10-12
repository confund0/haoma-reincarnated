package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/presence"
	"haoma-frontend/internal/session"
)

const (
	dcAliceID     = "0000000000000000000000000000a11c"
	dcAliceOldID  = "0000000000000000000000000000a110"
	dcAliceFinger = "11aa22bb33cc44dd55ee66ff77889900"

	dcAliceJoinedID = "0000000000000000000000000000a11d"

	dcBobID     = "0000000000000000000000000000b0b0"
	dcBobFinger = "99887766554433221100ffeeddccbbaa"
)

func seedAlicesTwoPairings(t *testing.T, d *daemon, stub *haomadStub) {
	t.Helper()
	pairedAt := time.Now().Unix() - 3600
	seedAlicePairingAt(t, d, dcAliceID, pairedAt)
	seedKnownPeer(t, d, dcAliceID, dcAliceFinger)
	seedAlicePairingAt(t, d, dcAliceOldID, pairedAt)
	seedKnownPeer(t, d, dcBobID, dcBobFinger)
	stub.PeerList = []backendapi.Peer{
		{ID: dcAliceID, KnownAddresses: []string{"alice-new-onion"}},
		{ID: dcAliceOldID, KnownAddresses: []string{"alice-old-onion"}},
		{ID: dcBobID, KnownAddresses: []string{"bob-onion"}},
	}
}

func seedAlicePairingAt(t *testing.T, d *daemon, peerID string, pairedAt int64) {
	t.Helper()
	if _, _, err := d.peerMeta.SetFingerprintAt(peerID, dcAliceFinger, pairedAt); err != nil {
		t.Fatalf("seed the Alice pairing %s: %v", peerID, err)
	}
	got, err := d.peerMeta.PairedAt(peerID)
	if err != nil {
		t.Fatal(err)
	}
	if got != pairedAt {
		t.Fatalf("precondition: the pairing %s reads paired_at %d, want %d", peerID, got, pairedAt)
	}
}

func assertRetiredExactly(t *testing.T, stub *haomadStub, sender string, want ...string) {
	t.Helper()
	got := map[string]int{}
	for _, id := range stub.PeerActionIDs {
		got[id]++
		if id == sender {
			t.Errorf("the sender %s was retired: %v. It is the device that stays", sender, stub.PeerActionIDs)
		}
	}
	if len(stub.PeerActionIDs) != len(want) {
		t.Fatalf("retired %v, want exactly %v", stub.PeerActionIDs, want)
	}
	for _, id := range want {
		if got[id] != 1 {
			t.Fatalf("retired %v, want exactly %v", stub.PeerActionIDs, want)
		}
	}
	if len(want) > 0 && stub.LastPeerActionReq != "retire" {
		t.Errorf("action = %q, want retire", stub.LastPeerActionReq)
	}
}

func TestDeviceCount_IsSiblingsPlusOne(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)

	if got := d.deviceCount(); got != 0 {
		t.Errorf("with no linked-device store the count = %d, want 0 (say nothing)", got)
	}
	linkSibling(t, d, railSiblingID)
	if got := d.deviceCount(); got != 2 {
		t.Errorf("with one sibling the count = %d, want 2", got)
	}
	if _, err := d.linkedDevices.Unlink(railSiblingID, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if got := d.deviceCount(); got != 1 {
		t.Errorf("after the sibling is dropped the count = %d, want 1", got)
	}
}

func TestUnlinkNotice_PushTellsEveryContactButTheRemovedDevice(t *testing.T) {
	const removedID = "0000000000000000000000000000dead"
	const retiredID = "0000000000000000000000000000d1ed"

	peers := []backendapi.Peer{
		{ID: dcAliceID},
		{ID: removedID},
		{ID: railSiblingID},
		{ID: dcBobID},
		{ID: retiredID, RetiredAt: 1_750_000_000},
	}
	got := contactsToTell(peers, []string{railSiblingID}, removedID)

	want := []string{dcAliceID, dcBobID}
	if len(got) != len(want) {
		t.Fatalf("addressed %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("addressed %v, want %v", got, want)
		}
	}
}

func aliceHoldingASession(t *testing.T, ctx context.Context, d *daemon) *session.Cipher {
	t.Helper()
	msgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	inbound, err := msg.BuildText(7, time.Now().Unix(), msgID, "hello", 0, "", "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	blob, aliceCipher := encryptAsAliceKeepingCipher(t, d, dcAliceID, inbound)
	processInboxEntry(ctx, d, railEntry(dcAliceID, "env-dc-body-1", blob))
	return aliceCipher
}

func openPresenceAsAlice(t *testing.T, ctx context.Context, aliceCipher *session.Cipher, payload []byte) *msg.PresenceBody {
	t.Helper()
	plain, err := aliceCipher.Decrypt(ctx, "bob-self", payload)
	if err != nil {
		t.Fatalf("alice could not open the send: %v", err)
	}
	wrapper, err := msg.Unmarshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	if wrapper.Kind != msg.KindPresence {
		t.Fatalf("the send is kind %q, want presence", wrapper.Kind)
	}
	body, err := wrapper.Presence()
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestUnlinkNotice_PushCarriesTheNoticeAndACountOfOne(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)

	linkSibling(t, d, railSiblingID)
	unlinkedAt := time.Now().Unix() - 5
	if _, err := d.linkedDevices.Unlink(railSiblingID, unlinkedAt); err != nil {
		t.Fatal(err)
	}
	seedKnownPeer(t, d, dcAliceID, dcAliceFinger)
	seedKnownPeer(t, d, dcBobID, dcBobFinger)
	stub.PeerList = []backendapi.Peer{
		{ID: dcAliceID, KnownAddresses: []string{"alice-onion"}},
		{ID: dcBobID, KnownAddresses: []string{"bob-onion"}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	aliceCipher := aliceHoldingASession(t, ctx, d)

	before := len(stub.SentPayloads)
	pushUnlinkNotice(ctx, d, railSiblingID, unlinkedAt)

	sent := stub.SentPayloads[before:]
	if len(sent) != 1 {
		t.Fatalf("the push produced %d sends, want exactly 1 (Alice). Bob has no session and must not stop it", len(sent))
	}
	body := openPresenceAsAlice(t, ctx, aliceCipher, sent[0])
	if body.UnlinkedAt != unlinkedAt {
		t.Errorf("the body carries unlinked_at %d, want %d. The contact acts on this, not on the log line", body.UnlinkedAt, unlinkedAt)
	}
	if body.DeviceCount != 1 {
		t.Errorf("the body carries device_count %d, want 1. A contact on an older build reads only this", body.DeviceCount)
	}
}

func TestUnlinkNotice_PresenceOnChatOpen_CarriesNoNotice(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	if _, err := d.linkedDevices.Unlink(railSiblingID, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	seedKnownPeer(t, d, dcAliceID, dcAliceFinger)
	stub.PeerList = []backendapi.Peer{{ID: dcAliceID, KnownAddresses: []string{"alice-onion"}}}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	aliceCipher := aliceHoldingASession(t, ctx, d)

	before := len(stub.SentPayloads)
	if err := d.shipPresenceToDevice(ctx, dcAliceID, msg.PresenceAvailable); err != nil {
		t.Fatal(err)
	}
	sent := stub.SentPayloads[before:]
	if len(sent) != 1 {
		t.Fatalf("the presence produced %d sends, want 1", len(sent))
	}
	body := openPresenceAsAlice(t, ctx, aliceCipher, sent[0])
	if body.UnlinkedAt != 0 {
		t.Errorf("a presence on chat open carries unlinked_at %d; only the push at unlink may", body.UnlinkedAt)
	}

	if body.DeviceCount != 1 {
		t.Errorf("the presence carries device_count %d, want 1", body.DeviceCount)
	}
}

func seedTwoContactsWithSessions(t *testing.T, ctx context.Context, d *daemon, stub *haomadStub) map[string]*session.Cipher {
	t.Helper()
	linkASibling(t, d)
	seedKnownPeer(t, d, dcAliceID, dcAliceFinger)
	seedKnownPeer(t, d, dcBobID, dcBobFinger)
	stub.PeerList = []backendapi.Peer{
		{ID: dcAliceID, KnownAddresses: []string{"alice-onion"}},
		{ID: dcBobID, KnownAddresses: []string{"bob-onion"}},
		{ID: railSiblingID, KnownAddresses: []string{"sibling-onion"}},
	}
	ciphers := map[string]*session.Cipher{}
	for i, peerID := range []string{dcAliceID, dcBobID} {
		msgID, err := msg.NewID()
		if err != nil {
			t.Fatal(err)
		}
		inbound, err := msg.BuildText(7, time.Now().Unix(), msgID, "hello", 0, "", "", nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		blob, c := encryptAsAliceOnOPK(t, d, peerID, inbound, i)
		processInboxEntry(ctx, d, railEntry(peerID, "env-un-session-"+peerID, blob))
		ciphers[peerID] = c
	}
	return ciphers
}

func assertEveryContactGotTheNotice(t *testing.T, ctx context.Context, d *daemon, stub *haomadStub, ciphers map[string]*session.Cipher, before int) {
	t.Helper()
	set, err := d.linkedDevices.Get()
	if err != nil {
		t.Fatal(err)
	}
	written := set.Removed[railSiblingID]
	if written == 0 {
		t.Fatal("precondition: the linked-device set holds no unlink time for the sibling")
	}
	got := map[string]bool{}
	for i, payload := range stub.SentPayloads[before:] {
		peerID := stub.SentPeerIDs[before+i]
		c, held := ciphers[peerID]
		if !held {
			t.Fatalf("the unlink sent to %s, which is not a contact this test holds", peerID)
		}
		body := openPresenceAsAlice(t, ctx, c, payload)
		if body.UnlinkedAt != written {
			t.Errorf("%s got unlinked_at %d, want %d, the time forgetSibling wrote", peerID, body.UnlinkedAt, written)
		}
		if body.DeviceCount != 1 {
			t.Errorf("%s got device_count %d, want 1", peerID, body.DeviceCount)
		}
		got[peerID] = true
	}
	if !got[dcAliceID] || !got[dcBobID] {
		t.Fatalf("the notice reached %v, want Alice and Bob", got)
	}
}

func TestUnlink_SendsEveryContactTheNoticeOfTheTimeItWrote(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ciphers := seedTwoContactsWithSessions(t, ctx, d, stub)

	before := len(stub.SentPayloads)
	out, err := unlinkDevice(ctx, d, ipc.UnlinkDeviceRequest{PeerID: railSiblingID})
	if err != nil || !out.Unlinked {
		t.Fatalf("unlinkDevice: %+v (%v)", out, err)
	}
	assertEveryContactGotTheNotice(t, ctx, d, stub, ciphers, before)
}

func TestUnlink_ClientGoneMidUnlink_StillTellsEveryContact(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	setup, cancelSetup := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelSetup()
	ciphers := seedTwoContactsWithSessions(t, setup, d, stub)
	stub.DestroyPINGuardSet = true

	req, cancelReq := context.WithCancel(context.Background())
	defer cancelReq()
	stub.OnUnlink = cancelReq

	before := len(stub.SentPayloads)
	out, err := unlinkDevice(req, d, ipc.UnlinkDeviceRequest{PeerID: railSiblingID})
	if err != nil || !out.Unlinked {
		t.Fatalf("unlinkDevice: %+v (%v)", out, err)
	}
	if req.Err() == nil {
		t.Fatal("precondition: the request was never cancelled, so this proves nothing")
	}
	assertEveryContactGotTheNotice(t, setup, d, stub, ciphers, before)
	if stub.DestroyPINGuardSet {
		t.Error("the guard was not cleared after the client went away")
	}
}

type aliceSession struct {
	d        *daemon
	peerID   string
	opkIndex int
	cipher   *session.Cipher
	sent     int

	olderBuild bool
}

func newAliceSession(t *testing.T, d *daemon, peerID string, opkIndex int) *aliceSession {
	t.Helper()
	if d.presenceCache == nil {
		d.presenceCache = presence.New()
	}
	return &aliceSession{d: d, peerID: peerID, opkIndex: opkIndex}
}

func (a *aliceSession) nextState() string {
	states := []string{msg.PresenceAvailable, msg.PresenceAway, msg.PresenceBusy}
	state := states[a.sent%len(states)]
	a.sent++
	return state
}

func (a *aliceSession) assertRead(t *testing.T, envID, state string) {
	t.Helper()
	if got := a.d.presenceCache.Snapshot(a.peerID).Chatty; got != state {
		t.Fatalf("%s was not decrypted and read: presence is %q, want %q. The assertions below would pass on a message nothing opened", envID, got, state)
	}
}

func (a *aliceSession) sendText(t *testing.T, ctx context.Context, envID string, seq uint64, sealedAt int64, count int) {
	t.Helper()
	state := a.nextState()
	msgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	wrapper, err := msg.BuildText(seq, sealedAt, msgID, "a message", 0, state, "", nil, count)
	if err != nil {
		t.Fatal(err)
	}
	a.deliver(t, ctx, envID, wrapper)
	a.assertRead(t, envID, state)
}

func (a *aliceSession) presence(t *testing.T, seq uint64, sealedAt int64, count int, unlinkedAt int64) (*msg.Wrapper, string) {
	t.Helper()
	state := a.nextState()
	msgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	wrapper, err := msg.BuildPresence(seq, sealedAt, msgID, state, 0, count, unlinkedAt)
	if err != nil {
		t.Fatal(err)
	}
	return wrapper, state
}

func (a *aliceSession) sendPresence(t *testing.T, ctx context.Context, envID string, seq uint64, sealedAt int64, count int, unlinkedAt int64) {
	t.Helper()
	wrapper, state := a.presence(t, seq, sealedAt, count, unlinkedAt)
	a.deliver(t, ctx, envID, wrapper)
	a.assertRead(t, envID, state)
}

func (a *aliceSession) deliver(t *testing.T, ctx context.Context, envID string, wrapper *msg.Wrapper) {
	t.Helper()
	if a.olderBuild {
		wrapper = withoutMarker(t, wrapper)
	}
	var blob []byte
	if a.cipher == nil {
		blob, a.cipher = encryptAsAliceOnOPK(t, a.d, a.peerID, wrapper, a.opkIndex)
	} else {
		plaintext, err := msg.Marshal(wrapper)
		if err != nil {
			t.Fatal(err)
		}
		var err2 error
		blob, err2 = a.cipher.Encrypt(ctx, "bob-self", plaintext)
		if err2 != nil {
			t.Fatal(err2)
		}
	}
	processInboxEntry(ctx, a.d, railEntry(a.peerID, envID, blob))
}

func withoutMarker(t *testing.T, wrapper *msg.Wrapper) *msg.Wrapper {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(wrapper.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["sends_unlink_notice"] != true {
		t.Fatalf("precondition: the builder did not set the marker: %s", wrapper.Body)
	}
	delete(body, "sends_unlink_notice")
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	out := *wrapper
	out.Body = raw
	return &out
}

func TestUnlinkNotice_RetiresThePersonsOtherPairing(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	seedAlicesTwoPairings(t, d, stub)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Now().Unix()
	alice := newAliceSession(t, d, dcAliceID, 0)
	alice.sendPresence(t, ctx, "env-un-1", 7, now, 1, now)

	assertRetiredExactly(t, stub, dcAliceID, dcAliceOldID)
}

func TestUnlinkNotice_NeverRetiresTheSender(t *testing.T) {
	const dcAliceOlderID = "0000000000000000000000000000a10f"
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	seedAlicesTwoPairings(t, d, stub)
	seedAlicePairingAt(t, d, dcAliceOlderID, time.Now().Unix()-7200)
	stub.PeerList = append(stub.PeerList, backendapi.Peer{ID: dcAliceOlderID, KnownAddresses: []string{"alice-older-onion"}})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Now().Unix()
	alice := newAliceSession(t, d, dcAliceID, 0)
	alice.sendPresence(t, ctx, "env-un-sender-1", 7, now, 1, now)

	assertRetiredExactly(t, stub, dcAliceID, dcAliceOldID, dcAliceOlderID)
	if stub.PeerList[0].ID != dcAliceID || stub.PeerList[0].RetiredAt != 0 {
		t.Errorf("the sender's registry row is %+v; it must stay live", stub.PeerList[0])
	}
}

func TestDeviceCount_MarkedSender_OneOnAText_RetiresNothing(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	seedAlicesTwoPairings(t, d, stub)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	alice := newAliceSession(t, d, dcAliceID, 0)
	alice.sendText(t, ctx, "env-dc-text-1", 7, time.Now().Unix(), 1)

	if len(stub.PeerActionIDs) != 0 {
		t.Errorf("a count of 1 on a text retired %v; only the unlink notice retires", stub.PeerActionIDs)
	}
}

func TestDeviceCount_MarkedSender_OneOnAPlainPresence_RetiresNothing(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	seedAlicesTwoPairings(t, d, stub)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	alice := newAliceSession(t, d, dcAliceID, 0)
	alice.sendPresence(t, ctx, "env-dc-pres-1", 7, time.Now().Unix(), 1, 0)

	if len(stub.PeerActionIDs) != 0 {
		t.Errorf("a count of 1 on a plain presence retired %v; only the unlink notice retires", stub.PeerActionIDs)
	}
}

func TestDeviceCount_Absent_RetiresNothing(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	seedAlicesTwoPairings(t, d, stub)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	alice := newAliceSession(t, d, dcAliceID, 0)
	alice.sendText(t, ctx, "env-dc-none-1", 7, time.Now().Unix(), 0)
	alice.sendPresence(t, ctx, "env-dc-none-2", 8, time.Now().Unix(), 0, 0)

	if len(stub.PeerActionIDs) != 0 {
		t.Errorf("a message with no count and no notice retired %v", stub.PeerActionIDs)
	}
}

func TestUnlinkNotice_BesideACountThatIsNotOne_RetiresNothing(t *testing.T) {
	for _, count := range []int{0, 2} {
		stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
		d, _, _, _ := newTestDaemon(t, stub)
		seedAlicesTwoPairings(t, d, stub)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		now := time.Now().Unix()
		alice := newAliceSession(t, d, dcAliceID, 0)
		alice.sendPresence(t, ctx, "env-un-count", 7, now, count, now)
		cancel()

		if len(stub.PeerActionIDs) != 0 {
			t.Errorf("a notice beside a count of %d retired %v", count, stub.PeerActionIDs)
		}
	}
}

func TestUnlinkNotice_OnTheRail_IsNeverActedOn(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	d.presenceCache = presence.New()
	linkSibling(t, d, railSiblingID)
	seedAlicesTwoPairings(t, d, stub)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Now().Unix()
	msgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	wrapper, err := msg.BuildPresence(4, now, msgID, msg.PresenceAvailable, 0, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	processInboxEntry(ctx, d, railEntry(railSiblingID, "env-un-rail-1", sealMirror(t, d, dcAliceFinger, wrapper)))

	if len(stub.PeerActionIDs) != 0 {
		t.Errorf("a notice on the rail retired %v; the rail carries my own notice and must never be acted on", stub.PeerActionIDs)
	}
}

func TestUnlinkNotice_Redelivered_IsANoOp(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	seedAlicesTwoPairings(t, d, stub)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Now().Unix()
	alice := newAliceSession(t, d, dcAliceID, 0)

	wrapper, _ := alice.presence(t, 7, now, 1, now)
	alice.deliver(t, ctx, "env-un-redeliver-1", wrapper)
	assertRetiredExactly(t, stub, dcAliceID, dcAliceOldID)

	alice.deliver(t, ctx, "env-un-redeliver-2", wrapper)
	if len(stub.PeerActionIDs) != 1 {
		t.Errorf("the redelivery called retire again: %v", stub.PeerActionIDs)
	}
}

func TestUnlinkNotice_OnARetiredPairing_RetiresNothing(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	seedAlicesTwoPairings(t, d, stub)
	stub.PeerList[0].RetiredAt = time.Now().Unix() - 10

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Now().Unix()
	alice := newAliceSession(t, d, dcAliceID, 0)
	alice.sendPresence(t, ctx, "env-un-retired-1", 7, now, 1, now)

	if len(stub.PeerActionIDs) != 0 {
		t.Errorf("a notice on a retired pairing retired %v", stub.PeerActionIDs)
	}
}

func TestDeviceCount_OlderSender_OneOnAText_Retires(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	seedAlicesTwoPairings(t, d, stub)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	alice := newAliceSession(t, d, dcAliceID, 0)
	alice.olderBuild = true
	alice.sendText(t, ctx, "env-old-text-1", 7, time.Now().Unix(), 1)

	assertRetiredExactly(t, stub, dcAliceID, dcAliceOldID)
	if marked, err := d.peerMeta.SendsUnlinkNotice(dcAliceID); err != nil || marked {
		t.Errorf("precondition: the older sender's pairing reads marked %v (%v)", marked, err)
	}
}

func TestDeviceCount_OlderSender_OneOnAPresence_Retires(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	seedAlicesTwoPairings(t, d, stub)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	alice := newAliceSession(t, d, dcAliceID, 0)
	alice.olderBuild = true
	alice.sendPresence(t, ctx, "env-old-pres-1", 7, time.Now().Unix(), 1, 0)

	assertRetiredExactly(t, stub, dcAliceID, dcAliceOldID)
}

func TestDeviceCount_OlderSender_PairingMadeAfterTheSeal_IsKept(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	seedAlicesTwoPairings(t, d, stub)
	now := time.Now().Unix()
	seedAlicePairingAt(t, d, dcAliceJoinedID, now)
	stub.PeerList = append(stub.PeerList, backendapi.Peer{ID: dcAliceJoinedID, KnownAddresses: []string{"alice-joined-onion"}})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	alice := newAliceSession(t, d, dcAliceID, 0)
	alice.olderBuild = true
	alice.sendText(t, ctx, "env-old-guard-1", 7, now-30, 1)

	assertRetiredExactly(t, stub, dcAliceID, dcAliceOldID)
}

func TestDeviceCount_OlderSender_SealedBeforeANewerCount_RetiresNothing(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	seedAlicesTwoPairings(t, d, stub)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Now().Unix()
	alice := newAliceSession(t, d, dcAliceID, 0)
	alice.olderBuild = true
	alice.sendText(t, ctx, "env-old-late-1", 8, now, 2)
	alice.sendText(t, ctx, "env-old-late-2", 7, now-120, 1)

	if len(stub.PeerActionIDs) != 0 {
		t.Errorf("a count sealed before a newer one retired %v", stub.PeerActionIDs)
	}
}

func TestDeviceCount_MarkedOnce_ThenAnUnmarkedCount_RetiresNothing(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	seedAlicesTwoPairings(t, d, stub)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Now().Unix()
	alice := newAliceSession(t, d, dcAliceID, 0)
	alice.sendText(t, ctx, "env-mark-1", 7, now, 0)
	if marked, err := d.peerMeta.SendsUnlinkNotice(dcAliceID); err != nil || !marked {
		t.Fatalf("precondition: the marked message left the pairing unmarked (%v, %v)", marked, err)
	}
	alice.olderBuild = true
	alice.sendText(t, ctx, "env-mark-2", 8, now, 1)

	if len(stub.PeerActionIDs) != 0 {
		t.Errorf("a count from a pairing that has shown the marker retired %v", stub.PeerActionIDs)
	}
}

func TestDeviceCount_MarkerIsPerPairing(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	seedAlicesTwoPairings(t, d, stub)
	if _, err := d.peerMeta.MarkSendsUnlinkNotice(dcAliceOldID, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	alice := newAliceSession(t, d, dcAliceID, 0)
	alice.olderBuild = true
	alice.sendText(t, ctx, "env-perpair-1", 7, time.Now().Unix(), 1)

	assertRetiredExactly(t, stub, dcAliceID, dcAliceOldID)
}

func TestUnlinkNotice_OlderThanOneAlreadyRead_RetiresNothing(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	now := time.Now().Unix()
	seedAlicePairingAt(t, d, dcAliceID, now-3600)
	seedKnownPeer(t, d, dcAliceID, dcAliceFinger)
	stub.PeerList = []backendapi.Peer{{ID: dcAliceID, KnownAddresses: []string{"alice-onion"}}}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	alice := newAliceSession(t, d, dcAliceID, 0)
	alice.sendPresence(t, ctx, "env-un-late-1", 7, now, 1, now)
	if len(stub.PeerActionIDs) != 0 {
		t.Fatalf("precondition: with one pairing the notice retired %v", stub.PeerActionIDs)
	}

	seedAlicePairingAt(t, d, dcAliceOldID, now-3600)
	stub.PeerList = append(stub.PeerList, backendapi.Peer{ID: dcAliceOldID, KnownAddresses: []string{"alice-old-onion"}})
	alice.sendPresence(t, ctx, "env-un-late-2", 8, now, 1, now-120)

	if len(stub.PeerActionIDs) != 0 {
		t.Errorf("a notice of an unlink older than one already read retired %v", stub.PeerActionIDs)
	}
}

func TestUnlinkNotice_AfterAnOlderNotice_StillRetires(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	now := time.Now().Unix()
	seedAlicePairingAt(t, d, dcAliceID, now-3600)
	seedKnownPeer(t, d, dcAliceID, dcAliceFinger)
	stub.PeerList = []backendapi.Peer{{ID: dcAliceID, KnownAddresses: []string{"alice-onion"}}}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	alice := newAliceSession(t, d, dcAliceID, 0)
	alice.sendPresence(t, ctx, "env-un-order-1", 7, now-120, 1, now-120)

	seedAlicePairingAt(t, d, dcAliceOldID, now-60)
	stub.PeerList = append(stub.PeerList, backendapi.Peer{ID: dcAliceOldID, KnownAddresses: []string{"alice-old-onion"}})
	alice.sendPresence(t, ctx, "env-un-order-2", 8, now, 1, now)

	assertRetiredExactly(t, stub, dcAliceID, dcAliceOldID)
}

func TestUnlinkNotice_AheadOfThisClock_IsReadAsNow(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	seedAlicesTwoPairings(t, d, stub)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := time.Now().Unix()
	alice := newAliceSession(t, d, dcAliceID, 0)
	alice.sendPresence(t, ctx, "env-un-ahead-1", 7, now, 1, now+3600)
	assertRetiredExactly(t, stub, dcAliceID, dcAliceOldID)

	fresh, err := d.peerMeta.ObserveUnlinkNotice(dcAliceFinger, now+60)
	if err != nil {
		t.Fatal(err)
	}
	if !fresh {
		t.Error("a notice a minute from now was refused: the watermark was left an hour in the future")
	}
}

func TestUnlinkNotice_PairingMadeAfterTheUnlink_IsKept(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	seedAlicesTwoPairings(t, d, stub)
	now := time.Now().Unix()
	seedAlicePairingAt(t, d, dcAliceJoinedID, now)
	stub.PeerList = append(stub.PeerList, backendapi.Peer{ID: dcAliceJoinedID, KnownAddresses: []string{"alice-joined-onion"}})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	alice := newAliceSession(t, d, dcAliceID, 0)
	alice.sendPresence(t, ctx, "env-un-joined-1", 7, now, 1, now-30)

	assertRetiredExactly(t, stub, dcAliceID, dcAliceOldID)
}

func TestUnlinkNotice_PairingMadeBeforeTheUnlink_IsRetired(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	now := time.Now().Unix()
	seedAlicePairingAt(t, d, dcAliceID, now-3600)
	seedKnownPeer(t, d, dcAliceID, dcAliceFinger)
	seedAlicePairingAt(t, d, dcAliceJoinedID, now-1)
	stub.PeerList = []backendapi.Peer{
		{ID: dcAliceID, KnownAddresses: []string{"alice-onion"}},
		{ID: dcAliceJoinedID, KnownAddresses: []string{"alice-joined-onion"}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	alice := newAliceSession(t, d, dcAliceID, 0)
	alice.sendPresence(t, ctx, "env-un-joined-2", 7, now, 1, now)

	assertRetiredExactly(t, stub, dcAliceID, dcAliceJoinedID)
}

func TestUnlinkNotice_PairingMadeInTheSecondOfTheUnlink_IsKept(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	seedAlicesTwoPairings(t, d, stub)
	now := time.Now().Unix()
	seedAlicePairingAt(t, d, dcAliceJoinedID, now)
	stub.PeerList = append(stub.PeerList, backendapi.Peer{ID: dcAliceJoinedID, KnownAddresses: []string{"alice-joined-onion"}})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	alice := newAliceSession(t, d, dcAliceID, 0)
	alice.sendPresence(t, ctx, "env-un-equal-1", 7, now, 1, now)

	assertRetiredExactly(t, stub, dcAliceID, dcAliceOldID)
}

func TestUnlinkNotice_GuardMeasuresTheUnlinkTimeNotTheSeal(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	seedAlicesTwoPairings(t, d, stub)
	now := time.Now().Unix()
	seedAlicePairingAt(t, d, dcAliceJoinedID, now-30)
	stub.PeerList = append(stub.PeerList, backendapi.Peer{ID: dcAliceJoinedID, KnownAddresses: []string{"alice-joined-onion"}})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	alice := newAliceSession(t, d, dcAliceID, 0)
	alice.sendPresence(t, ctx, "env-un-seal-1", 7, now, 1, now-60)

	assertRetiredExactly(t, stub, dcAliceID, dcAliceOldID)
}

func TestUnlinkNotice_PairingWithNoStamp_IsRetiredAsBefore(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	now := time.Now().Unix()
	seedAlicePairingAt(t, d, dcAliceID, now-3600)
	seedKnownPeer(t, d, dcAliceID, dcAliceFinger)
	seedAlicePairingAt(t, d, dcAliceOldID, 0)
	stub.PeerList = []backendapi.Peer{
		{ID: dcAliceID, KnownAddresses: []string{"alice-onion"}},
		{ID: dcAliceOldID, KnownAddresses: []string{"alice-old-onion"}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	alice := newAliceSession(t, d, dcAliceID, 0)
	alice.sendPresence(t, ctx, "env-un-nostamp-1", 7, now, 1, now-86400)

	assertRetiredExactly(t, stub, dcAliceID, dcAliceOldID)
}

func TestPairingAgeOutcome(t *testing.T) {
	const unlinkedAt = 1_790_000_000
	cases := []struct {
		name     string
		pairedAt int64
		readErr  error
		want     string
	}{
		{"a read that fails keeps the pairing", unlinkedAt - 3600, errors.New("store closed"), outcomeKeptUnreadable},
		{"a read that fails keeps even a record with no stamp", 0, errors.New("store closed"), outcomeKeptUnreadable},
		{"made after the unlink is kept", unlinkedAt + 1, nil, outcomeKeptNewer},
		{"made in the second of the unlink is kept", unlinkedAt, nil, outcomeKeptNewer},
		{"made before the unlink may be retired", unlinkedAt - 1, nil, ""},
		{"no stamp may be retired", 0, nil, ""},
	}
	for _, c := range cases {
		if got := pairingAgeOutcome(c.pairedAt, c.readErr, unlinkedAt); got != c.want {
			t.Errorf("%s: outcome %q, want %q", c.name, got, c.want)
		}
	}
}
