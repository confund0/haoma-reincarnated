package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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
	"haoma-frontend/internal/signal"
	"haoma-frontend/internal/store"
)

type farDevice struct {
	invite      *pair.Invite
	payload     []byte
	fingerprint string
}

func newFarDevice(t *testing.T, onion, nick string) farDevice {
	t.Helper()
	st, err := store.Unlock(t.TempDir(), "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Lock() })
	state, _, err := signal.LoadOrBootstrap(st, 4)
	if err != nil {
		t.Fatal(err)
	}
	inv, _, err := pair.Build(state, signal.NewStores(st, state), []string{onion}, nick, nil)
	if err != nil {
		t.Fatalf("build the far side's invite: %v", err)
	}
	payload, err := inv.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	fp, err := inv.IdentityFingerprint()
	if err != nil {
		t.Fatalf("derive the far side's fingerprint: %v", err)
	}
	return farDevice{invite: inv, payload: payload, fingerprint: fp}
}

const acceptRefusalBody = "dial vsomeonionaddressthatmustnotcrossbackaaaaaaaaaaaaaaaaaaa.onion failed"

type acceptBackend struct {
	URL string

	mu sync.Mutex

	registrations int

	mints        int
	accepts      int
	acceptStatus int

	inviterPayload []byte

	joinerPayload []byte
	waitStatus    int

	waits       int
	waitHandles []string
	waitScript  []int
	waitGate    chan struct{}

	cancelStatus  int
	deletedOnions int
	cancelled     []string
	sends         int

	sent []backendapi.SendRequest

	invited        [][]byte
	sendProbe      func() bool
	sendProbeSeen  []bool
	acceptedSecret string

	dialStarted chan struct{}
	dialGate    chan struct{}
}

func (b *acceptBackend) snapshot() acceptBackend {
	b.mu.Lock()
	defer b.mu.Unlock()
	return acceptBackend{
		registrations: b.registrations,
		waits:         b.waits,
		waitHandles:   append([]string(nil), b.waitHandles...),
		mints:         b.mints,
		accepts:       b.accepts,
		deletedOnions: b.deletedOnions,
		cancelled:     append([]string(nil), b.cancelled...),
		sends:         b.sends,
		sent:          append([]backendapi.SendRequest(nil), b.sent...),
		invited:       append([][]byte(nil), b.invited...),
		sendProbeSeen: append([]bool(nil), b.sendProbeSeen...),
	}
}

func (b *acceptBackend) holdTheDial(t *testing.T) (started <-chan struct{}, release func()) {
	t.Helper()
	b.mu.Lock()
	b.dialStarted = make(chan struct{}, 1)
	b.dialGate = make(chan struct{})
	gate := b.dialGate
	b.mu.Unlock()
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	return b.dialStarted, release
}

func (b *acceptBackend) watchSends(probe func() bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sendProbe = probe
}

func startAcceptBackend(t *testing.T) *acceptBackend {
	t.Helper()
	b := &acceptBackend{acceptStatus: http.StatusOK, waitStatus: http.StatusOK}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /onion/mint", func(w http.ResponseWriter, _ *http.Request) {
		b.mu.Lock()
		b.mints++
		b.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(backendapi.MintedOnion{
			Address:    "stubmintAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA1",
			PrivateKey: "ZmFrZS1iYXNlNjQta2V5",
		})
	})
	mux.HandleFunc("POST /onion/del", func(w http.ResponseWriter, _ *http.Request) {
		b.mu.Lock()
		b.deletedOnions++
		b.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /peers", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.registrations++
		b.mu.Unlock()
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("GET /peers/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(backendapi.Peer{ID: r.PathValue("id")})
	})
	mux.HandleFunc("POST /pair/onion/accept", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req backendapi.PairOnionAcceptRequest
		_ = json.Unmarshal(body, &req)
		b.mu.Lock()
		b.accepts++
		b.acceptedSecret = req.Secret
		status := b.acceptStatus
		payload := b.inviterPayload
		started, gate := b.dialStarted, b.dialGate
		b.mu.Unlock()
		if gate != nil {
			select {
			case started <- struct{}{}:
			default:
			}
			<-gate
		}
		if status != http.StatusOK {

			w.WriteHeader(status)
			_, _ = io.WriteString(w, acceptRefusalBody)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(backendapi.PairOnionAcceptResponse{InviterPayload: payload})
	})
	mux.HandleFunc("POST /pair/onion/wait/{id}", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.waits++
		b.waitHandles = append(b.waitHandles, r.PathValue("id"))
		scripted := 0
		if len(b.waitScript) > 0 {
			scripted, b.waitScript = b.waitScript[0], b.waitScript[1:]
		}
		status, payload, gate := b.waitStatus, b.joinerPayload, b.waitGate
		b.mu.Unlock()
		if scripted != 0 {
			w.WriteHeader(scripted)
			return
		}
		if gate != nil {
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(backendapi.PairOnionWaitResponse{JoinerPayload: payload})
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
		<-r.Context().Done()
	})
	mux.HandleFunc("DELETE /pair/onion/invite/{id}", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		status := b.cancelStatus
		if status == 0 {
			b.cancelled = append(b.cancelled, r.PathValue("id"))
			status = http.StatusNoContent
		}
		b.mu.Unlock()
		w.WriteHeader(status)
	})
	mux.HandleFunc("POST /pair/onion/invite", func(w http.ResponseWriter, r *http.Request) {
		var req backendapi.PairOnionInviteRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		b.mu.Lock()
		b.invited = append(b.invited, req.Payload)
		handle := fmt.Sprintf("handle-onion-%d", len(b.invited))
		b.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(backendapi.PairOnionInviteResponse{
			HandleID:  handle,
			Words:     []string{"one", "two", "three", "four", "five", "six", "seven"},
			ExpiresAt: time.Now().Add(time.Hour).Unix(),
		})
	})
	mux.HandleFunc("POST /send", func(w http.ResponseWriter, r *http.Request) {
		var req backendapi.SendRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		b.mu.Lock()
		b.sends++
		b.sent = append(b.sent, req)
		if b.sendProbe != nil {
			b.sendProbeSeen = append(b.sendProbeSeen, b.sendProbe())
		}
		b.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(backendapi.SendResponse{EnvelopeID: "env-accept-1"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	b.URL = srv.URL
	return b
}

func heldInvitation(t *testing.T, d *daemon, chatID chat.ChatID, msgID, secret string, expiresAt int64) invitation.Record {
	t.Helper()
	w, err := msg.BuildPairingInvitation(1, invitationMintedLate, msgID, secret, invitationDeviceA, invitationMintedLate, expiresAt, 0)
	if err != nil {
		t.Fatalf("BuildPairingInvitation: %v", err)
	}
	applyInboundInvitation(context.Background(), d, chatID, railPeerID, w)
	rec, err := d.invitations.Get(msgID)
	if err != nil {
		t.Fatalf("the invitation was not filed: %v", err)
	}
	return rec
}

func acceptFrame(t *testing.T, ctx context.Context, conn *websocket.Conn, msgID string, want ipc.FrameType) ipc.Frame {
	t.Helper()
	req, _ := ipc.NewFrame(ipc.FrameInvitationAccept, "accept-"+msgID, ipc.InvitationAcceptRequest{MsgID: msgID})
	writeFrame(t, ctx, conn, req)
	return readUntil(t, ctx, conn, want)
}

func directChats(t *testing.T, d *daemon) []*chat.DirectChat {
	t.Helper()
	all, err := d.chats.List()
	if err != nil {
		t.Fatalf("List chats: %v", err)
	}
	var out []*chat.DirectChat
	for _, c := range all {
		if dc, ok := c.(*chat.DirectChat); ok {
			out = append(out, dc)
		}
	}
	return out
}

type acceptFixture struct {
	d         *daemon
	backend   *acceptBackend
	conn      *websocket.Conn
	chatID    chat.ChatID
	far       farDevice
	msgID     string
	secret    string
	expiresAt int64

	addr     string
	certPath string
	token    string
}

func (fx *acceptFixture) dial(t *testing.T, ctx context.Context) *websocket.Conn {
	t.Helper()
	return dialTest(t, ctx, fx.addr, fx.certPath, fx.token)
}

func (fx *acceptFixture) lapseAt(t *testing.T, expiresAt int64) {
	t.Helper()
	rec, err := fx.d.invitations.Get(fx.msgID)
	if err != nil {
		t.Fatalf("the invitation was not held: %v", err)
	}
	rec.ExpiresAt = expiresAt
	if err := fx.d.invitations.Put(rec); err != nil {
		t.Fatalf("restate the deadline: %v", err)
	}
	fx.expiresAt = expiresAt
}

func newAcceptFixture(t *testing.T, ctx context.Context, secret string) *acceptFixture {
	t.Helper()
	backend := startAcceptBackend(t)
	d, addr, certPath, token := newTestDaemon(t, &haomadStub{URL: backend.URL})
	wireInvitationStore(d)
	far := newFarDevice(t, "faronionAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA2", "them")

	chatID := seedKnownPeer(t, d, railPeerID, far.fingerprint)
	expires := time.Now().Add(24 * time.Hour).Unix()
	rec := heldInvitation(t, d, chatID, "inv-accept-1", secret, expires)
	if rec.Fingerprint != far.fingerprint {
		t.Fatalf("fixture: the record names %q, want the conversation's person", rec.Fingerprint)
	}
	conn := dialTest(t, ctx, addr, certPath, token)
	return &acceptFixture{
		d: d, backend: backend, conn: conn, chatID: chatID, far: far,
		msgID: "inv-accept-1", secret: secret, expiresAt: expires,
		addr: addr, certPath: certPath, token: token,
	}
}

func TestInvitationAccept_FoldsIntoTheConversationThePersonAlreadyHas(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fx := newAcceptFixture(t, ctx, invitationTestSecret)
	fx.backend.inviterPayload = fx.far.payload

	resp := acceptFrame(t, ctx, fx.conn, fx.msgID, ipc.FrameInvitationAccepted)
	var out ipc.InvitationAcceptedResponse
	if err := json.Unmarshal(resp.Payload, &out); err != nil {
		t.Fatal(err)
	}
	if out.PeerID != fx.far.invite.PeerID {
		t.Fatalf("the accept reports peer %q, want the one the rendezvous answered with", out.PeerID)
	}
	if out.ChatID != string(fx.chatID) {
		t.Fatalf("the pairing landed in %q, but that person's conversation is %q — "+
			"one person is one conversation", out.ChatID, fx.chatID)
	}
	if out.IdentityFingerprint != fx.far.fingerprint {
		t.Fatalf("the accept reports fingerprint %q, want %q", out.IdentityFingerprint, fx.far.fingerprint)
	}

	chats := directChats(t, fx.d)
	if len(chats) != 1 {
		t.Fatalf("that person now has %d conversations; the fresh pairing was filed as a new one", len(chats))
	}
	routes := chats[0].FanoutPeerIDs()
	if len(routes) != 2 {
		t.Fatalf("the conversation routes %v, want both the pairing it had and the one just built", routes)
	}

	if fx.backend.acceptedSecret != invitationTestSecret {
		t.Fatal("the dial did not carry the secret the invitation was filed with")
	}
	held, err := fx.d.invitations.Get(fx.msgID)
	if err != nil {
		t.Fatalf("the record was deleted rather than kept as a tombstone: %v", err)
	}
	if held.State != events.InvitationAccepted {
		t.Fatalf("the record is %q, want accepted", held.State)
	}
	if held.Secret != "" {
		t.Fatal("the secret survived the rendezvous it addressed")
	}
	if rows := invitationRows(t, fx.d, fx.chatID); rows[fx.msgID].State != events.InvitationAccepted {
		t.Fatalf("the marker row is %q, want accepted", rows[fx.msgID].State)
	}
}

func TestInvitationAccept_MismatchedIdentityRegistersNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fx := newAcceptFixture(t, ctx, invitationTestSecret)
	stranger := newFarDevice(t, "strangerAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA3", "not-them")
	fx.backend.inviterPayload = stranger.payload

	errFrame := acceptFrame(t, ctx, fx.conn, fx.msgID, ipc.FrameError)
	var ep ipc.ErrorPayload
	if err := json.Unmarshal(errFrame.Payload, &ep); err != nil {
		t.Fatal(err)
	}
	if ep.Code != reasonFingerprintMismatch {
		t.Fatalf("refused with %q, want %q", ep.Code, reasonFingerprintMismatch)
	}

	got := fx.backend.snapshot()
	if got.registrations != 0 {
		t.Fatalf("%d peer registration(s) happened for an identity that was then refused — "+
			"the check has to precede the registration, not follow it", got.registrations)
	}
	if _, err := fx.d.chats.GetByDirectPeer(stranger.invite.PeerID); !errors.Is(err, chat.ErrNotFound) {
		t.Fatalf("the stranger reached a conversation: %v", err)
	}
	chats := directChats(t, fx.d)
	if len(chats) != 1 {
		t.Fatalf("the refused pairing left %d conversations", len(chats))
	}
	for _, route := range chats[0].FanoutPeerIDs() {
		if route == stranger.invite.PeerID {
			t.Fatal("the refused pairing became a route into that person's conversation")
		}
	}
	if rec, err := fx.d.peerMeta.Get(stranger.invite.PeerID); err == nil && rec.Fingerprint != "" {
		t.Fatal("the refused identity was recorded against a peer record")
	}

	held, err := fx.d.invitations.Get(fx.msgID)
	if err != nil {
		t.Fatalf("the record was deleted rather than kept as a tombstone: %v", err)
	}
	if held.State != events.InvitationFailed {
		t.Fatalf("the record is %q, want failed", held.State)
	}
	if held.Secret != "" {
		t.Fatal("a spent secret was left at rest")
	}
}

func TestInvitationAccept_ASecondAcceptIsRefusedEvenMidDial(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fx := newAcceptFixture(t, ctx, invitationTestSecret)
	fx.backend.inviterPayload = fx.far.payload
	started, release := fx.backend.holdTheDial(t)

	defer func() {
		if got := fx.backend.snapshot(); got.accepts > 1 || got.mints > 1 {
			t.Errorf("a second accept of one row got through while the first was mid-dial: "+
				"%d dial(s) and %d minted address(es) — each one publishes an address for nobody "+
				"and burns a one-time prekey behind it", got.accepts, got.mints)
		}
	}()

	first := make(chan ipc.Frame, 1)
	go func() {
		req, _ := ipc.NewFrame(ipc.FrameInvitationAccept, "accept-first", ipc.InvitationAcceptRequest{MsgID: fx.msgID})
		writeFrame(t, ctx, fx.conn, req)
		first <- readUntil(t, ctx, fx.conn, ipc.FrameInvitationAccepted)
	}()

	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("the first accept never reached the rendezvous")
	}

	second := fx.dial(t, ctx)
	errFrame := acceptFrame(t, ctx, second, fx.msgID, ipc.FrameError)
	var ep ipc.ErrorPayload
	if err := json.Unmarshal(errFrame.Payload, &ep); err != nil {
		t.Fatal(err)
	}
	if ep.Code != reasonInvitationNotLive {
		t.Fatalf("the second accept was refused with %q, want %q", ep.Code, reasonInvitationNotLive)
	}

	release()
	select {
	case <-first:
	case <-ctx.Done():
		t.Fatal("the first accept never completed")
	}

	got := fx.backend.snapshot()
	if got.accepts != 1 {
		t.Fatalf("the rendezvous was dialled %d times; it answers exactly once", got.accepts)
	}
	if got.mints != 1 {
		t.Fatalf("%d addresses were minted for one invitation — the second accept published one for nobody "+
			"and burnt a one-time prekey behind it", got.mints)
	}
	if got.registrations != 1 {
		t.Fatalf("%d peer registration(s) for one invitation", got.registrations)
	}
}

func TestInvitationAccept_OnlyADeterministicRefusalSettlesTheInvitation(t *testing.T) {
	cases := map[string]struct {
		status  int
		reason  string
		settled bool
	}{
		"nothing usable came back": {http.StatusBadGateway, reasonRendezvousUnreachable, false},
		"daemon not ready":         {http.StatusServiceUnavailable, reasonNotReady, false},
		"refused the mac":          {http.StatusUnauthorized, reasonRendezvousRefused, true},
		"request refused":          {http.StatusBadRequest, reasonBadRequest, true},
		"request not understood":   {http.StatusUnsupportedMediaType, reasonBadRequest, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			fx := newAcceptFixture(t, ctx, invitationTestSecret)
			fx.backend.acceptStatus = tc.status

			errFrame := acceptFrame(t, ctx, fx.conn, fx.msgID, ipc.FrameError)
			var ep ipc.ErrorPayload
			if err := json.Unmarshal(errFrame.Payload, &ep); err != nil {
				t.Fatal(err)
			}
			if ep.Code != tc.reason {
				t.Fatalf("refused with %q, want %q", ep.Code, tc.reason)
			}

			if strings.Contains(ep.Message, ".onion") || strings.Contains(ep.Message, acceptRefusalBody) {
				t.Fatalf("the daemon's answer reached the client: %q", ep.Message)
			}

			held, err := fx.d.invitations.Get(fx.msgID)
			if err != nil {
				t.Fatalf("the record went missing: %v", err)
			}
			if tc.settled {
				if held.State != events.InvitationFailed {
					t.Fatalf("the record is %q, want failed", held.State)
				}
				if held.Secret != "" {
					t.Fatal("a spent secret was left at rest")
				}
				return
			}
			if held.State != events.InvitationLive {
				t.Fatalf("a retryable failure retired the invitation: %q", held.State)
			}
			if held.Secret != invitationTestSecret {
				t.Fatal("a retryable failure destroyed the secret; the invitation can no longer be answered")
			}

			fx.backend.acceptStatus = http.StatusOK
			fx.backend.inviterPayload = fx.far.payload
			acceptFrame(t, ctx, fx.conn, fx.msgID, ipc.FrameInvitationAccepted)
		})
	}
}

