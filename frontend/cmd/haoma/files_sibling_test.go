package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/chacha20poly1305"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/files"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/signal"
	"haoma-frontend/internal/syncrail"
)

const (
	siblingTestToken = "sibling-own-token-5e4d3c2b1a"

	siblingTestStaging = "9c8b7a6f5e4d3c2b1a0f9e8d7c6b5a49"
)

type siblingFile struct {
	plaintext  []byte
	key, nonce []byte
	ciphertext []byte
	sha        string
}

func newSiblingFile(t *testing.T) siblingFile {
	t.Helper()
	f := siblingFile{
		plaintext: []byte("a photo I sent from my phone, bytes and all\n"),
		key:       make([]byte, chacha20poly1305.KeySize),
		nonce:     make([]byte, chacha20poly1305.NonceSizeX),
	}
	if _, err := rand.Read(f.key); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(f.nonce); err != nil {
		t.Fatal(err)
	}
	aead, err := chacha20poly1305.NewX(f.key)
	if err != nil {
		t.Fatal(err)
	}

	padded := make([]byte, len(f.plaintext)+1000)
	copy(padded, f.plaintext)
	if _, err := rand.Read(padded[len(f.plaintext):]); err != nil {
		t.Fatal(err)
	}
	f.ciphertext = aead.Seal(nil, f.nonce, padded, nil)
	sum := sha256.Sum256(f.ciphertext)
	f.sha = hex.EncodeToString(sum[:])
	return f
}

