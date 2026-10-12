package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/logid"
	"haoma-frontend/internal/msg"
)

func reloadDirect(t *testing.T, d *daemon, id chat.ChatID) *chat.DirectChat {
	t.Helper()
	c, err := d.chats.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	dc, ok := c.(*chat.DirectChat)
	if !ok {
		t.Fatalf("chat %s is not a direct chat", id)
	}
	return dc
}

func seedInboundFrom(t *testing.T, d *daemon, chatID chat.ChatID, msgID, fromPeerID string, displayTs int64) {
	t.Helper()
	body, _ := json.Marshal(events.TextBody{Text: "hi"})
	if _, err := d.events.AppendInbound(events.InboundParams{
		ChatID:            chatID,
		Kind:              events.KindText,
		DisplayTs:         displayTs,
		SenderTs:          displayTs,
		ArrivedFromPeerID: fromPeerID,
		MsgID:             msgID,
		Status:            events.DecryptOK,
		Body:              body,
	}); err != nil {
		t.Fatal(err)
	}
}

func target(msgID, fromPeerID string) events.ReceiptTarget {
	return events.ReceiptTarget{MsgID: msgID, ArrivedFromPeerID: fromPeerID}
}

func attachSecondDeviceWithNoSession(t *testing.T, d *daemon, primary, second string) *chat.DirectChat {
	t.Helper()
	dc, err := d.chats.GetByDirectPeer(primary)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.chats.AttachPeerID(dc.ID, second); err != nil {
		t.Fatal(err)
	}
	dc = reloadDirect(t, d, dc.ID)
	if len(dc.FanoutPeerIDs()) != 2 {
		t.Fatalf("routing set = %v, want both devices", dc.FanoutPeerIDs())
	}
	return dc
}

func receiptSentAt(t *testing.T, d *daemon, msgID string) int64 {
	t.Helper()
	ev, err := d.events.GetByMsgID(msgID)
	if err != nil {
		t.Fatalf("GetByMsgID(%s): %v", msgID, err)
	}
	return ev.ReadReceiptSentAt
}

func arrivedFrom(t *testing.T, d *daemon, msgID string) string {
	t.Helper()
	ev, err := d.events.GetByMsgID(msgID)
	if err != nil {
		t.Fatalf("GetByMsgID(%s): %v", msgID, err)
	}
	return ev.ArrivedFromPeerID
}

func TestProcessInboxEntry_RecordsTheSendingDevice(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const aliceID = "0000000000000000000000000000aaaa"
	const senderTs int64 = 1742643890
	blob, _ := makeInboundCiphertext(t, d, aliceID, "hello bob", 7, senderTs, 0)

	processInboxEntry(ctx, d, backendapi.InboxEntry{
		ArrivalAt: time.Now().UnixNano(),
		PeerID:    aliceID,
		Envelope: backendapi.RawEnvelope{
			ID: "env-arrived-from-1", Timestamp: senderTs, From: "alice-onion",
			Kind: "text", Payload: blob, Mac: []byte{1, 2, 3, 4},
		},
	})

	rows, err := d.events.List(chatIDForPeer(t, d, aliceID), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("persisted %d rows, want 1", len(rows))
	}
	if rows[0].ArrivedFromPeerID != aliceID {
		t.Fatalf("arrived_from_peer_id = %q, want %q — the receipt has no device to address",
			rows[0].ArrivedFromPeerID, aliceID)
	}
}

func TestProcessInboxEntry_HeldRowLearnsTheSendingDevice(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const aliceID = "0000000000000000000000000000aaab"
	const senderTs int64 = 1742643890
	blob, msgID := makeInboundCiphertext(t, d, aliceID, "hello bob", 7, senderTs, 0)

	dc, _, err := d.createDirectWithDefaults(aliceID)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(events.TextBody{Text: "hello bob"})
	if _, err := d.events.AppendInbound(events.InboundParams{
		ChatID:              dc.ID,
		Kind:                events.KindText,
		DisplayTs:           senderTs,
		SenderTs:            senderTs,
		MsgID:               msgID,
		Status:              events.DecryptOK,
		Body:                body,
		MirroredFromSibling: true,
	}); err != nil {
		t.Fatal(err)
	}

	processInboxEntry(ctx, d, backendapi.InboxEntry{
		ArrivalAt: time.Now().UnixNano(),
		PeerID:    aliceID,
		Envelope: backendapi.RawEnvelope{
			ID: "env-arrived-from-2", Timestamp: senderTs, From: "alice-onion",
			Kind: "text", Payload: blob, Mac: []byte{1, 2, 3, 4},
		},
	})

	rows, err := d.events.List(dc.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1 — the arrival must not add a second row", len(rows))
	}
	if got := arrivedFrom(t, d, msgID); got != aliceID {
		t.Fatalf("arrived_from_peer_id = %q, want %q — a swept row never learns its sending device, so its receipt is addressed to the chat's stored device for good", got, aliceID)
	}

	if rows[0].DisplayTs != senderTs {
		t.Errorf("display_ts = %d, want %d — recording the device moved the row", rows[0].DisplayTs, senderTs)
	}
	if rows[0].ReadAt != 0 || rows[0].ReadReceiptSentAt != 0 {
		t.Errorf("read axis disturbed: read_at=%d receipt_sent_at=%d", rows[0].ReadAt, rows[0].ReadReceiptSentAt)
	}
	if !rows[0].MirroredFromSibling {
		t.Error("mirrored-from-sibling was cleared; the write must touch one field only")
	}
}

