package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/chacha20poly1305"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/files"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/syncrail"
)

func noExpiry(context.Context, time.Time) time.Time { return time.Time{} }

func startTestFileServer(t *testing.T, d *daemon) {
	t.Helper()
	d.fileServer = newDaemonFileServer(d)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.fileServer.run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func newServingDaemon(t *testing.T) (*daemon, *sendFileStub, chat.ChatID) {
	t.Helper()
	stub := startSendFileStub(t)
	d := newSendFileTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)
	startTestFileServer(t, d)
	d.backendReachable.Store(true)
	return d, stub, chatID
}

type servable string

const (
	servableSent        servable = "a file I sent"
	servableFromContact servable = "a file from a contact"
	servableFromSibling servable = "a file fetched from my other device"
)

const priorStagingID = "9e8d7c6b5a49382716059e8d7c6b5a49"

func seedServableFile(t *testing.T, d *daemon, chatID chat.ChatID, kind servable) (string, []byte) {
	t.Helper()
	msgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	plain := make([]byte, 3000)
	if _, err := rand.Read(plain); err != nil {
		t.Fatal(err)
	}
	sealed, err := d.files.SealAtRest(chatID, msgID, plain)
	if err != nil {
		t.Fatal(err)
	}
	meta := files.Metadata{
		MsgID:             msgID,
		ChatID:            chatID,
		OriginalName:      "report.pdf",
		Mime:              "application/pdf",
		Size:              uint64(len(plain)) + chacha20poly1305.Overhead,
		SealedPath:        sealed,
		State:             files.StateReady,
		SiblingStagingIDs: []string{priorStagingID},
	}
	body := mustJSON(t, FileEventBody{
		Name: "report.pdf", Size: meta.Size, Mime: "application/pdf",
		State: string(files.StateReady), Caption: "the quarterly one",
	})
	switch kind {
	case servableSent:
		meta.Direction = files.DirOut
		meta.Token = "tok-contact"
		_, err = d.events.AppendOutbound(events.OutboundParams{
			ChatID: chatID, Kind: events.KindFile, SenderSeq: 1, EnvelopeID: "env-" + msgID, MsgID: msgID, Body: body,
		})
	case servableFromContact:
		meta.Direction = files.DirIn
		meta.Token = "tok-from-contact"
		_, err = d.events.AppendInbound(events.InboundParams{
			ChatID: chatID, Kind: events.KindFile, SenderTs: time.Now().Unix(), SenderSeq: 1,
			EnvelopeID: "env-" + msgID, MsgID: msgID, Status: events.DecryptOK, Body: body,
		})
	case servableFromSibling:
		meta.Direction = files.DirOut
		meta.FetchedFromSibling = true
		meta.StagingID = "1f2e3d4c5b6a79881f2e3d4c5b6a7988"
		_, err = d.events.AppendOutbound(events.OutboundParams{
			ChatID: chatID, Kind: events.KindFile, MsgID: msgID, Body: body, MirroredFromSibling: true,
		})
	default:
		t.Fatalf("unknown kind %q", kind)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := d.files.PutMeta(meta); err != nil {
		t.Fatal(err)
	}
	return msgID, plain
}

func requestFiles(t *testing.T, d *daemon, envelopeID string, msgIDs ...string) {
	t.Helper()
	payload := sealGapEntry(t, d, railSiblingID, syncrail.KindFileRequest, syncrail.FileRequestBody{MsgIDs: msgIDs})
	processSiblingRailEnvelope(context.Background(), d, railEntry(railSiblingID, envelopeID, payload))
}

type stubCalls struct {
	stages   []backendapi.StageFileRequest
	drops    []string
	sentTo   []string
	payloads [][]byte
	kinds    []string
}

func (s *sendFileStub) calls() stubCalls {
	s.mu.Lock()
	defer s.mu.Unlock()
	return stubCalls{
		stages:   append([]backendapi.StageFileRequest(nil), s.stages...),
		drops:    append([]string(nil), s.dropIDs...),
		sentTo:   append([]string(nil), s.sentPeerIDs...),
		payloads: append([][]byte(nil), s.sentPayloads...),
		kinds:    append([]string(nil), s.sentKinds...),
	}
}

func TestFileRequest_ServesTheSealedCopyInEachDirection(t *testing.T) {
	for _, kind := range []servable{servableSent, servableFromContact, servableFromSibling} {
		t.Run(string(kind), func(t *testing.T) {
			d, stub, chatID := newServingDaemon(t)
			buf := captureRailLog(t)
			msgID, plain := seedServableFile(t, d, chatID, kind)

			requestFiles(t, d, "env-req-1", msgID)
			waitForLog(t, buf, `msg="file request: served"`, "outcome=served", "msg_id="+msgID)

			c := stub.calls()
			if len(c.stages) != 1 {
				t.Fatalf("stagings = %d, want 1", len(c.stages))
			}
			stage := c.stages[0]
			if stage.MsgID == msgID || stage.MsgID == "" {
				t.Fatalf("staged under %q; want a random staging id, never the msg id", stage.MsgID)
			}
			if len(stage.RecipientPeerIDs) != 1 || stage.RecipientPeerIDs[0] != railSiblingID {
				t.Fatalf("staged for %v; want my other device alone", stage.RecipientPeerIDs)
			}
			if len(c.sentTo) != 1 || c.sentTo[0] != railSiblingID {
				t.Fatalf("sends = %v; want one, to the requester", c.sentTo)
			}
			if c.kinds[0] != "" {
				t.Fatalf("wire-kind hint leaked to the router: %q", c.kinds[0])
			}

			env := openGapEnvelope(t, d, c.payloads[0])
			got, err := syncrail.ParseGapSiblingOffer(env)
			if err != nil {
				t.Fatalf("ParseGapSiblingOffer: %v", err)
			}
			if got.MsgID != msgID || got.Fingerprint != railFingerprint {
				t.Fatalf("offer for msg %q fingerprint %q", got.MsgID, got.Fingerprint)
			}
			fo, reason := gapSiblingOfferRefusal(got)
			if reason != "" {
				t.Fatalf("the requester would refuse the offer: %s", reason)
			}
			if fo.Name != "report.pdf" || fo.Mime != "application/pdf" || fo.Caption != "the quarterly one" {
				t.Fatalf("file offer lost the row's name, mime or caption")
			}
			so := got.SiblingOffer
			if so.StagingID != stage.MsgID {
				t.Fatalf("offer names staging id %q, the staging was %q", so.StagingID, stage.MsgID)
			}
			if so.Token != "tok-mint-"+hex.EncodeToString([]byte{1, 0}) {
				t.Fatalf("offer token is not the one minted for the requester")
			}
			sum := sha256.Sum256(stage.Ciphertext)
			if so.Sha256Ciphertext != hex.EncodeToString(sum[:]) || so.BlobSize != uint64(len(stage.Ciphertext)) {
				t.Fatalf("offer hash or size does not describe the staged blob")
			}
			aead, err := chacha20poly1305.NewX(so.Key)
			if err != nil {
				t.Fatal(err)
			}
			opened, err := aead.Open(nil, so.Nonce, stage.Ciphertext, nil)
			if err != nil {
				t.Fatalf("the offer's key does not open the staged blob: %v", err)
			}
			if so.PlaintextSize != uint64(len(plain)) || !bytes.Equal(opened[:so.PlaintextSize], plain) {
				t.Fatal("the staged blob does not open to the file")
			}
			if len(opened) == len(plain) {
				t.Fatal("the staged blob carries no padding")
			}

			meta, err := d.files.GetMeta(msgID)
			if err != nil {
				t.Fatal(err)
			}
			if len(meta.SiblingStagingIDs) != 2 || meta.SiblingStagingIDs[0] != priorStagingID || meta.SiblingStagingIDs[1] != stage.MsgID {
				t.Fatalf("SiblingStagingIDs = %v, want [%s %s]", meta.SiblingStagingIDs, priorStagingID, stage.MsgID)
			}
			if meta.Direction != map[servable]files.Direction{servableSent: files.DirOut, servableFromContact: files.DirIn, servableFromSibling: files.DirOut}[kind] {
				t.Fatalf("the row's direction changed to %q", meta.Direction)
			}
			if len(c.drops) != 0 {
				t.Fatalf("a served blob was dropped: %v", c.drops)
			}

			log := buf.String()
			secrets := []string{
				so.Token,
				hex.EncodeToString(so.Key), hex.EncodeToString(so.Nonce),

				base64.StdEncoding.EncodeToString(so.Key), base64.StdEncoding.EncodeToString(so.Nonce),
				fmt.Sprint(so.Key), fmt.Sprint(so.Nonce),
				string(got.FileOffer),

				string(plain), hex.EncodeToString(plain), base64.StdEncoding.EncodeToString(plain), fmt.Sprint(plain),
			}
			for _, s := range secrets {
				if strings.Contains(log, s) {
					t.Fatalf("a secret reached the log: %q", s)
				}
			}
		})
	}
}

func TestFileRequest_ServesWhatItCanAndSkipsTheRest(t *testing.T) {
	d, stub, chatID := newServingDaemon(t)
	buf := captureRailLog(t)
	first, _ := seedServableFile(t, d, chatID, servableSent)
	second, _ := seedServableFile(t, d, chatID, servableFromContact)
	missing := "00112233445566778899aabbccddeeff"

	requestFiles(t, d, "env-req-1", first, missing, second)
	waitForLog(t, buf, `msg="file request: served"`, "msg_id="+second)
	waitForLog(t, buf, `msg="file request: not served"`, "outcome=no_row", "msg_id="+missing)
	c := stub.calls()
	if len(c.stages) != 2 || len(c.sentTo) != 2 {
		t.Fatalf("stagings = %d, sends = %d; want 2 and 2", len(c.stages), len(c.sentTo))
	}
	for i, want := range []string{first, second} {
		got, err := syncrail.ParseGapSiblingOffer(openGapEnvelope(t, d, c.payloads[i]))
		if err != nil {
			t.Fatal(err)
		}
		if got.MsgID != want {
			t.Fatalf("offer %d is for %q, want %q", i, got.MsgID, want)
		}
	}
}

func TestFileRequest_NotServedSendsNothing(t *testing.T) {
	cases := map[string]struct {
		outcome string
		level   string
		seed    func(t *testing.T, d *daemon, chatID chat.ChatID) string
	}{
		"no row": {"no_row", "DEBUG", func(*testing.T, *daemon, chat.ChatID) string {
			return "00112233445566778899aabbccddeeff"
		}},
		"deleted row": {"row_deleted", "DEBUG", func(t *testing.T, d *daemon, chatID chat.ChatID) string {
			id, _ := seedServableFile(t, d, chatID, servableSent)
			if _, err := d.events.ApplyDelete(id, time.Now().Unix(), ""); err != nil {
				t.Fatal(err)
			}
			return id
		}},
		"not a file": {"not_a_file", "DEBUG", func(t *testing.T, d *daemon, chatID chat.ChatID) string {
			return seedText(t, d, chatID, "not a file")
		}},
		"no metadata": {"no_metadata", "DEBUG", func(t *testing.T, d *daemon, chatID chat.ChatID) string {
			id, _ := seedServableFile(t, d, chatID, servableFromContact)
			if err := d.files.DeleteMeta(id); err != nil {
				t.Fatal(err)
			}
			return id
		}},
		"not ready": {"not_ready", "DEBUG", func(t *testing.T, d *daemon, chatID chat.ChatID) string {
			id, _ := seedServableFile(t, d, chatID, servableFromContact)
			if err := d.files.UpdateMeta(id, func(m *files.Metadata) error { m.State = files.StateDownloading; return nil }); err != nil {
				t.Fatal(err)
			}
			return id
		}},
		"no sealed copy": {"no_sealed_copy", "DEBUG", func(t *testing.T, d *daemon, chatID chat.ChatID) string {
			id, _ := seedServableFile(t, d, chatID, servableSent)
			if err := d.files.DeleteSealed(chatID, id); err != nil {
				t.Fatal(err)
			}
			return id
		}},
		"sealed copy does not open": {"sealed_copy_unopened", "WARN", func(t *testing.T, d *daemon, chatID chat.ChatID) string {
			id, _ := seedServableFile(t, d, chatID, servableSent)
			meta, err := d.files.GetMeta(id)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(meta.SealedPath)
			if err != nil {
				t.Fatal(err)
			}
			raw[len(raw)-1] ^= 0xff
			if err := os.WriteFile(meta.SealedPath, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			return id
		}},
		"no fingerprint for the chat": {"no_fingerprint", "DEBUG", func(t *testing.T, d *daemon, _ chat.ChatID) string {
			dc, err := d.chats.CreateDirect("0000000000000000000000000000cafe")
			if err != nil {
				t.Fatal(err)
			}
			id, _ := seedServableFile(t, d, dc.ID, servableFromContact)
			return id
		}},
		"sealed copy of another size than the row": {"size_disagrees", "DEBUG", func(t *testing.T, d *daemon, chatID chat.ChatID) string {
			id, _ := seedServableFile(t, d, chatID, servableSent)
			if _, err := d.files.SealAtRest(chatID, id, bytes.Repeat([]byte{7}, 2999)); err != nil {
				t.Fatal(err)
			}
			return id
		}},
		"metadata names another chat": {"chat_disagrees", "DEBUG", func(t *testing.T, d *daemon, chatID chat.ChatID) string {
			id, _ := seedServableFile(t, d, chatID, servableSent)
			if err := d.files.UpdateMeta(id, func(m *files.Metadata) error { m.ChatID = "ffeeddccbbaa99887766554433221100"; return nil }); err != nil {
				t.Fatal(err)
			}
			return id
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			d, stub, chatID := newServingDaemon(t)
			buf := captureRailLog(t)
			id := tc.seed(t, d, chatID)

			requestFiles(t, d, "env-req-1", id)
			line := waitForLog(t, buf, "file request: ", "outcome="+tc.outcome, "msg_id="+id)
			if !strings.Contains(line, "level="+tc.level) {
				t.Fatalf("logged at the wrong level, want %s: %s", tc.level, line)
			}
			c := stub.calls()
			if len(c.stages) != 0 || len(c.sentTo) != 0 {
				t.Fatalf("stagings = %d, sends = %d; want none", len(c.stages), len(c.sentTo))
			}
			if strings.Contains(buf.String(), `msg="file request: served"`) {
				t.Fatal("a file that cannot be served logged served")
			}
		})
	}
}

func TestFileRequest_RowGoneBeforeTheRecordDropsTheBlob(t *testing.T) {
	d, stub, chatID := newServingDaemon(t)
	buf := captureRailLog(t)
	id, _ := seedServableFile(t, d, chatID, servableFromContact)
	var once sync.Once
	withMetaCheckpoint(t, func(site, msgID string) {
		if site != "file_request_staged" || msgID != id {
			return
		}
		once.Do(func() {
			if err := d.files.DeleteMeta(id); err != nil {
				t.Errorf("delete during the serve: %v", err)
			}
		})
	})

	requestFiles(t, d, "env-req-1", id)
	waitForLog(t, buf, `msg="file request: not served"`, "outcome=row_gone", "msg_id="+id)
	c := stub.calls()
	if len(c.stages) != 1 {
		t.Fatalf("stagings = %d, want 1", len(c.stages))
	}
	if len(c.drops) != 1 || c.drops[0] != c.stages[0].MsgID {
		t.Fatalf("drops = %v, want the staged blob %q", c.drops, c.stages[0].MsgID)
	}
	if len(c.sentTo) != 0 {
		t.Fatalf("an offer went out for a row that is gone")
	}
}

func TestFileRequest_UnsentOfferDropsItsBlob(t *testing.T) {
	d, stub, chatID := newServingDaemon(t)
	buf := captureRailLog(t)
	stub.mu.Lock()
	stub.failSendTo = railSiblingID
	stub.mu.Unlock()
	id, _ := seedServableFile(t, d, chatID, servableSent)

	requestFiles(t, d, "env-req-1", id)
	waitForLog(t, buf, `msg="file request: sibling offer not sent"`, "outcome=send_failed", "msg_id="+id)
	c := stub.calls()
	if len(c.stages) != 1 || len(c.drops) != 1 || c.drops[0] != c.stages[0].MsgID {
		t.Fatalf("stagings = %d, drops = %v; want the one staged blob dropped", len(c.stages), c.drops)
	}
	meta, err := d.files.GetMeta(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.SiblingStagingIDs) != 1 || meta.SiblingStagingIDs[0] != priorStagingID {
		t.Fatalf("SiblingStagingIDs = %v, want only the one held before", meta.SiblingStagingIDs)
	}
	if _, err := d.files.GetGapOffer(id, railSiblingID); !errors.Is(err, files.ErrGapOfferNotFound) {
		t.Fatalf("the offer of a dropped blob is still kept: %v", err)
	}
}

func TestOfferStagedFile_WipesTheKey(t *testing.T) {
	for _, failSend := range []bool{false, true} {
		t.Run(fmt.Sprintf("send fails %t", failSend), func(t *testing.T) {
			stub := startSendFileStub(t)
			d := newSendFileTestDaemon(t, stub)
			linkSibling(t, d, railSiblingID)
			if failSend {
				stub.failSendTo = railSiblingID
			}
			st := siblingStaging{
				stagingID: "5a1b2c3d4e5f60718293a4b5c6d7e8f9",
				token:     "tok-for-the-requester",
				key:       bytes.Repeat([]byte{0xa7}, chacha20poly1305.KeySize),
				nonce:     bytes.Repeat([]byte{0x5c}, chacha20poly1305.NonceSizeX),
				sha256Hex: "c0ffee",
				size:      5000,
				plainLen:  4000,
			}
			row := FileEventBody{Name: "report.pdf", Mime: "application/pdf"}
			err := offerStagedFile(context.Background(), d, railSiblingID, gapFileMsgID, railFingerprint, row, st)
			if (err != nil) != failSend {
				t.Fatalf("err = %v, want failure %t", err, failSend)
			}
			if !bytes.Equal(st.key, make([]byte, len(st.key))) || !bytes.Equal(st.nonce, make([]byte, len(st.nonce))) {
				t.Fatal("the staging's key or nonce was left unwiped")
			}
			if err != nil && strings.Contains(err.Error(), st.token) {
				t.Fatalf("the error carries the token: %v", err)
			}
			if !failSend {
				got, pErr := syncrail.ParseGapSiblingOffer(openGapEnvelope(t, d, stub.calls().payloads[0]))
				if pErr != nil {
					t.Fatal(pErr)
				}
				if !bytes.Equal(got.SiblingOffer.Key, bytes.Repeat([]byte{0xa7}, chacha20poly1305.KeySize)) {
					t.Fatal("the offer on the wire does not hold the key")
				}
			}
		})
	}
}

func TestFileServer_SerialAndOneStagingPerFile(t *testing.T) {
	var (
		inFlight, maxInFlight atomic.Int32
		mu                    sync.Mutex
		served                []string
	)
	entered := make(chan string, 16)
	release := make(chan struct{})
	s := newFileServer(func(_ context.Context, item fileServeItem) time.Time {
		n := inFlight.Add(1)
		for {
			m := maxInFlight.Load()
			if n <= m || maxInFlight.CompareAndSwap(m, n) {
				break
			}
		}
		entered <- item.msgID
		if item.msgID == "a" {
			<-release
		}
		mu.Lock()
		served = append(served, item.requester+"/"+item.msgID)
		mu.Unlock()
		inFlight.Add(-1)
		return time.Time{}
	}, noExpiry)
	var releaseOnce sync.Once
	open := func() { releaseOnce.Do(func() { close(release) }) }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.run(ctx) }()

	defer func() { open(); cancel(); <-done }()

	if got := s.enqueue("sib", []string{"a", "b"}); got.queued != 2 {
		t.Fatalf("first request: %+v", got)
	}
	select {
	case id := <-entered:
		if id != "a" {
			t.Fatalf("first served %q, want a", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a was never served")
	}

	if got := s.enqueue("sib", []string{"a", "b", "c"}); got.queued != 1 || got.pending != 2 {
		t.Fatalf("repeated request: %+v, want 1 queued and 2 already pending", got)
	}
	open()
	for _, want := range []string{"b", "c"} {
		select {
		case id := <-entered:
			if id != want {
				t.Fatalf("served %q, want %q", id, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%q was never served", want)
		}
	}
	waitFor(t, "the queue to drain", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(served) == 3
	})
	if m := maxInFlight.Load(); m != 1 {
		t.Fatalf("%d files were served at once, want 1", m)
	}
	mu.Lock()
	got := strings.Join(served, " ")
	mu.Unlock()
	if got != "sib/a sib/b sib/c" {
		t.Fatalf("served %s", got)
	}

	waitFor(t, "a to leave the pending set", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.pending) == 0
	})
	if got := s.enqueue("sib", []string{"a"}); got.queued != 1 {
		t.Fatalf("a request after the serve: %+v, want it queued", got)
	}
}

func TestFileServer_QueueIsBounded(t *testing.T) {
	s := newFileServer(func(context.Context, fileServeItem) time.Time { return time.Time{} }, noExpiry)
	var ids []string
	for i := 0; i < maxQueuedFileServes+5; i++ {
		ids = append(ids, fmt.Sprintf("%032x", i))
	}
	got := fileServeEnqueued{}
	for i := 0; i < len(ids); i += syncrail.MaxFileRequestIDs {
		end := min(i+syncrail.MaxFileRequestIDs, len(ids))
		r := s.enqueue("sib", ids[i:end])
		got.queued += r.queued
		got.dropped += r.dropped
	}
	if got.queued != maxQueuedFileServes || got.dropped != 5 {
		t.Fatalf("%+v, want %d queued and 5 dropped", got, maxQueuedFileServes)
	}
}

func TestFileServer_StopsBetweenFilesOnShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var served atomic.Int32
	s := newFileServer(func(context.Context, fileServeItem) time.Time {
		served.Add(1)
		cancel()
		return time.Time{}
	}, noExpiry)
	s.enqueue("sib", []string{"a", "b", "c"})
	done := make(chan struct{})
	go func() { defer close(done); s.run(ctx) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker did not stop")
	}
	if n := served.Load(); n != 1 {
		t.Fatalf("served %d files after shutdown began, want 1", n)
	}
}

func TestFileRequest_RepeatWhileServingStagesOnce(t *testing.T) {
	d, stub, chatID := newServingDaemon(t)
	buf := captureRailLog(t)
	id, _ := seedServableFile(t, d, chatID, servableSent)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	withMetaCheckpoint(t, func(site, msgID string) {
		if site != "file_request_staged" || msgID != id {
			return
		}
		once.Do(func() {
			close(entered)
			<-release
		})
	})

	var releaseOnce sync.Once
	open := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(open)

	requestFiles(t, d, "env-req-1", id)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the serve never reached the staging record")
	}
	requestFiles(t, d, "env-req-1", id)
	waitForLog(t, buf, `msg="file request: received"`, "queued=0", "already_pending=1")
	open()
	waitForLog(t, buf, `msg="file request: served"`, "msg_id="+id)
	time.Sleep(200 * time.Millisecond)
	if c := stub.calls(); len(c.stages) != 1 || len(c.sentTo) != 1 {
		t.Fatalf("stagings = %d, sends = %d; want one of each", len(c.stages), len(c.sentTo))
	}
}

