package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/notify"
	"haoma-frontend/internal/signal"
	"haoma-frontend/internal/syncrail"
)

func adoptIdentityFrom(t *testing.T, dst, src *daemon) {
	t.Helper()
	kp := src.stores.GetIdentityKeyPair()
	if kp == nil {
		t.Fatal("source daemon has no identity keypair")
	}
	pub := kp.PublicKey().Serialize()
	priv := kp.PrivateKey().Serialize()
	if err := signal.AdoptIdentity(dst.store, dst.signalState, pub[:], priv[:]); err != nil {
		t.Fatalf("adopt identity: %v", err)
	}
	dst.stores = signal.NewStores(dst.store, dst.signalState)
}

func sealGapEntry(t *testing.T, d *daemon, fromPeerID string, kind syncrail.Kind, body any) (payload []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return sealRailEnvelope(t, d, syncrail.Envelope{
		V:    syncrail.Version,
		Kind: kind,
		Ts:   time.Now().Unix(),
		Body: raw,
	})
}

func openGapEnvelope(t *testing.T, d *daemon, payload []byte) syncrail.Envelope {
	t.Helper()
	key, err := d.railKey()
	if err != nil {
		t.Fatal(err)
	}
	env, err := syncrail.OpenEnvelope(key, payload)
	if err != nil {
		t.Fatalf("open rail envelope: %v", err)
	}
	return env
}

func seedText(t *testing.T, d *daemon, chatID chat.ChatID, text string) string {
	t.Helper()
	id, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(events.TextBody{Text: text})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.events.AppendOutbound(events.OutboundParams{
		ChatID: chatID,
		Kind:   events.KindText,
		MsgID:  id,
		Body:   body,
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func seedTextOn(t *testing.T, a, b *daemon, chatA, chatB chat.ChatID, text string) string {
	t.Helper()
	id, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(events.TextBody{Text: text})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []struct {
		d      *daemon
		chatID chat.ChatID
	}{{a, chatA}, {b, chatB}} {
		if _, err := w.d.events.AppendOutbound(events.OutboundParams{
			ChatID: w.chatID, Kind: events.KindText, MsgID: id,
			DisplayTs: 1742600000, Body: body,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func chatIDFor(t *testing.T, d *daemon, peerID string) chat.ChatID {
	t.Helper()
	dc, err := d.chats.GetByDirectPeer(peerID)
	if err != nil {
		t.Fatal(err)
	}
	return dc.ID
}

func TestGapFill_RequestReachesTheAnswerArm(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)
	seedText(t, d, chatID, "something the sibling missed")

	payload := sealGapEntry(t, d, railSiblingID, syncrail.KindGapRequest,
		syncrail.GapRequestBody{Watermark: 0})

	processSiblingRailEnvelope(context.Background(), d, railEntry(railSiblingID, "env-1", payload))
	stub.WaitForSend(t, 1, 5*time.Second)

	env := openGapEnvelope(t, d, stub.LastSendReq.Payload)
	if env.Kind != syncrail.KindGapResponse {
		t.Fatalf("answered with %q, want a gap response", env.Kind)
	}
	if stub.LastSendReq.PeerID != railSiblingID {
		t.Fatalf("answered %q, want the asking sibling", stub.LastSendReq.PeerID)
	}

	if stub.LastSendReq.Kind != "" {
		t.Fatalf("wire-kind hint leaked to the router: %q", stub.LastSendReq.Kind)
	}
}

func TestGapFill_AnswerCarriesRowsKeyedByFingerprint(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)
	msgID := seedText(t, d, chatID, "hello from the device that was up")

	payload := sealGapEntry(t, d, railSiblingID, syncrail.KindGapRequest,
		syncrail.GapRequestBody{Watermark: 0})
	processSiblingRailEnvelope(context.Background(), d, railEntry(railSiblingID, "env-1", payload))
	stub.WaitForSend(t, 1, 5*time.Second)

	var resp syncrail.GapResponseBody
	env := openGapEnvelope(t, d, stub.LastSendReq.Payload)
	if err := json.Unmarshal(env.Body, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(resp.Rows))
	}
	row := resp.Rows[0]
	if row.MsgID != msgID {
		t.Errorf("msg id = %q, want %q", row.MsgID, msgID)
	}
	if row.Fingerprint != railFingerprint {
		t.Errorf("fingerprint = %q, want %q", row.Fingerprint, railFingerprint)
	}
	if row.Kind != string(events.KindText) {
		t.Errorf("kind = %q", row.Kind)
	}
	if resp.RangeEnd == 0 {
		t.Error("response names no range end; the asker would sweep the same range forever")
	}
}

func TestGapFill_EmptyAnswerStillNamesARange(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)
	seedText(t, d, chatID, "already known over there")

	head, err := d.events.JournalHead()
	if err != nil {
		t.Fatal(err)
	}

	payload := sealGapEntry(t, d, railSiblingID, syncrail.KindGapRequest,
		syncrail.GapRequestBody{Watermark: head})
	processSiblingRailEnvelope(context.Background(), d, railEntry(railSiblingID, "env-1", payload))
	stub.WaitForSend(t, 1, 5*time.Second)

	var resp syncrail.GapResponseBody
	env := openGapEnvelope(t, d, stub.LastSendReq.Payload)
	if err := json.Unmarshal(env.Body, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Rows) != 0 {
		t.Fatalf("want an empty batch, got %d rows", len(resp.Rows))
	}
	if resp.RangeEnd != head {
		t.Fatalf("range end = %d, want the current head %d", resp.RangeEnd, head)
	}
}

func TestGapFill_ReciprocityTerminates(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	seedKnownPeer(t, d, railPeerID, railFingerprint)

	payload := sealGapEntry(t, d, railSiblingID, syncrail.KindGapRequest,
		syncrail.GapRequestBody{Watermark: 0, Reciprocate: true})
	processSiblingRailEnvelope(context.Background(), d, railEntry(railSiblingID, "env-1", payload))

	stub.WaitForSend(t, 2, 5*time.Second)

	env := openGapEnvelope(t, d, stub.LastSendReq.Payload)
	if env.Kind != syncrail.KindGapRequest {
		t.Fatalf("second send was %q, want the reciprocal request", env.Kind)
	}
	var req syncrail.GapRequestBody
	if err := json.Unmarshal(env.Body, &req); err != nil {
		t.Fatal(err)
	}
	if req.Reciprocate {
		t.Fatal("the reciprocal request asks for another reciprocal — the exchange would never terminate")
	}
}

func TestGapFill_AnswersExactlyOneBatchPerRequest(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)

	for i := 0; i < gapBatchRows+10; i++ {
		seedText(t, d, chatID, "row")
	}

	payload := sealGapEntry(t, d, railSiblingID, syncrail.KindGapRequest,
		syncrail.GapRequestBody{Watermark: 0})
	processSiblingRailEnvelope(context.Background(), d, railEntry(railSiblingID, "env-1", payload))
	stub.WaitForSend(t, 1, 5*time.Second)

	var resp syncrail.GapResponseBody
	env := openGapEnvelope(t, d, stub.LastSendReq.Payload)
	if err := json.Unmarshal(env.Body, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Rows) != gapBatchRows {
		t.Fatalf("batch carried %d rows, want the cap of %d", len(resp.Rows), gapBatchRows)
	}
	if !resp.More {
		t.Fatal("a truncated batch did not say more was waiting; the tail would be stranded")
	}

	time.Sleep(200 * time.Millisecond)
	if stub.SendCalls != 1 {
		t.Fatalf("answered with %d sends, want exactly 1 — the backlog is being streamed", stub.SendCalls)
	}
}

func TestGapFill_RequestRecordsTheSiblingsAcknowledgement(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)
	seedText(t, d, chatID, "row")

	payload := sealGapEntry(t, d, railSiblingID, syncrail.KindGapRequest,
		syncrail.GapRequestBody{Watermark: 3})
	processSiblingRailEnvelope(context.Background(), d, railEntry(railSiblingID, "env-1", payload))
	stub.WaitForSend(t, 1, 5*time.Second)

	acked, err := d.watermarks.AckedBy(railSiblingID)
	if err != nil {
		t.Fatal(err)
	}
	if acked != 3 {
		t.Fatalf("acknowledgement = %d, want 3", acked)
	}
}

func TestGapFill_FullRequestIsNotAnAcknowledgement(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	seedKnownPeer(t, d, railPeerID, railFingerprint)

	payload := sealGapEntry(t, d, railSiblingID, syncrail.KindGapRequest,
		syncrail.GapRequestBody{Watermark: 99, Full: true})
	processSiblingRailEnvelope(context.Background(), d, railEntry(railSiblingID, "env-1", payload))
	stub.WaitForSend(t, 1, 5*time.Second)

	if acked, _ := d.watermarks.AckedBy(railSiblingID); acked != 0 {
		t.Fatalf("a full-sync request was recorded as an acknowledgement of %d", acked)
	}
}

func TestGapFill_JournalPrunesOnlyOnAnAcknowledgement(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)
	for i := 0; i < 4; i++ {
		seedText(t, d, chatID, "row")
	}

	if _, err := d.watermarks.Advance(railSiblingID, 9999); err != nil {
		t.Fatal(err)
	}
	pruneJournalForSiblings(d)
	if entries, _ := d.events.JournalSince(0, 0); len(entries) != 4 {
		t.Fatalf("journal pruned to %d entries on a held cursor; that number has no local meaning", len(entries))
	}

	if _, err := d.watermarks.RecordAck(railSiblingID, 2); err != nil {
		t.Fatal(err)
	}
	pruneJournalForSiblings(d)
	entries, err := d.events.JournalSince(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("journal has %d entries after acknowledging 2 of 4, want 2", len(entries))
	}
}