func TestInvitationAccept_ALapsedInvitationSettlesWhenTouched(t *testing.T) {
	t.Run("already gone when it is touched", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		fx := newAcceptFixture(t, ctx, invitationTestSecret)
		fx.lapseAt(t, time.Now().Add(-time.Minute).Unix())

		errFrame := acceptFrame(t, ctx, fx.conn, fx.msgID, ipc.FrameError)
		var ep ipc.ErrorPayload
		if err := json.Unmarshal(errFrame.Payload, &ep); err != nil {
			t.Fatal(err)
		}
		if ep.Code != reasonInvitationLapsed {
			t.Fatalf("refused with %q, want %q", ep.Code, reasonInvitationLapsed)
		}
		if got := fx.backend.snapshot(); got.accepts != 0 || got.mints != 0 {
			t.Fatalf("a lapsed invitation was acted on: %d dial(s), %d mint(s)", got.accepts, got.mints)
		}
		assertSettledWithNoSecret(t, fx)
	})

	t.Run("gone by the time the dial gives up", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		fx := newAcceptFixture(t, ctx, invitationTestSecret)
		deadline := time.Now().Unix() + 1
		fx.lapseAt(t, deadline)
		fx.backend.acceptStatus = http.StatusBadGateway
		started, release := fx.backend.holdTheDial(t)

		errCh := make(chan ipc.Frame, 1)
		go func() {
			req, _ := ipc.NewFrame(ipc.FrameInvitationAccept, "accept-lapsing", ipc.InvitationAcceptRequest{MsgID: fx.msgID})
			writeFrame(t, ctx, fx.conn, req)
			errCh <- readUntil(t, ctx, fx.conn, ipc.FrameError)
		}()
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("the accept never reached the rendezvous")
		}
		for time.Now().Unix() <= deadline {
			time.Sleep(50 * time.Millisecond)
		}
		release()

		var ep ipc.ErrorPayload
		select {
		case f := <-errCh:
			if err := json.Unmarshal(f.Payload, &ep); err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("the accept never answered")
		}
		if ep.Code != reasonInvitationLapsed {
			t.Fatalf("refused with %q, want %q — an unclassifiable failure past the deadline is a lapse, "+
				"not something to try again", ep.Code, reasonInvitationLapsed)
		}
		assertSettledWithNoSecret(t, fx)
	})
}