func TestFileRequest_UnconfirmedSendKeepsItsBlob(t *testing.T) {
	d, stub, chatID := newServingDaemon(t)
	buf := captureRailLog(t)
	stub.mu.Lock()
	stub.garbleSendTo = railSiblingID
	stub.mu.Unlock()
	id, _ := seedServableFile(t, d, chatID, servableSent)

	requestFiles(t, d, "env-req-1", id)
	waitForLog(t, buf, `msg="file request: sibling offer send unconfirmed; its blob stays with the row"`,
		"outcome=send_unconfirmed", "msg_id="+id)
	c := stub.calls()
	if len(c.stages) != 1 || len(c.sentTo) != 1 {
		t.Fatalf("stagings = %d, sends = %d; want one of each", len(c.stages), len(c.sentTo))
	}
	if len(c.drops) != 0 {
		t.Fatalf("drops = %v; a blob the requester may fetch was dropped", c.drops)
	}
	meta, err := d.files.GetMeta(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.SiblingStagingIDs) != 2 || meta.SiblingStagingIDs[1] != c.stages[0].MsgID {
		t.Fatalf("SiblingStagingIDs = %v; the kept blob must stay recorded so it goes with the row", meta.SiblingStagingIDs)
	}

	stub.mu.Lock()
	stub.garbleSendTo = ""
	stub.mu.Unlock()
	requestFiles(t, d, "env-req-2", id)
	waitForLog(t, buf, `msg="file request: served"`, "outcome=resent", "msg_id="+id)
	if c := stub.calls(); len(c.stages) != 1 || len(c.sentTo) != 2 {
		t.Fatalf("stagings = %d, sends = %d; want the one blob offered twice", len(c.stages), len(c.sentTo))
	}
}

