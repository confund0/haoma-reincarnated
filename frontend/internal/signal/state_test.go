package signal

import (
	"bytes"
	"errors"
	"os"
	"testing"

	"go.mau.fi/libsignal/ecc"
	"go.mau.fi/libsignal/state/record"

	"haoma-frontend/internal/store"
)

func TestMain(m *testing.M) {
	store.DefaultKDFParams = store.KDFParams{
		Time: 1, Memory: 8 * 1024, Threads: 2, KeyLen: 32, SaltLen: 16,
	}
	os.Exit(m.Run())
}

func freshStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Unlock(dir, "test-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Lock() })
	return s
}

func TestBootstrap_GeneratesShapes(t *testing.T) {
	st, err := Bootstrap(5)
	if err != nil {
		t.Fatal(err)
	}
	if st.RegistrationID == 0 {
		t.Error("RegistrationID should be non-zero (astronomically unlikely to be zero)")
	}
	if st.IdentityKeyPair == nil {
		t.Fatal("IdentityKeyPair nil")
	}
	if st.IdentityKeyPair.PublicKey() == nil {
		t.Error("IdentityKeyPair.PublicKey nil")
	}
	if st.SignedPreKey == nil {
		t.Fatal("SignedPreKey nil")
	}
	if st.SignedPreKey.ID() != 1 {
		t.Errorf("SignedPreKey.ID = %d, want 1", st.SignedPreKey.ID())
	}
	if st.SignedPreKey.Timestamp() == 0 {
		t.Error("SignedPreKey.Timestamp should be set to now")
	}
	if got := len(st.OneTimePreKeys); got != 5 {
		t.Errorf("OneTimePreKeys = %d, want 5", got)
	}

	for i, opk := range st.OneTimePreKeys {
		want := uint32(i + 1)
		if opk.ID().IsEmpty {
			t.Errorf("opk[%d] id empty", i)
			continue
		}
		if opk.ID().Value != want {
			t.Errorf("opk[%d].ID = %d, want %d", i, opk.ID().Value, want)
		}
	}
}

func TestBootstrap_ZeroCountUsesDefault(t *testing.T) {
	st, err := Bootstrap(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.OneTimePreKeys) != DefaultOPKCount {
		t.Errorf("len(OneTimePreKeys) = %d, want DefaultOPKCount=%d", len(st.OneTimePreKeys), DefaultOPKCount)
	}
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	s := freshStore(t)
	orig, err := Bootstrap(3)
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(s, orig); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(s)
	if err != nil {
		t.Fatal(err)
	}

	if loaded.RegistrationID != orig.RegistrationID {
		t.Errorf("RegistrationID: got %d, want %d", loaded.RegistrationID, orig.RegistrationID)
	}

	if !bytes.Equal(loaded.IdentityKeyPair.PublicKey().Serialize(), orig.IdentityKeyPair.PublicKey().Serialize()) {
		t.Error("identity public key mismatch after load")
	}
	origPriv := orig.IdentityKeyPair.PrivateKey().Serialize()
	loadedPriv := loaded.IdentityKeyPair.PrivateKey().Serialize()
	if origPriv != loadedPriv {
		t.Error("identity private key mismatch after load")
	}

	if loaded.SignedPreKey.ID() != orig.SignedPreKey.ID() {
		t.Errorf("SignedPreKey.ID: got %d, want %d", loaded.SignedPreKey.ID(), orig.SignedPreKey.ID())
	}
	if !bytes.Equal(loaded.SignedPreKey.KeyPair().PublicKey().Serialize(), orig.SignedPreKey.KeyPair().PublicKey().Serialize()) {
		t.Error("SignedPreKey public key mismatch after load")
	}
	origSPKPriv := orig.SignedPreKey.KeyPair().PrivateKey().Serialize()
	loadedSPKPriv := loaded.SignedPreKey.KeyPair().PrivateKey().Serialize()
	if origSPKPriv != loadedSPKPriv {
		t.Error("SignedPreKey private key mismatch after load")
	}
	origSig := orig.SignedPreKey.Signature()
	loadedSig := loaded.SignedPreKey.Signature()
	if origSig != loadedSig {
		t.Error("SignedPreKey signature mismatch after load")
	}

	if len(loaded.OneTimePreKeys) != len(orig.OneTimePreKeys) {
		t.Errorf("OPK count: got %d, want %d", len(loaded.OneTimePreKeys), len(orig.OneTimePreKeys))
	}
	origIDs := opkIDSet(orig.OneTimePreKeys)
	loadedIDs := opkIDSet(loaded.OneTimePreKeys)
	if !idSetsEqual(origIDs, loadedIDs) {
		t.Errorf("OPK id set mismatch: got %v, want %v", loadedIDs, origIDs)
	}
}