func assertSettledWithNoSecret(t *testing.T, fx *acceptFixture) {
	t.Helper()
	held, err := fx.d.invitations.Get(fx.msgID)
	if err != nil {
		t.Fatalf("the record went missing: %v", err)
	}
	if held.State != events.InvitationFailed {
		t.Fatalf("the record is %q, want failed", held.State)
	}
	if held.Secret != "" {
		t.Fatal("a lapsed invitation kept a secret nobody will ever dial")
	}
	if rows := invitationRows(t, fx.d, fx.chatID); rows[fx.msgID].State != events.InvitationFailed {
		t.Fatalf("the marker row is %q, want failed", rows[fx.msgID].State)
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	out strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.out.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.out.String()
}

func TestInvitationAccept_TheSecretReachesNoLogLine(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const secret = "Zq7-invitation-secret-4f2a91c8b3e5"
	buf := &syncBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	fx := newAcceptFixture(t, ctx, secret)
	fx.backend.inviterPayload = fx.far.payload
	resp := acceptFrame(t, ctx, fx.conn, fx.msgID, ipc.FrameInvitationAccepted)

	logged := buf.String()
	if logged == "" {
		t.Fatal("nothing was logged; the assertion would pass on an empty file")
	}
	if !strings.Contains(logged, "invitation:") {
		t.Fatalf("the accept logged none of its own hops:\n%s", logged)
	}
	for i := 6; i <= len(secret); i++ {
		if strings.Contains(logged, secret[:i]) {
			t.Fatalf("a log line carries a %d-character prefix of the secret", i)
		}
	}
	if strings.Contains(string(resp.Payload), secret[:6]) {
		t.Fatal("the reply to the client carries the secret")
	}
}

type offerFixture struct {
	d        *daemon
	backend  *acceptBackend
	chatID   chat.ChatID
	far      farDevice
	handleID string
	msgID    string
	expires  int64

	addr     string
	certPath string
	token    string

	inviterPayload []byte
}

func newOfferFixture(t *testing.T) *offerFixture {
	t.Helper()
	return newOfferFixtureFor(t, newFarDevice(t, "faronionAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA2", "them"))
}

func newOfferFixtureFor(t *testing.T, far farDevice) *offerFixture {
	t.Helper()
	backend := startAcceptBackend(t)
	d, addr, certPath, token := newTestDaemon(t, &haomadStub{URL: backend.URL})
	wireInvitationStore(d)

	chatID := chat.ChatID("aa11bb22cc33dd44ee55ff6600778890")
	if _, err := d.chats.CreateDirectEdgeless(chatID, far.fingerprint); err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(7 * 24 * time.Hour).Unix()
	body, err := json.Marshal(events.InvitationBody{ExpiresAt: expires, State: events.InvitationLive})
	if err != nil {
		t.Fatal(err)
	}
	const msgID = "inv-offer-1"
	if _, err := d.events.AppendLocal(events.LocalParams{
		ChatID:    chatID,
		Kind:      events.KindInvitation,
		Direction: events.DirOut,
		MsgID:     msgID,
		Body:      body,
	}); err != nil {
		t.Fatal(err)
	}
	const handleID = "handle-offer-1"
	if err := d.invitations.PutNote(invitation.Note{
		MsgID: msgID, HandleID: handleID, Fingerprint: far.fingerprint,
		MintedAt: expires - 60, ExpiresAt: expires,
	}); err != nil {
		t.Fatal(err)
	}
	inviterInv, mine, err := pair.Build(d.signalState, d.stores, []string{"myonionAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA4"}, "me", nil)
	if err != nil {
		t.Fatal(err)
	}
	inviterPayload, err := inviterInv.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := pair.SaveMyKeys(d.store, handleID, mine, backendapi.MintedOnion{
		Address: "myonionAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA4", PrivateKey: "ZmFrZQ==",
	}); err != nil {
		t.Fatal(err)
	}
	return &offerFixture{
		d: d, backend: backend, chatID: chatID, far: far, handleID: handleID, msgID: msgID, expires: expires,
		addr: addr, certPath: certPath, token: token,
		inviterPayload: inviterPayload,
	}
}

func TestInvitationOffer_CompletesOnTheInheritedConversation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fx := newOfferFixture(t)
	fx.backend.joinerPayload = fx.far.payload
	fx.backend.watchSends(func() bool {
		_, err := fx.d.chats.GetByDirectPeer(fx.far.invite.PeerID)
		return err == nil
	})

	fx.d.awaitInvitationJoiner(ctx, fx.handleID, fx.far.fingerprint, fx.msgID)

	dc, err := fx.d.chats.GetByDirectPeer(fx.far.invite.PeerID)
	if err != nil {
		t.Fatalf("the completed pairing reached no conversation: %v", err)
	}
	if dc.ID != fx.chatID {
		t.Fatalf("the pairing landed in %q, but the inherited conversation is %q — "+
			"the whole point is reaching the person that conversation belongs to", dc.ID, fx.chatID)
	}
	if len(directChats(t, fx.d)) != 1 {
		t.Fatal("the completed pairing left a second conversation for one person")
	}

	got := fx.backend.snapshot()
	if got.registrations != 1 {
		t.Fatalf("%d peer registration(s), want exactly one", got.registrations)
	}
	if len(got.sendProbeSeen) == 0 {
		t.Fatal("no presence was shipped; the person would stay mute forever")
	}
	for i, routed := range got.sendProbeSeen {
		if !routed {
			t.Fatalf("send %d went out before the conversation routed the fresh pairing — "+
				"a reply to it creates the conversation the fold was going to use", i)
		}
	}

	row := invitationRows(t, fx.d, fx.chatID)[fx.msgID]
	if row.State != events.InvitationAccepted {
		t.Fatalf("the marker row is %q, want accepted", row.State)
	}

	if row.ExpiresAt != fx.expires {
		t.Fatalf("the stamped row lapses at %d, but the rendezvous lapsed at %d", row.ExpiresAt, fx.expires)
	}
	if _, err := fx.d.invitations.GetNote(fx.far.fingerprint, 0); !errors.Is(err, invitation.ErrNotFound) {
		t.Fatalf("the attempt note outlived the rendezvous it named: %v", err)
	}
	if _, _, err := pair.LoadMyKeys(fx.d.store, fx.handleID); !errors.Is(err, pair.ErrMyKeysNotFound) {
		t.Fatalf("the attempt's keys outlived the pairing they built: %v", err)
	}
	if rec, err := fx.d.peerMeta.Get(fx.far.invite.PeerID); err != nil || rec.Fingerprint != fx.far.fingerprint {
		t.Fatalf("the person's identity was not recorded against the fresh pairing: %+v (%v)", rec, err)
	}
}

