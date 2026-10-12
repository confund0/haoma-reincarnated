package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"haoma-frontend/internal/syncrail"
)

const gapFileMsgID = "0123456789abcdef0123456789abcdef"

func captureRailLog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return buf
}

func waitForLog(t *testing.T, buf *syncBuffer, parts ...string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(buf.String(), "\n") {
			all := true
			for _, p := range parts {
				if !strings.Contains(line, p) {
					all = false
					break
				}
			}
			if all {
				return line
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no log line holds %q; the log:\n%s", parts, buf.String())
	return ""
}

func gapOffer() syncrail.GapSiblingOfferBody {
	offer := syncrail.SiblingOffer{
		StagingID:        "5a1b2c3d4e5f60718293a4b5c6d7e8f9",
		Token:            "sibling-token-4f1e9d",
		Key:              bytes.Repeat([]byte{0xa7}, syncrail.KeySize),
		Nonce:            bytes.Repeat([]byte{0x5c}, syncrail.NonceSize),
		Sha256Ciphertext: "c0ffee",
		BlobSize:         5000,
		PlaintextSize:    4000,
	}
	fo := fmt.Sprintf(
		`{"token":%q,"url_path":%q,"name":"report.pdf","size":%d,"mime":"application/pdf","sha256_ciphertext":%q}`,
		offer.Token, "/files/"+offer.Token, offer.PlaintextSize+16, offer.Sha256Ciphertext)
	return syncrail.GapSiblingOfferBody{
		Fingerprint:  railFingerprint,
		MsgID:        gapFileMsgID,
		FileOffer:    json.RawMessage(fo),
		SiblingOffer: offer,
	}
}

func secretsOf(b syncrail.GapSiblingOfferBody) []string {
	o := b.SiblingOffer
	return []string{
		o.Token,
		hex.EncodeToString(o.Key),
		hex.EncodeToString(o.Nonce),
		fmt.Sprint(o.Key),
		fmt.Sprint(o.Nonce),
		string(b.FileOffer),
	}
}

func assertNoRailDrop(t *testing.T, log string) {
	t.Helper()
	for _, bad := range []string{"bad_mirror_body", "rail: drop"} {
		if strings.Contains(log, bad) {
			t.Fatalf("the envelope was dropped (%q); the log:\n%s", bad, log)
		}
	}
}

func TestFileRequest_RailRoutesTheRequest(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	startTestFileServer(t, d)
	buf := captureRailLog(t)

	const other = "fedcba9876543210fedcba9876543210"
	payload := sealGapEntry(t, d, railSiblingID, syncrail.KindFileRequest,
		syncrail.FileRequestBody{MsgIDs: []string{gapFileMsgID, other}})
	processSiblingRailEnvelope(context.Background(), d, railEntry(railSiblingID, "env-file-request-1", payload))

	if !strings.Contains(buf.String(), `msg="file request: received"`) ||
		!strings.Contains(buf.String(), "queued=2") {
		t.Fatalf("the request did not reach the serving worker; the log:\n%s", buf.String())
	}

	for _, id := range []string{gapFileMsgID, other} {
		waitForLog(t, buf, `msg="file request: not served"`, "outcome=no_row", "msg_id="+id,
			"sibling_peer_id="+railSiblingID)
	}
	assertNoRailDrop(t, buf.String())
	if stub.SendCalls != 0 {
		t.Fatalf("the responder sent %d envelopes for rows it does not hold", stub.SendCalls)
	}
}

func TestGapSiblingOffer_RailRoutesTheOffer(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	buf := captureRailLog(t)

	body := gapOffer()
	payload := sealGapEntry(t, d, railSiblingID, syncrail.KindGapSiblingOffer, body)
	processSiblingRailEnvelope(context.Background(), d, railEntry(railSiblingID, "env-gap-offer-1", payload))

	waitForLog(t, buf, `msg="sibling offer on gap-fill: not taken"`,
		"outcome=no_row", "msg_id=", "serving_peer_id=")
	log := buf.String()
	assertNoRailDrop(t, log)
	if !strings.Contains(log, "key_bytes=32") {
		t.Fatalf("the received line lost the offer's shape; the log:\n%s", log)
	}
	for _, s := range secretsOf(body) {
		if strings.Contains(log, s) {
			t.Fatalf("a secret reached the log: %q in\n%s", s, log)
		}
	}
	if stub.SendCalls != 0 {
		t.Fatalf("the requester sent %d envelopes; it answers nothing", stub.SendCalls)
	}
}

func TestGapSiblingOffer_RailRefusesAnOfferThatDisagrees(t *testing.T) {
	cases := map[string]struct {
		spoil  func(*syncrail.GapSiblingOfferBody)
		reason string
	}{
		"token": {func(b *syncrail.GapSiblingOfferBody) {
			b.FileOffer = bytes.Replace(b.FileOffer, []byte(`"token":"sibling-token-4f1e9d"`), []byte(`"token":"another-token"`), 1)
		}, "sibling_offer_token_disagrees"},
		"hash": {func(b *syncrail.GapSiblingOfferBody) {
			b.FileOffer = bytes.Replace(b.FileOffer, []byte(`"sha256_ciphertext":"c0ffee"`), []byte(`"sha256_ciphertext":"beef"`), 1)
		}, "sibling_offer_hash_disagrees"},
		"size": {func(b *syncrail.GapSiblingOfferBody) {
			b.FileOffer = bytes.Replace(b.FileOffer, []byte(`"size":4016`), []byte(`"size":4000`), 1)
		}, "sibling_offer_size_disagrees"},
		"staging id is the msg id": {func(b *syncrail.GapSiblingOfferBody) {
			b.SiblingOffer.StagingID = b.MsgID
		}, "sibling_offer_staging_is_msg_id"},
		"file offer not an object": {func(b *syncrail.GapSiblingOfferBody) {
			b.FileOffer = json.RawMessage(`["sibling-token-4f1e9d"]`)
		}, "sibling_offer_body_unparseable"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			stub := startHaomadStub(t, nil, http.StatusOK)
			d, _, _, _ := newTestDaemon(t, stub)
			linkSibling(t, d, railSiblingID)
			buf := captureRailLog(t)

			body := gapOffer()
			tc.spoil(&body)
			payload := sealGapEntry(t, d, railSiblingID, syncrail.KindGapSiblingOffer, body)
			processSiblingRailEnvelope(context.Background(), d, railEntry(railSiblingID, "env-gap-offer-1", payload))

			waitForLog(t, buf, `msg="sibling offer on gap-fill: refused"`, "reason="+tc.reason, "outcome=refused")
			log := buf.String()
			if strings.Contains(log, "outcome=not_taken") {
				t.Fatalf("a refused offer was also passed; the log:\n%s", log)
			}
			for _, s := range secretsOf(gapOffer()) {
				if strings.Contains(log, s) {
					t.Fatalf("a secret reached the log: %q in\n%s", s, log)
				}
			}
		})
	}
}

func TestGapFileKinds_RailDropsAnUnusableBodyByItsOwnReason(t *testing.T) {
	cases := map[string]struct {
		kind   syncrail.Kind
		body   any
		reason string
	}{
		"file request with no ids": {syncrail.KindFileRequest, syncrail.FileRequestBody{}, "bad_file_request"},
		"sibling offer on gap-fill with a short key": {syncrail.KindGapSiblingOffer, func() syncrail.GapSiblingOfferBody {
			b := gapOffer()
			b.SiblingOffer.Key = b.SiblingOffer.Key[:5]
			return b
		}(), "bad_gap_sibling_offer"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			stub := startHaomadStub(t, nil, http.StatusOK)
			d, _, _, _ := newTestDaemon(t, stub)
			linkSibling(t, d, railSiblingID)
			buf := captureRailLog(t)

			payload := sealGapEntry(t, d, railSiblingID, tc.kind, tc.body)
			processSiblingRailEnvelope(context.Background(), d, railEntry(railSiblingID, "env-1", payload))

			log := buf.String()
			if !strings.Contains(log, "reason="+tc.reason) {
				t.Fatalf("no drop with reason %q; the log:\n%s", tc.reason, log)
			}
			if strings.Contains(log, "bad_mirror_body") {
				t.Fatalf("the envelope reached the mirror parse; the log:\n%s", log)
			}
		})
	}
}

