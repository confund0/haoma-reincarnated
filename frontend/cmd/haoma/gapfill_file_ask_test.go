package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/files"
	"haoma-frontend/internal/gapfill"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/logid"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/syncrail"
)

func newAskingDevice(t *testing.T) (*daemon, *filesStub, chat.ChatID) {
	t.Helper()
	d, stub, chatID := newSiblingReceiver(t)
	d.watermarks = gapfill.NewWatermarks(d.store)
	return d, stub, chatID
}

func gapRow(t *testing.T, msgID string, inbound bool, f siblingFile, displayTs int64) syncrail.RowSnapshot {
	t.Helper()
	row := rawGapFileRow(t, msgID, inbound)
	var body FileEventBody
	if err := json.Unmarshal(row.Body, &body); err != nil {
		t.Fatal(err)
	}
	body.Size = uint64(len(f.plaintext)) + 16
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	row.Body = raw
	row.DisplayTs = displayTs
	return row
}

func landGapFile(t *testing.T, d *daemon, msgID string, inbound bool, f siblingFile, displayTs int64) {
	t.Helper()
	if _, _, err := applyGapRow(context.Background(), d, slog.Default(), gapRow(t, msgID, inbound, f, displayTs)); err != nil {
		t.Fatalf("applyGapRow: %v", err)
	}
}

func newMsgID(t *testing.T) string {
	t.Helper()
	id, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func fileRequestsSent(t *testing.T, d *daemon, stub *filesStub) [][]string {
	t.Helper()
	key, err := d.railKey()
	if err != nil {
		t.Fatal(err)
	}
	stub.mu.Lock()
	payloads := append([][]byte(nil), stub.sentPayloads...)
	stub.mu.Unlock()
	var out [][]string
	for _, p := range payloads {
		env, err := syncrail.OpenEnvelope(key, p)
		if err != nil || env.Kind != syncrail.KindFileRequest {
			continue
		}
		body, err := syncrail.ParseFileRequest(env)
		if err != nil {
			t.Fatalf("a file request the responder refuses: %v", err)
		}
		out = append(out, body.MsgIDs)
	}
	return out
}

func endWalk(d *daemon) {
	handleGapResponse(context.Background(), d, railSiblingID, syncrail.GapResponseBody{RangeEnd: 1})
}

func setClock(d *daemon, at time.Time) *time.Time {
	now := at
	d.fileAsker.now = func() time.Time { return now }
	return &now
}

func gapOfferFor(t *testing.T, msgID string, f siblingFile, staging, token string) syncrail.GapSiblingOfferBody {
	t.Helper()
	fo, err := json.Marshal(msg.FileOfferBody{
		Token: token, UrlPath: "/files/" + token, Name: "photo.jpg",
		Size: uint64(len(f.plaintext)) + 16, Mime: "image/jpeg", Sha256Ciphertext: f.sha,
	})
	if err != nil {
		t.Fatal(err)
	}
	return syncrail.GapSiblingOfferBody{
		Fingerprint: railFingerprint,
		MsgID:       msgID,
		FileOffer:   fo,
		SiblingOffer: syncrail.SiblingOffer{
			StagingID:        staging,
			Token:            token,
			Key:              append([]byte(nil), f.key...),
			Nonce:            append([]byte(nil), f.nonce...),
			Sha256Ciphertext: f.sha,
			BlobSize:         uint64(len(f.ciphertext)),
			PlaintextSize:    uint64(len(f.plaintext)),
		},
	}
}

func stagingFor(i int) string { return fmt.Sprintf("%032x", 0xabc000+i) }

func wantedOf(t *testing.T, d *daemon) map[string]files.WantedFile {
	t.Helper()
	all, err := d.files.ListWantedFiles()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]files.WantedFile{}
	for _, w := range all {
		out[w.MsgID] = w
	}
	return out
}

func stubCounts(stub *filesStub) (fetches, sends, receipts int) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	return stub.fetchCalls, stub.sendCalls, stub.receiptCalls
}