func TestInvitationOffer_AMismatchRegistersNothingAndReleasesTheRendezvous(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fx := newOfferFixture(t)
	stranger := newFarDevice(t, "strangerAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA3", "not-them")
	fx.backend.joinerPayload = stranger.payload

	fx.d.awaitInvitationJoiner(ctx, fx.handleID, fx.far.fingerprint, fx.msgID)

	got := fx.backend.snapshot()
	if got.registrations != 0 {
		t.Fatalf("%d peer registration(s) for an identity that was refused", got.registrations)
	}
	if got.sends != 0 {
		t.Fatalf("%d envelope(s) were sent to a refused pairing", got.sends)
	}
	if len(got.cancelled) != 1 || got.cancelled[0] != fx.handleID {
		t.Fatalf("cancelled %v, want the spent rendezvous (%s)", got.cancelled, fx.handleID)
	}
	if _, err := fx.d.chats.GetByDirectPeer(stranger.invite.PeerID); !errors.Is(err, chat.ErrNotFound) {
		t.Fatalf("the refused pairing reached a conversation: %v", err)
	}
	if rows := invitationRows(t, fx.d, fx.chatID); rows[fx.msgID].State != events.InvitationFailed {
		t.Fatalf("the marker row is %q, want failed", rows[fx.msgID].State)
	}
	if _, err := fx.d.invitations.GetNote(fx.far.fingerprint, 0); !errors.Is(err, invitation.ErrNotFound) {
		t.Fatalf("the attempt note outlived the rendezvous it named: %v", err)
	}
}

