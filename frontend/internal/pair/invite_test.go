package pair

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/signal"
	"haoma-frontend/internal/store"
)

func TestMain(m *testing.M) {
	store.DefaultKDFParams = store.KDFParams{
		Time: 1, Memory: 8 * 1024, Threads: 2, KeyLen: 32, SaltLen: 16,
	}
	os.Exit(m.Run())
}

func freshState(t *testing.T, opkCount int) *signal.State {
	t.Helper()
	s, err := signal.Bootstrap(opkCount)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestBuild_PopulatesAllFields(t *testing.T) {
	state := freshState(t, 4)
	inv, mine, err := Build(state, state, []string{"onion-a", "onion-b"}, "alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	if mine == nil {
		t.Fatal("Build returned nil MyKeys")
	}

	if len(inv.PeerID) != 32 {
		t.Errorf("peer_id len = %d, want 32 hex", len(inv.PeerID))
	}
	if _, err := hex.DecodeString(inv.PeerID); err != nil {
		t.Errorf("peer_id not hex: %v", err)
	}
	if mine.PeerID != inv.PeerID {
		t.Errorf("MyKeys.PeerID = %q, want %q", mine.PeerID, inv.PeerID)
	}
	if !stringSliceEqual(inv.Addresses, []string{"onion-a", "onion-b"}) {
		t.Errorf("addresses = %v", inv.Addresses)
	}
	secretBytes, _ := hex.DecodeString(inv.Secret)
	if len(secretBytes) != SecretLen {
		t.Errorf("secret len = %d, want %d", len(secretBytes), SecretLen)
	}
	if !bytes.Equal(mine.OutboundSecret, secretBytes) {
		t.Errorf("MyKeys.OutboundSecret diverges from inv.Secret")
	}
	if inv.Frontend.Nick != "alice" {
		t.Errorf("nickname = %q", inv.Frontend.Nick)
	}
	if inv.Frontend.Signal.RegistrationID != state.RegistrationID {
		t.Error("reg id mismatch")
	}
	idKey, _ := hex.DecodeString(inv.Frontend.Signal.IdentityKey)
	if len(idKey) != 33 {
		t.Errorf("identity_key len = %d, want 33", len(idKey))
	}
	if inv.Frontend.Signal.SignedPreKey.ID != state.SignedPreKey.ID() {
		t.Error("signed prekey id mismatch")
	}
	sig, _ := hex.DecodeString(inv.Frontend.Signal.SignedPreKey.Signature)
	if len(sig) != 64 {
		t.Errorf("spk signature len = %d, want 64", len(sig))
	}
	if inv.Frontend.Signal.OneTimePreKey.ID == 0 {
		t.Error("one-time prekey id zero")
	}
}

func TestBuild_DistinctSecrets(t *testing.T) {
	state := freshState(t, 4)
	a, _, err := Build(state, state, []string{"x"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := Build(state, state, []string{"x"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Secret == b.Secret {
		t.Error("two Build calls produced the same outbound secret")
	}
	if a.PeerID == b.PeerID {
		t.Error("two Build calls produced the same peer id")
	}
}

func TestBuild_NilStateErrors(t *testing.T) {
	state := freshState(t, 4)
	if _, _, err := Build(nil, state, []string{"x"}, "", nil); err == nil {
		t.Error("expected error on nil state")
	}
	if _, _, err := Build(state, nil, []string{"x"}, "", nil); err == nil {
		t.Error("expected error on nil opk source")
	}
}

func TestBuild_ExplicitMyKeys(t *testing.T) {
	state := freshState(t, 4)
	want := make([]byte, SecretLen)
	for i := range want {
		want[i] = byte(i)
	}
	const reusePeerID = "deadbeef00000000000000000000babe"
	inv, mine, err := Build(state, state, []string{"x"}, "", &MyKeys{
		PeerID:         reusePeerID,
		OutboundSecret: want,
	})
	if err != nil {
		t.Fatal(err)
	}
	if inv.PeerID != reusePeerID {
		t.Errorf("peer_id = %q, want %q", inv.PeerID, reusePeerID)
	}
	if mine.PeerID != reusePeerID {
		t.Errorf("MyKeys.PeerID = %q, want %q", mine.PeerID, reusePeerID)
	}
	gotSecret, err := hex.DecodeString(inv.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotSecret, want) {
		t.Errorf("secret round-trip drift")
	}
}

func TestBuild_BadMyKeys(t *testing.T) {
	state := freshState(t, 4)
	if _, _, err := Build(state, state, []string{"x"}, "", &MyKeys{PeerID: "", OutboundSecret: bytes.Repeat([]byte{1}, SecretLen)}); err == nil {
		t.Error("expected error: empty peer-id")
	}
	if _, _, err := Build(state, state, []string{"x"}, "", &MyKeys{PeerID: "abc", OutboundSecret: []byte{1, 2, 3}}); err == nil {
		t.Error("expected error: short outbound secret")
	}
}

func TestBuild_EmptyAddressesErrors(t *testing.T) {
	state := freshState(t, 4)
	if _, _, err := Build(state, state, nil, "", nil); err == nil {
		t.Error("expected error on empty addresses")
	}
	if _, _, err := Build(state, state, []string{}, "", nil); err == nil {
		t.Error("expected error on empty-slice addresses")
	}
}

func TestBuild_NoOPKsErrors(t *testing.T) {
	state := freshState(t, 4)
	state.OneTimePreKeys = nil
	_, _, err := Build(state, state, []string{"x"}, "", nil)
	if err != ErrNoPreKeysAvailable {
		t.Errorf("err = %v, want ErrNoPreKeysAvailable", err)
	}
}

type fakeOPKSource struct {
	consumed map[uint32]bool
	pubs     map[uint32][]byte
}

func (f *fakeOPKSource) AvailableOPK() (uint32, []byte, error) {
	for id := uint32(1); id <= 1000; id++ {
		if f.consumed[id] {
			continue
		}
		pub, ok := f.pubs[id]
		if !ok {
			continue
		}
		return id, pub, nil
	}
	return 0, nil, signal.ErrOPKPoolEmpty
}

func TestBuild_RepairPicksFreshOPK(t *testing.T) {
	state := freshState(t, 4)

	src := &fakeOPKSource{
		consumed: map[uint32]bool{},
		pubs:     map[uint32][]byte{},
	}
	for _, k := range state.OneTimePreKeys {
		id := k.ID()
		if id.IsEmpty {
			continue
		}
		src.pubs[id.Value] = k.KeyPair().PublicKey().Serialize()
	}

	first, _, err := Build(state, src, []string{"x"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}

	src.consumed[first.Frontend.Signal.OneTimePreKey.ID] = true

	second, _, err := Build(state, src, []string{"x"}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Frontend.Signal.OneTimePreKey.ID == second.Frontend.Signal.OneTimePreKey.ID {
		t.Errorf("Build re-used consumed OPK id=%d on second call — regression: re-pair would fail with 'store: not found'", first.Frontend.Signal.OneTimePreKey.ID)
	}
}

func TestMarshal_Parse_RoundTrip(t *testing.T) {
	state := freshState(t, 4)
	orig, _, err := Build(state, state, []string{"onion-a"}, "alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := orig.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	round, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	origBytes, _ := json.Marshal(orig)
	roundBytes, _ := json.Marshal(round)
	if string(origBytes) != string(roundBytes) {
		t.Errorf("round-trip differs:\norig:  %s\nround: %s", origBytes, roundBytes)
	}
}

func TestParse_RejectsUnknownFields(t *testing.T) {
	state := freshState(t, 4)
	inv, _, _ := Build(state, state, []string{"x"}, "", nil)
	raw, _ := inv.Marshal()
	mangled := strings.Replace(string(raw), `"peer_id"`, `"malicious_field":"bad","peer_id"`, 1)
	if _, err := Parse([]byte(mangled)); err == nil {
		t.Error("expected error on unknown field")
	}
}

func TestValidate_EmptyFieldsRejected(t *testing.T) {
	state := freshState(t, 4)
	good, _, _ := Build(state, state, []string{"x"}, "", nil)

	cases := []struct {
		name   string
		mutate func(*Invite)
	}{
		{"empty peer_id", func(i *Invite) { i.PeerID = "" }},
		{"non-hex peer_id", func(i *Invite) { i.PeerID = "not-hex-!!" }},
		{"empty addresses", func(i *Invite) { i.Addresses = nil }},
		{"short secret", func(i *Invite) { i.Secret = "aa" }},
		{"non-hex secret", func(i *Invite) { i.Secret = "not-hex-!!" }},
		{"zero reg id", func(i *Invite) { i.Frontend.Signal.RegistrationID = 0 }},
		{"short identity key", func(i *Invite) { i.Frontend.Signal.IdentityKey = hex.EncodeToString([]byte("short")) }},
		{"zero spk id", func(i *Invite) { i.Frontend.Signal.SignedPreKey.ID = 0 }},
		{"short spk pub", func(i *Invite) { i.Frontend.Signal.SignedPreKey.Public = "aabb" }},
		{"short spk sig", func(i *Invite) { i.Frontend.Signal.SignedPreKey.Signature = "aabb" }},
		{"zero opk id", func(i *Invite) { i.Frontend.Signal.OneTimePreKey.ID = 0 }},
		{"short opk pub", func(i *Invite) { i.Frontend.Signal.OneTimePreKey.Public = "aabb" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := *good
			c.mutate(&v)
			if err := v.Validate(); err == nil {
				t.Errorf("expected error for %s", c.name)
			}
		})
	}
}

func TestValidate_GoodPasses(t *testing.T) {
	state := freshState(t, 4)
	good, _, _ := Build(state, state, []string{"x"}, "", nil)
	if err := good.Validate(); err != nil {
		t.Errorf("good invite failed validation: %v", err)
	}
}

func TestToBundle_ReconstructsFields(t *testing.T) {
	state := freshState(t, 4)
	inv, _, _ := Build(state, state, []string{"x"}, "", nil)
	bundle, err := inv.ToBundle()
	if err != nil {
		t.Fatal(err)
	}
	if bundle.RegistrationID() != state.RegistrationID {
		t.Error("bundle reg id mismatch")
	}
	if bundle.DeviceID() != DeviceID {
		t.Errorf("bundle device id = %d, want %d", bundle.DeviceID(), DeviceID)
	}
	if bundle.SignedPreKeyID() != state.SignedPreKey.ID() {
		t.Error("bundle spk id mismatch")
	}
	pkID := bundle.PreKeyID()
	if pkID == nil || pkID.IsEmpty {
		t.Error("bundle prekey id empty")
	}
	if bundle.IdentityKey() == nil {
		t.Error("bundle identity key nil")
	}
	if bundle.PreKey() == nil {
		t.Error("bundle prekey pub nil")
	}
	if bundle.SignedPreKey() == nil {
		t.Error("bundle signed prekey pub nil")
	}
	gotSig := bundle.SignedPreKeySignature()
	wantSig := state.SignedPreKey.Signature()
	if gotSig != wantSig {
		t.Error("bundle signed prekey signature mismatch")
	}
}

func TestBackend_ProjectsTwoKeyForm(t *testing.T) {
	state := freshState(t, 4)
	inv, mine, _ := Build(state, state, []string{"onion-a"}, "alice", nil)
	minted := backendapi.MintedOnion{
		Address:    "myonionAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA1",
		PrivateKey: "ZmFrZS1iYXNlNjQta2V5",
	}
	b := inv.Backend(mine, minted)
	if b.PeerID != inv.PeerID {
		t.Error("backend peer id drift")
	}
	if !stringSliceEqual(b.Addresses, inv.Addresses) {
		t.Error("backend addresses drift")
	}
	if b.InboundSecret != inv.Secret {
		t.Error("backend inbound_secret should equal inviter's outbound (= inv.Secret)")
	}
	if b.OutboundSecret != hex.EncodeToString(mine.OutboundSecret) {
		t.Error("backend outbound_secret should equal MyKeys.OutboundSecret")
	}
	if b.MyOnionAddr != minted.Address || b.MyOnionPrivateKey != minted.PrivateKey {
		t.Error("backend MyOnion fields should mirror the minted onion")
	}

	b.Addresses[0] = "hijacked"
	if inv.Addresses[0] == "hijacked" {
		t.Error("Backend() returned a live slice reference; should be a copy")
	}
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
