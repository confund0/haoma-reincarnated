package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/peerstate"
)

func pairedPeer(t *testing.T, d *daemon, peerID, fingerprint, nick string) {
	t.Helper()
	if nick != "" {
		if _, err := d.peerMeta.SetNick(peerID, nick, 0); err != nil {
			t.Fatal(err)
		}
	}
	if fingerprint == "" {
		return
	}
	if _, _, err := d.peerMeta.SetFingerprint(peerID, fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.createDirectWithDefaults(peerID); err != nil {
		t.Fatal(err)
	}
}

func TestPushPeerPaired_ANewContactIsReadyToCross(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)

	c := newCollector()
	d.contactsPush = newContactsPusher(c.send)
	d.contactsPush.timing = contactsPushTiming{debounce: 20 * time.Millisecond, ceiling: time.Second}

	pairedPeer(t, d, railPeerID, railFingerprint, "alice")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pushPeerPaired(ctx, d, railPeerID, "tor-accept")

	rec, err := d.peerMeta.GetPerson(railFingerprint)
	if err != nil {
		t.Fatalf("the new contact is not on record as a person: %v", err)
	}
	if rec.Nick != "alice" {
		t.Errorf("nick on the person record = %q, want alice", rec.Nick)
	}

	batch := c.await(t, 2*time.Second)
	if len(batch) != 1 || batch[0] != railFingerprint {
		t.Fatalf("batch = %v, want [%s]", batch, railFingerprint)
	}

	body := contactsPushBody(d, batch)
	if len(body.Contacts) != 1 {
		t.Fatalf("the envelope carries %d contacts, want 1", len(body.Contacts))
	}
	got := body.Contacts[0]
	if got.Fingerprint != railFingerprint {
		t.Errorf("fingerprint = %q, want %q", got.Fingerprint, railFingerprint)
	}
	if got.Nick != "alice" {
		t.Errorf("nick = %q, want alice", got.Nick)
	}
}

func TestPushPeerPaired_MidPairingWritesNobody(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)

	c := newCollector()
	d.contactsPush = newContactsPusher(c.send)
	d.contactsPush.timing = contactsPushTiming{debounce: 20 * time.Millisecond, ceiling: time.Second}

	pairedPeer(t, d, railPeerID, "", "alice")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pushPeerPaired(ctx, d, railPeerID, "tor-accept")

	time.Sleep(80 * time.Millisecond)
	if n := c.count(); n != 0 {
		t.Errorf("%d pushes, want 0 — a pairing with no shared identifier marks nobody", n)
	}
	persons, err := d.peerMeta.Persons()
	if err != nil {
		t.Fatal(err)
	}
	if len(persons) != 0 {
		t.Errorf("%d people on record, want 0", len(persons))
	}
}

func TestNotePairingCompleted_TheJoinersLateFingerprintStillCrosses(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)

	c := newCollector()
	d.contactsPush = newContactsPusher(c.send)
	d.contactsPush.timing = contactsPushTiming{debounce: 20 * time.Millisecond, ceiling: time.Second}

	pairedPeer(t, d, railPeerID, "", "alice")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pushPeerPaired(ctx, d, railPeerID, "tor-accept")

	if _, _, err := d.peerMeta.SetFingerprint(railPeerID, railFingerprint); err != nil {
		t.Fatal(err)
	}
	notePairingCompleted(d, railPeerID, "joiner-first-decrypt")

	rec, err := d.peerMeta.GetPerson(railFingerprint)
	if err != nil {
		t.Fatalf("the joiner's contact is not on record as a person: %v", err)
	}
	if rec.Nick != "alice" {
		t.Errorf("nick on the person record = %q, want alice", rec.Nick)
	}
	batch := c.await(t, 2*time.Second)
	body := contactsPushBody(d, batch)
	if len(body.Contacts) != 1 || body.Contacts[0].Nick != "alice" {
		t.Fatalf("the envelope carries %+v, want one contact named alice", body.Contacts)
	}
}