func TestGapOfferNotQueued(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"refused before send": {fmt.Errorf("%w: refused before send: x", errGapOfferNotQueued), true},
		"haomad answered 409": {fmt.Errorf("gapfill: send: %w", &backendapi.StatusError{Path: "/send", StatusCode: 409, Msg: "no"}), true},
		"cut off at shutdown": {fmt.Errorf("gapfill: send: %w", fmt.Errorf("backendapi: send post: %w", context.Canceled)), false},
		"202 did not decode":  {fmt.Errorf("gapfill: send: %w", fmt.Errorf("backendapi: decode send: %w", io.ErrUnexpectedEOF)), false},
	}
	for name, tc := range cases {
		if got := gapOfferNotQueued(tc.err); got != tc.want {
			t.Errorf("%s: got %t, want %t", name, got, tc.want)
		}
	}
}

func TestFileRequest_NotServedToADeviceNoLongerLinked(t *testing.T) {
	d, stub, chatID := newServingDaemon(t)
	buf := captureRailLog(t)
	id, _ := seedServableFile(t, d, chatID, servableSent)
	const gone = "0000000000000000000000000000dead"

	d.fileServer.enqueue(gone, []string{id})
	waitForLog(t, buf, `msg="file request: not served"`, "outcome=not_a_sibling", "msg_id="+id, "sibling_peer_id="+gone)
	if c := stub.calls(); len(c.stages) != 0 || len(c.sentTo) != 0 {
		t.Fatalf("stagings = %d, sends = %d; want none", len(c.stages), len(c.sentTo))
	}
}

