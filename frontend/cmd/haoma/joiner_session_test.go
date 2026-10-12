package main

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/pair"
	"haoma-frontend/internal/presence"
	"haoma-frontend/internal/session"
	"haoma-frontend/internal/syncrail"
)

type joinerSide struct {
	d     *daemon
	stub  *haomadStub
	far   farDevice
	mine  *pair.MyKeys
	onion string
}

func newJoinerSide(t *testing.T) *joinerSide {
	t.Helper()
	const onion = "joineronionAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA5"
	stub := startHaomadStub(t, []string{onion}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	d.presenceCache = presence.New()
	inv, mine, err := pair.Build(d.signalState, d.stores, []string{onion}, "joiner", nil)
	if err != nil {
		t.Fatalf("build the joiner's invite: %v", err)
	}
	payload, err := inv.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	fp, err := inv.IdentityFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	return &joinerSide{
		d: d, stub: stub, mine: mine, onion: onion,
		far: farDevice{invite: inv, payload: payload, fingerprint: fp},
	}
}

func (j *joinerSide) accept(t *testing.T, ctx context.Context, inviterPayload []byte) (string, chat.ChatID) {
	t.Helper()
	inv, err := pair.Parse(inviterPayload)
	if err != nil {
		t.Fatalf("parse the inviter's payload: %v", err)
	}
	if err := pair.ImportResponder(ctx, j.d.stores, j.d.backendClient, inv, j.mine,
		backendapi.MintedOnion{Address: j.onion, PrivateKey: "ZmFrZQ=="}); err != nil {
		t.Fatalf("ImportResponder: %v", err)
	}
	dc, _, err := j.d.createDirectWithDefaults(inv.PeerID)
	if err != nil {
		t.Fatal(err)
	}
	return inv.PeerID, dc.ID
}

func sendsTo(sent []backendapi.SendRequest, peerID string) []backendapi.SendRequest {
	var out []backendapi.SendRequest
	for _, s := range sent {
		if s.PeerID == peerID {
			out = append(out, s)
		}
	}
	return out
}

func onlyFirstPresence(t *testing.T, sent []backendapi.SendRequest, joinerPeerID string) backendapi.SendRequest {
	t.Helper()
	toJoiner := sendsTo(sent, joinerPeerID)
	if len(toJoiner) != 1 {
		t.Fatalf("the inviter sent %d envelope(s) to the new pairing, want exactly one first presence — "+
			"with none the joiner holds no session and every send it makes to this pairing fails", len(toJoiner))
	}
	if toJoiner[0].Kind != backendapi.WireKindPresence {
		t.Fatalf("the one send to the new pairing has kind %q, want %q", toJoiner[0].Kind, backendapi.WireKindPresence)
	}
	return toJoiner[0]
}

func joinerAnswers(t *testing.T, ctx context.Context, j *joinerSide, inviterPayload []byte, first backendapi.SendRequest) {
	t.Helper()
	inviterPeerID, chatID := j.accept(t, ctx, inviterPayload)

	if _, err := j.d.cipher.Encrypt(ctx, inviterPeerID, []byte("before")); !errors.Is(err, session.ErrNoSession) {
		t.Fatalf("before the first presence the joiner's encrypt answered %v, want no session", err)
	}

	processInboxEntry(ctx, j.d, backendapi.InboxEntry{
		ArrivalAt: time.Now().UnixNano(),
		PeerID:    inviterPeerID,
		Envelope: backendapi.RawEnvelope{
			ID: "env-first-presence", Timestamp: time.Now().Unix(), From: "inviter-onion",
			Kind: first.Kind, Payload: first.Payload,
			Mac: []byte{1, 2, 3, 4},
		},
	})

	j.stub.WaitForSend(t, 1, 5*time.Second)
	if len(j.stub.SentPeerIDs) != 1 || j.stub.SentPeerIDs[0] != inviterPeerID || j.stub.SentKinds[0] != backendapi.WireKindPresence {
		t.Fatalf("after the first presence the joiner sent %v (kinds %v), want one presence to %s",
			j.stub.SentPeerIDs, j.stub.SentKinds, inviterPeerID)
	}
	if _, err := j.d.cipher.Encrypt(ctx, inviterPeerID, []byte("after")); err != nil {
		t.Fatalf("after the first presence the joiner still cannot encrypt to the pairing: %v", err)
	}

	rows, err := j.d.events.List(chatID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("the first presence wrote %d row(s) into the joiner's conversation, want none", len(rows))
	}
	rec, err := j.d.peerMeta.Get(inviterPeerID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.SendsUnlinkNoticeAt == 0 {
		t.Fatal("the first presence did not carry the unlink-notice marker an ordinary presence carries")
	}
}

func TestFirstPresence_InvitationOffer_LetsTheJoinerSend(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	j := newJoinerSide(t)
	fx := newOfferFixtureFor(t, j.far)
	fx.backend.joinerPayload = j.far.payload

	fx.d.awaitInvitationJoiner(ctx, fx.handleID, j.far.fingerprint, fx.msgID)

	first := onlyFirstPresence(t, fx.backend.snapshot().sent, j.far.invite.PeerID)
	joinerAnswers(t, ctx, j, fx.inviterPayload, first)
}

func TestFirstPresence_OnionInvite_LetsTheJoinerSend(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	j := newJoinerSide(t)
	backend := startAcceptBackend(t)
	backend.joinerPayload = j.far.payload
	_, addr, certPath, token := newTestDaemon(t, &haomadStub{URL: backend.URL})
	conn := dialTest(t, ctx, addr, certPath, token)

	req, _ := ipc.NewFrame(ipc.FramePairOnionInvite, "onion-invite-1", ipc.PairOnionInviteRequest{Nick: "me"})
	writeFrame(t, ctx, conn, req)
	readUntil(t, ctx, conn, ipc.FramePairOnionStarted)

	readUntil(t, ctx, conn, ipc.FramePairOnionCompleted)

	got := backend.snapshot()
	if len(got.invited) != 1 {
		t.Fatalf("%d rendezvous published, want one", len(got.invited))
	}
	first := onlyFirstPresence(t, got.sent, j.far.invite.PeerID)
	joinerAnswers(t, ctx, j, got.invited[0], first)
}

const passOnFreshPeerID = "0000000000000000000000000000abcd"

func passOnBody(expiresAt int64) syncrail.InvitationBody {
	return syncrail.InvitationBody{
		ID:             "inv-pass-on-1",
		Fingerprint:    railFingerprint,
		OfferingDevice: invitationDeviceA,
		Secret:         invitationTestSecret,
		MintedAt:       invitationMintedLate,
		ExpiresAt:      expiresAt,
	}
}

func firstMessageFrom(t *testing.T, ctx context.Context, d *daemon, peerID, envID string) {
	t.Helper()
	firstMessageOnOPK(t, ctx, d, peerID, envID, 0)
}

func firstMessageOnOPK(t *testing.T, ctx context.Context, d *daemon, peerID, envID string, opk int) {
	t.Helper()
	id, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	w, err := msg.BuildText(1, time.Now().Unix(), id, "hello", 0, "", "", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	blob, _ := encryptAsAliceOnOPK(t, d, peerID, w, opk)
	processInboxEntry(ctx, d, backendapi.InboxEntry{
		ArrivalAt: time.Now().UnixNano(),
		PeerID:    peerID,
		Envelope: backendapi.RawEnvelope{
			ID: envID, Timestamp: time.Now().Unix(), From: "peer-onion",
			Kind: "text", Payload: blob, Mac: []byte{1, 2, 3, 4},
		},
	})
}

func passOnsTo(stub *haomadStub, peerID string) int {
	n := 0
	for i, id := range stub.SentPeerIDs {
		if id == peerID && stub.SentKinds[i] == "" {
			n++
		}
	}
	return n
}

func TestInvitation_PassOnTakesAPairingThatHoldsASession(t *testing.T) {
	ctx := context.Background()
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)
	firstMessageFrom(t, ctx, d, railPeerID, "env-seed-1")
	stub.WaitForSend(t, 1, 5*time.Second)

	if _, _, err := d.peerMeta.SetFingerprint(passOnFreshPeerID, railFingerprint); err != nil {
		t.Fatal(err)
	}
	if err := d.chats.AttachPeerID(chatID, passOnFreshPeerID); err != nil {
		t.Fatal(err)
	}
	if _, named, err := resolveChatByFingerprint(d, railFingerprint); err != nil || named != passOnFreshPeerID {
		t.Fatalf("precondition: the resolve names %q (%v), want the pairing with no session", named, err)
	}

	forwardInvitation(ctx, d, railSiblingID, passOnBody(time.Now().Add(24*time.Hour).Unix()))

	stub.WaitForSend(t, 1, 5*time.Second)
	if got := passOnsTo(stub, railPeerID); got != 1 {
		t.Fatalf("%d pass-on(s) went through the pairing that holds a session, want one (sent to %v, kinds %q)", got, stub.SentPeerIDs, stub.SentKinds)
	}
	if got := passOnsTo(stub, passOnFreshPeerID); got != 0 {
		t.Fatalf("%d pass-on(s) went to the pairing with no session", got)
	}
	if applied, err := railMirrorApplied(d.store, "inv-pass-on-1"); err != nil || !applied {
		t.Fatalf("the pass-on was not ledgered after the send: %v %v", applied, err)
	}
}

func TestInvitation_PassOnWaitsForTheFirstSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	seedKnownPeer(t, d, passOnFreshPeerID, railFingerprint)
	body := passOnBody(time.Now().Add(24 * time.Hour).Unix())

	done := make(chan struct{})
	go func() {
		defer close(done)
		forwardInvitation(ctx, d, railSiblingID, body)
	}()
	select {
	case <-done:
		t.Fatal("the pass-on gave up on a pairing whose session has not been made yet; nothing sends it again")
	case <-stub.SendDone:
		t.Fatal("the pass-on sent before any pairing with the person held a session")
	case <-time.After(300 * time.Millisecond):
	}

	redelivered := make(chan struct{})
	go func() {
		defer close(redelivered)
		forwardInvitation(ctx, d, railSiblingID, body)
	}()
	select {
	case <-redelivered:
	case <-time.After(5 * time.Second):
		t.Fatal("a redelivery waited beside the running pass-on instead of ending")
	}

	firstMessageFrom(t, ctx, d, passOnFreshPeerID, "env-first-1")

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the pass-on did not go out after the session was made")
	}

	stub.WaitForSend(t, 2, 5*time.Second)
	if got := passOnsTo(stub, passOnFreshPeerID); got != 1 {
		t.Fatalf("%d pass-on(s) after the session was made, want exactly one (sent to %v, kinds %v)",
			got, stub.SentPeerIDs, stub.SentKinds)
	}
	if applied, err := railMirrorApplied(d.store, body.ID); err != nil || !applied {
		t.Fatalf("the pass-on was not ledgered after the send: %v %v", applied, err)
	}
}

