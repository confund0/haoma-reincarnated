package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/invitation"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/pair"
	"haoma-frontend/internal/store"
	"haoma-frontend/internal/syncrail"
)

const (
	invitationTestSecret = "correct-horse-battery-staple-secret"

	invitationMintedEarly = 1742640000
	invitationMintedLate  = 1742643600

	invitationDeviceA = "0000000000000000000000000000aaaa"
	invitationDeviceB = "0000000000000000000000000000bbbb"

	invitationTheirView = "cc33dd44ee55ff6600778899aa11bb22"
)

func theirDevice(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Unlock(t.TempDir(), "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Lock() })
	return st
}

func theirTag(t *testing.T, st *store.Store) string {
	t.Helper()
	tag, err := pair.OfferingDeviceTag(st, invitationTheirView)
	if err != nil {
		t.Fatalf("derive offering tag: %v", err)
	}
	return tag
}

func invitationWrapper(t *testing.T, msgID, offeringDevice string, mintedAt, expiresAt int64) *msg.Wrapper {
	t.Helper()
	w, err := msg.BuildPairingInvitation(1, mintedAt, msgID, invitationTestSecret, offeringDevice, mintedAt, expiresAt, 0)
	if err != nil {
		t.Fatalf("BuildPairingInvitation: %v", err)
	}
	return w
}

func invitationRows(t *testing.T, d *daemon, chatID chat.ChatID) map[string]events.InvitationBody {
	t.Helper()
	rows, err := d.events.List(chatID, 0, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	out := map[string]events.InvitationBody{}
	for _, ev := range rows {
		if ev.Kind != events.KindInvitation {
			continue
		}
		var body events.InvitationBody
		if err := json.Unmarshal(ev.Body, &body); err != nil {
			t.Fatalf("decode invitation body: %v", err)
		}
		out[ev.MsgID] = body
	}
	return out
}

func wireInvitationStore(d *daemon) {
	d.invitations = invitation.New(d.store)
}

func TestInvitation_DedupeRunsBeforeSupersede(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	wireInvitationStore(d)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)
	ctx := context.Background()
	expires := time.Now().Add(24 * time.Hour).Unix()

	first := invitationWrapper(t, "inv-old", invitationDeviceA, invitationMintedEarly, expires)
	applyInboundInvitation(ctx, d, chatID, railPeerID, first)
	second := invitationWrapper(t, "inv-new", invitationDeviceA, invitationMintedLate, expires)
	applyInboundInvitation(ctx, d, chatID, railPeerID, second)

	rows := invitationRows(t, d, chatID)
	if len(rows) != 2 {
		t.Fatalf("the conversation holds %d invitation rows, want 2", len(rows))
	}
	if rows["inv-old"].State != events.InvitationSuperseded {
		t.Fatalf("the earlier invitation is %q, want superseded", rows["inv-old"].State)
	}
	if rows["inv-new"].State != events.InvitationLive {
		t.Fatalf("the newer invitation is %q, want live", rows["inv-new"].State)
	}

	applyInboundInvitation(ctx, d, chatID, railPeerID, first)

	rows = invitationRows(t, d, chatID)
	if len(rows) != 2 {
		t.Fatalf("a redelivery changed the row count to %d", len(rows))
	}
	if rows["inv-new"].State != events.InvitationLive {
		t.Fatalf("a redelivery of the older invitation retired the newer one: %q", rows["inv-new"].State)
	}
	held, err := d.invitations.Get("inv-new")
	if err != nil {
		t.Fatalf("the newer invitation is no longer held: %v", err)
	}
	if held.State != events.InvitationLive {
		t.Fatalf("the newer invitation's record is %q, want live", held.State)
	}
}

func TestInvitation_AnOlderArrivalDoesNotRetireANewerOne(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	wireInvitationStore(d)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)
	ctx := context.Background()
	expires := time.Now().Add(24 * time.Hour).Unix()

	applyInboundInvitation(ctx, d, chatID, railPeerID, invitationWrapper(t, "inv-new", invitationDeviceA, invitationMintedLate, expires))
	applyInboundInvitation(ctx, d, chatID, railPeerID, invitationWrapper(t, "inv-old", invitationDeviceA, invitationMintedEarly, expires))

	rows := invitationRows(t, d, chatID)
	if len(rows) != 1 {
		t.Fatalf("the conversation holds %d invitation rows, want only the newer one", len(rows))
	}
	if rows["inv-new"].State != events.InvitationLive {
		t.Fatalf("the newer invitation is %q, want live", rows["inv-new"].State)
	}
	if _, err := d.invitations.Get("inv-old"); !errors.Is(err, invitation.ErrNotFound) {
		t.Fatalf("the older invitation was filed anyway: %v", err)
	}
}

