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

	fetchState string

	haomadIDs []string

	sendCalls   int
	lastSendReq backendapi.SendRequest

	sentPeerIDs []string

	sentPayloads [][]byte

	sentKinds []string

	receiptCalls  int
	lastReceipt   backendapi.RedeemFileReceiptRequest
	receiptStatus int
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
		stub.haomadIDs = append(stub.haomadIDs, stub.lastFetch.MsgID)
		state := stub.fetchState
		if state == "" {
			state = "pending"
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"msg_id": stub.lastFetch.MsgID,
			"token":  stub.lastFetch.Token,
			"state":  state,
		})
	})
	mux.HandleFunc("GET /files/staging/{msg_id}", func(w http.ResponseWriter, r *http.Request) {
		stub.mu.Lock()
		stub.haomadIDs = append(stub.haomadIDs, r.PathValue("msg_id"))
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
		stub.haomadIDs = append(stub.haomadIDs, r.PathValue("msg_id"))
		stub.dropBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /send", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		stub.mu.Lock()
		stub.sendCalls++
		stub.lastSendReq = backendapi.SendRequest{}
		_ = json.Unmarshal(body, &stub.lastSendReq)
		stub.sentPeerIDs = append(stub.sentPeerIDs, stub.lastSendReq.PeerID)
		stub.sentPayloads = append(stub.sentPayloads, append([]byte(nil), stub.lastSendReq.Payload...))
		stub.sentKinds = append(stub.sentKinds, stub.lastSendReq.Kind)
		stub.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(backendapi.SendResponse{EnvelopeID: "env-stub-receipt-001"})
	})
	mux.HandleFunc("POST /files/receipts", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		stub.mu.Lock()
		stub.receiptCalls++
		_ = json.Unmarshal(body, &stub.lastReceipt)
		status := stub.receiptStatus
		token := stub.lastReceipt.Token
		stub.mu.Unlock()
		if status == 0 {
			status = http.StatusOK
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(backendapi.RedeemFileReceiptResponse{Token: token})
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

func ingestOfferFrom(t *testing.T, d *daemon, chatID chat.ChatID, fromPeerID, token string) string {
	t.Helper()
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
		Token:            token,
		UrlPath:          "/files/" + token,
		Name:             "secret.pdf",
		Size:             1024,
		Mime:             "application/pdf",
		Sha256Ciphertext: strings.Repeat("a", 64),
	}
	entry := backendapi.InboxEntry{
		ArrivalAt: time.Now().UnixNano(),
		PeerID:    fromPeerID,
		Envelope: backendapi.RawEnvelope{
			ID:        "env-" + token,
			Timestamp: wrapper.Ts,
			Kind:      "text",
		},
	}
	ingestFileOffer(context.Background(), d, chatID, entry, wrapper, body)
	return msgID
}

func TestIngestFileOffer_RecordsTheOfferingDevice(t *testing.T) {
	stub := startFilesStub(t)
	d := newFilesTestDaemon(t, stub)
	const phone = "00000000000000000000000000003001"
	const laptop = "00000000000000000000000000003002"
	dc, _, err := d.createDirectWithDefaults(phone)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.chats.AttachPeerID(dc.ID, laptop); err != nil {
		t.Fatal(err)
	}

	msgID := ingestOfferFrom(t, d, dc.ID, laptop, "tok-from-laptop")

	meta, err := d.files.GetMeta(msgID)
	if err != nil {
		t.Fatalf("GetMeta: %v", err)
	}
	if meta.OfferPeerID != laptop {
		t.Fatalf("meta.OfferPeerID = %q, want %q (the device the envelope came from)", meta.OfferPeerID, laptop)
	}
}

func TestResolveOfferPeer_AddressesTheDeviceThatSentTheOffer(t *testing.T) {
	stub := startFilesStub(t)
	d := newFilesTestDaemon(t, stub)
	const phone = "00000000000000000000000000003101"
	const laptop = "00000000000000000000000000003102"
	dc, _, err := d.createDirectWithDefaults(phone)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.chats.AttachPeerID(dc.ID, laptop); err != nil {
		t.Fatal(err)
	}

	msgID := ingestOfferFrom(t, d, dc.ID, laptop, "tok-answer-target")

	if got := resolveOfferPeer(d, msgID); got != laptop {
		t.Fatalf("resolveOfferPeer = %q, want %q — the answer would go to a device holding no record of that token", got, laptop)
	}
}

func TestResolveOfferPeer_FallsBackToThePrimaryDevice(t *testing.T) {
	stub := startFilesStub(t)
	d := newFilesTestDaemon(t, stub)
	const phone = "00000000000000000000000000003201"
	const laptop = "00000000000000000000000000003202"
	dc, _, err := d.createDirectWithDefaults(phone)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.chats.AttachPeerID(dc.ID, laptop); err != nil {
		t.Fatal(err)
	}

	msgID := ingestOfferFrom(t, d, dc.ID, laptop, "tok-old-row")

	meta, err := d.files.GetMeta(msgID)
	if err != nil {
		t.Fatal(err)
	}
	meta.OfferPeerID = ""
	if err := d.files.PutMeta(meta); err != nil {
		t.Fatal(err)
	}

	if got := resolveOfferPeer(d, msgID); got != phone {
		t.Fatalf("resolveOfferPeer = %q, want the chat's primary device %q", got, phone)
	}
}

func TestResolveOfferPeer_RetiredDeviceFallsBackToThePrimary(t *testing.T) {
	stub := startFilesStub(t)
	d := newFilesTestDaemon(t, stub)
	const phone = "00000000000000000000000000003301"
	const retired = "00000000000000000000000000003302"
	dc, _, err := d.createDirectWithDefaults(phone)
	if err != nil {
		t.Fatal(err)
	}

	if got := dc.FanoutPeerIDs(); len(got) != 1 || got[0] != phone {
		t.Fatalf("routing set = %v, want [%s]", got, phone)
	}

	msgID := ingestOfferFrom(t, d, dc.ID, retired, "tok-retired-device")

	meta, err := d.files.GetMeta(msgID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.OfferPeerID != retired {
		t.Fatalf("meta.OfferPeerID = %q, want %q", meta.OfferPeerID, retired)
	}
	if got := resolveOfferPeer(d, msgID); got != phone {
		t.Fatalf("resolveOfferPeer = %q, want the chat's primary device %q — a retired peerID is addressed for good", got, phone)
	}
}

func TestResolveOfferPeer_UnknownRowAddressesNobody(t *testing.T) {
	stub := startFilesStub(t)
	d := newFilesTestDaemon(t, stub)
	if got := resolveOfferPeer(d, "no-such-msg-id"); got != "" {
		t.Fatalf("resolveOfferPeer = %q, want empty", got)
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

	want := []byte(strings.Repeat("o", 4096))
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

func TestSweepStagedFetches_RecoversOrphan(t *testing.T) {
	stub := startFilesStub(t)
	d := newFilesTestDaemon(t, stub)

	const peerID = "0000000000000000000000000000dddd"
	dc, _, err := d.createDirectWithDefaults(peerID)
	if err != nil {
		t.Fatalf("createDirectWithDefaults: %v", err)
	}
	chatID := dc.ID
	msgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	feBody := FileEventBody{Token: "tok-orphan", UrlPath: "/files/tok-orphan", Name: "doc.pdf", Size: 2048, Sha256Ciphertext: strings.Repeat("c", 64), State: string(files.StateDownloading)}
	bodyRaw, _ := json.Marshal(feBody)
	if _, err := d.events.AppendInbound(events.InboundParams{
		ChatID: chatID, Kind: events.KindFile, SenderTs: time.Now().Unix(),
		EnvelopeID: "env-orphan", MsgID: msgID, Status: events.DecryptOK, Body: bodyRaw,
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.files.PutMeta(files.Metadata{
		MsgID: msgID, ChatID: chatID, Direction: files.DirIn, Token: "tok-orphan",
		OriginalName: "doc.pdf", Size: 2048, Sha256Ciphertext: strings.Repeat("c", 64),
		State: files.StateDownloading, CreatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}

	want := []byte(strings.Repeat("s", 2048))
	stub.mu.Lock()
	stub.stagingBody = want
	stub.mu.Unlock()

	sweepStagedFetches(context.Background(), d)

	stagingPath, err := d.files.StagingPath(msgID)
	if err != nil {
		t.Fatal(err)
	}
	if !waitFile(stagingPath, 2*time.Second) {
		t.Fatalf("staging file %s did not appear — orphan not recovered", stagingPath)
	}
	meta, err := d.files.GetMeta(msgID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.State != files.StateAwaitingKey {
		t.Fatalf("meta.State = %q, want awaiting_key (recovered)", meta.State)
	}

	stub.mu.Lock()
	dropCalls := stub.dropCalls
	stub.mu.Unlock()
	if dropCalls != 1 {
		t.Fatalf("dropCalls = %d, want 1", dropCalls)
	}
}

func TestSweepStagedFetches_NotStagedLeavesRow(t *testing.T) {
	stub := startFilesStub(t)
	d := newFilesTestDaemon(t, stub)

	const peerID = "0000000000000000000000000000eeee"
	dc, _, err := d.createDirectWithDefaults(peerID)
	if err != nil {
		t.Fatal(err)
	}
	chatID := dc.ID
	msgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	feBody := FileEventBody{Token: "tok-inflight", UrlPath: "/files/tok-inflight", Name: "big.bin", Size: 999, Sha256Ciphertext: strings.Repeat("d", 64), State: string(files.StateDownloading)}
	bodyRaw, _ := json.Marshal(feBody)
	if _, err := d.events.AppendInbound(events.InboundParams{
		ChatID: chatID, Kind: events.KindFile, SenderTs: time.Now().Unix(),
		EnvelopeID: "env-inflight", MsgID: msgID, Status: events.DecryptOK, Body: bodyRaw,
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.files.PutMeta(files.Metadata{
		MsgID: msgID, ChatID: chatID, Direction: files.DirIn, Token: "tok-inflight",
		Size: 999, Sha256Ciphertext: strings.Repeat("d", 64),
		State: files.StateDownloading, CreatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}

	stub.mu.Lock()
	stub.stagingStatus = http.StatusNotFound
	stub.mu.Unlock()

	sweepStagedFetches(context.Background(), d)

	meta, err := d.files.GetMeta(msgID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.State != files.StateDownloading {
		t.Fatalf("meta.State = %q, want downloading (must not fail an in-flight row)", meta.State)
	}
	stub.mu.Lock()
	sendCalls := stub.sendCalls
	stub.mu.Unlock()
	if sendCalls != 0 {
		t.Fatalf("receipt sendCalls = %d, want 0 (nothing to receipt yet)", sendCalls)
	}
}

func TestSweepStagedFetches_ShortBlobKeepsKey(t *testing.T) {
	stub := startFilesStub(t)
	d := newFilesTestDaemon(t, stub)

	const peerID = "0000000000000000000000000000d0d0"
	dc, _, err := d.createDirectWithDefaults(peerID)
	if err != nil {
		t.Fatal(err)
	}
	chatID := dc.ID
	msgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}
	const size = 2048
	feBody := FileEventBody{Token: "tok-short", UrlPath: "/files/tok-short", Name: "doc.pdf", Size: size, Sha256Ciphertext: strings.Repeat("e", 64), State: string(files.StateDownloading)}
	bodyRaw, _ := json.Marshal(feBody)
	if _, err := d.events.AppendInbound(events.InboundParams{
		ChatID: chatID, Kind: events.KindFile, SenderTs: time.Now().Unix(),
		EnvelopeID: "env-short", MsgID: msgID, Status: events.DecryptOK, Body: bodyRaw,
	}); err != nil {
		t.Fatal(err)
	}
	key := []byte(strings.Repeat("\x11", 32))
	nonce := []byte(strings.Repeat("\x22", 24))
	if err := d.files.PutMeta(files.Metadata{
		MsgID: msgID, ChatID: chatID, Direction: files.DirIn, Token: "tok-short",
		OriginalName: "doc.pdf", Size: size, Sha256Ciphertext: strings.Repeat("e", 64),
		KeyBytes: key, Nonce: nonce,
		State: files.StateDownloading, CreatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}

	stub.mu.Lock()
	stub.stagingBody = []byte(strings.Repeat("h", size/2))
	stub.mu.Unlock()

	sweepStagedFetches(context.Background(), d)

	meta, err := d.files.GetMeta(msgID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.State != files.StateDownloading {
		t.Fatalf("meta.State = %q, want downloading (a short blob must not settle the row)", meta.State)
	}
	if string(meta.KeyBytes) != string(key) || string(meta.Nonce) != string(nonce) {
		t.Fatalf("key or nonce changed after a short blob: key len %d, nonce len %d", len(meta.KeyBytes), len(meta.Nonce))
	}
	if stagingExists(d, msgID) {
		t.Fatal("a short blob was written to the local staging dir")
	}
	stub.mu.Lock()
	dropCalls := stub.dropCalls
	stub.mu.Unlock()
	if dropCalls != 0 {
		t.Fatalf("dropCalls = %d, want 0 (haomad keeps its copy)", dropCalls)
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

func TestFileFetchProgress_MovesAFailedRowBackToDownloading(t *testing.T) {
	cases := []struct {
		from files.State
		want files.State
	}{
		{files.StateFailedTransient, files.StateDownloading},
		{files.StatePending, files.StateDownloading},
		{files.StateAwaitingKey, files.StateAwaitingKey},
		{files.StateReady, files.StateReady},
	}
	for _, c := range cases {
		t.Run(string(c.from), func(t *testing.T) {
			stub := startFilesStub(t)
			d := newFilesTestDaemon(t, stub)
			dc, _, err := d.createDirectWithDefaults("0000000000000000000000000000dddd")
			if err != nil {
				t.Fatal(err)
			}
			msgID, _ := msg.NewID()
			bodyRaw, _ := json.Marshal(FileEventBody{State: string(c.from), Token: "tok-retry"})
			if _, err := d.events.AppendInbound(events.InboundParams{
				ChatID:     dc.ID,
				Kind:       events.KindFile,
				SenderTs:   time.Now().Unix(),
				EnvelopeID: "env-file-retry",
				MsgID:      msgID,
				Status:     events.DecryptOK,
				Body:       bodyRaw,
			}); err != nil {
				t.Fatal(err)
			}
			if err := d.files.PutMeta(files.Metadata{
				MsgID: msgID, ChatID: dc.ID, Direction: files.DirIn,
				Token: "tok-retry", State: c.from,
			}); err != nil {
				t.Fatal(err)
			}

			fileFetchProgressHandler(d)(backendapi.FileFetchEvent{
				MsgID:         msgID,
				State:         string(files.StateDownloading),
				BytesReceived: 100,
				TotalBytes:    1000,
			})

			ev, err := d.events.GetByMsgID(msgID)
			if err != nil {
				t.Fatal(err)
			}
			var fb FileEventBody
			if err := json.Unmarshal(ev.Body, &fb); err != nil {
				t.Fatal(err)
			}
			if fb.State != string(c.want) {
				t.Fatalf("row state after progress = %q, want %q", fb.State, c.want)
			}
			meta, err := d.files.GetMeta(msgID)
			if err != nil {
				t.Fatal(err)
			}
			if meta.State != c.want {
				t.Fatalf("meta state after progress = %q, want %q", meta.State, c.want)
			}
		})
	}
}

func TestIngestFileOffer_RowAlreadyHeld_StillFetchesTheBytes(t *testing.T) {
	stub := startFilesStub(t)
	d := newFilesTestDaemon(t, stub)

	const peerID = "0000000000000000000000000000cccc"
	dc, _, err := d.createDirectWithDefaults(peerID)
	if err != nil {
		t.Fatalf("createDirectWithDefaults: %v", err)
	}
	chatID := dc.ID

	msgID, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}

	sibBody, _ := json.Marshal(FileEventBody{
		Token:            "tok-sibling-1",
		UrlPath:          "/files/tok-sibling-1",
		Name:             "secret.pdf",
		Size:             1024,
		Mime:             "application/pdf",
		Sha256Ciphertext: strings.Repeat("a", 64),
		State:            string(files.StatePending),
	})
	if _, err := d.events.AppendInbound(events.InboundParams{
		ChatID:              chatID,
		Kind:                events.KindFile,
		SenderTs:            time.Now().Unix(),
		MsgID:               msgID,
		Status:              events.DecryptOK,
		Body:                sibBody,
		MirroredFromSibling: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.files.GetMeta(msgID); err == nil {
		t.Fatal("precondition: a recovered row must arrive with no file metadata")
	}

	wrapper := &msg.Wrapper{
		V:     msg.Version,
		Seq:   1,
		Ts:    time.Now().Unix(),
		MsgID: msgID,
		Kind:  msg.KindFileOffer,
	}
	offer := &msg.FileOfferBody{
		Token:            "tok-mine-1",
		UrlPath:          "/files/tok-mine-1",
		Name:             "secret.pdf",
		Size:             1024,
		Mime:             "application/pdf",
		Sha256Ciphertext: strings.Repeat("a", 64),
	}
	ingestFileOffer(context.Background(), d, chatID, backendapi.InboxEntry{
		ArrivalAt: time.Now().UnixNano(),
		PeerID:    peerID,
		Envelope: backendapi.RawEnvelope{
			ID:        "env-file-dup-1",
			Timestamp: wrapper.Ts,
			From:      "alice-onion",
			Kind:      "text",
		},
	}, wrapper, offer)

	rows, err := d.events.List(chatID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1 — the row was already held", len(rows))
	}

	meta, err := d.files.GetMeta(msgID)
	if err != nil {
		t.Fatalf("no file metadata after the offer arrived: %v — the attachment is unreachable and no sweep will find it", err)
	}
	if meta.Token != "tok-mine-1" || meta.Direction != files.DirIn || meta.State != files.StateDownloading {
		t.Fatalf("meta = %+v, want this device's token, inbound, downloading", meta)
	}

	stub.mu.Lock()
	fetchCalls, lastFetch := stub.fetchCalls, stub.lastFetch
	stub.mu.Unlock()
	if fetchCalls != 1 {
		t.Fatalf("fetchCalls = %d, want 1 — no download was started for a file this device does not have", fetchCalls)
	}
	if lastFetch.MsgID != msgID || lastFetch.Token != "tok-mine-1" {
		t.Fatalf("fetch req = %+v, want msg_id=%s token=tok-mine-1", lastFetch, msgID)
	}

	want := []byte(strings.Repeat("o", 1024))
	stub.mu.Lock()
	stub.stagingBody = want
	stub.mu.Unlock()
	fileFetchStateHandler(context.Background(), d)(backendapi.FileFetchEvent{
		MsgID:         msgID,
		Token:         "tok-mine-1",
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
	staged, err := d.files.GetMeta(msgID)
	if err != nil {
		t.Fatal(err)
	}
	if staged.State != files.StateAwaitingKey {
		t.Fatalf("meta.State = %q, want awaiting_key", staged.State)
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
