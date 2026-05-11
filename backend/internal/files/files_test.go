package files_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"haoma/internal/files"
	"haoma/internal/store"
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
	testMsgA = "0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a"
	testMsgB = "0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b"
	peerA    = "11111111111111111111111111111111"
	peerB    = "22222222222222222222222222222222"
	peerC    = "33333333333333333333333333333333"
)

func TestStageBlob_WritesBlobAndMintsTokens(t *testing.T) {
	mgr, _, dir := newManager(t)

	ct := []byte("encrypted-bytes-go-here")
	tokens, err := mgr.StageBlob(testMsgA, ct, []string{peerA, peerB}, 0)
	if err != nil {
		t.Fatalf("StageBlob: %v", err)
	}
	if len(tokens) != 2 {
		t.Fatalf("tokens: got %d, want 2", len(tokens))
	}
	if tokens[0] == tokens[1] {
		t.Fatalf("tokens collide")
	}

	blobPath := filepath.Join(dir, "files", testMsgA)
	got, err := os.ReadFile(blobPath)
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	if !bytes.Equal(got, ct) {
		t.Fatalf("blob bytes diverged")
	}
	st, err := os.Stat(blobPath)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("blob perm = %v, want 0o600", st.Mode().Perm())
	}

	row0, err := mgr.LookupToken(tokens[0])
	if err != nil {
		t.Fatalf("LookupToken[0]: %v", err)
	}
	if row0.RecipientPeerID != peerA || row0.MsgID != testMsgA {
		t.Errorf("row[0] = %+v", row0)
	}
	if row0.ReceiptsRemaining != 1 {
		t.Errorf("row[0].ReceiptsRemaining = %d, want 1", row0.ReceiptsRemaining)
	}
	row1, err := mgr.LookupToken(tokens[1])
	if err != nil {
		t.Fatalf("LookupToken[1]: %v", err)
	}
	if row1.RecipientPeerID != peerB {
		t.Errorf("row[1].RecipientPeerID = %q, want %q", row1.RecipientPeerID, peerB)
	}
}

func TestStageBlob_RejectsRestage(t *testing.T) {
	mgr, _, _ := newManager(t)

	ct := []byte("first")
	if _, err := mgr.StageBlob(testMsgA, ct, []string{peerA}, 0); err != nil {
		t.Fatal(err)
	}
	_, err := mgr.StageBlob(testMsgA, ct, []string{peerB}, 0)
	if !errors.Is(err, files.ErrMsgIDInUse) {
		t.Fatalf("re-stage err: got %v, want ErrMsgIDInUse", err)
	}
}

func TestStageBlob_RejectsTooLarge(t *testing.T) {
	mgr, _, _ := newManager(t)
	huge := make([]byte, files.MaxBlobBytes+1)
	_, err := mgr.StageBlob(testMsgA, huge, []string{peerA}, 0)
	if !errors.Is(err, files.ErrCiphertextTooLong) {
		t.Fatalf("err: got %v, want ErrCiphertextTooLong", err)
	}
}

func TestStageBlob_RejectsEmpty(t *testing.T) {
	mgr, _, _ := newManager(t)
	if _, err := mgr.StageBlob("", []byte("x"), []string{peerA}, 0); err == nil {
		t.Fatal("empty msg_id: want error")
	}
	if _, err := mgr.StageBlob(testMsgA, nil, []string{peerA}, 0); err == nil {
		t.Fatal("empty ciphertext: want error")
	}
	if _, err := mgr.StageBlob(testMsgA, []byte("x"), nil, 0); err == nil {
		t.Fatal("nil recipients: want error")
	}
	if _, err := mgr.StageBlob(testMsgA, []byte("x"), []string{""}, 0); err == nil {
		t.Fatal("empty peer id: want error")
	}
}

func TestLookupToken_Unknown(t *testing.T) {
	mgr, _, _ := newManager(t)
	if _, err := mgr.LookupToken("does-not-exist"); !errors.Is(err, files.ErrTokenNotFound) {
		t.Fatalf("err: got %v, want ErrTokenNotFound", err)
	}
	if _, err := mgr.LookupToken(""); !errors.Is(err, files.ErrTokenNotFound) {
		t.Fatalf("empty token err: got %v, want ErrTokenNotFound", err)
	}
}