func TestInvitation_SupersedeRetiresOnlyOlderInvitations(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	wireInvitationStore(d)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)
	ctx := context.Background()
	expires := time.Now().Add(24 * time.Hour).Unix()

	applyInboundInvitation(ctx, d, chatID, railPeerID, invitationWrapper(t, "inv-new", invitationDeviceA, invitationMintedLate, expires))

	supersedeOlderInvitations(d, slog.Default(), chatID, "inv-old", invitationDeviceA, invitationMintedEarly)

	if state := invitationRows(t, d, chatID)["inv-new"].State; state != events.InvitationLive {
		t.Fatalf("an older invitation retired the newer one: %q", state)
	}
	held, err := d.invitations.Get("inv-new")
	if err != nil {
		t.Fatal(err)
	}
	if held.State != events.InvitationLive {
		t.Fatalf("the newer invitation's record is %q, want live", held.State)
	}
}

func TestInvitation_EachOfTheirDevicesKeepsItsOwnLiveInvitation(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	wireInvitationStore(d)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)
	ctx := context.Background()
	expires := time.Now().Add(24 * time.Hour).Unix()

	applyInboundInvitation(ctx, d, chatID, railPeerID, invitationWrapper(t, "inv-a", invitationDeviceA, invitationMintedEarly, expires))
	applyInboundInvitation(ctx, d, chatID, railPeerID, invitationWrapper(t, "inv-b", invitationDeviceB, invitationMintedLate, expires))

	rows := invitationRows(t, d, chatID)
	if len(rows) != 2 {
		t.Fatalf("the conversation holds %d invitation rows, want one per device", len(rows))
	}
	for _, id := range []string{"inv-a", "inv-b"} {
		if rows[id].State != events.InvitationLive {
			t.Fatalf("%s is %q — one device's invitation retired another device's", id, rows[id].State)
		}
		held, err := d.invitations.Get(id)
		if err != nil {
			t.Fatalf("%s is no longer held: %v", id, err)
		}
		if held.State != events.InvitationLive {
			t.Fatalf("%s's record is %q, want live", id, held.State)
		}
		if held.Secret != invitationTestSecret {
			t.Fatalf("%s is no longer answerable: the secret did not survive", id)
		}
	}

	applyInboundInvitation(ctx, d, chatID, railPeerID, invitationWrapper(t, "inv-a2", invitationDeviceA, invitationMintedLate+1, expires))

	rows = invitationRows(t, d, chatID)
	if rows["inv-a"].State != events.InvitationSuperseded {
		t.Fatalf("a device's own earlier invitation is %q, want superseded", rows["inv-a"].State)
	}
	if rows["inv-a2"].State != events.InvitationLive {
		t.Fatalf("the device's newer invitation is %q, want live", rows["inv-a2"].State)
	}
	if rows["inv-b"].State != events.InvitationLive {
		t.Fatalf("the other device's invitation is %q — it was retired by a device it has nothing to do with", rows["inv-b"].State)
	}
}