func TestInvitationOffer_AConcludedAttemptLeavesTheNewerNoteAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fx := newOfferFixture(t)
	fx.backend.joinerPayload = fx.far.payload

	const newerHandle = "handle-offer-2"
	newerExpires := fx.expires + 3600
	if err := fx.d.invitations.PutNote(invitation.Note{
		MsgID: "inv-offer-2", HandleID: newerHandle, Fingerprint: fx.far.fingerprint,
		MintedAt: newerExpires - 60, ExpiresAt: newerExpires,
	}); err != nil {
		t.Fatal(err)
	}

	fx.d.awaitInvitationJoiner(ctx, fx.handleID, fx.far.fingerprint, fx.msgID)

	note, err := fx.d.invitations.GetNote(fx.far.fingerprint, 0)
	if err != nil {
		t.Fatalf("the earlier attempt took the newer rendezvous's note down with it: %v", err)
	}
	if note.HandleID != newerHandle {
		t.Fatalf("the note names %q, want the rendezvous the second offer published (%s)", note.HandleID, newerHandle)
	}
	if got := invitationRows(t, fx.d, fx.chatID)[fx.msgID].ExpiresAt; got == newerExpires {
		t.Fatalf("the first offer's row was stamped with the second offer's deadline (%d)", got)
	}

	if _, _, err := pair.LoadMyKeys(fx.d.store, fx.handleID); !errors.Is(err, pair.ErrMyKeysNotFound) {
		t.Fatalf("the concluded attempt's keys outlived the pairing they built: %v", err)
	}
}