func TestSaveLoad_SPKSignatureVerifiesAgainstLoadedIdentity(t *testing.T) {
	s := freshStore(t)
	orig, err := Bootstrap(3)
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(s, orig); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(s)
	if err != nil {
		t.Fatal(err)
	}

	idPub := loaded.IdentityKeyPair.PublicKey().PublicKey()
	spkPubSerialized := loaded.SignedPreKey.KeyPair().PublicKey().Serialize()
	sig := loaded.SignedPreKey.Signature()

	if !ecc.VerifySignature(idPub, spkPubSerialized, sig) {
		t.Fatal("loaded SPK signature does NOT verify against loaded identity — upstream bug regressed")
	}
}

func TestLoad_EmptyStore_ReturnsErrNotInitialized(t *testing.T) {
	s := freshStore(t)
	_, err := Load(s)
	if !errors.Is(err, ErrNotInitialized) {
		t.Errorf("err = %v, want ErrNotInitialized", err)
	}
}

func TestLoadOrBootstrap_FirstCallCreates(t *testing.T) {
	s := freshStore(t)
	st, created, err := LoadOrBootstrap(s, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("first call should have created")
	}
	if len(st.OneTimePreKeys) != 4 {
		t.Errorf("len(OneTimePreKeys) = %d, want 4", len(st.OneTimePreKeys))
	}
}

func TestLoadOrBootstrap_SecondCallLoads(t *testing.T) {
	s := freshStore(t)
	first, _, err := LoadOrBootstrap(s, 4)
	if err != nil {
		t.Fatal(err)
	}

	second, created, err := LoadOrBootstrap(s, 4)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("second call should have loaded, not created")
	}

	if !bytes.Equal(first.IdentityKeyPair.PublicKey().Serialize(), second.IdentityKeyPair.PublicKey().Serialize()) {
		t.Error("second call regenerated the identity key (should have loaded existing)")
	}
	if first.RegistrationID != second.RegistrationID {
		t.Error("second call regenerated the registration id")
	}
}

func TestConsumeOneTimePreKey_RemovesFromStore(t *testing.T) {
	s := freshStore(t)
	orig, _ := Bootstrap(5)
	if err := Save(s, orig); err != nil {
		t.Fatal(err)
	}

	if err := ConsumeOneTimePreKey(s, 3); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.OneTimePreKeys) != 4 {
		t.Errorf("after consume, OPK count = %d, want 4", len(loaded.OneTimePreKeys))
	}
	for _, opk := range loaded.OneTimePreKeys {
		if opk.ID().Value == 3 {
			t.Error("consumed opk id=3 still present")
		}
	}
}

func TestConsumeOneTimePreKey_UnknownID_NoError(t *testing.T) {
	s := freshStore(t)
	orig, _ := Bootstrap(3)
	_ = Save(s, orig)

	if err := ConsumeOneTimePreKey(s, 999); err != nil {
		t.Errorf("consume unknown id errored: %v", err)
	}
}

func TestSummary_ExposesNonSecrets(t *testing.T) {
	st, _ := Bootstrap(5)
	sum := st.Summary()
	if sum.RegistrationID != st.RegistrationID {
		t.Error("summary RegistrationID mismatch")
	}
	if sum.IdentityFingerprint == "" {
		t.Error("summary should include identity fingerprint")
	}
	if sum.SignedPreKeyID != 1 {
		t.Errorf("SignedPreKeyID = %d, want 1", sum.SignedPreKeyID)
	}
	if sum.OneTimePreKeyCount != 5 {
		t.Errorf("OPK count = %d, want 5", sum.OneTimePreKeyCount)
	}
	if sum.OneTimePreKeyLowest != 1 || sum.OneTimePreKeyHighest != 5 {
		t.Errorf("OPK range = [%d, %d], want [1, 5]", sum.OneTimePreKeyLowest, sum.OneTimePreKeyHighest)
	}
}

func TestState_MarshalJSON_DoesNotLeakPrivate(t *testing.T) {
	st, _ := Bootstrap(2)
	b, err := st.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)

	for _, forbidden := range []string{"private", "priv_key", "IdentityKeyPair", "KeyPair"} {
		if containsFold(got, forbidden) {
			t.Errorf("marshaled state includes %q: %s", forbidden, got)
		}
	}
}

func opkIDSet(opks []*record.PreKey) map[uint32]bool {
	out := make(map[uint32]bool, len(opks))
	for _, k := range opks {
		if id := k.ID(); !id.IsEmpty {
			out[id.Value] = true
		}
	}
	return out
}

func idSetsEqual(a, b map[uint32]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func containsFold(s, sub string) bool {
	return len(sub) <= len(s) && indexFold(s, sub) >= 0
}

func indexFold(s, sub string) int {
	if len(sub) == 0 {
		return 0
	}
outer:
	for i := 0; i+len(sub) <= len(s); i++ {
		for j := 0; j < len(sub); j++ {
			if lower(s[i+j]) != lower(sub[j]) {
				continue outer
			}
		}
		return i
	}
	return -1
}

func lower(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + 32
	}
	return b
}
