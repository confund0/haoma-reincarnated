package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/files"
	"haoma-frontend/internal/msg"
)

const (
	peerR = "0000000000000000000000000000a0a0"
	peerS = "0000000000000000000000000000b0b0"
)

type keyFirstPair struct {
	t        *testing.T
	sender   *daemon
	receiver *daemon
	sstub    *sendFileStub
	rstub    *filesStub
	rChat    chat.ChatID

	sDelivered int
	rDelivered int
	seq        int
}

func newKeyFirstPair(t *testing.T) *keyFirstPair {
	t.Helper()
	p := &keyFirstPair{t: t, sstub: startSendFileStub(t), rstub: startFilesStub(t)}
	p.sender = newSendFileTestDaemon(t, p.sstub)
	p.receiver = newFilesTestDaemon(t, p.rstub)

	processRemoteBundle(t, p.sender, peerR, p.receiver.signalState)
	if _, err := p.sender.chats.CreateDirect(peerR); err != nil {
		t.Fatal(err)
	}
	dc, _, err := p.receiver.createDirectWithDefaults(peerS)
	if err != nil {
		t.Fatal(err)
	}
	p.rChat = dc.ID
	return p
}

func (p *keyFirstPair) send(plaintext []byte) string {
	p.t.Helper()
	dc, err := p.sender.chats.GetByDirectPeer(peerR)
	if err != nil {
		p.t.Fatal(err)
	}
	res, code, err := runSendFile(context.Background(), p.sender, dc, peerR, writeTempFile(p.t, plaintext), "", "")
	if err != nil {
		p.t.Fatalf("runSendFile: code=%q err=%v", code, err)
	}
	return res.MsgID
}

func (p *keyFirstPair) senderSends() [][]byte {
	p.sstub.mu.Lock()
	defer p.sstub.mu.Unlock()
	var out [][]byte
	for i, to := range p.sstub.sentPeerIDs {
		if to == peerR && p.sstub.sentKinds[i] != backendapi.WireKindPresence {
			out = append(out, p.sstub.sentPayloads[i])
		}
	}
	return out
}

func (p *keyFirstPair) receiverSends() [][]byte {
	p.rstub.mu.Lock()
	defer p.rstub.mu.Unlock()
	var out [][]byte
	for i, to := range p.rstub.sentPeerIDs {
		if to == peerS && p.rstub.sentKinds[i] != backendapi.WireKindPresence {
			out = append(out, p.rstub.sentPayloads[i])
		}
	}
	return out
}

func (p *keyFirstPair) toReceiver() {
	p.t.Helper()
	sends := p.senderSends()
	if p.sDelivered >= len(sends) {
		p.t.Fatalf("nothing from the sender to deliver (delivered %d of %d)", p.sDelivered, len(sends))
	}
	p.deliver(p.receiver, peerS, sends[p.sDelivered])
	p.sDelivered++
}

func (p *keyFirstPair) toSender() {
	p.t.Helper()
	sends := p.receiverSends()
	if p.rDelivered >= len(sends) {
		p.t.Fatalf("nothing from the receiver to deliver (delivered %d of %d)", p.rDelivered, len(sends))
	}
	p.deliver(p.sender, peerR, sends[p.rDelivered])
	p.rDelivered++
}

func (p *keyFirstPair) deliver(to *daemon, from string, payload []byte) {
	p.seq++
	processInboxEntry(context.Background(), to, backendapi.InboxEntry{
		ArrivalAt: time.Now().UnixNano(),
		PeerID:    from,
		Envelope: backendapi.RawEnvelope{
			ID:        "env-crossed-" + strings.Repeat("0", 3) + string(rune('a'+p.seq)),
			Timestamp: time.Now().Unix(),
			From:      "peer-onion",
			Payload:   payload,
			Mac:       []byte{1, 2, 3, 4},
		},
	})
}

func (p *keyFirstPair) downloadDone(msgID string) {
	p.t.Helper()
	p.sstub.mu.Lock()
	blob := p.sstub.stages[0].Ciphertext
	p.sstub.mu.Unlock()
	meta, err := p.receiver.files.GetMeta(msgID)
	if err != nil {
		p.t.Fatalf("receiver has no metadata row: %v", err)
	}
	p.rstub.mu.Lock()
	p.rstub.stagingBody = blob
	p.rstub.mu.Unlock()
	fileFetchStateHandler(context.Background(), p.receiver)(backendapi.FileFetchEvent{
		MsgID: msgID, Token: meta.Token, State: string(files.StateReady),
		BytesReceived: int64(len(blob)), TotalBytes: int64(len(blob)),
	})
}

