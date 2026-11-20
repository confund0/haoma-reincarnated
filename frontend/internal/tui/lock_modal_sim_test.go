package tui_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"haoma-frontend/internal/ipc"
)

type lockFrameRecorder struct {
	mu     sync.Mutex
	states []bool
}

func (r *lockFrameRecorder) record(v bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states = append(r.states, v)
}

func (r *lockFrameRecorder) snapshot() []bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]bool(nil), r.states...)
}

func spinLockRecordingServer(t *testing.T, rec *lockFrameRecorder) (dir, addr string, cancel func()) {
	t.Helper()
	script := func(_ context.Context, s *ipc.Session) {
		for {
			f, err := s.Recv()
			if err != nil {
				return
			}
			if f.Type != ipc.FrameClientLockState {
				continue
			}
			var req ipc.ClientLockStateRequest
			if json.Unmarshal(f.Payload, &req) == nil {
				rec.record(req.SoftLocked)
			}
		}
	}
	return spinScriptedIPCServer(t, script)
}

func typePIN(sim tcell.SimulationScreen, pin string) {
	typeText(sim, pin)
	sim.InjectKey(tcell.KeyEnter, '\r', tcell.ModNone)
}

func TestShowPINGate_Escalation_SendsNoUnlockFrame(t *testing.T) {
	rec := &lockFrameRecorder{}
	dir, addr, cancel := spinLockRecordingServer(t, rec)
	defer cancel()

	app, sim := launchApp(t, dir, addr)

	result := make(chan bool, 1)
	go func() { result <- app.ShowPINGate("1234", 0) }()
	waitForScreen(t, sim, "Enter PIN to unlock")

	typePIN(sim, "0000")
	waitForScreen(t, sim, "Wrong PIN")
	typePIN(sim, "0000")
	waitForScreen(t, sim, "Wrong PIN")
	typePIN(sim, "0000")

	select {
	case ok := <-result:
		if ok {
			t.Fatalf("ShowPINGate returned true after 3 wrong PINs")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ShowPINGate never returned after the 3rd wrong PIN")
	}

	time.Sleep(50 * time.Millisecond)
	got := rec.snapshot()
	if len(got) != 1 || got[0] != true {
		t.Fatalf("recorded client_lock_state frames = %v, want [true] only (the lock, no unlock)", got)
	}
}

func TestShowPINGate_RightPIN_SendsUnlockFrame(t *testing.T) {
	rec := &lockFrameRecorder{}
	dir, addr, cancel := spinLockRecordingServer(t, rec)
	defer cancel()

	app, sim := launchApp(t, dir, addr)

	result := make(chan bool, 1)
	go func() { result <- app.ShowPINGate("1234", 0) }()
	waitForScreen(t, sim, "Enter PIN to unlock")

	typePIN(sim, "1234")

	select {
	case ok := <-result:
		if !ok {
			t.Fatalf("ShowPINGate returned false for the right PIN")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ShowPINGate never returned after the right PIN")
	}

	got := waitForFrameCount(t, rec, 2)
	if got[0] != true || got[1] != false {
		t.Fatalf("recorded client_lock_state frames = %v, want [true false] (lock then unlock)", got)
	}
}

func waitForFrameCount(t *testing.T, rec *lockFrameRecorder, n int) []bool {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		got := rec.snapshot()
		if len(got) >= n {
			return got
		}
		select {
		case <-deadline:
			t.Fatalf("recorded only %v after 5s, want %d frame(s)", got, n)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}
