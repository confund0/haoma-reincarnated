package signal

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/dgraph-io/badger/v4"
	"go.mau.fi/libsignal/protocol"
	"go.mau.fi/libsignal/util/keyhelper"

	"haoma-frontend/internal/store"
)

func freshStores(t *testing.T) (*Stores, *store.Store, *State) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Unlock(dir, "test-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Lock() })
	state, created, err := LoadOrBootstrap(st, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("freshStores: expected new bootstrap, got load")
	}
	return NewStores(st, state), st, state
}

func TestStores_GetIdentity(t *testing.T) {
	s, _, state := freshStores(t)
	if s.GetIdentityKeyPair() != state.IdentityKeyPair {
		t.Error("GetIdentityKeyPair returns something other than State's")
	}
	if s.GetLocalRegistrationID() != state.RegistrationID {
		t.Error("GetLocalRegistrationID mismatch")
	}
}

func TestStores_IsTrustedIdentity_Tofu(t *testing.T) {
	s, _, _ := freshStores(t)
	addr := protocol.NewSignalAddress("peer-unknown", 1)

	remote, _ := keyhelper.GenerateIdentityKeyPair()
	trusted, err := s.IsTrustedIdentity(context.Background(), addr, remote.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	if !trusted {
		t.Error("unknown peer should be trusted on first use")
	}
}

func TestStores_SaveIdentity_PersistsAndAccepts(t *testing.T) {
	s, _, _ := freshStores(t)
	addr := protocol.NewSignalAddress("peer-1", 1)
	remote, _ := keyhelper.GenerateIdentityKeyPair()

	if err := s.SaveIdentity(context.Background(), addr, remote.PublicKey()); err != nil {
		t.Fatal(err)
	}

	trusted, err := s.IsTrustedIdentity(context.Background(), addr, remote.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	if !trusted {
		t.Error("stored key should be trusted on re-presentation")
	}
}

func TestStores_IsTrustedIdentity_MismatchReturnsFalse(t *testing.T) {
	s, _, _ := freshStores(t)
	addr := protocol.NewSignalAddress("peer-1", 1)
	original, _ := keyhelper.GenerateIdentityKeyPair()
	rogue, _ := keyhelper.GenerateIdentityKeyPair()

	if err := s.SaveIdentity(context.Background(), addr, original.PublicKey()); err != nil {
		t.Fatal(err)
	}
	trusted, err := s.IsTrustedIdentity(context.Background(), addr, rogue.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	if trusted {
		t.Error("rogue key should NOT be trusted — TOFU mismatch")
	}
}

func TestStores_GetRemoteIdentity_RoundTrip(t *testing.T) {
	s, _, _ := freshStores(t)
	addr := protocol.NewSignalAddress("peer-1", 1)
	remote, _ := keyhelper.GenerateIdentityKeyPair()

	if err := s.SaveIdentity(context.Background(), addr, remote.PublicKey()); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRemoteIdentity(addr)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Serialize(), remote.PublicKey().Serialize()) {
		t.Error("GetRemoteIdentity returned a different key than SaveIdentity stored")
	}
}

func TestStores_GetRemoteIdentity_MissingReturnsErrNotFound(t *testing.T) {
	s, _, _ := freshStores(t)
	_, err := s.GetRemoteIdentity(protocol.NewSignalAddress("nobody", 1))
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want store.ErrNotFound", err)
	}
}

func TestStores_PreKey_LoadsBootstrappedKey(t *testing.T) {
	s, _, _ := freshStores(t)

	got, err := s.LoadPreKey(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID().IsEmpty || got.ID().Value != 2 {
		t.Errorf("got id %v, want 2", got.ID())
	}
}

func TestStores_PreKey_ContainsRemoveCycle(t *testing.T) {
	s, _, _ := freshStores(t)
	ctx := context.Background()

	ok, err := s.ContainsPreKey(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("bootstrapped prekey 3 should be present")
	}
	if err := s.RemovePreKey(ctx, 3); err != nil {
		t.Fatal(err)
	}
	ok, err = s.ContainsPreKey(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("prekey 3 still present after RemovePreKey")
	}
}

func TestStores_PreKey_LoadMissingErrors(t *testing.T) {
	s, _, _ := freshStores(t)
	_, err := s.LoadPreKey(context.Background(), 9999)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want store.ErrNotFound", err)
	}
}

func TestStores_PreKey_Store_Overwrites(t *testing.T) {
	s, _, _ := freshStores(t)
	ctx := context.Background()

	ser := s.serializer
	fresh, _ := keyhelper.GeneratePreKeys(7, 7, ser.PreKeyRecord)
	replacement := fresh[0]
	if err := s.StorePreKey(ctx, 7, replacement); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadPreKey(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Serialize(), replacement.Serialize()) {
		t.Error("Store then Load returned a different record")
	}
}

func freshStoresN(t *testing.T, n int) *Stores {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Unlock(dir, "test-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Lock() })
	state, _, err := LoadOrBootstrap(st, n)
	if err != nil {
		t.Fatal(err)
	}
	return NewStores(st, state)
}

func poolSnapshot(t *testing.T, s *Stores) (count int, ids map[uint32]bool) {
	t.Helper()
	ids = make(map[uint32]bool)
	if err := s.store.View(func(txn *badger.Txn) error {
		c, _, err := countAndMaxOPK(txn)
		if err != nil {
			return err
		}
		count = c
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(opkKeyPrefix)
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			k := it.Item().Key()
			if len(k) == len(opkKeyPrefix)+4 {
				ids[binary.BigEndian.Uint32(k[len(opkKeyPrefix):])] = true
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return count, ids
}

func TestStores_OPKRefill_TriggersBelowLowWater(t *testing.T) {

	s := freshStoresN(t, LowWaterOPK+1)
	ctx := context.Background()

	if err := s.RemovePreKey(ctx, 1); err != nil {
		t.Fatal(err)
	}
	count, _ := poolSnapshot(t, s)
	if count != LowWaterOPK {
		t.Fatalf("after 1st remove, count = %d, want %d (boundary, no refill)", count, LowWaterOPK)
	}

	if err := s.RemovePreKey(ctx, 2); err != nil {
		t.Fatal(err)
	}
	count, ids := poolSnapshot(t, s)
	if count != DefaultOPKCount {
		t.Fatalf("after refill, count = %d, want %d", count, DefaultOPKCount)
	}

	if ids[1] || ids[2] {
		t.Errorf("refill resurrected consumed ids: 1=%v 2=%v", ids[1], ids[2])
	}

	for id := uint32(3); id <= LowWaterOPK+1; id++ {
		if !ids[id] {
			t.Errorf("bootstrap id %d missing after refill", id)
		}
	}

	for id := uint32(LowWaterOPK + 2); id <= LowWaterOPK+1+71; id++ {
		if !ids[id] {
			t.Errorf("expected freshly minted id %d missing", id)
		}
	}
}

func TestStores_OPKRefill_NoopAtFullPool(t *testing.T) {
	s := freshStoresN(t, DefaultOPKCount)
	if err := s.RemovePreKey(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	count, ids := poolSnapshot(t, s)
	if count != DefaultOPKCount-1 {
		t.Errorf("after 1 remove from full pool, count = %d, want %d (no refill)", count, DefaultOPKCount-1)
	}

	for id := range ids {
		if id > DefaultOPKCount {
			t.Errorf("unexpected refilled id %d above bootstrap range", id)
		}
	}
}

func TestStores_OPKRefill_DispenseOPKReturnsValidPostRefill(t *testing.T) {
	s := freshStoresN(t, LowWaterOPK+1)
	ctx := context.Background()

	_ = s.RemovePreKey(ctx, 1)
	_ = s.RemovePreKey(ctx, 2)

	id, pub, err := s.DispenseOPK()
	if err != nil {
		t.Fatalf("DispenseOPK: %v", err)
	}

	if id != 3 {
		t.Errorf("DispenseOPK id = %d, want 3", id)
	}

	if len(pub) != 33 {
		t.Errorf("DispenseOPK pub len = %d, want 33", len(pub))
	}
}

func TestStores_OPKRefill_DirectMaybeRefillNoOp(t *testing.T) {
	s := freshStoresN(t, DefaultOPKCount)
	minted, err := s.maybeRefill(LowWaterOPK, DefaultOPKCount)
	if err != nil {
		t.Fatal(err)
	}
	if minted != 0 {
		t.Errorf("minted = %d, want 0 (above threshold)", minted)
	}
}

func TestStores_SignedPreKey_LoadBootstrapped(t *testing.T) {
	s, _, _ := freshStores(t)
	got, err := s.LoadSignedPreKey(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID() != 1 {
		t.Errorf("got id %d, want 1", got.ID())
	}
}

func TestStores_SignedPreKey_LoadAll(t *testing.T) {
	s, _, _ := freshStores(t)
	got, err := s.LoadSignedPreKeys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("len = %d, want 1 (bootstrap writes one)", len(got))
	}
}

func TestStores_SignedPreKey_ContainsRemoveCycle(t *testing.T) {
	s, _, _ := freshStores(t)
	ctx := context.Background()

	ok, err := s.ContainsSignedPreKey(ctx, 1)
	if err != nil || !ok {
		t.Fatalf("ContainsSignedPreKey(1) = %v, %v; want true, nil", ok, err)
	}
	if err := s.RemoveSignedPreKey(ctx, 1); err != nil {
		t.Fatal(err)
	}
	ok, _ = s.ContainsSignedPreKey(ctx, 1)
	if ok {
		t.Error("signed prekey still present after Remove")
	}
}

func TestStores_Session_LoadAbsent_ReturnsFreshRecord(t *testing.T) {
	s, _, _ := freshStores(t)
	addr := protocol.NewSignalAddress("peer-1", 1)

	rec, err := s.LoadSession(context.Background(), addr)
	if err != nil {
		t.Fatal(err)
	}
	if rec == nil {
		t.Fatal("LoadSession returned nil for absent session; want fresh record")
	}

	ok, err := s.ContainsSession(context.Background(), addr)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("ContainsSession true for session that has never been stored")
	}
}

func TestStores_Session_ContainsRemoveCycle(t *testing.T) {
	s, st, _ := freshStores(t)
	ctx := context.Background()
	addr := protocol.NewSignalAddress("peer-1", 1)

	if err := st.Put(sessionKey(addr.String()), []byte("placeholder")); err != nil {
		t.Fatal(err)
	}
	ok, err := s.ContainsSession(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("ContainsSession false after raw Put")
	}
	if err := s.DeleteSession(ctx, addr); err != nil {
		t.Fatal(err)
	}
	ok, err = s.ContainsSession(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("ContainsSession still true after DeleteSession")
	}
}

func TestStores_Session_DeleteAll_KeepsIdentities(t *testing.T) {
	s, st, _ := freshStores(t)
	ctx := context.Background()

	for _, name := range []string{"alice", "bob"} {
		addr := protocol.NewSignalAddress(name, 1)
		remote, _ := keyhelper.GenerateIdentityKeyPair()
		if err := s.SaveIdentity(ctx, addr, remote.PublicKey()); err != nil {
			t.Fatal(err)
		}
		if err := st.Put(sessionKey(addr.String()), []byte("placeholder")); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.DeleteAllSessions(ctx); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"alice", "bob"} {
		addr := protocol.NewSignalAddress(name, 1)
		ok, _ := s.ContainsSession(ctx, addr)
		if ok {
			t.Errorf("session for %s still present after DeleteAllSessions", name)
		}
	}

	for _, name := range []string{"alice", "bob"} {
		addr := protocol.NewSignalAddress(name, 1)
		_, err := s.GetRemoteIdentity(addr)
		if err != nil {
			t.Errorf("remote identity for %s cleared by DeleteAllSessions: %v", name, err)
		}
	}
}

func TestStores_GetSubDeviceSessions_EmptyUntilMultiDevice(t *testing.T) {

	s, _, _ := freshStores(t)
	ids, err := s.GetSubDeviceSessions(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Errorf("want empty; got %v", ids)
	}
}

func TestStores_DispenseOPK_NeverHandsTheSameKeyTwice(t *testing.T) {
	s := freshStoresN(t, LowWaterOPK+5)

	first, _, err := s.DispenseOPK()
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := s.DispenseOPK()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("both invites carry opk %d — the second pairing can never complete", first)
	}

	if _, err := s.LoadPreKey(context.Background(), first); err != nil {
		t.Fatalf("dispensed opk %d is not loadable: %v", first, err)
	}
}

func TestStores_DispenseOPK_DoesNotRecycleUnredeemedKeys(t *testing.T) {
	s := freshStoresN(t, LowWaterOPK+5)
	seen := map[uint32]bool{}
	for i := 0; i < 5; i++ {
		id, _, err := s.DispenseOPK()
		if err != nil {
			t.Fatal(err)
		}
		if seen[id] {
			t.Fatalf("opk %d was dispensed twice", id)
		}
		seen[id] = true
	}
}

func TestStores_DispenseOPK_ConsumeClearsTheMarker(t *testing.T) {
	s := freshStoresN(t, LowWaterOPK+5)
	id, _, err := s.DispenseOPK()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RemovePreKey(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.Get(opkDispensedKey(id)); err == nil {
		t.Errorf("handout marker for consumed opk %d survived", id)
	}
}

func TestStores_DispenseOPK_RefillTracksOfferableKeys(t *testing.T) {
	s := freshStoresN(t, LowWaterOPK+2)
	for i := 0; i < LowWaterOPK+2; i++ {
		if _, _, err := s.DispenseOPK(); err != nil {
			t.Fatalf("dispense %d failed — the pool ran dry despite refill: %v", i, err)
		}
	}
}

func missCaptureStores(t *testing.T, n int) (*Stores, *bytes.Buffer) {
	t.Helper()
	s := freshStoresN(t, n)
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return s, &buf
}

func missField(t *testing.T, out, key string) string {
	t.Helper()
	var hits []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, `msg="one-time prekey miss"`) {
			hits = append(hits, line)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("want exactly one prekey-miss warning, got %d — the classification cannot be read:\n%s", len(hits), out)
	}
	for _, field := range strings.Fields(hits[0]) {
		if v, ok := strings.CutPrefix(field, key+"="); ok {
			return v
		}
	}
	t.Fatalf("prekey-miss warning states no %s:\n%s", key, hits[0])
	return ""
}

func adoptOnto(t *testing.T, s *Stores) {
	t.Helper()
	source, err := Bootstrap(2)
	if err != nil {
		t.Fatal(err)
	}
	privArr := source.IdentityKeyPair.PrivateKey().Serialize()
	if err := AdoptIdentity(s.store, s.state, source.IdentityKeyPair.PublicKey().Serialize(), privArr[:]); err != nil {
		t.Fatalf("AdoptIdentity: %v", err)
	}
}

func TestStores_OPKMiss_AdoptedPoolIsNotReportedAsSpent(t *testing.T) {
	s, buf := missCaptureStores(t, 4)
	adoptOnto(t, s)

	if _, err := s.LoadPreKey(context.Background(), 1); err == nil {
		t.Fatal("LoadPreKey(1) succeeded after adoption — the pre-link pool was not deleted, so this test proves nothing")
	}

	if got := missField(t, buf.String(), "cause"); got != opkMissCauseAdoption {
		t.Fatalf("cause = %q, want %q — an id from a pool adoption deleted is being filed as a spent key", got, opkMissCauseAdoption)
	}
	if got := missField(t, buf.String(), "erased_through"); got != "4" {
		t.Fatalf("erased_through = %q, want 4 — the line does not say where the deleted pool ended", got)
	}
}

func TestStores_OPKMiss_SpentKeyOnANeverLinkedDevice(t *testing.T) {
	s, buf := missCaptureStores(t, 4)
	ctx := context.Background()

	if err := s.RemovePreKey(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadPreKey(ctx, 1); err == nil {
		t.Fatal("LoadPreKey(1) succeeded after RemovePreKey — nothing was consumed")
	}

	if got := missField(t, buf.String(), "cause"); got != opkMissCauseSpent {
		t.Fatalf("cause = %q, want %q — a consumed key on a device that never linked is being blamed on adoption", got, opkMissCauseSpent)
	}
	if got := missField(t, buf.String(), "erased_through"); got != "0" {
		t.Fatalf("erased_through = %q, want 0 — this device has never adopted an identity", got)
	}
}

func TestStores_OPKMiss_SpentKeyFromThePoolAdoptionMinted(t *testing.T) {
	s, buf := missCaptureStores(t, 4)
	adoptOnto(t, s)
	ctx := context.Background()

	var newest uint32
	for _, opk := range s.state.OneTimePreKeys {
		if id := opk.ID(); !id.IsEmpty && (newest == 0 || id.Value < newest) {
			newest = id.Value
		}
	}
	if newest <= 4 {
		t.Fatalf("the pool adoption minted starts at %d, want an id above the deleted pool's 4", newest)
	}
	if err := s.RemovePreKey(ctx, newest); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadPreKey(ctx, newest); err == nil {
		t.Fatalf("LoadPreKey(%d) succeeded after RemovePreKey", newest)
	}

	if got := missField(t, buf.String(), "cause"); got != opkMissCauseSpent {
		t.Fatalf("cause = %q, want %q — a key minted AFTER adoption and consumed normally is being blamed on adoption", got, opkMissCauseSpent)
	}
}

func TestStores_OPKMiss_IdAboveThePoolWasNeverWritten(t *testing.T) {
	s, buf := missCaptureStores(t, 4)
	adoptOnto(t, s)

	if _, err := s.LoadPreKey(context.Background(), 9999); err == nil {
		t.Fatal("LoadPreKey(9999) succeeded")
	}
	if got := missField(t, buf.String(), "cause"); got != opkMissCauseNeverWritten {
		t.Fatalf("cause = %q, want %q", got, opkMissCauseNeverWritten)
	}
}

func TestStores_OPKMiss_CorruptBoundaryNamesNoCause(t *testing.T) {
	s, buf := missCaptureStores(t, 4)
	if err := s.store.Put([]byte(opkErasedThroughKey), []byte{9}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.LoadPreKey(context.Background(), 9999); err == nil {
		t.Fatal("LoadPreKey(9999) succeeded")
	}

	out := buf.String()
	if got := missField(t, out, "cause"); got != opkMissCauseUnknown {
		t.Fatalf("cause = %q, want %q — a corrupt boundary row was read as a fact", got, opkMissCauseUnknown)
	}
	if got := missField(t, out, "pool"); got != "4" {
		t.Fatalf("pool = %q, want 4 — a corrupt boundary row erased the numbers that were readable", got)
	}
	if !strings.Contains(out, "boundary_err=") {
		t.Fatalf("the line does not say the boundary read failed:\n%s", out)
	}
}
