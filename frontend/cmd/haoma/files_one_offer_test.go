package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/files"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/syncrail"
)

const oneOfferContactToken = "tok-contact-for-this-device-4a1b"

func debugLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func rawGapFileRow(t *testing.T, msgID string, inbound bool) syncrail.RowSnapshot {
	t.Helper()
	body, err := json.Marshal(FileEventBody{
		Token:            "tok-of-the-answering-device",
		UrlPath:          "/files/tok-of-the-answering-device",
		Name:             "photo.jpg",
		Size:             2048,
		Mime:             "image/jpeg",
		Sha256Ciphertext: strings.Repeat("b", 64),
		State:            string(files.StateReady),
		BytesReceived:    2048,
		LastError:        "an old failure",
		Caption:          "from the trip",
	})
	if err != nil {
		t.Fatal(err)
	}
	return syncrail.RowSnapshot{
		Fingerprint: railFingerprint,
		MsgID:       msgID,
		Kind:        string(events.KindFile),
		Inbound:     inbound,
		DisplayTs:   1742600000,
		SenderTs:    1742600000,
		Body:        body,
	}
}

func contactOffer(msgID string) (*msg.Wrapper, *msg.FileOfferBody, backendapi.InboxEntry) {
	w := &msg.Wrapper{V: msg.Version, Seq: 1, Ts: time.Now().Unix(), MsgID: msgID, Kind: msg.KindFileOffer}
	body := &msg.FileOfferBody{
		Token:            oneOfferContactToken,
		UrlPath:          "/files/" + oneOfferContactToken,
		Name:             "photo.jpg",
		Size:             2048,
		Mime:             "image/jpeg",
		Sha256Ciphertext: strings.Repeat("b", 64),
	}
	entry := backendapi.InboxEntry{
		ArrivalAt: time.Now().UnixNano(),
		PeerID:    railPeerID,
		Envelope:  backendapi.RawEnvelope{ID: "env-contact-" + msgID[:8], Timestamp: w.Ts, From: "contact-onion", Kind: "text"},
	}
	return w, body, entry
}

func TestGapFill_FileRowFromAnOlderBuildLandsHonest(t *testing.T) {
	for _, inbound := range []bool{true, false} {
		d, _, chatID := newSiblingReceiver(t)
		msgID, err := msg.NewID()
		if err != nil {
			t.Fatal(err)
		}
		handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{
			RangeEnd: 5,
			Rows:     []syncrail.RowSnapshot{rawGapFileRow(t, msgID, inbound)},
		})

		ev, body := fileRow(t, d, chatID, msgID)
		if (ev.Direction == events.DirIn) != inbound {
			t.Fatalf("inbound=%t: row direction = %s", inbound, ev.Direction)
		}
		if strings.Contains(string(ev.Body), "tok-of-the-answering-device") {
			t.Errorf("inbound=%t: the answering device's token landed in the row: %s", inbound, ev.Body)
		}
		if body.State != string(files.StatePending) || body.Token != "" || body.UrlPath != "" ||
			body.BytesReceived != 0 || body.LastError != "" {
			t.Errorf("inbound=%t: row body state=%q token=%q url_path=%q bytes=%d error=%q, want pending and nothing else",
				inbound, body.State, body.Token, body.UrlPath, body.BytesReceived, body.LastError)
		}
		if body.Name != "photo.jpg" || body.Size != 2048 || body.Caption != "from the trip" ||
			body.Sha256Ciphertext != strings.Repeat("b", 64) {
			t.Errorf("inbound=%t: what the file is did not land: %+v", inbound, body)
		}
		if _, err := d.files.GetMeta(msgID); !errors.Is(err, files.ErrMetaNotFound) {
			t.Errorf("inbound=%t: gap-fill wrote file metadata (err=%v) — a row is not an offer", inbound, err)
		}

		stampFileEventState(d, chatID, msgID, files.StateDownloading, 0, "")
		if _, body := fileRow(t, d, chatID, msgID); body.State != string(files.StateDownloading) {
			t.Errorf("inbound=%t: state after a stamp = %q, want downloading — the row can never move", inbound, body.State)
		}
	}
}

