package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/presence"
)

func TestProcessInboxEntry_AVerifiedEnvelopeTurnsTheDownArrowOn(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	d.presenceCache = presence.New()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const aliceID = "0000000000000000000000000000aaaa"
	const senderTs int64 = 1742643890
	blob, _ := makeInboundCiphertext(t, d, aliceID, "hello bob", 7, senderTs, 0)

	if d.presenceCache.Snapshot(aliceID).Sending {
		t.Fatal("precondition: the down arrow is on before any envelope")
	}
	processInboxEntry(ctx, d, backendapi.InboxEntry{
		ArrivalAt: time.Now().UnixNano(),
		PeerID:    aliceID,
		Envelope: backendapi.RawEnvelope{
			ID: "env-sending-1", Timestamp: senderTs, From: "alice-onion",
			Kind: "text", Payload: blob, Mac: []byte{1, 2, 3, 4},
		},
	})
	snap := d.presenceCache.Snapshot(aliceID)
	if !snap.Sending {
		t.Fatalf("after a verified text from Alice the down arrow is off: %+v", snap)
	}
	if snap.Accepting {
		t.Errorf("an envelope from Alice turned the up arrow on: %+v", snap)
	}
}

func TestProcessInboxEntry_AnEnvelopeThatDoesNotDecryptLeavesTheDownArrowOff(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	d.presenceCache = presence.New()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const malloryID = "0000000000000000000000000000bbbb"
	processInboxEntry(ctx, d, backendapi.InboxEntry{
		ArrivalAt: time.Now().UnixNano(),
		PeerID:    malloryID,
		Envelope: backendapi.RawEnvelope{
			ID: "env-sending-garbage", Timestamp: time.Now().Unix(), From: "mallory-onion",
			Kind: "text", Payload: []byte("not a libsignal message"), Mac: []byte{1, 2, 3, 4},
		},
	})
	if snap := d.presenceCache.Snapshot(malloryID); snap.Sending {
		t.Fatalf("an envelope that did not decrypt turned the down arrow on: %+v", snap)
	}
}

func TestRail_AnOpenedEnvelopeTurnsTheLinkedDeviceDownArrowOn(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	d.presenceCache = presence.New()
	linkSibling(t, d, railSiblingID)
	contactChat := seedKnownPeer(t, d, railPeerID, railFingerprint)

	stale := railEntry(railSiblingID, "env-rail-stale", sealMirror(t, d, railFingerprint, railText(t, "old", 2, time.Now().Unix(), 0)))
	stale.ArrivalAt = time.Now().Add(-d.presenceCache.Staleness() - time.Minute).UnixNano()
	processInboxEntry(context.Background(), d, stale)
	if snap := d.presenceCache.Snapshot(railSiblingID); snap.Sending {
		t.Fatalf("a backlog rail envelope turned the down arrow on: %+v", snap)
	}

	processInboxEntry(context.Background(), d, railEntry(railSiblingID, "env-rail-garbage", []byte("sealed by nobody")))
	if snap := d.presenceCache.Snapshot(railSiblingID); snap.Sending {
		t.Fatalf("a rail envelope that did not open turned the down arrow on: %+v", snap)
	}

	w := railText(t, "mirrored", 3, time.Now().Unix(), 0)
	processInboxEntry(context.Background(), d, railEntry(railSiblingID, "env-rail-sending", sealMirror(t, d, railFingerprint, w)))
	if snap := d.presenceCache.Snapshot(railSiblingID); !snap.Sending {
		t.Fatalf("after an opened rail envelope the linked device's down arrow is off: %+v", snap)
	}

	if snap := d.presenceCache.Snapshot(railPeerID); snap.Sending {
		t.Errorf("a mirror turned the contact's down arrow on: %+v", snap)
	}

	if snap, ok := d.chatPresence(mustDirectChat(t, d, contactChat)); !ok || snap.Sending {
		t.Errorf("a mirror lit the contact's chat: %+v (has presence %v)", snap, ok)
	}
}

