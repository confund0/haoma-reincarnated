package session_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"go.mau.fi/libsignal/keys/prekey"
	"go.mau.fi/libsignal/protocol"
	"go.mau.fi/libsignal/serialize"
	libsession "go.mau.fi/libsignal/session"
	"go.mau.fi/libsignal/util/optional"

	"haoma-frontend/internal/session"
	"haoma-frontend/internal/signal"
	"haoma-frontend/internal/store"
)

const (
	aliceID = "0000000000000000000000000000000a"
	bobID   = "0000000000000000000000000000000b"
)

func newStateAndStores(t *testing.T) (*signal.State, *signal.Stores) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Lock() })
	state, _, err := signal.LoadOrBootstrap(st, 5)
	if err != nil {
		t.Fatal(err)
	}
	return state, signal.NewStores(st, state)
}

func bundleFromState(t *testing.T, state *signal.State) *prekey.Bundle {
	t.Helper()
	if len(state.OneTimePreKeys) == 0 {
		t.Fatal("state has no one-time prekeys")
	}
	opk := state.OneTimePreKeys[0]
	id := opk.ID()
	if id.IsEmpty {
		t.Fatal("first OPK has empty id")
	}
	spkSig := state.SignedPreKey.Signature()
	return prekey.NewBundle(
		state.RegistrationID,
		session.DeviceID,
		optional.NewOptionalUint32(id.Value),
		state.SignedPreKey.ID(),
		opk.KeyPair().PublicKey(),
		state.SignedPreKey.KeyPair().PublicKey(),
		spkSig,
		state.IdentityKeyPair.PublicKey(),
	)
}

func processBundleFor(t *testing.T, mine *signal.Stores, peerID string, peerState *signal.State) {
	t.Helper()
	addr := protocol.NewSignalAddress(peerID, session.DeviceID)
	bundle := bundleFromState(t, peerState)
	ser := serialize.NewJSONSerializer()
	builder := libsession.NewBuilder(mine, mine, mine, mine, addr, ser)
	if err := builder.ProcessBundle(context.Background(), bundle); err != nil {
		t.Fatalf("ProcessBundle: %v", err)
	}
}

func pairAlice(t *testing.T) (alice, bob *signal.Stores) {
	t.Helper()
	_, alice = newStateAndStores(t)
	bState, bob := newStateAndStores(t)
	processBundleFor(t, alice, bobID, bState)
	return alice, bob
}

func TestRoundTrip_TypeTagTransition(t *testing.T) {
	ctx := context.Background()
	alice, bob := pairAlice(t)
	aCipher := session.New(alice)
	bCipher := session.New(bob)

	blob1, err := aCipher.Encrypt(ctx, bobID, []byte("hello"))
	if err != nil {
		t.Fatalf("encrypt #1: %v", err)
	}
	if got := blob1[0]; got != protocol.PREKEY_TYPE {
		t.Errorf("blob #1 tag = %d, want %d (PreKey)", got, protocol.PREKEY_TYPE)
	}
	if _, err := bCipher.Decrypt(ctx, aliceID, blob1); err != nil {
		t.Fatalf("bob decrypt #1: %v", err)
	}

	blob2, err := aCipher.Encrypt(ctx, bobID, []byte("you there"))
	if err != nil {
		t.Fatalf("encrypt #2: %v", err)
	}
	if got := blob2[0]; got != protocol.PREKEY_TYPE {
		t.Errorf("blob #2 tag = %d, want %d (still PreKey, no reply yet)", got, protocol.PREKEY_TYPE)
	}
	if _, err := bCipher.Decrypt(ctx, aliceID, blob2); err != nil {
		t.Fatalf("bob decrypt #2: %v", err)
	}

	reply, err := bCipher.Encrypt(ctx, aliceID, []byte("yes"))
	if err != nil {
		t.Fatalf("bob encrypt reply: %v", err)
	}
	if _, err := aCipher.Decrypt(ctx, bobID, reply); err != nil {
		t.Fatalf("alice decrypt reply: %v", err)
	}

	blob3, err := aCipher.Encrypt(ctx, bobID, []byte("good"))
	if err != nil {
		t.Fatalf("encrypt #3: %v", err)
	}
	if got := blob3[0]; got != protocol.WHISPER_TYPE {
		t.Errorf("blob #3 tag = %d, want %d (SignalMessage)", got, protocol.WHISPER_TYPE)
	}
	plain, err := bCipher.Decrypt(ctx, aliceID, blob3)
	if err != nil {
		t.Fatalf("bob decrypt #3: %v", err)
	}
	if !bytes.Equal(plain, []byte("good")) {
		t.Errorf("plaintext #3 = %q, want %q", plain, "good")
	}
}