func TestOneOffer_ContactOfferOnAGapFilledRowStillRunsTheTransfer(t *testing.T) {
	d, stub, chatID := newSiblingReceiver(t)
	msgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{
		RangeEnd: 5,
		Rows:     []syncrail.RowSnapshot{rawGapFileRow(t, msgID, true)},
	})
	if _, err := d.files.GetMeta(msgID); !errors.Is(err, files.ErrMetaNotFound) {
		t.Fatalf("precondition: the gap-filled row must hold no metadata (err=%v)", err)
	}

	w, offer, entry := contactOffer(msgID)
	ingestFileOffer(context.Background(), d, chatID, entry, w, offer)

	stub.mu.Lock()
	fetchCalls, fetch := stub.fetchCalls, stub.lastFetch
	stub.mu.Unlock()
	if fetchCalls != 1 || fetch.Token != oneOfferContactToken {
		t.Fatalf("fetch calls = %d token = %q, want one fetch with the contact's token for this device — a held row refused the transfer",
			fetchCalls, fetch.Token)
	}
	meta, err := d.files.GetMeta(msgID)
	if err != nil {
		t.Fatalf("no metadata after the contact's offer: %v", err)
	}
	if meta.Token != oneOfferContactToken || meta.Direction != files.DirIn {
		t.Errorf("metadata token=%q dir=%s, want the contact's offer", meta.Token, meta.Direction)
	}
	fileFetchStateHandler(context.Background(), d)(backendapi.FileFetchEvent{MsgID: msgID, State: string(files.StateDownloading)})
	if _, body := fileRow(t, d, chatID, msgID); body.State != string(files.StateDownloading) {
		t.Errorf("row state = %q after haomad said downloading, want downloading", body.State)
	}
}

func TestOneOffer_ContactOfferAfterASiblingOfferIsDiscarded(t *testing.T) {
	logs := debugLogs(t)
	d, stub, chatID := newSiblingReceiver(t)
	msgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{
		RangeEnd: 5,
		Rows:     []syncrail.RowSnapshot{rawGapFileRow(t, msgID, true)},
	})
	taken := siblingTakenMeta(msgID, chatID)
	held := siblingHeldOffer(msgID)
	if err := d.files.PutMetaAndSiblingOffer(taken, held); err != nil {
		t.Fatal(err)
	}

	stub.mu.Lock()
	fetchBefore, sendsBefore := stub.fetchCalls, stub.sendCalls
	stub.mu.Unlock()

	w, offer, entry := contactOffer(msgID)
	ingestFileOffer(context.Background(), d, chatID, entry, w, offer)

	stub.mu.Lock()
	fetchCalls, sends := stub.fetchCalls-fetchBefore, stub.sendCalls-sendsBefore
	stub.mu.Unlock()
	if fetchCalls != 0 || sends != 0 {
		t.Errorf("fetch calls = %d sends = %d, want 0 and 0 — a second offer started a transfer", fetchCalls, sends)
	}
	meta, err := d.files.GetMeta(msgID)
	if err != nil {
		t.Fatal(err)
	}
	if !meta.FetchedFromSibling || meta.Token != "" || meta.StagingID != held.StagingID {
		t.Errorf("the sibling offer's metadata was overwritten: %+v", meta)
	}
	if _, err := d.files.GetMetaByToken(oneOfferContactToken); !errors.Is(err, files.ErrMetaNotFound) {
		t.Errorf("the discarded offer's token was indexed (err=%v)", err)
	}
	if _, err := d.files.GetSiblingOffer(msgID); err != nil {
		t.Errorf("the sibling offer's key is gone: %v", err)
	}
	if !strings.Contains(logs.String(), "outcome=already_held") {
		t.Errorf("no already_held line:\n%s", logs.String())
	}
}

