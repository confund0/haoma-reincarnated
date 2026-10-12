package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/invitation"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/pair"
)

func (fx *offerFixture) shortWaitRetries() {
	fx.d.invitationWaitRetry = 2 * time.Millisecond
	fx.d.invitationWaitRetryMax = 10 * time.Millisecond
}

func (fx *offerFixture) waitRunning(handleID string) bool {
	_, running := fx.d.invitationWaiters.Load(handleID)
	return running
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestInvitationReattach_CompletesAPairingAnsweredWhileDown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fx := newOfferFixture(t)
	fx.backend.joinerPayload = fx.far.payload
	conn := dialTest(t, ctx, fx.addr, fx.certPath, fx.token)

	if got := fx.backend.snapshot().waits; got != 0 {
		t.Fatalf("fixture: %d wait(s) before the restart pass; nothing should be waiting", got)
	}

	reattachInvitationWaiters(ctx, fx.d)

	f := readUntil(t, ctx, conn, ipc.FramePeerPaired)
	var paired ipc.PeerPairedPush
	if err := json.Unmarshal(f.Payload, &paired); err != nil {
		t.Fatal(err)
	}
	if paired.PeerID != fx.far.invite.PeerID {
		t.Fatalf("the paired frame names %q, want the person who answered (%s)", paired.PeerID, fx.far.invite.PeerID)
	}
	eventually(t, "the wait to end", func() bool { return !fx.waitRunning(fx.handleID) })

	dc, err := fx.d.chats.GetByDirectPeer(fx.far.invite.PeerID)
	if err != nil {
		t.Fatalf("the completed pairing reached no conversation: %v", err)
	}
	if dc.ID != fx.chatID {
		t.Fatalf("the pairing landed in %q, want the inherited conversation %q", dc.ID, fx.chatID)
	}
	got := fx.backend.snapshot()
	if got.registrations != 1 {
		t.Fatalf("%d peer registration(s), want exactly one", got.registrations)
	}
	if got.waits != 1 {
		t.Fatalf("%d wait(s), want one", got.waits)
	}

	if len(got.waitHandles) != 1 || got.waitHandles[0] != fx.handleID {
		t.Fatalf("waited on %v, want the handle the note names (%s)", got.waitHandles, fx.handleID)
	}
	if row := invitationRows(t, fx.d, fx.chatID)[fx.msgID]; row.State != events.InvitationAccepted {
		t.Fatalf("the marker row is %q, want accepted", row.State)
	}
	if _, err := fx.d.invitations.GetNote(fx.far.fingerprint, 0); !errors.Is(err, invitation.ErrNotFound) {
		t.Fatalf("the note outlived the pairing: %v", err)
	}
}

func TestInvitationReattach_RunsWhenTheBackendIsReady(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fx := newOfferFixture(t)
	fx.backend.joinerPayload = fx.far.payload

	loopCtx, stopLoop := context.WithCancel(ctx)
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		eventsLoop(loopCtx, fx.d)
	}()
	t.Cleanup(func() {
		stopLoop()
		<-loopDone
	})

	eventually(t, "the pairing to complete after the backend-ready transition", func() bool {
		_, err := fx.d.chats.GetByDirectPeer(fx.far.invite.PeerID)
		return err == nil
	})
	eventually(t, "the wait to end", func() bool { return !fx.waitRunning(fx.handleID) })
	got := fx.backend.snapshot()
	if got.waits != 1 || got.registrations != 1 {
		t.Fatalf("%d wait(s) and %d registration(s), want one of each", got.waits, got.registrations)
	}
	if got.waitHandles[0] != fx.handleID {
		t.Fatalf("waited on %q, want the handle the note names (%s)", got.waitHandles[0], fx.handleID)
	}
}