func TestInvitation_PassOnWaitEndsAtTheDeadlineAndAtShutdown(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	seedKnownPeer(t, d, passOnFreshPeerID, railFingerprint)

	lapsed := make(chan struct{})
	go func() {
		defer close(lapsed)
		forwardInvitation(context.Background(), d, railSiblingID, passOnBody(time.Now().Add(time.Second).Unix()))
	}()
	select {
	case <-lapsed:
	case <-time.After(3 * time.Second):
		t.Fatal("the wait ran on past an invitation that lapses in a second")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		forwardInvitation(ctx, d, railSiblingID, passOnBody(time.Now().Add(24*time.Hour).Unix()))
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the wait did not end when this process stopped")
	}

	select {
	case <-stub.SendDone:
		t.Fatal("a pass-on with no session was sent")
	default:
	}
	if applied, err := railMirrorApplied(d.store, "inv-pass-on-1"); err != nil || applied {
		t.Fatalf("a pass-on that never went out was ledgered: %v %v", applied, err)
	}
}

func waitForPassOn(t *testing.T, ctx context.Context, d *daemon, stub *haomadStub, body syncrail.InvitationBody) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		forwardInvitation(ctx, d, railSiblingID, body)
	}()
	select {
	case <-done:
		t.Fatal("the pass-on ended instead of waiting for a pairing it can use")
	case <-stub.SendDone:
		t.Fatal("the pass-on sent before a pairing it can use held a session")
	case <-time.After(300 * time.Millisecond):
	}
	return done
}