func TestInvitation_DerivedNamesKeepBothDevicesLive(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	wireInvitationStore(d)
	chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)
	ctx := context.Background()
	expires := time.Now().Add(24 * time.Hour).Unix()

	deviceA, deviceB := theirDevice(t), theirDevice(t)
	tagA, tagB := theirTag(t, deviceA), theirTag(t, deviceB)
	if tagA == tagB {
		t.Fatalf("two of their devices both named themselves %q to me", tagA)
	}

	applyInboundInvitation(ctx, d, chatID, railPeerID, invitationWrapper(t, "inv-a", tagA, invitationMintedEarly, expires))
	applyInboundInvitation(ctx, d, chatID, railPeerID, invitationWrapper(t, "inv-b", tagB, invitationMintedLate, expires))

	rows := invitationRows(t, d, chatID)
	if len(rows) != 2 {
		t.Fatalf("the conversation holds %d invitation rows, want one per device", len(rows))
	}
	for _, id := range []string{"inv-a", "inv-b"} {
		if rows[id].State != events.InvitationLive {
			t.Fatalf("%s is %q — one device's invitation retired another device's", id, rows[id].State)
		}
		held, err := d.invitations.Get(id)
		if err != nil {
			t.Fatalf("%s is no longer held: %v", id, err)
		}
		if held.Secret != invitationTestSecret {
			t.Fatalf("%s is no longer answerable: the secret did not survive", id)
		}
	}

	if again := theirTag(t, deviceA); again != tagA {
		t.Fatalf("the same device named itself %q on a second offer, was %q", again, tagA)
	}
	applyInboundInvitation(ctx, d, chatID, railPeerID, invitationWrapper(t, "inv-a2", tagA, invitationMintedLate+1, expires))

	rows = invitationRows(t, d, chatID)
	if rows["inv-a"].State != events.InvitationSuperseded {
		t.Fatalf("that device's own earlier invitation is %q, want superseded", rows["inv-a"].State)
	}
	if rows["inv-a2"].State != events.InvitationLive {
		t.Fatalf("that device's newer invitation is %q, want live", rows["inv-a2"].State)
	}
	if rows["inv-b"].State != events.InvitationLive {
		t.Fatalf("the other device's invitation is %q — retired by a device it has nothing to do with", rows["inv-b"].State)
	}
}

func TestInvitation_RefusesAnUnusableDeadline(t *testing.T) {
	cases := map[string]int64{
		"already lapsed": time.Now().Add(-time.Minute).Unix(),
		"absurdly far":   time.Now().Add(365 * 24 * time.Hour).Unix(),
	}
	for name, expires := range cases {
		t.Run(name, func(t *testing.T) {
			stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
			d, _, _, _ := newTestDaemon(t, stub)
			wireInvitationStore(d)
			chatID := seedKnownPeer(t, d, railPeerID, railFingerprint)

			applyInboundInvitation(context.Background(), d, chatID, railPeerID,
				invitationWrapper(t, "inv-1", invitationDeviceA, invitationMintedLate, expires))

			if rows := invitationRows(t, d, chatID); len(rows) != 0 {
				t.Fatalf("an unusable deadline was filed as a row: %+v", rows)
			}
			if _, err := d.invitations.Get("inv-1"); !errors.Is(err, invitation.ErrNotFound) {
				t.Fatalf("an unusable deadline left a secret at rest: %v", err)
			}
		})
	}
}

func TestInvitation_NoRowWithoutTheRecord(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	wireInvitationStore(d)

	dc, err := d.chats.CreateDirect(railPeerID)
	if err != nil {
		t.Fatal(err)
	}

	applyInboundInvitation(context.Background(), d, dc.ID, railPeerID,
		invitationWrapper(t, "inv-1", invitationDeviceA, invitationMintedLate, time.Now().Add(24*time.Hour).Unix()))

	if rows := invitationRows(t, d, dc.ID); len(rows) != 0 {
		t.Fatalf("a marker row was written for an invitation that was refused: %+v", rows)
	}
}

func TestInvitation_MasterLedgersAfterTheSend(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	seedKnownPeer(t, d, railPeerID, railFingerprint)
	ctx := context.Background()

	seedBlob, _ := makeInboundCiphertext(t, d, railPeerID, "hello", 1, time.Now().Unix(), 0)
	processInboxEntry(ctx, d, backendapi.InboxEntry{
		ArrivalAt: time.Now().UnixNano(),
		PeerID:    railPeerID,
		Envelope: backendapi.RawEnvelope{
			ID: "env-seed-1", Timestamp: time.Now().Unix(), From: "peer-onion",
			Kind: "text", Payload: seedBlob,
			Mac: []byte{1, 2, 3, 4},
		},
	})

	body := syncrail.InvitationBody{
		ID:             "inv-1",
		Fingerprint:    railFingerprint,
		OfferingDevice: invitationDeviceA,
		Secret:         invitationTestSecret,
		MintedAt:       invitationMintedLate,
		ExpiresAt:      time.Now().Add(24 * time.Hour).Unix(),
	}

	d.passOnRetryPauses = []time.Duration{time.Millisecond, time.Millisecond}
	stub.sendStatus = http.StatusInternalServerError
	sendsBefore := stub.SendCalls
	forwardInvitation(ctx, d, railSiblingID, body)
	if got := stub.SendCalls - sendsBefore; got != 3 {
		t.Fatalf("a pass-on haomad refused was tried %d time(s), want 3 — nothing else sends it again", got)
	}
	forwardInvitation(ctx, d, railSiblingID, body)
	if got := stub.SendCalls - sendsBefore; got != 6 {
		t.Fatalf("a later copy of a pass-on that never went out was tried %d time(s) in all, want 6", got)
	}

	stub.sendStatus = http.StatusAccepted
	forwardInvitation(ctx, d, railSiblingID, body)
	if got := stub.SendCalls - sendsBefore; got != 7 {
		t.Fatalf("the retry did not send: %d call(s)", got)
	}
	forwardInvitation(ctx, d, railSiblingID, body)
	if got := stub.SendCalls - sendsBefore; got != 7 {
		t.Fatalf("an invitation already passed on was passed on again: %d call(s)", got)
	}
}

