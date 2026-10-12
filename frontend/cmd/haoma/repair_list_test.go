package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"haoma-frontend/internal/events"
	"haoma-frontend/internal/invitation"
	"haoma-frontend/internal/ipc"
)

func TestInvitationsList_TheWireShapeCannotCarryTheSecret(t *testing.T) {
	rec := invitation.Record{
		MsgID:          "inv-1",
		ChatID:         "aabbccddeeff00112233445566778899",
		Fingerprint:    "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		OfferingDevice: "fedcba9876543210fedcba9876543210",
		Secret:         invitationTestSecret,
		MintedAt:       1234567800,
		ExpiresAt:      1234567890,
		State:          events.InvitationLive,
	}

	entry := ipc.InvitationEntry{
		MsgID:     rec.MsgID,
		Label:     "alice",
		Direction: ipc.InvitationOfferedToMe,
		ExpiresAt: rec.ExpiresAt,
		State:     rec.State,
	}
	raw, err := json.Marshal(ipc.InvitationsListedResponse{Invitations: []ipc.InvitationEntry{entry}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), invitationTestSecret) {
		t.Fatalf("the secret reached the listed frame: %s", raw)
	}

	if strings.Contains(string(raw), rec.Fingerprint) {
		t.Fatalf("the identity fingerprint reached the listed frame: %s", raw)
	}
	if strings.Contains(string(raw), rec.OfferingDevice) {
		t.Fatalf("the offering device's derived name reached the listed frame: %s", raw)
	}

	if strings.Contains(string(raw), rec.ChatID) {
		t.Fatalf("the conversation id reached the listed frame: %s", raw)
	}
}

func TestInvitationsList_TheWireShapeCannotCarryTheHandle(t *testing.T) {
	n := invitation.Note{
		MsgID:       "inv-2",
		HandleID:    "handle-0123456789abcdef",
		Fingerprint: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		MintedAt:    1234567800,
		ExpiresAt:   1234567890,
	}
	entry := ipc.InvitationEntry{
		MsgID:     n.MsgID,
		Label:     "alice",
		Direction: ipc.InvitationOfferedByMe,
		ExpiresAt: n.ExpiresAt,
		State:     events.InvitationLive,
	}
	raw, err := json.Marshal(ipc.InvitationsListedResponse{Invitations: []ipc.InvitationEntry{entry}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), n.HandleID) {
		t.Fatalf("the rendezvous handle reached the listed frame: %s", raw)
	}

}

func TestInvitationsList_AnOwnOfferNamesItsPersonByFingerprint(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	wireInvitationStore(d)

	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)
	heldInvitation(t, d, chatID, "inv-in-1", invitationTestSecret, time.Now().Add(24*time.Hour).Unix())

	const offeredTo = "05d2a7c91e3b48f06a5c1d9e27b4803f5c61e9a0d7b3248f1e5a6c90b3d7e2f418"
	const handle = "handle-0123456789abcdef"
	if err := d.invitations.PutNote(invitation.Note{
		MsgID: "inv-out-1", HandleID: handle, Fingerprint: offeredTo,
		MintedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(7 * 24 * time.Hour).Unix(),
	}); err != nil {
		t.Fatal(err)
	}

	conn := dialTest(t, ctx, addr, certPath, token)
	req, _ := ipc.NewFrame(ipc.FrameInvitationsList, "list-1", ipc.InvitationsListRequest{})
	writeFrame(t, ctx, conn, req)
	f := readUntil(t, ctx, conn, ipc.FrameInvitationsListed)
	var out ipc.InvitationsListedResponse
	if err := json.Unmarshal(f.Payload, &out); err != nil {
		t.Fatal(err)
	}
	byID := map[string]ipc.InvitationEntry{}
	for _, e := range out.Invitations {
		byID[e.MsgID] = e
	}
	if e, ok := byID["inv-out-1"]; !ok || e.Direction != ipc.InvitationOfferedByMe {
		t.Fatalf("the offered invitation is missing or has the wrong direction: %+v", out.Invitations)
	} else if e.Fingerprint != offeredTo {
		t.Fatalf("the offered invitation names %q, want the person it was offered to (%s)", e.Fingerprint, offeredTo)
	}
	if e, ok := byID["inv-in-1"]; !ok || e.Direction != ipc.InvitationOfferedToMe {
		t.Fatalf("the received invitation is missing or has the wrong direction: %+v", out.Invitations)
	} else if e.Fingerprint != "" {
		t.Fatalf("an invitation offered to this device carries a fingerprint: %q", e.Fingerprint)
	}
	raw := string(f.Payload)
	if strings.Contains(raw, handle) {
		t.Fatalf("the rendezvous handle reached the listed frame: %s", raw)
	}
	if strings.Contains(raw, invitationTestSecret) {
		t.Fatalf("the secret reached the listed frame: %s", raw)
	}
	if strings.Contains(raw, railFingerprint) {
		t.Fatalf("the fingerprint of the person who offered to this device reached the listed frame: %s", raw)
	}
}