func awaitDone(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal(what)
	}
}

func TestInvitation_PassOnSkipsARetiredPairing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	const retired = passOnFreshPeerID
	chatID := seedKnownPeer(t, d, retired, railFingerprint)
	firstMessageFrom(t, ctx, d, retired, "env-retired-1")
	stub.WaitForSend(t, 1, 5*time.Second)
	if _, _, err := d.peerMeta.SetFingerprint(railPeerID, railFingerprint); err != nil {
		t.Fatal(err)
	}
	if err := d.chats.AttachPeerID(chatID, railPeerID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.chats.DropRoutes(chatID, railPeerID, []string{retired}); err != nil {
		t.Fatal(err)
	}
	if _, named, err := resolveChatByFingerprint(d, railFingerprint); err != nil || named != retired {
		t.Fatalf("precondition: the resolve names %q (%v), want the retired pairing", named, err)
	}

	done := waitForPassOn(t, ctx, d, stub, passOnBody(time.Now().Add(24*time.Hour).Unix()))
	firstMessageOnOPK(t, ctx, d, railPeerID, "env-live-1", 1)
	awaitDone(t, done, "the pass-on did not go out after the live pairing had a session")

	stub.WaitForSend(t, 2, 5*time.Second)
	if got := passOnsTo(stub, retired); got != 0 {
		t.Fatalf("%d pass-on(s) went to the retired pairing", got)
	}
	if got := passOnsTo(stub, railPeerID); got != 1 {
		t.Fatalf("%d pass-on(s) went to the live pairing, want one (sent to %v, kinds %q)", got, stub.SentPeerIDs, stub.SentKinds)
	}
}