func TestIngestFileOffer_HeldRowLearnsTheOfferingDevice(t *testing.T) {
	stub := startFilesStub(t)
	d := newFilesTestDaemon(t, stub)
	const phone = "00000000000000000000000000004701"
	dc, _, err := d.createDirectWithDefaults(phone)
	if err != nil {
		t.Fatal(err)
	}

	msgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(FileEventBody{Token: "tok-held", Name: "secret.pdf", State: "downloading"})
	if _, err := d.events.AppendInbound(events.InboundParams{
		ChatID:              dc.ID,
		Kind:                events.KindFile,
		DisplayTs:           1742643885,
		SenderTs:            1742643885,
		MsgID:               msgID,
		Status:              events.DecryptOK,
		Body:                body,
		MirroredFromSibling: true,
	}); err != nil {
		t.Fatal(err)
	}

	wrapper := &msg.Wrapper{V: msg.Version, Seq: 1, Ts: 1742643885, MsgID: msgID, Kind: msg.KindFileOffer}
	offer := &msg.FileOfferBody{
		Token:            "tok-held",
		UrlPath:          "/files/tok-held",
		Name:             "secret.pdf",
		Size:             1024,
		Mime:             "application/pdf",
		Sha256Ciphertext: strings.Repeat("a", 64),
	}
	entry := backendapi.InboxEntry{
		ArrivalAt: time.Now().UnixNano(),
		PeerID:    phone,
		Envelope:  backendapi.RawEnvelope{ID: "env-file-held", Timestamp: wrapper.Ts, Kind: "text"},
	}
	ingestFileOffer(context.Background(), d, dc.ID, entry, wrapper, offer)

	if got := arrivedFrom(t, d, msgID); got != phone {
		t.Fatalf("arrived_from_peer_id = %q, want %q", got, phone)
	}
	meta, err := d.files.GetMeta(msgID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.OfferPeerID != phone {
		t.Fatalf("offer_peer_id = %q, want %q", meta.OfferPeerID, phone)
	}
	if meta.OfferPeerID != arrivedFrom(t, d, msgID) {
		t.Fatal("the event row and the metadata row name different devices; both are written from the envelope's own sender and cannot disagree")
	}
}

func TestIngestFileOffer_FreshRowRecordsTheOfferingDevice(t *testing.T) {
	stub := startFilesStub(t)
	d := newFilesTestDaemon(t, stub)
	const phone = "00000000000000000000000000004801"
	dc, _, err := d.createDirectWithDefaults(phone)
	if err != nil {
		t.Fatal(err)
	}
	msgID := ingestOfferFrom(t, d, dc.ID, phone, "tok-fresh")

	if got := arrivedFrom(t, d, msgID); got != phone {
		t.Fatalf("arrived_from_peer_id = %q, want %q — a file row ships a read receipt too", got, phone)
	}
}

func TestMarkRead_CarriesTheSendingDevice(t *testing.T) {
	d := minimalDaemon(t, "http://127.0.0.1:1")
	const phone = "00000000000000000000000000004901"
	const laptop = "00000000000000000000000000004902"
	dc, _, err := d.createDirectWithDefaults(phone)
	if err != nil {
		t.Fatal(err)
	}
	seedInboundFrom(t, d, dc.ID, "mid-1", laptop, 1742643885)
	seedInboundFrom(t, d, dc.ID, "mid-2", "", 1742643886)

	_, pending, err := d.events.MarkRead(dc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending = %v, want 2 rows", pending)
	}
	if pending[0].MsgID != "mid-1" || pending[0].ArrivedFromPeerID != laptop {
		t.Errorf("first pending row = %+v, want mid-1 from %s", pending[0], laptop)
	}
	if pending[1].MsgID != "mid-2" || pending[1].ArrivedFromPeerID != "" {
		t.Errorf("second pending row = %+v, want mid-2 with no device", pending[1])
	}
}

func TestResolveReceiptPeer_AddressesTheDeviceThatSentTheMessage(t *testing.T) {
	d := minimalDaemon(t, "http://127.0.0.1:1")
	const phone = "00000000000000000000000000004101"
	const laptop = "00000000000000000000000000004102"
	dc, _, err := d.createDirectWithDefaults(phone)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.chats.AttachPeerID(dc.ID, laptop); err != nil {
		t.Fatal(err)
	}
	dc = reloadDirect(t, d, dc.ID)

	got := resolveReceiptPeer(dc, receiptFallbackPeer(dc), target("mid-from-laptop", laptop))
	if got != laptop {
		t.Fatalf("resolveReceiptPeer = %q, want %q — a dead stored device would retry this receipt forever", got, laptop)
	}
}

func TestResolveReceiptPeer_NoRecordedDeviceFallsBack(t *testing.T) {
	d := minimalDaemon(t, "http://127.0.0.1:1")
	const phone = "00000000000000000000000000004201"
	const laptop = "00000000000000000000000000004202"
	dc, _, err := d.createDirectWithDefaults(phone)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.chats.AttachPeerID(dc.ID, laptop); err != nil {
		t.Fatal(err)
	}
	dc = reloadDirect(t, d, dc.ID)

	got := resolveReceiptPeer(dc, receiptFallbackPeer(dc), target("mid-old-row", ""))
	if got != phone {
		t.Fatalf("resolveReceiptPeer = %q, want the chat's stored device %q", got, phone)
	}
}

func TestResolveReceiptPeer_DeviceOutsideTheRoutingSetFallsBack(t *testing.T) {
	d := minimalDaemon(t, "http://127.0.0.1:1")
	const phone = "00000000000000000000000000004301"
	const stranger = "00000000000000000000000000004302"
	dc, _, err := d.createDirectWithDefaults(phone)
	if err != nil {
		t.Fatal(err)
	}
	if got := dc.FanoutPeerIDs(); len(got) != 1 || got[0] != phone {
		t.Fatalf("routing set = %v, want [%s]", got, phone)
	}

	got := resolveReceiptPeer(dc, receiptFallbackPeer(dc), target("mid-from-stranger", stranger))
	if got != phone {
		t.Fatalf("resolveReceiptPeer = %q, want the chat's stored device %q", got, phone)
	}
}

func TestGroupReceiptTargets_MixedBatchSplitsPerDevice(t *testing.T) {
	d := minimalDaemon(t, "http://127.0.0.1:1")
	const phone = "00000000000000000000000000004501"
	const laptop = "00000000000000000000000000004502"
	dc, _, err := d.createDirectWithDefaults(phone)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.chats.AttachPeerID(dc.ID, laptop); err != nil {
		t.Fatal(err)
	}
	dc = reloadDirect(t, d, dc.ID)

	groups := groupReceiptTargets(dc, receiptFallbackPeer(dc), []events.ReceiptTarget{
		target("mid-1", laptop),
		target("mid-2", phone),
		target("mid-3", laptop),

		target("mid-4", ""),
	})

	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2 — a mixed batch acked as one envelope leaves one device unaddressed", len(groups))
	}
	if groups[0].peerID != laptop {
		t.Errorf("first group peer = %q, want %q (mid-1 came from the laptop)", groups[0].peerID, laptop)
	}
	if got := groups[0].msgIDs; len(got) != 2 || got[0] != "mid-1" || got[1] != "mid-3" {
		t.Errorf("laptop group = %v, want [mid-1 mid-3]", got)
	}
	if groups[1].peerID != phone {
		t.Errorf("second group peer = %q, want %q", groups[1].peerID, phone)
	}
	if got := groups[1].msgIDs; len(got) != 2 || got[0] != "mid-2" || got[1] != "mid-4" {
		t.Errorf("phone group = %v, want [mid-2 mid-4]", got)
	}
}

