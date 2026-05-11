package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/notify"
	"haoma-frontend/internal/peerstate"
	"haoma-frontend/internal/store"
)

type fakeNotifyRunner struct {
	mu    sync.Mutex
	Calls [][]string
}

func (f *fakeNotifyRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	full := append([]string{name}, args...)
	f.Calls = append(f.Calls, full)
	return "1", nil
}

func (f *fakeNotifyRunner) snapshot() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]string, len(f.Calls))
	for i, c := range f.Calls {
		out[i] = append([]string(nil), c...)
	}
	return out
}

func notifyTestDaemon(t *testing.T) (*daemon, *fakeNotifyRunner) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Unlock(dir, "test-pass")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Lock() })

	runner := &fakeNotifyRunner{}
	lp := func(name string) (string, error) {
		if name == "notify-send" {
			return "/usr/bin/notify-send", nil
		}
		return "", errors.New("not found")
	}

	d := &daemon{
		store:    st,
		chats:    chat.NewStore(st),
		peerMeta: peerstate.NewMeta(st),
		notifier: notify.New(runner, lp, "linux"),
	}

	d.settingsSnapshot.Store(&ipc.Settings{
		NotifyShellEnabled:  false,
		NotifyShowSender:    false,
		NotifyShowBody:      false,
		NotificationsOnLock: true,
	})
	return d, runner
}

func waitForNotifyCalls(t *testing.T, r *fakeNotifyRunner, want int) [][]string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got := r.snapshot()
		if len(got) >= want {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("waitForNotifyCalls: wanted %d call(s); saw %d (%v)", want, len(r.snapshot()), r.snapshot())
	return nil
}

func assertNoCalls(t *testing.T, r *fakeNotifyRunner) {
	t.Helper()
	time.Sleep(50 * time.Millisecond)
	if calls := r.snapshot(); len(calls) != 0 {
		t.Fatalf("expected no shellouts; got %d (%v)", len(calls), calls)
	}
}

func makeChat(t *testing.T, d *daemon, peerID string) chat.ChatID {
	t.Helper()
	dc, err := d.chats.CreateDirect(peerID)
	if err != nil {
		t.Fatal(err)
	}
	return dc.ID
}

func TestEmitInbound_SuppressedWhenShellDisabled(t *testing.T) {
	d, r := notifyTestDaemon(t)
	chatID := makeChat(t, d, "peer1")
	emitInboundNotification(context.Background(), d, chatID, "peer1", "hello")
	assertNoCalls(t, r)
}

func TestEmitInbound_FiresWhenShellEnabled(t *testing.T) {
	d, r := notifyTestDaemon(t)
	d.settingsSnapshot.Store(&ipc.Settings{
		NotifyShellEnabled:  true,
		NotificationsOnLock: true,
	})
	chatID := makeChat(t, d, "peer1")
	emitInboundNotification(context.Background(), d, chatID, "peer1", "hello")
	calls := waitForNotifyCalls(t, r, 1)
	if calls[0][0] != "/usr/bin/notify-send" {
		t.Errorf("expected notify-send; got %q", calls[0][0])
	}
}

func TestEmitInbound_SuppressedWhenChatFocused(t *testing.T) {
	d, r := notifyTestDaemon(t)
	d.settingsSnapshot.Store(&ipc.Settings{NotifyShellEnabled: true, NotificationsOnLock: true})
	chatID := makeChat(t, d, "peer1")
	focus := ipc.ClientFocusRequest{ChatID: string(chatID), ScrollPosition: 0}
	d.clientFocus.Store(&focus)
	emitInboundNotification(context.Background(), d, chatID, "peer1", "hello")
	assertNoCalls(t, r)
}

func TestEmitInbound_FiresWhenScrolledIntoHistory(t *testing.T) {

	d, r := notifyTestDaemon(t)
	d.settingsSnapshot.Store(&ipc.Settings{NotifyShellEnabled: true, NotificationsOnLock: true})
	chatID := makeChat(t, d, "peer1")
	focus := ipc.ClientFocusRequest{ChatID: string(chatID), ScrollPosition: 5}
	d.clientFocus.Store(&focus)
	emitInboundNotification(context.Background(), d, chatID, "peer1", "hello")
	waitForNotifyCalls(t, r, 1)
}

func TestEmitInbound_SuppressedWhenSoftLockedAndOptedOut(t *testing.T) {
	d, r := notifyTestDaemon(t)
	d.settingsSnapshot.Store(&ipc.Settings{
		NotifyShellEnabled:  true,
		NotificationsOnLock: false,
	})
	d.clientSoftLocked.Store(true)
	chatID := makeChat(t, d, "peer1")
	emitInboundNotification(context.Background(), d, chatID, "peer1", "hello")
	assertNoCalls(t, r)
}