func TestFileRequest_ShutdownIsNotAFault(t *testing.T) {
	d, stub, chatID := newServingDaemon(t)
	buf := captureRailLog(t)
	id, _ := seedServableFile(t, d, chatID, servableSent)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	serveFileRequestItem(ctx, d, railSiblingID, id)
	if !strings.Contains(buf.String(), "outcome=shutdown") || strings.Contains(buf.String(), "level=WARN") {
		t.Fatalf("want the shutdown outcome and no Warn; the log:\n%s", buf.String())
	}
	if c := stub.calls(); len(c.stages) != 0 {
		t.Fatalf("staged %d blobs after shutdown began", len(c.stages))
	}
}

func TestUnrecordStagedFile_AtShutdownLeavesItToTheRow(t *testing.T) {
	d, stub, chatID := newServingDaemon(t)
	id, _ := seedServableFile(t, d, chatID, servableSent)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	unrecordStagedFile(ctx, d, slog.Default(), id, railSiblingID, priorStagingID)
	if c := stub.calls(); len(c.drops) != 0 {
		t.Fatalf("drops = %v after shutdown began", c.drops)
	}
	meta, err := d.files.GetMeta(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.SiblingStagingIDs) != 1 || meta.SiblingStagingIDs[0] != priorStagingID {
		t.Fatalf("SiblingStagingIDs = %v; the id must stay for the row to drop", meta.SiblingStagingIDs)
	}
}

