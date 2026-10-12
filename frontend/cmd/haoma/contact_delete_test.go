package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/peerstate"
	"haoma-frontend/internal/syncrail"
)

const (
	cdSibOnA     = "0000000000000000000000000000b0b0"
	cdSibOnB     = "0000000000000000000000000000a0a0"
	cdDaveOnA    = "000000000000000000000000000da7ea"
	cdDaveOnB    = "000000000000000000000000000da7eb"
	cdDaveNewA   = "000000000000000000000000000da7ec"
	cdDaveNewB   = "000000000000000000000000000da7ed"
	cdDaveFinger = railFingerprint
)

type twoDevices struct {
	a, b         *daemon
	stubA, stubB *haomadStub
	connA, connB *websocket.Conn
	chatA, chatB chat.ChatID
	ctx          context.Context
}

func newTwoDevices(t *testing.T) *twoDevices {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	stubA := startHaomadStub(t, []string{"onion-a"}, http.StatusCreated)
	stubA.MissingPeers = map[string]bool{}
	devA, addrA, certA, tokA := newTestDaemon(t, stubA)
	stubB := startHaomadStub(t, []string{"onion-b"}, http.StatusCreated)
	stubB.MissingPeers = map[string]bool{}
	devB, addrB, certB, tokB := newTestDaemon(t, stubB)
	adoptIdentityFrom(t, devB, devA)
	linkSibling(t, devA, cdSibOnA)
	linkSibling(t, devB, cdSibOnB)

	past := time.Now().Unix() - 3600
	w := &twoDevices{a: devA, b: devB, stubA: stubA, stubB: stubB, ctx: ctx}
	w.chatA = seedDaveAt(t, devA, cdDaveOnA, past)
	w.chatB = seedDaveAt(t, devB, cdDaveOnB, past)
	stubA.PeerList = []backendapi.Peer{{ID: cdDaveOnA}, {ID: cdSibOnA}}
	stubB.PeerList = []backendapi.Peer{{ID: cdDaveOnB}, {ID: cdSibOnB}}
	seedRows(t, devA, w.chatA, 2)
	seedRows(t, devB, w.chatB, 3)
	w.connA = dialTest(t, ctx, addrA, certA, tokA)
	w.connB = dialTest(t, ctx, addrB, certB, tokB)
	return w
}