type offerBackend struct {
	URL string

	mu           sync.Mutex
	handles      int
	cancelled    []string
	noteAtCancel map[string]string
	deleted      []string
	notes        *invitation.Store

	sendStatus int

	mints    int
	mintGate chan struct{}

	probe     string
	readyGate chan struct{}

	streams chan string

	readySent        bool
	sends            int
	sendsBeforeReady int
}

const (
	probeReady  = ""
	probeHeld   = "held"
	probeSilent = "silent"
	probeClosed = "closed"
)

func (b *offerBackend) handleCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.handles
}

func (b *offerBackend) sendCounts() (sends, beforeReady int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sends, b.sendsBeforeReady
}

func (b *offerBackend) observeCancel(handleID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cancelled = append(b.cancelled, handleID)
	if b.notes != nil {
		if note, err := b.notes.GetNote(railFingerprint, 0); err == nil {
			b.noteAtCancel[handleID] = note.HandleID
		} else {
			b.noteAtCancel[handleID] = ""
		}
	}
}

func (b *offerBackend) watchNotes(s *invitation.Store) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.notes = s
}

func (b *offerBackend) snapshot() ([]string, map[string]string, []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	cancelled := append([]string(nil), b.cancelled...)
	notes := map[string]string{}
	for k, v := range b.noteAtCancel {
		notes[k] = v
	}
	return cancelled, notes, append([]string(nil), b.deleted...)
}