func TestWantedSet_GapFillLandsTheFileRowWanted(t *testing.T) {
	d, _, chatID := newAskingDevice(t)
	f := newSiblingFile(t)
	in, out := newMsgID(t), newMsgID(t)
	landGapFile(t, d, in, true, f, 100)
	landGapFile(t, d, out, false, f, 200)
	text := newMsgID(t)
	body, _ := json.Marshal(events.TextBody{Text: "hi"})
	if _, _, err := applyGapRow(context.Background(), d, slog.Default(), syncrail.RowSnapshot{
		Fingerprint: railFingerprint, MsgID: text, Kind: string(events.KindText), Inbound: true, DisplayTs: 300, Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	got := wantedOf(t, d)
	if len(got) != 2 || got[in].ChatID != chatID || got[out].DisplayTs != 200 {
		t.Fatalf("wanted = %+v, want the two file rows with their chat and place", got)
	}

	w, offer, entry := contactOffer(in)
	offer.Size = uint64(len(f.plaintext)) + 16
	ingestFileOffer(context.Background(), d, chatID, entry, w, offer)
	if _, still := wantedOf(t, d)[in]; still {
		t.Fatal("a file whose contact offer was taken is still wanted")
	}
}

func TestWantedSet_OneOffScanAddsRowsHeldBefore(t *testing.T) {
	d, stub, chatID := newAskingDevice(t)
	setClock(d, time.Unix(1_800_000_000, 0))
	appendFileRow := func(msgID string, deleted bool) {
		body, _ := json.Marshal(FileEventBody{Name: "old.jpg", Size: 100, State: string(files.StatePending)})
		if _, err := d.events.AppendInbound(events.InboundParams{
			ChatID: chatID, Kind: events.KindFile, SenderTs: 1, SenderPeerID: railPeerID,
			MsgID: msgID, Status: events.DecryptOK, Body: body,
		}); err != nil {
			t.Fatal(err)
		}
		if deleted {
			if _, err := d.events.ApplyDelete(msgID, 5, railPeerID); err != nil {
				t.Fatal(err)
			}
		}
	}
	old, withMeta, deleted := newMsgID(t), newMsgID(t), newMsgID(t)
	appendFileRow(old, false)
	appendFileRow(withMeta, false)
	appendFileRow(deleted, true)
	if err := d.files.PutMeta(files.Metadata{MsgID: withMeta, ChatID: chatID, Direction: files.DirIn, Token: "t", State: files.StateReady}); err != nil {
		t.Fatal(err)
	}

	endWalk(d)
	reqs := fileRequestsSent(t, d, stub)
	if len(reqs) != 1 || len(reqs[0]) != 1 || reqs[0][0] != old {
		t.Fatalf("requests = %v, want one naming only the row held with no metadata", reqs)
	}
	if done, _ := d.files.WantedFilled(); !done {
		t.Fatal("the scan did not record that it ran")
	}

	later := newMsgID(t)
	appendFileRow(later, false)
	setClock(d, time.Unix(1_800_000_000, 0).Add(fileRequestAnswerWait+time.Second))
	endWalk(d)
	reqs = fileRequestsSent(t, d, stub)
	if len(reqs) != 2 || len(reqs[1]) != 1 || reqs[1][0] != old {
		t.Fatalf("requests = %v, want the second naming only the first row again", reqs)
	}
}

func TestFileRequest_SentWhenEitherWalkEnds(t *testing.T) {
	for _, sweep := range []string{"this device started it", "my other device started it"} {
		t.Run(sweep, func(t *testing.T) {
			d, stub, _ := newAskingDevice(t)
			f := newSiblingFile(t)
			ctx := context.Background()
			if sweep == "this device started it" {
				askSibling(ctx, d, railSiblingID, true, false)
			} else {
				handleGapRequest(ctx, d, railSiblingID, syncrail.GapRequestBody{Reciprocate: true})
			}
			if !sentGapRequest(t, d, stub) {
				t.Fatal("no gap request went to my other device, so no walk began")
			}

			id := newMsgID(t)
			more := sealGapEntry(t, d, railSiblingID, syncrail.KindGapResponse, syncrail.GapResponseBody{
				Rows: []syncrail.RowSnapshot{gapRow(t, id, true, f, 100)}, RangeEnd: 1, More: true,
			})
			handleGapResponse(ctx, d, railSiblingID, mustParseGapResponse(t, d, more))
			if reqs := fileRequestsSent(t, d, stub); len(reqs) != 0 {
				t.Fatalf("a request went with more batches behind it: %v", reqs)
			}

			last := sealGapEntry(t, d, railSiblingID, syncrail.KindGapResponse, syncrail.GapResponseBody{RangeEnd: 2})
			processSiblingRailEnvelope(ctx, d, railEntry(railSiblingID, "env-last-"+id[:6], last))
			deadline := time.Now().Add(5 * time.Second)
			for len(fileRequestsSent(t, d, stub)) == 0 {
				if time.Now().After(deadline) {
					t.Fatal("no file request after the walk ended")
				}
				time.Sleep(10 * time.Millisecond)
			}
			reqs := fileRequestsSent(t, d, stub)
			if len(reqs) != 1 || len(reqs[0]) != 1 || reqs[0][0] != id {
				t.Fatalf("requests = %v, want one naming %s", reqs, id)
			}
			stub.mu.Lock()
			peers := append([]string(nil), stub.sentPeerIDs...)
			stub.mu.Unlock()
			if peers[len(peers)-1] != railSiblingID {
				t.Fatalf("the request went to %q, want my other device", peers[len(peers)-1])
			}
		})
	}
}

func sentGapRequest(t *testing.T, d *daemon, stub *filesStub) bool {
	t.Helper()
	key, err := d.railKey()
	if err != nil {
		t.Fatal(err)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	for _, p := range stub.sentPayloads {
		if env, err := syncrail.OpenEnvelope(key, p); err == nil && env.Kind == syncrail.KindGapRequest {
			return true
		}
	}
	return false
}

func mustParseGapResponse(t *testing.T, d *daemon, payload []byte) syncrail.GapResponseBody {
	t.Helper()
	body, err := syncrail.ParseGapResponse(openGapEnvelope(t, d, payload))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestFileRequest_LogNamesTheFilesByShortID(t *testing.T) {
	buf := captureRailLog(t)
	d, stub, _ := newAskingDevice(t)
	f := newSiblingFile(t)
	ids := []string{newMsgID(t), newMsgID(t)}
	for i, id := range ids {
		landGapFile(t, d, id, true, f, int64(100+i))
	}
	endWalk(d)
	if reqs := fileRequestsSent(t, d, stub); len(reqs) != 1 || len(reqs[0]) != 2 {
		t.Fatalf("requests = %v, want one naming both files", reqs)
	}
	line := waitForLog(t, buf, `msg="file request: files asked for"`)
	for _, id := range ids {
		if strings.Contains(line, id) {
			t.Errorf("the line holds the full id %s: %s", id, line)
		}
		if !strings.Contains(line, logid.Short(id)) {
			t.Errorf("the line does not name %s: %s", logid.Short(id), line)
		}
	}
}

func TestFileRequest_OncePerInterval(t *testing.T) {
	d, stub, _ := newAskingDevice(t)
	f := newSiblingFile(t)
	start := time.Unix(1_800_000_000, 0)
	setClock(d, start)
	landGapFile(t, d, newMsgID(t), true, f, 100)
	endWalk(d)

	landGapFile(t, d, newMsgID(t), true, f, 200)
	setClock(d, start.Add(fileRequestInterval-time.Second))
	endWalk(d)
	if n := len(fileRequestsSent(t, d, stub)); n != 1 {
		t.Fatalf("requests inside the interval = %d, want 1", n)
	}
	setClock(d, start.Add(fileRequestInterval))
	endWalk(d)
	if n := len(fileRequestsSent(t, d, stub)); n != 2 {
		t.Fatalf("requests after the interval = %d, want 2", n)
	}
}

func TestFileRequest_InTurnsNewestFirst(t *testing.T) {
	d, stub, chatID := newAskingDevice(t)
	f := newSiblingFile(t)
	start := time.Unix(1_800_000_000, 0)
	setClock(d, start)
	ids := make([]string, 12)
	for i := range ids {
		ids[i] = newMsgID(t)
		landGapFile(t, d, ids[i], true, f, int64(1000+i))
	}
	endWalk(d)
	reqs := fileRequestsSent(t, d, stub)
	if len(reqs) != 1 || len(reqs[0]) != fileRequestsInFlight {
		t.Fatalf("first request = %v, want %d ids", reqs, fileRequestsInFlight)
	}
	for i, id := range reqs[0] {
		if id != ids[11-i] {
			t.Fatalf("id %d = %s, want the rows newest first (%s)", i, id, ids[11-i])
		}
	}

	setClock(d, start.Add(2*fileRequestInterval))
	endWalk(d)
	if n := len(fileRequestsSent(t, d, stub)); n != 1 {
		t.Fatalf("requests while ten were unanswered = %d, want still 1", n)
	}

	setClock(d, start.Add(fileRequestAnswerWait+time.Second))
	endWalk(d)
	reqs = fileRequestsSent(t, d, stub)
	if len(reqs) != 2 || len(reqs[1]) != fileRequestsInFlight {
		t.Fatalf("second request = %v", reqs)
	}
	want := append([]string{ids[1], ids[0]}, ids[11], ids[10], ids[9], ids[8], ids[7], ids[6], ids[5], ids[4])
	for i := range want {
		if reqs[1][i] != want[i] {
			t.Fatalf("second request = %v, want %v", reqs[1], want)
		}
	}
	_, body := fileRow(t, d, chatID, ids[0])
	if body.State != string(files.StatePending) {
		t.Fatalf("an unanswered row moved to %q, want pending", body.State)
	}
}

func TestPickFileRequest_Limits(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	var wanted []files.WantedFile
	for i := 0; i < 70; i++ {
		wanted = append(wanted, files.WantedFile{MsgID: fmt.Sprintf("%032x", i), DisplayTs: int64(i)})
	}
	ids, _ := pickFileRequest(wanted, 0, now, 1000)
	if len(ids) != syncrail.MaxFileRequestIDs {
		t.Fatalf("ids = %d, want the envelope limit %d", len(ids), syncrail.MaxFileRequestIDs)
	}
	if err := (syncrail.FileRequestBody{MsgIDs: ids}).Check(); err != nil {
		t.Fatalf("the pick builds a request the responder refuses: %v", err)
	}
	ids, _ = pickFileRequest(wanted, 7, now, fileRequestsInFlight)
	if len(ids) != 3 {
		t.Fatalf("ids with seven downloading = %d, want 3", len(ids))
	}
	ids, _ = pickFileRequest(wanted, fileRequestsInFlight, now, fileRequestsInFlight)
	if len(ids) != 0 {
		t.Fatalf("ids with the limit downloading = %d, want 0", len(ids))
	}
}

func TestGapSiblingOffer_TakenOntoTheHeldRowKeepingItsDirection(t *testing.T) {
	for _, inbound := range []bool{true, false} {
		t.Run(fmt.Sprintf("inbound=%t", inbound), func(t *testing.T) {
			buf := captureRailLog(t)
			d, stub, chatID := newAskingDevice(t)
			f := newSiblingFile(t)
			id := newMsgID(t)
			landGapFile(t, d, id, inbound, f, 100)
			before, _ := d.events.List(chatID, 0, 0)
			fetchBefore, sendsBefore, _ := stubCounts(stub)

			body := gapOfferFor(t, id, f, stagingFor(1), "tok-gap-1")
			secrets := secretsOf(body)
			processSiblingRailEnvelope(context.Background(), d, railEntry(railSiblingID, "env-offer-"+id[:6],
				sealGapEntry(t, d, railSiblingID, syncrail.KindGapSiblingOffer, body)))
			waitForLog(t, buf, `msg="sibling offer: fetch enqueued on haomad"`, "route=gapfill")

			fetches, _, _ := stubCounts(stub)
			stub.mu.Lock()
			fetch := stub.lastFetch
			stub.mu.Unlock()
			if fetches != fetchBefore+1 || fetch.MsgID != stagingFor(1) || fetch.PeerID != railSiblingID || fetch.Token != "tok-gap-1" {
				t.Fatalf("fetch calls %d->%d, last = %+v; want one fetch from my other device by the staging id",
					fetchBefore, fetches, fetch)
			}
			meta, err := d.files.GetMeta(id)
			if err != nil {
				t.Fatal(err)
			}
			wantDir := files.DirOut
			if inbound {
				wantDir = files.DirIn
			}
			if meta.Direction != wantDir || !meta.FetchedFromSibling || !meta.GapFill || meta.GapAsks != 1 || meta.Token != "" {
				t.Fatalf("meta = dir %s from_sibling %t gap_fill %t asks %d token %q; want %s, true, true, 1, none",
					meta.Direction, meta.FetchedFromSibling, meta.GapFill, meta.GapAsks, meta.Token, wantDir)
			}
			if _, still := wantedOf(t, d)[id]; still {
				t.Fatal("the file is still wanted after its offer was taken")
			}
			after, _ := d.events.List(chatID, 0, 0)
			if len(after) != len(before) {
				t.Fatalf("rows %d -> %d; the take appended a row", len(before), len(after))
			}

			stub.mu.Lock()
			stub.stagingBody = f.ciphertext
			stub.mu.Unlock()
			fileFetchStateHandler(context.Background(), d)(backendapi.FileFetchEvent{MsgID: stagingFor(1), State: string(files.StateReady)})
			got, err := d.files.UnsealAtRest(chatID, id)
			if err != nil || !bytes.Equal(got, f.plaintext) {
				t.Fatalf("sealed copy = %q, %v; want the file", got, err)
			}
			ev, rowBody := fileRow(t, d, chatID, id)
			if rowBody.State != string(files.StateReady) || (ev.Direction == events.DirIn) != inbound {
				t.Fatalf("row state %s direction %s, want ready and the direction it had", rowBody.State, ev.Direction)
			}
			waitForLog(t, buf, `msg="sibling offer: file sealed"`, "route=gapfill")

			_, sends, receipts := stubCounts(stub)
			if sends != sendsBefore || receipts != 0 {
				t.Fatalf("sends %d->%d, receipt acks %d; the take answers nobody, the contact least", sendsBefore, sends, receipts)
			}
			log := buf.String()
			for _, s := range secrets {
				if strings.Contains(log, s) {
					t.Fatalf("a secret reached the log: %q", s)
				}
			}
		})
	}
}

func TestSiblingOffer_SealLogNamesTheSendRoute(t *testing.T) {
	buf := captureRailLog(t)
	d, stub, _ := newSiblingReceiver(t)
	f := newSiblingFile(t)
	w := siblingOfferWrapper(t, f, "")
	processInboxEntry(context.Background(), d, railEntry(railSiblingID, "env-sib-route", sealSiblingOffer(t, d, w, offerFor(f))))
	stub.mu.Lock()
	stub.stagingBody = f.ciphertext
	stub.mu.Unlock()
	fileFetchStateHandler(context.Background(), d)(backendapi.FileFetchEvent{MsgID: siblingTestStaging, State: string(files.StateReady)})
	waitForLog(t, buf, `msg="sibling offer: file sealed"`, "route=send")
	if _, still := wantedOf(t, d)[w.MsgID]; still {
		t.Fatal("a file taken at send is wanted")
	}
}

func TestGapSiblingOffer_SecondOfferPostsNoFetch(t *testing.T) {
	buf := captureRailLog(t)
	d, stub, chatID := newAskingDevice(t)
	f := newSiblingFile(t)
	id := newMsgID(t)
	landGapFile(t, d, id, true, f, 100)
	handleGapSiblingOffer(context.Background(), d, railSiblingID, gapOfferFor(t, id, f, stagingFor(1), "tok-gap-1"))
	fetches, _, _ := stubCounts(stub)
	if fetches != 1 {
		t.Fatalf("fetches after the first offer = %d, want 1", fetches)
	}
	handleGapSiblingOffer(context.Background(), d, railSiblingID, gapOfferFor(t, id, f, stagingFor(1), "tok-gap-1"))
	handleGapSiblingOffer(context.Background(), d, railSiblingID, gapOfferFor(t, id, f, stagingFor(2), "tok-gap-2"))
	if fetches, _, _ = stubCounts(stub); fetches != 1 {
		t.Fatalf("fetches after two more offers = %d, want still 1", fetches)
	}
	if meta, _ := d.files.GetMeta(id); meta.StagingID != stagingFor(1) {
		t.Fatalf("the first take was overwritten: staging %s", meta.StagingID)
	}

	other := newMsgID(t)
	landGapFile(t, d, other, true, f, 200)
	w, offer, entry := contactOffer(other)
	offer.Size = uint64(len(f.plaintext)) + 16
	ingestFileOffer(context.Background(), d, chatID, entry, w, offer)
	fetchBefore, _, _ := stubCounts(stub)
	handleGapSiblingOffer(context.Background(), d, railSiblingID, gapOfferFor(t, other, f, stagingFor(3), "tok-gap-3"))
	if fetches, _, _ = stubCounts(stub); fetches != fetchBefore {
		t.Fatalf("an offer after a contact's posted a fetch")
	}
	if !strings.Contains(buf.String(), "outcome=already_held") {
		t.Fatalf("no already_held line:\n%s", buf.String())
	}
}

func TestGapSiblingOffer_RefusedWhenItDoesNotFitTheRow(t *testing.T) {
	cases := map[string]func(t *testing.T, d *daemon, body *syncrail.GapSiblingOfferBody){
		"another person's fingerprint": func(t *testing.T, d *daemon, body *syncrail.GapSiblingOfferBody) {
			const otherPeer = "0000000000000000000000000000cafe"
			const otherFingerprint = "ff11ee22dd33cc44bb55aa6699887766"
			seedKnownPeer(t, d, otherPeer, otherFingerprint)
			body.Fingerprint = otherFingerprint
		},
		"an unknown fingerprint": func(t *testing.T, d *daemon, body *syncrail.GapSiblingOfferBody) {
			body.Fingerprint = "00000000000000000000000000000001"
		},
		"another size": func(t *testing.T, d *daemon, body *syncrail.GapSiblingOfferBody) {

			var fo msg.FileOfferBody
			_ = json.Unmarshal(body.FileOffer, &fo)
			fo.Size++
			body.FileOffer, _ = json.Marshal(fo)
			body.SiblingOffer.PlaintextSize++
		},
	}
	for name, spoil := range cases {
		t.Run(name, func(t *testing.T) {
			buf := captureRailLog(t)
			d, stub, _ := newAskingDevice(t)
			f := newSiblingFile(t)
			id := newMsgID(t)
			landGapFile(t, d, id, true, f, 100)
			body := gapOfferFor(t, id, f, stagingFor(1), "tok-gap-1")
			spoil(t, d, &body)
			handleGapSiblingOffer(context.Background(), d, railSiblingID, body)
			if fetches, _, _ := stubCounts(stub); fetches != 0 {
				t.Fatalf("fetches = %d, want 0", fetches)
			}
			if _, err := d.files.GetMeta(id); !errors.Is(err, files.ErrMetaNotFound) {
				t.Fatalf("metadata written for a refused offer (err=%v)", err)
			}
			waitForLog(t, buf, `msg="sibling offer on gap-fill: refused"`)
		})
	}
}

func TestStagedFetchSweep_SkipsAContactFileFromMyOtherDevice(t *testing.T) {
	d, stub, _ := newAskingDevice(t)
	f := newSiblingFile(t)
	id := newMsgID(t)
	landGapFile(t, d, id, true, f, 100)
	handleGapSiblingOffer(context.Background(), d, railSiblingID, gapOfferFor(t, id, f, stagingFor(1), "tok-gap-1"))
	stub.mu.Lock()
	stub.stagingBody = f.ciphertext
	stub.haomadIDs = nil
	sendsBefore := stub.sendCalls
	stub.mu.Unlock()

	sweepStagedFetches(context.Background(), d)

	stub.mu.Lock()
	ids, sends := append([]string(nil), stub.haomadIDs...), stub.sendCalls
	stub.mu.Unlock()
	for _, got := range ids {
		if got == id {
			t.Fatalf("the contact's sweep handed haomad the msg id: %v", ids)
		}
	}
	if sends != sendsBefore {
		t.Fatalf("sends %d -> %d; the contact's sweep sent for a file from my other device", sendsBefore, sends)
	}
}

func takeAndFail(t *testing.T, d *daemon, id string, f siblingFile, i int, reason string) {
	t.Helper()
	handleGapSiblingOffer(context.Background(), d, railSiblingID, gapOfferFor(t, id, f, stagingFor(i), fmt.Sprintf("tok-gap-%d", i)))
	if _, err := d.files.GetMeta(id); err != nil {
		t.Fatalf("offer %d not taken: %v", i, err)
	}
	fileFetchStateHandler(context.Background(), d)(backendapi.FileFetchEvent{
		MsgID: stagingFor(i), State: string(files.StateFailedPermanent), Reason: reason,
	})
}

func TestGapFillDownload_FailureReasons(t *testing.T) {
	cases := map[string]bool{
		backendapi.FetchFailBlobGone:  true,
		backendapi.FetchFailLimit:     true,
		backendapi.FetchFailIntegrity: false,
		"":                            false,
	}
	for reason, askAgain := range cases {
		t.Run("reason="+reason, func(t *testing.T) {
			d, _, chatID := newAskingDevice(t)
			f := newSiblingFile(t)
			id := newMsgID(t)
			landGapFile(t, d, id, true, f, 100)
			takeAndFail(t, d, id, f, 1, reason)

			_, body := fileRow(t, d, chatID, id)
			meta, metaErr := d.files.GetMeta(id)
			w, wanted := wantedOf(t, d)[id]
			if askAgain {
				if !errors.Is(metaErr, files.ErrMetaNotFound) || !wanted || w.Asks != 1 || body.State != string(files.StatePending) {
					t.Fatalf("meta err %v, wanted %t (%+v), row %s; want no metadata, wanted with one ask, pending", metaErr, wanted, w, body.State)
				}
				if _, err := d.files.GetSiblingOffer(id); !errors.Is(err, files.ErrSiblingOfferNotFound) {
					t.Errorf("held key kept: %v", err)
				}
				if _, err := d.files.SiblingOfferMsgID(stagingFor(1)); !errors.Is(err, files.ErrSiblingOfferNotFound) {
					t.Errorf("staging entry kept: %v", err)
				}
				return
			}
			if metaErr != nil || meta.State != files.StateFailedPermanent || wanted || body.State != string(files.StateFailedPermanent) {
				t.Fatalf("meta %+v (%v), wanted %t, row %s; want final", meta, metaErr, wanted, body.State)
			}
			if meta.FailReason != reason {
				t.Errorf("fail reason = %q, want %q", meta.FailReason, reason)
			}
		})
	}
}

func TestContactFile_PermanentFailureStaysFinal(t *testing.T) {
	d, _, chatID := newAskingDevice(t)
	f := newSiblingFile(t)
	id := newMsgID(t)
	landGapFile(t, d, id, true, f, 100)
	w, offer, entry := contactOffer(id)
	offer.Size = uint64(len(f.plaintext)) + 16
	ingestFileOffer(context.Background(), d, chatID, entry, w, offer)
	fileFetchStateHandler(context.Background(), d)(backendapi.FileFetchEvent{
		MsgID: id, State: string(files.StateFailedPermanent), Reason: backendapi.FetchFailBlobGone,
	})
	meta, err := d.files.GetMeta(id)
	if err != nil || meta.State != files.StateFailedPermanent {
		t.Fatalf("meta %+v (%v), want failed_permanent", meta, err)
	}
	if _, wanted := wantedOf(t, d)[id]; wanted {
		t.Fatal("a contact's failed file was put back in the wanted set")
	}
}

func TestGapFillDownload_DecryptFailureIsIntegrity(t *testing.T) {
	d, stub, chatID := newAskingDevice(t)
	f := newSiblingFile(t)
	id := newMsgID(t)
	landGapFile(t, d, id, true, f, 100)
	body := gapOfferFor(t, id, f, stagingFor(1), "tok-gap-1")
	body.SiblingOffer.Key = bytes.Repeat([]byte{0x11}, len(f.key))
	handleGapSiblingOffer(context.Background(), d, railSiblingID, body)
	stub.mu.Lock()
	stub.stagingBody = f.ciphertext
	stub.mu.Unlock()
	fileFetchStateHandler(context.Background(), d)(backendapi.FileFetchEvent{MsgID: stagingFor(1), State: string(files.StateReady)})
	meta, err := d.files.GetMeta(id)
	if err != nil || meta.State != files.StateFailedPermanent || meta.FailReason != files.FailIntegrity {
		t.Fatalf("meta %+v (%v), want failed_permanent for integrity", meta, err)
	}
	if _, wanted := wantedOf(t, d)[id]; wanted {
		t.Fatal("a file with wrong bytes is wanted again")
	}
	_, row := fileRow(t, d, chatID, id)
	if row.State != string(files.StateFailedPermanent) {
		t.Fatalf("row %s, want failed_permanent", row.State)
	}
}

func TestGapFillDownload_AskLimitAndTheRetryVerb(t *testing.T) {
	d, _, chatID := newAskingDevice(t)
	f := newSiblingFile(t)
	id := newMsgID(t)
	landGapFile(t, d, id, true, f, 100)
	for i := 1; i <= files.MaxGapAsks; i++ {
		takeAndFail(t, d, id, f, i, backendapi.FetchFailBlobGone)
		w, wanted := wantedOf(t, d)[id]
		if i < files.MaxGapAsks {
			if !wanted || w.Asks != i {
				t.Fatalf("after failure %d: wanted %t asks %d, want wanted with %d", i, wanted, w.Asks, i)
			}
			continue
		}
		if wanted {
			t.Fatalf("after failure %d the file is still wanted; the limit is %d", i, files.MaxGapAsks)
		}
	}
	meta, err := d.files.GetMeta(id)
	if err != nil || meta.State != files.StateFailedPermanent || meta.GapAsks != files.MaxGapAsks {
		t.Fatalf("meta %+v (%v), want final at the limit", meta, err)
	}

	retryGapFiles(d)
	if _, err := d.files.GetMeta(id); !errors.Is(err, files.ErrMetaNotFound) {
		t.Fatalf("the retry verb kept the failed metadata (err=%v)", err)
	}
	if w, wanted := wantedOf(t, d)[id]; !wanted || w.Asks != 0 || w.AskedAt != 0 {
		t.Fatalf("after retry: wanted %t %+v, want wanted with the count at zero", wanted, w)
	}
	_, row := fileRow(t, d, chatID, id)
	if row.State != string(files.StatePending) {
		t.Fatalf("row %s after retry, want pending", row.State)
	}
}

func TestRetryVerb_ResetsTheGapFillAsks(t *testing.T) {
	hs := startHaomadStub(t, nil, http.StatusOK)
	d, addr, certPath, token := newTestDaemon(t, hs)
	mgr, err := files.NewManager(d.store, d.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	d.files = mgr
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)

	seed := func(reason string, asks int) string {
		id := newMsgID(t)
		body, _ := json.Marshal(FileEventBody{Name: "p.jpg", Size: 100, State: string(files.StateFailedPermanent)})
		if _, err := d.events.AppendInbound(events.InboundParams{
			ChatID: chatID, Kind: events.KindFile, SenderTs: 1, SenderPeerID: railPeerID,
			MsgID: id, Status: events.DecryptOK, Body: body,
		}); err != nil {
			t.Fatal(err)
		}
		if err := mgr.TakeGapSiblingOffer(files.Metadata{
			MsgID: id, ChatID: chatID, Direction: files.DirIn, State: files.StateFailedPermanent,
			FetchedFromSibling: true, GapFill: true, StagingID: stagingFor(asks), FailReason: reason,
		}, siblingHeldOfferAt(id, stagingFor(asks))); err != nil {
			t.Fatal(err)
		}
		return id
	}
	gone := seed(backendapi.FetchFailBlobGone, 1)
	wrong := seed(files.FailIntegrity, 2)
	waiting := newMsgID(t)
	if _, err := mgr.AddWantedFile(files.WantedFile{MsgID: waiting, ChatID: chatID, Asks: 7, AskedAt: 99}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)
	req, _ := ipc.NewFrame(ipc.FrameRetryFiles, "retry-1", struct{}{})
	writeFrame(t, ctx, conn, req)
	readRetryAnswer(t, ctx, conn)

	wanted := wantedOf(t, d)
	if w, ok := wanted[gone]; !ok || w.Asks != 0 {
		t.Errorf("the gone blob's file: wanted %t %+v, want wanted at zero", ok, w)
	}
	if _, ok := wanted[wrong]; ok {
		t.Error("a file with wrong bytes was asked for again")
	}
	if w := wanted[waiting]; w.Asks != 0 || w.AskedAt != 0 {
		t.Errorf("a waiting file kept its count: %+v", w)
	}
}

func readRetryAnswer(t *testing.T, ctx context.Context, conn *websocket.Conn) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		readCtx, c := context.WithTimeout(ctx, time.Until(deadline))
		_, data, err := conn.Read(readCtx)
		c()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		f, err := ipc.Decode(data)
		if err != nil {
			t.Fatal(err)
		}
		if f.ID == "retry-1" {
			return
		}
	}
	t.Fatal("no answer to the retry verb")
}

func siblingHeldOfferAt(msgID, staging string) files.SiblingOffer {
	o := siblingHeldOffer(msgID)
	o.StagingID = staging
	return o
}

func TestGapFill_SettledDownloadsKeepAskingWithNoOtherActivity(t *testing.T) {
	d, stub, _ := newAskingDevice(t)
	f := newSiblingFile(t)
	stub.mu.Lock()
	stub.stagingBody = f.ciphertext
	stub.mu.Unlock()
	const total = 25
	ids := make([]string, total)
	for i := range ids {
		ids[i] = newMsgID(t)
		landGapFile(t, d, ids[i], true, f, int64(1000+i))
	}

	var clockMu sync.Mutex
	clock := time.Unix(1_800_000_000, 0)
	d.fileAsker.now = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		clock = clock.Add(2 * time.Minute)
		return clock
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sweeps := 0
	tr := newSyncTrigger(func(context.Context) sweepOutcome {
		sweeps++
		endWalk(d)
		return sweepStarted
	})
	tr.timing = syncTriggerTiming{debounce: 5 * time.Millisecond, ceiling: 20 * time.Millisecond, minInterval: 10 * time.Millisecond}
	d.syncTrigger = tr
	go tr.run(ctx)

	key, err := d.railKey()
	if err != nil {
		t.Fatal(err)
	}
	served := map[string]bool{}
	var maxIDs int
	respond := func() {
		stub.mu.Lock()
		payloads := append([][]byte(nil), stub.sentPayloads...)
		stub.mu.Unlock()
		for _, p := range payloads {
			env, err := syncrail.OpenEnvelope(key, p)
			if err != nil || env.Kind != syncrail.KindFileRequest {
				continue
			}
			body, err := syncrail.ParseFileRequest(env)
			if err != nil {
				continue
			}
			maxIDs = max(maxIDs, len(body.MsgIDs))
			for _, id := range body.MsgIDs {
				if served[id] {
					continue
				}
				served[id] = true
				n := len(served)
				handleGapSiblingOffer(ctx, d, railSiblingID, gapOfferFor(t, id, f, stagingFor(n), fmt.Sprintf("tok-live-%d", n)))
				fileFetchStateHandler(ctx, d)(backendapi.FileFetchEvent{MsgID: stagingFor(n), State: string(files.StateReady)})
			}
		}
	}

	endWalk(d)
	deadline := time.Now().Add(20 * time.Second)
	for {
		respond()
		ready := 0
		for _, id := range ids {
			if m, err := d.files.GetMeta(id); err == nil && m.State == files.StateReady {
				ready++
			}
		}
		if ready == total {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d files arrived, %d asked for; an idle requester stopped asking", ready, total, len(served))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if maxIDs > fileRequestsInFlight {
		t.Fatalf("a request named %d files, over the in-flight limit %d", maxIDs, fileRequestsInFlight)
	}
	cancel()
}

func TestGapDownloadsInFlight_CountsOnlyRunningDownloads(t *testing.T) {
	d, _, _ := newAskingDevice(t)
	f := newSiblingFile(t)
	states := []files.State{files.StatePending, files.StateDownloading, files.StateAwaitingKey, files.StateFailedTransient}
	for i, st := range states {
		id := newMsgID(t)
		landGapFile(t, d, id, true, f, int64(100+i))
		handleGapSiblingOffer(context.Background(), d, railSiblingID, gapOfferFor(t, id, f, stagingFor(i+1), fmt.Sprintf("tok-%d", i)))
		if err := d.files.UpdateMeta(id, func(m *files.Metadata) error { m.State = st; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	n, err := gapDownloadsInFlight(d)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("in flight = %d, want 3: pending, downloading, awaiting key; not the transient failure", n)
	}
}

func TestGapFillDownload_LateFailureOfASealedFileIsIgnored(t *testing.T) {
	for _, reason := range []string{backendapi.FetchFailBlobGone, backendapi.FetchFailIntegrity} {
		t.Run(reason, func(t *testing.T) {
			d, _, chatID := newAskingDevice(t)
			f := newSiblingFile(t)
			id := newMsgID(t)
			landGapFile(t, d, id, true, f, 100)
			handleGapSiblingOffer(context.Background(), d, railSiblingID, gapOfferFor(t, id, f, stagingFor(1), "tok-1"))

			if err := d.files.UpdateMeta(id, func(m *files.Metadata) error { m.State = files.StateReady; return nil }); err != nil {
				t.Fatal(err)
			}
			applySiblingFetchState(context.Background(), d, slog.Default(), id, files.StateFailedPermanent, 0, reason)
			meta, err := d.files.GetMeta(id)
			if err != nil || meta.State != files.StateReady || meta.FailReason != "" {
				t.Fatalf("meta %+v (%v), want ready with no reason", meta, err)
			}
			if _, wanted := wantedOf(t, d)[id]; wanted {
				t.Fatal("a sealed file was put back in the wanted set")
			}
			_ = chatID
		})
	}
}

func TestGapFillDownload_FailureOnADeletedRowIsNotAskedAgain(t *testing.T) {
	d, _, _ := newAskingDevice(t)
	f := newSiblingFile(t)
	id := newMsgID(t)
	landGapFile(t, d, id, true, f, 100)
	handleGapSiblingOffer(context.Background(), d, railSiblingID, gapOfferFor(t, id, f, stagingFor(1), "tok-1"))
	if _, err := d.events.ApplyDelete(id, 5, railPeerID); err != nil {
		t.Fatal(err)
	}
	applySiblingFetchState(context.Background(), d, slog.Default(), id, files.StateFailedPermanent, 0, backendapi.FetchFailBlobGone)
	if _, wanted := wantedOf(t, d)[id]; wanted {
		t.Fatal("a deleted row's file was put back in the wanted set")
	}
}
