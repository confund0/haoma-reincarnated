package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/files"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/peerstate"
	"haoma-frontend/internal/presence"
	"haoma-frontend/internal/session"
	"haoma-frontend/internal/signal"
	"haoma-frontend/internal/store"
)

type sendFileStub struct {
	URL string

	mu             sync.Mutex
	stageCalls     int
	lastStage      backendapi.StageFileRequest
	mintTokens     []string
	stageStatus    int
	dropCalls      int
	lastDropMsgID  string
	sendCalls      int
	lastSendReq    backendapi.SendRequest
	sendEnvelopeID string
}

func startSendFileStub(t *testing.T) *sendFileStub {
	t.Helper()
	stub := &sendFileStub{stageStatus: http.StatusCreated}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /files", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		stub.mu.Lock()
		stub.stageCalls++
		_ = json.Unmarshal(body, &stub.lastStage)
		status := stub.stageStatus
		if status == 0 {
			status = http.StatusCreated
		}
		tokens := stub.mintTokens
		if len(tokens) == 0 {
			tokens = make([]string, len(stub.lastStage.RecipientPeerIDs))
			for i := range tokens {
				tokens[i] = "tok-mint-" + hex.EncodeToString([]byte{byte(stub.stageCalls), byte(i)})
			}
		}
		msgID := stub.lastStage.MsgID
		stub.mu.Unlock()
		if status != http.StatusCreated {
			http.Error(w, "stub stage refused", status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(backendapi.StageFileResponse{
			MsgID:  msgID,
			Tokens: tokens,
		})
	})
	mux.HandleFunc("DELETE /files/{msg_id}", func(w http.ResponseWriter, r *http.Request) {
		stub.mu.Lock()
		stub.dropCalls++
		stub.lastDropMsgID = r.PathValue("msg_id")
		stub.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /send", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		stub.mu.Lock()
		stub.sendCalls++
		_ = json.Unmarshal(body, &stub.lastSendReq)
		envelopeID := stub.sendEnvelopeID
		if envelopeID == "" {
			envelopeID = "env-stub-send-" + hex.EncodeToString([]byte{byte(stub.sendCalls)})
		}
		stub.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(backendapi.SendResponse{EnvelopeID: envelopeID})
	})
	srv := httptest.NewServer(mux)
	stub.URL = srv.URL
	t.Cleanup(srv.Close)
	return stub
}

func newSendFileTestDaemon(t *testing.T, stub *sendFileStub) *daemon {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Lock() })

	sigState, _, err := signal.LoadOrBootstrap(st, 4)
	if err != nil {
		t.Fatal(err)
	}
	stores := signal.NewStores(st, sigState)
	bus := events.NewBus()
	mgr, err := files.NewManager(st, dir)
	if err != nil {
		t.Fatal(err)
	}
	d := &daemon{
		dataDir:       dir,
		store:         st,
		signalState:   sigState,
		stores:        stores,
		cipher:        session.New(stores),
		peerSeq:       peerstate.New(st),
		peerMeta:      peerstate.NewMeta(st),
		chats:         chat.NewStore(st),
		events:        events.New(st, bus, nil),
		eventBus:      bus,
		files:         mgr,
		presenceCache: presence.New(),
		backendClient: backendapi.New(stub.URL, "", nil),
		ipcSrv:        ipc.NewServer("test-token"),
	}
	d.settingsSnapshot.Store(defaultSettings())
	return d
}

func writeTempFile(t *testing.T, content []byte) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "payload.bin")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}