func startOfferBackend(t *testing.T) *offerBackend {
	t.Helper()
	b := &offerBackend{noteAtCancel: map[string]string{}, streams: make(chan string, 8)}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /onion/mint", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.mints++
		gate := b.mintGate
		b.mu.Unlock()
		if gate != nil {
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(backendapi.MintedOnion{
			Address:    "stubmintAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA1",
			PrivateKey: "ZmFrZS1iYXNlNjQta2V5",
		})
	})
	mux.HandleFunc("POST /onion/del", func(w http.ResponseWriter, _ *http.Request) {
		b.mu.Lock()
		b.deleted = append(b.deleted, "onion")
		b.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /pair/onion/invite", func(w http.ResponseWriter, _ *http.Request) {
		b.mu.Lock()
		b.handles++
		handle := fmt.Sprintf("handle-%d", b.handles)
		b.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(backendapi.PairOnionInviteResponse{
			HandleID:  handle,
			Secret:    invitationTestSecret,
			ExpiresAt: time.Now().Add(7 * 24 * time.Hour).Unix(),
		})
	})
	mux.HandleFunc("DELETE /pair/onion/invite/{id}", func(w http.ResponseWriter, r *http.Request) {
		b.observeCancel(r.PathValue("id"))
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /pair/onion/wait/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		b.mu.Lock()
		handle := fmt.Sprintf("handle-%d", b.handles+1)
		mode, gate := b.probe, b.readyGate
		b.readySent = false
		b.mu.Unlock()
		select {
		case b.streams <- handle:
		default:
		}
		switch mode {
		case probeClosed:
			return
		case probeSilent:
			<-r.Context().Done()
			return
		}
		emit := func(p backendapi.PairOnionProbe) {
			body, _ := json.Marshal(p)
			_, _ = fmt.Fprintf(w, "event: pair.onion-probe\ndata: %s\n\n", body)
			flusher.Flush()
		}
		if mode == probeHeld {
			emit(backendapi.PairOnionProbe{HandleID: "handle-other", Attempt: 1, Ready: true, At: time.Now().Unix()})
			emit(backendapi.PairOnionProbe{HandleID: handle, Attempt: 1, Error: "dial failed", At: time.Now().Unix()})
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		last := backendapi.PairOnionProbe{HandleID: handle, Attempt: 2, Ready: true, At: time.Now().Unix()}
		if mode == probeGaveUp {
			last.Attempt = 30
			last.Error = probeGaveUp
		}
		b.mu.Lock()
		b.readySent = true
		b.mu.Unlock()
		emit(last)
		<-r.Context().Done()
	})
	mux.HandleFunc("POST /send", func(w http.ResponseWriter, _ *http.Request) {
		b.mu.Lock()
		b.sends++
		if !b.readySent {
			b.sendsBeforeReady++
		}
		status := b.sendStatus
		b.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(backendapi.SendResponse{EnvelopeID: "env-offer-1"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	b.URL = srv.URL
	return b
}

func offerInvitationFrame(t *testing.T, ctx context.Context, conn *websocket.Conn, fingerprint string) ipc.InvitationOfferedResponse {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		req, _ := ipc.NewFrame(ipc.FrameInvitationOffer, "offer-1", ipc.InvitationOfferRequest{Fingerprint: fingerprint})
		writeFrame(t, ctx, conn, req)
		resp := readUntil(t, ctx, conn, ipc.FrameInvitationOffered, ipc.FrameError)
		if resp.Type == ipc.FrameError {
			var ep ipc.ErrorPayload
			_ = json.Unmarshal(resp.Payload, &ep)
			if ep.Code == ipc.ReasonOfferInProgress && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
				continue
			}
			t.Fatalf("server returned error instead of %s: code=%s message=%s", ipc.FrameInvitationOffered, ep.Code, ep.Message)
		}
		var out ipc.InvitationOfferedResponse
		if err := json.Unmarshal(resp.Payload, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
}

func TestInvitation_RepeatOfferCancelsThePreviousRendezvousBeforeReplacingTheNote(t *testing.T) {
	backend := startOfferBackend(t)
	d, addr, certPath, token := newTestDaemon(t, &haomadStub{URL: backend.URL})
	wireInvitationStore(d)
	backend.watchNotes(d.invitations)
	linkSibling(t, d, railSiblingID)
	if _, err := d.chats.CreateDirectEdgeless(chat.ChatID("aa11bb22cc33dd44ee55ff6600778890"), railFingerprint); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	first := offerInvitationFrame(t, ctx, conn, railFingerprint)
	noteAfterFirst, err := d.invitations.GetNote(railFingerprint, 0)
	if err != nil {
		t.Fatalf("the first offer wrote no note: %v", err)
	}
	if cancelled, _, _ := backend.snapshot(); len(cancelled) != 0 {
		t.Fatalf("the first offer cancelled something: %v", cancelled)
	}

	second := offerInvitationFrame(t, ctx, conn, railFingerprint)
	if second.MsgID == first.MsgID {
		t.Fatal("the second offer reused the first invitation's id")
	}

	cancelled, noteAtCancel, _ := backend.snapshot()
	if len(cancelled) != 1 || cancelled[0] != noteAfterFirst.HandleID {
		t.Fatalf("cancelled %v, want exactly the first rendezvous (%s)", cancelled, noteAfterFirst.HandleID)
	}
	if got := noteAtCancel[noteAfterFirst.HandleID]; got != noteAfterFirst.HandleID {
		t.Fatalf("the note named %q when the cancel arrived, want the rendezvous being cancelled (%s) — "+
			"replacing the note first loses the only handle that can reach it",
			got, noteAfterFirst.HandleID)
	}
	noteAfterSecond, err := d.invitations.GetNote(railFingerprint, 0)
	if err != nil {
		t.Fatalf("the second offer wrote no note: %v", err)
	}
	if noteAfterSecond.HandleID == noteAfterFirst.HandleID {
		t.Fatal("the note still names the cancelled rendezvous")
	}
}

func TestInvitation_AnOfferNoSiblingTookLeavesNoNote(t *testing.T) {
	backend := startOfferBackend(t)
	backend.sendStatus = http.StatusInternalServerError
	d, addr, certPath, token := newTestDaemon(t, &haomadStub{URL: backend.URL})
	wireInvitationStore(d)
	linkSibling(t, d, railSiblingID)
	if _, err := d.chats.CreateDirectEdgeless(chat.ChatID("aa11bb22cc33dd44ee55ff6600778890"), railFingerprint); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)
	req, _ := ipc.NewFrame(ipc.FrameInvitationOffer, "offer-1", ipc.InvitationOfferRequest{Fingerprint: railFingerprint})
	writeFrame(t, ctx, conn, req)
	f := readUntil(t, ctx, conn, ipc.FrameError)
	var refused ipc.ErrorPayload
	if err := json.Unmarshal(f.Payload, &refused); err != nil {
		t.Fatal(err)
	}
	if refused.Code != reasonRailSendFailed {
		t.Fatalf("the offer was refused with %q, want %q — the fixture did not reach the exit under test", refused.Code, reasonRailSendFailed)
	}

	cancelled, _, _ := backend.snapshot()
	if len(cancelled) != 1 {
		t.Fatalf("cancelled %v, want the one rendezvous the offer published", cancelled)
	}
	if n, err := d.invitations.GetNote(railFingerprint, 0); !errors.Is(err, invitation.ErrNotFound) {
		t.Fatalf("the failed offer left its note behind: %+v (%v)", n, err)
	}
}

func (b *offerBackend) mintCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.mints
}

func TestInvitation_OneOfferAtATime(t *testing.T) {
	backend := startOfferBackend(t)
	gate := make(chan struct{})
	backend.mintGate = gate
	released := false
	release := func() {
		if !released {
			released = true
			close(gate)
		}
	}
	t.Cleanup(release)
	d, addr, certPath, token := newTestDaemon(t, &haomadStub{URL: backend.URL})
	wireInvitationStore(d)
	linkSibling(t, d, railSiblingID)
	if _, err := d.chats.CreateDirectEdgeless(chat.ChatID("aa11bb22cc33dd44ee55ff6600778890"), railFingerprint); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	first := dialTest(t, ctx, addr, certPath, token)
	second := dialTest(t, ctx, addr, certPath, token)

	req, _ := ipc.NewFrame(ipc.FrameInvitationOffer, "offer-1", ipc.InvitationOfferRequest{Fingerprint: railFingerprint})
	writeFrame(t, ctx, first, req)
	deadline := time.Now().Add(10 * time.Second)
	for backend.mintCount() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("the first offer never reached the mint")
		}
		time.Sleep(5 * time.Millisecond)
	}

	req2, _ := ipc.NewFrame(ipc.FrameInvitationOffer, "offer-2", ipc.InvitationOfferRequest{Fingerprint: railFingerprint})
	writeFrame(t, ctx, second, req2)
	f := readUntil(t, ctx, second, ipc.FrameError)
	var refused ipc.ErrorPayload
	if err := json.Unmarshal(f.Payload, &refused); err != nil {
		t.Fatal(err)
	}
	if refused.Code != ipc.ReasonOfferInProgress {
		t.Fatalf("the second offer was refused with %q, want %q", refused.Code, ipc.ReasonOfferInProgress)
	}
	if got := backend.mintCount(); got != 1 {
		t.Fatalf("%d mints with one offer in flight; the refused offer minted", got)
	}

	release()
	readUntil(t, ctx, first, ipc.FrameInvitationOffered)

	writeFrame(t, ctx, second, req2)
	readUntil(t, ctx, second, ipc.FrameInvitationOffered)
	if got := backend.mintCount(); got != 2 {
		t.Fatalf("%d mints, want 2: the first offer and the one after it", got)
	}
}