func TestInvitationReattach_TriesAgainUntilHaomadKnowsTheHandle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fx := newOfferFixture(t)
	fx.shortWaitRetries()
	fx.backend.joinerPayload = fx.far.payload
	fx.backend.waitScript = []int{http.StatusNotFound, http.StatusNotFound, http.StatusRequestTimeout, http.StatusNotFound}

	reattachInvitationWaiters(ctx, fx.d)
	eventually(t, "the wait to end", func() bool { return !fx.waitRunning(fx.handleID) })

	got := fx.backend.snapshot()
	if got.waits != 5 {
		t.Fatalf("%d wait(s), want 5: four that haomad could not take, and the one that collected", got.waits)
	}
	if got.registrations != 1 {
		t.Fatalf("%d peer registration(s), want exactly one", got.registrations)
	}
	if _, err := fx.d.chats.GetByDirectPeer(fx.far.invite.PeerID); err != nil {
		t.Fatalf("the pairing did not complete: %v", err)
	}
}

func TestInvitationReattach_OneWaitPerInvitation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fx := newOfferFixture(t)
	fx.backend.joinerPayload = fx.far.payload
	gate := make(chan struct{})
	fx.backend.waitGate = gate
	released := false
	release := func() {
		if !released {
			released = true
			close(gate)
		}
	}
	t.Cleanup(release)

	reattachInvitationWaiters(ctx, fx.d)
	eventually(t, "the first wait to reach haomad", func() bool { return fx.backend.snapshot().waits == 1 })

	reattachInvitationWaiters(ctx, fx.d)
	reattachInvitationWaiters(ctx, fx.d)
	if fx.d.attachInvitationWaiter(context.Background(), fx.handleID, fx.far.fingerprint, fx.msgID) {
		t.Fatal("a second wait was started for a rendezvous that already has one")
	}
	time.Sleep(100 * time.Millisecond)
	if got := fx.backend.snapshot().waits; got != 1 {
		t.Fatalf("%d waits reached haomad for one invitation, want one", got)
	}

	release()
	eventually(t, "the wait to end", func() bool { return !fx.waitRunning(fx.handleID) })
	if got := fx.backend.snapshot(); got.registrations != 1 || got.waits != 1 {
		t.Fatalf("%d registration(s) and %d wait(s), want one of each", got.registrations, got.waits)
	}
}

func TestInvitationReattach_SkipsALapsedNote(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fx := newOfferFixture(t)
	fx.backend.waitGate = make(chan struct{})
	t.Cleanup(func() { close(fx.backend.waitGate) })

	lapsed := time.Now().Add(-time.Minute).Unix()
	if err := fx.d.invitations.PutNote(invitation.Note{
		MsgID: fx.msgID, HandleID: fx.handleID, Fingerprint: fx.far.fingerprint,
		MintedAt: lapsed - 60, ExpiresAt: lapsed,
	}); err != nil {
		t.Fatal(err)
	}
	reattachInvitationWaiters(ctx, fx.d)
	time.Sleep(100 * time.Millisecond)
	if fx.waitRunning(fx.handleID) || fx.backend.snapshot().waits != 0 {
		t.Fatal("a lapsed invitation was waited on")
	}

	if err := fx.d.invitations.PutNote(invitation.Note{
		MsgID: fx.msgID, HandleID: fx.handleID, Fingerprint: fx.far.fingerprint,
		MintedAt: fx.expires - 60, ExpiresAt: fx.expires,
	}); err != nil {
		t.Fatal(err)
	}
	reattachInvitationWaiters(ctx, fx.d)
	eventually(t, "the live invitation's wait to reach haomad", func() bool { return fx.backend.snapshot().waits == 1 })
}

