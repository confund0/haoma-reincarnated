package pair_test

import (
	"bytes"
	"errors"
	"testing"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/pair"
	"haoma-frontend/internal/store"
)

func init() {
	store.DefaultKDFParams = store.KDFParams{
		Time: 1, Memory: 8 * 1024, Threads: 2, KeyLen: 32, SaltLen: 16,
	}
}

func newSecretStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Lock() })
	return st
}

func mkKeys(peerID string, fill byte) *pair.MyKeys {
	return &pair.MyKeys{
		PeerID:         peerID,
		OutboundSecret: bytes.Repeat([]byte{fill}, pair.SecretLen),
	}
}

func mkMinted(addrSuffix string) backendapi.MintedOnion {
	return backendapi.MintedOnion{
		Address:    "myonion-" + addrSuffix,
		PrivateKey: "ZmFrZS1iYXNlNjQta2V5",
	}
}

func TestSaveMyKeys_LoadMyKeys_RoundTrip(t *testing.T) {
	st := newSecretStore(t)
	want := mkKeys("peer-aaa", 0xAB)
	wantMint := mkMinted("aaa")
	if err := pair.SaveMyKeys(st, "handle-1", want, wantMint); err != nil {
		t.Fatal(err)
	}
	got, gotMint, err := pair.LoadMyKeys(st, "handle-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.PeerID != want.PeerID {
		t.Errorf("PeerID drift: got %q, want %q", got.PeerID, want.PeerID)
	}
	if !bytes.Equal(got.OutboundSecret, want.OutboundSecret) {
		t.Errorf("OutboundSecret round-trip drift")
	}
	if gotMint.Address != wantMint.Address || gotMint.PrivateKey != wantMint.PrivateKey {
		t.Errorf("MintedOnion drift: got %+v, want %+v", gotMint, wantMint)
	}
}

func TestSaveMyKeys_OverwritesExisting(t *testing.T) {
	st := newSecretStore(t)
	first := mkKeys("peer-aaa", 0x11)
	second := mkKeys("peer-bbb", 0x22)
	if err := pair.SaveMyKeys(st, "handle-1", first, mkMinted("first")); err != nil {
		t.Fatal(err)
	}
	if err := pair.SaveMyKeys(st, "handle-1", second, mkMinted("second")); err != nil {
		t.Fatal(err)
	}
	got, gotMint, _ := pair.LoadMyKeys(st, "handle-1")
	if got.PeerID != second.PeerID || !bytes.Equal(got.OutboundSecret, second.OutboundSecret) {
		t.Error("second SaveMyKeys didn't overwrite first")
	}
	if gotMint.Address != "myonion-second" {
		t.Errorf("minted overwrite: got %q", gotMint.Address)
	}
}

func TestLoadMyKeys_MissingHandle_ErrMyKeysNotFound(t *testing.T) {
	st := newSecretStore(t)
	_, _, err := pair.LoadMyKeys(st, "ghost-handle")
	if !errors.Is(err, pair.ErrMyKeysNotFound) {
		t.Errorf("err = %v, want ErrMyKeysNotFound", err)
	}
}

func TestSaveMyKeys_RejectsWrongLength(t *testing.T) {
	st := newSecretStore(t)
	bad := &pair.MyKeys{PeerID: "abc", OutboundSecret: []byte{1, 2, 3}}
	if err := pair.SaveMyKeys(st, "h", bad, mkMinted("x")); err == nil {
		t.Error("expected error on short secret")
	}
}

func TestSaveMyKeys_RejectsEmptyHandle(t *testing.T) {
	st := newSecretStore(t)
	if err := pair.SaveMyKeys(st, "", mkKeys("p", 0), mkMinted("x")); err == nil {
		t.Error("expected error on empty handle")
	}
}

func TestSaveMyKeys_RejectsEmptyMinted(t *testing.T) {
	st := newSecretStore(t)
	if err := pair.SaveMyKeys(st, "h", mkKeys("p", 0xCC), backendapi.MintedOnion{}); err == nil {
		t.Error("expected error on empty MintedOnion")
	}
}

func TestSaveMyKeys_PerHandleSeparate(t *testing.T) {
	st := newSecretStore(t)
	a := mkKeys("p-a", 0xAA)
	b := mkKeys("p-b", 0xBB)
	if err := pair.SaveMyKeys(st, "h-alice", a, mkMinted("a")); err != nil {
		t.Fatal(err)
	}
	if err := pair.SaveMyKeys(st, "h-bob", b, mkMinted("b")); err != nil {
		t.Fatal(err)
	}
	gotA, _, _ := pair.LoadMyKeys(st, "h-alice")
	gotB, _, _ := pair.LoadMyKeys(st, "h-bob")
	if gotA.PeerID != a.PeerID || gotB.PeerID != b.PeerID {
		t.Errorf("per-handle keys crossed")
	}
}

func TestDeleteMyKeys_Idempotent(t *testing.T) {
	st := newSecretStore(t)
	if err := pair.SaveMyKeys(st, "h", mkKeys("p", 0xAB), mkMinted("x")); err != nil {
		t.Fatal(err)
	}
	if err := pair.DeleteMyKeys(st, "h"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := pair.LoadMyKeys(st, "h"); !errors.Is(err, pair.ErrMyKeysNotFound) {
		t.Errorf("after Delete: err = %v, want ErrMyKeysNotFound", err)
	}

	if err := pair.DeleteMyKeys(st, "h"); err != nil {
		t.Errorf("second Delete: %v, want nil", err)
	}
}