func TestInvitation_PassOnSkipsAPairingOfAnotherPerson(t *testing.T) {
	ctx := context.Background()
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)
	if err := d.chats.AttachPeerID(chatID, passOnFreshPeerID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.peerMeta.SetFingerprint(passOnFreshPeerID, "ffeeddccbbaa99887766554433221100"); err != nil {
		t.Fatal(err)
	}
	firstMessageFrom(t, ctx, d, passOnFreshPeerID, "env-other-1")
	stub.WaitForSend(t, 1, 5*time.Second)

	if got, _, _ := passOnRoute(ctx, d, chatID, railPeerID, railFingerprint, 0); got != "" {
		t.Fatalf("the route is %q, a pairing of another person", got)
	}
}

func TestInvitation_PassOnDropsForAnUnlinkedSiblingOrALapsedInvitation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	t.Run("unlinked while it waited", func(t *testing.T) {
		stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
		d, _, _, _ := newTestDaemon(t, stub)
		linkSibling(t, d, railSiblingID)
		seedKnownPeer(t, d, passOnFreshPeerID, railFingerprint)
		body := passOnBody(time.Now().Add(24 * time.Hour).Unix())

		done := waitForPassOn(t, ctx, d, stub, body)
		if _, err := d.linkedDevices.Unlink(railSiblingID, time.Now().Unix()+1); err != nil {
			t.Fatal(err)
		}
		if d.isSibling(railSiblingID) {
			t.Fatal("precondition: the sibling is still linked after Unlink")
		}
		firstMessageFrom(t, ctx, d, passOnFreshPeerID, "env-first-1")
		awaitDone(t, done, "the pass-on did not end after the session appeared")
		stub.WaitForSend(t, 1, 5*time.Second)
		if got := passOnsTo(stub, passOnFreshPeerID); got != 0 {
			t.Fatalf("%d pass-on(s) for a device that is no longer linked", got)
		}
		if applied, _ := railMirrorApplied(d.store, body.ID); applied {
			t.Fatal("a dropped pass-on was ledgered")
		}
	})

	t.Run("lapsed", func(t *testing.T) {
		stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
		d, _, _, _ := newTestDaemon(t, stub)
		linkSibling(t, d, railSiblingID)
		seedKnownPeer(t, d, railPeerID, railFingerprint)
		firstMessageFrom(t, ctx, d, railPeerID, "env-seed-1")
		stub.WaitForSend(t, 1, 5*time.Second)
		body := passOnBody(time.Now().Unix() - 1)

		forwardInvitation(ctx, d, railSiblingID, body)
		select {
		case <-stub.SendDone:
			t.Fatal("a lapsed invitation was passed on")
		case <-time.After(100 * time.Millisecond):
		}
		if applied, _ := railMirrorApplied(d.store, body.ID); applied {
			t.Fatal("a dropped pass-on was ledgered")
		}
	})
}