func TestEmitInbound_FiresWhenSoftLockedAndOptedIn(t *testing.T) {
	d, r := notifyTestDaemon(t)
	d.settingsSnapshot.Store(&ipc.Settings{
		NotifyShellEnabled:  true,
		NotificationsOnLock: true,
	})
	d.clientSoftLocked.Store(true)
	chatID := makeChat(t, d, "peer1")
	emitInboundNotification(context.Background(), d, chatID, "peer1", "hello")
	waitForNotifyCalls(t, r, 1)
}

func TestEmitInbound_SuppressedWhenChatMuted(t *testing.T) {
	d, r := notifyTestDaemon(t)
	d.settingsSnapshot.Store(&ipc.Settings{NotifyShellEnabled: true, NotificationsOnLock: true})
	chatID := makeChat(t, d, "peer1")
	if err := d.chats.SetNotificationsMuted(chatID, true); err != nil {
		t.Fatal(err)
	}
	emitInboundNotification(context.Background(), d, chatID, "peer1", "hello")
	assertNoCalls(t, r)
}

func TestEmitInbound_OneChatMutedDoesNotSilenceAnother(t *testing.T) {
	d, r := notifyTestDaemon(t)
	d.settingsSnapshot.Store(&ipc.Settings{NotifyShellEnabled: true, NotificationsOnLock: true})
	mutedID := makeChat(t, d, "annoying-prick")
	loudID := makeChat(t, d, "best-friend")
	if err := d.chats.SetNotificationsMuted(mutedID, true); err != nil {
		t.Fatal(err)
	}
	emitInboundNotification(context.Background(), d, mutedID, "annoying-prick", "ugh")
	emitInboundNotification(context.Background(), d, loudID, "best-friend", "hey!")
	calls := waitForNotifyCalls(t, r, 1)
	if len(calls) != 1 {
		t.Errorf("expected exactly 1 call (loud chat); got %d", len(calls))
	}
}

func TestEmitInbound_PrivacyMatrix_BothOff(t *testing.T) {
	d, r := notifyTestDaemon(t)
	d.settingsSnapshot.Store(&ipc.Settings{
		NotifyShellEnabled: true, NotifyShowSender: false, NotifyShowBody: false,
		NotificationsOnLock: true,
	})
	chatID := makeChat(t, d, "peer1")
	emitInboundNotification(context.Background(), d, chatID, "peer1", "secret")
	calls := waitForNotifyCalls(t, r, 1)
	args := calls[0]
	title := args[len(args)-2]
	body := args[len(args)-1]
	if title != "Haoma" || body != "New message" {
		t.Errorf("(both off) expected (Haoma, New message); got (%q, %q)", title, body)
	}
}

func TestEmitInbound_PrivacyMatrix_BothOn(t *testing.T) {
	d, r := notifyTestDaemon(t)
	d.settingsSnapshot.Store(&ipc.Settings{
		NotifyShellEnabled: true, NotifyShowSender: true, NotifyShowBody: true,
		NotificationsOnLock: true,
	})
	chatID := makeChat(t, d, "peer1")
	if _, err := d.peerMeta.SetAlias("peer1", "Alice"); err != nil {
		t.Fatal(err)
	}
	emitInboundNotification(context.Background(), d, chatID, "peer1", "hello world")
	calls := waitForNotifyCalls(t, r, 1)
	args := calls[0]
	title := args[len(args)-2]
	body := args[len(args)-1]
	if title != "Alice" || body != "hello world" {
		t.Errorf("(both on) expected (Alice, hello world); got (%q, %q)", title, body)
	}
}

func TestEmitInbound_NilSnapshotIsSafe(t *testing.T) {
	d, r := notifyTestDaemon(t)
	d.settingsSnapshot.Store(nil)
	chatID := makeChat(t, d, "peer1")
	emitInboundNotification(context.Background(), d, chatID, "peer1", "hi")
	assertNoCalls(t, r)
}

func TestHandleClientLockState_FlipsAtomic(t *testing.T) {
	d, _ := notifyTestDaemon(t)
	sd := newSessionDispatcher(d)

	frame, _ := ipc.NewFrame(ipc.FrameClientLockState, "", ipc.ClientLockStateRequest{SoftLocked: true})
	sd.handleClientLockState(nil, frame)
	if !d.clientSoftLocked.Load() {
		t.Errorf("expected SoftLocked=true after frame")
	}

	frame2, _ := ipc.NewFrame(ipc.FrameClientLockState, "", ipc.ClientLockStateRequest{SoftLocked: false})
	sd.handleClientLockState(nil, frame2)
	if d.clientSoftLocked.Load() {
		t.Errorf("expected SoftLocked=false after second frame")
	}
}