func offerSent(t *testing.T, d *daemon, c stubCalls, i int) syncrail.GapSiblingOfferBody {
	t.Helper()
	got, err := syncrail.ParseGapSiblingOffer(openGapEnvelope(t, d, c.payloads[i]))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func backdateKept(t *testing.T, d *daemon, msgID string, age time.Duration) files.GapOffer {
	t.Helper()
	kept, err := d.files.GetGapOffer(msgID, railSiblingID)
	if err != nil {
		t.Fatal(err)
	}
	kept.StagedAt = time.Now().Add(-age).Unix()
	if err := d.files.RecordGapOffer(kept); err != nil {
		t.Fatal(err)
	}
	return kept
}

func TestFileRequest_RepeatWithinTheHourResendsTheKeptOffer(t *testing.T) {
	d, stub, chatID := newServingDaemon(t)
	buf := captureRailLog(t)
	id, plain := seedServableFile(t, d, chatID, servableFromContact)

	requestFiles(t, d, "env-req-1", id)
	waitForLog(t, buf, `msg="file request: served"`, "outcome=served", "msg_id="+id)
	requestFiles(t, d, "env-req-2", id)
	waitForLog(t, buf, `msg="file request: served"`, "outcome=resent", "msg_id="+id)

	c := stub.calls()
	if len(c.stages) != 1 || len(c.sentTo) != 2 {
		t.Fatalf("stagings = %d, sends = %d; want one staging offered twice", len(c.stages), len(c.sentTo))
	}
	first, second := offerSent(t, d, c, 0), offerSent(t, d, c, 1)
	if first.SiblingOffer.StagingID != second.SiblingOffer.StagingID ||
		first.SiblingOffer.Token != second.SiblingOffer.Token ||
		!bytes.Equal(first.SiblingOffer.Key, second.SiblingOffer.Key) ||
		!bytes.Equal(first.SiblingOffer.Nonce, second.SiblingOffer.Nonce) {
		t.Fatal("the offer sent again is not the kept one")
	}
	if _, reason := gapSiblingOfferRefusal(second); reason != "" {
		t.Fatalf("the requester would refuse the offer sent again: %s", reason)
	}
	kept, err := d.files.GetGapOffer(id, railSiblingID)
	if err != nil {
		t.Fatalf("no offer kept: %v", err)
	}
	defer kept.Wipe()
	if kept.StagingID != c.stages[0].MsgID {
		t.Fatalf("kept staging id %q, staged %q", kept.StagingID, c.stages[0].MsgID)
	}
	log := buf.String()
	so := second.SiblingOffer
	for _, sec := range []string{so.Token, hex.EncodeToString(so.Key), base64.StdEncoding.EncodeToString(so.Key),
		base64.StdEncoding.EncodeToString(so.Nonce), hex.EncodeToString(so.Nonce), string(plain), base64.StdEncoding.EncodeToString(plain)} {
		if strings.Contains(log, sec) {
			t.Fatalf("a secret reached the log: %q", sec)
		}
	}
}

func TestFileRequest_RepeatAfterTheHourStagesAgain(t *testing.T) {
	d, stub, chatID := newServingDaemon(t)
	buf := captureRailLog(t)
	id, _ := seedServableFile(t, d, chatID, servableSent)

	requestFiles(t, d, "env-req-1", id)
	waitForLog(t, buf, `msg="file request: served"`, "outcome=served", "msg_id="+id)
	old := backdateKept(t, d, id, defaultGapOfferLifetime+time.Minute)
	old.Wipe()

	requestFiles(t, d, "env-req-2", id)
	waitFor(t, "a second staging", func() bool { return len(stub.calls().stages) == 2 })
	waitFor(t, "the second offer", func() bool { return len(stub.calls().sentTo) == 2 })
	c := stub.calls()
	if len(c.drops) != 1 || c.drops[0] != old.StagingID {
		t.Fatalf("drops = %v, want the ended blob %q", c.drops, old.StagingID)
	}
	fresh := c.stages[1].MsgID
	if fresh == old.StagingID {
		t.Fatal("staged again under the old staging id")
	}
	if got := offerSent(t, d, c, 1); got.SiblingOffer.StagingID != fresh {
		t.Fatal("the new offer does not name the new blob")
	}
	kept, err := d.files.GetGapOffer(id, railSiblingID)
	if err != nil || kept.StagingID != fresh {
		t.Fatalf("kept offer = %v, %v; want the new staging", kept, err)
	}
	kept.Wipe()
	meta, _ := d.files.GetMeta(id)
	if strings.Contains(strings.Join(meta.SiblingStagingIDs, " "), old.StagingID) || !strings.Contains(strings.Join(meta.SiblingStagingIDs, " "), fresh) {
		t.Fatalf("SiblingStagingIDs = %v; want the new id and not the ended one", meta.SiblingStagingIDs)
	}
}

func TestKeptOffer_BackendReadyDropsAnEndedHour(t *testing.T) {
	d, stub, chatID := newServingDaemon(t)
	buf := captureRailLog(t)
	id, _ := seedServableFile(t, d, chatID, servableSent)
	requestFiles(t, d, "env-req-1", id)
	waitForLog(t, buf, `msg="file request: served"`, "msg_id="+id)
	old := backdateKept(t, d, id, defaultGapOfferLifetime+time.Minute)
	old.Wipe()

	checkKeptGapOffers(d)
	waitForLog(t, buf, `msg="file request: kept offer dropped after its hour"`, "outcome=expired", "staging_id="+old.StagingID)
	assertKeptOfferGone(t, d, stub, id, old.StagingID)
}

func TestKeptOffer_TimerDropsAtTheEndOfTheHour(t *testing.T) {
	d, stub, chatID := newServingDaemon(t)
	buf := captureRailLog(t)
	id, _ := seedServableFile(t, d, chatID, servableSent)
	requestFiles(t, d, "env-req-1", id)
	waitForLog(t, buf, `msg="file request: served"`, "msg_id="+id)

	old := backdateKept(t, d, id, defaultGapOfferLifetime-2*time.Second)
	old.Wipe()
	checkKeptGapOffers(d)
	waitForLog(t, buf, `msg="file request: kept offers checked"`, "kept=1", "dropped=0")
	if c := stub.calls(); len(c.drops) != 0 {
		t.Fatalf("dropped %v before the hour ended", c.drops)
	}
	waitForLog(t, buf, `msg="file request: kept offer dropped after its hour"`, "staging_id="+old.StagingID)
	assertKeptOfferGone(t, d, stub, id, old.StagingID)
}

func assertKeptOfferGone(t *testing.T, d *daemon, stub *sendFileStub, msgID, stagingID string) {
	t.Helper()
	if c := stub.calls(); len(c.drops) != 1 || c.drops[0] != stagingID {
		t.Fatalf("drops = %v, want [%s]", c.drops, stagingID)
	}
	if _, err := d.files.GetGapOffer(msgID, railSiblingID); !errors.Is(err, files.ErrGapOfferNotFound) {
		t.Fatalf("the kept offer and its key outlived the hour: %v", err)
	}
	meta, err := d.files.GetMeta(msgID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(meta.SiblingStagingIDs, " "), stagingID) {
		t.Fatalf("SiblingStagingIDs = %v still names the dropped blob", meta.SiblingStagingIDs)
	}
}

func TestFileServer_TimerRunsTheExpiryPass(t *testing.T) {
	var passes atomic.Int32
	s := newFileServer(func(context.Context, fileServeItem) time.Time { return time.Time{} },
		func(context.Context, time.Time) time.Time {
			if passes.Add(1) == 1 {
				return time.Now().Add(50 * time.Millisecond)
			}
			return time.Time{}
		})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.run(ctx) }()
	defer func() { cancel(); <-done }()
	s.askExpiry()
	waitFor(t, "the timer's pass", func() bool { return passes.Load() == 2 })
	time.Sleep(150 * time.Millisecond)
	if n := passes.Load(); n != 2 {
		t.Fatalf("%d passes; a timer with nothing kept must stay stopped", n)
	}
}