func siblingOfferWrapper(t *testing.T, f siblingFile, caption string) *msg.Wrapper {
	t.Helper()
	id, err := msg.NewID()
	if err != nil {
		t.Fatal(err)
	}

	w, err := msg.BuildFileOffer(3, time.Now().Unix(), id, siblingTestToken, "/files/"+siblingTestToken,
		"photo.jpg", uint64(len(f.plaintext)+chacha20poly1305.Overhead), "image/jpeg", f.sha, caption, 0)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func sealSiblingOffer(t *testing.T, d *daemon, wrapper *msg.Wrapper, offer syncrail.SiblingOffer) []byte {
	t.Helper()
	plain, err := msg.Marshal(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(syncrail.MirrorBody{
		Fingerprint:  railFingerprint,
		Wrapper:      plain,
		WireKind:     string(wrapper.Kind),
		MsgID:        wrapper.MsgID,
		DisplayTs:    wrapper.Ts,
		OriginChatID: "chat-on-the-other-device",
		OriginPeerID: "ffffffffffffffffffffffffffffffff",
		SiblingOffer: &offer,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sealRailEnvelope(t, d, syncrail.Envelope{V: syncrail.Version, Kind: syncrail.KindMirror, Ts: wrapper.Ts, Body: body})
}

func offerFor(f siblingFile) syncrail.SiblingOffer {
	return syncrail.SiblingOffer{
		StagingID:        siblingTestStaging,
		Token:            siblingTestToken,
		Key:              append([]byte(nil), f.key...),
		Nonce:            append([]byte(nil), f.nonce...),
		Sha256Ciphertext: f.sha,
		BlobSize:         uint64(len(f.ciphertext)),
		PlaintextSize:    uint64(len(f.plaintext)),
	}
}

func newSiblingReceiver(t *testing.T) (*daemon, *filesStub, chat.ChatID) {
	t.Helper()
	stub := startFilesStub(t)
	d := newFilesTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)
	remote, err := signal.Bootstrap(3)
	if err != nil {
		t.Fatal(err)
	}
	processRemoteBundle(t, d, railPeerID, remote)
	return d, stub, chatID
}

func fileRow(t *testing.T, d *daemon, chatID chat.ChatID, msgID string) (events.Event, FileEventBody) {
	t.Helper()
	rows, err := d.events.List(chatID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found []events.Event
	for _, r := range rows {
		if r.MsgID == msgID {
			found = append(found, r)
		}
	}
	if len(found) != 1 {
		t.Fatalf("rows for msg_id %s = %d, want exactly 1", msgID, len(found))
	}
	var body FileEventBody
	if err := json.Unmarshal(found[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	return found[0], body
}

func TestSiblingOffer_FetchesFromTheServingDeviceAndSeals(t *testing.T) {
	d, stub, chatID := newSiblingReceiver(t)
	f := newSiblingFile(t)
	w := siblingOfferWrapper(t, f, "look at this")
	ctx := context.Background()

	processInboxEntry(ctx, d, railEntry(railSiblingID, "env-sib-offer", sealSiblingOffer(t, d, w, offerFor(f))))

	stub.mu.Lock()
	fetchCalls, fetch := stub.fetchCalls, stub.lastFetch
	stub.mu.Unlock()
	if fetchCalls != 1 {
		t.Fatalf("fetch calls = %d, want 1", fetchCalls)
	}
	if fetch.PeerID != railSiblingID {
		t.Errorf("fetch addressed %q, want the device that sent the offer (%q)", fetch.PeerID, railSiblingID)
	}
	if fetch.Token != siblingTestToken || fetch.UrlPath != "/files/"+siblingTestToken {
		t.Errorf("fetch token/url = %q %q, want this device's own", fetch.Token, fetch.UrlPath)
	}
	if fetch.MsgID != siblingTestStaging {
		t.Errorf("fetch named %q to haomad, want the staging id — haomad must never see the msg id", fetch.MsgID)
	}
	if fetch.ExpectedSha256 != f.sha || fetch.ExpectedSize != int64(len(f.ciphertext)) {
		t.Errorf("fetch expects sha=%s size=%d, want sha=%s size=%d", fetch.ExpectedSha256, fetch.ExpectedSize, f.sha, len(f.ciphertext))
	}

	ev, body := fileRow(t, d, chatID, w.MsgID)
	if ev.Direction != events.DirOut || !ev.MirroredFromSibling {
		t.Errorf("row direction=%s mirrored=%t, want an outbound row from my other device", ev.Direction, ev.MirroredFromSibling)
	}
	if body.Token != "" || body.UrlPath != "" {
		t.Errorf("row body holds token=%q url_path=%q — a body reaches every client", body.Token, body.UrlPath)
	}
	if body.Caption != "look at this" {
		t.Errorf("caption = %q, want it kept", body.Caption)
	}
	if body.State != string(files.StatePending) {
		t.Errorf("transfer state = %q, want pending, as haomad answered the fetch", body.State)
	}

	stub.mu.Lock()
	stub.stagingBody = f.ciphertext
	stub.mu.Unlock()
	fileFetchStateHandler(ctx, d)(backendapi.FileFetchEvent{MsgID: siblingTestStaging, State: string(files.StateReady)})

	got, err := d.files.UnsealAtRest(chatID, w.MsgID)
	if err != nil {
		t.Fatalf("no sealed copy: %v", err)
	}
	if !bytes.Equal(got, f.plaintext) {
		t.Fatalf("sealed copy holds %q, want the bytes the other device sent", got)
	}
	meta, err := d.files.GetMeta(w.MsgID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.State != files.StateReady || meta.SealedPath == "" || meta.Direction != files.DirOut || !meta.FetchedFromSibling {
		t.Errorf("meta state=%s sealed_path=%q dir=%s fetched_from_sibling=%t, want ready, a path, out, true",
			meta.State, meta.SealedPath, meta.Direction, meta.FetchedFromSibling)
	}
	if len(meta.KeyBytes) != 0 || len(meta.Nonce) != 0 || meta.Token != "" {
		t.Error("the metadata row holds key material; it is printed whole by the store tools")
	}
	ev2, body2 := fileRow(t, d, chatID, w.MsgID)
	if ev2.DisplayTs != ev.DisplayTs || ev2.SenderSeq != ev.SenderSeq || body2.State != string(files.StateReady) {
		t.Errorf("row after seal: display_ts=%d seq=%d state=%s, want the same row (%d, %d) at ready",
			ev2.DisplayTs, ev2.SenderSeq, body2.State, ev.DisplayTs, ev.SenderSeq)
	}
	if _, err := d.files.GetSiblingOffer(w.MsgID); !errors.Is(err, files.ErrSiblingOfferNotFound) {
		t.Errorf("the key outlived the seal (err=%v)", err)
	}

	stub.mu.Lock()
	sends, receipts := stub.sendCalls, stub.receiptCalls
	haomadIDs := append([]string(nil), stub.haomadIDs...)
	stub.mu.Unlock()
	if sends != 0 || receipts != 0 {
		t.Errorf("sends=%d receipt acks=%d, want 0 and 0 — the sibling answers nobody", sends, receipts)
	}

	if len(haomadIDs) != 3 {
		t.Errorf("haomad file calls = %v, want fetch, pull and drop", haomadIDs)
	}
	for _, id := range haomadIDs {
		if id != siblingTestStaging {
			t.Errorf("haomad was handed %q, want only the staging id — never the msg id %s", id, w.MsgID)
		}
	}
}

func TestSiblingOffer_RefusesAnOfferThatDisagreesWithItsWrapper(t *testing.T) {
	for name, spoil := range map[string]func(*syncrail.SiblingOffer){
		"token":          func(o *syncrail.SiblingOffer) { o.Token = "a-contacts-token" },
		"hash":           func(o *syncrail.SiblingOffer) { o.Sha256Ciphertext = "00" },
		"key":            func(o *syncrail.SiblingOffer) { o.Key = o.Key[:8] },
		"no staging id":  func(o *syncrail.SiblingOffer) { o.StagingID = "" },
		"plaintext size": func(o *syncrail.SiblingOffer) { o.PlaintextSize-- },
	} {
		t.Run(name, func(t *testing.T) {
			d, stub, chatID := newSiblingReceiver(t)
			f := newSiblingFile(t)
			w := siblingOfferWrapper(t, f, "")
			offer := offerFor(f)
			spoil(&offer)
			processInboxEntry(context.Background(), d, railEntry(railSiblingID, "env-sib-bad", sealSiblingOffer(t, d, w, offer)))
			stub.mu.Lock()
			fetchCalls := stub.fetchCalls
			stub.mu.Unlock()
			if fetchCalls != 0 {
				t.Fatalf("fetch calls = %d, want 0 for a refused offer", fetchCalls)
			}
			fileRow(t, d, chatID, w.MsgID)
		})
	}
}

func TestSiblingOffer_SecretsReachNoLogLine(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	d, stub, chatID := newSiblingReceiver(t)
	f := newSiblingFile(t)
	w := siblingOfferWrapper(t, f, "")
	ctx := context.Background()
	processInboxEntry(ctx, d, railEntry(railSiblingID, "env-sib-log", sealSiblingOffer(t, d, w, offerFor(f))))
	stub.mu.Lock()
	stub.stagingBody = f.ciphertext
	stub.mu.Unlock()

	handler := fileFetchStateHandler(ctx, d)
	handler(backendapi.FileFetchEvent{MsgID: siblingTestStaging, Token: siblingTestToken, State: string(files.StateDownloading)})
	handler(backendapi.FileFetchEvent{MsgID: siblingTestStaging, Token: siblingTestToken, State: string(files.StateFailedTransient), LastError: dialErrorNaming(siblingTestToken)})
	handler(backendapi.FileFetchEvent{MsgID: siblingTestStaging, Token: siblingTestToken, State: string(files.StateReady)})
	handler(backendapi.FileFetchEvent{MsgID: siblingTestStaging, Token: siblingTestToken, State: string(files.StateReady)})
	if _, err := d.files.UnsealAtRest(chatID, w.MsgID); err != nil {
		t.Fatalf("precondition: the flow did not complete: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "sibling offer: file sealed") {
		t.Fatalf("precondition: the flow logged nothing to search:\n%s", out)
	}
	for name, secret := range map[string]string{
		"token":        siblingTestToken,
		"key hex":      hex.EncodeToString(f.key),
		"key base64":   base64.StdEncoding.EncodeToString(f.key),
		"nonce hex":    hex.EncodeToString(f.nonce),
		"nonce base64": base64.StdEncoding.EncodeToString(f.nonce),
	} {
		if strings.Contains(out, secret) {
			t.Errorf("the %s reached a log line", name)
		}
	}
}

func TestSiblingFetchSweep_SealsWhatHaomadFetchedWhileNobodyListened(t *testing.T) {
	d, stub, chatID := newSiblingReceiver(t)
	f := newSiblingFile(t)
	w := siblingOfferWrapper(t, f, "")
	ctx := context.Background()
	processInboxEntry(ctx, d, railEntry(railSiblingID, "env-sib-sweep", sealSiblingOffer(t, d, w, offerFor(f))))

	stub.mu.Lock()
	stub.stagingBody = f.ciphertext
	stub.fetchState = string(files.StateReady)
	stub.mu.Unlock()
	sweepSiblingFetches(ctx, d)

	got, err := d.files.UnsealAtRest(chatID, w.MsgID)
	if err != nil || !bytes.Equal(got, f.plaintext) {
		t.Fatalf("sweep did not seal the file: err=%v", err)
	}
	stub.mu.Lock()
	fetchCalls, fetch := stub.fetchCalls, stub.lastFetch
	stub.mu.Unlock()
	if fetchCalls != 2 || fetch.PeerID != railSiblingID || fetch.Token != siblingTestToken || fetch.MsgID != siblingTestStaging {
		t.Errorf("sweep asked haomad %d times (want 2: offer + sweep), last to %q with its own token=%t",
			fetchCalls, fetch.PeerID, fetch.Token == siblingTestToken)
	}
}

func TestSiblingFetchSweep_PullsNothingWhileHaomadIsStillFetching(t *testing.T) {
	d, stub, chatID := newSiblingReceiver(t)
	f := newSiblingFile(t)
	w := siblingOfferWrapper(t, f, "")
	ctx := context.Background()
	processInboxEntry(ctx, d, railEntry(railSiblingID, "env-sib-busy", sealSiblingOffer(t, d, w, offerFor(f))))

	stub.mu.Lock()
	stub.stagingBody = f.ciphertext[:len(f.ciphertext)/2]
	stub.fetchState = string(files.StateDownloading)
	stub.mu.Unlock()
	sweepSiblingFetches(ctx, d)

	if _, err := d.files.UnsealAtRest(chatID, w.MsgID); err == nil {
		t.Fatal("the sweep sealed something while haomad was still fetching")
	}
	meta, err := d.files.GetMeta(w.MsgID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.State == files.StateFailedPermanent {
		t.Fatal("the sweep failed the file permanently while haomad was still fetching")
	}
	if _, err := d.files.GetSiblingOffer(w.MsgID); err != nil {
		t.Fatalf("the key was dropped while the fetch is still running: %v", err)
	}
	stub.mu.Lock()
	drops := stub.dropCalls
	stub.mu.Unlock()
	if drops != 0 {
		t.Errorf("the sweep pulled from haomad (%d staging drops) before haomad said ready", drops)
	}
}

func TestSiblingFetchSweep_ShortBlobKeepsTheKey(t *testing.T) {
	d, stub, chatID := newSiblingReceiver(t)
	f := newSiblingFile(t)
	w := siblingOfferWrapper(t, f, "")
	ctx := context.Background()
	processInboxEntry(ctx, d, railEntry(railSiblingID, "env-sib-short", sealSiblingOffer(t, d, w, offerFor(f))))

	stub.mu.Lock()
	stub.stagingBody = f.ciphertext[:len(f.ciphertext)-1]
	stub.fetchState = string(files.StateReady)
	stub.mu.Unlock()
	sweepSiblingFetches(ctx, d)

	if _, err := d.files.UnsealAtRest(chatID, w.MsgID); err == nil {
		t.Fatal("the sweep sealed a short blob")
	}
	meta, err := d.files.GetMeta(w.MsgID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.State == files.StateFailedPermanent {
		t.Fatal("a short blob failed the file permanently")
	}
	if _, err := d.files.GetSiblingOffer(w.MsgID); err != nil {
		t.Fatalf("a short blob dropped the held key: %v", err)
	}
	if stagingExists(d, w.MsgID) {
		t.Error("a short blob was written to the local staging dir")
	}
	stub.mu.Lock()
	drops := stub.dropCalls
	stub.mu.Unlock()
	if drops != 0 {
		t.Errorf("haomad was asked to drop its copy of a short blob (%d drops)", drops)
	}
}

func TestSiblingFetchSweep_DropsTheKeyOfASettledRow(t *testing.T) {
	d, _, _ := newSiblingReceiver(t)
	f := newSiblingFile(t)
	w := siblingOfferWrapper(t, f, "")
	ctx := context.Background()
	processInboxEntry(ctx, d, railEntry(railSiblingID, "env-sib-settled", sealSiblingOffer(t, d, w, offerFor(f))))

	meta, err := d.files.GetMeta(w.MsgID)
	if err != nil {
		t.Fatal(err)
	}
	meta.State = files.StateFailedPermanent
	if err := d.files.PutMeta(meta); err != nil {
		t.Fatal(err)
	}
	sweepSiblingFetches(ctx, d)
	if _, err := d.files.GetSiblingOffer(w.MsgID); !errors.Is(err, files.ErrSiblingOfferNotFound) {
		t.Fatalf("the key of a failed row outlived the sweep (err=%v)", err)
	}
}

func TestFinalizeSiblingFetch_SecondCallerDoesNothing(t *testing.T) {
	d, stub, chatID := newSiblingReceiver(t)
	f := newSiblingFile(t)
	w := siblingOfferWrapper(t, f, "")
	ctx := context.Background()
	processInboxEntry(ctx, d, railEntry(railSiblingID, "env-sib-busy2", sealSiblingOffer(t, d, w, offerFor(f))))
	stub.mu.Lock()
	stub.stagingBody = f.ciphertext
	stub.mu.Unlock()

	d.siblingSealBusy.Store(w.MsgID, struct{}{})
	if err := finalizeSiblingFetch(ctx, d, w.MsgID); err != nil {
		t.Fatalf("second caller returned %v, want nil", err)
	}
	if _, err := d.files.UnsealAtRest(chatID, w.MsgID); err == nil {
		t.Fatal("the second caller sealed the file")
	}
	d.siblingSealBusy.Delete(w.MsgID)
	if err := finalizeSiblingFetch(ctx, d, w.MsgID); err != nil {
		t.Fatalf("first caller: %v", err)
	}
	if _, err := d.files.UnsealAtRest(chatID, w.MsgID); err != nil {
		t.Fatalf("the caller holding the file did not seal it: %v", err)
	}
}

func TestRunSendFile_SiblingGetsABlobOfItsOwnStagedApart(t *testing.T) {
	stub := startSendFileStub(t)
	d := newSendFileTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000aaaa"
	preEstablishSession(t, d, peerID)
	if _, _, err := d.peerMeta.SetFingerprint(peerID, railFingerprint); err != nil {
		t.Fatal(err)
	}
	linkSibling(t, d, railSiblingID)
	dc, err := d.chats.GetByDirectPeer(peerID)
	if err != nil {
		t.Fatal(err)
	}

	plaintext := []byte("sent from this device, to be held on the other one too\n")
	res, code, err := runSendFile(context.Background(), d, dc, peerID, writeTempFile(t, plaintext), "", "with a caption")
	if err != nil {
		t.Fatalf("runSendFile: code=%q err=%v", code, err)
	}

	stub.mu.Lock()
	stages := append([]backendapi.StageFileRequest(nil), stub.stages...)
	sentTo := append([]string(nil), stub.sentPeerIDs...)
	payloads := append([][]byte(nil), stub.sentPayloads...)
	stub.mu.Unlock()

	if len(stages) != 2 {
		t.Fatalf("staging calls = %d, want 2: the contact's and the sibling's, apart", len(stages))
	}
	contact, sibling := stages[0], stages[1]
	for i, st := range stages {
		hasPeer, hasSib := false, false
		for _, r := range st.RecipientPeerIDs {
			hasPeer = hasPeer || r == peerID
			hasSib = hasSib || r == railSiblingID
		}
		if hasPeer && hasSib {
			t.Fatalf("staging call %d names the contact and the sibling together: %v", i, st.RecipientPeerIDs)
		}
	}
	if contact.MsgID != res.MsgID || len(contact.RecipientPeerIDs) != 1 || contact.RecipientPeerIDs[0] != peerID {
		t.Fatalf("contact staging = %s %v, want the msg id and the contact alone", contact.MsgID, contact.RecipientPeerIDs)
	}
	if len(sibling.RecipientPeerIDs) != 1 || sibling.RecipientPeerIDs[0] != railSiblingID {
		t.Fatalf("sibling staging recipients = %v, want the sibling alone", sibling.RecipientPeerIDs)
	}
	if sibling.MsgID == res.MsgID || len(sibling.MsgID) != 32 {
		t.Fatalf("sibling staged under %q, want a fresh 32-hex id that is not the msg id %s", sibling.MsgID, res.MsgID)
	}
	if bytes.Equal(sibling.Ciphertext, contact.Ciphertext) {
		t.Fatal("the sibling's blob is the contact's blob")
	}

	contactTok, sibTok := "tok-mint-0100", "tok-mint-0200"

	meta, err := d.files.GetMeta(res.MsgID)
	if err != nil {
		t.Fatal(err)
	}
	if _, has := meta.RecipientTokens[railSiblingID]; has {
		t.Error("the sibling is in RecipientTokens; a receipt could then pull the key by a second path")
	}
	if len(meta.SiblingStagingIDs) != 1 || meta.SiblingStagingIDs[0] != sibling.MsgID {
		t.Errorf("meta.SiblingStagingIDs = %v, want [%s] so delete and expiry drop the sibling's blob", meta.SiblingStagingIDs, sibling.MsgID)
	}

	var railPayload []byte
	for i, to := range sentTo {
		if to == railSiblingID {
			if railPayload != nil {
				t.Fatal("the sibling got two rail sends for one file")
			}
			railPayload = payloads[i]
		}
	}
	if railPayload == nil {
		t.Fatalf("no rail send to the sibling; sends went to %v", sentTo)
	}
	key, err := d.railKey()
	if err != nil {
		t.Fatal(err)
	}
	env, err := syncrail.OpenEnvelope(key, railPayload)
	if err != nil {
		t.Fatal(err)
	}
	body, err := syncrail.ParseMirrorBody(env)
	if err != nil {
		t.Fatal(err)
	}
	so := body.SiblingOffer
	if so == nil {
		t.Fatal("the sibling got a plain mirror, not a sibling offer")
	}
	if so.Token != sibTok || so.StagingID != sibling.MsgID {
		t.Errorf("offer token=%q staging=%q, want %q and %q", so.Token, so.StagingID, sibTok, sibling.MsgID)
	}
	if bytes.Equal(so.Key, meta.KeyBytes) {
		t.Error("the sibling offer holds the contact's file key")
	}
	w, err := msg.Unmarshal(body.Wrapper)
	if err != nil {
		t.Fatal(err)
	}
	fo, err := w.FileOffer()
	if err != nil {
		t.Fatal(err)
	}
	if fo.Token != sibTok || fo.Caption != "with a caption" || w.MsgID != res.MsgID || w.Seq != res.SenderSeq {
		t.Errorf("sibling wrapper token=%q caption=%q msg_id=%s seq=%d, want %q, the caption, %s, %d",
			fo.Token, fo.Caption, w.MsgID, w.Seq, sibTok, res.MsgID, res.SenderSeq)
	}
	if strings.Contains(string(env.Body), contactTok) {
		t.Error("the contact's token reached the sibling")
	}

	aead, err := chacha20poly1305.NewX(so.Key)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := aead.Open(nil, so.Nonce, sibling.Ciphertext, nil)
	if err != nil {
		t.Fatalf("the sibling offer's key does not open the sibling's blob: %v", err)
	}
	if so.PlaintextSize != uint64(len(plaintext)) || !bytes.Equal(opened[:len(plaintext)], plaintext) {
		t.Fatalf("the sibling's blob does not open to the file followed by padding (plaintext_size=%d)", so.PlaintextSize)
	}
	sum := sha256.Sum256(sibling.Ciphertext)
	if so.Sha256Ciphertext != hex.EncodeToString(sum[:]) || fo.Sha256Ciphertext != so.Sha256Ciphertext || so.BlobSize != uint64(len(sibling.Ciphertext)) {
		t.Error("the sibling offer's hash or blob size does not name the sibling's blob")
	}
	if fo.Size != uint64(len(contact.Ciphertext)) {
		t.Errorf("sibling wrapper size = %d, want the %d the contact's row shows", fo.Size, len(contact.Ciphertext))
	}

	pad := len(opened) - len(plaintext)
	if len(sibling.Ciphertext) == len(contact.Ciphertext) || pad < 1 {
		t.Fatalf("sibling blob %d bytes, contact blob %d — the padding is missing", len(sibling.Ciphertext), len(contact.Ciphertext))
	}
	if limit := max(64<<10, len(plaintext)/4); pad > limit {
		t.Fatalf("padding %d bytes, want at most %d", pad, limit)
	}
}

func TestSiblingPadLen_StaysInItsBounds(t *testing.T) {
	for _, size := range []int{0, 1, 1000, 64 << 10, 1 << 20, files.MaxPlaintextBytes} {
		limit := max(64<<10, size/4)
		if got := siblingPadMax(size); got != limit {
			t.Fatalf("siblingPadMax(%d) = %d, want %d", size, got, limit)
		}
		for i := 0; i < 200; i++ {
			n, err := siblingPadLen(size)
			if err != nil {
				t.Fatal(err)
			}
			if n < 1 || n > limit {
				t.Fatalf("siblingPadLen(%d) = %d, want 1..%d", size, n, limit)
			}
		}
	}
	if files.MaxPlaintextBytes+siblingPadMax(files.MaxPlaintextBytes)+chacha20poly1305.Overhead > files.MaxPullBytes {
		t.Fatal("a padded file at the cap does not fit what haoma pulls from haomad")
	}
}

func TestRunSendFile_FailedSendDropsTheSiblingBlobToo(t *testing.T) {
	stub := startSendFileStub(t)
	d := newSendFileTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000aaaa"

	if _, err := d.chats.CreateDirect(peerID); err != nil {
		t.Fatal(err)
	}
	linkSibling(t, d, railSiblingID)
	dc, err := d.chats.GetByDirectPeer(peerID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := runSendFile(context.Background(), d, dc, peerID, writeTempFile(t, []byte("x\n")), "", ""); err == nil {
		t.Fatal("precondition: the send should fail with no session")
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.stages) != 2 {
		t.Fatalf("staging calls = %d, want 2", len(stub.stages))
	}
	want := map[string]bool{stub.stages[0].MsgID: false, stub.stages[1].MsgID: false}
	for _, id := range stub.dropIDs {
		if _, ok := want[id]; ok {
			want[id] = true
		}
	}
	for id, dropped := range want {
		if !dropped {
			t.Errorf("staged blob %s was not dropped after the send failed (drops: %v)", id, stub.dropIDs)
		}
	}
}

func TestRunSendFile_NoSiblingStagesForTheContactOnly(t *testing.T) {
	stub := startSendFileStub(t)
	d := newSendFileTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000aaaa"
	preEstablishSession(t, d, peerID)
	dc, err := d.chats.GetByDirectPeer(peerID)
	if err != nil {
		t.Fatal(err)
	}
	if _, code, err := runSendFile(context.Background(), d, dc, peerID, writeTempFile(t, []byte("x\n")), "", ""); err != nil {
		t.Fatalf("runSendFile: code=%q err=%v", code, err)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.lastStage.RecipientPeerIDs) != 1 || stub.sendCalls != 1 {
		t.Fatalf("recipients=%v sends=%d, want the contact alone and one send", stub.lastStage.RecipientPeerIDs, stub.sendCalls)
	}
}

func TestSiblingOffer_RefusesAStagingIDThatIsTheMsgID(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	d, stub, chatID := newSiblingReceiver(t)
	f := newSiblingFile(t)
	w := siblingOfferWrapper(t, f, "")
	offer := offerFor(f)
	offer.StagingID = w.MsgID
	processInboxEntry(context.Background(), d, railEntry(railSiblingID, "env-sib-msgid", sealSiblingOffer(t, d, w, offer)))
	stub.mu.Lock()
	ids := append([]string(nil), stub.haomadIDs...)
	stub.mu.Unlock()
	if len(ids) != 0 {
		t.Fatalf("haomad was called with %v for an offer staged under the msg id", ids)
	}
	if _, err := d.files.GetMeta(w.MsgID); !errors.Is(err, files.ErrMetaNotFound) {
		t.Fatalf("the offer was taken, not refused (metadata err=%v)", err)
	}

	if !strings.Contains(logs.String(), "reason=sibling_offer_staging_is_msg_id") {
		t.Fatalf("the offer was not refused for its staging id:\n%s", logs.String())
	}
	fileRow(t, d, chatID, w.MsgID)
}

func TestSiblingOffer_HaomadEventsLandOnTheMsgIDRow(t *testing.T) {
	d, _, chatID := newSiblingReceiver(t)
	f := newSiblingFile(t)
	w := siblingOfferWrapper(t, f, "")
	ctx := context.Background()
	processInboxEntry(ctx, d, railEntry(railSiblingID, "env-sib-ev", sealSiblingOffer(t, d, w, offerFor(f))))
	fileFetchStateHandler(ctx, d)(backendapi.FileFetchEvent{
		MsgID: siblingTestStaging, State: string(files.StateFailedTransient), LastError: "dial failed",
	})
	if _, body := fileRow(t, d, chatID, w.MsgID); body.State != string(files.StateFailedTransient) {
		t.Fatalf("row state = %q, want failed_transient from haomad's event", body.State)
	}
}

func dialErrorNaming(token string) string {
	return (&url.Error{
		Op:  "Get",
		URL: "http://abcdefgh….onion/files/" + token,
		Err: errors.New("dial tcp: socks connect: host unreachable"),
	}).Error()
}

func TestSiblingOffer_HaomadErrorTextNeverReachesTheRow(t *testing.T) {
	d, _, chatID := newSiblingReceiver(t)
	f := newSiblingFile(t)
	w := siblingOfferWrapper(t, f, "")
	ctx := context.Background()
	processInboxEntry(ctx, d, railEntry(railSiblingID, "env-sib-err", sealSiblingOffer(t, d, w, offerFor(f))))

	lastErr := dialErrorNaming(siblingTestToken)
	if !strings.Contains(lastErr, siblingTestToken) {
		t.Fatalf("precondition: the dial error does not name the token: %s", lastErr)
	}
	for _, state := range []files.State{files.StateFailedTransient, files.StateFailedPermanent} {
		fileFetchStateHandler(ctx, d)(backendapi.FileFetchEvent{
			MsgID: siblingTestStaging, Token: siblingTestToken, State: string(state), LastError: lastErr,
		})
		ev, body := fileRow(t, d, chatID, w.MsgID)
		if body.State != string(state) {
			t.Fatalf("precondition: row state %q, want %q", body.State, state)
		}
		if strings.Contains(string(ev.Body), siblingTestToken) {
			t.Fatalf("the token reached the row body on %s: %s", state, ev.Body)
		}
	}
}

func TestSiblingOffer_LateEventsForASettledFileChangeNothing(t *testing.T) {
	t.Run("after the seal", func(t *testing.T) {
		d, stub, chatID := newSiblingReceiver(t)
		f := newSiblingFile(t)
		w := siblingOfferWrapper(t, f, "")
		ctx := context.Background()
		processInboxEntry(ctx, d, railEntry(railSiblingID, "env-sib-late", sealSiblingOffer(t, d, w, offerFor(f))))
		stub.mu.Lock()
		stub.stagingBody = f.ciphertext
		stub.mu.Unlock()
		handler := fileFetchStateHandler(ctx, d)
		handler(backendapi.FileFetchEvent{MsgID: siblingTestStaging, Token: siblingTestToken, State: string(files.StateReady)})
		if _, body := fileRow(t, d, chatID, w.MsgID); body.State != string(files.StateReady) {
			t.Fatalf("precondition: not sealed (%s)", body.State)
		}
		stub.mu.Lock()
		before := len(stub.haomadIDs)
		stub.mu.Unlock()

		handler(backendapi.FileFetchEvent{MsgID: siblingTestStaging, Token: siblingTestToken, State: string(files.StateFailedTransient), LastError: "late"})
		handler(backendapi.FileFetchEvent{MsgID: siblingTestStaging, Token: siblingTestToken, State: string(files.StateReady)})

		if _, body := fileRow(t, d, chatID, w.MsgID); body.State != string(files.StateReady) {
			t.Fatalf("a late event moved a sealed row to %q", body.State)
		}
		assertSettledEventHandled(t, d, stub, before, w.MsgID)
	})
	t.Run("deleted mid-fetch", func(t *testing.T) {
		d, stub, _ := newSiblingReceiver(t)
		f := newSiblingFile(t)
		w := siblingOfferWrapper(t, f, "")
		ctx := context.Background()
		processInboxEntry(ctx, d, railEntry(railSiblingID, "env-sib-del", sealSiblingOffer(t, d, w, offerFor(f))))
		if err := d.files.DeleteMeta(w.MsgID); err != nil {
			t.Fatal(err)
		}
		stub.mu.Lock()
		stub.stagingBody = f.ciphertext
		before := len(stub.haomadIDs)
		stub.mu.Unlock()

		fileFetchStateHandler(ctx, d)(backendapi.FileFetchEvent{MsgID: siblingTestStaging, Token: siblingTestToken, State: string(files.StateReady)})

		if _, err := d.files.GetMeta(w.MsgID); !errors.Is(err, files.ErrMetaNotFound) {
			t.Fatalf("a late ready brought a deleted row's metadata back (err=%v)", err)
		}
		assertSettledEventHandled(t, d, stub, before, w.MsgID)
	})
}

func assertSettledEventHandled(t *testing.T, d *daemon, stub *filesStub, before int, msgID string) {
	t.Helper()
	stub.mu.Lock()
	after := append([]string(nil), stub.haomadIDs[before:]...)
	drops, sends := stub.dropCalls, stub.sendCalls
	stub.mu.Unlock()
	if len(after) != 1 || after[0] != siblingTestStaging {
		t.Errorf("haomad calls after the late ready = %v, want one drop of %s", after, siblingTestStaging)
	}
	if drops == 0 {
		t.Error("haomad's copy was not dropped")
	}
	if sends != 0 {
		t.Errorf("sends = %d, want 0 — the contact flow ran", sends)
	}
	for _, id := range []string{siblingTestStaging, msgID} {
		if stagingExists(d, id) {
			t.Errorf("a blob was left in haoma's staging under %s", id)
		}
	}
}

func TestFinalizeSiblingFetch_AfterTheSealIsANoOp(t *testing.T) {
	d, stub, chatID := newSiblingReceiver(t)
	f := newSiblingFile(t)
	w := siblingOfferWrapper(t, f, "")
	ctx := context.Background()
	processInboxEntry(ctx, d, railEntry(railSiblingID, "env-sib-seq", sealSiblingOffer(t, d, w, offerFor(f))))
	stub.mu.Lock()
	stub.stagingBody = f.ciphertext
	stub.fetchState = string(files.StateReady)
	stub.mu.Unlock()
	sweepSiblingFetches(ctx, d)
	if err := finalizeSiblingFetch(ctx, d, w.MsgID); err != nil {
		t.Fatalf("finalize after the seal returned %v, want nil", err)
	}
	if _, body := fileRow(t, d, chatID, w.MsgID); body.State != string(files.StateReady) {
		t.Fatalf("row after a second finalize = %q, want ready", body.State)
	}
}

func TestSiblingOffer_RowTakesTheStateHaomadAnswers(t *testing.T) {
	for _, state := range []files.State{files.StatePending, files.StateDownloading, files.StateFailedTransient, files.StateFailedPermanent, files.StateReady} {
		t.Run(string(state), func(t *testing.T) {
			d, stub, chatID := newSiblingReceiver(t)
			f := newSiblingFile(t)
			w := siblingOfferWrapper(t, f, "")
			stub.mu.Lock()
			stub.fetchState = string(state)
			stub.stagingBody = f.ciphertext
			stub.mu.Unlock()
			processInboxEntry(context.Background(), d, railEntry(railSiblingID, "env-sib-st", sealSiblingOffer(t, d, w, offerFor(f))))
			if _, body := fileRow(t, d, chatID, w.MsgID); body.State != string(state) {
				t.Fatalf("row state = %q, want %q", body.State, state)
			}
			if state == files.StateReady {
				if got, err := d.files.UnsealAtRest(chatID, w.MsgID); err != nil || !bytes.Equal(got, f.plaintext) {
					t.Fatalf("haomad answered ready but the file was not sealed: %v", err)
				}
			}
		})
	}
}

func TestRunSendFile_SiblingBlobDroppedWhenNoOfferFollows(t *testing.T) {
	stub := startSendFileStub(t)
	d := newSendFileTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000aaaa"
	preEstablishSession(t, d, peerID)
	linkSibling(t, d, railSiblingID)
	dc, err := d.chats.GetByDirectPeer(peerID)
	if err != nil {
		t.Fatal(err)
	}
	res, code, err := runSendFile(context.Background(), d, dc, peerID, writeTempFile(t, []byte("orphan?\n")), "", "")
	if err != nil {
		t.Fatalf("runSendFile: code=%q err=%v", code, err)
	}
	stub.mu.Lock()
	stages := append([]backendapi.StageFileRequest(nil), stub.stages...)
	drops := append([]string(nil), stub.dropIDs...)
	sentTo := append([]string(nil), stub.sentPeerIDs...)
	stub.mu.Unlock()
	if len(stages) != 2 {
		t.Fatalf("precondition: staging calls = %d, want 2", len(stages))
	}
	for _, to := range sentTo {
		if to == railSiblingID {
			t.Fatal("precondition: the sibling got an offer")
		}
	}
	if len(drops) != 1 || drops[0] != stages[1].MsgID {
		t.Fatalf("drops = %v, want the sibling's blob %s alone", drops, stages[1].MsgID)
	}
	meta, err := d.files.GetMeta(res.MsgID)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.SiblingStagingIDs) != 0 {
		t.Fatalf("meta still names the dropped blob: %v", meta.SiblingStagingIDs)
	}
}

func TestFileFetchEvent_AnUnheldIDNeverRunsTheContactTail(t *testing.T) {
	d, stub, _ := newSiblingReceiver(t)
	stub.mu.Lock()
	stub.stagingBody = []byte("a blob nobody here holds")
	stub.mu.Unlock()
	const stray = "7e7e7e7e7e7e7e7e7e7e7e7e7e7e7e7e"
	fileFetchStateHandler(context.Background(), d)(backendapi.FileFetchEvent{MsgID: stray, Token: "stray-token", State: string(files.StateReady)})
	stub.mu.Lock()
	ids := append([]string(nil), stub.haomadIDs...)
	drops, sends := stub.dropCalls, stub.sendCalls
	stub.mu.Unlock()
	if len(ids) != 1 || ids[0] != stray || drops != 1 {
		t.Fatalf("haomad calls = %v (drops %d), want one drop of %s and no pull", ids, drops, stray)
	}
	if sends != 0 || stagingExists(d, stray) {
		t.Fatalf("the contact tail ran: sends=%d staged=%t", sends, stagingExists(d, stray))
	}
}

func TestSiblingOffer_SweptRowLeavesNoStagingEntry(t *testing.T) {
	d, stub, _ := newSiblingReceiver(t)
	f := newSiblingFile(t)
	w := siblingOfferWrapper(t, f, "")
	ctx := context.Background()
	processInboxEntry(ctx, d, railEntry(railSiblingID, "env-sib-swept", sealSiblingOffer(t, d, w, offerFor(f))))
	stub.mu.Lock()
	stub.stagingBody = f.ciphertext
	stub.mu.Unlock()
	fileFetchStateHandler(ctx, d)(backendapi.FileFetchEvent{MsgID: siblingTestStaging, Token: siblingTestToken, State: string(files.StateReady)})
	if _, ok, _ := siblingFetchMsgID(d, siblingTestStaging); !ok {
		t.Fatal("precondition: the entry was gone before the row")
	}

	if err := d.files.DeleteMeta(w.MsgID); err != nil {
		t.Fatal(err)
	}
	if msgID, ok, err := siblingFetchMsgID(d, siblingTestStaging); ok || err != nil {
		t.Fatalf("the staging id still resolves to %q after the row went (err=%v)", msgID, err)
	}
}

func TestFileFetchEvent_UnreadableIndexDropsTheEvent(t *testing.T) {
	d, stub, _ := newSiblingReceiver(t)
	stub.mu.Lock()
	stub.stagingBody = []byte("some blob")
	stub.mu.Unlock()
	if err := d.store.Lock(); err != nil {
		t.Fatal(err)
	}
	fileFetchStateHandler(context.Background(), d)(backendapi.FileFetchEvent{MsgID: siblingTestStaging, Token: siblingTestToken, State: string(files.StateReady)})
	stub.mu.Lock()
	ids := append([]string(nil), stub.haomadIDs...)
	stub.mu.Unlock()
	if len(ids) != 0 {
		t.Fatalf("haomad was called with %v for an event whose owner could not be read", ids)
	}
}

func TestRunSendFile_SiblingBlobDroppedWhenItsOfferSendFails(t *testing.T) {
	stub := startSendFileStub(t)
	d := newSendFileTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000aaaa"
	preEstablishSession(t, d, peerID)
	if _, _, err := d.peerMeta.SetFingerprint(peerID, railFingerprint); err != nil {
		t.Fatal(err)
	}
	linkSibling(t, d, railSiblingID)
	stub.mu.Lock()
	stub.failSendTo = railSiblingID
	stub.mu.Unlock()
	dc, err := d.chats.GetByDirectPeer(peerID)
	if err != nil {
		t.Fatal(err)
	}
	res, code, err := runSendFile(context.Background(), d, dc, peerID, writeTempFile(t, []byte("refused\n")), "", "")
	if err != nil {
		t.Fatalf("runSendFile: code=%q err=%v", code, err)
	}
	stub.mu.Lock()
	stages := append([]backendapi.StageFileRequest(nil), stub.stages...)
	drops := append([]string(nil), stub.dropIDs...)
	sentTo := append([]string(nil), stub.sentPeerIDs...)
	stub.mu.Unlock()
	tried := false
	for _, to := range sentTo {
		tried = tried || to == railSiblingID
	}
	if len(stages) != 2 || !tried {
		t.Fatalf("precondition: stages=%d sibling send tried=%t", len(stages), tried)
	}
	if len(drops) != 1 || drops[0] != stages[1].MsgID {
		t.Fatalf("drops = %v, want the sibling's blob %s alone", drops, stages[1].MsgID)
	}
	meta, err := d.files.GetMeta(res.MsgID)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.SiblingStagingIDs) != 0 {
		t.Fatalf("meta still names the dropped blob: %v", meta.SiblingStagingIDs)
	}
}

func TestFinalizeSiblingFetch_EveryCrashPointIsRepairedByTheSweep(t *testing.T) {
	for _, step := range []string{"sealed", "meta_ready", "row_ready"} {
		t.Run(step, func(t *testing.T) {
			d, stub, chatID := newSiblingReceiver(t)
			f := newSiblingFile(t)
			w := siblingOfferWrapper(t, f, "")
			ctx := context.Background()
			processInboxEntry(ctx, d, railEntry(railSiblingID, "env-sib-crash", sealSiblingOffer(t, d, w, offerFor(f))))
			stub.mu.Lock()
			stub.stagingBody = f.ciphertext
			stub.fetchState = string(files.StateReady)
			stub.mu.Unlock()

			crash := errors.New("crashed at " + step)
			prev := siblingFinalizeCheckpoint
			siblingFinalizeCheckpoint = func(at string) error {
				if at == step {
					return crash
				}
				return nil
			}
			err := finalizeSiblingFetch(ctx, d, w.MsgID)
			siblingFinalizeCheckpoint = prev
			if !errors.Is(err, crash) {
				t.Fatalf("precondition: the seal did not stop at %s (err=%v)", step, err)
			}

			sweepSiblingFetches(ctx, d)

			got, err := d.files.UnsealAtRest(chatID, w.MsgID)
			if err != nil || !bytes.Equal(got, f.plaintext) {
				t.Fatalf("no sealed copy of the file after the sweep: %v", err)
			}
			meta, err := d.files.GetMeta(w.MsgID)
			if err != nil || meta.State != files.StateReady {
				t.Fatalf("metadata after the sweep: state=%s err=%v, want ready", meta.State, err)
			}
			if _, body := fileRow(t, d, chatID, w.MsgID); body.State != string(files.StateReady) {
				t.Fatalf("row after the sweep = %q, want ready", body.State)
			}
			if _, err := d.files.GetSiblingOffer(w.MsgID); !errors.Is(err, files.ErrSiblingOfferNotFound) {
				t.Fatalf("the key outlived the repaired seal (err=%v)", err)
			}
		})
	}
}

func TestFinalizeSiblingFetch_ADeleteDuringTheSealStays(t *testing.T) {
	d, stub, chatID := newSiblingReceiver(t)
	f := newSiblingFile(t)
	w := siblingOfferWrapper(t, f, "")
	ctx := context.Background()
	processInboxEntry(ctx, d, railEntry(railSiblingID, "env-sib-del-seal", sealSiblingOffer(t, d, w, offerFor(f))))
	stub.mu.Lock()
	stub.stagingBody = f.ciphertext
	stub.mu.Unlock()

	prev := siblingFinalizeCheckpoint
	siblingFinalizeCheckpoint = func(at string) error {
		if at == "sealed" {
			if _, err := d.files.UnsealAtRest(chatID, w.MsgID); err != nil {
				t.Errorf("precondition: no sealed copy at the checkpoint: %v", err)
			}
			return d.files.DeleteMeta(w.MsgID)
		}
		return nil
	}
	err := finalizeSiblingFetch(ctx, d, w.MsgID)
	siblingFinalizeCheckpoint = prev
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if _, err := d.files.GetMeta(w.MsgID); !errors.Is(err, files.ErrMetaNotFound) {
		t.Fatalf("the deleted file's metadata came back (err=%v)", err)
	}
	if _, err := d.files.UnsealAtRest(chatID, w.MsgID); !errors.Is(err, files.ErrSealedNotFound) {
		t.Fatalf("the deleted file's sealed copy stayed on disk (err=%v)", err)
	}
}