func selfCheckFixture(t *testing.T, backend *offerBackend) (*daemon, context.Context, *websocket.Conn) {
	t.Helper()
	d, addr, certPath, token := newTestDaemon(t, &haomadStub{URL: backend.URL})
	wireInvitationStore(d)
	linkSibling(t, d, railSiblingID)
	if _, err := d.chats.CreateDirectEdgeless(chat.ChatID("aa11bb22cc33dd44ee55ff6600778890"), railFingerprint); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return d, ctx, dialTest(t, ctx, addr, certPath, token)
}

func offerRefusal(t *testing.T, ctx context.Context, conn *websocket.Conn, id string) string {
	t.Helper()
	req, _ := ipc.NewFrame(ipc.FrameInvitationOffer, id, ipc.InvitationOfferRequest{Fingerprint: railFingerprint})
	writeFrame(t, ctx, conn, req)
	f := readUntil(t, ctx, conn, ipc.FrameError)
	var refused ipc.ErrorPayload
	if err := json.Unmarshal(f.Payload, &refused); err != nil {
		t.Fatal(err)
	}
	return refused.Code
}

func waitForStream(t *testing.T, ctx context.Context, backend *offerBackend) string {
	t.Helper()
	var handle string
	select {
	case handle = <-backend.streams:
	case <-ctx.Done():
		t.Fatal("the offer never opened haomad's event stream to wait for the rendezvous")
	}
	var n int
	if _, err := fmt.Sscanf(handle, "handle-%d", &n); err != nil {
		t.Fatalf("the stream reports on %q: %v", handle, err)
	}
	for backend.handleCount() < n {
		select {
		case <-ctx.Done():
			t.Fatalf("the offer never published %s", handle)
		case <-time.After(5 * time.Millisecond):
		}
	}
	return handle
}