func TestOneOffer_SiblingOfferAfterAContactOfferIsDiscarded(t *testing.T) {
	logs := debugLogs(t)
	d, stub, chatID := newSiblingReceiver(t)
	f := newSiblingFile(t)
	w := siblingOfferWrapper(t, f, "")
	contact := files.Metadata{
		MsgID: w.MsgID, ChatID: chatID, Direction: files.DirIn,
		Token: oneOfferContactToken, OfferPeerID: railPeerID,
		OriginalName: "photo.jpg", Size: 2048, State: files.StateDownloading,
	}
	if err := d.files.PutMetaIfAbsent(contact); err != nil {
		t.Fatal(err)
	}

	processInboxEntry(context.Background(), d, railEntry(railSiblingID, "env-sib-second", sealSiblingOffer(t, d, w, offerFor(f))))

	stub.mu.Lock()
	fetchCalls := stub.fetchCalls
	stub.mu.Unlock()
	if fetchCalls != 0 {
		t.Errorf("fetch calls = %d, want 0 — a second offer started a transfer", fetchCalls)
	}
	meta, err := d.files.GetMeta(w.MsgID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.FetchedFromSibling || meta.Token != oneOfferContactToken || meta.Direction != files.DirIn ||
		meta.State != files.StateDownloading {
		t.Errorf("the contact's metadata was overwritten: %+v", meta)
	}
	if _, err := d.files.GetSiblingOffer(w.MsgID); !errors.Is(err, files.ErrSiblingOfferNotFound) {
		t.Errorf("a discarded sibling offer left its key held (err=%v)", err)
	}
	if _, err := d.files.SiblingOfferMsgID(siblingTestStaging); !errors.Is(err, files.ErrSiblingOfferNotFound) {
		t.Errorf("a discarded sibling offer left its staging-id entry (err=%v)", err)
	}
	if !strings.Contains(logs.String(), "outcome=already_held") {
		t.Errorf("no already_held line:\n%s", logs.String())
	}
}

func siblingTakenMeta(msgID string, chatID chat.ChatID) files.Metadata {
	return files.Metadata{
		MsgID: msgID, ChatID: chatID, Direction: files.DirIn,
		OriginalName: "photo.jpg", Size: 2048, State: files.StatePending,
		FetchedFromSibling: true, StagingID: siblingTestStaging,
	}
}

func siblingHeldOffer(msgID string) files.SiblingOffer {
	return files.SiblingOffer{
		MsgID: msgID, ServingPeerID: railSiblingID, StagingID: siblingTestStaging,
		Token: siblingTestToken, UrlPath: "/files/" + siblingTestToken,
		Key: bytes.Repeat([]byte{0x3c}, 32), Nonce: bytes.Repeat([]byte{0x7e}, 24),
		BlobSize: 5000, PlaintextSize: 4000, Sha256Ciphertext: strings.Repeat("b", 64),
	}
}

func TestOneOffer_ContactOfferNamingARowThatIsNotItsFileIsRefused(t *testing.T) {
	cases := map[string]func(t *testing.T, d *daemon, chatID chat.ChatID, msgID string){
		"a file I sent": func(t *testing.T, d *daemon, chatID chat.ChatID, msgID string) {
			handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{
				RangeEnd: 5,
				Rows:     []syncrail.RowSnapshot{rawGapFileRow(t, msgID, false)},
			})
		},
		"another conversation": func(t *testing.T, d *daemon, _ chat.ChatID, msgID string) {
			other, _, err := d.createDirectWithDefaults("0000000000000000000000000000abcd")
			if err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(FileEventBody{Name: "photo.jpg", State: string(files.StatePending)})
			if _, err := d.events.AppendInbound(events.InboundParams{
				ChatID: other.ID, Kind: events.KindFile, SenderTs: time.Now().Unix(),
				MsgID: msgID, Status: events.DecryptOK, Body: body,
			}); err != nil {
				t.Fatal(err)
			}
		},
		"another kind": func(t *testing.T, d *daemon, chatID chat.ChatID, msgID string) {
			body, _ := json.Marshal(events.TextBody{Text: "hello"})
			if _, err := d.events.AppendInbound(events.InboundParams{
				ChatID: chatID, Kind: events.KindText, SenderTs: time.Now().Unix(),
				MsgID: msgID, Status: events.DecryptOK, Body: body,
			}); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, seed := range cases {
		t.Run(name, func(t *testing.T) {
			logs := debugLogs(t)
			d, stub, chatID := newSiblingReceiver(t)
			msgID, err := msg.NewID()
			if err != nil {
				t.Fatal(err)
			}
			seed(t, d, chatID, msgID)
			unreadBefore := unreadCount(t, d, chatID)

			stub.mu.Lock()
			fetchBefore, sendsBefore := stub.fetchCalls, stub.sendCalls
			stub.mu.Unlock()

			w, offer, entry := contactOffer(msgID)
			ingestFileOffer(context.Background(), d, chatID, entry, w, offer)

			stub.mu.Lock()
			fetchCalls, sends := stub.fetchCalls-fetchBefore, stub.sendCalls-sendsBefore
			stub.mu.Unlock()
			if fetchCalls != 0 || sends != 0 {
				t.Errorf("fetch calls = %d sends = %d, want 0 and 0 — an offer for a msg id that is not its file started a transfer", fetchCalls, sends)
			}
			if _, err := d.files.GetMeta(msgID); !errors.Is(err, files.ErrMetaNotFound) {
				t.Errorf("metadata was written for a msg id that is not this contact's file (err=%v)", err)
			}
			if got := unreadCount(t, d, chatID); got != unreadBefore {
				t.Errorf("unread = %d, want %d — a refused offer counted as an arrival", got, unreadBefore)
			}
			held, err := d.events.GetByMsgID(msgID)
			if err != nil {
				t.Fatal(err)
			}
			if held.ArrivedFromPeerID != "" {
				t.Errorf("the held row was given the contact's device %q", held.ArrivedFromPeerID)
			}
			if !strings.Contains(logs.String(), "outcome=foreign_msg_id") {
				t.Errorf("no foreign_msg_id line:\n%s", logs.String())
			}
		})
	}
}