func TestInvitationReattach_TakesBackAnInvitationToAPersonAlreadyReachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fx := newOfferFixture(t)
	fx.backend.waitGate = make(chan struct{})
	t.Cleanup(func() { close(fx.backend.waitGate) })

	seedKnownPeer(t, fx.d, "0123456789abcdef0123456789abcdef", fx.far.fingerprint)

	reattachInvitationWaiters(ctx, fx.d)
	time.Sleep(100 * time.Millisecond)
	got := fx.backend.snapshot()
	if fx.waitRunning(fx.handleID) || got.waits != 0 {
		t.Fatal("an invitation to a person this device can already reach was waited on")
	}
	if len(got.cancelled) != 1 || got.cancelled[0] != fx.handleID {
		t.Fatalf("cancelled %v, want the rendezvous the note names (%s)", got.cancelled, fx.handleID)
	}
	if got.deletedOnions != 0 {
		t.Fatalf("%d address(es) released; the address may back a live registration and must stay", got.deletedOnions)
	}
	if row := invitationRows(t, fx.d, fx.chatID)[fx.msgID]; row.State != events.InvitationSuperseded || row.ExpiresAt != fx.expires {
		t.Fatalf("the marker row is %q lapsing at %d, want superseded lapsing at %d", row.State, row.ExpiresAt, fx.expires)
	}
	if _, err := fx.d.invitations.GetNote(fx.far.fingerprint, 0); !errors.Is(err, invitation.ErrNotFound) {
		t.Fatalf("the note outlived the invitation that was taken back: %v", err)
	}
	if _, _, err := pair.LoadMyKeys(fx.d.store, fx.handleID); !errors.Is(err, pair.ErrMyKeysNotFound) {
		t.Fatalf("the keys outlived the invitation that was taken back: %v", err)
	}
}

func TestInvitationReattach_TakesBackAnInvitationToAnUnknownContact(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fx := newOfferFixture(t)
	const gone = "05ffeeddccbbaa99887766554433221100ffeeddccbbaa9988776655443322110f"
	if err := fx.d.invitations.PutNote(invitation.Note{
		MsgID: "inv-offer-gone", HandleID: "handle-offer-gone", Fingerprint: gone,
		MintedAt: fx.expires - 60, ExpiresAt: fx.expires,
	}); err != nil {
		t.Fatal(err)
	}
	fx.backend.waitGate = make(chan struct{})
	t.Cleanup(func() { close(fx.backend.waitGate) })

	reattachInvitationWaiters(ctx, fx.d)
	eventually(t, "the live invitation's wait to reach haomad", func() bool { return fx.backend.snapshot().waits == 1 })

	got := fx.backend.snapshot()
	if len(got.cancelled) != 1 || got.cancelled[0] != "handle-offer-gone" {
		t.Fatalf("cancelled %v, want only the rendezvous for the unknown contact", got.cancelled)
	}
	if _, err := fx.d.invitations.GetNote(gone, 0); !errors.Is(err, invitation.ErrNotFound) {
		t.Fatalf("the note for an unknown contact survived: %v", err)
	}

	if _, err := fx.d.invitations.GetNote(fx.far.fingerprint, 0); err != nil {
		t.Fatalf("the live invitation's note went too: %v", err)
	}
}

func TestInvitationReattach_AFailedCancelKeepsTheNote(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fx := newOfferFixture(t)
	fx.backend.cancelStatus = http.StatusInternalServerError
	seedKnownPeer(t, fx.d, "0123456789abcdef0123456789abcdef", fx.far.fingerprint)

	reattachInvitationWaiters(ctx, fx.d)

	if _, err := fx.d.invitations.GetNote(fx.far.fingerprint, 0); err != nil {
		t.Fatalf("the note went although the rendezvous was not cancelled: %v", err)
	}
	if row := invitationRows(t, fx.d, fx.chatID)[fx.msgID]; row.State != events.InvitationLive {
		t.Fatalf("the marker row is %q although nothing was taken back, want live", row.State)
	}
}