func TestGapFill_ResponseInsertsAMissedRowAndAdvancesTheCursor(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	seedKnownPeer(t, d, railPeerID, railFingerprint)

	body, _ := json.Marshal(events.TextBody{Text: "sent while this device was off"})
	resp := syncrail.GapResponseBody{
		RangeEnd: 17,
		Rows: []syncrail.RowSnapshot{{
			Fingerprint: railFingerprint,
			MsgID:       "missed-1",
			Kind:        string(events.KindText),
			Inbound:     true,
			DisplayTs:   1742600000,
			SenderTs:    1742600000,
			Body:        body,
		}},
	}
	handleGapResponse(context.Background(), d, railSiblingID, resp)

	got, err := d.events.GetByMsgID("missed-1")
	if err != nil {
		t.Fatalf("missed row not inserted: %v", err)
	}
	if got.Direction != events.DirIn {
		t.Errorf("direction = %q, want inbound", got.Direction)
	}

	if got.DisplayTs != 1742600000 {
		t.Errorf("display_ts = %d, want the origin's 1742600000", got.DisplayTs)
	}

	if got.SenderPeerID != railPeerID {
		t.Errorf("sender peer id = %q, want this device's %q", got.SenderPeerID, railPeerID)
	}

	wm, err := d.watermarks.Get(railSiblingID)
	if err != nil {
		t.Fatal(err)
	}
	if wm != 17 {
		t.Fatalf("watermark = %d, want 17", wm)
	}
}

func TestGapFill_RecoveredOutboundRowDoesNotSeedTheBottomStep(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	seedKnownPeer(t, d, railPeerID, railFingerprint)

	body, _ := json.Marshal(events.TextBody{Text: "sent from my other device while this one was off"})
	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{
		RangeEnd: 23,
		Rows: []syncrail.RowSnapshot{{
			Fingerprint: railFingerprint,
			MsgID:       "recovered-out-1",
			Kind:        string(events.KindText),
			DisplayTs:   1742600000,
			Body:        body,
		}},
	})

	got, err := d.events.GetByMsgID("recovered-out-1")
	if err != nil {
		t.Fatalf("recovered row not inserted: %v", err)
	}
	if got.Direction != events.DirOut {
		t.Fatalf("direction = %q, want outbound — this test asserts the delivery ladder", got.Direction)
	}
	if got.DeliveryState != events.DeliverySiblingFloor {
		t.Errorf("delivery_state = %q, want the sibling floor %q", got.DeliveryState, events.DeliverySiblingFloor)
	}
}

func TestGapFill_RecoveredOutboundFileRowDoesNotSeedTheBottomStep(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	seedKnownPeer(t, d, railPeerID, railFingerprint)

	body, _ := json.Marshal(struct {
		State string `json:"state"`
	}{State: "pending"})
	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{
		RangeEnd: 23,
		Rows: []syncrail.RowSnapshot{{
			Fingerprint: railFingerprint,
			MsgID:       "recovered-file-1",
			Kind:        string(events.KindFile),
			DisplayTs:   1742600000,
			Body:        body,
		}},
	})

	got, err := d.events.GetByMsgID("recovered-file-1")
	if err != nil {
		t.Fatalf("recovered row not inserted: %v", err)
	}
	if got.Direction != events.DirOut {
		t.Fatalf("direction = %q, want outbound — this test asserts the delivery ladder", got.Direction)
	}
	if got.DeliveryState != events.DeliverySiblingFloor {
		t.Errorf("delivery_state = %q, want the sibling floor %q", got.DeliveryState, events.DeliverySiblingFloor)
	}
}

func TestGapFill_RecoveredOutboundFileRowKeepsAStepAboveTheFloor(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	seedKnownPeer(t, d, railPeerID, railFingerprint)

	body, _ := json.Marshal(struct {
		State string `json:"state"`
	}{State: "pending"})
	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{
		RangeEnd: 23,
		Rows: []syncrail.RowSnapshot{{
			Fingerprint:  railFingerprint,
			MsgID:        "recovered-file-2",
			Kind:         string(events.KindFile),
			DisplayTs:    1742600000,
			Body:         body,
			DeliveryRank: events.DeliveryDelivered,
		}},
	})

	got, err := d.events.GetByMsgID("recovered-file-2")
	if err != nil {
		t.Fatalf("recovered row not inserted: %v", err)
	}
	if got.DeliveryState != events.DeliveryDelivered {
		t.Errorf("delivery_state = %q, want %q — the snapshot's named step was discarded for the floor",
			got.DeliveryState, events.DeliveryDelivered)
	}
}

func TestGapFill_ApplyingABatchTwiceIsANoOp(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)

	body, _ := json.Marshal(events.TextBody{Text: "twice"})
	resp := syncrail.GapResponseBody{
		RangeEnd: 5,
		Rows: []syncrail.RowSnapshot{{
			Fingerprint: railFingerprint,
			MsgID:       "dup-1",
			Kind:        string(events.KindText),
			DisplayTs:   1742600000,
			Body:        body,
		}},
	}
	handleGapResponse(context.Background(), d, railSiblingID, resp)
	handleGapResponse(context.Background(), d, railSiblingID, resp)

	rows, err := d.events.List(chatID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range rows {
		if r.MsgID == "dup-1" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("row present %d times, want 1", n)
	}
}