func TestResendKeptOffer_WipesTheKeptKey(t *testing.T) {
	for _, failSend := range []bool{false, true} {
		t.Run(fmt.Sprintf("send fails %t", failSend), func(t *testing.T) {
			stub := startSendFileStub(t)
			d := newSendFileTestDaemon(t, stub)
			linkSibling(t, d, railSiblingID)
			if failSend {
				stub.failSendTo = railSiblingID
			}
			kept := files.GapOffer{
				MsgID: gapFileMsgID, RequesterPeerID: railSiblingID, StagingID: "5a1b2c3d4e5f60718293a4b5c6d7e8f9",
				Token: "tok-kept", UrlPath: "/files/tok-kept",
				Key:      bytes.Repeat([]byte{0xa7}, chacha20poly1305.KeySize),
				Nonce:    bytes.Repeat([]byte{0x5c}, chacha20poly1305.NonceSizeX),
				BlobSize: 5000, PlaintextSize: 4000, Sha256Ciphertext: "c0ffee", StagedAt: time.Now().Unix(),
			}
			row := FileEventBody{Name: "report.pdf", Mime: "application/pdf", Size: 4000 + chacha20poly1305.Overhead}
			logger := slog.Default()
			fault := func(string, string, ...any) {}
			resendKeptOffer(context.Background(), d, logger, fault, railSiblingID, gapFileMsgID, railFingerprint, row, kept)
			if !bytes.Equal(kept.Key, make([]byte, len(kept.Key))) || !bytes.Equal(kept.Nonce, make([]byte, len(kept.Nonce))) {
				t.Fatal("the kept key or nonce was left unwiped")
			}
		})
	}
}

