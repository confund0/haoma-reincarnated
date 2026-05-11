package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

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

type filesStub struct {
	URL string

	mu          sync.Mutex
	fetchCalls  int
	lastFetch   backendapi.FetchFileRequest
	stagingBody []byte
	dropCalls   int
	dropBody    []byte

	stagingStatus int

	sendCalls   int
	lastSendReq backendapi.SendRequest
}

func startFilesStub(t *testing.T) *filesStub {
	t.Helper()
	stub := &filesStub{stagingStatus: http.StatusOK}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /files/fetch", func(w http.ResponseWriter, r *http.Request) {
		stub.mu.Lock()
		defer stub.mu.Unlock()
		stub.fetchCalls++
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &stub.lastFetch)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"msg_id": stub.lastFetch.MsgID,
			"token":  stub.lastFetch.Token,
			"state":  "pending",
		})
	})
	mux.HandleFunc("GET /files/staging/{msg_id}", func(w http.ResponseWriter, r *http.Request) {
		stub.mu.Lock()
		body := stub.stagingBody
		status := stub.stagingStatus
		stub.mu.Unlock()
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
	mux.HandleFunc("DELETE /files/staging/{msg_id}", func(w http.ResponseWriter, r *http.Request) {
		stub.mu.Lock()
		defer stub.mu.Unlock()
		stub.dropCalls++
		stub.dropBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /send", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		stub.mu.Lock()
		stub.sendCalls++
		_ = json.Unmarshal(body, &stub.lastSendReq)
		stub.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(backendapi.SendResponse{EnvelopeID: "env-stub-receipt-001"})
	})
	srv := httptest.NewServer(mux)
	stub.URL = srv.URL
	t.Cleanup(srv.Close)
	return stub
}

func newFilesTestDaemon(t *testing.T, stub *filesStub) *daemon {
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

func TestIngestFileOffer_PersistsRowAndKicksFetch(t *testing.T) {
	stub := startFilesStub(t)
	d := newFilesTestDaemon(t, stub)

	const peerID = "0000000000000000000000000000aaaa"
	dc, _, err := d.createDirectWithDefaults(peerID)
	if err != nil {
		t.Fatalf("createDirectWithDefaults: %v", err)
	}
	chatID := dc.ID

	msgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	wrapper := &msg.Wrapper{
		V:     msg.Version,
		Seq:   1,
		Ts:    time.Now().Unix(),
		MsgID: msgID,
		Kind:  msg.KindFileOffer,
	}
	body := &msg.FileOfferBody{
		Token:            "tok-test-1",
		UrlPath:          "/files/tok-test-1",
		Name:             "secret.pdf",
		Size:             1024,
		Mime:             "application/pdf",
		Sha256Ciphertext: strings.Repeat("a", 64),
	}
	entry := backendapi.InboxEntry{
		ArrivalAt: time.Now().UnixNano(),
		PeerID:    peerID,
		Envelope: backendapi.RawEnvelope{
			ID:        "env-file-1",
			Timestamp: wrapper.Ts,
			From:      "alice-onion",
			Kind:      "text",
		},
	}

	ingestFileOffer(context.Background(), d, chatID, entry, wrapper, body)

	rows, err := d.events.List(chatID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	ev := rows[0]
	if ev.Kind != events.KindFile {
		t.Fatalf("kind = %q, want %q", ev.Kind, events.KindFile)
	}
	if ev.Direction != events.DirIn {
		t.Fatalf("direction = %q, want in", ev.Direction)
	}
	if ev.MsgID != msgID {
		t.Fatalf("msg_id = %q, want %q", ev.MsgID, msgID)
	}
	var fb FileEventBody
	if err := json.Unmarshal(ev.Body, &fb); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if fb.Name != "secret.pdf" || fb.Size != 1024 || fb.State != string(files.StateDownloading) {
		t.Fatalf("body = %+v, want name=secret.pdf size=1024 state=downloading", fb)
	}

	meta, err := d.files.GetMeta(msgID)
	if err != nil {
		t.Fatalf("GetMeta: %v", err)
	}
	if meta.State != files.StateDownloading {
		t.Fatalf("meta.State = %q, want downloading", meta.State)
	}
	if meta.Direction != files.DirIn {
		t.Fatalf("meta.Direction = %q, want in", meta.Direction)
	}
	if meta.Token != "tok-test-1" {
		t.Fatalf("meta.Token = %q, want tok-test-1", meta.Token)
	}

	stub.mu.Lock()
	got := stub.lastFetch
	calls := stub.fetchCalls
	stub.mu.Unlock()
	if calls != 1 {
		t.Fatalf("fetchCalls = %d, want 1", calls)
	}
	if got.MsgID != msgID || got.PeerID != peerID || got.Token != "tok-test-1" {
		t.Fatalf("fetch req = %+v", got)
	}
	if got.ExpectedSize != 1024 || got.ExpectedSha256 != strings.Repeat("a", 64) {
		t.Fatalf("fetch req size/hash mismatch: %+v", got)
	}
}

func TestFileFetchStateHandler_Ready_PullsAndStages(t *testing.T) {
	stub := startFilesStub(t)
	d := newFilesTestDaemon(t, stub)

	const peerID = "0000000000000000000000000000bbbb"
	dc, _, err := d.createDirectWithDefaults(peerID)
	if err != nil {
		t.Fatalf("createDirectWithDefaults: %v", err)
	}
	chatID := dc.ID

	msgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	feBody := FileEventBody{
		Token:            "tok-test-2",
		UrlPath:          "/files/tok-test-2",
		Name:             "movie.mp4",
		Size:             4096,
		Sha256Ciphertext: strings.Repeat("b", 64),
		State:            string(files.StateDownloading),
	}
	bodyRaw, _ := json.Marshal(feBody)
	if _, err := d.events.AppendInbound(events.InboundParams{
		ChatID:     chatID,
		Kind:       events.KindFile,
		SenderTs:   time.Now().Unix(),
		EnvelopeID: "env-file-2",
		MsgID:      msgID,
		Status:     events.DecryptOK,
		Body:       bodyRaw,
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.files.PutMeta(files.Metadata{
		MsgID:            msgID,
		ChatID:           chatID,
		Direction:        files.DirIn,
		Token:            "tok-test-2",
		OriginalName:     "movie.mp4",
		Size:             4096,
		Sha256Ciphertext: strings.Repeat("b", 64),
		State:            files.StateDownloading,
		CreatedAt:        time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}

	want := []byte("opaque-ciphertext-blob")
	stub.mu.Lock()
	stub.stagingBody = want
	stub.mu.Unlock()

	handler := fileFetchStateHandler(context.Background(), d)
	handler(backendapi.FileFetchEvent{
		MsgID:         msgID,
		Token:         "tok-test-2",
		State:         string(files.StateReady),
		BytesReceived: int64(len(want)),
		TotalBytes:    int64(len(want)),
		At:            time.Now().Unix(),
	})

	stagingPath, err := d.files.StagingPath(msgID)
	if err != nil {
		t.Fatal(err)
	}
	if !waitFile(stagingPath, 2*time.Second) {
		t.Fatalf("staging file %s did not appear", stagingPath)
	}
	got, err := readFile(stagingPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("staging bytes diverged: got %q want %q", got, want)
	}

	meta, err := d.files.GetMeta(msgID)
	if err != nil {
		t.Fatalf("GetMeta: %v", err)
	}
	if meta.State != files.StateAwaitingKey {
		t.Fatalf("meta.State = %q, want awaiting_key", meta.State)
	}

	ev, err := d.events.GetByMsgID(msgID)
	if err != nil {
		t.Fatal(err)
	}
	var fb FileEventBody
	if err := json.Unmarshal(ev.Body, &fb); err != nil {
		t.Fatal(err)
	}
	if fb.State != string(files.StateAwaitingKey) {
		t.Fatalf("event body state = %q, want awaiting_key", fb.State)
	}

	stub.mu.Lock()
	dropCalls := stub.dropCalls
	dropBody := string(stub.dropBody)
	stub.mu.Unlock()
	if dropCalls != 1 {
		t.Fatalf("dropCalls = %d, want 1", dropCalls)
	}
	if !strings.Contains(dropBody, "tok-test-2") {
		t.Fatalf("drop body = %q, want token tok-test-2", dropBody)
	}
}

func TestFileFetchStateHandler_FailedPermanent_StampsRow(t *testing.T) {
	stub := startFilesStub(t)
	d := newFilesTestDaemon(t, stub)

	const peerID = "0000000000000000000000000000cccc"
	dc, _, err := d.createDirectWithDefaults(peerID)
	if err != nil {
		t.Fatal(err)
	}
	chatID := dc.ID

	msgID, _ := msg.NewID()
	feBody := FileEventBody{State: string(files.StateDownloading), Token: "tok-fail"}
	bodyRaw, _ := json.Marshal(feBody)
	if _, err := d.events.AppendInbound(events.InboundParams{
		ChatID:     chatID,
		Kind:       events.KindFile,
		SenderTs:   time.Now().Unix(),
		EnvelopeID: "env-file-fail",
		MsgID:      msgID,
		Status:     events.DecryptOK,
		Body:       bodyRaw,
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.files.PutMeta(files.Metadata{
		MsgID: msgID, ChatID: chatID, Direction: files.DirIn,
		Token: "tok-fail", State: files.StateDownloading,
	}); err != nil {
		t.Fatal(err)
	}

	handler := fileFetchStateHandler(context.Background(), d)
	handler(backendapi.FileFetchEvent{
		MsgID:     msgID,
		Token:     "tok-fail",
		State:     string(files.StateFailedPermanent),
		LastError: "peer 410 (token invalidated)",
		At:        time.Now().Unix(),
	})

	ev, err := d.events.GetByMsgID(msgID)
	if err != nil {
		t.Fatal(err)
	}
	var fb FileEventBody
	if err := json.Unmarshal(ev.Body, &fb); err != nil {
		t.Fatal(err)
	}
	if fb.State != string(files.StateFailedPermanent) {
		t.Fatalf("event state = %q, want failed_permanent", fb.State)
	}
	if fb.LastError == "" {
		t.Fatalf("event LastError empty; want a 410 reason")
	}
	meta, err := d.files.GetMeta(msgID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.State != files.StateFailedPermanent {
		t.Fatalf("meta state = %q, want failed_permanent", meta.State)
	}
}

func TestIngestFileOffer_DefersReceipt(t *testing.T) {
	stub := startFilesStub(t)
	d := newFilesTestDaemon(t, stub)

	const peerID = "0000000000000000000000000000a1a1"
	preEstablishSession(t, d, peerID)
	chatID := chatIDForPeer(t, d, peerID)

	msgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	wrapper := &msg.Wrapper{
		V:     msg.Version,
		Seq:   1,
		Ts:    time.Now().Unix(),
		MsgID: msgID,
		Kind:  msg.KindFileOffer,
	}
	body := &msg.FileOfferBody{
		Token:            "tok-defer-receipt",
		UrlPath:          "/files/tok-defer-receipt",
		Name:             "secret.bin",
		Size:             64,
		Mime:             "application/octet-stream",
		Sha256Ciphertext: strings.Repeat("0", 64),
	}
	entry := backendapi.InboxEntry{
		ArrivalAt: time.Now().UnixNano(),
		PeerID:    peerID,
		Envelope: backendapi.RawEnvelope{
			ID: "env-defer-1", Timestamp: wrapper.Ts, From: "alice-onion", Kind: "text",
		},
	}

	ingestFileOffer(context.Background(), d, chatID, entry, wrapper, body)

	stub.mu.Lock()
	sendsAfterIngest := stub.sendCalls
	fetchesAfterIngest := stub.fetchCalls
	stub.mu.Unlock()
	if sendsAfterIngest != 0 {
		t.Fatalf("ingestFileOffer shipped %d /send envelope(s); want 0 (receipt must defer until post-pull)", sendsAfterIngest)
	}
	if fetchesAfterIngest != 1 {
		t.Fatalf("ingestFileOffer enqueued %d fetch(es); want 1", fetchesAfterIngest)
	}

	stub.mu.Lock()
	stub.stagingBody = []byte("opaque-cipher")
	stub.mu.Unlock()
	handler := fileFetchStateHandler(context.Background(), d)
	handler(backendapi.FileFetchEvent{
		MsgID:         msgID,
		Token:         "tok-defer-receipt",
		State:         string(files.StateReady),
		BytesReceived: int64(len("opaque-cipher")),
		TotalBytes:    int64(len("opaque-cipher")),
		At:            time.Now().Unix(),
	})

	stub.mu.Lock()
	sendsAfterReady := stub.sendCalls
	lastSendPeer := stub.lastSendReq.PeerID
	stub.mu.Unlock()
	if sendsAfterReady != 1 {
		t.Fatalf("post-pull shipped %d /send envelope(s); want 1 (receipt should fire after pull success)", sendsAfterReady)
	}
	if lastSendPeer != peerID {
		t.Fatalf("post-pull receipt addressed to %q, want %q", lastSendPeer, peerID)
	}
}

func waitFile(path string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func readFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}