func countRows(t *testing.T, d *daemon, chatID chat.ChatID, msgID string) (int, []events.Event) {
	t.Helper()
	page, err := d.events.ListBefore(chatID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range page {
		if r.MsgID == msgID {
			n++
		}
	}
	return n, page
}

func TestArrival_GapFillThenPeerEnvelope_IsOneRow(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)

	const senderTs int64 = 1742643890
	blob, msgID := makeInboundCiphertext(t, d, railPeerID, "sync edit target", 2, senderTs, 0)

	body, _ := json.Marshal(events.TextBody{Text: "sync edit target"})
	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{
		RangeEnd: 9,
		Rows: []syncrail.RowSnapshot{{
			Fingerprint: railFingerprint,
			MsgID:       msgID,
			Kind:        string(events.KindText),
			Inbound:     true,
			DisplayTs:   senderTs,
			SenderTs:    senderTs,
			Body:        body,
		}},
	})

	processInboxEntry(context.Background(), d, backendapi.InboxEntry{
		ArrivalAt: time.Now().UnixNano(),
		PeerID:    railPeerID,
		Envelope: backendapi.RawEnvelope{
			ID: "env-peer-copy-1", Timestamp: senderTs, From: "peer-onion",
			Kind: "text", Payload: blob, Mac: []byte{1, 2, 3, 4},
		},
	})

	if n, _ := countRows(t, d, chatID, msgID); n != 1 {
		t.Fatalf("the chat holds the message %d times, want 1", n)
	}

	if _, err := d.events.ApplyEdit(msgID, "edited over there", senderTs+5, railPeerID); err != nil {
		t.Fatalf("apply edit: %v", err)
	}
	_, page := countRows(t, d, chatID, msgID)
	for _, r := range page {
		if r.MsgID == msgID && r.EditedAt == 0 {
			t.Fatalf("a copy of the message in the page is unedited (recv_seq %d) — the edit landed on a row nobody reads", r.RecvSeq)
		}
	}
}

func TestArrival_PeerEnvelopeThenGapFill_TheSweepLeavesTheRowAlone(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)

	const senderTs int64 = 1742643890
	blob, msgID := makeInboundCiphertext(t, d, railPeerID, "arrived here first", 2, senderTs, 0)
	processInboxEntry(context.Background(), d, backendapi.InboxEntry{
		ArrivalAt: time.Now().UnixNano(),
		PeerID:    railPeerID,
		Envelope: backendapi.RawEnvelope{
			ID: "env-peer-copy-2", Timestamp: senderTs, From: "peer-onion",
			Kind: "text", Payload: blob, Mac: []byte{1, 2, 3, 4},
		},
	})

	body, _ := json.Marshal(events.TextBody{Text: "arrived here first"})
	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{
		RangeEnd: 9,
		Rows: []syncrail.RowSnapshot{{
			Fingerprint: railFingerprint,
			MsgID:       msgID,
			Kind:        string(events.KindText),
			Inbound:     true,
			DisplayTs:   senderTs,
			SenderTs:    senderTs,
			Body:        body,
		}},
	})

	if n, _ := countRows(t, d, chatID, msgID); n != 1 {
		t.Fatalf("the chat holds the message %d times, want 1", n)
	}
	held, err := d.events.GetByMsgID(msgID)
	if err != nil {
		t.Fatal(err)
	}
	if held.EnvelopeID != "env-peer-copy-2" {
		t.Errorf("the held row's envelope id is %q, want the peer's own — the sweep rewrote a row that was already right", held.EnvelopeID)
	}
	if held.EditedAt != 0 {
		t.Errorf("the sweep stamped an edit (edited_at=%d) onto an unchanged row", held.EditedAt)
	}
}

func unreadCount(t *testing.T, d *daemon, chatID chat.ChatID) uint32 {
	t.Helper()
	c, err := d.chats.Get(chatID)
	if err != nil {
		t.Fatal(err)
	}
	dc, ok := c.(*chat.DirectChat)
	if !ok {
		t.Fatalf("chat %s is not a direct chat", chatID)
	}
	return dc.UnreadCount
}

func wireNotifier(t *testing.T, d *daemon) *fakeNotifyRunner {
	t.Helper()
	runner := &fakeNotifyRunner{}
	d.notifier = notify.New(runner, func(name string) (string, error) {
		if name == "notify-send" {
			return "/usr/bin/notify-send", nil
		}
		return "", errors.New("not found")
	}, "linux")
	d.settingsSnapshot.Store(&ipc.Settings{
		NotifyShellEnabled:  true,
		NotificationsOnLock: true,
	})
	return runner
}

func TestGapFill_InboundRowLandsSuppressedWhenReceiptsAreOff(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)
	if err := d.chats.SetDisableReadReceipts(chatID, true); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(events.TextBody{Text: "arrived while receipts were off"})
	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{
		RangeEnd: 11,
		Rows: []syncrail.RowSnapshot{{
			Fingerprint: railFingerprint,
			MsgID:       "recovered-quiet-1",
			Kind:        string(events.KindText),
			Inbound:     true,
			DisplayTs:   1742600000,
			SenderTs:    1742600000,
			Body:        body,
		}},
	})

	got, err := d.events.GetByMsgID("recovered-quiet-1")
	if err != nil {
		t.Fatalf("recovered row not inserted: %v", err)
	}
	if got.ReadReceiptSentAt != events.ReadReceiptSuppressedSentinel {
		t.Errorf("read_receipt_sent_at = %d, want the suppression sentinel %d — a later mark-read would ack a message received while receipts were off",
			got.ReadReceiptSentAt, events.ReadReceiptSuppressedSentinel)
	}
}

func TestArrival_GapFillThenPeerEnvelope_StillBadgesAndNotifies(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)
	runner := wireNotifier(t, d)

	const senderTs int64 = 1742643890
	const text = "the sweep got here first"
	blob, msgID := makeInboundCiphertext(t, d, railPeerID, text, 2, senderTs, 0)

	body, _ := json.Marshal(events.TextBody{Text: text})
	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{
		RangeEnd: 9,
		Rows: []syncrail.RowSnapshot{{
			Fingerprint: railFingerprint,
			MsgID:       msgID,
			Kind:        string(events.KindText),
			Inbound:     true,
			DisplayTs:   senderTs,
			SenderTs:    senderTs,
			Body:        body,
		}},
	})

	if got := unreadCount(t, d, chatID); got != 1 {
		t.Fatalf("unread = %d after an insert-only sweep, want 1 — the row it placed is genuinely unread", got)
	}
	assertNoCalls(t, runner)

	processInboxEntry(context.Background(), d, backendapi.InboxEntry{
		ArrivalAt: time.Now().UnixNano(),
		PeerID:    railPeerID,
		Envelope: backendapi.RawEnvelope{
			ID: "env-peer-copy-3", Timestamp: senderTs, From: "peer-onion",
			Kind: "text", Payload: blob, Mac: []byte{1, 2, 3, 4},
		},
	})

	if got := unreadCount(t, d, chatID); got != 2 {
		t.Errorf("unread = %d after the peer's envelope arrived, want 2", got)
	}
	waitForNotifyCalls(t, runner, 1)
	if n, _ := countRows(t, d, chatID, msgID); n != 1 {
		t.Errorf("the chat holds the message %d times, want 1", n)
	}
}

func TestGapFill_RecoveredRowsBadgeWhenTheSameBatchConvergesAReadTime(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)

	body, _ := json.Marshal(events.TextBody{Text: "already shared"})
	sharedMsgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.events.AppendInbound(events.InboundParams{
		ChatID: chatID, Kind: events.KindText, MsgID: sharedMsgID,
		SenderPeerID: railPeerID, SenderTs: 1742600000, DisplayTs: 1742600000,
		Status: events.DecryptOK, Body: body,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.chats.IncrementUnread(chatID); err != nil {
		t.Fatal(err)
	}

	newID1, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	newID2, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	newBody, _ := json.Marshal(events.TextBody{Text: "never had this one"})

	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{
		RangeEnd: 30,
		Rows: []syncrail.RowSnapshot{
			{
				Fingerprint: railFingerprint, MsgID: sharedMsgID, Kind: string(events.KindText),
				Inbound: true, DisplayTs: 1742600000, Body: body, ReadAt: 1742600500,
			},
			{
				Fingerprint: railFingerprint, MsgID: newID1, Kind: string(events.KindText),
				Inbound: true, DisplayTs: 1742600100, SenderTs: 1742600100, Body: newBody,
			},
			{
				Fingerprint: railFingerprint, MsgID: newID2, Kind: string(events.KindText),
				Inbound: true, DisplayTs: 1742600200, SenderTs: 1742600200, Body: newBody,
			},
		},
	})

	for _, id := range []string{sharedMsgID, newID1, newID2} {
		if _, err := d.events.GetByMsgID(id); err != nil {
			t.Fatalf("msg_id=%s did not land: %v", id, err)
		}
	}
	if got := unreadCount(t, d, chatID); got != 2 {
		t.Fatalf("unread = %d, want 2 — the two rows this batch inserted are genuinely unread "+
			"everywhere, and the recount the shared row's read time triggered must find them", got)
	}
}