func (p *keyFirstPair) keysSent() int { return len(p.senderSends()) - 1 }

func (p *keyFirstPair) sealed(msgID string, want []byte) {
	p.t.Helper()
	got, err := p.receiver.files.UnsealAtRest(p.rChat, msgID)
	if err != nil {
		p.t.Fatalf("the receiver holds no sealed copy: %v", err)
	}
	if !bytes.Equal(got, want) {
		p.t.Fatal("the receiver sealed other bytes")
	}
}

func (p *keyFirstPair) receiverRowState(msgID string) string {
	p.t.Helper()
	_, body := fileRow(p.t, p.receiver, p.rChat, msgID)
	return body.State
}

func TestIngestFileOffer_ShipsReceiptOnArrival(t *testing.T) {
	p := newKeyFirstPair(t)
	plaintext := []byte("a contact's file, key first\n")
	msgID := p.send(plaintext)

	p.toReceiver()
	if n := len(p.receiverSends()); n != 1 {
		t.Fatalf("receiver sent %d message(s) when the offer arrived, want 1: the receipt", n)
	}
	p.rstub.mu.Lock()
	fetches := p.rstub.fetchCalls
	p.rstub.mu.Unlock()
	if fetches != 1 {
		t.Fatalf("fetch enqueued %d time(s) on arrival, want 1", fetches)
	}

	p.toSender()
	if p.keysSent() != 1 {
		t.Fatalf("the sender shipped %d key(s) for the receipt, want 1 — the receipt did not hold the right token", p.keysSent())
	}
	p.toReceiver()
	if st := p.receiverRowState(msgID); st != string(files.StateDownloading) {
		t.Fatalf("row after the key = %q, want downloading: the bytes are not here yet", st)
	}
	meta, _ := p.receiver.files.GetMeta(msgID)
	if len(meta.KeyBytes) == 0 {
		t.Fatal("the key was not held while the download runs")
	}

	before := len(p.receiverSends())
	p.downloadDone(msgID)
	p.sealed(msgID, plaintext)

	if after := len(p.receiverSends()); after != before+1 {
		t.Fatalf("receiver sent %d message(s) after the download, want 1 (the delivered answer, no second receipt)", after-before)
	}
	if p.keysSent() != 1 {
		t.Fatalf("keys shipped = %d, want 1", p.keysSent())
	}
}

func TestKeyAfterBytes_SecondAskGetsNoSecondKeyWhileTheFirstIsOnItsWay(t *testing.T) {
	p := newKeyFirstPair(t)
	plaintext := []byte("a contact's file, bytes first\n")
	msgID := p.send(plaintext)

	p.toReceiver()
	p.downloadDone(msgID)
	if n := len(p.receiverSends()); n != 2 {
		t.Fatalf("receiver sent %d message(s), want 2: the receipt at arrival and the one after the download", n)
	}
	if st := p.receiverRowState(msgID); st != string(files.StateAwaitingKey) {
		t.Fatalf("row after the download = %q, want awaiting_key", st)
	}

	p.toSender()
	p.toSender()
	if p.keysSent() != 1 {
		t.Fatalf("keys shipped = %d, want 1: the second ask came while the first key was on its way", p.keysSent())
	}
	p.toReceiver()
	p.sealed(msgID, plaintext)
}

func TestKeyAfterBytes_SecondAskGetsTheKeyAgainOnceTheFirstNoLongerWaits(t *testing.T) {
	for _, state := range []string{"sent", "failed", "gone", "error"} {
		t.Run(state, func(t *testing.T) {
			p := newKeyFirstPair(t)
			plaintext := []byte("a contact's file whose first key was lost\n")
			msgID := p.send(plaintext)

			p.toReceiver()
			p.downloadDone(msgID)
			p.toSender()
			meta, err := p.sender.files.GetMeta(msgID)
			if err != nil {
				t.Fatal(err)
			}
			first := meta.KeyEnvelopes[peerR]
			if first == "" {
				t.Fatal("the sender did not record the key's envelope for the device")
			}
			p.sstub.mu.Lock()
			p.sstub.outboxState = map[string]string{first: state}
			p.sstub.mu.Unlock()

			p.toSender()
			if p.keysSent() != 2 {
				t.Fatalf("keys shipped = %d, want 2: the first key message is %s", p.keysSent(), state)
			}
			p.sDelivered++
			p.toReceiver()
			p.sealed(msgID, plaintext)
		})
	}
}

