//go:build faults

package main

import (
	"context"
	"testing"
	"time"
)

func TestReadStallFault(t *testing.T) {
	if f := readStallFault(func(string) string { return "" }); f != nil {
		t.Fatal("the fault is on with the variable empty")
	}
	f := readStallFault(func(k string) string {
		if k == stallPeerCircuitEnv {
			return " somepeer "
		}
		return ""
	})
	if f == nil || f.peerID != "somepeer" {
		t.Fatalf("fault = %+v, want on for somepeer", f)
	}

	var off *stallFault
	off.wrap(nil, "somepeer", "tag", nil)
}

func sendsUntilAnswered(t *testing.T, fx *renewFixture, attempts int) (int, bool) {
	t.Helper()
	for i := 1; i <= attempts; i++ {
		if fx.post(t, fx.addr) {
			return i, true
		}
	}
	return attempts, false
}

func newStallFixture(t *testing.T) *renewFixture {
	t.Helper()
	fx := newRenewFixture(t)
	fx.d.stallFault = &stallFault{peerID: fx.peer.ID}
	return fx
}

func TestStallFault_StallsTheTagOfTheFirstAnswer(t *testing.T) {
	fx := newStallFixture(t)
	if !fx.post(t, fx.addr) {
		t.Fatal("the first request, before the fault is armed, was not answered")
	}
	if !fx.d.stallFault.stalls(fx.plainTag()) {
		t.Fatal("the first answer did not arm the fault on its tag")
	}
	if !fx.post(t, fx.peer.MyOnionAddr) {
		t.Fatal("the self-probe host was stalled")
	}
	fx.d.onInboundVerified(context.Background(), fx.peer.ID)
	n, ok := sendsUntilAnswered(t, fx, 3)
	if !ok {
		t.Fatalf("no request answered in %d attempts", n)
	}
	if n != 2 {
		t.Fatalf("answered on attempt %d, want 2: one stalled, then the renewal moved the tag", n)
	}
	if !fx.d.stallFault.stalls(fx.plainTag()) {
		t.Fatal("the old tag is no longer stalled: something cleared the fault")
	}
	if !fx.d.stallFault.through {
		t.Fatal("the request under the new tag was not noted")
	}
}

func TestStallFault_WithoutARenewalTheCheckFails(t *testing.T) {
	fx := newStallFixture(t)
	if !fx.post(t, fx.addr) {
		t.Fatal("the first request was not answered")
	}
	fx.d.onInboundVerified(context.Background(), fx.peer.ID)
	fx.d.routes.mu.Lock()
	fx.d.routes.state(fx.peer.ID).nextRenewAt = time.Now().Add(time.Hour)
	fx.d.routes.mu.Unlock()
	if n, ok := sendsUntilAnswered(t, fx, 4); ok {
		t.Fatalf("answered on attempt %d with no renewal; the check would pass for the wrong reason", n)
	}
}

func TestStallFault_OtherPeerUntouched(t *testing.T) {
	fx := newRenewFixture(t)
	fx.d.stallFault = &stallFault{peerID: "anotherpeer", armedTag: fx.plainTag()}
	if !fx.post(t, fx.addr) {
		t.Fatal("a peer the fault does not name was stalled")
	}
}