func TestGapFill_RecountFiresOncePerBatchNotPerRow(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)

	body, _ := json.Marshal(events.TextBody{Text: "hi"})
	msgIDs := make([]string, 3)
	for i := range msgIDs {
		id, err := msg.NewID()
		if err != nil {
			t.Fatal(err)
		}
		msgIDs[i] = id
		if _, err := d.events.AppendInbound(events.InboundParams{
			ChatID: chatID, Kind: events.KindText, MsgID: id,
			SenderPeerID: railPeerID, SenderTs: 1742600000, DisplayTs: 1742600000,
			Status: events.DecryptOK, Body: body,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := d.chats.IncrementUnread(chatID); err != nil {
			t.Fatal(err)
		}
	}

	buf := &syncBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	rows := make([]syncrail.RowSnapshot, len(msgIDs))
	for i, id := range msgIDs {
		rows[i] = syncrail.RowSnapshot{
			Fingerprint: railFingerprint, MsgID: id, Kind: string(events.KindText),
			Inbound: true, DisplayTs: 1742600000, Body: body, ReadAt: 1742600500,
		}
	}
	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{
		RangeEnd: 30,
		Rows:     rows,
	})

	if got := unreadCount(t, d, chatID); got != 0 {
		t.Fatalf("unread = %d, want 0 — all three rows converged to read", got)
	}
	recomputes := strings.Count(buf.String(), `msg="recomputeChatUnread: called"`)
	if recomputes != 1 {
		t.Fatalf("recomputeChatUnread ran %d time(s) across one batch converging three rows in one "+
			"conversation, want 1 — a recount inside the per-row loop instead of once after it turns "+
			"an N-row sweep into N full scans of the conversation", recomputes)
	}
}

func TestGapFill_InterruptedBatchStillRecountsOnReplay(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)

	body, _ := json.Marshal(events.TextBody{Text: "recovered but never badged"})
	msgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	row := syncrail.RowSnapshot{
		Fingerprint: railFingerprint, MsgID: msgID, Kind: string(events.KindText),
		Inbound: true, DisplayTs: 1742600000, SenderTs: 1742600000, Body: body,
	}

	logger := slog.With(slog.String("sibling_peer_id", railSiblingID))
	if _, _, err := applyGapRow(context.Background(), d, logger, row); err != nil {
		t.Fatal(err)
	}
	if got := unreadCount(t, d, chatID); got != 0 {
		t.Fatalf("test fixture is wrong: unread = %d before any recount, want 0 (stale)", got)
	}

	buf := &syncBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{
		RangeEnd: 5,
		Rows:     []syncrail.RowSnapshot{row},
	})

	if !strings.Contains(buf.String(), `msg="recomputeChatUnread: called"`) {
		t.Fatalf("recomputeChatUnread never ran on the replay — a batch that changes nothing must still recount")
	}
	if got := unreadCount(t, d, chatID); got != 1 {
		t.Fatalf("unread = %d after the replay, want 1 — the badge was stale from the interrupted sweep", got)
	}
}

func TestGapFill_RecountPushesWhenTheCountChanges(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)

	body, _ := json.Marshal(events.TextBody{Text: "will be read"})
	msgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.events.AppendInbound(events.InboundParams{
		ChatID: chatID, Kind: events.KindText, MsgID: msgID,
		SenderPeerID: railPeerID, SenderTs: 1742600000, DisplayTs: 1742600000,
		Status: events.DecryptOK, Body: body,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.chats.IncrementUnread(chatID); err != nil {
		t.Fatal(err)
	}

	buf := &syncBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{
		RangeEnd: 5,
		Rows: []syncrail.RowSnapshot{{
			Fingerprint: railFingerprint, MsgID: msgID, Kind: string(events.KindText),
			Inbound: true, DisplayTs: 1742600000, Body: body, ReadAt: 1742600500,
		}},
	})

	if got := unreadCount(t, d, chatID); got != 0 {
		t.Fatalf("unread = %d after the read time converged, want 0", got)
	}
	if !strings.Contains(buf.String(), `msg="recomputeChatUnread: count moved"`) {
		t.Fatalf("recomputeChatUnread ran but did not log (and therefore did not push) the change — " +
			"the stored value moved from 1 to 0 and the client update was dropped")
	}
}

func TestArrival_GapFillThenLiveMirror_IsOneRow(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)

	wrapper := railText(t, "sent from my other device", 4, 1742600000, 0)
	body, _ := json.Marshal(events.TextBody{Text: "sent from my other device"})
	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{
		RangeEnd: 9,
		Rows: []syncrail.RowSnapshot{{
			Fingerprint: railFingerprint,
			MsgID:       wrapper.MsgID,
			Kind:        string(events.KindText),
			DisplayTs:   1742600000,
			Body:        body,
		}},
	})

	processInboxEntry(context.Background(), d, railEntry(
		railSiblingID, "env-mirror-late-1", sealMirror(t, d, railFingerprint, wrapper),
	))

	if n, _ := countRows(t, d, chatID, wrapper.MsgID); n != 1 {
		t.Fatalf("the chat holds the message %d times, want 1", n)
	}
}

func TestGapFill_ResponseMergesANewerEdit(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)
	msgID := seedText(t, d, chatID, "original")

	body, _ := json.Marshal(events.TextBody{Text: "edited over there"})
	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{
		RangeEnd: 9,
		Rows: []syncrail.RowSnapshot{{
			Fingerprint: railFingerprint,
			MsgID:       msgID,
			Kind:        string(events.KindText),
			DisplayTs:   1742600000,
			Body:        body,
			EditedAt:    1742700000,
		}},
	})

	got, err := d.events.GetByMsgID(msgID)
	if err != nil {
		t.Fatal(err)
	}
	var text events.TextBody
	if err := json.Unmarshal(got.Body, &text); err != nil {
		t.Fatal(err)
	}
	if text.Text != "edited over there" {
		t.Fatalf("body = %q, want the newer edit", text.Text)
	}
	if got.EditedAt != 1742700000 {
		t.Fatalf("edited_at = %d, want 1742700000", got.EditedAt)
	}
}

func TestGapFill_StaleEditDoesNotClobber(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)
	msgID := seedText(t, d, chatID, "original")
	if _, err := d.events.ApplyEdit(msgID, "newer local edit", 1742800000, ""); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(events.TextBody{Text: "older remote edit"})
	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{
		RangeEnd: 9,
		Rows: []syncrail.RowSnapshot{{
			Fingerprint: railFingerprint,
			MsgID:       msgID,
			Kind:        string(events.KindText),
			Body:        body,
			EditedAt:    1742700000,
		}},
	})

	got, _ := d.events.GetByMsgID(msgID)
	var text events.TextBody
	if err := json.Unmarshal(got.Body, &text); err != nil {
		t.Fatal(err)
	}
	if text.Text != "newer local edit" {
		t.Fatalf("body = %q, want the newer local edit to survive", text.Text)
	}
}

