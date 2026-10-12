package signal

import (
	"testing"

	"github.com/dgraph-io/badger/v4"
	"go.mau.fi/libsignal/ecc"

	"haoma-frontend/internal/store"
)

func adoptFixture(t *testing.T) (st *store.Store, joiner, source *State) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Unlock(dir, "pw")
	if err != nil {
		t.Fatalf("unlock joiner store: %v", err)
	}
	t.Cleanup(func() { _ = st.Lock() })

	joiner, _, err = LoadOrBootstrap(st, 4)
	if err != nil {
		t.Fatalf("bootstrap joiner: %v", err)
	}
	source, err = Bootstrap(4)
	if err != nil {
		t.Fatalf("bootstrap source: %v", err)
	}
	if joiner.IdentityKeyPair.PublicKey().Fingerprint() == source.IdentityKeyPair.PublicKey().Fingerprint() {
		t.Fatal("fixture is degenerate: the two devices already share a fingerprint, so nothing below can prove adoption happened")
	}
	return st, joiner, source
}

func sourceKeys(state *State) (pub, priv []byte) {
	privArr := state.IdentityKeyPair.PrivateKey().Serialize()
	return state.IdentityKeyPair.PublicKey().Serialize(), privArr[:]
}

func TestAdoptIdentity_FingerprintEqualsSource(t *testing.T) {
	st, joiner, source := adoptFixture(t)
	pub, priv := sourceKeys(source)

	if err := AdoptIdentity(st, joiner, pub, priv); err != nil {
		t.Fatalf("AdoptIdentity: %v", err)
	}

	got := joiner.IdentityKeyPair.PublicKey().Fingerprint()
	want := source.IdentityKeyPair.PublicKey().Fingerprint()
	if got != want {
		t.Fatalf("adopted fingerprint = %q, want the source device's %q — without this equality a peer cannot recognise the two devices as one identity", got, want)
	}

	reloaded, err := Load(st)
	if err != nil {
		t.Fatalf("reload after adoption: %v", err)
	}
	if fp := reloaded.IdentityKeyPair.PublicKey().Fingerprint(); fp != want {
		t.Fatalf("reloaded fingerprint = %q, want %q — adoption did not reach the store", fp, want)
	}
}

func TestAdoptIdentity_KeepsOwnRegistrationID(t *testing.T) {
	st, joiner, source := adoptFixture(t)
	before := joiner.RegistrationID
	if before == source.RegistrationID {
		t.Skip("the two bootstraps drew the same registration id; nothing to prove here")
	}
	pub, priv := sourceKeys(source)

	if err := AdoptIdentity(st, joiner, pub, priv); err != nil {
		t.Fatalf("AdoptIdentity: %v", err)
	}

	if joiner.RegistrationID != before {
		t.Fatalf("registration id = %d, want the device's own %d — it must not come from the bundle", joiner.RegistrationID, before)
	}
	reloaded, err := Load(st)
	if err != nil {
		t.Fatalf("reload after adoption: %v", err)
	}
	if reloaded.RegistrationID != before {
		t.Fatalf("persisted registration id = %d, want %d", reloaded.RegistrationID, before)
	}
}

func TestAdoptIdentity_SignedPreKeyVerifiesUnderAdoptedIdentity(t *testing.T) {
	st, joiner, source := adoptFixture(t)
	oldSPKID := joiner.SignedPreKey.ID()
	pub, priv := sourceKeys(source)

	if err := AdoptIdentity(st, joiner, pub, priv); err != nil {
		t.Fatalf("AdoptIdentity: %v", err)
	}

	adoptedPub := joiner.IdentityKeyPair.PublicKey().PublicKey()

	sig := joiner.SignedPreKey.Signature()
	if !ecc.VerifySignature(adoptedPub, joiner.SignedPreKey.KeyPair().PublicKey().Serialize(), sig) {
		t.Fatal("in-memory signed prekey does not verify under the adopted identity — a peer pairing with this device after linking would reject its bundle")
	}
	if joiner.SignedPreKey.ID() == oldSPKID {
		t.Fatalf("signed prekey id is still %d — ids must advance past anything a peer may already have fetched", oldSPKID)
	}

	reloaded, err := Load(st)
	if err != nil {
		t.Fatalf("reload after adoption: %v", err)
	}
	rsig := reloaded.SignedPreKey.Signature()
	if !ecc.VerifySignature(adoptedPub, reloaded.SignedPreKey.KeyPair().PublicKey().Serialize(), rsig) {
		t.Fatal("reloaded signed prekey does not verify under the adopted identity")
	}

	if ok, err := NewStores(st, reloaded).ContainsSignedPreKey(t.Context(), oldSPKID); err != nil {
		t.Fatalf("ContainsSignedPreKey: %v", err)
	} else if ok {
		t.Fatalf("signed prekey %d from before adoption is still stored", oldSPKID)
	}
}