func TestGroupReceiptTargets_OneDeviceIsOneEnvelope(t *testing.T) {
	d := minimalDaemon(t, "http://127.0.0.1:1")
	const phone = "00000000000000000000000000004601"
	dc, _, err := d.createDirectWithDefaults(phone)
	if err != nil {
		t.Fatal(err)
	}

	groups := groupReceiptTargets(dc, receiptFallbackPeer(dc), []events.ReceiptTarget{
		target("mid-a", phone),
		target("mid-b", phone),
	})
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(groups))
	}
	if groups[0].peerID != phone || len(groups[0].msgIDs) != 2 {
		t.Fatalf("group = %+v, want both rows addressed to %q", groups[0], phone)
	}
}

func TestShipReadReceipt_SuppressedChatStampsTheSentinelAndShipsNothing(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	const phone = "00000000000000000000000000005001"
	const laptop = "00000000000000000000000000005002"
	preEstablishSession(t, d, phone)
	attachSecondDevice(t, d, phone, laptop)
	chatID := chatIDForPeer(t, d, phone)
	if err := d.chats.SetDisableReadReceipts(chatID, true); err != nil {
		t.Fatal(err)
	}

	seedInboundFrom(t, d, chatID, "mid-sup-1", laptop, 1742643885)
	seedInboundFrom(t, d, chatID, "mid-sup-2", phone, 1742643886)
	_, pending, err := d.events.MarkRead(chatID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending = %v, want 2 rows", pending)
	}

	d.shipReadReceipt(chatID, pending)

	if stub.SendCalls != 0 {
		t.Fatalf("haomad saw %d POST /send calls, want 0 — a suppressed conversation must put nothing on the wire", stub.SendCalls)
	}
	for _, id := range []string{"mid-sup-1", "mid-sup-2"} {
		if got := receiptSentAt(t, d, id); got != events.ReadReceiptSuppressedSentinel {
			t.Errorf("%s receipt_sent_at = %d, want the suppression sentinel %d", id, got, events.ReadReceiptSuppressedSentinel)
		}
	}

	_, again, err := d.events.MarkRead(chatID)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Errorf("pending after suppression = %v, want none", again)
	}
}