func TestKeptOffer_EndedOfferStaysUntilItsBlobIsDropped(t *testing.T) {
	prev := gapOfferDropRetry
	gapOfferDropRetry = 200 * time.Millisecond
	t.Cleanup(func() { gapOfferDropRetry = prev })
	d, stub, chatID := newServingDaemon(t)
	buf := captureRailLog(t)
	id, _ := seedServableFile(t, d, chatID, servableSent)
	requestFiles(t, d, "env-req-1", id)
	waitForLog(t, buf, `msg="file request: served"`, "msg_id="+id)
	old := backdateKept(t, d, id, defaultGapOfferLifetime+time.Minute)
	old.Wipe()
	stub.mu.Lock()
	stub.dropStatus = http.StatusInternalServerError
	stub.mu.Unlock()

	checkKeptGapOffers(d)
	waitForLog(t, buf, `msg="file request: kept offer's blob not dropped after its hour; tried again"`, "staging_id="+old.StagingID)
	kept, err := d.files.GetGapOffer(id, railSiblingID)
	if err != nil {
		t.Fatalf("the kept offer went before its blob: %v", err)
	}
	kept.Wipe()
	meta, _ := d.files.GetMeta(id)
	if !strings.Contains(strings.Join(meta.SiblingStagingIDs, " "), old.StagingID) {
		t.Fatalf("SiblingStagingIDs = %v; the id of a blob still hosted must stay", meta.SiblingStagingIDs)
	}

	stub.mu.Lock()
	stub.dropStatus = 0
	stub.mu.Unlock()
	waitForLog(t, buf, `msg="file request: kept offer dropped after its hour"`, "staging_id="+old.StagingID)
	if _, err := d.files.GetGapOffer(id, railSiblingID); !errors.Is(err, files.ErrGapOfferNotFound) {
		t.Fatalf("the kept offer outlived a drop that worked: %v", err)
	}
	meta, _ = d.files.GetMeta(id)
	if strings.Contains(strings.Join(meta.SiblingStagingIDs, " "), old.StagingID) {
		t.Fatalf("SiblingStagingIDs = %v still names the dropped blob", meta.SiblingStagingIDs)
	}
}