func TestRoundTrip_BobReplies(t *testing.T) {
	ctx := context.Background()
	alice, bob := pairAlice(t)
	aCipher := session.New(alice)
	bCipher := session.New(bob)

	blob, err := aCipher.Encrypt(ctx, bobID, []byte("ping"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bCipher.Decrypt(ctx, aliceID, blob); err != nil {
		t.Fatalf("bob decrypt initial: %v", err)
	}

	reply, err := bCipher.Encrypt(ctx, aliceID, []byte("pong"))
	if err != nil {
		t.Fatalf("bob encrypt reply: %v", err)
	}
	got, err := aCipher.Decrypt(ctx, bobID, reply)
	if err != nil {
		t.Fatalf("alice decrypt reply: %v", err)
	}
	if !bytes.Equal(got, []byte("pong")) {
		t.Errorf("reply plaintext = %q, want %q", got, "pong")
	}
}

func TestRoundTrip_LargePayload(t *testing.T) {
	ctx := context.Background()
	alice, bob := pairAlice(t)
	aCipher := session.New(alice)
	bCipher := session.New(bob)

	plaintext := bytes.Repeat([]byte{0xAB}, 10*1024)
	blob, err := aCipher.Encrypt(ctx, bobID, plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	got, err := bCipher.Decrypt(ctx, aliceID, blob)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("large round-trip drift: len got=%d want=%d", len(got), len(plaintext))
	}
}

func TestRoundTrip_EmptyPlaintext(t *testing.T) {
	ctx := context.Background()
	alice, bob := pairAlice(t)
	aCipher := session.New(alice)
	bCipher := session.New(bob)

	blob, err := aCipher.Encrypt(ctx, bobID, []byte{})
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	got, err := bCipher.Decrypt(ctx, aliceID, blob)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty round-trip produced %d bytes, want 0", len(got))
	}
}

func TestEncrypt_NoSession(t *testing.T) {
	_, alice := newStateAndStores(t)
	aCipher := session.New(alice)
	_, err := aCipher.Encrypt(context.Background(), bobID, []byte("hi"))
	if err == nil {
		t.Fatal("expected encrypt to fail with no session record")
	}
	if !errors.Is(err, session.ErrNoSession) {
		t.Errorf("err = %v, want wraps ErrNoSession", err)
	}
}

func TestDecrypt_ShortBlob(t *testing.T) {
	_, alice := newStateAndStores(t)
	aCipher := session.New(alice)
	_, err := aCipher.Decrypt(context.Background(), bobID, nil)
	if err == nil || !strings.Contains(err.Error(), session.ErrShortBlob.Error()) {
		t.Fatalf("nil blob err = %v, want ErrShortBlob", err)
	}
}

func TestDecrypt_UnknownType(t *testing.T) {
	_, alice := newStateAndStores(t)
	aCipher := session.New(alice)
	_, err := aCipher.Decrypt(context.Background(), bobID, []byte{0x42, 0x00, 0x01})
	if err == nil || !strings.Contains(err.Error(), session.ErrUnknownType.Error()) {
		t.Fatalf("unknown-type blob err = %v, want ErrUnknownType", err)
	}
}

func TestDecrypt_TamperedCiphertext(t *testing.T) {
	ctx := context.Background()
	alice, bob := pairAlice(t)
	aCipher := session.New(alice)
	bCipher := session.New(bob)

	blob, err := aCipher.Encrypt(ctx, bobID, []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}

	blob[len(blob)-1] ^= 0x01
	_, err = bCipher.Decrypt(ctx, aliceID, blob)
	if err == nil {
		t.Fatal("expected decrypt to fail on tampered ciphertext")
	}
}

func TestDecrypt_WrongRecipient(t *testing.T) {
	ctx := context.Background()
	alice, _ := pairAlice(t)
	_, carol := newStateAndStores(t)
	aCipher := session.New(alice)
	cCipher := session.New(carol)

	blob, err := aCipher.Encrypt(ctx, bobID, []byte("for bob only"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = cCipher.Decrypt(ctx, aliceID, blob)
	if err == nil {
		t.Fatal("expected decrypt to fail when recipient stores don't match")
	}
}