func seedDaveAt(t *testing.T, d *daemon, peerID string, pairedAt int64) chat.ChatID {
	t.Helper()
	if _, _, err := d.peerMeta.SetFingerprintAt(peerID, cdDaveFinger, pairedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := d.peerMeta.SetNick(peerID, "dave", 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.peerMeta.SeedPersonFromPairing(peerID); err != nil {
		t.Fatal(err)
	}
	dc, _, err := d.createDirectWithDefaults(peerID)
	if err != nil {
		t.Fatal(err)
	}
	return dc.ID
}

func sentSince(s *haomadStub, from int) [][]byte {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if from >= len(s.SentPayloads) {
		return nil
	}
	return append([][]byte(nil), s.SentPayloads[from:]...)
}

func sentCount(s *haomadStub) int {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return len(s.SentPayloads)
}

func contactDeletesIn(t *testing.T, d *daemon, payloads [][]byte) []syncrail.ContactDeleteBody {
	t.Helper()
	key, err := d.railKey()
	if err != nil {
		t.Fatal(err)
	}
	var out []syncrail.ContactDeleteBody
	for _, p := range payloads {
		env, err := syncrail.OpenEnvelope(key, p)
		if err != nil || env.Kind != syncrail.KindContactDelete {
			continue
		}
		body, err := syncrail.ParseContactDelete(env)
		if err != nil {
			t.Fatalf("a contact delete that does not parse: %v", err)
		}
		out = append(out, body)
	}
	return out
}

func assertDaveGone(t *testing.T, d *daemon, chatID chat.ChatID) {
	t.Helper()
	if _, err := d.chats.Get(chatID); !errors.Is(err, chat.ErrNotFound) {
		t.Errorf("the chat is still here: %v", err)
	}
	if rows, err := d.events.List(chatID, 0, 0); err != nil || len(rows) != 0 {
		t.Errorf("%d rows of the chat are left (err %v), want none", len(rows), err)
	}
	if ids, _ := d.peerMeta.PeerIDsByFingerprint(cdDaveFinger); len(ids) != 0 {
		t.Errorf("pairing records left for Dave: %v", ids)
	}
	if has, _ := d.peerMeta.HasPerson(cdDaveFinger); has {
		t.Error("the person record is still here")
	}
	if _, err := d.chats.GetByFingerprint(cdDaveFinger); !errors.Is(err, chat.ErrNotFound) {
		t.Errorf("a chat for Dave is still found by fingerprint: %v", err)
	}
	if gone, _, err := personDeletedHere(d, cdDaveFinger); err != nil || !gone {
		t.Errorf("personDeletedHere = %v (err %v), want true", gone, err)
	}
}

func (w *twoDevices) operatorDeleteOnA(t *testing.T, peerID string) {
	t.Helper()
	deleteOverIPC(t, w.ctx, w.connA, "pa-del-"+peerID, peerID)
}

func TestContactDelete_DeletesThePersonOnBothDevices(t *testing.T) {
	w := newTwoDevices(t)

	w.operatorDeleteOnA(t, cdDaveOnA)
	w.stubA.WaitForSend(t, 1, 5*time.Second)

	assertDaveGone(t, w.a, w.chatA)
	if got := w.stubA.PeerActionIDs; len(got) != 1 || got[0] != cdDaveOnA {
		t.Fatalf("A asked haomad to delete %v, want [%s]", got, cdDaveOnA)
	}
	tombA, found, err := w.a.peerMeta.Tombstone(cdDaveFinger)
	if err != nil || !found || !tombA.Done {
		t.Fatalf("A's tombstone = %+v found=%v err=%v, want a done one", tombA, found, err)
	}

	sent := sentSince(w.stubA, 0)
	deletes := contactDeletesIn(t, w.b, sent)
	if len(deletes) != 1 || len(deletes[0].Deleted) != 1 {
		t.Fatalf("A pushed %d contact deletes, want one naming one person", len(deletes))
	}
	if got := deletes[0].Deleted[0]; got.Fingerprint != cdDaveFinger || got.DeletedAt != tombA.DeletedAt {
		t.Fatalf("the push names %+v, want Dave at %d", got, tombA.DeletedAt)
	}
	w.stubA.sendMu.Lock()
	target := w.stubA.SentPeerIDs[0]
	w.stubA.sendMu.Unlock()
	if target != cdSibOnA {
		t.Fatalf("the push went to %s, want my other device %s", target, cdSibOnA)
	}

	processSiblingRailEnvelope(w.ctx, w.b, railEntry(cdSibOnB, "env-del-1", sent[0]))

	pd := readUntil(t, w.ctx, w.connB, ipc.FramePeerDeleted)
	var pdp ipc.PeerDeletedPayload
	_ = json.Unmarshal(pd.Payload, &pdp)
	if pdp.PeerID != cdDaveOnB {
		t.Fatalf("B's client was told peer %s was deleted, want %s", pdp.PeerID, cdDaveOnB)
	}
	cd := readUntil(t, w.ctx, w.connB, ipc.FrameChatDeleted)
	var cdp ipc.ChatDeletedPayload
	_ = json.Unmarshal(cd.Payload, &cdp)
	if cdp.ChatID != string(w.chatB) || cdp.DeletedCount != 3 {
		t.Fatalf("B's client got chat.deleted %+v, want %s with 3 rows", cdp, w.chatB)
	}
	assertDaveGone(t, w.b, w.chatB)
	if got := w.stubB.PeerActionIDs; len(got) != 1 || got[0] != cdDaveOnB {
		t.Fatalf("B asked haomad to delete %v, want [%s]", got, cdDaveOnB)
	}
	tombB, found, err := w.b.peerMeta.Tombstone(cdDaveFinger)
	if err != nil || !found || tombB.DeletedAt != tombA.DeletedAt {
		t.Fatalf("B's tombstone = %+v found=%v err=%v, want A's time %d", tombB, found, err, tombA.DeletedAt)
	}

	waitDone(t, w.b)
	applyDeletedContacts(w.ctx, w.b, slog.Default(), deletes[0].Deleted, "test_replay")
	if got := w.stubB.PeerActionIDs; len(got) != 1 {
		t.Fatalf("a replayed delete asked haomad again: %v", got)
	}
}

func waitDone(t *testing.T, d *daemon) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if tomb, found, _ := d.peerMeta.Tombstone(cdDaveFinger); found && tomb.Done {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the delete was never recorded as done")
}

func TestContactDelete_NoBackfillBringsThePersonBack(t *testing.T) {
	w := newTwoDevices(t)
	w.operatorDeleteOnA(t, cdDaveOnA)
	w.stubA.WaitForSend(t, 1, 5*time.Second)

	resp := w.answer(t, w.b, w.stubB, cdSibOnB, w.a, syncrail.SectionContacts)
	if !holdsContact(resp, cdDaveFinger) {
		t.Fatal("precondition: B, which never got the delete, does not describe Dave")
	}
	handleBackfillResponse(w.ctx, w.a, cdSibOnA, resp)
	if has, _ := w.a.peerMeta.HasPerson(cdDaveFinger); has {
		t.Fatal("A took Dave back from B's contacts section")
	}
	if got, err := inheritedContacts(w.a); err != nil || len(got) != 0 {
		t.Fatalf("A offers %v as inherited contacts (err %v), want none", got, err)
	}

	resp = w.answer(t, w.b, w.stubB, cdSibOnB, w.a, syncrail.SectionChats)
	if len(resp.Chats) == 0 {
		t.Fatal("precondition: B describes no chat")
	}
	handleBackfillResponse(w.ctx, w.a, cdSibOnA, resp)
	if _, err := w.a.chats.GetByFingerprint(cdDaveFinger); !errors.Is(err, chat.ErrNotFound) {
		t.Fatalf("A adopted Dave's chat from B: %v", err)
	}
	if _, err := w.a.chats.Get(chat.ChatID(resp.Chats[0].ChatID)); !errors.Is(err, chat.ErrNotFound) {
		t.Fatalf("A holds B's chat with Dave: %v", err)
	}
	assertDaveGone(t, w.a, w.chatA)

	resp = w.answer(t, w.a, w.stubA, cdSibOnA, w.b, syncrail.SectionContacts)
	if holdsContact(resp, cdDaveFinger) {
		t.Fatal("A described Dave to B after deleting him")
	}
	if len(resp.DeletedContacts) != 1 || resp.DeletedContacts[0].Fingerprint != cdDaveFinger {
		t.Fatalf("A's contacts section carries deletes %+v, want Dave's", resp.DeletedContacts)
	}
	handleBackfillResponse(w.ctx, w.b, cdSibOnB, resp)
	assertDaveGone(t, w.b, w.chatB)

	resp = w.answer(t, w.b, w.stubB, cdSibOnB, w.a, syncrail.SectionContacts)
	if holdsContact(resp, cdDaveFinger) {
		t.Fatal("B still describes Dave after taking the delete")
	}

	if _, _, err := w.a.peerMeta.SetFingerprintAt(cdDaveOnA, cdDaveFinger, time.Now().Unix()-3600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.a.peerMeta.SetNick(cdDaveOnA, "dave", 0); err != nil {
		t.Fatal(err)
	}
	resp = w.answer(t, w.a, w.stubA, cdSibOnA, w.b, syncrail.SectionContacts)
	if holdsContact(resp, cdDaveFinger) {
		t.Fatal("A described Dave to B from a record older than the delete")
	}
}

func (w *twoDevices) answer(t *testing.T, from *daemon, fromStub *haomadStub, fromSib string, to *daemon, section syncrail.BackfillSection) syncrail.BackfillResponseBody {
	t.Helper()
	n := sentCount(fromStub)
	handleBackfillRequest(w.ctx, from, fromSib, syncrail.BackfillRequestBody{Section: section})
	var out [][]byte
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if out = sentSince(fromStub, n); len(out) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(out) == 0 {
		t.Fatalf("no %s answer was sent", section)
	}
	resp, err := syncrail.ParseBackfillResponse(openGapEnvelope(t, to, out[0]))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Section != section {
		t.Fatalf("answer is for %s, want %s", resp.Section, section)
	}
	return resp
}

func holdsContact(resp syncrail.BackfillResponseBody, fp string) bool {
	for _, c := range resp.Contacts {
		if c.Fingerprint == fp {
			return true
		}
	}
	return false
}

func TestContactDelete_RePairAfterTheDeleteIsNotBlocked(t *testing.T) {
	w := newTwoDevices(t)
	w.operatorDeleteOnA(t, cdDaveOnA)
	w.stubA.WaitForSend(t, 1, 5*time.Second)
	deletes := contactDeletesIn(t, w.b, sentSince(w.stubA, 0))
	if len(deletes) != 1 {
		t.Fatal("precondition: no delete was pushed")
	}
	applyDeletedContacts(w.ctx, w.b, slog.Default(), deletes[0].Deleted, "sibling_push")
	assertDaveGone(t, w.b, w.chatB)
	deletedAt := deletes[0].Deleted[0].DeletedAt

	newChatA := seedDaveAt(t, w.a, cdDaveNewA, deletedAt+10)
	notePairingCompleted(w.a, cdDaveNewA, "test")
	if gone, _, _ := personDeletedHere(w.a, cdDaveFinger); gone {
		t.Fatal("A still reads Dave as deleted after pairing with him again")
	}
	if newChatA == w.chatA {
		t.Fatal("precondition: the re-pair landed in the deleted chat")
	}

	body := contactsPushBody(w.a, []string{cdDaveFinger})
	if len(body.Contacts) != 1 || body.Contacts[0].PairedAt != deletedAt+10 {
		t.Fatalf("A's push describes %+v, want Dave paired at %d", body.Contacts, deletedAt+10)
	}
	handleContactsPush(w.ctx, w.b, cdSibOnB, body)
	if has, _ := w.b.peerMeta.HasPerson(cdDaveFinger); !has {
		t.Fatal("B refused Dave after he paired with A again")
	}
	adopted, err := w.b.chats.GetByFingerprint(cdDaveFinger)
	if err != nil || adopted.ID != newChatA {
		t.Fatalf("B adopted %v (err %v), want A's new chat %s", adopted, err, newChatA)
	}
	listed, err := inheritedContacts(w.b)
	if err != nil || len(listed) != 1 || listed[0].Fingerprint != cdDaveFinger {
		t.Fatalf("B offers %+v (err %v), want Dave", listed, err)
	}

	text, _ := json.Marshal(events.TextBody{Text: "hello again"})
	handleGapResponse(w.ctx, w.b, cdSibOnB, syncrail.GapResponseBody{
		RangeEnd: 2,
		Rows: []syncrail.RowSnapshot{{
			Fingerprint: cdDaveFinger, MsgID: "after-the-delete-1", Kind: string(events.KindText),
			DisplayTs: deletedAt + 20, Body: text,
		}},
	})
	if _, err := w.b.events.GetByMsgID("after-the-delete-1"); err != nil {
		t.Fatalf("a row written after the delete did not land on B: %v", err)
	}

	applyDeletedContacts(w.ctx, w.b, slog.Default(), deletes[0].Deleted, "sibling_sweep")
	if _, err := w.b.chats.Get(newChatA); err != nil {
		t.Fatalf("the delete coming round again erased the chat B adopted after it: %v", err)
	}
	if _, err := w.b.events.GetByMsgID("after-the-delete-1"); err != nil {
		t.Fatalf("the delete coming round again erased a row from after it: %v", err)
	}

	if _, _, err := w.b.peerMeta.SetFingerprintAt(cdDaveNewB, cdDaveFinger, time.Now().Unix()+5); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.b.createDirectWithDefaults(cdDaveNewB); err != nil {
		t.Fatal(err)
	}
	w.stubB.PeerList = append(w.stubB.PeerList, backendapi.Peer{ID: cdDaveNewB})

	before := len(w.stubB.PeerActionIDs)
	resp := w.answer(t, w.a, w.stubA, cdSibOnA, w.b, syncrail.SectionContacts)
	if len(resp.DeletedContacts) != 1 || !holdsContact(resp, cdDaveFinger) {
		t.Fatalf("A's answer: deletes %+v, contacts %+v; want the delete and Dave", resp.DeletedContacts, resp.Contacts)
	}
	handleBackfillResponse(w.ctx, w.b, cdSibOnB, resp)
	if got := w.stubB.PeerActionIDs[before:]; len(got) != 0 {
		t.Fatalf("the delete coming round again deleted %v on B", got)
	}
	if ids, _ := w.b.peerMeta.PeerIDsByFingerprint(cdDaveFinger); len(ids) != 1 || ids[0] != cdDaveNewB {
		t.Fatalf("B's pairings with Dave are %v, want only the new one", ids)
	}
	if _, err := w.b.chats.Get(newChatA); err != nil {
		t.Fatalf("the new chat on B is gone: %v", err)
	}
}

func TestContactDelete_ARetiredPairingDeleteDoesNotCross(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedChatPrimaryOnTheOldPairing(t, d, stub)
	seedRows(t, d, chatID, 3)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)
	writeFrame(t, ctx, conn, mustFrame(ipc.FramePeerAction, "pa-ret", ipc.PeerActionRequest{PeerID: dcAliceOldID, Action: ipc.PeerActionRetire}))
	awaitChatUpdated(t, ctx, conn, chatID, dcAliceID)
	deleteOverIPC(t, ctx, conn, "pa-del", dcAliceOldID)

	time.Sleep(200 * time.Millisecond)
	if got := contactDeletesIn(t, d, sentSince(stub, 0)); len(got) != 0 {
		t.Fatalf("a pairing delete crossed to my other device: %+v", got)
	}
	if _, found, _ := d.peerMeta.Tombstone(dcAliceFinger); found {
		t.Fatal("a pairing delete left a tombstone for the person")
	}
	ids, _ := d.peerMeta.PeerIDsByFingerprint(dcAliceFinger)
	if len(ids) != 1 || ids[0] != dcAliceID {
		t.Fatalf("pairing records for Alice: %v, want only the live %s", ids, dcAliceID)
	}
	if rows, _ := d.events.List(chatID, 0, 0); len(rows) != 3 {
		t.Fatalf("%d rows left, want the 3 the chat held", len(rows))
	}
}

func TestContactDelete_UnknownPersonAndOwnDevice(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	now := time.Now().Unix()

	applyDeletedContacts(context.Background(), d, slog.Default(),
		[]syncrail.DeletedContact{{Fingerprint: cdDaveFinger, DeletedAt: now - 5}}, "test")
	tomb, found, err := d.peerMeta.Tombstone(cdDaveFinger)
	if err != nil || !found || !tomb.Done || tomb.DeletedAt != now-5 {
		t.Fatalf("tombstone = %+v found=%v err=%v", tomb, found, err)
	}
	if stub.PeerActionCalls != 0 {
		t.Fatalf("haomad was asked %d times for a person this device never knew", stub.PeerActionCalls)
	}

	const ownFinger = "cc11bb22cc33dd44ee55ff6600778899"
	if _, _, err := d.peerMeta.SetFingerprintAt(railSiblingID, ownFinger, now-3600); err != nil {
		t.Fatal(err)
	}
	applyDeletedContacts(context.Background(), d, slog.Default(),
		[]syncrail.DeletedContact{{Fingerprint: ownFinger, DeletedAt: now}}, "test")
	if ids, _ := d.peerMeta.PeerIDsByFingerprint(ownFinger); len(ids) != 1 {
		t.Fatalf("a delete naming my own device removed its record: %v", ids)
	}
	if stub.PeerActionCalls != 0 {
		t.Fatalf("a delete naming my own device asked haomad %d times", stub.PeerActionCalls)
	}
	if _, found, _ := d.peerMeta.Tombstone(ownFinger); found {
		t.Fatal("a refused delete left a tombstone on my own fingerprint")
	}

	own := d.stores.GetIdentityKeyPair().PublicKey().Fingerprint()
	applyDeletedContacts(context.Background(), d, slog.Default(),
		[]syncrail.DeletedContact{{Fingerprint: own, DeletedAt: now}}, "test")
	if _, found, _ := d.peerMeta.Tombstone(own); found {
		t.Fatal("a delete naming my own identity left a tombstone")
	}
}

func TestContactDelete_GapRowOfADeletedContactIsSkippedNotHeld(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	deletedAt := time.Now().Unix() - 100
	if _, _, err := d.peerMeta.MarkDeleted(cdDaveFinger, deletedAt, deletedAt); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(events.TextBody{Text: "a row"})
	row := func(id string, at int64) syncrail.RowSnapshot {
		return syncrail.RowSnapshot{
			Fingerprint: cdDaveFinger, MsgID: id, Kind: string(events.KindText),
			Inbound: true, DisplayTs: at, Body: body,
		}
	}
	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{RangeEnd: 5, Rows: []syncrail.RowSnapshot{row("before-1", deletedAt-60)}})
	if wm, _ := d.watermarks.Get(railSiblingID); wm != 5 {
		t.Fatalf("watermark = %d, want 5: a row from before the delete held the cursor", wm)
	}
	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{RangeEnd: 9, Rows: []syncrail.RowSnapshot{row("after-1", deletedAt+60)}})
	if wm, _ := d.watermarks.Get(railSiblingID); wm != 5 {
		t.Fatalf("watermark = %d, want 5: a row from after the delete was skipped, and is lost for good", wm)
	}

	seedDaveAt(t, d, cdDaveNewA, deletedAt+10)
	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{RangeEnd: 12, Rows: []syncrail.RowSnapshot{
		row("after-1", deletedAt+60), row("before-2", deletedAt-60), row("after-2", deletedAt+60),
	}})
	if _, err := d.events.GetByMsgID("after-1"); err != nil {
		t.Fatalf("the held row did not land once Dave was known: %v", err)
	}
	if _, err := d.events.GetByMsgID("before-2"); err == nil {
		t.Fatal("a row from before the delete landed in the chat of the new pairing")
	}
	if _, err := d.events.GetByMsgID("after-2"); err != nil {
		t.Fatalf("a row from after the delete did not land: %v", err)
	}
	if wm, _ := d.watermarks.Get(railSiblingID); wm != 12 {
		t.Fatalf("watermark = %d, want 12", wm)
	}
}