func TestInvitationOffer_AWaitThatEndsEmptyLeavesTheInvitationStanding(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fx := newOfferFixture(t)
	fx.backend.waitStatus = http.StatusServiceUnavailable

	fx.d.awaitInvitationJoiner(ctx, fx.handleID, fx.far.fingerprint, fx.msgID)

	if got := fx.backend.snapshot(); len(got.cancelled) != 0 {
		t.Fatalf("a rendezvous nobody dialled was cancelled: %v", got.cancelled)
	}
	if _, err := fx.d.invitations.GetNote(fx.far.fingerprint, 0); err != nil {
		t.Fatalf("the note naming the standing rendezvous was dropped: %v", err)
	}
	if rows := invitationRows(t, fx.d, fx.chatID); rows[fx.msgID].State != events.InvitationLive {
		t.Fatalf("the marker row is %q, want live", rows[fx.msgID].State)
	}
}

func TestCheckInvitationFingerprint(t *testing.T) {
	far := newFarDevice(t, "faronionAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA2", "them")
	got, err := checkInvitationFingerprint(far.invite, far.fingerprint)
	if err != nil {
		t.Fatalf("the identity the invite carries was refused: %v", err)
	}
	if got != far.fingerprint {
		t.Fatalf("the helper derived %q, want %q", got, far.fingerprint)
	}
	if _, err := checkInvitationFingerprint(far.invite, "somebody-else"); !errors.Is(err, errFingerprintMismatch) {
		t.Fatalf("a mismatch returned %v, want a refusal", err)
	}

	if _, err := checkInvitationFingerprint(far.invite, ""); err == nil {
		t.Fatal("an invite was accepted with no identity to check it against")
	}
}
