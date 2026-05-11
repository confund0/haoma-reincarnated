package signal

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
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

func TestStores_OPKRefill_AvailableOPKReturnsValidPostRefill(t *testing.T) {
	s := freshStoresN(t, LowWaterOPK+1)
	ctx := context.Background()

	_ = s.RemovePreKey(ctx, 1)
	_ = s.RemovePreKey(ctx, 2)

	id, pub, err := s.AvailableOPK()
	if err != nil {
		t.Fatalf("AvailableOPK: %v", err)
	}

	if id != 3 {
		t.Errorf("AvailableOPK id = %d, want 3", id)
	}

	if len(pub) != 33 {
		t.Errorf("AvailableOPK pub len = %d, want 33", len(pub))
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