func TestPushPeerPaired_DoesNotSendASiblingsNameBackOlder(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)

	c := newCollector()
	d.contactsPush = newContactsPusher(c.send)
	d.contactsPush.timing = contactsPushTiming{debounce: 20 * time.Millisecond, ceiling: time.Second}

	if _, err := d.peerMeta.MergePerson(railFingerprint, peerstate.MetaRecord{
		Nick: "alice-renamed", NickAt: 5000,
	}); err != nil {
		t.Fatal(err)
	}
	pairedPeer(t, d, railPeerID, railFingerprint, "alice")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pushPeerPaired(ctx, d, railPeerID, "tor-accept")

	batch := c.await(t, 2*time.Second)
	body := contactsPushBody(d, batch)
	if len(body.Contacts) != 1 {
		t.Fatalf("the envelope carries %d contacts, want 1", len(body.Contacts))
	}
	got := body.Contacts[0]
	if got.Nick != "alice-renamed" || got.NickAt != 5000 {
		t.Errorf("the envelope carries %q@%d, want alice-renamed@5000 — the pair-time seed overwrote a newer name",
			got.Nick, got.NickAt)
	}
}

func TestContactsPush_TheOtherDeviceDrawsTheNewContactWithNoSweep(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)

	a, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, a, railSiblingID)
	pairedPeer(t, a, railPeerID, railFingerprint, "alice")

	b, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, b, railSiblingID)
	if got := inheritedContactsOf(t, b); len(got) != 0 {
		t.Fatalf("precondition: the other device already lists %d contact(s)", len(got))
	}

	body := contactsPushBody(a, []string{railFingerprint})
	if len(body.Contacts) != 1 {
		t.Fatalf("the envelope carries %d contacts, want 1", len(body.Contacts))
	}
	if len(body.Chats) != 1 {
		t.Fatalf("the envelope carries %d conversations, want 1 — the other device cannot draw a person with no conversation",
			len(body.Chats))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	handleContactsPush(ctx, b, railSiblingID, body)

	got := inheritedContactsOf(t, b)
	if len(got) != 1 {
		t.Fatalf("the other device lists %d contacts after the push, want 1 — it still waits for a sweep", len(got))
	}
	if got[0].Fingerprint != railFingerprint {
		t.Errorf("fingerprint = %q, want %q", got[0].Fingerprint, railFingerprint)
	}
	if got[0].Nick != "alice" {
		t.Errorf("nick = %q, want alice", got[0].Nick)
	}

	if _, _, reason, detail, ok := pairingOfferPlan(context.Background(), b, railFingerprint, false); !ok {
		t.Errorf("the offer refuses a row the pane draws: reason=%q detail=%q", reason, detail)
	}
}