func TestShipReadReceipt_AFailedGroupLeavesItsRowsPending(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	const phone = "00000000000000000000000000005101"
	const halfPaired = "00000000000000000000000000005102"
	preEstablishSession(t, d, phone)
	attachSecondDeviceWithNoSession(t, d, phone, halfPaired)
	chatID := chatIDForPeer(t, d, phone)

	seedInboundFrom(t, d, chatID, "mid-dead", halfPaired, 1742643885)
	seedInboundFrom(t, d, chatID, "mid-live", phone, 1742643886)
	_, pending, err := d.events.MarkRead(chatID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending = %v, want 2 rows", pending)
	}

	d.shipReadReceipt(chatID, pending)

	if stub.SendCalls != 1 {
		t.Fatalf("haomad saw %d POST /send calls, want 1 — the second group must still be acked after the first fails", stub.SendCalls)
	}
	if got := stub.SentPeerIDs; len(got) != 1 || got[0] != phone {
		t.Fatalf("receipt addressed %v, want [%s]", got, phone)
	}
	if got := receiptSentAt(t, d, "mid-live"); got == 0 {
		t.Error("the shipped group's row claims no receipt")
	}
	if got := receiptSentAt(t, d, "mid-dead"); got != 0 {
		t.Errorf("the failed group's row claims a receipt at %d; nothing was sent for it", got)
	}

	_, again, err := d.events.MarkRead(chatID)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 || again[0].MsgID != "mid-dead" {
		t.Fatalf("pending after the run = %v, want just mid-dead re-harvested", again)
	}
	if again[0].ArrivedFromPeerID != halfPaired {
		t.Errorf("re-harvested row names device %q, want %q", again[0].ArrivedFromPeerID, halfPaired)
	}
}

func TestShipReadReceipt_NoMessageIDReachesDiskWhole(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	const phone = "00000000000000000000000000005201"

	const acked = "aabbccdd11223344556677889900ffee"
	preEstablishSession(t, d, phone)
	chatID := chatIDForPeer(t, d, phone)
	seedInboundFrom(t, d, chatID, acked, phone, 1742643885)
	_, pending, err := d.events.MarkRead(chatID)
	if err != nil {
		t.Fatal(err)
	}

	read := captureDefaultLog(t)
	d.shipReadReceipt(chatID, pending)
	written := read()

	if strings.Contains(written, acked) {
		t.Errorf("the message id reached disk whole; the log holds %q", acked)
	}
	short := logid.Short(acked)
	if !strings.Contains(written, "acked_msg_ids="+short) {
		t.Errorf("the acknowledged list does not carry the short form %q", short)
	}
	if !strings.Contains(written, "read receipt acknowledges this message") {
		t.Error("no per-message line was written; a joined list is not searchable by a message id")
	}
	if !strings.Contains(written, "msg_id="+short) {
		t.Errorf("the per-message line does not name the message under msg_id")
	}
}