func assertTakenBack(t *testing.T, d *daemon, backend *offerBackend, handle string, sendsBefore int) {
	t.Helper()
	if sends, _ := backend.sendCounts(); sends != sendsBefore {
		t.Fatalf("%d send(s) during a refused offer; the secret left", sends-sendsBefore)
	}
	cancelled, _, _ := backend.snapshot()
	if len(cancelled) == 0 || cancelled[len(cancelled)-1] != handle {
		t.Fatalf("cancelled %v, want the refused offer's rendezvous %s last", cancelled, handle)
	}
	if _, _, err := pair.LoadMyKeys(d.store, handle); err == nil {
		t.Fatalf("the refused offer left the keys for %s", handle)
	}
	if n, err := d.invitations.GetNote(railFingerprint, 0); err == nil && n.HandleID == handle {
		t.Fatalf("a note names the refused offer's rendezvous: %+v", n)
	}
}

func TestInvitation_TheSecretWaitsForTheRendezvousToAnswer(t *testing.T) {
	backend := startOfferBackend(t)
	backend.probe = probeHeld
	gate := make(chan struct{})
	backend.readyGate = gate
	released := false
	t.Cleanup(func() {
		if !released {
			close(gate)
		}
	})
	d, ctx, conn := selfCheckFixture(t, backend)

	req, _ := ipc.NewFrame(ipc.FrameInvitationOffer, "offer-1", ipc.InvitationOfferRequest{Fingerprint: railFingerprint})
	writeFrame(t, ctx, conn, req)
	handle := waitForStream(t, ctx, backend)

	time.Sleep(300 * time.Millisecond)
	if sends, _ := backend.sendCounts(); sends != 0 {
		t.Fatalf("%d send(s) before the rendezvous answered; the invitation left too early", sends)
	}
	if n, err := d.invitations.GetNote(railFingerprint, 0); !errors.Is(err, invitation.ErrNotFound) {
		t.Fatalf("a note during the check: %+v (%v) — the note is written only after the rendezvous answers", n, err)
	}

	released = true
	close(gate)
	readUntil(t, ctx, conn, ipc.FrameInvitationOffered)
	sends, early := backend.sendCounts()
	if early != 0 {
		t.Fatalf("%d send(s) came before the ready attempt", early)
	}
	if sends == 0 {
		t.Fatal("the rendezvous answered and no invitation went to the linked device")
	}
	if n, err := d.invitations.GetNote(railFingerprint, 0); err != nil || n.HandleID != handle {
		t.Fatalf("after the offer the note is %+v (%v), want one naming %s", n, err, handle)
	}
	if cancelled, _, _ := backend.snapshot(); len(cancelled) != 0 {
		t.Fatalf("an offer that went out cancelled %v", cancelled)
	}
}

func TestInvitation_TheSecretLeavesAfterTheRendezvousAnswers(t *testing.T) {
	backend := startOfferBackend(t)
	_, ctx, conn := selfCheckFixture(t, backend)

	got := offerInvitationFrame(t, ctx, conn, railFingerprint)
	if got.Siblings != 1 {
		t.Fatalf("handed to %d linked device(s), want 1", got.Siblings)
	}
	sends, early := backend.sendCounts()
	if sends == 0 || early != 0 {
		t.Fatalf("sends %d, before the ready attempt %d; want some, and none early", sends, early)
	}
}

func TestInvitation_AGiveUpTakesTheOfferBackAndSendsNothing(t *testing.T) {
	backend := startOfferBackend(t)
	backend.probe = probeGaveUp
	d, ctx, conn := selfCheckFixture(t, backend)

	if code := offerRefusal(t, ctx, conn, "offer-1"); code != ipc.ReasonSelfCheckFailed {
		t.Fatalf("refused with %q, want %q", code, ipc.ReasonSelfCheckFailed)
	}
	assertTakenBack(t, d, backend, "handle-1", 0)
	if _, _, deleted := backend.snapshot(); len(deleted) != 1 {
		t.Fatalf("released %d onion(s), want the one the offer minted", len(deleted))
	}
}

