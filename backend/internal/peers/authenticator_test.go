package peers

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"haoma/internal/ids"
	"haoma/internal/xport"
)

func TestHMACVerifier_Accepts_Signed(t *testing.T) {
	r, _ := newTestRegistry(t)
	alice := freshPeer(t, "alice-onion")
	if err := r.Add(alice); err != nil {
		t.Fatal(err)
	}
	v := &HMACVerifier{Registry: r}

	env := xport.Sign(xport.Envelope{
		ID:        "msg-1",
		Timestamp: 1_700_000_000,
		From:      "alice-onion",
		Payload:   []byte("ciphertext"),
	}, alice.InboundSecret)

	if err := v.Verify(context.Background(), env); err != nil {
		t.Errorf("Verify accepted-signed envelope: %v", err)
	}

	got, err := r.Get(alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastPassiveAt == 0 {
		t.Errorf("LastPassiveAt not updated on successful verify")
	}
}

func TestHMACVerifier_RejectsUnknownSource(t *testing.T) {
	r, _ := newTestRegistry(t)
	v := &HMACVerifier{Registry: r}

	env := xport.Envelope{
		ID: "msg-1", Timestamp: 1, From: "mystery-onion", Payload: []byte("x"),
	}
	env = xport.Sign(env, make([]byte, 32))

	err := v.Verify(context.Background(), env)
	if !errors.Is(err, ErrUnknownSource) {
		t.Errorf("err = %v, want ErrUnknownSource", err)
	}
}

func TestHMACVerifier_BadMacIncrementsIDS(t *testing.T) {
	r, _ := newTestRegistry(t)
	alice := freshPeer(t, "alice-onion")
	if err := r.Add(alice); err != nil {
		t.Fatal(err)
	}
	v := &HMACVerifier{Registry: r}

	wrongSecret, _ := NewPeerSecret()
	env := xport.Sign(xport.Envelope{
		ID: "msg-spoof", Timestamp: 1, From: "alice-onion", Payload: []byte("x"),
	}, wrongSecret)

	if err := v.Verify(context.Background(), env); !errors.Is(err, xport.ErrMacMismatch) {
		t.Errorf("err = %v, want ErrMacMismatch", err)
	}

	got, err := r.Get(alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.IDSCounters["alice-onion"] != 1 {
		t.Errorf("IDSCounters[alice-onion] = %d, want 1", got.IDSCounters["alice-onion"])
	}
}

func TestHMACVerifier_InjectedNow(t *testing.T) {
	r, _ := newTestRegistry(t)
	alice := freshPeer(t, "alice-onion")
	if err := r.Add(alice); err != nil {
		t.Fatal(err)
	}
	fixed := time.Unix(1_234_567_890, 0)
	v := &HMACVerifier{Registry: r, Now: func() time.Time { return fixed }}

	env := xport.Sign(xport.Envelope{
		ID: "x", Timestamp: 1, From: "alice-onion", Payload: []byte("y"),
	}, alice.InboundSecret)

	if err := v.Verify(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastPassiveAt != fixed.Unix() {
		t.Errorf("LastPassiveAt = %d, want %d (injected Now)", got.LastPassiveAt, fixed.Unix())
	}
}

func TestHMACVerifier_SecondaryAddress(t *testing.T) {
	r, _ := newTestRegistry(t)
	alice := freshPeer(t, "alice-primary", "alice-backup")
	if err := r.Add(alice); err != nil {
		t.Fatal(err)
	}
	v := &HMACVerifier{Registry: r}

	env := xport.Sign(xport.Envelope{
		ID: "x", Timestamp: 1, From: "alice-backup", Payload: []byte("y"),
	}, alice.InboundSecret)

	if err := v.Verify(context.Background(), env); err != nil {
		t.Errorf("secondary-address verify: %v", err)
	}
}

func TestHMACVerifier_WithIDS_EmitsUnknownSourceEvent(t *testing.T) {
	r, _ := newTestRegistry(t)
	engine := ids.New()
	sub, cancel := engine.Subscribe(8)
	defer cancel()

	v := &HMACVerifier{Registry: r, IDS: engine}
	env := xport.Envelope{ID: "x", Timestamp: 1, From: "ghost-onion", Payload: []byte("y")}
	env = xport.Sign(env, make([]byte, 32))

	if err := v.Verify(context.Background(), env); !errors.Is(err, ErrUnknownSource) {
		t.Fatalf("err = %v, want ErrUnknownSource", err)
	}
	select {
	case n := <-sub:
		if n.Event.Kind != ids.EventUnknownSource || n.Event.SourceAddr != "ghost-onion" {
			t.Errorf("notification = %+v", n)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("IDS did not receive UnknownSource event")
	}
}

func TestHMACVerifier_WithIDS_BadMAC_BumpsViaRule(t *testing.T) {
	r, _ := newTestRegistry(t)
	alice := freshPeer(t, "alice-onion")
	if err := r.Add(alice); err != nil {
		t.Fatal(err)
	}
	engine := ids.New()
	for _, rule := range ids.Defaults() {
		engine.AddRule(rule)
	}

	v := &HMACVerifier{Registry: r, IDS: engine}
	wrongSecret, _ := NewPeerSecret()
	env := xport.Sign(xport.Envelope{
		ID: "x", Timestamp: 1, From: "alice-onion", Payload: []byte("y"),
	}, wrongSecret)

	if err := v.Verify(context.Background(), env); !errors.Is(err, xport.ErrMacMismatch) {
		t.Fatalf("err = %v, want ErrMacMismatch", err)
	}

	got, err := r.Get(alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.IDSCounters["alice-onion"] != 1 {
		t.Errorf("IDSCounters[alice-onion] = %d, want 1 (via IDS CounterBump)", got.IDSCounters["alice-onion"])
	}
	snap := engine.Snapshot()
	if snap.EventCounts["bad_mac"] != 1 {
		t.Errorf("ids Stats EventCounts = %v", snap.EventCounts)
	}
	if snap.ActionCounts["counter_bump"] != 1 {
		t.Errorf("ids Stats ActionCounts = %v", snap.ActionCounts)
	}
}

func TestHMACVerifier_WithIDS_Success_EmitsConnectionAccepted(t *testing.T) {
	r, _ := newTestRegistry(t)
	alice := freshPeer(t, "alice-onion")
	if err := r.Add(alice); err != nil {
		t.Fatal(err)
	}
	engine := ids.New()
	v := &HMACVerifier{Registry: r, IDS: engine}
	env := xport.Sign(xport.Envelope{
		ID: "x", Timestamp: 1, From: "alice-onion", Payload: []byte("y"),
	}, alice.InboundSecret)

	if err := v.Verify(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	snap := engine.Snapshot()
	if snap.EventCounts["connection_accepted"] != 1 {
		t.Errorf("connection_accepted count = %d, want 1", snap.EventCounts["connection_accepted"])
	}
}

func TestHMACVerifier_WithIDS_AlertReachesSubscribers(t *testing.T) {
	r, _ := newTestRegistry(t)
	alice := freshPeer(t, "alice-onion")
	if err := r.Add(alice); err != nil {
		t.Fatal(err)
	}
	engine := ids.New()

	engine.AddRule(&ids.ThresholdPerSource{
		RuleName: "eager", Kind: ids.EventBadMAC, Threshold: 1,
		Window: time.Minute, Level: ids.AlertCritical, Message: "bad mac",
	})
	sub, cancel := engine.Subscribe(4)
	defer cancel()

	v := &HMACVerifier{Registry: r, IDS: engine}
	wrongSecret, _ := NewPeerSecret()
	env := xport.Sign(xport.Envelope{
		ID: "x", Timestamp: 1, From: "alice-onion", Payload: []byte("y"),
	}, wrongSecret)
	_ = v.Verify(context.Background(), env)

	select {
	case n := <-sub:
		if n.Event.Kind != ids.EventBadMAC {
			t.Errorf("event Kind = %v", n.Event.Kind)
		}
		var sawAlert bool
		for _, a := range n.Actions {
			if _, ok := a.(ids.AlertEmit); ok {
				sawAlert = true
			}
		}
		if !sawAlert {
			t.Errorf("actions had no AlertEmit: %v", n.Actions)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("subscriber didn't get notification")
	}
}

func TestHMACVerifier_WithoutIDS_FallsBackToDirectBump(t *testing.T) {
	r, _ := newTestRegistry(t)
	alice := freshPeer(t, "alice-onion")
	if err := r.Add(alice); err != nil {
		t.Fatal(err)
	}
	v := &HMACVerifier{Registry: r}

	wrongSecret, _ := NewPeerSecret()
	env := xport.Sign(xport.Envelope{
		ID: "x", Timestamp: 1, From: "alice-onion", Payload: []byte("y"),
	}, wrongSecret)
	_ = v.Verify(context.Background(), env)

	got, err := r.Get(alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.IDSCounters["alice-onion"] != 1 {
		t.Errorf("IDSCounters = %v, want 1 (legacy direct bump path)", got.IDSCounters)
	}
}

func TestHMACVerifier_OnVerifySuccess_FiresOnVerifiedInbound(t *testing.T) {
	r, _ := newTestRegistry(t)
	alice := freshPeer(t, "alice-onion")
	if err := r.Add(alice); err != nil {
		t.Fatal(err)
	}
	var gotPeerID string
	v := &HMACVerifier{
		Registry: r,
		OnVerifySuccess: func(_ context.Context, peerID string) {
			gotPeerID = peerID
		},
	}

	env := xport.Sign(xport.Envelope{
		ID: "m", Timestamp: 1, From: "alice-onion", Payload: []byte("hi"),
	}, alice.InboundSecret)

	if err := v.Verify(context.Background(), env); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if gotPeerID != alice.ID {
		t.Errorf("OnVerifySuccess peerID = %q, want %q", gotPeerID, alice.ID)
	}
}

func TestHMACVerifier_OnVerifySuccess_FiresOnStatusInbound(t *testing.T) {

	r, _ := newTestRegistry(t)
	alice := freshPeer(t, "alice-onion")
	if err := r.Add(alice); err != nil {
		t.Fatal(err)
	}
	fired := 0
	v := &HMACVerifier{
		Registry: r,
		OnVerifySuccess: func(context.Context, string) {
			fired++
		},
	}

	env := xport.Sign(xport.Envelope{
		ID: "s", Timestamp: 1, From: "alice-onion",
		Kind:    xport.KindStatus,
		Payload: []byte(`{"state":"online"}`),
	}, alice.InboundSecret)

	if err := v.Verify(context.Background(), env); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if fired != 1 {
		t.Errorf("OnVerifySuccess fired %d times, want 1", fired)
	}
}

func TestHMACVerifier_OnVerifySuccess_NotFiredOnBadMAC(t *testing.T) {
	r, _ := newTestRegistry(t)
	alice := freshPeer(t, "alice-onion")
	if err := r.Add(alice); err != nil {
		t.Fatal(err)
	}
	fired := false
	v := &HMACVerifier{
		Registry: r,
		OnVerifySuccess: func(context.Context, string) {
			fired = true
		},
	}

	wrongSecret, _ := NewPeerSecret()
	env := xport.Sign(xport.Envelope{
		ID: "m", Timestamp: 1, From: "alice-onion", Payload: []byte("x"),
	}, wrongSecret)

	_ = v.Verify(context.Background(), env)
	if fired {
		t.Error("OnVerifySuccess fired on bad-MAC envelope; should only fire on fully-verified path")
	}
}

func TestHMACVerifier_RetiredPeer_Rejected(t *testing.T) {
	r, _ := newTestRegistry(t)
	const onion = "shared-retirement-onion"

	oldBE := testBackend(t, []string{onion})
	if _, err := r.Import(oldBE); err != nil {
		t.Fatal(err)
	}
	oldSecret, _ := hex.DecodeString(oldBE.InboundSecret)

	newBE := testBackend(t, []string{onion})
	if _, err := r.Import(newBE); err != nil {
		t.Fatal(err)
	}

	v := &HMACVerifier{Registry: r}
	env := xport.Sign(xport.Envelope{
		ID:        "relic",
		Timestamp: 1_800_000_000,
		From:      onion,
		Payload:   []byte("from the old channel"),
	}, oldSecret)

	err := v.Verify(context.Background(), env)
	if err == nil {
		t.Fatalf("retired-peer envelope verified; want rejection")
	}

	if !errors.Is(err, xport.ErrMacMismatch) {
		t.Errorf("err = %v, want ErrMacMismatch", err)
	}
}
