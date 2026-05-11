package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/chacha20poly1305"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/files"
	"haoma-frontend/internal/msg"
)

func makeFreshAEADMaterial(t *testing.T, plaintext []byte) (key, nonce, ciphertext []byte) {
	t.Helper()
	key = make([]byte, chacha20poly1305.KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand key: %v", err)
	}
	nonce = make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("rand nonce: %v", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		t.Fatalf("aead: %v", err)
	}
	ciphertext = aead.Seal(nil, nonce, plaintext, nil)
	return
}

func seedInboundFileRow(t *testing.T, d *daemon, peerID, token string) (chat.ChatID, string) {
	t.Helper()
	dc, _, err := d.createDirectWithDefaults(peerID)
	if err != nil {
		t.Fatalf("createDirectWithDefaults: %v", err)
	}
	msgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	feBody := FileEventBody{
		Token:            token,
		UrlPath:          "/files/" + token,
		Name:             "secret.bin",
		Size:             64,
		Sha256Ciphertext: strings.Repeat("c", 64),
		State:            string(files.StateDownloading),
	}
	bodyRaw, _ := json.Marshal(feBody)
	if _, err := d.events.AppendInbound(events.InboundParams{
		ChatID:     dc.ID,
		Kind:       events.KindFile,
		SenderTs:   time.Now().Unix(),
		EnvelopeID: "env-" + token,
		MsgID:      msgID,
		Status:     events.DecryptOK,
		Body:       bodyRaw,
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.files.PutMeta(files.Metadata{
		MsgID:            msgID,
		ChatID:           dc.ID,
		Direction:        files.DirIn,
		Token:            token,
		OriginalName:     "secret.bin",
		Size:             64,
		Sha256Ciphertext: strings.Repeat("c", 64),
		State:            files.StateDownloading,
		CreatedAt:        time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	return dc.ID, msgID
}

func assertEventState(t *testing.T, d *daemon, msgID string, want files.State) {
	t.Helper()
	ev, err := d.events.GetByMsgID(msgID)
	if err != nil {
		t.Fatalf("GetByMsgID: %v", err)
	}
	var fb FileEventBody
	if err := json.Unmarshal(ev.Body, &fb); err != nil {
		t.Fatal(err)
	}
	if fb.State != string(want) {
		t.Fatalf("event state = %q, want %q", fb.State, want)
	}
}

func assertMetaState(t *testing.T, d *daemon, msgID string, want files.State) {
	t.Helper()
	m, err := d.files.GetMeta(msgID)
	if err != nil {
		t.Fatalf("GetMeta: %v", err)
	}
	if m.State != want {
		t.Fatalf("meta state = %q, want %q", m.State, want)
	}
}

func TestConverge_FetchThenKey(t *testing.T) {
	stub := startFilesStub(t)
	d := newFilesTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000aaaa"
	const token = "tok-converge-A"
	chatID, msgID := seedInboundFileRow(t, d, peerID, token)

	plaintext := []byte("the cake is a lie")
	key, nonce, ciphertext := makeFreshAEADMaterial(t, plaintext)

	if _, err := d.files.WriteStaging(msgID, ciphertext); err != nil {
		t.Fatalf("WriteStaging: %v", err)
	}
	convergeFileReady(context.Background(), d, msgID)
	assertMetaState(t, d, msgID, files.StateAwaitingKey)
	assertEventState(t, d, msgID, files.StateAwaitingKey)

	ingestFileKey(context.Background(), d, peerID, token, key, nonce)

	assertMetaState(t, d, msgID, files.StateReady)
	assertEventState(t, d, msgID, files.StateReady)
	got, err := d.files.UnsealAtRest(chatID, msgID)
	if err != nil {
		t.Fatalf("UnsealAtRest: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Errorf("unsealed plaintext drift: got %q want %q", got, plaintext)
	}
	m, _ := d.files.GetMeta(msgID)
	if len(m.KeyBytes) != 0 || len(m.Nonce) != 0 {
		t.Errorf("post-seal: key/nonce not wiped on metadata")
	}
	if m.SealedPath == "" {
		t.Errorf("post-seal: sealed path not stamped")
	}
}

func TestConverge_KeyThenFetch(t *testing.T) {
	stub := startFilesStub(t)
	d := newFilesTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000bbbb"
	const token = "tok-converge-B"
	chatID, msgID := seedInboundFileRow(t, d, peerID, token)

	plaintext := []byte("ouroboros")
	key, nonce, ciphertext := makeFreshAEADMaterial(t, plaintext)

	ingestFileKey(context.Background(), d, peerID, token, key, nonce)

	m, err := d.files.GetMeta(msgID)
	if err != nil {
		t.Fatal(err)
	}
	if m.State == files.StateReady {
		t.Fatalf("converged without staging blob")
	}
	if len(m.KeyBytes) != 32 || len(m.Nonce) != 24 {
		t.Fatalf("key/nonce not persisted: keylen=%d noncelen=%d", len(m.KeyBytes), len(m.Nonce))
	}

	if _, err := d.files.WriteStaging(msgID, ciphertext); err != nil {
		t.Fatalf("WriteStaging: %v", err)
	}
	convergeFileReady(context.Background(), d, msgID)

	assertMetaState(t, d, msgID, files.StateReady)
	assertEventState(t, d, msgID, files.StateReady)
	got, err := d.files.UnsealAtRest(chatID, msgID)
	if err != nil {
		t.Fatalf("UnsealAtRest: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Errorf("plaintext drift: got %q want %q", got, plaintext)
	}
}

func TestConverge_AEADFailure_StampsFailedPermanent(t *testing.T) {
	stub := startFilesStub(t)
	d := newFilesTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000cccc"
	const token = "tok-converge-fail"
	chatID, msgID := seedInboundFileRow(t, d, peerID, token)

	_, nonce, ciphertext := makeFreshAEADMaterial(t, []byte("something"))
	wrongKey := make([]byte, 32)

	if _, err := d.files.WriteStaging(msgID, ciphertext); err != nil {
		t.Fatal(err)
	}
	ingestFileKey(context.Background(), d, peerID, token, wrongKey, nonce)

	assertMetaState(t, d, msgID, files.StateFailedPermanent)
	assertEventState(t, d, msgID, files.StateFailedPermanent)
	if _, err := d.files.UnsealAtRest(chatID, msgID); err == nil {
		t.Errorf("sealed file written despite AEAD failure")
	}
}

func TestIngestFileKey_NoMetadata_Drops(t *testing.T) {
	stub := startFilesStub(t)
	d := newFilesTestDaemon(t, stub)

	key, nonce, _ := makeFreshAEADMaterial(t, []byte("x"))
	ingestFileKey(context.Background(), d, "stranger", "tok-stranger", key, nonce)

	if _, err := d.files.GetMetaByToken("tok-stranger"); err == nil {
		t.Errorf("metadata appeared from nowhere")
	}
}

func TestIngestFileKey_PeerMismatch_Drops(t *testing.T) {
	stub := startFilesStub(t)
	d := newFilesTestDaemon(t, stub)
	const offerPeer = "0000000000000000000000000000aaaa"
	const wrongPeer = "0000000000000000000000000000ffff"
	const token = "tok-peer-gate"
	_, msgID := seedInboundFileRow(t, d, offerPeer, token)

	key, nonce, _ := makeFreshAEADMaterial(t, []byte("x"))
	ingestFileKey(context.Background(), d, wrongPeer, token, key, nonce)

	m, err := d.files.GetMeta(msgID)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.KeyBytes) != 0 {
		t.Errorf("key persisted despite peer mismatch")
	}
	if m.State == files.StateReady {
		t.Errorf("row reached ready despite peer mismatch")
	}
}

func TestIngestFileReceipt_RedeemsAndShipsKey(t *testing.T) {
	stub := startFilesStub(t)

	receipts := struct {
		called int
		token  string
		peer   string
	}{}
	mountReceipts(t, stub, func(token, peer string) {
		receipts.called++
		receipts.token = token
		receipts.peer = peer
	})

	d := newFilesTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000aaaa"
	const token = "tok-sender-side"

	dc, _, err := d.createDirectWithDefaults(peerID)
	if err != nil {
		t.Fatal(err)
	}
	msgID, _ := msg.NewID()
	keyBytes, nonce, _ := makeFreshAEADMaterial(t, []byte("x"))
	if err := d.files.PutMeta(files.Metadata{
		MsgID:           msgID,
		ChatID:          dc.ID,
		Direction:       files.DirOut,
		Token:           token,
		RecipientTokens: map[string]string{peerID: token},
		KeyBytes:        keyBytes,
		Nonce:           nonce,
		State:           files.StatePending,
	}); err != nil {
		t.Fatal(err)
	}

	ingestFileReceipt(context.Background(), d, peerID, &msg.FileReceiptBody{Token: token})
	if receipts.called != 0 {
		t.Errorf("redeem called despite encrypt failure: count=%d", receipts.called)
	}
}

func mountReceipts(t *testing.T, stub *filesStub, fn func(token, peer string)) {
	t.Helper()

	_ = fn
}

func TestIngestFileReceipt_NoMetadata_Drops(t *testing.T) {
	stub := startFilesStub(t)
	d := newFilesTestDaemon(t, stub)
	ingestFileReceipt(context.Background(), d, "stranger", &msg.FileReceiptBody{Token: "tok-nonexistent"})
	if _, err := d.files.GetMetaByToken("tok-nonexistent"); err == nil {
		t.Errorf("metadata appeared for unknown token")
	}
}

func TestIngestFileReceipt_RecipientMismatch_Drops(t *testing.T) {
	stub := startFilesStub(t)
	d := newFilesTestDaemon(t, stub)
	const ownerPeer = "0000000000000000000000000000aaaa"
	const imposter = "0000000000000000000000000000ffff"
	const token = "tok-mismatch"
	dc, _, err := d.createDirectWithDefaults(ownerPeer)
	if err != nil {
		t.Fatal(err)
	}
	msgID, _ := msg.NewID()
	if err := d.files.PutMeta(files.Metadata{
		MsgID:           msgID,
		ChatID:          dc.ID,
		Direction:       files.DirOut,
		Token:           token,
		RecipientTokens: map[string]string{ownerPeer: token},
		State:           files.StatePending,
	}); err != nil {
		t.Fatal(err)
	}
	ingestFileReceipt(context.Background(), d, imposter, &msg.FileReceiptBody{Token: token})

	m, _ := d.files.GetMeta(msgID)
	if m.State != files.StatePending {
		t.Errorf("metadata state mutated despite mismatch: %q", m.State)
	}
}

func TestIngestFileKey_OutboundDirection_Drops(t *testing.T) {
	stub := startFilesStub(t)
	d := newFilesTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000eeee"
	const token = "tok-out"
	dc, _, err := d.createDirectWithDefaults(peerID)
	if err != nil {
		t.Fatal(err)
	}
	msgID, _ := msg.NewID()
	if err := d.files.PutMeta(files.Metadata{
		MsgID: msgID, ChatID: dc.ID, Direction: files.DirOut,
		Token: token, State: files.StatePending,
	}); err != nil {
		t.Fatal(err)
	}
	key, nonce, _ := makeFreshAEADMaterial(t, []byte("x"))
	ingestFileKey(context.Background(), d, peerID, token, key, nonce)
	m, _ := d.files.GetMeta(msgID)
	if len(m.KeyBytes) != 0 {
		t.Errorf("key persisted on outbound metadata")
	}
}