func TestKeptOffer_StagedInTheFutureHasEnded(t *testing.T) {
	d, stub, chatID := newServingDaemon(t)
	buf := captureRailLog(t)
	id, _ := seedServableFile(t, d, chatID, servableSent)
	requestFiles(t, d, "env-req-1", id)
	waitForLog(t, buf, `msg="file request: served"`, "msg_id="+id)
	old := backdateKept(t, d, id, -365*24*time.Hour)
	old.Wipe()

	requestFiles(t, d, "env-req-2", id)
	waitFor(t, "a second staging", func() bool { return len(stub.calls().stages) == 2 })
	waitFor(t, "the second offer", func() bool { return len(stub.calls().sentTo) == 2 })
	if strings.Contains(buf.String(), "outcome=resent") {
		t.Fatal("an offer staged in the future was sent again")
	}
	c := stub.calls()
	if len(c.drops) != 1 || c.drops[0] != old.StagingID {
		t.Fatalf("drops = %v, want the old blob %q", c.drops, old.StagingID)
	}
}

func TestKeptOffer_PassDropsAnOfferStagedInTheFuture(t *testing.T) {
	d, stub, chatID := newServingDaemon(t)
	buf := captureRailLog(t)
	id, _ := seedServableFile(t, d, chatID, servableSent)
	requestFiles(t, d, "env-req-1", id)
	waitForLog(t, buf, `msg="file request: served"`, "msg_id="+id)
	old := backdateKept(t, d, id, -365*24*time.Hour)
	old.Wipe()

	checkKeptGapOffers(d)
	waitForLog(t, buf, `msg="file request: kept offer dropped after its hour"`, "staging_id="+old.StagingID)
	assertKeptOfferGone(t, d, stub, id, old.StagingID)
}

func TestGapOfferEnded(t *testing.T) {
	d := &daemon{}
	now := time.Unix(1_800_000_000, 0)
	for name, tc := range map[string]struct {
		staged time.Time
		want   bool
	}{
		"just staged":              {now, false},
		"inside the hour":          {now.Add(-59 * time.Minute), false},
		"the hour is over":         {now.Add(-defaultGapOfferLifetime), true},
		"a clock read a bit apart": {now.Add(gapOfferClockSlack), false},
		"staged past the slack":    {now.Add(gapOfferClockSlack + time.Second), true},
		"staged a year ahead":      {now.Add(365 * 24 * time.Hour), true},
	} {
		if got := gapOfferEnded(d, tc.staged.Unix(), now); got != tc.want {
			t.Errorf("%s: got %t, want %t", name, got, tc.want)
		}
	}
}

func TestKeptOffer_NothingDroppedWhileHaomadIsUnreachable(t *testing.T) {
	d, stub, chatID := newServingDaemon(t)
	buf := captureRailLog(t)
	id, _ := seedServableFile(t, d, chatID, servableSent)
	requestFiles(t, d, "env-req-1", id)
	waitForLog(t, buf, `msg="file request: served"`, "msg_id="+id)
	old := backdateKept(t, d, id, defaultGapOfferLifetime+time.Minute)
	old.Wipe()
	d.backendReachable.Store(false)

	checkKeptGapOffers(d)
	waitForLog(t, buf, `msg="file request: kept offers checked"`, "drop_failed=1")
	if c := stub.calls(); len(c.drops) != 0 {
		t.Fatalf("drops = %v while haomad was unreachable", c.drops)
	}
	kept, err := d.files.GetGapOffer(id, railSiblingID)
	if err != nil {
		t.Fatalf("the kept offer went while haomad was unreachable: %v", err)
	}
	kept.Wipe()
}

func TestKeptOffer_LifetimeOverrideShortensTheHour(t *testing.T) {
	d, stub, chatID := newServingDaemon(t)
	d.gapOfferTTL = 2 * time.Second
	buf := captureRailLog(t)
	id, _ := seedServableFile(t, d, chatID, servableSent)
	requestFiles(t, d, "env-req-1", id)
	waitForLog(t, buf, `msg="file request: served"`, "msg_id="+id)
	staged := stub.calls().stages[0].MsgID
	waitForLog(t, buf, `msg="file request: kept offer dropped after its hour"`, "staging_id="+staged)
	assertKeptOfferGone(t, d, stub, id, staged)
}

func TestGapOfferLifetimeFromEnv(t *testing.T) {
	for raw, want := range map[string]time.Duration{
		"":     defaultGapOfferLifetime,
		"90s":  90 * time.Second,
		"59m":  59 * time.Minute,
		"2h":   defaultGapOfferLifetime,
		"soon": defaultGapOfferLifetime,
		"0s":   defaultGapOfferLifetime,
		"-5m":  defaultGapOfferLifetime,
	} {
		t.Setenv(gapOfferLifetimeEnv, raw)
		if got := gapOfferLifetimeFromEnv(); got != want {
			t.Errorf("%s=%q: got %v, want %v", gapOfferLifetimeEnv, raw, got, want)
		}
	}
}