func TestSiblingOffer_ShipsNoReceiptOnArrival(t *testing.T) {
	d, stub, _ := newSiblingReceiver(t)
	f := newSiblingFile(t)
	w := siblingOfferWrapper(t, f, "")
	processInboxEntry(context.Background(), d, railEntry(railSiblingID, "env-sib-no-receipt", sealSiblingOffer(t, d, w, offerFor(f))))
	stub.mu.Lock()
	sends, fetches := stub.sendCalls, stub.fetchCalls
	stub.mu.Unlock()
	if fetches != 1 {
		t.Fatalf("precondition: the sibling fetch was not enqueued (%d)", fetches)
	}
	if sends != 0 {
		t.Fatalf("a sibling offer shipped %d message(s) on arrival, want none", sends)
	}
}

func TestKeyFirst_NoTokenInAnyLogLine(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	p := newKeyFirstPair(t)

	p.sstub.mu.Lock()
	p.sstub.mintTokens = []string{"hK3vQ9zR7tW1xY5bN8mC2dF6gJ4kL0pS-uE_aI9oT3w"}
	p.sstub.mu.Unlock()
	plaintext := []byte("a contact's file, logged end to end\n")
	msgID := p.send(plaintext)
	p.toReceiver()
	p.downloadDone(msgID)
	p.toSender()
	meta, err := p.sender.files.GetMeta(msgID)
	if err != nil {
		t.Fatal(err)
	}
	p.sstub.mu.Lock()
	p.sstub.outboxState = map[string]string{meta.KeyEnvelopes[peerR]: "failed"}
	p.sstub.mu.Unlock()
	p.toSender()
	p.toReceiver()
	p.sealed(msgID, plaintext)

	token := meta.RecipientTokens[peerR]
	if len(token) < 16 {
		t.Fatalf("precondition: token %q too short to search for", token)
	}
	out := buf.String()
	for _, want := range []string{"file receipt: early receipt shipped", "file receipt: post-pull receipt shipped", "file receipt: the earlier key did not arrive", "file key: arrived after the bytes"} {
		if !strings.Contains(out, want) {
			t.Fatalf("precondition: the run never logged %q", want)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, token[:8]) {
			t.Errorf("the token reached a log line: %s", line)
		}
	}
}

func TestStateStamp_AKeyWrittenMeanwhileSurvives(t *testing.T) {
	p := newKeyFirstPair(t)
	plaintext := []byte("a key that lands mid-stamp\n")
	msgID := p.send(plaintext)
	p.toReceiver()
	p.toSender()

	fired := false
	prev := metaCheckpoint
	metaCheckpoint = func(site, id string) {
		if site != "stamp" || id != msgID || fired {
			return
		}
		fired = true
		p.toReceiver()
	}

	fileFetchStateHandler(context.Background(), p.receiver)(backendapi.FileFetchEvent{
		MsgID: msgID, State: string(files.StateFailedTransient), LastError: "dial failed",
	})
	metaCheckpoint = prev
	if !fired {
		t.Fatal("precondition: the stamp never reached its checkpoint")
	}

	meta, err := p.receiver.files.GetMeta(msgID)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.KeyBytes) == 0 || len(meta.Nonce) == 0 {
		t.Fatal("the stamp wrote back a row without the key that landed meanwhile")
	}
	if meta.State != files.StateFailedTransient {
		t.Fatalf("state = %q, want the stamp's failed_transient", meta.State)
	}
	p.downloadDone(msgID)
	p.sealed(msgID, plaintext)
}

func TestRail_MirroredFileReceiptChangesNothing(t *testing.T) {
	d, stub, chatID := newSiblingReceiver(t)
	const tok = "contact-token-held-here-9f8e7d6c5b4a"
	msgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if err := d.files.PutMeta(files.Metadata{
		MsgID: msgID, ChatID: chatID, Direction: files.DirOut, Token: tok,
		RecipientTokens: map[string]string{railPeerID: tok},
		KeyBytes:        bytes.Repeat([]byte{1}, 32), Nonce: bytes.Repeat([]byte{2}, 24),
		State: files.StateReady,
	}); err != nil {
		t.Fatal(err)
	}
	receiptID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	w, err := msg.BuildFileReceipt(4, time.Now().Unix(), receiptID, tok, 0)
	if err != nil {
		t.Fatal(err)
	}
	processInboxEntry(context.Background(), d, railEntry(railSiblingID, "env-old-receipt", sealMirror(t, d, railFingerprint, w)))

	stub.mu.Lock()
	sends := stub.sendCalls
	stub.mu.Unlock()
	if sends != 0 {
		t.Fatalf("a mirrored receipt made this device send %d message(s)", sends)
	}
	meta, err := d.files.GetMeta(msgID)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.KeyEnvelopes) != 0 {
		t.Fatalf("a mirrored receipt was read as a receipt: %v", meta.KeyEnvelopes)
	}
}