func TestGapFill_ResponseAppliesATombstone(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)
	msgID := seedText(t, d, chatID, "delete me")

	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{
		RangeEnd: 9,
		Rows: []syncrail.RowSnapshot{{
			Fingerprint: railFingerprint,
			MsgID:       msgID,
			Kind:        string(events.KindText),
			DeletedAt:   1742700000,
		}},
	})

	got, err := d.events.GetByMsgID(msgID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DeletedAt == 0 {
		t.Fatal("row not tombstoned; deleted content still shows on this device")
	}
	if len(got.Body) != 0 {
		t.Fatal("tombstoned row still carries a body")
	}
}

func TestGapFill_SweptRowShipsATombstone(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)
	msgID := seedText(t, d, chatID, "will be swept")

	if _, _, err := d.events.DeleteByChat(chatID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.events.GetByMsgID(msgID); err == nil {
		t.Fatal("precondition: row still present after the sweep")
	}

	payload := sealGapEntry(t, d, railSiblingID, syncrail.KindGapRequest,
		syncrail.GapRequestBody{Watermark: 0})
	processSiblingRailEnvelope(context.Background(), d, railEntry(railSiblingID, "env-1", payload))
	stub.WaitForSend(t, 1, 5*time.Second)

	var resp syncrail.GapResponseBody
	env := openGapEnvelope(t, d, stub.LastSendReq.Payload)
	if err := json.Unmarshal(env.Body, &resp); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, row := range resp.Rows {
		if row.MsgID == msgID {
			found = true
			if !row.Swept {
				t.Fatal("swept row shipped without its tombstone flag")
			}
		}
	}
	if !found {
		t.Fatal("swept row was skipped entirely; its delete would never propagate")
	}
}

func TestGapFill_UnknownFingerprintIsDropped(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)

	body, _ := json.Marshal(events.TextBody{Text: "from a stranger"})
	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{
		RangeEnd: 3,
		Rows: []syncrail.RowSnapshot{{
			Fingerprint: "ffffffffffffffffffffffffffffffff",
			MsgID:       "orphan-1",
			Kind:        string(events.KindText),
			Body:        body,
		}},
	})

	if _, err := d.events.GetByMsgID("orphan-1"); err == nil {
		t.Fatal("a row for an unknown fingerprint was materialised")
	}

	if wm, _ := d.watermarks.Get(railSiblingID); wm != 0 {
		t.Fatalf("watermark = %d, want 0 — the cursor advanced past a row this device could not route yet, losing it permanently", wm)
	}
}

func TestGapFill_MalformedRowDoesNotStallTheCursor(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	seedKnownPeer(t, d, railPeerID, railFingerprint)

	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{
		RangeEnd: 4,
		Rows: []syncrail.RowSnapshot{{

			Fingerprint: railFingerprint,
		}},
	})

	if wm, _ := d.watermarks.Get(railSiblingID); wm != 4 {
		t.Fatalf("watermark = %d, want 4 — a permanently-bad row stalled the cursor", wm)
	}
}

func TestGapFill_HeldRowAppliesOnceTheContactAppears(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)

	body, _ := json.Marshal(events.TextBody{Text: "sent before this device met them"})
	batch := syncrail.GapResponseBody{
		RangeEnd: 6,
		Rows: []syncrail.RowSnapshot{{
			Fingerprint: railFingerprint,
			MsgID:       "later-1",
			Kind:        string(events.KindText),
			Inbound:     true,
			DisplayTs:   1742600000,
			Body:        body,
		}},
	}

	handleGapResponse(context.Background(), d, railSiblingID, batch)
	if _, err := d.events.GetByMsgID("later-1"); err == nil {
		t.Fatal("row applied against a contact this device had not met")
	}
	if wm, _ := d.watermarks.Get(railSiblingID); wm != 0 {
		t.Fatalf("cursor moved to %d while a row was held", wm)
	}

	seedKnownPeer(t, d, railPeerID, railFingerprint)

	handleGapResponse(context.Background(), d, railSiblingID, batch)
	if _, err := d.events.GetByMsgID("later-1"); err != nil {
		t.Fatalf("held row did not apply after the contact appeared: %v", err)
	}
	if wm, _ := d.watermarks.Get(railSiblingID); wm != 6 {
		t.Fatalf("watermark = %d, want 6 once nothing is held", wm)
	}
}

func TestGapFill_UnlinkedDeviceSendsNothing(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	seedKnownPeer(t, d, railPeerID, railFingerprint)

	sweepGapFill(context.Background(), d)

	if stub.SendCalls != 0 {
		t.Fatalf("an unlinked device sent %d rail envelopes", stub.SendCalls)
	}
}

func TestGapFill_TwoDevicesConvergeAcrossAnOfflineGap(t *testing.T) {
	stubA := startHaomadStub(t, nil, http.StatusOK)
	devA, _, _, _ := newTestDaemon(t, stubA)
	stubB := startHaomadStub(t, nil, http.StatusOK)
	devB, _, _, _ := newTestDaemon(t, stubB)
	adoptIdentityFrom(t, devB, devA)

	const peerOnA = "0000000000000000000000000000aaaa"
	const peerOnB = "0000000000000000000000000000bbbb"
	const sibOnA = "0000000000000000000000000000b0b0"
	const sibOnB = "0000000000000000000000000000a0a0"
	chatA := seedKnownPeer(t, devA, peerOnA, railFingerprint)
	seedKnownPeer(t, devB, peerOnB, railFingerprint)
	linkSibling(t, devA, sibOnA)
	linkSibling(t, devB, sibOnB)

	doomed := seedTextOn(t, devA, devB, chatA, chatIDFor(t, devB, peerOnB), "regrettable")

	kept := seedText(t, devA, chatA, "arrived while B was down")
	edited := seedText(t, devA, chatA, "first version")
	if _, err := devA.events.ApplyEdit(edited, "second version", 1742700000, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := devA.events.ApplyDelete(doomed, 1742700001, ""); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	askSibling(ctx, devB, sibOnB, false, false)
	stubB.WaitForSend(t, 1, 5*time.Second)
	processSiblingRailEnvelope(ctx, devA, railEntry(sibOnA, "req-1", stubB.LastSendReq.Payload))
	stubA.WaitForSend(t, 1, 5*time.Second)

	answer := openGapEnvelope(t, devB, stubA.LastSendReq.Payload)
	resp, err := syncrail.ParseGapResponse(answer)
	if err != nil {
		t.Fatal(err)
	}
	handleGapResponse(ctx, devB, sibOnB, resp)

	if got, err := devB.events.GetByMsgID(kept); err != nil {
		t.Fatalf("plain message did not converge: %v", err)
	} else {
		var text events.TextBody
		_ = json.Unmarshal(got.Body, &text)
		if text.Text != "arrived while B was down" {
			t.Errorf("converged body = %q", text.Text)
		}
	}

	got, err := devB.events.GetByMsgID(edited)
	if err != nil {
		t.Fatalf("edited message did not converge: %v", err)
	}
	var text events.TextBody
	_ = json.Unmarshal(got.Body, &text)
	if text.Text != "second version" {
		t.Errorf("edit did not converge: body = %q", text.Text)
	}

	tomb, err := devB.events.GetByMsgID(doomed)
	if err != nil {
		t.Fatalf("tombstone did not converge: %v", err)
	}
	if tomb.DeletedAt == 0 {
		t.Error("deleted message arrived on B without its tombstone")
	}

	if wm, _ := devB.watermarks.Get(sibOnB); wm == 0 {
		t.Error("watermark did not advance after a completed exchange")
	}
}

func TestGapFill_ReactionShipsWithItsTargetWhenTheTargetWasEditedLater(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)

	target := seedText(t, d, chatID, "react to me")
	cursor, err := d.events.JournalHead()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.events.AppendReactionBreadcrumb(target, "👍", "", "react-1", 1742700000, 0); err != nil {
		t.Fatal(err)
	}

	if _, err := d.events.ApplyEdit(target, "react to me, revised", 1742700001, ""); err != nil {
		t.Fatal(err)
	}

	payload := sealGapEntry(t, d, railSiblingID, syncrail.KindGapRequest,
		syncrail.GapRequestBody{Watermark: cursor})
	processSiblingRailEnvelope(context.Background(), d, railEntry(railSiblingID, "env-1", payload))
	stub.WaitForSend(t, 1, 5*time.Second)

	var resp syncrail.GapResponseBody
	env := openGapEnvelope(t, d, stub.LastSendReq.Payload)
	if err := json.Unmarshal(env.Body, &resp); err != nil {
		t.Fatal(err)
	}

	var reactionAt, targetAt = -1, -1
	for i, row := range resp.Rows {
		switch row.MsgID {
		case "react-1":
			reactionAt = i
		case target:
			targetAt = i
		}
	}
	if reactionAt < 0 {
		t.Fatal("reaction missing from the answer")
	}
	if targetAt < 0 {
		t.Fatal("reaction shipped without its target; the receiving device has nothing to attach it to")
	}
	if targetAt > reactionAt {
		t.Fatal("target ships after its reaction; the reaction would be dropped on arrival")
	}
}