func TestSendFileRequest_ReachesTheResponder(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	buf := captureRailLog(t)

	if err := sendFileRequest(context.Background(), d, railSiblingID, []string{gapFileMsgID}); err != nil {
		t.Fatalf("sendFileRequest: %v", err)
	}
	stub.WaitForSend(t, 1, 5*time.Second)
	if stub.LastSendReq.PeerID != railSiblingID {
		t.Fatalf("sent to %q, want the sibling", stub.LastSendReq.PeerID)
	}
	if stub.LastSendReq.Kind != "" {
		t.Fatalf("wire-kind hint leaked to the router: %q", stub.LastSendReq.Kind)
	}
	env := openGapEnvelope(t, d, stub.LastSendReq.Payload)
	body, err := syncrail.ParseFileRequest(env)
	if err != nil {
		t.Fatalf("ParseFileRequest: %v", err)
	}
	if len(body.MsgIDs) != 1 || body.MsgIDs[0] != gapFileMsgID {
		t.Fatalf("msg ids = %v", body.MsgIDs)
	}

	startTestFileServer(t, d)
	processSiblingRailEnvelope(context.Background(), d, railEntry(railSiblingID, "env-2", stub.LastSendReq.Payload))
	waitForLog(t, buf, `msg="file request: received"`, "msg_ids=1", "queued=1")
}