func row(id string, mintedAt int64, answerable bool, dir string) invitationRow {
	return invitationRow{
		entry:      ipc.InvitationEntry{MsgID: id, Direction: dir},
		mintedAt:   mintedAt,
		answerable: answerable,
	}
}

func TestInvitationsList_OrdersWhatIsAnswerableFirst(t *testing.T) {
	rows := []invitationRow{
		row("old-live", 100, true, ipc.InvitationOfferedToMe),
		row("settled-newest", 900, false, ipc.InvitationOfferedToMe),
		row("new-live", 500, true, ipc.InvitationOfferedToMe),
		row("settled-older", 200, false, ipc.InvitationOfferedToMe),
		row("retired", 400, false, ipc.InvitationOfferedToMe),
	}
	sortInvitationRows(rows)

	want := []string{"new-live", "old-live", "settled-newest", "retired", "settled-older"}
	for i, id := range want {
		if rows[i].entry.MsgID != id {
			got := make([]string, len(rows))
			for j, r := range rows {
				got[j] = r.entry.MsgID
			}
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestInvitationsList_OwnOffersNeverSortIntoTheAnswerableBand(t *testing.T) {
	rows := []invitationRow{
		row("mine-newest", 900, false, ipc.InvitationOfferedByMe),
		row("theirs-older", 100, true, ipc.InvitationOfferedToMe),
	}
	sortInvitationRows(rows)
	if rows[0].entry.MsgID != "theirs-older" {
		t.Fatalf("top row is %q, want the answerable one", rows[0].entry.MsgID)
	}
}

func TestInvitationsList_TheClockDecidesTheAnswerableBand(t *testing.T) {
	const now = int64(1_700_000_000)
	if events.InvitationAnswerable(events.InvitationLive, now-1, now) {
		t.Fatal("an invitation past its deadline is answerable")
	}
	if events.InvitationAnswerable(events.InvitationLive, now, now) {
		t.Fatal("an invitation is answerable at the instant it lapses")
	}
	if events.InvitationAnswerable(events.InvitationLive, 0, now) {
		t.Fatal("an invitation with no deadline is answerable")
	}
	if !events.InvitationAnswerable(events.InvitationLive, now+1, now) {
		t.Fatal("a standing invitation is not answerable")
	}
	if events.InvitationAnswerable(events.InvitationSuperseded, now+1, now) {
		t.Fatal("a retired invitation is answerable")
	}
}

func TestInvitationsList_TiesAreStable(t *testing.T) {
	build := func() []invitationRow {
		return []invitationRow{
			row("inv-c", 500, true, ipc.InvitationOfferedToMe),
			row("inv-a", 500, true, ipc.InvitationOfferedToMe),
			row("inv-b", 500, true, ipc.InvitationOfferedToMe),
		}
	}
	first, second := build(), build()
	sortInvitationRows(first)
	sortInvitationRows(second)
	for i := range first {
		if first[i].entry.MsgID != second[i].entry.MsgID {
			t.Fatalf("two sorts of the same list disagreed at %d: %q vs %q", i, first[i].entry.MsgID, second[i].entry.MsgID)
		}
	}
	if first[0].entry.MsgID != "inv-a" {
		t.Fatalf("tie broken to %q, want the row id to settle it", first[0].entry.MsgID)
	}
}