func TestProcessInboxEntry_ABacklogEnvelopeLightsNothing(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	d.presenceCache = presence.New()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const staleID = "0000000000000000000000000000aaaa"
	const freshID = "0000000000000000000000000000cccc"
	busyText := func() *msg.Wrapper {
		id, err := msg.NewID()
		if err != nil {
			t.Fatal(err)
		}
		w, err := msg.BuildText(1, time.Now().Unix(), id, "hello", 0, "busy", "", nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	staleBlob, _ := encryptAsAliceOnOPK(t, d, staleID, busyText(), 0)
	freshBlob, _ := encryptAsAliceOnOPK(t, d, freshID, busyText(), 1)

	old := time.Now().Add(-d.presenceCache.Staleness() - time.Minute).UnixNano()
	entry := func(peerID, envID string, arrival int64, blob []byte) backendapi.InboxEntry {
		return backendapi.InboxEntry{
			ArrivalAt: arrival,
			PeerID:    peerID,
			Envelope: backendapi.RawEnvelope{
				ID: envID, Timestamp: time.Now().Unix(), From: "onion",
				Kind: "text", Payload: blob, Mac: []byte{1, 2, 3, 4},
			},
		}
	}
	processInboxEntry(ctx, d, entry(staleID, "env-stale", old, staleBlob))
	processInboxEntry(ctx, d, entry(freshID, "env-fresh", time.Now().UnixNano(), freshBlob))

	if snap := d.presenceCache.Snapshot(staleID); snap.Sending || snap.Chatty != "" {
		t.Errorf("a backlog envelope lit the presence: %+v", snap)
	}
	if snap := d.presenceCache.Snapshot(freshID); !snap.Sending || snap.Chatty != "busy" {
		t.Errorf("a fresh envelope did not light the down arrow and busy: %+v", snap)
	}

	processInboxEntry(ctx, d, entry(staleID, "env-stale-again", time.Now().UnixNano(), staleBlob))
	if snap := d.presenceCache.Snapshot(staleID); snap.Sending || snap.Chatty != "" {
		t.Errorf("a redelivered envelope lit the presence: %+v", snap)
	}
}

func TestArrivedInsideTTL(t *testing.T) {
	c := presence.New()
	now := time.Now()
	cases := []struct {
		name    string
		arrival int64
		want    bool
	}{
		{"just now", now.UnixNano(), true},
		{"one second inside", now.Add(-c.Staleness() + time.Second).UnixNano(), true},
		{"one second past", now.Add(-c.Staleness() - time.Second).UnixNano(), false},
		{"no arrival time", 0, false},
	}
	for _, tc := range cases {
		if got := arrivedInsideTTL(c, backendapi.InboxEntry{ArrivalAt: tc.arrival}, now); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestPushPresenceChanges_TheFramesCarryTheDownArrow(t *testing.T) {
	d, clk, aliceChat, _ := personDaemon(t)
	next := presencePushTap(t, d)
	waitUntil(t, func() bool { return d.ipcSrv.SessionCount() == 1 }, "the test client was never registered")
	startPushLoop(t, d)

	expectPushes := func(step string, want ...presencePush) {
		t.Helper()
		left := map[presencePush]int{}
		for _, w := range want {
			left[w]++
		}
		for range want {
			got := next()
			if left[got] == 0 {
				t.Fatalf("%s: pushed %+v, want the frames %+v", step, got, want)
			}
			left[got]--
		}
	}

	phoneSends := clk.armedBy(t, func() { d.presenceCache.ObserveSending(alicePhoneID) })
	expectPushes("the phone sends",
		presencePush{ipc.FramePresenceChanged, alicePhoneID, false, true, "", presence.EffectiveUnknown},
		presencePush{ipc.FrameChatPresenceChanged, string(aliceChat), false, true, "", presence.EffectiveUnknown})

	d.presenceCache.ObserveHuman(alicePCID, presence.EffectiveBusy)
	expectPushes("busy on the desktop",
		presencePush{ipc.FramePresenceChanged, alicePCID, false, false, "busy", "busy"},
		presencePush{ipc.FrameChatPresenceChanged, string(aliceChat), false, true, "busy", "busy"})

	phoneSends.fire()
	expectPushes("the phone's down arrow expires",
		presencePush{ipc.FramePresenceChanged, alicePhoneID, false, false, "", presence.EffectiveUnknown},
		presencePush{ipc.FrameChatPresenceChanged, string(aliceChat), false, false, "busy", "busy"})
}

func TestPresenceSnapshots_RowsCarryTheDownArrow(t *testing.T) {
	d, _, aliceChat, _ := personDaemon(t)
	d.presenceCache.ObserveSending(alicePhoneID)

	var pc, phone ipc.PeerEntry
	applyPresenceSnapshot(d, alicePCID, &pc)
	applyPresenceSnapshot(d, alicePhoneID, &phone)
	if pc.Sending || !phone.Sending {
		t.Errorf("peer rows: desktop sending=%v, phone sending=%v; want false, true", pc.Sending, phone.Sending)
	}
	entry := ipc.ChatEntry{ChatID: string(aliceChat), Kind: ipc.ChatKindDirect, PeerID: alicePCID}
	applyChatPresenceSnapshot(d, &entry, mustDirectChat(t, d, aliceChat))
	if !entry.Sending {
		t.Errorf("Alice's chat row has the down arrow off; the phone sent")
	}
}
