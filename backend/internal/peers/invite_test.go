package peers

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"haoma/internal/xport"
)

func testBackend(t *testing.T, addresses []string) *BackendInvite {
	t.Helper()
	id, err := NewPeerID()
	if err != nil {
		t.Fatal(err)
	}
	secret, err := NewPeerSecret()
	if err != nil {
		t.Fatal(err)
	}
	hexS := hex.EncodeToString(secret)
	return &BackendInvite{
		PeerID:         id,
		Addresses:      addresses,
		InboundSecret:  hexS,
		OutboundSecret: hexS,
	}
}

func TestNewInvite_FreshSecret(t *testing.T) {
	inv, err := NewInvite([]string{"alice-onion"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if inv.PeerID == "" || len(inv.PeerID) != 32 {
		t.Errorf("PeerID = %q (len %d), want 32-char hex", inv.PeerID, len(inv.PeerID))
	}
	secret, err := inv.DecodeSecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(secret) != 32 {
		t.Errorf("decoded secret length = %d, want 32", len(secret))
	}
}

func TestNewInvite_ReusesSecret(t *testing.T) {
	supplied, err := NewPeerSecret()
	if err != nil {
		t.Fatal(err)
	}
	inv, err := NewInvite([]string{"bob-onion"}, supplied)
	if err != nil {
		t.Fatal(err)
	}
	got, err := inv.DecodeSecret()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, supplied) {
		t.Errorf("Invite did not reuse provided secret")
	}
}

func TestNewInvite_Rejects_EmptyAddresses(t *testing.T) {
	if _, err := NewInvite(nil, nil); err == nil {
		t.Error("expected error for empty addresses")
	}
}

func TestNewInvite_Rejects_BadSecretLength(t *testing.T) {
	if _, err := NewInvite([]string{"a"}, []byte{1, 2, 3}); err == nil {
		t.Error("expected error for short secret")
	}
}

func TestInvite_JSONRoundTrip(t *testing.T) {
	inv, err := NewInvite([]string{"a-onion"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(inv)
	if err != nil {
		t.Fatal(err)
	}
	var back Invite
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.PeerID != inv.PeerID || back.Secret != inv.Secret {
		t.Errorf("JSON round-trip mismatch: got %+v, want %+v", back, inv)
	}
}

func TestImport_AddsPeer(t *testing.T) {
	r, _ := newTestRegistry(t)
	be := testBackend(t, []string{"alice-onion"})
	retired, err := r.Import(be)
	if err != nil {
		t.Fatal(err)
	}
	if len(retired) != 0 {
		t.Errorf("fresh import retired peers: %v", retired)
	}
	p, err := r.Get(be.PeerID)
	if err != nil {
		t.Fatal(err)
	}
	inbound, _ := hex.DecodeString(be.InboundSecret)
	outbound, _ := hex.DecodeString(be.OutboundSecret)
	if !bytes.Equal(p.InboundSecret, inbound) || !bytes.Equal(p.OutboundSecret, outbound) {
		t.Errorf("secrets not imported correctly")
	}
}

func TestImport_PopulatesMyOnion(t *testing.T) {
	r, _ := newTestRegistry(t)
	be := testBackend(t, []string{"alice-onion"})
	be.MyOnionAddr = "myonion4567890123456789012345678901234567890123456789abcd"
	be.MyOnionPrivateKey = "QkFTRTY0RVhQQU5ERURLRVk="

	if _, err := r.Import(be); err != nil {
		t.Fatal(err)
	}
	p, err := r.Get(be.PeerID)
	if err != nil {
		t.Fatal(err)
	}
	if p.MyOnionAddr != be.MyOnionAddr {
		t.Errorf("MyOnionAddr = %q, want %q", p.MyOnionAddr, be.MyOnionAddr)
	}
	if p.MyOnionPrivateKey != be.MyOnionPrivateKey {
		t.Errorf("MyOnionPrivateKey = %q, want %q", p.MyOnionPrivateKey, be.MyOnionPrivateKey)
	}
}

func TestImport_RejectsBadHex(t *testing.T) {
	r, _ := newTestRegistry(t)
	be := &BackendInvite{
		PeerID:         "abcdef",
		Addresses:      []string{"a"},
		InboundSecret:  "NOT-HEX",
		OutboundSecret: "NOT-HEX",
	}
	if _, err := r.Import(be); err == nil {
		t.Error("expected error for bad-hex secret")
	}
}

func TestImport_Nil(t *testing.T) {
	r, _ := newTestRegistry(t)
	if _, err := r.Import(nil); err == nil {
		t.Error("expected error for nil invite")
	}
}

func TestInvite_TwoPartyRoundTrip(t *testing.T) {
	aliceRegistry, _ := newTestRegistry(t)
	bobRegistry, _ := newTestRegistry(t)

	const aliceOnion = "alice-onion-56chars-long-enough"
	const bobOnion = "bob-onion-56chars-different"

	aliceInv, err := NewInvite([]string{aliceOnion}, nil)
	if err != nil {
		t.Fatal(err)
	}
	aliceOutbound := aliceInv.Secret

	bobInv, err := NewInvite([]string{bobOnion}, nil)
	if err != nil {
		t.Fatal(err)
	}
	bobOutbound := bobInv.Secret

	bobImportsAlice := &BackendInvite{
		PeerID:         aliceInv.PeerID,
		Addresses:      aliceInv.Addresses,
		InboundSecret:  aliceOutbound,
		OutboundSecret: bobOutbound,
	}
	if _, err := bobRegistry.Import(bobImportsAlice); err != nil {
		t.Fatal(err)
	}

	aliceImportsBob := &BackendInvite{
		PeerID:         bobInv.PeerID,
		Addresses:      bobInv.Addresses,
		InboundSecret:  bobOutbound,
		OutboundSecret: aliceOutbound,
	}
	if _, err := aliceRegistry.Import(aliceImportsBob); err != nil {
		t.Fatal(err)
	}

	bobPeerOnAlice, err := aliceRegistry.Get(bobInv.PeerID)
	if err != nil {
		t.Fatal(err)
	}
	env := xport.Sign(xport.Envelope{
		ID:        "hello-bob",
		Timestamp: 1_700_000_000,
		From:      aliceOnion,
		Payload:   []byte("ciphertext-from-alice"),
	}, bobPeerOnAlice.OutboundSecret)

	v := &HMACVerifier{Registry: bobRegistry}
	if err := v.Verify(context.Background(), env); err != nil {
		t.Fatalf("Bob's verifier rejected Alice's envelope: %v", err)
	}

	bad := env
	bad.Payload = []byte("ciphertext-forged")
	if err := v.Verify(context.Background(), bad); !errors.Is(err, xport.ErrMacMismatch) {
		t.Errorf("tampered payload: err = %v, want ErrMacMismatch", err)
	}
}

func TestImport_Collision_RetiresOldPeer(t *testing.T) {
	r, s := newTestRegistry(t)
	const onion = "shared-onion-56chars"

	fixed := time.Unix(1_722_000_000, 0)
	r.Now = func() time.Time { return fixed }

	oldBE := testBackend(t, []string{onion})
	if _, err := r.Import(oldBE); err != nil {
		t.Fatal(err)
	}

	newBE := testBackend(t, []string{onion})
	retired, err := r.Import(newBE)
	if err != nil {
		t.Fatal(err)
	}

	if len(retired) != 1 || retired[0] != oldBE.PeerID {
		t.Fatalf("retired = %v, want [%s]", retired, oldBE.PeerID)
	}

	old, err := r.Get(oldBE.PeerID)
	if err != nil {
		t.Fatalf("Get(old): %v", err)
	}
	if old.RetiredAt != fixed.Unix() {
		t.Errorf("RetiredAt = %d, want %d", old.RetiredAt, fixed.Unix())
	}
	if old.InboundSecret != nil || old.OutboundSecret != nil {
		t.Errorf("retired secrets not nilled: in=%x out=%x", old.InboundSecret, old.OutboundSecret)
	}
	if old.KnownAddresses != nil {
		t.Errorf("old.KnownAddresses = %v, want nil", old.KnownAddresses)
	}

	id, err := s.GetAddrIndex(onion)
	if err != nil {
		t.Fatalf("GetAddrIndex: %v", err)
	}
	if string(id) != newBE.PeerID {
		t.Errorf("addr: index points to %q, want %q", id, newBE.PeerID)
	}
}

func TestImport_Collision_MultiAddressCleanup(t *testing.T) {
	r, s := newTestRegistry(t)
	const shared = "shared-addr"
	const oldOnly = "old-only-addr"

	oldBE := testBackend(t, []string{shared, oldOnly})
	if _, err := r.Import(oldBE); err != nil {
		t.Fatal(err)
	}

	newBE := testBackend(t, []string{shared, "new-only-addr"})
	if _, err := r.Import(newBE); err != nil {
		t.Fatal(err)
	}

	for _, addr := range []string{shared, "new-only-addr"} {
		id, err := s.GetAddrIndex(addr)
		if err != nil {
			t.Fatalf("GetAddrIndex(%s): %v", addr, err)
		}
		if string(id) != newBE.PeerID {
			t.Errorf("addr:%s points to %q, want %q", addr, id, newBE.PeerID)
		}
	}

	if _, err := s.GetAddrIndex(oldOnly); err == nil {
		t.Errorf("addr:%s still present; want cleared", oldOnly)
	}
}

func TestImport_Collision_MultipleDisplacedPeers(t *testing.T) {
	r, _ := newTestRegistry(t)
	const addrA = "addr-a"
	const addrB = "addr-b"

	peer1 := testBackend(t, []string{addrA})
	peer2 := testBackend(t, []string{addrB})
	if _, err := r.Import(peer1); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Import(peer2); err != nil {
		t.Fatal(err)
	}

	merged := testBackend(t, []string{addrA, addrB})
	retired, err := r.Import(merged)
	if err != nil {
		t.Fatal(err)
	}

	if len(retired) != 2 {
		t.Fatalf("retired = %v, want 2 entries", retired)
	}
	gotSet := map[string]bool{retired[0]: true, retired[1]: true}
	if !gotSet[peer1.PeerID] || !gotSet[peer2.PeerID] {
		t.Errorf("retired = %v, want [%s %s] in any order", retired, peer1.PeerID, peer2.PeerID)
	}

	for _, pid := range []string{peer1.PeerID, peer2.PeerID} {
		p, err := r.Get(pid)
		if err != nil {
			t.Fatalf("Get(%s): %v", pid, err)
		}
		if p.RetiredAt == 0 || p.InboundSecret != nil || p.OutboundSecret != nil || p.KnownAddresses != nil {
			t.Errorf("peer %s not retired cleanly: %+v", pid, p)
		}
	}
}

func TestImport_NoCollision_LeavesExistingPeersAlone(t *testing.T) {
	r, _ := newTestRegistry(t)

	aliceBE := testBackend(t, []string{"alice-onion"})
	if _, err := r.Import(aliceBE); err != nil {
		t.Fatal(err)
	}

	bobBE := testBackend(t, []string{"bob-onion"})
	retired, err := r.Import(bobBE)
	if err != nil {
		t.Fatal(err)
	}
	if len(retired) != 0 {
		t.Errorf("non-colliding import retired %v", retired)
	}

	alice, err := r.Get(aliceBE.PeerID)
	if err != nil {
		t.Fatal(err)
	}
	if alice.RetiredAt != 0 || alice.InboundSecret == nil || alice.OutboundSecret == nil || len(alice.KnownAddresses) == 0 {
		t.Errorf("alice disturbed by non-colliding bob import: %+v", alice)
	}
}