func TestInvitation_ACheckThatRunsOutTakesTheOfferBack(t *testing.T) {
	saved := invitationSelfCheckTimeout
	invitationSelfCheckTimeout = 300 * time.Millisecond
	t.Cleanup(func() { invitationSelfCheckTimeout = saved })
	backend := startOfferBackend(t)
	backend.probe = probeSilent
	d, ctx, conn := selfCheckFixture(t, backend)

	if code := offerRefusal(t, ctx, conn, "offer-1"); code != ipc.ReasonSelfCheckFailed {
		t.Fatalf("refused with %q, want %q", code, ipc.ReasonSelfCheckFailed)
	}
	assertTakenBack(t, d, backend, "handle-1", 0)
}

func TestInvitation_AClosedStreamTakesTheOfferBack(t *testing.T) {
	backend := startOfferBackend(t)
	backend.probe = probeClosed
	d, ctx, conn := selfCheckFixture(t, backend)

	if code := offerRefusal(t, ctx, conn, "offer-1"); code != ipc.ReasonBackendDown {
		t.Fatalf("refused with %q, want %q", code, ipc.ReasonBackendDown)
	}
	assertTakenBack(t, d, backend, "handle-1", 0)
}

func TestInvitation_APreviousInvitationStandsAfterAFailedCheck(t *testing.T) {
	backend := startOfferBackend(t)
	d, ctx, conn := selfCheckFixture(t, backend)
	offerInvitationFrame(t, ctx, conn, railFingerprint)
	sendsAfterFirst, _ := backend.sendCounts()

	backend.mu.Lock()
	backend.probe = probeGaveUp
	backend.mu.Unlock()
	if code := offerRefusal(t, ctx, conn, "offer-2"); code != ipc.ReasonSelfCheckFailed {
		t.Fatalf("refused with %q, want %q", code, ipc.ReasonSelfCheckFailed)
	}
	assertTakenBack(t, d, backend, "handle-2", sendsAfterFirst)
	if cancelled, _, _ := backend.snapshot(); len(cancelled) != 1 {
		t.Fatalf("cancelled %v, want only the refused offer's rendezvous", cancelled)
	}
	if n, err := d.invitations.GetNote(railFingerprint, 0); err != nil || n.HandleID != "handle-1" {
		t.Fatalf("the note is %+v (%v), want it still naming the previous invitation handle-1", n, err)
	}
	if _, _, err := pair.LoadMyKeys(d.store, "handle-1"); err != nil {
		t.Fatalf("the previous invitation's keys went: %v", err)
	}
}

func TestInvitation_AnAnswerDuringTheCheckSendsNoSecondInvitation(t *testing.T) {
	backend := startOfferBackend(t)
	d, ctx, conn := selfCheckFixture(t, backend)
	offerInvitationFrame(t, ctx, conn, railFingerprint)
	<-backend.streams
	sendsAfterFirst, _ := backend.sendCounts()

	gate := make(chan struct{})
	released := false
	t.Cleanup(func() {
		if !released {
			close(gate)
		}
	})
	backend.mu.Lock()
	backend.probe = probeHeld
	backend.readyGate = gate
	backend.mu.Unlock()
	req, _ := ipc.NewFrame(ipc.FrameInvitationOffer, "offer-2", ipc.InvitationOfferRequest{Fingerprint: railFingerprint})
	writeFrame(t, ctx, conn, req)
	handle := waitForStream(t, ctx, backend)

	if _, _, err := d.peerMeta.SetFingerprint("peer-answered-previous", railFingerprint); err != nil {
		t.Fatal(err)
	}
	released = true
	close(gate)

	f := readUntil(t, ctx, conn, ipc.FrameError)
	var refused ipc.ErrorPayload
	if err := json.Unmarshal(f.Payload, &refused); err != nil {
		t.Fatal(err)
	}
	if refused.Code != ipc.ReasonAlreadyPaired {
		t.Fatalf("refused with %q, want %q", refused.Code, ipc.ReasonAlreadyPaired)
	}
	assertTakenBack(t, d, backend, handle, sendsAfterFirst)
	if cancelled, _, _ := backend.snapshot(); len(cancelled) != 1 {
		t.Fatalf("cancelled %v, want only the new rendezvous; the answered one is not touched", cancelled)
	}
}