func TestKeyAlreadyOnItsWay_OnlyEnqueuedHoldsTheKeyBack(t *testing.T) {
	stub := startSendFileStub(t)
	d := newSendFileTestDaemon(t, stub)
	for state, want := range map[string]bool{
		"enqueued": true, "sent": false, "failed": false, "gone": false, "error": false,
	} {
		stub.mu.Lock()
		stub.outboxState = map[string]string{"env-x": state}
		stub.mu.Unlock()
		if got, why := keyAlreadyOnItsWay(context.Background(), d, "env-x"); got != want {
			t.Errorf("outbox %s: on its way = %t (%s), want %t", state, got, why, want)
		}
	}
	d.backendClient = nil
	if got, why := keyAlreadyOnItsWay(context.Background(), d, "env-x"); got || why != "no_backend" {
		t.Errorf("no haomad: on its way = %t (%s), want false (no_backend)", got, why)
	}
}

func withMetaCheckpoint(t *testing.T, fn func(site, msgID string)) {
	t.Helper()
	prev := metaCheckpoint
	metaCheckpoint = fn
	t.Cleanup(func() { metaCheckpoint = prev })
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestConverge_AKeyLandingBetweenReadAndWriteIsKept(t *testing.T) {
	p := newKeyFirstPair(t)
	plaintext := []byte("a key that lands mid-converge\n")
	msgID := p.send(plaintext)
	p.toReceiver()
	p.toSender()

	done := make(chan struct{})
	fired := false
	withMetaCheckpoint(t, func(site, id string) {
		if site != "converge_read" || id != msgID || fired {
			return
		}
		fired = true
		go func() {
			defer close(done)
			p.toReceiver()
		}()
		waitFor(t, "the key to be written", func() bool {
			m, err := p.receiver.files.GetMeta(msgID)
			return err == nil && len(m.KeyBytes) > 0
		})
	})
	p.downloadDone(msgID)
	if !fired {
		t.Fatal("precondition: converge never reached its checkpoint")
	}
	<-done
	p.sealed(msgID, plaintext)
	if st := p.receiverRowState(msgID); st != string(files.StateReady) {
		t.Fatalf("row = %q, want ready", st)
	}
}

func TestConverge_TwoAtOnceSealOnce(t *testing.T) {
	p := newKeyFirstPair(t)
	plaintext := []byte("two converges, one seal\n")
	msgID := p.send(plaintext)
	p.toReceiver()
	p.toSender()
	p.toReceiver()
	p.sstub.mu.Lock()
	blob := p.sstub.stages[0].Ciphertext
	p.sstub.mu.Unlock()
	if _, err := p.receiver.files.WriteStaging(msgID, blob); err != nil {
		t.Fatal(err)
	}

	var second string
	done := make(chan struct{})
	fired := false
	withMetaCheckpoint(t, func(site, id string) {
		if site != "converge_read" || id != msgID || fired {
			return
		}
		fired = true
		go func() {
			defer close(done)
			second = convergeFileReady(context.Background(), p.receiver, msgID)
		}()

		waitFor(t, "the second converge to finish or wait", func() bool {
			select {
			case <-done:
				return true
			default:
			}
			l := &p.receiver.fileSealLocks
			l.mu.Lock()
			defer l.mu.Unlock()
			return l.locks[msgID] != nil && l.locks[msgID].refs == 2
		})
	})
	first := convergeFileReady(context.Background(), p.receiver, msgID)
	<-done
	if first != "sealed" || second != "settled" {
		t.Fatalf("first converge %q, second %q; want sealed then settled", first, second)
	}
	p.sealed(msgID, plaintext)
}

func TestStateStamp_NeverMovesAReadyFileBack(t *testing.T) {
	p := newKeyFirstPair(t)
	plaintext := []byte("sealed, then a late failure\n")
	msgID := p.send(plaintext)
	p.toReceiver()
	p.toSender()
	p.toReceiver()
	p.downloadDone(msgID)
	p.sealed(msgID, plaintext)

	stampFileEventState(p.receiver, "", msgID, files.StateFailedTransient, 0, "late failure")
	if st := p.receiverRowState(msgID); st != string(files.StateReady) {
		t.Fatalf("row = %q after a late failure, want ready", st)
	}
	if m, _ := p.receiver.files.GetMeta(msgID); m.State != files.StateReady {
		t.Fatalf("metadata = %q after a late failure, want ready", m.State)
	}
}

func TestRecordKeyEnvelope_DoesNotBringBackADeletedFile(t *testing.T) {
	p := newKeyFirstPair(t)
	msgID := p.send([]byte("deleted while its key went out\n"))
	p.toReceiver()
	fired := false
	withMetaCheckpoint(t, func(site, id string) {
		if site != "record_key" || id != msgID || fired {
			return
		}
		fired = true
		if err := p.sender.files.DeleteMeta(msgID); err != nil {
			t.Error(err)
		}
	})
	p.toSender()
	if !fired {
		t.Fatal("precondition: the record never reached its checkpoint")
	}
	if _, err := p.sender.files.GetMeta(msgID); !errors.Is(err, files.ErrMetaNotFound) {
		t.Fatalf("the deleted file's metadata came back (err=%v)", err)
	}
}

func TestDropUnofferedSiblingStagings_KeepsAKeyEnvelopeWrittenMeanwhile(t *testing.T) {
	stub := startSendFileStub(t)
	d := newSendFileTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000aaaa"
	preEstablishSession(t, d, peerID)
	linkSibling(t, d, railSiblingID)
	dc, err := d.chats.GetByDirectPeer(peerID)
	if err != nil {
		t.Fatal(err)
	}
	fired := false
	withMetaCheckpoint(t, func(site, id string) {
		if site != "drop_unoffered" || fired {
			return
		}
		fired = true
		if err := d.files.UpdateMeta(id, func(m *files.Metadata) error {
			m.KeyEnvelopes = map[string]string{peerID: "env-key-1"}
			return nil
		}); err != nil {
			t.Error(err)
		}
	})
	res, code, err := runSendFile(context.Background(), d, dc, peerID, writeTempFile(t, []byte("orphan\n")), "", "")
	if err != nil {
		t.Fatalf("runSendFile: code=%q err=%v", code, err)
	}
	if !fired {
		t.Fatal("precondition: the drop never reached its checkpoint")
	}
	meta, err := d.files.GetMeta(res.MsgID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.KeyEnvelopes[peerID] != "env-key-1" {
		t.Fatalf("the key envelope written meanwhile was lost: %v", meta.KeyEnvelopes)
	}
	if len(meta.SiblingStagingIDs) != 0 {
		t.Fatalf("the dropped blob is still named: %v", meta.SiblingStagingIDs)
	}
}

func captureDebugLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestDownloadFinished_SaysWhatHappened(t *testing.T) {
	buf := captureDebugLog(t)
	p := newKeyFirstPair(t)
	msgID := p.send([]byte("a finished download\n"))
	p.toReceiver()
	p.toSender()
	p.toReceiver()
	p.downloadDone(msgID)
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, `msg="file download finished"`) && strings.Contains(line, "msg_id="+msgID) {
			if !strings.Contains(line, "post_pull_receipt=skipped_key_here") || !strings.Contains(line, "converge=sealed") {
				t.Fatalf("the line does not say what happened: %s", line)
			}
			return
		}
	}
	t.Fatalf("no \"file download finished\" line for %s:\n%s", msgID, buf.String())
}

func TestStagedFetchSweep_WritesTheReadyLine(t *testing.T) {
	buf := captureDebugLog(t)
	p := newKeyFirstPair(t)
	msgID := p.send([]byte("finished while nobody listened\n"))
	p.toReceiver()
	p.sstub.mu.Lock()
	blob := p.sstub.stages[0].Ciphertext
	p.sstub.mu.Unlock()
	p.rstub.mu.Lock()
	p.rstub.stagingBody = blob
	p.rstub.mu.Unlock()

	sweepStagedFetches(context.Background(), p.receiver)

	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, `msg="file fetch ready: blob pulled"`) && strings.Contains(line, "msg_id="+msgID) {
			return
		}
	}
	t.Fatalf("the sweep wrote no ready line for %s", msgID)
}