func TestRunSendFile_HappyPath(t *testing.T) {
	stub := startSendFileStub(t)
	d := newSendFileTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000aaaa"
	preEstablishSession(t, d, peerID)

	dc, err := d.chats.GetByDirectPeer(peerID)
	if err != nil {
		t.Fatalf("GetByDirectPeer: %v", err)
	}

	plaintext := []byte("the cake is most assuredly a lie\n")
	path := writeTempFile(t, plaintext)

	res, code, err := runSendFile(context.Background(), d, dc, peerID, path)
	if err != nil {
		t.Fatalf("runSendFile: code=%q err=%v", code, err)
	}
	if res.MsgID == "" || res.EnvelopeID == "" || res.SenderSeq == 0 {
		t.Fatalf("result missing fields: %+v", res)
	}
	if res.Name != filepath.Base(path) {
		t.Errorf("name = %q, want %q", res.Name, filepath.Base(path))
	}

	stub.mu.Lock()
	gotStage := stub.lastStage
	stageCalls := stub.stageCalls
	gotSend := stub.lastSendReq
	sendCalls := stub.sendCalls
	stub.mu.Unlock()

	wantWireSize := uint64(len(gotStage.Ciphertext))
	if res.Size != wantWireSize {
		t.Errorf("size = %d, want %d (matches staged ciphertext bytes)", res.Size, wantWireSize)
	}

	if stageCalls != 1 {
		t.Errorf("stage calls = %d, want 1", stageCalls)
	}
	if gotStage.MsgID != res.MsgID {
		t.Errorf("staged msg_id = %q, want %q", gotStage.MsgID, res.MsgID)
	}
	if len(gotStage.RecipientPeerIDs) != 1 || gotStage.RecipientPeerIDs[0] != peerID {
		t.Errorf("staged recipients = %v, want [%s]", gotStage.RecipientPeerIDs, peerID)
	}
	if len(gotStage.Ciphertext) <= len(plaintext) {
		t.Errorf("ciphertext (%d) not larger than plaintext (%d) — AEAD overhead missing?",
			len(gotStage.Ciphertext), len(plaintext))
	}

	if sendCalls != 1 {
		t.Errorf("send calls = %d, want 1", sendCalls)
	}
	if gotSend.PeerID != peerID {
		t.Errorf("send peer_id = %q, want %q", gotSend.PeerID, peerID)
	}
	if len(gotSend.Payload) == 0 {
		t.Error("send payload empty")
	}

	if bytes.Contains(gotSend.Payload, plaintext) {
		t.Error("send payload contains plaintext — libsignal encryption didn't run")
	}

	metaPrimary, err := d.files.GetMeta(res.MsgID)
	if err != nil {
		t.Fatalf("GetMeta: %v", err)
	}
	if metaPrimary.Direction != files.DirOut {
		t.Errorf("meta.Direction = %q, want out", metaPrimary.Direction)
	}
	if len(metaPrimary.KeyBytes) != 32 {
		t.Errorf("meta.KeyBytes len = %d, want 32", len(metaPrimary.KeyBytes))
	}
	if len(metaPrimary.Nonce) != 24 {
		t.Errorf("meta.Nonce len = %d, want 24", len(metaPrimary.Nonce))
	}
	if metaPrimary.RecipientTokens[peerID] == "" {
		t.Errorf("meta.RecipientTokens missing entry for %s", peerID)
	}
	if metaPrimary.RecipientTokens[peerID] != metaPrimary.Token {
		t.Errorf("primary token / recipient token diverge: %q vs %q",
			metaPrimary.Token, metaPrimary.RecipientTokens[peerID])
	}
	if metaPrimary.State != files.StateReady {
		t.Errorf("meta.State = %q, want ready", metaPrimary.State)
	}
	if metaPrimary.Size != wantWireSize {
		t.Errorf("meta.Size = %d, want %d (ciphertext bytes)", metaPrimary.Size, wantWireSize)
	}

	sum := sha256.Sum256(gotStage.Ciphertext)
	wantHex := hex.EncodeToString(sum[:])
	if metaPrimary.Sha256Ciphertext != wantHex {
		t.Errorf("meta.Sha256Ciphertext = %q, want %q", metaPrimary.Sha256Ciphertext, wantHex)
	}

	got, err := d.files.UnsealAtRest(dc.ID, res.MsgID)
	if err != nil {
		t.Fatalf("UnsealAtRest sender copy: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("unsealed sender plaintext drift")
	}

	rows, err := d.events.List(dc.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fileEv *events.Event
	for i := range rows {
		if rows[i].Kind == events.KindFile && rows[i].MsgID == res.MsgID {
			fileEv = &rows[i]
			break
		}
	}
	if fileEv == nil {
		t.Fatalf("no KindFile row found in chat %s (rows=%d)", dc.ID, len(rows))
	}
	if fileEv.Direction != events.DirOut {
		t.Errorf("event direction = %q, want out", fileEv.Direction)
	}
	if fileEv.EnvelopeID != res.EnvelopeID {
		t.Errorf("event envelope_id = %q, want %q", fileEv.EnvelopeID, res.EnvelopeID)
	}
	var feBody FileEventBody
	if err := json.Unmarshal(fileEv.Body, &feBody); err != nil {
		t.Fatalf("decode FileEventBody: %v", err)
	}
	if feBody.State != string(files.StateReady) {
		t.Errorf("event state = %q, want ready", feBody.State)
	}
	if feBody.Size != wantWireSize {
		t.Errorf("event size = %d, want %d (ciphertext bytes)", feBody.Size, wantWireSize)
	}
	if feBody.Sha256Ciphertext != wantHex {
		t.Errorf("event sha256 = %q, want %q", feBody.Sha256Ciphertext, wantHex)
	}
}

func TestRunSendFile_OversizedRefuses(t *testing.T) {
	stub := startSendFileStub(t)
	d := newSendFileTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000bbbb"
	preEstablishSession(t, d, peerID)
	dc, _ := d.chats.GetByDirectPeer(peerID)

	huge := make([]byte, files.MaxPlaintextBytes+1)
	path := writeTempFile(t, huge)

	_, code, err := runSendFile(context.Background(), d, dc, peerID, path)
	if err == nil {
		t.Fatal("expected too_large error, got nil")
	}
	if code != "too_large" {
		t.Errorf("code = %q, want too_large", code)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.stageCalls != 0 {
		t.Errorf("stage called %d times despite oversize refusal", stub.stageCalls)
	}
	if stub.sendCalls != 0 {
		t.Errorf("send called %d times despite oversize refusal", stub.sendCalls)
	}
}

func TestRunSendFile_MissingPath(t *testing.T) {
	stub := startSendFileStub(t)
	d := newSendFileTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000cccc"
	preEstablishSession(t, d, peerID)
	dc, _ := d.chats.GetByDirectPeer(peerID)

	_, code, err := runSendFile(context.Background(), d, dc, peerID, filepath.Join(t.TempDir(), "no-such-file"))
	if err == nil {
		t.Fatal("expected file_open error, got nil")
	}
	if code != "file_open" {
		t.Errorf("code = %q, want file_open", code)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.stageCalls != 0 || stub.sendCalls != 0 {
		t.Errorf("haomad touched on missing-path: stage=%d send=%d", stub.stageCalls, stub.sendCalls)
	}
}

func TestRunSendFile_DirectoryRefuses(t *testing.T) {
	stub := startSendFileStub(t)
	d := newSendFileTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000dddd"
	preEstablishSession(t, d, peerID)
	dc, _ := d.chats.GetByDirectPeer(peerID)

	dir := t.TempDir()
	_, code, err := runSendFile(context.Background(), d, dc, peerID, dir)
	if err == nil {
		t.Fatal("expected directory rejection, got nil")
	}
	if code != "file_open" {
		t.Errorf("code = %q, want file_open", code)
	}
}

func TestRunSendFile_NoSession_EncryptFailedDropsBlob(t *testing.T) {
	stub := startSendFileStub(t)
	d := newSendFileTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000eeee"

	dc, _, err := d.createDirectWithDefaults(peerID)
	if err != nil {
		t.Fatalf("createDirectWithDefaults: %v", err)
	}

	plaintext := []byte("never goes anywhere")
	path := writeTempFile(t, plaintext)

	_, code, err := runSendFile(context.Background(), d, dc, peerID, path)
	if err == nil {
		t.Fatal("expected encrypt_failed error, got nil")
	}
	if code != "encrypt_failed" {
		t.Errorf("code = %q, want encrypt_failed", code)
	}
	stub.mu.Lock()
	stage := stub.stageCalls
	drops := stub.dropCalls
	send := stub.sendCalls
	stub.mu.Unlock()
	if stage != 1 {
		t.Errorf("stage calls = %d, want 1 (stage runs before encrypt)", stage)
	}
	if drops != 1 {
		t.Errorf("drop calls = %d, want 1 (cleanup after encrypt failure)", drops)
	}
	if send != 0 {
		t.Errorf("send calls = %d, want 0 (encrypt failed before send)", send)
	}

	rows, _ := d.events.List(dc.ID, 0, 0)
	for _, ev := range rows {
		if ev.Kind == events.KindFile {
			t.Errorf("KindFile event row persisted despite encrypt failure: %+v", ev)
		}
	}
}

func TestRunSendFile_OfferShapeMatchesStaging(t *testing.T) {
	stub := startSendFileStub(t)
	stub.mu.Lock()
	stub.mintTokens = []string{"tok-shape-check"}
	stub.mu.Unlock()

	d := newSendFileTestDaemon(t, stub)
	const peerID = "00000000000000000000000000001111"
	preEstablishSession(t, d, peerID)
	dc, _ := d.chats.GetByDirectPeer(peerID)

	plaintext := []byte("shape check")
	path := writeTempFile(t, plaintext)

	res, code, err := runSendFile(context.Background(), d, dc, peerID, path)
	if err != nil {
		t.Fatalf("runSendFile: code=%q err=%v", code, err)
	}
	meta, err := d.files.GetMetaByToken("tok-shape-check")
	if err != nil {
		t.Fatalf("GetMetaByToken: %v", err)
	}
	if meta.MsgID != res.MsgID {
		t.Errorf("meta.MsgID = %q, want %q", meta.MsgID, res.MsgID)
	}
	if meta.RecipientTokens[peerID] != "tok-shape-check" {
		t.Errorf("recipient token = %q, want tok-shape-check", meta.RecipientTokens[peerID])
	}

	ev, err := d.events.GetByMsgID(res.MsgID)
	if err != nil {
		t.Fatalf("GetByMsgID: %v", err)
	}
	var feBody FileEventBody
	if err := json.Unmarshal(ev.Body, &feBody); err != nil {
		t.Fatal(err)
	}
	if feBody.Token != "tok-shape-check" {
		t.Errorf("event token = %q, want tok-shape-check", feBody.Token)
	}
	if feBody.UrlPath != "/files/tok-shape-check" {
		t.Errorf("event url_path = %q, want /files/tok-shape-check", feBody.UrlPath)
	}

	stub.mu.Lock()
	stage := stub.lastStage
	stub.mu.Unlock()
	sum := sha256.Sum256(stage.Ciphertext)
	if meta.Sha256Ciphertext != hex.EncodeToString(sum[:]) {
		t.Errorf("meta.Sha256Ciphertext drift")
	}

	if msg.KindFileOffer == "" {
		t.Fatal("msg.KindFileOffer constant missing — wire schema regression")
	}
}