func TestContactDelete_DeadPeerRowsAreRemovedOnStart(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	old := time.Now().Unix() - 2*3600
	const (
		dead    = "00000000000000000000000000000dea"
		live    = "000000000000000000000000000001fe"
		young   = "0000000000000000000000000000a0a1"
		chatted = "0000000000000000000000000000c4a7"
	)
	for _, id := range []string{dead, live, chatted} {
		if _, _, err := d.peerMeta.SetFingerprintAt(id, cdDaveFinger, old); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := d.peerMeta.SetFingerprintAt(young, cdDaveFinger, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := d.chats.CreateDirect(chatted); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.peerMeta.SetFingerprintAt(railSiblingID, "cc11bb22cc33dd44ee55ff6600778899", old); err != nil {
		t.Fatal(err)
	}
	stub.PeerList = []backendapi.Peer{{ID: live}}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)
	sweepDeadPeerRows(ctx, d)

	ids, _ := d.peerMeta.PeerIDsByFingerprint(cdDaveFinger)
	want := []string{live, chatted, young}
	if len(ids) != 3 {
		t.Fatalf("records left: %v, want %v", ids, want)
	}
	for _, id := range ids {
		if id == dead {
			t.Fatalf("the dead record is still here: %v", ids)
		}
	}
	if mine, _ := d.peerMeta.PeerIDsByFingerprint("cc11bb22cc33dd44ee55ff6600778899"); len(mine) != 1 {
		t.Fatal("the sweep removed my own device's record")
	}
	pd := readUntil(t, ctx, conn, ipc.FramePeerDeleted)
	var p ipc.PeerDeletedPayload
	_ = json.Unmarshal(pd.Payload, &p)
	if p.PeerID != dead {
		t.Fatalf("clients were told %s was deleted, want %s", p.PeerID, dead)
	}

	sweepDeadPeerRows(ctx, d)
	if ids2, _ := d.peerMeta.PeerIDsByFingerprint(cdDaveFinger); len(ids2) != 3 {
		t.Fatalf("the second run changed the records: %v", ids2)
	}
}

func TestContactDelete_NoRowIsPushedForAPairingHaomadDoesNotHave(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusCreated)
	stub.MissingPeers = map[string]bool{cdDaveOnA: true}
	d, addr, certPath, token := newTestDaemon(t, stub)
	seedDaveAt(t, d, cdDaveNewA, time.Now().Unix()-60)
	if _, _, err := d.peerMeta.SetFingerprintAt(cdDaveOnA, cdDaveFinger, time.Now().Unix()-3600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	broadcastPersonPeers(ctx, d, cdDaveFinger)

	for i := 0; i < 2; i++ {
		f := readUntil(t, ctx, conn, ipc.FramePeerUpdated)
		var u ipc.PeerUpdatedPayload
		_ = json.Unmarshal(f.Payload, &u)
		if u.Peer.ID == cdDaveOnA {
			t.Fatalf("a row was pushed for %s, which haomad does not have", cdDaveOnA)
		}
		if u.Peer.ID == cdDaveNewA {
			return
		}
	}
	t.Fatal("the live pairing's row was not pushed")
}

func TestContactDelete_OneOfTwoLivePairingsIsAPairingDelete(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedChatPrimaryOnTheOldPairing(t, d, stub)
	seedRows(t, d, chatID, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	deleteOverIPC(t, ctx, conn, "pa-del", dcAliceOldID)
	time.Sleep(200 * time.Millisecond)

	if _, found, _ := d.peerMeta.Tombstone(dcAliceFinger); found {
		t.Fatal("deleting one of two live pairings left a tombstone for the person")
	}
	if got := contactDeletesIn(t, d, sentSince(stub, 0)); len(got) != 0 {
		t.Fatalf("deleting one of two live pairings crossed: %+v", got)
	}
	if got := stub.PeerActionIDs; len(got) != 1 || got[0] != dcAliceOldID {
		t.Fatalf("haomad was asked to delete %v, want [%s]", got, dcAliceOldID)
	}
	got := readChat(t, d, chatID)
	if got.PeerID != dcAliceID {
		t.Fatalf("the chat's primary is %s, want the live %s", got.PeerID, dcAliceID)
	}
	if rows, _ := d.events.List(chatID, 0, 0); len(rows) != 2 {
		t.Fatalf("%d rows left, want 2", len(rows))
	}
}

func TestContactDelete_TheLastLivePairingIsAPersonDelete(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedChatPrimaryOnTheOldPairing(t, d, stub)
	seedRows(t, d, chatID, 2)
	for i := range stub.PeerList {
		if stub.PeerList[i].ID == dcAliceOldID {
			stub.PeerList[i].RetiredAt = time.Now().Unix() - 60
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	deleteOverIPC(t, ctx, conn, "pa-del", dcAliceID)
	stub.WaitForSend(t, 1, 5*time.Second)

	if _, found, _ := d.peerMeta.Tombstone(dcAliceFinger); !found {
		t.Fatal("deleting the last live pairing left no tombstone")
	}
	if got := contactDeletesIn(t, d, sentSince(stub, 0)); len(got) != 1 {
		t.Fatalf("the person delete pushed %d contact deletes, want 1", len(got))
	}
	if ids, _ := d.peerMeta.PeerIDsByFingerprint(dcAliceFinger); len(ids) != 0 {
		t.Fatalf("pairing records left: %v", ids)
	}
	if _, err := d.chats.Get(chatID); !errors.Is(err, chat.ErrNotFound) {
		t.Fatalf("the chat survived the person delete: %v", err)
	}
}

func TestContactDelete_ARePairBeforeTheDeleteArrivesKeepsTheNewPairing(t *testing.T) {
	w := newTwoDevices(t)
	deletedAt := time.Now().Unix() - 100
	if _, _, err := w.b.peerMeta.SetFingerprintAt(cdDaveNewB, cdDaveFinger, deletedAt+10); err != nil {
		t.Fatal(err)
	}
	folded, _, err := w.b.createDirectWithDefaults(cdDaveNewB)
	if err != nil || folded.ID != w.chatB {
		t.Fatalf("precondition: the re-pair did not fold into the old chat (%v)", err)
	}
	w.stubB.PeerList = append(w.stubB.PeerList, backendapi.Peer{ID: cdDaveNewB})
	newRow := appendRowAt(t, w.b, w.chatB, "after-the-delete-b", time.Now().Unix()-5)

	applyDeletedContacts(w.ctx, w.b, slog.Default(),
		[]syncrail.DeletedContact{{Fingerprint: cdDaveFinger, DeletedAt: deletedAt}}, "sibling_push")

	if got := w.stubB.PeerActionIDs; len(got) != 1 || got[0] != cdDaveOnB {
		t.Fatalf("haomad was asked to delete %v, want only the old pairing %s", got, cdDaveOnB)
	}
	got := readChat(t, w.b, w.chatB)
	if got.PeerID != cdDaveNewB || len(got.PeerIDs) != 1 || got.PeerIDs[0] != cdDaveNewB {
		t.Fatalf("primary %s, routes %v; want the new pairing alone", got.PeerID, got.PeerIDs)
	}
	if _, err := w.b.chats.GetByDirectPeer(cdDaveOnB); !errors.Is(err, chat.ErrNotFound) {
		t.Fatalf("the old pairing still resolves to the chat: %v", err)
	}
	rows, _ := w.b.events.List(w.chatB, 0, 0)
	if len(rows) != 1 || rows[0].MsgID != newRow {
		t.Fatalf("rows left: %d, want only the one from after the delete", len(rows))
	}
	if ids, _ := w.b.peerMeta.PeerIDsByFingerprint(cdDaveFinger); len(ids) != 1 || ids[0] != cdDaveNewB {
		t.Fatalf("pairing records: %v, want only the new one", ids)
	}
	if has, _ := w.b.peerMeta.HasPerson(cdDaveFinger); !has {
		t.Fatal("the person record went although a pairing made after the delete stays")
	}
}

func appendRowAt(t *testing.T, d *daemon, chatID chat.ChatID, msgID string, at int64) string {
	t.Helper()
	body, _ := json.Marshal(events.TextBody{Text: msgID})
	if _, err := d.events.AppendInbound(events.InboundParams{
		ChatID: chatID, Kind: events.KindText, SenderTs: at, DisplayTs: at, MsgID: msgID,
		EnvelopeID: "env-" + msgID, Status: events.DecryptOK, Body: body,
	}); err != nil {
		t.Fatal(err)
	}
	return msgID
}

func TestContactDelete_AFullSyncAfterARePairBringsNoOldRowBack(t *testing.T) {
	w := newTwoDevices(t)

	seedText(t, w.b, w.chatB, "old history on b 1")
	seedText(t, w.b, w.chatB, "old history on b 2")
	w.operatorDeleteOnA(t, cdDaveOnA)
	w.stubA.WaitForSend(t, 1, 5*time.Second)

	tomb, _, _ := w.a.peerMeta.Tombstone(cdDaveFinger)
	newChatA := seedDaveAt(t, w.a, cdDaveNewA, tomb.DeletedAt+10)

	n := sentCount(w.stubB)
	handleGapRequest(w.ctx, w.b, cdSibOnB, syncrail.GapRequestBody{Full: true})
	var out [][]byte
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(out) == 0 {
		out = sentSince(w.stubB, n)
		time.Sleep(10 * time.Millisecond)
	}
	if len(out) == 0 {
		t.Fatal("B sent no gap-fill answer")
	}
	resp, err := syncrail.ParseGapResponse(openGapEnvelope(t, w.a, out[0]))
	if err != nil {
		t.Fatal(err)
	}
	var daveRows []string
	for _, r := range resp.Rows {
		if r.Fingerprint == cdDaveFinger {
			daveRows = append(daveRows, r.MsgID)
		}
	}
	if len(daveRows) == 0 {
		t.Fatal("precondition: B's full answer carries no row of Dave's")
	}
	handleGapResponse(w.ctx, w.a, cdSibOnA, resp)
	for _, id := range daveRows {
		if _, err := w.a.events.GetByMsgID(id); err == nil {
			t.Fatalf("row %s from before the delete came back on A", id)
		}
	}
	if rows, _ := w.a.events.List(newChatA, 0, 0); len(rows) != 0 {
		t.Fatalf("A's new chat with Dave holds %d rows from B's old history", len(rows))
	}
	if wm, _ := w.a.watermarks.Get(cdSibOnA); wm == 0 {
		t.Fatal("the rows of the deleted history held A's cursor")
	}
}

func TestContactDelete_ASenderClockAheadDoesNotCoverALaterPairing(t *testing.T) {
	w := newTwoDevices(t)
	ahead := time.Now().Unix() + 600
	deleted := []syncrail.DeletedContact{{Fingerprint: cdDaveFinger, DeletedAt: ahead}}
	applyDeletedContacts(w.ctx, w.b, slog.Default(), deleted, "sibling_push")
	assertDaveGone(t, w.b, w.chatB)
	held, _, _ := w.b.peerMeta.Tombstone(cdDaveFinger)
	if held.DeletedAt != ahead || held.AppliedAt > time.Now().Unix() || !held.Done {
		t.Fatalf("tombstone = %+v, want the sender's time %d kept, applied now, done", held, ahead)
	}

	newChat := seedDaveAt(t, w.b, cdDaveNewB, time.Now().Unix()+5)
	w.stubB.PeerList = append(w.stubB.PeerList, backendapi.Peer{ID: cdDaveNewB})
	if gone, _, _ := personDeletedHere(w.b, cdDaveFinger); gone {
		t.Fatal("the new pairing is read as deleted")
	}

	before := len(w.stubB.PeerActionIDs)
	applyDeletedContacts(w.ctx, w.b, slog.Default(), deleted, "sibling_sweep")
	if got := w.stubB.PeerActionIDs[before:]; len(got) != 0 {
		t.Fatalf("the same delete again deleted %v", got)
	}
	if _, err := w.b.chats.Get(newChat); err != nil {
		t.Fatalf("the new chat went: %v", err)
	}
	again, _, _ := w.b.peerMeta.Tombstone(cdDaveFinger)
	if again != held {
		t.Fatalf("the same delete again moved the tombstone: %+v, was %+v", again, held)
	}
}

func TestContactDelete_ANotDoneDeleteSparesAChatAdoptedAfterIt(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	now := time.Now().Unix()
	if _, _, err := d.peerMeta.MarkDeleted(cdDaveFinger, now-100, now-50); err != nil {
		t.Fatal(err)
	}

	if _, err := d.peerMeta.MergePerson(cdDaveFinger, peerstateRecord("dave")); err != nil {
		t.Fatal(err)
	}
	dc, err := d.chats.CreateDirectEdgeless(chat.ChatID("0000000000000000000000000000c0de"), cdDaveFinger)
	if err != nil {
		t.Fatal(err)
	}
	kept := appendRowAt(t, d, dc.ID, "after-adoption", now-10)

	applyDeletedContacts(context.Background(), d, slog.Default(),
		[]syncrail.DeletedContact{{Fingerprint: cdDaveFinger, DeletedAt: now - 100}}, "sibling_sweep")

	if _, err := d.chats.Get(dc.ID); err != nil {
		t.Fatalf("a not-done delete erased a chat adopted after it: %v", err)
	}
	if _, err := d.events.GetByMsgID(kept); err != nil {
		t.Fatalf("a not-done delete erased a row from after it: %v", err)
	}
	if has, _ := d.peerMeta.HasPerson(cdDaveFinger); !has {
		t.Fatal("a not-done delete removed the person adopted after it")
	}
}

func TestContactDelete_DeadPeerSweepRemovesNothingOnASuspectList(t *testing.T) {
	old := time.Now().Unix() - 2*3600
	ids := []string{
		"00000000000000000000000000000d01", "00000000000000000000000000000d02",
		"00000000000000000000000000000d03", "00000000000000000000000000000d04",
	}
	cases := []struct {
		name string
		list []backendapi.Peer

		chatted []string
	}{

		{"an empty list", nil, ids[1:]},
		{"most records would go", []backendapi.Peer{{ID: ids[0]}}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stub := startHaomadStub(t, nil, http.StatusCreated)
			d, _, _, _ := newTestDaemon(t, stub)
			for _, id := range ids {
				if _, _, err := d.peerMeta.SetFingerprintAt(id, cdDaveFinger, old); err != nil {
					t.Fatal(err)
				}
			}
			for _, id := range c.chatted {
				if _, err := d.chats.CreateDirect(id); err != nil {
					t.Fatal(err)
				}
			}
			stub.PeerList = c.list
			sweepDeadPeerRows(context.Background(), d)
			if got, _ := d.peerMeta.PeerIDsByFingerprint(cdDaveFinger); len(got) != len(ids) {
				t.Fatalf("records left: %v, want all %d", got, len(ids))
			}
		})
	}
}

func peerstateRecord(nick string) peerstate.MetaRecord {
	return peerstate.MetaRecord{Nick: nick}
}

func TestContactDelete_AMirrorThatOvertakesTheRePairStillLands(t *testing.T) {
	prevSettle := unplacedSweepSettle
	unplacedSweepSettle = 10 * time.Millisecond
	t.Cleanup(func() { unplacedSweepSettle = prevSettle })

	w := newTwoDevices(t)

	deletedAt := time.Now().Unix() - 100
	deleted := []syncrail.DeletedContact{{Fingerprint: cdDaveFinger, DeletedAt: deletedAt}}
	applyDeletedContacts(w.ctx, w.a, slog.Default(), deleted, "test")
	applyDeletedContacts(w.ctx, w.b, slog.Default(), deleted, "sibling_push")

	newChatA := seedDaveAt(t, w.a, cdDaveNewA, deletedAt+10)
	notePairingCompleted(w.a, cdDaveNewA, "test")
	text := seedText(t, w.a, newChatA, "the first text after the re-pair")

	nB := sentCount(w.stubB)
	processSiblingRailEnvelope(w.ctx, w.b, railEntry(cdSibOnB, "env-mirror-early",
		sealMirror(t, w.b, cdDaveFinger, railText(t, "the first text after the re-pair", 1, time.Now().Unix(), 1))))
	asked := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !asked {
		key, _ := w.b.railKey()
		for _, p := range sentSince(w.stubB, nB) {
			env, err := syncrail.OpenEnvelope(key, p)
			if err != nil || env.Kind != syncrail.KindBackfillRequest {
				continue
			}
			if req, err := syncrail.ParseBackfillRequest(env); err == nil && req.Section == syncrail.SectionContacts {
				asked = true
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !asked {
		t.Fatal("a mirror for a person B does not know asked for no sweep: its row waits for whatever sweep comes next")
	}

	gap := w.gapAnswer(t)
	handleGapResponse(w.ctx, w.b, cdSibOnB, gap)
	if _, err := w.b.events.GetByMsgID(text); err == nil {
		t.Fatal("precondition: the row landed before Dave was known")
	}
	if wm, _ := w.b.watermarks.Get(cdSibOnB); wm >= gap.RangeEnd {
		t.Fatalf("watermark = %d: the row of the re-paired Dave was skipped past, and is lost for good", wm)
	}

	handleBackfillResponse(w.ctx, w.b, cdSibOnB, w.answer(t, w.a, w.stubA, cdSibOnA, w.b, syncrail.SectionContacts))
	handleBackfillResponse(w.ctx, w.b, cdSibOnB, w.answer(t, w.a, w.stubA, cdSibOnA, w.b, syncrail.SectionChats))
	handleGapResponse(w.ctx, w.b, cdSibOnB, w.gapAnswer(t))
	got, err := w.b.events.GetByMsgID(text)
	if err != nil {
		t.Fatalf("the first text after the re-pair never reached B: %v", err)
	}
	if got.ChatID != newChatA {
		t.Fatalf("the text landed in %s, want A's new chat %s", got.ChatID, newChatA)
	}
}

func (w *twoDevices) gapAnswer(t *testing.T) syncrail.GapResponseBody {
	t.Helper()
	n := sentCount(w.stubA)
	handleGapRequest(w.ctx, w.a, cdSibOnA, syncrail.GapRequestBody{})
	var out [][]byte
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, p := range sentSince(w.stubA, n) {
			env, err := syncrail.OpenEnvelope(mustRailKey(t, w.b), p)
			if err == nil && env.Kind == syncrail.KindGapResponse {
				out = append(out, p)
			}
		}
		if len(out) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(out) == 0 {
		t.Fatal("A sent no gap-fill answer")
	}
	resp, err := syncrail.ParseGapResponse(openGapEnvelope(t, w.b, out[0]))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func mustRailKey(t *testing.T, d *daemon) []byte {
	t.Helper()
	key, err := d.railKey()
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestContactDelete_AnOperatorDeleteBeatsAFutureTombstone(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	stub.MissingPeers = map[string]bool{}
	d, addr, certPath, token := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	now := time.Now().Unix()
	ahead := now + 600
	if _, _, err := d.peerMeta.MarkDeleted(cdDaveFinger, ahead, now-100); err != nil {
		t.Fatal(err)
	}
	if err := d.peerMeta.MarkDeleteDone(cdDaveFinger, ahead); err != nil {
		t.Fatal(err)
	}
	chatID := seedDaveAt(t, d, cdDaveNewA, now-50)
	stub.PeerList = []backendapi.Peer{{ID: cdDaveNewA}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	deleteOverIPC(t, ctx, conn, "pa-del", cdDaveNewA)
	stub.WaitForSend(t, 1, 5*time.Second)

	if got := stub.PeerActionIDs; len(got) != 1 || got[0] != cdDaveNewA {
		t.Fatalf("haomad was asked to delete %v, want [%s]", got, cdDaveNewA)
	}
	if _, err := d.chats.Get(chatID); !errors.Is(err, chat.ErrNotFound) {
		t.Fatalf("the chat survived: %v", err)
	}
	tomb, _, _ := d.peerMeta.Tombstone(cdDaveFinger)
	if tomb.DeletedAt <= ahead || !tomb.Done {
		t.Fatalf("tombstone = %+v, want one later than %d and done", tomb, ahead)
	}
	sent := contactDeletesIn(t, d, sentSince(stub, 0))
	if len(sent) != 1 || sent[0].Deleted[0].DeletedAt != tomb.DeletedAt {
		t.Fatalf("pushed %+v, want the new time %d", sent, tomb.DeletedAt)
	}
}

func TestContactDelete_ASweptRowStillCrosses(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	deletedAt := time.Now().Unix() - 100
	if _, _, err := d.peerMeta.MarkDeleted(cdDaveFinger, deletedAt, deletedAt); err != nil {
		t.Fatal(err)
	}
	chatID := seedDaveAt(t, d, cdDaveNewA, deletedAt+10)
	id := appendRowAt(t, d, chatID, "swept-later", deletedAt+20)

	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{
		RangeEnd: 3,
		Rows:     []syncrail.RowSnapshot{{Fingerprint: cdDaveFinger, MsgID: id, Swept: true}},
	})
	got, err := d.events.GetByMsgID(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.DeletedAt == 0 {
		t.Fatal("a swept row of the new chat was skipped as history from before the delete")
	}
}

func TestContactDelete_AKeptChatIsPushedWithItsNewUnreadCount(t *testing.T) {
	w := newTwoDevices(t)
	deletedAt := time.Now().Unix() - 100
	if _, _, err := w.b.peerMeta.SetFingerprintAt(cdDaveNewB, cdDaveFinger, deletedAt+10); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.b.createDirectWithDefaults(cdDaveNewB); err != nil {
		t.Fatal(err)
	}
	w.stubB.PeerList = append(w.stubB.PeerList, backendapi.Peer{ID: cdDaveNewB})
	appendRowAt(t, w.b, w.chatB, "unread-after", time.Now().Unix()-5)
	recomputeChatUnread(w.ctx, w.b, w.chatB)
	if c := readChat(t, w.b, w.chatB); c.UnreadCount != 4 {
		t.Fatalf("precondition: unread = %d, want 4", c.UnreadCount)
	}

	applyDeletedContacts(w.ctx, w.b, slog.Default(),
		[]syncrail.DeletedContact{{Fingerprint: cdDaveFinger, DeletedAt: deletedAt}}, "sibling_push")

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f := readUntil(t, w.ctx, w.connB, ipc.FrameChatUpdated)
		var u ipc.ChatUpdatedPayload
		_ = json.Unmarshal(f.Payload, &u)
		if u.Chat.ChatID != string(w.chatB) {
			continue
		}
		if u.Chat.UnreadCount != 1 {
			t.Fatalf("chat.updated carries unread %d, want 1: the erased rows are still counted", u.Chat.UnreadCount)
		}
		return
	}
	t.Fatal("no chat.updated for the kept chat")
}

func TestUnplacedMirrorSweep_BacksOff(t *testing.T) {
	var u unplacedMirrorSweep
	t0 := time.Unix(1_800_000_000, 0)
	if w := u.nextWait(t0); w != unplacedSweepSettle {
		t.Fatalf("first ask waits %s, want the settle %s", w, unplacedSweepSettle)
	}
	u.last = t0
	gaps := []time.Duration{}
	now := t0
	for i := 0; i < 8; i++ {
		now = now.Add(time.Second)
		u.nextWait(now)
		gaps = append(gaps, u.gap)
		u.last = now
	}
	if gaps[0] != 2*unplacedSweepGap || gaps[len(gaps)-1] != unplacedSweepMaxGap {
		t.Fatalf("gaps %v, want doubling from %s up to %s", gaps, 2*unplacedSweepGap, unplacedSweepMaxGap)
	}
	u.nextWait(now.Add(3 * unplacedSweepMaxGap))
	if u.gap != unplacedSweepGap {
		t.Fatalf("after a quiet spell the gap is %s, want %s", u.gap, unplacedSweepGap)
	}
}