func TestDecrementReceipts_DownToInvalidated(t *testing.T) {
	mgr, _, _ := newManager(t)
	tokens, err := mgr.StageBlob(testMsgA, []byte("x"), []string{peerA}, 0)
	if err != nil {
		t.Fatal(err)
	}
	tok := tokens[0]
	if err := mgr.DecrementReceipts(tok); err != nil {
		t.Fatalf("Decrement: %v", err)
	}
	row, err := mgr.LookupToken(tok)
	if !errors.Is(err, files.ErrTokenInvalidated) {
		t.Fatalf("err: got %v, want ErrTokenInvalidated", err)
	}
	if row.MsgID != testMsgA {
		t.Errorf("invalidated row body lost: %+v", row)
	}

	if err := mgr.DecrementReceipts(tok); err != nil {
		t.Fatalf("second Decrement: %v", err)
	}
	if _, err := mgr.LookupToken(tok); !errors.Is(err, files.ErrTokenInvalidated) {
		t.Fatalf("err: got %v, want ErrTokenInvalidated", err)
	}
}

func TestDecrementReceipts_Unknown(t *testing.T) {
	mgr, _, _ := newManager(t)
	if err := mgr.DecrementReceipts("nope"); !errors.Is(err, files.ErrTokenNotFound) {
		t.Fatalf("err: got %v, want ErrTokenNotFound", err)
	}
}

func TestOpenBlob_RoundTrip(t *testing.T) {
	mgr, _, _ := newManager(t)
	ct := []byte("the bytes of a small file")
	if _, err := mgr.StageBlob(testMsgA, ct, []string{peerA}, 0); err != nil {
		t.Fatal(err)
	}
	f, size, err := mgr.OpenBlob(testMsgA)
	if err != nil {
		t.Fatalf("OpenBlob: %v", err)
	}
	defer f.Close()
	if size != int64(len(ct)) {
		t.Errorf("size = %d, want %d", size, len(ct))
	}
	got := make([]byte, len(ct))
	if _, err := f.Read(got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, ct) {
		t.Fatalf("bytes diverged")
	}
}

func TestOpenBlob_Unknown(t *testing.T) {
	mgr, _, _ := newManager(t)
	if _, _, err := mgr.OpenBlob("does-not-exist"); !errors.Is(err, files.ErrBlobNotFound) {
		t.Fatalf("err: got %v, want ErrBlobNotFound", err)
	}
}

func TestDropByMsgID_RemovesBlobAndAllTokens(t *testing.T) {
	mgr, _, dir := newManager(t)

	tokens, err := mgr.StageBlob(testMsgA, []byte("x"), []string{peerA, peerB, peerC}, 0)
	if err != nil {
		t.Fatal(err)
	}

	if err := mgr.DropByMsgID(testMsgA); err != nil {
		t.Fatalf("DropByMsgID: %v", err)
	}
	for _, tok := range tokens {
		if _, err := mgr.LookupToken(tok); !errors.Is(err, files.ErrTokenNotFound) {
			t.Errorf("token %s: err = %v, want ErrTokenNotFound", tok, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "files", testMsgA)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("blob still present after drop: err = %v", err)
	}

	if _, err := mgr.StageBlob(testMsgA, []byte("y"), []string{peerA}, 0); err != nil {
		t.Errorf("re-stage after drop: %v", err)
	}
}

func TestDropByMsgID_IdempotentOnUnknown(t *testing.T) {
	mgr, _, _ := newManager(t)
	if err := mgr.DropByMsgID(testMsgB); err != nil {
		t.Fatalf("DropByMsgID on unknown: %v", err)
	}
}

func TestStageBlob_OnlyAffectsItsOwnTokens(t *testing.T) {
	mgr, _, _ := newManager(t)

	tokensA, err := mgr.StageBlob(testMsgA, []byte("a"), []string{peerA, peerB}, 0)
	if err != nil {
		t.Fatal(err)
	}
	tokensB, err := mgr.StageBlob(testMsgB, []byte("b"), []string{peerC}, 0)
	if err != nil {
		t.Fatal(err)
	}

	if err := mgr.DropByMsgID(testMsgA); err != nil {
		t.Fatal(err)
	}
	for _, tok := range tokensA {
		if _, err := mgr.LookupToken(tok); !errors.Is(err, files.ErrTokenNotFound) {
			t.Errorf("A-side token leaked: %v", err)
		}
	}
	for _, tok := range tokensB {
		row, err := mgr.LookupToken(tok)
		if err != nil {
			t.Errorf("B-side token wiped: %v", err)
		}
		if row.MsgID != testMsgB {
			t.Errorf("B-side row corrupted: %+v", row)
		}
	}
}

func TestNewManager_Errors(t *testing.T) {
	if _, err := files.NewManager(nil, t.TempDir()); err == nil {
		t.Error("nil store: want error")
	}
	dir := t.TempDir()
	st, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Lock() })
	if _, err := files.NewManager(st, ""); err == nil {
		t.Error("empty data dir: want error")
	}
}