func TestContactsPushBody_SendsTheNameThisDeviceShows(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	pairedPeer(t, d, railPeerID, railFingerprint, "alice")
	notePairingCompleted(d, railPeerID, "tor-accept")

	if _, err := d.peerMeta.SetNick(railPeerID, "alice-new", 9000); err != nil {
		t.Fatal(err)
	}
	stale, err := d.peerMeta.GetPerson(railFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if stale.Nick != "alice" {
		t.Fatalf("precondition: the person record holds %q, so this test is not measuring the fold", stale.Nick)
	}

	body := contactsPushBody(d, []string{railFingerprint})
	if len(body.Contacts) != 1 {
		t.Fatalf("the envelope carries %d contacts, want 1", len(body.Contacts))
	}
	got := body.Contacts[0]
	if got.Nick != "alice-new" || got.NickAt != 9000 {
		t.Errorf("the envelope carries %q@%d, want alice-new@9000 — the push and the screen disagree",
			got.Nick, got.NickAt)
	}
}

func TestNotePairingCompleted_ANamelessPairingWritesNoRecord(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	pairedPeer(t, d, railPeerID, railFingerprint, "")

	notePairingCompleted(d, railPeerID, "tor-accept")

	if _, err := d.peerMeta.GetPerson(railFingerprint); err == nil {
		t.Error("a person record was written carrying a fingerprint and nothing else")
	}

	body := contactsPushBody(d, []string{railFingerprint})
	if len(body.Contacts) != 1 || len(body.Chats) != 1 {
		t.Fatalf("the envelope carries %d contacts and %d conversations, want 1 and 1",
			len(body.Contacts), len(body.Chats))
	}

	b, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, b, railSiblingID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	handleContactsPush(ctx, b, railSiblingID, body)

	got := inheritedContactsOf(t, b)
	if len(got) != 1 {
		t.Fatalf("the other device lists %d contacts, want 1 — a contact with no name still has to be drawn", len(got))
	}
	if got[0].Nick != "" || got[0].Alias != "" {
		t.Errorf("nick=%q alias=%q, want both empty", got[0].Nick, got[0].Alias)
	}

	if want := peerstate.ShortID(railFingerprint); got[0].Label != want {
		t.Errorf("label = %q, want %q", got[0].Label, want)
	}
	if _, _, reason, detail, ok := pairingOfferPlan(context.Background(), b, railFingerprint, false); !ok {
		t.Errorf("the offer refuses a row the pane draws: reason=%q detail=%q", reason, detail)
	}
}

func TestNotePairingCompleted_ASiblingIsNotAContact(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)

	c := newCollector()
	d.contactsPush = newContactsPusher(c.send)
	d.contactsPush.timing = contactsPushTiming{debounce: 20 * time.Millisecond, ceiling: time.Second}

	if _, err := d.peerMeta.SetNick(railSiblingID, "my-phone", 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.peerMeta.SetFingerprint(railSiblingID, railFingerprint); err != nil {
		t.Fatal(err)
	}

	notePairingCompleted(d, railSiblingID, "joiner-first-decrypt")

	if _, err := d.peerMeta.GetPerson(railFingerprint); err == nil {
		t.Error("one of my own devices was written to the address book as a person")
	}
	time.Sleep(80 * time.Millisecond)
	if n := c.count(); n != 0 {
		t.Errorf("%d pushes, want 0 — one of my own devices was described to my other devices as a contact", n)
	}

	body := contactsPushBody(d, []string{railFingerprint})
	if len(body.Contacts) != 0 {
		t.Errorf("the envelope carries %d contacts, want 0", len(body.Contacts))
	}
}

func TestContactsPush_LeavesTheOtherDevicesChatSettingsAlone(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)

	a, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, a, railSiblingID)
	pairedPeer(t, a, railPeerID, railFingerprint, "alice")
	aChat, found := chatForPerson(a, railFingerprint)
	if !found {
		t.Fatal("precondition: the pairing left no conversation")
	}
	const fourWeeks = 28 * 24 * 60 * 60
	if err := a.chats.SetRetentionTTL(aChat.ID, fourWeeks); err != nil {
		t.Fatal(err)
	}

	b, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, b, railSiblingID)
	const bChatID = "0000000000000000000000000000b0b0"
	if _, err := b.chats.CreateDirectEdgeless(chat.ChatID(bChatID), railFingerprint); err != nil {
		t.Fatal(err)
	}
	bChat, found := chatForPerson(b, railFingerprint)
	if !found {
		t.Fatal("precondition: the other device has no conversation for this person")
	}
	const oneDay = 24 * 60 * 60
	chosenAt := time.Now().Unix() - 7*24*60*60
	if err := b.chats.SetRetentionAndTimerTs(bChat.ID, oneDay, chosenAt); err != nil {
		t.Fatal(err)
	}
	if moved, err := b.chats.MergeSettings(bChat.ID, chat.SettingsUpdate{
		Receipts: true,
		At:       chat.SettingsStamps{Receipts: chosenAt},
	}); err != nil {
		t.Fatal(err)
	} else if len(moved) == 0 {
		t.Fatal("precondition: receipts-off did not take, so this test measures nothing")
	}

	body := contactsPushBody(a, []string{railFingerprint})
	if len(body.Chats) != 1 {
		t.Fatalf("the envelope carries %d conversations, want 1", len(body.Chats))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	handleContactsPush(ctx, b, railSiblingID, body)

	after, err := b.chats.Get(bChat.ID)
	if err != nil {
		t.Fatal(err)
	}
	dc, ok := after.(*chat.DirectChat)
	if !ok {
		t.Fatalf("the conversation came back as %T", after)
	}
	if dc.RetentionTTL != oneDay {
		t.Errorf("retention = %d, want %d — the push replaced a choice the operator made", dc.RetentionTTL, oneDay)
	}
	if dc.LastTimerChangeTs != chosenAt {
		t.Errorf("retention stamp = %d, want %d — a reset stamp re-opens convergence with the peer",
			dc.LastTimerChangeTs, chosenAt)
	}
	if !dc.DisableReadReceipts {
		t.Error("read receipts were switched back on by a contacts push")
	}

	rec, err := b.peerMeta.GetPerson(railFingerprint)
	if err != nil {
		t.Fatalf("the person did not cross at all: %v", err)
	}
	if rec.Nick != "alice" {
		t.Errorf("nick = %q, want alice", rec.Nick)
	}
}