func TestInvitation_PassOnWakesWhenThisDeviceCompletesAPairing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, passOnFreshPeerID, railFingerprint)
	done := waitForPassOn(t, ctx, d, stub, passOnBody(time.Now().Add(24*time.Hour).Unix()))

	far := newFarDevice(t, "faronionAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA2", "them")
	_, mine, err := pair.Build(d.signalState, d.stores, []string{"our-onion"}, "me", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := pair.Import(ctx, d.stores, d.backendClient, far.invite, mine, backendapi.MintedOnion{Address: "our-onion", PrivateKey: "ZmFrZQ=="}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	newPeer := far.invite.PeerID
	if _, _, err := d.peerMeta.SetFingerprint(newPeer, railFingerprint); err != nil {
		t.Fatal(err)
	}
	if err := d.chats.AttachPeerID(chatID, newPeer); err != nil {
		t.Fatal(err)
	}
	stub.PeerList = []backendapi.Peer{{ID: passOnFreshPeerID}, {ID: newPeer}}
	pushPeerPaired(ctx, d, newPeer, "test")

	awaitDone(t, done, "the pass-on did not wake when this device completed a pairing with the person")
	stub.WaitForSend(t, 1, 5*time.Second)
	if got := passOnsTo(stub, newPeer); got != 1 {
		t.Fatalf("%d pass-on(s) through the new pairing, want one (sent to %v)", got, stub.SentPeerIDs)
	}
}

func TestInvitation_PassOnWaitsForTheFold(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := chat.ChatID("aa11bb22cc33dd44ee55ff6600778890")
	if _, err := d.chats.CreateDirectEdgeless(chatID, railFingerprint); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.peerMeta.SetFingerprint(passOnFreshPeerID, railFingerprint); err != nil {
		t.Fatal(err)
	}
	if _, named, err := resolveChatByFingerprint(d, railFingerprint); err != nil || named != "" {
		t.Fatalf("precondition: the resolve names %q (%v), want no pairing in the conversation", named, err)
	}

	done := waitForPassOn(t, ctx, d, stub, passOnBody(time.Now().Add(24*time.Hour).Unix()))
	if err := d.chats.AttachPeerID(chatID, passOnFreshPeerID); err != nil {
		t.Fatal(err)
	}
	firstMessageFrom(t, ctx, d, passOnFreshPeerID, "env-first-1")
	awaitDone(t, done, "the pass-on did not go out after the fold and the session")
	stub.WaitForSend(t, 2, 5*time.Second)
	if got := passOnsTo(stub, passOnFreshPeerID); got != 1 {
		t.Fatalf("%d pass-on(s) after the fold, want one (sent to %v, kinds %q)", got, stub.SentPeerIDs, stub.SentKinds)
	}
}

func TestInvitation_PassOnDropsWhenThisDeviceHasNoPairingWithThePerson(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	if _, err := d.chats.CreateDirectEdgeless(chat.ChatID("aa11bb22cc33dd44ee55ff6600778890"), railFingerprint); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		forwardInvitation(context.Background(), d, railSiblingID, passOnBody(time.Now().Add(24*time.Hour).Unix()))
	}()
	awaitDone(t, done, "the pass-on waited for a person this device has no pairing with")
}
