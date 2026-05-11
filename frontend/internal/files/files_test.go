package files_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/chacha20poly1305"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/files"
	"haoma-frontend/internal/store"
)

func init() {
	store.DefaultKDFParams = store.KDFParams{
		Time: 1, Memory: 8 * 1024, Threads: 2, KeyLen: 32, SaltLen: 16,
	}
}

func newManager(t *testing.T) (*files.Manager, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Lock() })
	mgr, err := files.NewManager(st, dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return mgr, st, dir
}

const (
	testChatID = chat.ChatID("aabbccddeeff00112233445566778899")
	testMsgA   = "0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a"
	testMsgB   = "0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b"
)

func TestNewManager_BootstrapsAndPersistsMaster(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	mgr1, err := files.NewManager(st, dir)
	if err != nil {
		t.Fatalf("first NewManager: %v", err)
	}

	plaintext := []byte("a private message")
	if _, err := mgr1.SealAtRest(testChatID, testMsgA, plaintext); err != nil {
		t.Fatalf("SealAtRest: %v", err)
	}
	if err := st.Lock(); err != nil {
		t.Fatal(err)
	}
	st2, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Lock()
	mgr2, err := files.NewManager(st2, dir)
	if err != nil {
		t.Fatalf("second NewManager: %v", err)
	}
	got, err := mgr2.UnsealAtRest(testChatID, testMsgA)
	if err != nil {
		t.Fatalf("UnsealAtRest after re-open: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Errorf("unseal got %q, want %q", got, plaintext)
	}
}

func TestSealAtRest_RoundTrip(t *testing.T) {
	mgr, _, _ := newManager(t)
	plaintext := []byte("the quick brown fox jumps over the lazy dog")
	path, err := mgr.SealAtRest(testChatID, testMsgA, plaintext)
	if err != nil {
		t.Fatalf("SealAtRest: %v", err)
	}
	if filepath.Base(path) != testMsgA {
		t.Errorf("sealed filename = %q, want %q (random name should equal msg id)", filepath.Base(path), testMsgA)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("perms = %o, want 0600", st.Mode().Perm())
	}
	got, err := mgr.UnsealAtRest(testChatID, testMsgA)
	if err != nil {
		t.Fatalf("UnsealAtRest: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Errorf("got %q, want %q", got, plaintext)
	}
}

func TestSealAtRest_TamperedCiphertextFailsUnseal(t *testing.T) {
	mgr, _, _ := newManager(t)
	path, err := mgr.SealAtRest(testChatID, testMsgA, []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	raw[40] ^= 0x01
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.UnsealAtRest(testChatID, testMsgA); err == nil {
		t.Fatal("expected unseal failure on tampered ciphertext, got nil")
	}
}

func TestUnsealAtRest_RejectsCrossMsgIDOpen(t *testing.T) {
	mgr, _, _ := newManager(t)
	plaintext := []byte("bound to msgA")
	pathA, err := mgr.SealAtRest(testChatID, testMsgA, plaintext)
	if err != nil {
		t.Fatal(err)
	}

	chatDir := filepath.Dir(pathA)
	pathB := filepath.Join(chatDir, testMsgB)
	raw, err := os.ReadFile(pathA)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathB, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.UnsealAtRest(testChatID, testMsgB); err == nil {
		t.Fatal("expected unseal failure when sealed bytes are moved between rows")
	}
}

func TestSealAtRest_RejectsOversize(t *testing.T) {
	mgr, _, _ := newManager(t)
	big := make([]byte, files.MaxPlaintextBytes+1)
	if _, err := mgr.SealAtRest(testChatID, testMsgA, big); err == nil || !strings.Contains(err.Error(), "exceeds cap") {
		t.Fatalf("err = %v, want oversize rejection", err)
	}
}

func TestSealAtRest_RejectsNonHexIDs(t *testing.T) {
	mgr, _, _ := newManager(t)
	if _, err := mgr.SealAtRest(chat.ChatID("../escape"), testMsgA, []byte("x")); err == nil {
		t.Fatal("expected error for non-hex chat id")
	}
	if _, err := mgr.SealAtRest(testChatID, "../escape", []byte("x")); err == nil {
		t.Fatal("expected error for non-hex msg id")
	}
}

func TestUnsealAtRest_NotFound(t *testing.T) {
	mgr, _, _ := newManager(t)
	if _, err := mgr.UnsealAtRest(testChatID, testMsgA); !errors.Is(err, files.ErrSealedNotFound) {
		t.Fatalf("err = %v, want ErrSealedNotFound", err)
	}
}

func TestDeleteSealed_IdempotentOnMissing(t *testing.T) {
	mgr, _, _ := newManager(t)
	if err := mgr.DeleteSealed(testChatID, testMsgA); err != nil {
		t.Errorf("DeleteSealed on missing file should be a no-op, got %v", err)
	}
}

func TestDeleteSealedByChat_RemovesAllUnderChatDir(t *testing.T) {
	mgr, _, dir := newManager(t)
	for _, m := range []string{testMsgA, testMsgB} {
		if _, err := mgr.SealAtRest(testChatID, m, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := mgr.DeleteSealedByChat(testChatID); err != nil {
		t.Fatalf("DeleteSealedByChat: %v", err)
	}
	chatDir := filepath.Join(dir, files.SubdirName, string(testChatID))
	if _, err := os.Stat(chatDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("chat dir still exists: %v", err)
	}
}

func TestPutMeta_GetMeta_RoundTrip(t *testing.T) {
	mgr, _, _ := newManager(t)
	now := time.Now().Unix()
	in := files.Metadata{
		MsgID:            testMsgA,
		ChatID:           testChatID,
		Direction:        files.DirOut,
		Token:            "Q5oF1aQpJ1xVbwVN4f6PcWcL4lWCNW5kdeJ-l7Q9OVo",
		OriginalName:     "report.pdf",
		Mime:             "application/pdf",
		Size:             1024,
		Sha256Ciphertext: "deadbeef",
		BlobPath:         "/var/lib/haomad/blobs/abc",
		State:            files.StatePending,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := mgr.PutMeta(in); err != nil {
		t.Fatalf("PutMeta: %v", err)
	}
	got, err := mgr.GetMeta(testMsgA)
	if err != nil {
		t.Fatalf("GetMeta: %v", err)
	}
	want, _ := json.Marshal(in)
	have, _ := json.Marshal(got)
	if string(want) != string(have) {
		t.Errorf("metadata drift\n got %s\nwant %s", have, want)
	}
}

func TestGetMeta_NotFound(t *testing.T) {
	mgr, _, _ := newManager(t)
	if _, err := mgr.GetMeta(testMsgA); !errors.Is(err, files.ErrMetaNotFound) {
		t.Fatalf("err = %v, want ErrMetaNotFound", err)
	}
}

func TestDeleteMeta_RemovesPrimaryAndIndex(t *testing.T) {
	mgr, _, _ := newManager(t)
	if err := mgr.PutMeta(files.Metadata{MsgID: testMsgA, ChatID: testChatID, State: files.StateReady}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.PutMeta(files.Metadata{MsgID: testMsgB, ChatID: testChatID, State: files.StateReady}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.DeleteMeta(testMsgA); err != nil {
		t.Fatalf("DeleteMeta: %v", err)
	}
	if _, err := mgr.GetMeta(testMsgA); !errors.Is(err, files.ErrMetaNotFound) {
		t.Errorf("primary still readable: %v", err)
	}

	n, err := mgr.DeleteMetaByChat(testChatID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("DeleteMetaByChat removed %d, want 1 (msgA index should already be gone)", n)
	}
}

func TestDeleteMeta_IdempotentOnMissing(t *testing.T) {
	mgr, _, _ := newManager(t)
	if err := mgr.DeleteMeta(testMsgA); err != nil {
		t.Errorf("DeleteMeta on missing should be no-op, got %v", err)
	}
}

func TestListByChat_ReturnsAllMetadataForChat(t *testing.T) {
	mgr, _, _ := newManager(t)
	const otherChat = chat.ChatID("ffeeddccbbaa99887766554433221100")
	a := files.Metadata{MsgID: testMsgA, ChatID: testChatID, OriginalName: "a.bin", State: files.StateReady}
	b := files.Metadata{MsgID: testMsgB, ChatID: testChatID, OriginalName: "b.bin", State: files.StateDownloading}
	other := files.Metadata{MsgID: "ffffffffffffffffffffffffffffffff", ChatID: otherChat, OriginalName: "other.bin", State: files.StateReady}
	for _, m := range []files.Metadata{a, b, other} {
		if err := mgr.PutMeta(m); err != nil {
			t.Fatalf("PutMeta(%s): %v", m.MsgID, err)
		}
	}
	got, err := mgr.ListByChat(testChatID)
	if err != nil {
		t.Fatalf("ListByChat: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListByChat returned %d rows, want 2", len(got))
	}
	seen := map[string]string{}
	for _, m := range got {
		seen[m.MsgID] = m.OriginalName
	}
	if seen[testMsgA] != "a.bin" || seen[testMsgB] != "b.bin" {
		t.Errorf("unexpected rows: %+v", seen)
	}
	if _, leaked := seen["ffffffffffffffffffffffffffffffff"]; leaked {
		t.Errorf("ListByChat leaked rows from other chat")
	}
}

func TestListByChat_EmptyChat(t *testing.T) {
	mgr, _, _ := newManager(t)
	got, err := mgr.ListByChat(testChatID)
	if err != nil {
		t.Fatalf("ListByChat on empty chat: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 rows, got %d", len(got))
	}
}

func TestGetMetaByToken_PrimaryAndRecipientTokens(t *testing.T) {
	mgr, _, _ := newManager(t)
	const (
		tokenA = "Q5oF1aQpJ1xVbwVN4f6PcWcL4lWCNW5kdeJ-l7Q9OVo"
		tokenB = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		tokenC = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	)
	in := files.Metadata{
		MsgID:     testMsgA,
		ChatID:    testChatID,
		Direction: files.DirOut,
		Token:     tokenA,
		RecipientTokens: map[string]string{
			"peer-bob":   tokenA,
			"peer-carol": tokenB,
			"peer-dave":  tokenC,
		},
		Size:             10,
		Sha256Ciphertext: "deadbeef",
		State:            files.StatePending,
		CreatedAt:        time.Now().Unix(),
	}
	if err := mgr.PutMeta(in); err != nil {
		t.Fatalf("PutMeta: %v", err)
	}
	for _, tok := range []string{tokenA, tokenB, tokenC} {
		got, err := mgr.GetMetaByToken(tok)
		if err != nil {
			t.Fatalf("GetMetaByToken(%q) failed: %v", tok, err)
		}
		if got.MsgID != testMsgA {
			t.Errorf("GetMetaByToken(%q) returned msg_id %q, want %q", tok, got.MsgID, testMsgA)
		}
	}
	if err := mgr.DeleteMeta(testMsgA); err != nil {
		t.Fatalf("DeleteMeta: %v", err)
	}
	for _, tok := range []string{tokenA, tokenB, tokenC} {
		if _, err := mgr.GetMetaByToken(tok); !errors.Is(err, files.ErrMetaNotFound) {
			t.Errorf("GetMetaByToken(%q) after delete: err=%v, want ErrMetaNotFound", tok, err)
		}
	}
}

func TestGetMetaByToken_UnknownToken(t *testing.T) {
	mgr, _, _ := newManager(t)
	if _, err := mgr.GetMetaByToken("never-minted"); !errors.Is(err, files.ErrMetaNotFound) {
		t.Fatalf("err = %v, want ErrMetaNotFound", err)
	}
}

func TestDecryptSealMove_RoundTrip(t *testing.T) {
	mgr, _, _ := newManager(t)
	plaintext := []byte("the quick brown fox jumps over the lazy dog")
	key, nonce, ciphertext := encryptForTest(t, plaintext)

	if _, err := mgr.WriteStaging(testMsgA, ciphertext); err != nil {
		t.Fatalf("WriteStaging: %v", err)
	}
	sealedPath, err := mgr.DecryptSealMove(testChatID, testMsgA, key, nonce)
	if err != nil {
		t.Fatalf("DecryptSealMove: %v", err)
	}
	if sealedPath == "" {
		t.Fatal("empty sealed path")
	}

	got, err := mgr.UnsealAtRest(testChatID, testMsgA)
	if err != nil {
		t.Fatalf("UnsealAtRest: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Errorf("plaintext drift\n got %q\nwant %q", got, plaintext)
	}

	stagingPath, _ := mgr.StagingPath(testMsgA)
	if _, err := os.Stat(stagingPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("staging still exists at %s: %v", stagingPath, err)
	}
}

func TestDecryptSealMove_WrongKey_ReturnsAEADErr(t *testing.T) {
	mgr, _, _ := newManager(t)
	plaintext := []byte("secret bytes")
	_, nonce, ciphertext := encryptForTest(t, plaintext)
	wrongKey := make([]byte, 32)

	if _, err := mgr.WriteStaging(testMsgA, ciphertext); err != nil {
		t.Fatal(err)
	}
	_, err := mgr.DecryptSealMove(testChatID, testMsgA, wrongKey, nonce)
	if !errors.Is(err, files.ErrAEADOpen) {
		t.Fatalf("err = %v, want ErrAEADOpen", err)
	}

	if _, err := mgr.UnsealAtRest(testChatID, testMsgA); !errors.Is(err, files.ErrSealedNotFound) {
		t.Errorf("sealed file written despite AEAD failure: err=%v", err)
	}
}

func TestDecryptSealMove_StagingMissing(t *testing.T) {
	mgr, _, _ := newManager(t)
	key, nonce, _ := encryptForTest(t, []byte("anything"))
	_, err := mgr.DecryptSealMove(testChatID, testMsgA, key, nonce)
	if !errors.Is(err, files.ErrSealedNotFound) {
		t.Fatalf("err = %v, want ErrSealedNotFound", err)
	}
}

func TestDecryptSealMove_RejectsBadKeyOrNonce(t *testing.T) {
	mgr, _, _ := newManager(t)
	if _, err := mgr.DecryptSealMove(testChatID, testMsgA, make([]byte, 16), make([]byte, 24)); err == nil {
		t.Error("short key accepted")
	}
	if _, err := mgr.DecryptSealMove(testChatID, testMsgA, make([]byte, 32), make([]byte, 12)); err == nil {
		t.Error("short nonce accepted")
	}
}

func encryptForTest(t *testing.T, plaintext []byte) (key, nonce, ciphertext []byte) {
	t.Helper()
	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand key: %v", err)
	}
	nonce = make([]byte, 24)
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

func TestJanitor_HandlesRetentionDeletion(t *testing.T) {
	mgr, st, _ := newManager(t)
	bus := events.NewBus()
	now := int64(1742643890)
	log := events.New(st, bus, func() time.Time { return time.Unix(now, 0) })
	jan := files.NewJanitor(mgr, bus, nil, nil)

	if _, err := mgr.SealAtRest(testChatID, testMsgA, []byte("retention victim")); err != nil {
		t.Fatal(err)
	}
	if err := mgr.PutMeta(files.Metadata{
		MsgID: testMsgA, ChatID: testChatID, Direction: files.DirOut, State: files.StateReady,
	}); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"name": "blob.bin"})

	if _, err := log.AppendOutbound(events.OutboundParams{
		ChatID:        testChatID,
		Kind:          events.KindFile,
		SenderSeq:     1,
		EnvelopeID:    "env-S",
		MsgID:         testMsgA,
		ExpireSeconds: 1,
		Body:          body,
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go jan.Run(ctx)
	defer cancel()
	waitForSubscribers(t, bus, 1, 1)

	if _, err := log.SweepExpired(now + 10); err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := mgr.GetMeta(testMsgA); errors.Is(err, files.ErrMetaNotFound) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := mgr.GetMeta(testMsgA); !errors.Is(err, files.ErrMetaNotFound) {
		t.Errorf("metadata still present after sweep: %v", err)
	}
	if _, err := mgr.UnsealAtRest(testChatID, testMsgA); !errors.Is(err, files.ErrSealedNotFound) {
		t.Errorf("sealed file still present after sweep: %v", err)
	}
}

func TestJanitor_HandlesTombstoneEvent(t *testing.T) {
	mgr, st, _ := newManager(t)
	bus := events.NewBus()
	log := events.New(st, bus, nil)
	jan := files.NewJanitor(mgr, bus, nil, nil)

	if _, err := mgr.SealAtRest(testChatID, testMsgA, []byte("tombstone victim")); err != nil {
		t.Fatal(err)
	}
	if err := mgr.PutMeta(files.Metadata{
		MsgID: testMsgA, ChatID: testChatID, Direction: files.DirOut, State: files.StateReady,
	}); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"name": "blob.bin"})
	if _, err := log.AppendOutbound(events.OutboundParams{
		ChatID:     testChatID,
		Kind:       events.KindFile,
		SenderSeq:  1,
		EnvelopeID: "env-A",
		MsgID:      testMsgA,
		Body:       body,
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go jan.Run(ctx)
	defer cancel()
	waitForSubscribers(t, bus, 1, 1)

	if _, err := log.ApplyDelete(testMsgA, time.Now().Unix(), ""); err != nil {
		t.Fatalf("ApplyDelete: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := mgr.GetMeta(testMsgA); errors.Is(err, files.ErrMetaNotFound) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := mgr.GetMeta(testMsgA); !errors.Is(err, files.ErrMetaNotFound) {
		t.Errorf("metadata still present after tombstone: %v", err)
	}
	if _, err := mgr.UnsealAtRest(testChatID, testMsgA); !errors.Is(err, files.ErrSealedNotFound) {
		t.Errorf("sealed file still present after tombstone: %v", err)
	}
}

func TestJanitor_IgnoresNonFileTombstones(t *testing.T) {
	mgr, st, _ := newManager(t)
	bus := events.NewBus()
	log := events.New(st, bus, nil)
	jan := files.NewJanitor(mgr, bus, nil, nil)

	if _, err := mgr.SealAtRest(testChatID, testMsgA, []byte("preserved")); err != nil {
		t.Fatal(err)
	}
	if err := mgr.PutMeta(files.Metadata{
		MsgID: testMsgA, ChatID: testChatID, Direction: files.DirOut, State: files.StateReady,
	}); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(events.TextBody{Text: "irrelevant"})
	if _, err := log.AppendOutbound(events.OutboundParams{
		ChatID: testChatID, Kind: events.KindText, SenderSeq: 1,
		EnvelopeID: "env-T", MsgID: testMsgB, Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go jan.Run(ctx)
	defer cancel()
	waitForSubscribers(t, bus, 1, 1)

	if _, err := log.ApplyDelete(testMsgB, time.Now().Unix(), ""); err != nil {
		t.Fatalf("ApplyDelete on text row: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	if _, err := mgr.GetMeta(testMsgA); err != nil {
		t.Errorf("msgA metadata was wrongly cleaned: %v", err)
	}
	if _, err := mgr.UnsealAtRest(testChatID, testMsgA); err != nil {
		t.Errorf("msgA sealed file was wrongly cleaned: %v", err)
	}
}

type recordingDropper struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (r *recordingDropper) DropFile(_ context.Context, msgID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, msgID)
	return r.err
}

func (r *recordingDropper) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.calls))
	copy(out, r.calls)
	return out
}

func TestJanitor_OutboundCascadesToRemote(t *testing.T) {
	mgr, st, _ := newManager(t)
	bus := events.NewBus()
	log := events.New(st, bus, nil)
	rec := &recordingDropper{}
	jan := files.NewJanitor(mgr, bus, nil, rec)

	if _, err := mgr.SealAtRest(testChatID, testMsgA, []byte("outbound victim")); err != nil {
		t.Fatal(err)
	}
	if err := mgr.PutMeta(files.Metadata{
		MsgID: testMsgA, ChatID: testChatID, Direction: files.DirOut, State: files.StateReady,
	}); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"name": "blob.bin"})
	if _, err := log.AppendOutbound(events.OutboundParams{
		ChatID: testChatID, Kind: events.KindFile, SenderSeq: 1,
		EnvelopeID: "env-A", MsgID: testMsgA, Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go jan.Run(ctx)
	defer cancel()
	waitForSubscribers(t, bus, 1, 1)

	if _, err := log.ApplyDelete(testMsgA, time.Now().Unix(), ""); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if calls := rec.snapshot(); len(calls) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	calls := rec.snapshot()
	if len(calls) != 1 || calls[0] != testMsgA {
		t.Errorf("DropFile calls = %v, want [%s]", calls, testMsgA)
	}
}

func TestJanitor_InboundDoesNotCascade(t *testing.T) {
	mgr, st, _ := newManager(t)
	bus := events.NewBus()
	log := events.New(st, bus, nil)
	rec := &recordingDropper{}
	jan := files.NewJanitor(mgr, bus, nil, rec)

	if _, err := mgr.SealAtRest(testChatID, testMsgA, []byte("inbound victim")); err != nil {
		t.Fatal(err)
	}
	if err := mgr.PutMeta(files.Metadata{
		MsgID: testMsgA, ChatID: testChatID, Direction: files.DirIn, State: files.StateReady,
	}); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"name": "blob.bin"})

	if _, err := log.AppendOutbound(events.OutboundParams{
		ChatID: testChatID, Kind: events.KindFile, SenderSeq: 1,
		EnvelopeID: "env-B", MsgID: testMsgA, Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go jan.Run(ctx)
	defer cancel()
	waitForSubscribers(t, bus, 1, 1)

	if _, err := log.ApplyDelete(testMsgA, time.Now().Unix(), ""); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := mgr.GetMeta(testMsgA); errors.Is(err, files.ErrMetaNotFound) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if calls := rec.snapshot(); len(calls) != 0 {
		t.Errorf("DropFile calls = %v, want none for inbound row", calls)
	}
}

func TestJanitor_NilRemoteIsNoOp(t *testing.T) {
	mgr, st, _ := newManager(t)
	bus := events.NewBus()
	log := events.New(st, bus, nil)
	jan := files.NewJanitor(mgr, bus, nil, nil)

	if _, err := mgr.SealAtRest(testChatID, testMsgA, []byte("outbound, no remote")); err != nil {
		t.Fatal(err)
	}
	if err := mgr.PutMeta(files.Metadata{
		MsgID: testMsgA, ChatID: testChatID, Direction: files.DirOut, State: files.StateReady,
	}); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"name": "blob.bin"})
	if _, err := log.AppendOutbound(events.OutboundParams{
		ChatID: testChatID, Kind: events.KindFile, SenderSeq: 1,
		EnvelopeID: "env-C", MsgID: testMsgA, Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go jan.Run(ctx)
	defer cancel()
	waitForSubscribers(t, bus, 1, 1)

	if _, err := log.ApplyDelete(testMsgA, time.Now().Unix(), ""); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := mgr.GetMeta(testMsgA); errors.Is(err, files.ErrMetaNotFound) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := mgr.GetMeta(testMsgA); !errors.Is(err, files.ErrMetaNotFound) {
		t.Errorf("local metadata not cleaned with nil dropper: %v", err)
	}
}

func TestJanitor_StopsOnContextCancel(t *testing.T) {
	mgr, _, _ := newManager(t)
	bus := events.NewBus()
	jan := files.NewJanitor(mgr, bus, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		jan.Run(ctx)
		close(done)
	}()
	waitForSubscribers(t, bus, 1, 1)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("janitor did not stop on ctx cancel")
	}
	if bus.SubscriberCount() != 0 || bus.DeletionSubscriberCount() != 0 {
		t.Errorf("subs = (%d, %d), want (0, 0) after cancel",
			bus.SubscriberCount(), bus.DeletionSubscriberCount())
	}
}

func waitForSubscribers(t *testing.T, bus *events.Bus, evWant, delWant int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if bus.SubscriberCount() >= evWant && bus.DeletionSubscriberCount() >= delWant {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("janitor never attached: subs = (%d, %d), want >= (%d, %d)",
		bus.SubscriberCount(), bus.DeletionSubscriberCount(), evWant, delWant)
}

func TestWriteOpenTransient_RoundTrip(t *testing.T) {
	mgr, _, dir := newManager(t)
	plaintext := []byte("ready for viewer spawn")
	if _, err := mgr.SealAtRest(testChatID, testMsgA, plaintext); err != nil {
		t.Fatalf("SealAtRest: %v", err)
	}

	got, err := mgr.WriteOpenTransient(testChatID, testMsgA)
	if err != nil {
		t.Fatalf("WriteOpenTransient: %v", err)
	}
	want := filepath.Join(dir, files.SubdirName, files.OpenSubdir, testMsgA)
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}

	body, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("read transient: %v", err)
	}
	if string(body) != string(plaintext) {
		t.Errorf("plaintext = %q, want %q", body, plaintext)
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("perms = %v, want 0600", info.Mode().Perm())
	}
}

func TestWriteOpenTransient_OverwritesPrior(t *testing.T) {
	mgr, _, _ := newManager(t)
	if _, err := mgr.SealAtRest(testChatID, testMsgA, []byte("v1-shorter")); err != nil {
		t.Fatalf("SealAtRest v1: %v", err)
	}
	pathA, err := mgr.WriteOpenTransient(testChatID, testMsgA)
	if err != nil {
		t.Fatalf("WriteOpenTransient v1: %v", err)
	}

	if _, err := mgr.SealAtRest(testChatID, testMsgA, []byte("v2-much-longer")); err != nil {
		t.Fatalf("SealAtRest v2: %v", err)
	}
	pathB, err := mgr.WriteOpenTransient(testChatID, testMsgA)
	if err != nil {
		t.Fatalf("WriteOpenTransient v2: %v", err)
	}
	if pathA != pathB {
		t.Errorf("paths differ between calls: %q vs %q", pathA, pathB)
	}
	body, err := os.ReadFile(pathB)
	if err != nil {
		t.Fatalf("read transient: %v", err)
	}
	if string(body) != "v2-much-longer" {
		t.Errorf("plaintext = %q, want v2-much-longer (re-Open should replace)", body)
	}
}

func TestWriteOpenTransient_RejectsNonHexMsgID(t *testing.T) {
	mgr, _, _ := newManager(t)
	if _, err := mgr.WriteOpenTransient(testChatID, "../escape"); err == nil {
		t.Errorf("expected error for non-hex msg id")
	}
}

func TestWriteOpenTransient_FailsOnUnsealError(t *testing.T) {
	mgr, _, _ := newManager(t)
	if _, err := mgr.WriteOpenTransient(testChatID, testMsgA); !errors.Is(err, files.ErrSealedNotFound) {
		t.Errorf("err = %v, want ErrSealedNotFound", err)
	}
}

func TestWipeOpenTransient_NukesDir(t *testing.T) {
	mgr, _, dir := newManager(t)
	if _, err := mgr.SealAtRest(testChatID, testMsgA, []byte("x")); err != nil {
		t.Fatalf("SealAtRest: %v", err)
	}
	if _, err := mgr.WriteOpenTransient(testChatID, testMsgA); err != nil {
		t.Fatalf("WriteOpenTransient: %v", err)
	}

	if err := mgr.WipeOpenTransient(); err != nil {
		t.Fatalf("WipeOpenTransient: %v", err)
	}
	openDir := filepath.Join(dir, files.SubdirName, files.OpenSubdir)
	if _, err := os.Stat(openDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("open dir still exists after wipe: stat err = %v", err)
	}

	if err := mgr.WipeOpenTransient(); err != nil {
		t.Errorf("second WipeOpenTransient on missing dir: %v", err)
	}
}
