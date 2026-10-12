package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/linkeddevice"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/syncrail"
)

const (
	railSiblingID = "0000000000000000000000000000d0d0"

	railPeerID = "0000000000000000000000000000beef"

	railFingerprint = "aa11bb22cc33dd44ee55ff6600778899"
)

func linkSibling(t *testing.T, d *daemon, peerID string) {
	t.Helper()
	d.linkedDevices = linkeddevice.New(d.store)
	if _, err := d.linkedDevices.Link(peerID, time.Now().Unix()); err != nil {
		t.Fatalf("link sibling %q: %v", peerID, err)
	}
	if !d.isSibling(peerID) {
		t.Fatalf("precondition: %q not reported as a sibling after Link", peerID)
	}
}

func seedKnownPeer(t *testing.T, d *daemon, peerID, fingerprint string) chat.ChatID {
	t.Helper()
	if _, _, err := d.peerMeta.SetFingerprint(peerID, fingerprint); err != nil {
		t.Fatalf("seed fingerprint for %q: %v", peerID, err)
	}
	dc, err := d.chats.CreateDirect(peerID)
	if err != nil {
		t.Fatalf("create direct chat for %q: %v", peerID, err)
	}
	return dc.ID
}

func railText(t *testing.T, text string, seq uint64, ts int64, deviceCount int) *msg.Wrapper {
	t.Helper()
	id, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	w, err := msg.BuildText(seq, ts, id, text, 0, "", "", nil, deviceCount)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func railFileOffer(t *testing.T, seq uint64, ts int64) *msg.Wrapper {
	t.Helper()
	id, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	w, err := msg.BuildFileOffer(seq, ts, id, "tok-1", "/blob/1", "photo.jpg", 1024, "image/jpeg", "deadbeef", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func sealMirror(t *testing.T, d *daemon, fingerprint string, wrapper *msg.Wrapper) []byte {
	t.Helper()
	plain, err := msg.Marshal(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(syncrail.MirrorBody{
		Fingerprint:  fingerprint,
		Wrapper:      plain,
		WireKind:     string(wrapper.Kind),
		MsgID:        wrapper.MsgID,
		DisplayTs:    wrapper.Ts,
		OriginChatID: "chat-on-the-other-device",
		OriginPeerID: "ffffffffffffffffffffffffffffffff",
	})
	if err != nil {
		t.Fatal(err)
	}
	return sealRailEnvelope(t, d, syncrail.Envelope{
		V:    syncrail.Version,
		Kind: syncrail.KindMirror,
		Ts:   wrapper.Ts,
		Body: body,
	})
}

func sealRailEnvelope(t *testing.T, d *daemon, env syncrail.Envelope) []byte {
	t.Helper()
	key, err := d.railKey()
	if err != nil {
		t.Fatalf("railKey: %v", err)
	}
	sealed, err := syncrail.SealEnvelope(key, env)
	if err != nil {
		t.Fatalf("SealEnvelope: %v", err)
	}
	return sealed
}

func railEntry(peerID, envelopeID string, payload []byte) backendapi.InboxEntry {
	return backendapi.InboxEntry{
		ArrivalAt: time.Now().UnixNano(),
		PeerID:    peerID,
		Envelope: backendapi.RawEnvelope{
			ID:        envelopeID,
			Timestamp: time.Now().Unix(),
			From:      "sibling-onion",
			Payload:   payload,
			Mac:       []byte{9, 9, 9, 9},
		},
	}
}

func chatExistsForPeer(d *daemon, peerID string) bool {
	_, err := d.chats.GetByDirectPeer(peerID)
	return err == nil
}

func TestRail_MirroredText_AppliesAsOutboundRowResolvedByFingerprint(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	wrapper := railText(t, "sent from my phone", 4, time.Now().Unix(), 0)
	processInboxEntry(ctx, d, railEntry(railSiblingID, "env-rail-1", sealMirror(t, d, railFingerprint, wrapper)))

	rows, err := d.events.List(chatID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("persisted %d rows in the fingerprint-resolved chat, want 1", len(rows))
	}
	ev := rows[0]
	if ev.Direction != events.DirOut {
		t.Errorf("direction = %q, want %q — a mirror is a message I sent, from my other device", ev.Direction, events.DirOut)
	}
	if ev.MsgID != wrapper.MsgID {
		t.Errorf("msg_id = %q, want %q (the end-to-end id both devices agree on)", ev.MsgID, wrapper.MsgID)
	}
	if ev.Kind != events.KindText {
		t.Errorf("kind = %q, want %q", ev.Kind, events.KindText)
	}
	var body events.TextBody
	if err := json.Unmarshal(ev.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Text != "sent from my phone" {
		t.Errorf("body.text = %q, want the mirrored text", body.Text)
	}

	if chatExistsForPeer(d, railSiblingID) {
		t.Error("a chat was created for the sibling peerID; a sibling is SELF, never a contact")
	}

	tl := readUntil(t, ctx, conn, ipc.FrameTimelineEvent)
	var p ipc.TimelineEventPayload
	if err := json.Unmarshal(tl.Payload, &p); err != nil {
		t.Fatal(err)
	}
	var pushed events.Event
	if err := json.Unmarshal(p.Event, &pushed); err != nil {
		t.Fatal(err)
	}
	if pushed.ChatID != chatID || pushed.Direction != events.DirOut || pushed.MsgID != wrapper.MsgID {
		t.Errorf("pushed timeline event = %+v, want the outbound mirrored row in %s", pushed, chatID)
	}
}

func TestRail_MirroredText_KeepsTheOriginDisplayPosition(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	origin := time.Now().Unix() - 600
	wrapper := railText(t, "typed on my other device ten minutes ago", 4, origin, 0)
	processInboxEntry(ctx, d, railEntry(railSiblingID, "env-rail-origin", sealMirror(t, d, railFingerprint, wrapper)))

	rows, err := d.events.List(chatID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("persisted %d rows, want 1", len(rows))
	}
	if got := rows[0].DisplayTs; got != origin {
		t.Errorf("display_ts = %d, want %d — the mirror landed at this device's clock "+
			"instead of the position the sending device gave it, so the same message "+
			"sits at two different points in the conversation on the two devices",
			got, origin)
	}
}

func TestRail_MirroredText_SortsByWhenItWasSentNotWhenItArrived(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	now := time.Now().Unix()

	peerTs := now - 60
	blob, _ := makeInboundCiphertext(t, d, railPeerID, "from the peer, sent second", 9, peerTs, 0)
	processInboxEntry(ctx, d, backendapi.InboxEntry{
		ArrivalAt: time.Now().UnixNano(),
		PeerID:    railPeerID,
		Envelope: backendapi.RawEnvelope{
			ID: "env-peer", Timestamp: peerTs, From: "peer-onion",
			Kind: "text", Payload: blob, Mac: []byte{1, 2, 3, 4},
		},
	})

	mineWrapper := railText(t, "from my other device, sent first", 4, now-300, 0)
	processInboxEntry(ctx, d, railEntry(railSiblingID, "env-mine", sealMirror(t, d, railFingerprint, mineWrapper)))

	rows, err := d.events.List(chatID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("persisted %d rows, want 2", len(rows))
	}
	if rows[0].MsgID != mineWrapper.MsgID {
		t.Errorf("first row is %q, want the one sent first (%q) — the conversation is "+
			"ordered by arrival, so a device that was away reads its own history "+
			"in the wrong order",
			rows[0].MsgID, mineWrapper.MsgID)
	}
}

func TestRail_MirroredText_AppliedTwiceIsNoop(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)

	ctx := context.Background()
	sealed := sealMirror(t, d, railFingerprint, railText(t, "exactly once", 4, time.Now().Unix(), 0))

	processInboxEntry(ctx, d, railEntry(railSiblingID, "env-rail-dup", sealed))
	after1, err := d.events.List(chatID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(after1) != 1 {
		t.Fatalf("first apply persisted %d rows, want 1", len(after1))
	}

	processInboxEntry(ctx, d, railEntry(railSiblingID, "env-rail-dup", sealed))
	after2, err := d.events.List(chatID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(after2) != 1 {
		t.Fatalf("second apply persisted %d rows total, want 1 (must be a no-op)", len(after2))
	}
	if after2[0].RecvSeq != after1[0].RecvSeq {
		t.Errorf("row was rewritten on replay (recv_seq %d → %d)", after1[0].RecvSeq, after2[0].RecvSeq)
	}
}

func TestRail_MirroredText_DoesNotSeedTheBottomStep(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	wrapper := railText(t, "the peer read this hours ago", 7, time.Now().Unix(), 0)
	processInboxEntry(ctx, d, railEntry(railSiblingID, "env-rail-floor", sealMirror(t, d, railFingerprint, wrapper)))

	rows, err := d.events.List(chatID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("persisted %d rows, want 1", len(rows))
	}
	if rows[0].DeliveryState != events.DeliverySiblingFloor {
		t.Errorf("stored delivery_state = %q, want the sibling floor %q", rows[0].DeliveryState, events.DeliverySiblingFloor)
	}

	tl := readUntil(t, ctx, conn, ipc.FrameTimelineEvent)
	var p ipc.TimelineEventPayload
	if err := json.Unmarshal(tl.Payload, &p); err != nil {
		t.Fatal(err)
	}
	var pushed events.Event
	if err := json.Unmarshal(p.Event, &pushed); err != nil {
		t.Fatal(err)
	}
	if pushed.DeliveryState != events.DeliverySiblingFloor {
		t.Errorf("pushed delivery_state = %q, want the sibling floor %q", pushed.DeliveryState, events.DeliverySiblingFloor)
	}
}

func TestRail_MirroredFileOffer_DoesNotSeedTheBottomStep(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	wrapper := railFileOffer(t, 7, time.Now().Unix())
	processInboxEntry(ctx, d, railEntry(railSiblingID, "env-rail-file-floor", sealMirror(t, d, railFingerprint, wrapper)))

	rows, err := d.events.List(chatID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("persisted %d rows, want 1", len(rows))
	}
	if rows[0].Kind != events.KindFile {
		t.Fatalf("kind = %q, want %q", rows[0].Kind, events.KindFile)
	}
	if rows[0].DeliveryState != events.DeliverySiblingFloor {
		t.Errorf("stored delivery_state = %q, want the sibling floor %q", rows[0].DeliveryState, events.DeliverySiblingFloor)
	}

	tl := readUntil(t, ctx, conn, ipc.FrameTimelineEvent)
	var p ipc.TimelineEventPayload
	if err := json.Unmarshal(tl.Payload, &p); err != nil {
		t.Fatal(err)
	}
	var pushed events.Event
	if err := json.Unmarshal(p.Event, &pushed); err != nil {
		t.Fatal(err)
	}
	if pushed.DeliveryState != events.DeliverySiblingFloor {
		t.Errorf("pushed delivery_state = %q, want the sibling floor %q", pushed.DeliveryState, events.DeliverySiblingFloor)
	}
}

func TestRail_MirroredEdit_AppliesToTheOutboundRowAndIsReplayProof(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)

	ctx := context.Background()
	original := railText(t, "frist", 4, time.Now().Unix(), 0)
	processInboxEntry(ctx, d, railEntry(railSiblingID, "env-rail-t", sealMirror(t, d, railFingerprint, original)))

	editID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	edit, err := msg.BuildEdit(5, time.Now().Unix(), editID, original.MsgID, "first", 0)
	if err != nil {
		t.Fatal(err)
	}
	processInboxEntry(ctx, d, railEntry(railSiblingID, "env-rail-e", sealMirror(t, d, railFingerprint, edit)))

	ev, err := d.events.GetByMsgID(original.MsgID)
	if err != nil {
		t.Fatal(err)
	}
	var body events.TextBody
	if err := json.Unmarshal(ev.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Text != "first" {
		t.Errorf("body.text = %q after mirrored edit, want %q", body.Text, "first")
	}
	if ev.EditedAt == 0 {
		t.Error("EditedAt not stamped — the mirrored edit did not go through ApplyEdit")
	}

	processInboxEntry(ctx, d, railEntry(railSiblingID, "env-rail-e", sealMirror(t, d, railFingerprint, edit)))
	rows, err := d.events.List(chatID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("chat holds %d rows after an edit replay, want 1", len(rows))
	}
}

func TestRail_UnknownFingerprint_DropsWithoutCreatingAChat(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)

	knownChat := seedKnownPeer(t, d, railPeerID, railFingerprint)

	ctx := context.Background()
	processInboxEntry(ctx, d, railEntry(railSiblingID, "env-rail-x",
		sealMirror(t, d, "ffffffffffffffffffffffffffffffff", railText(t, "who?", 1, time.Now().Unix(), 0))))

	chats, err := d.chats.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 1 {
		t.Fatalf("chat count = %d, want 1 — the rail must not force-create a conversation", len(chats))
	}
	rows, err := d.events.List(knownChat, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("the unknown-fingerprint mirror landed in the known chat (%d rows)", len(rows))
	}
	if seq, err := d.events.PeekNextRecvSeq(); err == nil && seq != 1 {
		t.Errorf("an event row was written for an unroutable mirror (next recv_seq = %d)", seq)
	}
}

func TestRail_NonSiblingPeerMessage_StillTakesTheLibsignalPath(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)

	const aliceID = "0000000000000000000000000000aaaa"
	const senderTs int64 = 1742643890
	blob, _ := makeInboundCiphertext(t, d, aliceID, "hello from a real peer", 7, senderTs, 0)

	processInboxEntry(context.Background(), d, backendapi.InboxEntry{
		ArrivalAt: time.Now().UnixNano(),
		PeerID:    aliceID,
		Envelope: backendapi.RawEnvelope{
			ID: "env-peer-1", Timestamp: senderTs, From: "alice-onion",
			Kind: "text", Payload: blob, Mac: []byte{1, 2, 3, 4},
		},
	})

	rows, err := d.events.List(chatIDForPeer(t, d, aliceID), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("peer message persisted %d rows, want 1", len(rows))
	}
	ev := rows[0]
	if ev.Direction != events.DirIn {
		t.Errorf("direction = %q, want %q — peer traffic is inbound", ev.Direction, events.DirIn)
	}
	if ev.DecryptStatus != events.DecryptOK {
		t.Errorf("decrypt_status = %q, want ok", ev.DecryptStatus)
	}
	if ev.SenderTs != senderTs || ev.EnvelopeID != "env-peer-1" {
		t.Errorf("peer row drift: %+v", ev)
	}
	var body events.TextBody
	if err := json.Unmarshal(ev.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Text != "hello from a real peer" {
		t.Errorf("body.text = %q, want the peer's text", body.Text)
	}
}

func TestRail_TamperedSiblingCiphertext_FailsClosed(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	seedKnownPeer(t, d, railPeerID, railFingerprint)

	sealed := sealMirror(t, d, railFingerprint, railText(t, "tamper me", 4, time.Now().Unix(), 0))

	tampered := append([]byte(nil), sealed...)
	tampered[syncrail.NonceSize+1] ^= 0x40

	processInboxEntry(context.Background(), d, railEntry(railSiblingID, "env-rail-bad", tampered))

	if seq, err := d.events.PeekNextRecvSeq(); err == nil && seq != 1 {
		t.Errorf("an event row was written for a tampered rail envelope (next recv_seq = %d)", seq)
	}
	if chatExistsForPeer(d, railSiblingID) {
		t.Error("a chat was created for the sibling — the tampered envelope fell through to the peer path")
	}
}

func TestRail_UnknownRailKind_DropsCleanly(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	seedKnownPeer(t, d, railPeerID, railFingerprint)

	sealed := sealRailEnvelope(t, d, syncrail.Envelope{
		V:    syncrail.Version,
		Kind: syncrail.Kind("contacts_snapshot_from_the_future"),
		Ts:   time.Now().Unix(),
		Body: json.RawMessage(`{"whatever":1}`),
	})
	processInboxEntry(context.Background(), d, railEntry(railSiblingID, "env-rail-future", sealed))

	if seq, err := d.events.PeekNextRecvSeq(); err == nil && seq != 1 {
		t.Errorf("an event row was written for an unknown rail kind (next recv_seq = %d)", seq)
	}
	if chatExistsForPeer(d, railSiblingID) {
		t.Error("a chat was created for the sibling — the unknown-kind envelope fell through to the peer path")
	}
}

func TestRail_MirroredFileHandshakeStep_IsANoop(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)

	id, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := msg.BuildFileReceipt(9, time.Now().Unix(), id, "tok-abc", 0)
	if err != nil {
		t.Fatal(err)
	}
	processInboxEntry(context.Background(), d, railEntry(railSiblingID, "env-rail-fr",
		sealMirror(t, d, railFingerprint, receipt)))

	rows, err := d.events.List(chatID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("a per-device file handshake step wrote %d rows; want 0", len(rows))
	}
}

func TestRail_FingerprintLookup_PicksTheRightChatAmongPeers(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)

	const otherPeerID = "0000000000000000000000000000cafe"
	const otherFingerprint = "1122334455667788990011223344556677"
	wantChat := seedKnownPeer(t, d, railPeerID, railFingerprint)
	otherChat := seedKnownPeer(t, d, otherPeerID, otherFingerprint)

	got, err := d.peerMeta.PeerIDsByFingerprint(railFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != railPeerID {
		t.Fatalf("PeerIDsByFingerprint = %v, want [%s]", got, railPeerID)
	}

	processInboxEntry(context.Background(), d, railEntry(railSiblingID, "env-rail-pick",
		sealMirror(t, d, railFingerprint, railText(t, "for the right person", 2, time.Now().Unix(), 0))))

	want, err := d.events.List(wantChat, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) != 1 {
		t.Errorf("target chat holds %d rows, want 1", len(want))
	}
	other, err := d.events.List(otherChat, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 0 {
		t.Errorf("the other peer's chat holds %d rows, want 0", len(other))
	}
}

func TestIsSibling_UnreadableSetIsTreatedAsPeerTraffic(t *testing.T) {
	d := inheritedTestDaemon(t)
	d.linkedDevices = unreadableLinkedDevices(t)

	if _, err := d.readIsSibling(railSiblingID); err == nil {
		t.Fatal("the fixture reads fine, so nothing is broken to test")
	}
	if d.isSibling(railSiblingID) {
		t.Error("a sibling check that failed answered true — the envelope would take the rail")
	}
	if got := d.siblings(); got != nil {
		t.Errorf("the fan-out target list on a failed read = %v, want none", got)
	}
}