func TestAdoptIdentity_RegeneratesOneTimePreKeys(t *testing.T) {
	st, joiner, source := adoptFixture(t)
	before := map[uint32][]byte{}
	for _, opk := range joiner.OneTimePreKeys {
		if opk.ID().IsEmpty {
			continue
		}
		before[opk.ID().Value] = opk.KeyPair().PublicKey().Serialize()
	}
	if len(before) == 0 {
		t.Fatal("fixture has no one-time prekeys to regenerate")
	}
	pub, priv := sourceKeys(source)

	if err := AdoptIdentity(st, joiner, pub, priv); err != nil {
		t.Fatalf("AdoptIdentity: %v", err)
	}

	if len(joiner.OneTimePreKeys) != len(before) {
		t.Fatalf("one-time prekey count = %d, want the pool depth it had (%d)", len(joiner.OneTimePreKeys), len(before))
	}
	for _, opk := range joiner.OneTimePreKeys {
		id := opk.ID()
		if id.IsEmpty {
			continue
		}
		if _, reused := before[id.Value]; reused {
			t.Fatalf("one-time prekey id %d was reused after adoption — ids must advance past every pre-link key", id.Value)
		}
	}

	reloaded, err := Load(st)
	if err != nil {
		t.Fatalf("reload after adoption: %v", err)
	}
	if len(reloaded.OneTimePreKeys) != len(before) {
		t.Fatalf("persisted one-time prekey count = %d, want %d — the pre-link pool was not deleted", len(reloaded.OneTimePreKeys), len(before))
	}
}

func TestAdoptIdentity_RejectsMismatchedKeypairAndChangesNothing(t *testing.T) {
	st, joiner, source := adoptFixture(t)
	_, priv := sourceKeys(source)
	other, err := Bootstrap(2)
	if err != nil {
		t.Fatal(err)
	}
	wrongPub := other.IdentityKeyPair.PublicKey().Serialize()

	fpBefore := joiner.IdentityKeyPair.PublicKey().Fingerprint()
	spkBefore := joiner.SignedPreKey.ID()

	if err := AdoptIdentity(st, joiner, wrongPub, priv); err == nil {
		t.Fatal("AdoptIdentity accepted a public key that does not derive from the private key")
	}

	if fp := joiner.IdentityKeyPair.PublicKey().Fingerprint(); fp != fpBefore {
		t.Fatalf("in-memory identity changed on a refused adoption (%q → %q)", fpBefore, fp)
	}
	if joiner.SignedPreKey.ID() != spkBefore {
		t.Fatal("signed prekey was regenerated on a refused adoption")
	}
	reloaded, err := Load(st)
	if err != nil {
		t.Fatalf("reload after refused adoption: %v", err)
	}
	if fp := reloaded.IdentityKeyPair.PublicKey().Fingerprint(); fp != fpBefore {
		t.Fatalf("stored identity changed on a refused adoption (%q → %q)", fpBefore, fp)
	}
}

func TestAdoptIdentity_RejectsWrongLengths(t *testing.T) {
	st, joiner, source := adoptFixture(t)
	pub, priv := sourceKeys(source)

	if err := AdoptIdentity(st, joiner, pub[:10], priv); err == nil {
		t.Fatal("AdoptIdentity accepted a short public key")
	}
	if err := AdoptIdentity(st, joiner, pub, priv[:10]); err == nil {
		t.Fatal("AdoptIdentity accepted a short private key")
	}
}

func TestAdoptIdentity_RecordsTheErasedOneTimePreKeyBoundary(t *testing.T) {
	st, joiner, source := adoptFixture(t)
	var highestBefore uint32
	for _, opk := range joiner.OneTimePreKeys {
		if id := opk.ID(); !id.IsEmpty && id.Value > highestBefore {
			highestBefore = id.Value
		}
	}
	if highestBefore == 0 {
		t.Fatal("fixture holds no one-time prekeys, so there is no boundary to record")
	}
	pub, priv := sourceKeys(source)

	if err := AdoptIdentity(st, joiner, pub, priv); err != nil {
		t.Fatalf("AdoptIdentity: %v", err)
	}

	if got := storedErasedThrough(t, st); got != highestBefore {
		t.Fatalf("recorded boundary = %d, want the deleted pool's highest id %d", got, highestBefore)
	}
}

func TestAdoptIdentity_BoundaryNeverMovesDown(t *testing.T) {
	st, joiner, source := adoptFixture(t)
	pub, priv := sourceKeys(source)
	if err := AdoptIdentity(st, joiner, pub, priv); err != nil {
		t.Fatalf("first AdoptIdentity: %v", err)
	}
	first := storedErasedThrough(t, st)

	second, err := Bootstrap(4)
	if err != nil {
		t.Fatal(err)
	}
	pub2, priv2 := sourceKeys(second)
	if err := AdoptIdentity(st, joiner, pub2, priv2); err != nil {
		t.Fatalf("second AdoptIdentity: %v", err)
	}

	if got := storedErasedThrough(t, st); got < first {
		t.Fatalf("boundary after the second link = %d, below the first link's %d", got, first)
	}
}

func TestReadErasedThrough_AbsentAndCorrupt(t *testing.T) {
	st, _, _ := adoptFixture(t)

	if got := storedErasedThrough(t, st); got != 0 {
		t.Fatalf("boundary on a device that never adopted = %d, want 0", got)
	}

	if err := st.Put([]byte(opkErasedThroughKey), []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	err := st.View(func(txn *badger.Txn) error {
		_, rerr := readErasedThrough(txn)
		return rerr
	})
	if err == nil {
		t.Fatal("a two-byte boundary was read without error — a corrupt row would be reported as 'never adopted'")
	}
}

func storedErasedThrough(t *testing.T, st *store.Store) uint32 {
	t.Helper()
	var got uint32
	if err := st.View(func(txn *badger.Txn) error {
		v, err := readErasedThrough(txn)
		got = v
		return err
	}); err != nil {
		t.Fatalf("read the erased one-time prekey boundary: %v", err)
	}
	return got
}