func TestSendFileRequest_RefusesWhatTheResponderRefuses(t *testing.T) {
	over := make([]string, syncrail.MaxFileRequestIDs+1)
	for i := range over {
		over[i] = fmt.Sprintf("%032x", i+1)
	}
	cases := map[string][]string{
		"none":           nil,
		"over the limit": over,
		"an empty id":    {gapFileMsgID, ""},
		"an id twice":    {gapFileMsgID, gapFileMsgID},
	}
	for name, ids := range cases {
		t.Run(name, func(t *testing.T) {
			stub := startHaomadStub(t, nil, http.StatusOK)
			d, _, _, _ := newTestDaemon(t, stub)
			linkSibling(t, d, railSiblingID)
			if err := sendFileRequest(context.Background(), d, railSiblingID, ids); err == nil {
				t.Fatal("sendFileRequest sent a request the responder refuses")
			}
			if stub.SendCalls != 0 {
				t.Fatalf("%d sends reached the router", stub.SendCalls)
			}
		})
	}
}

func TestSendGapSiblingOffer_ReachesTheRequester(t *testing.T) {
	stub := startHaomadStub(t, nil, http.StatusOK)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	buf := captureRailLog(t)

	body := gapOffer()
	want := gapOffer()
	if err := sendGapSiblingOffer(context.Background(), d, railSiblingID, body); err != nil {
		t.Fatalf("sendGapSiblingOffer: %v", err)
	}
	if !bytes.Equal(body.SiblingOffer.Key, make([]byte, syncrail.KeySize)) ||
		!bytes.Equal(body.SiblingOffer.Nonce, make([]byte, syncrail.NonceSize)) ||
		!bytes.Equal(body.FileOffer, make([]byte, len(body.FileOffer))) {
		t.Fatal("the caller's key, nonce or file offer was left unwiped")
	}
	stub.WaitForSend(t, 1, 5*time.Second)
	if stub.LastSendReq.PeerID != railSiblingID {
		t.Fatalf("sent to %q, want the sibling", stub.LastSendReq.PeerID)
	}
	if stub.LastSendReq.Kind != "" {
		t.Fatalf("wire-kind hint leaked to the router: %q", stub.LastSendReq.Kind)
	}
	env := openGapEnvelope(t, d, stub.LastSendReq.Payload)
	got, err := syncrail.ParseGapSiblingOffer(env)
	if err != nil {
		t.Fatalf("ParseGapSiblingOffer: %v", err)
	}
	if got.MsgID != want.MsgID || got.Fingerprint != want.Fingerprint ||
		!bytes.Equal(got.SiblingOffer.Key, want.SiblingOffer.Key) ||
		got.SiblingOffer.Token != want.SiblingOffer.Token {
		t.Fatal("the offer changed on the way through the rail")
	}

	processSiblingRailEnvelope(context.Background(), d, railEntry(railSiblingID, "env-2", stub.LastSendReq.Payload))
	waitForLog(t, buf, `msg="sibling offer on gap-fill: not taken"`, "outcome=no_row")
	for _, s := range secretsOf(want) {
		if strings.Contains(buf.String(), s) {
			t.Fatalf("a secret reached the log: %q", s)
		}
	}
}

func TestSendGapSiblingOffer_RefusesWhatTheRequesterRefuses(t *testing.T) {
	cases := map[string]func(*syncrail.GapSiblingOfferBody){
		"no fingerprint": func(b *syncrail.GapSiblingOfferBody) { b.Fingerprint = "" },
		"short nonce":    func(b *syncrail.GapSiblingOfferBody) { b.SiblingOffer.Nonce = b.SiblingOffer.Nonce[:4] },
		"token disagrees": func(b *syncrail.GapSiblingOfferBody) {
			b.SiblingOffer.Token = "another-token"
		},
	}
	for name, spoil := range cases {
		t.Run(name, func(t *testing.T) {
			stub := startHaomadStub(t, nil, http.StatusOK)
			d, _, _, _ := newTestDaemon(t, stub)
			linkSibling(t, d, railSiblingID)
			body := gapOffer()
			spoil(&body)
			err := sendGapSiblingOffer(context.Background(), d, railSiblingID, body)
			if err == nil {
				t.Fatal("sendGapSiblingOffer sent an offer the requester refuses")
			}
			for _, s := range secretsOf(gapOffer()) {
				if strings.Contains(err.Error(), s) {
					t.Fatalf("the error carries a secret: %v", err)
				}
			}
			if stub.SendCalls != 0 {
				t.Fatalf("%d sends reached the router", stub.SendCalls)
			}
			if !bytes.Equal(body.SiblingOffer.Key, make([]byte, syncrail.KeySize)) {
				t.Fatal("a refused offer's key was left unwiped")
			}
		})
	}
}

func TestFileRequest_LimitEqualsTheGapBatch(t *testing.T) {
	if syncrail.MaxFileRequestIDs != gapBatchRows {
		t.Fatalf("MaxFileRequestIDs = %d, gapBatchRows = %d; they must be equal", syncrail.MaxFileRequestIDs, gapBatchRows)
	}
}