func TestInvitationReattach_AnUnknownHandleIsTriedOnlyWithinTheWindow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fx := newOfferFixture(t)
	fx.shortWaitRetries()
	fx.d.invitationRetryWindow = 50 * time.Millisecond
	fx.backend.waitStatus = http.StatusNotFound

	reattachInvitationWaiters(ctx, fx.d)
	eventually(t, "the wait to try more than once", func() bool { return fx.backend.snapshot().waits >= 2 })
	eventually(t, "the wait to stop at the end of the window", func() bool { return !fx.waitRunning(fx.handleID) })
	settled := fx.backend.snapshot().waits
	time.Sleep(100 * time.Millisecond)
	if got := fx.backend.snapshot().waits; got != settled {
		t.Fatalf("the wait kept trying past the window: %d waits, then %d", settled, got)
	}
	if _, err := fx.d.invitations.GetNote(fx.far.fingerprint, 0); err != nil {
		t.Fatalf("the end of the window dropped the note: %v", err)
	}

	reattachInvitationWaiters(ctx, fx.d)
	eventually(t, "the next transition to attach a new wait", func() bool { return fx.backend.snapshot().waits > settled })
}

func TestInvitationReattach_AShutdownIsNotAnEmptyWait(t *testing.T) {
	buf := &syncBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	fx := newOfferFixture(t)
	fx.backend.waitGate = make(chan struct{})
	t.Cleanup(func() { close(fx.backend.waitGate) })

	ctx, stop := context.WithCancel(context.Background())
	reattachInvitationWaiters(ctx, fx.d)
	eventually(t, "the wait to reach haomad", func() bool { return fx.backend.snapshot().waits == 1 })
	stop()
	eventually(t, "the wait to end", func() bool { return !fx.waitRunning(fx.handleID) })

	if strings.Contains(buf.String(), "the wait ended without anyone arriving") {
		t.Fatalf("a shutdown was logged as an empty wait:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "the wait for the person was attached again") {
		t.Fatal("fixture: the capture saw no INFO line at all, so it proves nothing")
	}
}

func TestInvitationReattach_ASupersededWaitStopsTrying(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fx := newOfferFixture(t)
	fx.shortWaitRetries()
	fx.backend.waitStatus = http.StatusNotFound

	reattachInvitationWaiters(ctx, fx.d)
	eventually(t, "the wait to try more than once", func() bool { return fx.backend.snapshot().waits >= 2 })

	if err := fx.d.invitations.PutNote(invitation.Note{
		MsgID: "inv-offer-2", HandleID: "handle-offer-2", Fingerprint: fx.far.fingerprint,
		MintedAt: fx.expires, ExpiresAt: fx.expires + 3600,
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the superseded wait to stop", func() bool { return !fx.waitRunning(fx.handleID) })
	settled := fx.backend.snapshot().waits
	time.Sleep(100 * time.Millisecond)
	if got := fx.backend.snapshot().waits; got != settled {
		t.Fatalf("the superseded wait kept trying: %d waits, then %d", settled, got)
	}

	note, err := fx.d.invitations.GetNote(fx.far.fingerprint, 0)
	if err != nil || note.HandleID != "handle-offer-2" {
		t.Fatalf("the newer note did not survive: %+v (%v)", note, err)
	}
	if got := fx.backend.snapshot().cancelled; len(got) != 0 {
		t.Fatalf("a wait that stopped trying cancelled %v", got)
	}
	if row := invitationRows(t, fx.d, fx.chatID)[fx.msgID]; row.State != events.InvitationLive {
		t.Fatalf("the first offer's row is %q, want it left as it was", row.State)
	}
}

func TestInvitationWaitRetryable(t *testing.T) {
	status := func(code int) error {
		return fmt.Errorf("wrapped: %w", &backendapi.StatusError{Path: "/pair/onion/wait/h", StatusCode: code, Msg: http.StatusText(code)})
	}
	cases := map[string]struct {
		err  error
		want bool
	}{
		"unknown handle":   {status(http.StatusNotFound), true},
		"long poll ended":  {status(http.StatusRequestTimeout), true},
		"cancelled":        {status(http.StatusGone), false},
		"lapsed":           {status(http.StatusGatewayTimeout), false},
		"haomad failed":    {status(http.StatusInternalServerError), false},
		"no answer at all": {errors.New("dial tcp 127.0.0.1:1: connection refused"), true},
	}
	for name, c := range cases {
		if got := invitationWaitRetryable(c.err); got != c.want {
			t.Errorf("%s: retryable = %v, want %v", name, got, c.want)
		}
	}
}