func TestGapFill_ReadTimeConvergesAcrossAnOfflineGap(t *testing.T) {
	stubA := startHaomadStub(t, nil, http.StatusOK)
	devA, _, _, _ := newTestDaemon(t, stubA)
	stubB := startHaomadStub(t, nil, http.StatusOK)
	devB, _, _, _ := newTestDaemon(t, stubB)
	adoptIdentityFrom(t, devB, devA)

	const peerOnA = "0000000000000000000000000000aaaa"
	const peerOnB = "0000000000000000000000000000bbbb"
	const sibOnA = "0000000000000000000000000000b0b0"
	const sibOnB = "0000000000000000000000000000a0a0"
	chatA := seedKnownPeer(t, devA, peerOnA, railFingerprint)
	chatB := seedKnownPeer(t, devB, peerOnB, railFingerprint)
	linkSibling(t, devA, sibOnA)
	linkSibling(t, devB, sibOnB)

	msgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(events.TextBody{Text: "read me"})
	for _, w := range []struct {
		d      *daemon
		chatID chat.ChatID
		peer   string
	}{{devA, chatA, peerOnA}, {devB, chatB, peerOnB}} {
		if _, err := w.d.events.AppendInbound(events.InboundParams{
			ChatID: w.chatID, Kind: events.KindText, MsgID: msgID,
			SenderPeerID: w.peer, SenderTs: 1742600000, DisplayTs: 1742600000,
			Status: events.DecryptOK, Body: body,
		}); err != nil {
			t.Fatal(err)
		}
	}

	if _, _, err := devA.events.MarkRead(chatA); err != nil {
		t.Fatal(err)
	}
	aRow, err := devA.events.GetByMsgID(msgID)
	if err != nil {
		t.Fatal(err)
	}
	if aRow.ReadAt == 0 {
		t.Fatal("MarkRead did not stamp A's own row")
	}
	if bRow, err := devB.events.GetByMsgID(msgID); err != nil {
		t.Fatal(err)
	} else if bRow.ReadAt != 0 {
		t.Fatal("B already shows read before any sync — the test fixture is wrong")
	}

	ctx := context.Background()
	askSibling(ctx, devB, sibOnB, false, false)
	stubB.WaitForSend(t, 1, 5*time.Second)
	processSiblingRailEnvelope(ctx, devA, railEntry(sibOnA, "req-1", stubB.LastSendReq.Payload))
	stubA.WaitForSend(t, 1, 5*time.Second)
	answer := openGapEnvelope(t, devB, stubA.LastSendReq.Payload)
	resp, err := syncrail.ParseGapResponse(answer)
	if err != nil {
		t.Fatal(err)
	}
	handleGapResponse(ctx, devB, sibOnB, resp)

	bRow, err := devB.events.GetByMsgID(msgID)
	if err != nil {
		t.Fatal(err)
	}
	if bRow.ChatID != chatB {
		t.Fatalf("converged row landed in chat %q, want %q", bRow.ChatID, chatB)
	}
	if bRow.ReadAt != aRow.ReadAt {
		t.Fatalf("B's read time = %d, want A's %d — the badge would still show unread", bRow.ReadAt, aRow.ReadAt)
	}
}

func TestGapFill_ReceiptSentClaimConvergesAndDoesNotReExport(t *testing.T) {
	stubA := startHaomadStub(t, nil, http.StatusOK)
	devA, _, _, _ := newTestDaemon(t, stubA)
	stubB := startHaomadStub(t, nil, http.StatusOK)
	devB, _, _, _ := newTestDaemon(t, stubB)
	adoptIdentityFrom(t, devB, devA)

	const peerOnA = "0000000000000000000000000000aaaa"
	const peerOnB = "0000000000000000000000000000bbbb"
	const sibOnA = "0000000000000000000000000000b0b0"
	const sibOnB = "0000000000000000000000000000a0a0"
	chatA := seedKnownPeer(t, devA, peerOnA, railFingerprint)
	chatB := seedKnownPeer(t, devB, peerOnB, railFingerprint)
	linkSibling(t, devA, sibOnA)
	linkSibling(t, devB, sibOnB)

	msgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(events.TextBody{Text: "ack me"})
	for _, w := range []struct {
		d      *daemon
		chatID chat.ChatID
		peer   string
	}{{devA, chatA, peerOnA}, {devB, chatB, peerOnB}} {
		if _, err := w.d.events.AppendInbound(events.InboundParams{
			ChatID: w.chatID, Kind: events.KindText, MsgID: msgID,
			SenderPeerID: w.peer, SenderTs: 1742600000, DisplayTs: 1742600000,
			Status: events.DecryptOK, Body: body,
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := devA.events.MarkReadReceiptSent([]string{msgID}, 1742600050); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	askSibling(ctx, devB, sibOnB, false, false)
	stubB.WaitForSend(t, 1, 5*time.Second)
	processSiblingRailEnvelope(ctx, devA, railEntry(sibOnA, "req-1", stubB.LastSendReq.Payload))
	stubA.WaitForSend(t, 1, 5*time.Second)
	answer := openGapEnvelope(t, devB, stubA.LastSendReq.Payload)
	resp, err := syncrail.ParseGapResponse(answer)
	if err != nil {
		t.Fatal(err)
	}
	handleGapResponse(ctx, devB, sibOnB, resp)

	bRow, err := devB.events.GetByMsgID(msgID)
	if err != nil {
		t.Fatal(err)
	}
	if bRow.ReadReceiptSentAt != 1742600050 {
		t.Fatalf("B's ReadReceiptSentAt = %d, want A's 1742600050 — B would ship a second receipt for the same reading",
			bRow.ReadReceiptSentAt)
	}
	if bRow.ReceiptClaimOrigin {
		t.Fatal("B's inherited claim was stamped ReceiptClaimOrigin=true — it could re-export past A and loop")
	}

	bRows, _, _, err := buildGapBatch(devB, slog.Default(), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range bRows {
		if row.MsgID != msgID {
			continue
		}
		if row.ReceiptSentAt != 0 {
			t.Fatalf("B's own answer re-shipped the inherited claim: receipt_sent_at=%d — "+
				"a third device (or A itself, echoed back) would treat this as B's own origin claim",
				row.ReceiptSentAt)
		}
	}
}

func TestGapFill_DeliveryRankConvergesAcrossAnOfflineGap(t *testing.T) {
	stubA := startHaomadStub(t, nil, http.StatusOK)
	devA, _, _, _ := newTestDaemon(t, stubA)
	stubB := startHaomadStub(t, nil, http.StatusOK)
	devB, _, _, _ := newTestDaemon(t, stubB)
	adoptIdentityFrom(t, devB, devA)

	const peerOnA = "0000000000000000000000000000aaaa"
	const peerOnB = "0000000000000000000000000000bbbb"
	const sibOnA = "0000000000000000000000000000b0b0"
	const sibOnB = "0000000000000000000000000000a0a0"
	chatA := seedKnownPeer(t, devA, peerOnA, railFingerprint)
	chatB := seedKnownPeer(t, devB, peerOnB, railFingerprint)
	linkSibling(t, devA, sibOnA)
	linkSibling(t, devB, sibOnB)

	msgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(events.TextBody{Text: "sent from A"})
	if _, err := devA.events.AppendOutbound(events.OutboundParams{
		ChatID: chatA, Kind: events.KindText, MsgID: msgID,
		EnvelopeID: "env-a-1", Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := devB.events.AppendOutbound(events.OutboundParams{
		ChatID: chatB, Kind: events.KindText, MsgID: msgID,
		DeliveryState: events.DeliverySent, MirroredFromSibling: true, Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	if _, _, err := devA.events.ApplyReadReceipt(msgID, 1742600200, chatA); err != nil {
		t.Fatal(err)
	}
	aRow, err := devA.events.GetByMsgID(msgID)
	if err != nil {
		t.Fatal(err)
	}
	if aRow.DeliveryState != events.DeliveryRead {
		t.Fatalf("A's own row did not reach %q: got %q", events.DeliveryRead, aRow.DeliveryState)
	}

	ctx := context.Background()
	askSibling(ctx, devB, sibOnB, false, false)
	stubB.WaitForSend(t, 1, 5*time.Second)
	processSiblingRailEnvelope(ctx, devA, railEntry(sibOnA, "req-1", stubB.LastSendReq.Payload))
	stubA.WaitForSend(t, 1, 5*time.Second)
	answer := openGapEnvelope(t, devB, stubA.LastSendReq.Payload)
	resp, err := syncrail.ParseGapResponse(answer)
	if err != nil {
		t.Fatal(err)
	}
	handleGapResponse(ctx, devB, sibOnB, resp)

	bRow, err := devB.events.GetByMsgID(msgID)
	if err != nil {
		t.Fatal(err)
	}
	if bRow.DeliveryState != events.DeliveryRead {
		t.Fatalf("B's delivery_state = %q, want %q — the rank did not converge", bRow.DeliveryState, events.DeliveryRead)
	}
	if bRow.ReadAt != aRow.ReadAt {
		t.Fatalf("B's ReadAt = %d, want A's %d", bRow.ReadAt, aRow.ReadAt)
	}
}

func TestGapFill_DeliveryRankConvergesWithoutAReadTime(t *testing.T) {
	stubA := startHaomadStub(t, nil, http.StatusOK)
	devA, _, _, _ := newTestDaemon(t, stubA)
	stubB := startHaomadStub(t, nil, http.StatusOK)
	devB, _, _, _ := newTestDaemon(t, stubB)
	adoptIdentityFrom(t, devB, devA)

	const peerOnA = "0000000000000000000000000000aaaa"
	const peerOnB = "0000000000000000000000000000bbbb"
	const sibOnA = "0000000000000000000000000000b0b0"
	const sibOnB = "0000000000000000000000000000a0a0"
	chatA := seedKnownPeer(t, devA, peerOnA, railFingerprint)
	chatB := seedKnownPeer(t, devB, peerOnB, railFingerprint)
	linkSibling(t, devA, sibOnA)
	linkSibling(t, devB, sibOnB)

	msgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(struct {
		State string `json:"state"`
	}{State: "pending"})
	if _, err := devA.events.AppendOutbound(events.OutboundParams{
		ChatID: chatA, Kind: events.KindFile, MsgID: msgID,
		EnvelopeID: "env-a-1", Body: body,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := devB.events.AppendOutbound(events.OutboundParams{
		ChatID: chatB, Kind: events.KindFile, MsgID: msgID,
		DeliveryState: events.DeliverySent, MirroredFromSibling: true, Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := devA.events.ApplyDeliveredReceipt(msgID, chatA); err != nil {
		t.Fatal(err)
	}
	aRow, err := devA.events.GetByMsgID(msgID)
	if err != nil {
		t.Fatal(err)
	}
	if aRow.DeliveryState != events.DeliveryDelivered {
		t.Fatalf("A's own row did not reach %q: got %q", events.DeliveryDelivered, aRow.DeliveryState)
	}
	if aRow.ReadAt != 0 {
		t.Fatalf("test fixture is wrong: A's ReadAt = %d, want 0 — this case must isolate the rank from the read axis", aRow.ReadAt)
	}

	ctx := context.Background()
	askSibling(ctx, devB, sibOnB, false, false)
	stubB.WaitForSend(t, 1, 5*time.Second)
	processSiblingRailEnvelope(ctx, devA, railEntry(sibOnA, "req-1", stubB.LastSendReq.Payload))
	stubA.WaitForSend(t, 1, 5*time.Second)
	answer := openGapEnvelope(t, devB, stubA.LastSendReq.Payload)
	resp, err := syncrail.ParseGapResponse(answer)
	if err != nil {
		t.Fatal(err)
	}
	handleGapResponse(ctx, devB, sibOnB, resp)

	bRow, err := devB.events.GetByMsgID(msgID)
	if err != nil {
		t.Fatal(err)
	}
	if bRow.DeliveryState != events.DeliveryDelivered {
		t.Fatalf("B's delivery_state = %q, want %q — the rank merge did not run", bRow.DeliveryState, events.DeliveryDelivered)
	}
	if bRow.ReadAt != 0 {
		t.Fatalf("B's ReadAt = %d, want 0 — this row was never read", bRow.ReadAt)
	}
}

func TestGapFill_DeliveryFailureConvergesAcrossAnOfflineGap(t *testing.T) {
	stubA := startHaomadStub(t, nil, http.StatusOK)
	devA, _, _, _ := newTestDaemon(t, stubA)
	stubB := startHaomadStub(t, nil, http.StatusOK)
	devB, _, _, _ := newTestDaemon(t, stubB)
	adoptIdentityFrom(t, devB, devA)

	const peerOnA = "0000000000000000000000000000aaaa"
	const peerOnB = "0000000000000000000000000000bbbb"
	const sibOnA = "0000000000000000000000000000b0b0"
	const sibOnB = "0000000000000000000000000000a0a0"
	chatA := seedKnownPeer(t, devA, peerOnA, railFingerprint)
	chatB := seedKnownPeer(t, devB, peerOnB, railFingerprint)
	linkSibling(t, devA, sibOnA)
	linkSibling(t, devB, sibOnB)

	msgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(events.TextBody{Text: "sent from A"})
	if _, err := devA.events.AppendOutbound(events.OutboundParams{
		ChatID: chatA, Kind: events.KindText, MsgID: msgID,
		EnvelopeID: "env-a-1", Body: body,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := devB.events.AppendOutbound(events.OutboundParams{
		ChatID: chatB, Kind: events.KindText, MsgID: msgID,
		DeliveryState: events.DeliverySent, MirroredFromSibling: true, Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	if _, _, err := devA.events.UpdateDeliveryState("env-a-1", events.DeliveryFailed); err != nil {
		t.Fatal(err)
	}
	aRow, err := devA.events.GetByMsgID(msgID)
	if err != nil {
		t.Fatal(err)
	}
	if aRow.DeliveryState != events.DeliveryFailed {
		t.Fatalf("A's own row did not reach %q: got %q", events.DeliveryFailed, aRow.DeliveryState)
	}

	ctx := context.Background()
	askSibling(ctx, devB, sibOnB, false, false)
	stubB.WaitForSend(t, 1, 5*time.Second)
	processSiblingRailEnvelope(ctx, devA, railEntry(sibOnA, "req-1", stubB.LastSendReq.Payload))
	stubA.WaitForSend(t, 1, 5*time.Second)
	answer := openGapEnvelope(t, devB, stubA.LastSendReq.Payload)
	resp, err := syncrail.ParseGapResponse(answer)
	if err != nil {
		t.Fatal(err)
	}
	handleGapResponse(ctx, devB, sibOnB, resp)

	bRow, err := devB.events.GetByMsgID(msgID)
	if err != nil {
		t.Fatal(err)
	}
	if bRow.DeliveryState != events.DeliveryFailed {
		t.Fatalf("B's delivery_state = %q, want %q — the failure overlay never crossed", bRow.DeliveryState, events.DeliveryFailed)
	}
}

func TestGapFill_ReadingAllUnreadOnOneDeviceClearsTheOthersBadge(t *testing.T) {
	stubA := startHaomadStub(t, nil, http.StatusOK)
	devA, _, _, _ := newTestDaemon(t, stubA)
	stubB := startHaomadStub(t, nil, http.StatusOK)
	devB, _, _, _ := newTestDaemon(t, stubB)
	adoptIdentityFrom(t, devB, devA)

	const peerOnA = "0000000000000000000000000000aaaa"
	const peerOnB = "0000000000000000000000000000bbbb"
	const sibOnA = "0000000000000000000000000000b0b0"
	const sibOnB = "0000000000000000000000000000a0a0"
	chatA := seedKnownPeer(t, devA, peerOnA, railFingerprint)
	chatB := seedKnownPeer(t, devB, peerOnB, railFingerprint)
	linkSibling(t, devA, sibOnA)
	linkSibling(t, devB, sibOnB)

	body, _ := json.Marshal(events.TextBody{Text: "hi"})
	msgIDs := make([]string, 3)
	for i := range msgIDs {
		id, err := msg.NewID()
		if err != nil {
			t.Fatal(err)
		}
		msgIDs[i] = id
		for _, w := range []struct {
			d      *daemon
			chatID chat.ChatID
			peer   string
		}{{devA, chatA, peerOnA}, {devB, chatB, peerOnB}} {
			if _, err := w.d.events.AppendInbound(events.InboundParams{
				ChatID: w.chatID, Kind: events.KindText, MsgID: id,
				SenderPeerID: w.peer, SenderTs: 1742600000, DisplayTs: 1742600000,
				Status: events.DecryptOK, Body: body,
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := w.d.chats.IncrementUnread(w.chatID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if c, err := devB.chats.Get(chatB); err != nil {
		t.Fatal(err)
	} else if dc := c.(*chat.DirectChat); dc.UnreadCount != 3 {
		t.Fatalf("test fixture is wrong: B's unread count = %d, want 3", dc.UnreadCount)
	}

	if _, _, err := devA.events.MarkRead(chatA); err != nil {
		t.Fatal(err)
	}
	if _, err := devA.chats.ClearUnread(chatA); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	askSibling(ctx, devB, sibOnB, false, false)
	stubB.WaitForSend(t, 1, 5*time.Second)
	processSiblingRailEnvelope(ctx, devA, railEntry(sibOnA, "req-1", stubB.LastSendReq.Payload))
	stubA.WaitForSend(t, 1, 5*time.Second)
	answer := openGapEnvelope(t, devB, stubA.LastSendReq.Payload)
	resp, err := syncrail.ParseGapResponse(answer)
	if err != nil {
		t.Fatal(err)
	}
	handleGapResponse(ctx, devB, sibOnB, resp)

	for _, id := range msgIDs {
		bRow, err := devB.events.GetByMsgID(id)
		if err != nil {
			t.Fatal(err)
		}
		if bRow.ReadAt == 0 {
			t.Fatalf("msg_id=%s did not converge to read on B", id)
		}
	}

	c, err := devB.chats.Get(chatB)
	if err != nil {
		t.Fatal(err)
	}
	dc := c.(*chat.DirectChat)
	if dc.UnreadCount != 0 {
		t.Fatalf("B's unread count = %d, want 0 — every row shows read but the chat list still says %d",
			dc.UnreadCount, dc.UnreadCount)
	}
}

func TestGapFill_PartialConvergenceLeavesTheRemainingUnreadCounted(t *testing.T) {
	stubA := startHaomadStub(t, nil, http.StatusOK)
	devA, _, _, _ := newTestDaemon(t, stubA)
	stubB := startHaomadStub(t, nil, http.StatusOK)
	devB, _, _, _ := newTestDaemon(t, stubB)
	adoptIdentityFrom(t, devB, devA)

	const peerOnA = "0000000000000000000000000000aaaa"
	const peerOnB = "0000000000000000000000000000bbbb"
	const sibOnA = "0000000000000000000000000000b0b0"
	const sibOnB = "0000000000000000000000000000a0a0"
	chatA := seedKnownPeer(t, devA, peerOnA, railFingerprint)
	chatB := seedKnownPeer(t, devB, peerOnB, railFingerprint)
	linkSibling(t, devA, sibOnA)
	linkSibling(t, devB, sibOnB)

	body, _ := json.Marshal(events.TextBody{Text: "hi"})
	msgIDs := make([]string, 3)
	for i := range msgIDs {
		id, err := msg.NewID()
		if err != nil {
			t.Fatal(err)
		}
		msgIDs[i] = id

		if _, err := devB.events.AppendInbound(events.InboundParams{
			ChatID: chatB, Kind: events.KindText, MsgID: id,
			SenderPeerID: peerOnB, SenderTs: 1742600000, DisplayTs: 1742600000,
			Status: events.DecryptOK, Body: body,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := devB.chats.IncrementUnread(chatB); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := devA.events.AppendInbound(events.InboundParams{
		ChatID: chatA, Kind: events.KindText, MsgID: msgIDs[0],
		SenderPeerID: peerOnA, SenderTs: 1742600000, DisplayTs: 1742600000,
		Status: events.DecryptOK, Body: body,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := devA.chats.IncrementUnread(chatA); err != nil {
		t.Fatal(err)
	}

	if _, _, err := devA.events.MarkRead(chatA); err != nil {
		t.Fatal(err)
	}
	if _, err := devA.chats.ClearUnread(chatA); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	askSibling(ctx, devB, sibOnB, false, false)
	stubB.WaitForSend(t, 1, 5*time.Second)
	processSiblingRailEnvelope(ctx, devA, railEntry(sibOnA, "req-1", stubB.LastSendReq.Payload))
	stubA.WaitForSend(t, 1, 5*time.Second)
	answer := openGapEnvelope(t, devB, stubA.LastSendReq.Payload)
	resp, err := syncrail.ParseGapResponse(answer)
	if err != nil {
		t.Fatal(err)
	}
	handleGapResponse(ctx, devB, sibOnB, resp)

	got, err := devB.events.GetByMsgID(msgIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if got.ReadAt == 0 {
		t.Fatalf("msg_id=%s did not converge to read on B", msgIDs[0])
	}

	for _, id := range msgIDs[1:] {
		got, err := devB.events.GetByMsgID(id)
		if err != nil {
			t.Fatal(err)
		}
		if got.ReadAt != 0 {
			t.Fatalf("msg_id=%s shows read on B, but no device ever read it", id)
		}
	}

	c, err := devB.chats.Get(chatB)
	if err != nil {
		t.Fatal(err)
	}
	dc := c.(*chat.DirectChat)
	if dc.UnreadCount != 2 {
		t.Fatalf("B's unread count = %d, want 2 — two messages A never received are genuinely still unread",
			dc.UnreadCount)
	}
}
