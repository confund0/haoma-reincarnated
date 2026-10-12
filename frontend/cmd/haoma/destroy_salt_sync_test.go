package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/syncrail"
)

type outboxFront struct {
	URL string

	mu     sync.Mutex
	states map[string]string
	asked  []string

	guardSibling string
}

func newOutboxFront(t *testing.T, stub *haomadStub) *outboxFront {
	t.Helper()
	target, err := url.Parse(stub.URL)
	if err != nil {
		t.Fatal(err)
	}
	f := &outboxFront{states: map[string]string{}, guardSibling: railSiblingID}
	proxy := httputil.NewSingleHostReverseProxy(target)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /outbox/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		f.mu.Lock()
		f.asked = append(f.asked, id)
		st, ok := f.states[id]
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(backendapi.OutboxRow{EnvelopeID: id, State: st})
	})
	mux.HandleFunc("GET /destroy-pin", func(w http.ResponseWriter, _ *http.Request) {
		resp, err := http.Get(stub.URL + "/destroy-pin")
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		var st backendapi.DestroyPINState
		_ = json.NewDecoder(resp.Body).Decode(&st)
		if st.Set {
			f.mu.Lock()
			st.SiblingPeerID = f.guardSibling
			f.mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(st)
	})
	mux.Handle("/", proxy)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	return f
}

func (f *outboxFront) set(id, state string) {
	f.mu.Lock()
	f.states[id] = state
	f.mu.Unlock()
}

func (f *outboxFront) askedFor(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, a := range f.asked {
		if a == id {
			n++
		}
	}
	return n
}

func shortSaltOrder(t *testing.T, wait time.Duration) {
	t.Helper()
	previous := saltOrder
	saltOrder = saltOrderTiming{wait: wait, poll: 10 * time.Millisecond}
	t.Cleanup(func() { saltOrder = previous })
}

func noSendWithin(t *testing.T, stub *haomadStub, d time.Duration, why string) {
	t.Helper()
	select {
	case <-stub.SendDone:
		t.Fatalf("a /send happened: %s", why)
	case <-time.After(d):
	}
}

func openRail(t *testing.T, d *daemon, payload []byte) syncrail.Envelope {
	t.Helper()
	key, err := d.railKey()
	if err != nil {
		t.Fatalf("railKey: %v", err)
	}
	env, err := syncrail.OpenEnvelope(key, payload)
	if err != nil {
		t.Fatalf("a sent payload did not open as a rail envelope: %v", err)
	}
	return env
}

func saltEnvelopes(t *testing.T, d *daemon, stub *haomadStub) int {
	t.Helper()
	n := 0
	for _, p := range stub.SentPayloads {
		if openRail(t, d, p).Kind == syncrail.KindDestroySalt {
			n++
		}
	}
	return n
}

func TestSaltOrder_TheSaltWaitsUntilTheIdentityBundleHasLeft(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	front := newOutboxFront(t, stub)
	d.backendClient = backendapi.New(front.URL, "", nil)
	linkSibling(t, d, railSiblingID)
	shortSaltOrder(t, 30*time.Second)

	const bundle = "env-bundle-0001"
	front.set(bundle, outboxStateEnqueued)
	noteIdentityBundleQueued(d, railSiblingID, bundle)

	out, err := setLocalDestroyPIN(context.Background(), d, ipc.SetDestroyPINRequest{New: "1234"})
	if err != nil {
		t.Fatalf("setLocalDestroyPIN: %v", err)
	}
	if !out.Accepted {
		t.Fatalf("the set was refused: %q", out.Reason)
	}
	if out.SaltPushed != 0 {
		t.Fatalf("salt_pushed = %d while the identity bundle is still queued, want 0", out.SaltPushed)
	}
	noSendWithin(t, stub, 300*time.Millisecond, "the salt went while the identity bundle was still in the outbox")
	if front.askedFor(bundle) == 0 {
		t.Fatal("the wait never asked haomad about the identity bundle's envelope")
	}

	front.set(bundle, deliveryStateSent)
	stub.WaitForSend(t, 1, 5*time.Second)
	if got := saltEnvelopes(t, d, stub); got != 1 {
		t.Fatalf("%d salt envelopes after the bundle left, want 1", got)
	}
	_, salt, err := syncrail.ParseDestroySalt(openRail(t, d, stub.SentPayloads[0]))
	if err != nil {
		t.Fatalf("ParseDestroySalt: %v", err)
	}
	if hex.EncodeToString(salt) != stub.DestroyPINSalt {
		t.Fatalf("pushed salt %q, but haomad holds %q", hex.EncodeToString(salt), stub.DestroyPINSalt)
	}
	if stub.SentPeerIDs[0] != railSiblingID {
		t.Fatalf("salt went to %q, want %q", stub.SentPeerIDs[0], railSiblingID)
	}
}

func TestSaltOrder_TheSaltGoesAtTheBound(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	front := newOutboxFront(t, stub)
	d.backendClient = backendapi.New(front.URL, "", nil)
	linkSibling(t, d, railSiblingID)
	const bound = 400 * time.Millisecond
	shortSaltOrder(t, bound)

	const bundle = "env-bundle-0002"
	front.set(bundle, outboxStateEnqueued)
	noteIdentityBundleQueued(d, railSiblingID, bundle)

	start := time.Now()
	if _, err := setLocalDestroyPIN(context.Background(), d, ipc.SetDestroyPINRequest{New: "1234"}); err != nil {
		t.Fatalf("setLocalDestroyPIN: %v", err)
	}
	stub.WaitForSend(t, 1, 5*time.Second)
	if waited := time.Since(start); waited < bound {
		t.Fatalf("the salt went after %s, before the %s bound, with the bundle still queued", waited, bound)
	}
	if got := saltEnvelopes(t, d, stub); got != 1 {
		t.Fatalf("%d salt envelopes at the bound, want 1", got)
	}
}

func TestSaltOrder_TheWaitEndsOnEveryOutcome(t *testing.T) {
	timing := saltOrderTiming{wait: 200 * time.Millisecond, poll: 5 * time.Millisecond}
	cases := []struct {
		name  string
		state string
		err   error
		want  string
	}{
		{"sent", deliveryStateSent, nil, bundleOutSent},
		{"failed", deliveryStateFailed, nil, bundleOutFailed},
		{"swept", "", backendapi.ErrOutboxEntryNotFound, bundleOutGone},
		{"queued", outboxStateEnqueued, nil, bundleOutTimedOut},
		{"haomad down", "", context.DeadlineExceeded, bundleOutTimedOut},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			state := func(context.Context, string) (string, error) { return c.state, c.err }
			if got := awaitEnvelopeLeft(context.Background(), state, "env-x", timing); got != c.want {
				t.Fatalf("outcome = %q, want %q", got, c.want)
			}
		})
	}
}

func TestSaltOrder_TheIdentityBundleIsRecordedForTheSaltToWaitOn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	stub := newLinkStub(t, "offer-onion")
	d, _, _, _ := linkDaemon(t, stub)
	d.backendClient = backendapi.New(stub.URL, "", nil)

	const peerID = "00000000000000000000000000002222"
	preEstablishSession(t, d, peerID)

	if got := identityBundleInFlight(d, peerID); got != "" {
		t.Fatalf("precondition: a bundle is recorded before any was sent: %q", got)
	}
	if err := d.sendIdentityBundle(ctx, peerID); err != nil {
		t.Fatalf("sendIdentityBundle: %v", err)
	}
	if got := identityBundleInFlight(d, peerID); got != "env-link-1" {
		t.Fatalf("recorded bundle envelope = %q, want the id haomad answered, %q", got, "env-link-1")
	}
}

func setGuard(stub *haomadStub, salt []byte) {
	stub.DestroyPINGuardSet = true
	stub.DestroyPINSalt = hex.EncodeToString(salt)
	stub.DestroyPINVerifier = "00"
}

func backfillRequestWithReport(t *testing.T, d *daemon, envID string, report *syncrail.DestroySaltReport) {
	t.Helper()
	sealed := sealRailEnvelope(t, d, syncrail.Envelope{
		V:    syncrail.Version,
		Kind: syncrail.KindBackfillRequest,
		Ts:   time.Now().Unix(),
		Body: mustJSON(t, syncrail.BackfillRequestBody{
			Section:     syncrail.SectionContacts,
			DestroySalt: report,
		}),
	})
	processSiblingRailEnvelope(context.Background(), d, railEntry(railSiblingID, envID, sealed))
}

func TestSaltHeal_ADeviceThatLacksTheSaltGetsItAgainOnce(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	front := newOutboxFront(t, stub)
	d.backendClient = backendapi.New(front.URL, "", nil)
	linkSibling(t, d, railSiblingID)
	salt := []byte("heal-salt-abcdef")
	setGuard(stub, salt)

	backfillRequestWithReport(t, d, "env-req-1", &syncrail.DestroySaltReport{Held: false})

	stub.WaitForSend(t, 2, 5*time.Second)
	if got := saltEnvelopes(t, d, stub); got != 1 {
		t.Fatalf("%d salt envelopes after a report of no salt, want 1", got)
	}
	var sent []byte
	for _, p := range stub.SentPayloads {
		if env := openRail(t, d, p); env.Kind == syncrail.KindDestroySalt {
			_, got, err := syncrail.ParseDestroySalt(env)
			if err != nil {
				t.Fatalf("ParseDestroySalt: %v", err)
			}
			sent = got
		}
	}
	if !bytes.Equal(sent, salt) {
		t.Fatalf("sent salt %q, want this device's %q", sent, salt)
	}

	front.set("env-stub-0001", outboxStateEnqueued)
	backfillRequestWithReport(t, d, "env-req-2", &syncrail.DestroySaltReport{Held: false})
	stub.WaitForSend(t, 1, 5*time.Second)
	noSendWithin(t, stub, 300*time.Millisecond, "a second salt went while the first was still in the outbox")
	if got := saltEnvelopes(t, d, stub); got != 1 {
		t.Fatalf("%d salt envelopes after a second report with the push still queued, want 1", got)
	}
}

func TestSaltHeal_AMatchingReportSendsNothing(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	front := newOutboxFront(t, stub)
	d.backendClient = backendapi.New(front.URL, "", nil)
	linkSibling(t, d, railSiblingID)
	salt := []byte("held-salt-abcdef")
	setGuard(stub, salt)

	backfillRequestWithReport(t, d, "env-req-1", &syncrail.DestroySaltReport{
		Held: true, SHA256: syncrail.DestroySaltDigest(salt),
	})
	stub.WaitForSend(t, 1, 5*time.Second)
	noSendWithin(t, stub, 300*time.Millisecond, "a salt went to a device that holds it")
	if got := saltEnvelopes(t, d, stub); got != 0 {
		t.Fatalf("%d salt envelopes to a device that holds the salt, want 0", got)
	}

	backfillRequestWithReport(t, d, "env-req-2", &syncrail.DestroySaltReport{
		Held: true, SHA256: syncrail.DestroySaltDigest([]byte("some-other-salt!")),
	})
	stub.WaitForSend(t, 2, 5*time.Second)
	if got := saltEnvelopes(t, d, stub); got != 1 {
		t.Fatalf("%d salt envelopes after a report of another salt, want 1", got)
	}
}

func TestSaltHeal_NothingIsSentWithoutAGuardOrWithoutAReport(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	front := newOutboxFront(t, stub)
	d.backendClient = backendapi.New(front.URL, "", nil)
	linkSibling(t, d, railSiblingID)

	backfillRequestWithReport(t, d, "env-req-1", &syncrail.DestroySaltReport{Held: false})
	stub.WaitForSend(t, 1, 5*time.Second)
	noSendWithin(t, stub, 300*time.Millisecond, "a salt went from a device with no destroy PIN")

	setGuard(stub, []byte("guard-salt-abcde"))
	backfillRequestWithReport(t, d, "env-req-2", nil)
	stub.WaitForSend(t, 1, 5*time.Second)
	noSendWithin(t, stub, 300*time.Millisecond, "a salt went on a request that carried no report")
	if got := saltEnvelopes(t, d, stub); got != 0 {
		t.Fatalf("%d salt envelopes, want 0", got)
	}
}

func TestSaltHeal_TheSweepReportsTheSaltThisDeviceHolds(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	ctx := context.Background()

	reportOf := func(i int) *syncrail.DestroySaltReport {
		t.Helper()
		env := openRail(t, d, stub.SentPayloads[i])
		if env.Kind != syncrail.KindBackfillRequest {
			t.Fatalf("send %d is %q, want a backfill request", i, env.Kind)
		}
		req, err := syncrail.ParseBackfillRequest(env)
		if err != nil {
			t.Fatalf("ParseBackfillRequest: %v", err)
		}
		return req.DestroySalt
	}

	if got := sweepGapFill(ctx, d); got != sweepStarted {
		t.Fatalf("sweep outcome = %q, want %q", got, sweepStarted)
	}
	stub.WaitForSend(t, 1, 5*time.Second)
	if r := reportOf(0); r == nil || r.Held || r.SHA256 != "" {
		t.Fatalf("report before any salt = %+v, want held=false with no digest", r)
	}

	salt := []byte("their-salt-abcde")
	applyDestroySalt(ctx, d, railSiblingID, syncrail.NewDestroySaltBody(salt, 1), salt)
	if got := sweepGapFill(ctx, d); got != sweepStarted {
		t.Fatalf("sweep outcome = %q, want %q", got, sweepStarted)
	}
	stub.WaitForSend(t, 1, 5*time.Second)
	if r := reportOf(1); r == nil || !r.Held || r.SHA256 != syncrail.DestroySaltDigest(salt) {
		t.Fatalf("report after the salt = %+v, want held=true with its digest", r)
	}

	if err := askBackfill(ctx, d, railSiblingID, syncrail.SectionContacts, "", false); err != nil {
		t.Fatalf("askBackfill: %v", err)
	}
	stub.WaitForSend(t, 1, 5*time.Second)
	if r := reportOf(2); r != nil {
		t.Fatalf("the request a completed link sends carries a report %+v, want none", r)
	}
}

func TestSaltHeal_TheSameSaltTwiceIsANoOp(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	ctx := context.Background()

	salt := []byte("twice-salt-abcde")
	applyDestroySalt(ctx, d, railSiblingID, syncrail.NewDestroySaltBody(salt, 1), salt)
	first, _, err := d.linkedDevices.SiblingSalt(railSiblingID)
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(1100 * time.Millisecond)
	applyDestroySalt(ctx, d, railSiblingID, syncrail.NewDestroySaltBody(salt, 2), salt)
	second, _, err := d.linkedDevices.SiblingSalt(railSiblingID)
	if err != nil {
		t.Fatal(err)
	}
	if second.ReceivedAt != first.ReceivedAt || second.SetAt != first.SetAt {
		t.Fatalf("the second copy rewrote the row: first %+v, second %+v", first, second)
	}
}

func TestDeviceLink_ARailEnvelopeBeforeTheBundleIsNamedPlainly(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)

	const offering = "00000000000000000000000000003333"
	bundleCh, release := registerDeviceLinkWaiter(d, offering)
	defer release()

	buf := &syncBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	key := bytes.Repeat([]byte{0x42}, syncrail.KeySize)
	var sealed []byte
	for {
		s, err := syncrail.SealEnvelope(key, syncrail.Envelope{
			V: syncrail.Version, Kind: syncrail.KindDestroySalt, Ts: 1,
			Body: mustJSON(t, syncrail.NewDestroySaltBody([]byte("early-salt-abcde"), 1)),
		})
		if err != nil {
			t.Fatal(err)
		}
		if s[0] != 2 && s[0] != 3 {
			sealed = s
			break
		}
	}
	processDeviceLinkInbound(context.Background(), d, railEntry(offering, "env-early-1", sealed))

	logs := buf.String()
	if !strings.Contains(logs, "arrived before the link finished") || !strings.Contains(logs, reasonBeforeLink) {
		t.Fatalf("no plain line for the early envelope; logs:\n%s", logs)
	}
	if strings.Contains(logs, reasonBundleBadShape) {
		t.Fatalf("the early envelope was still logged as a bad bundle; logs:\n%s", logs)
	}
	select {
	case <-bundleCh:
		t.Fatal("the early envelope was handed to the accept as a bundle")
	default:
	}
}

func TestSaltHeal_OnlyTheGuardsDeviceGetsTheSaltAgain(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	front := newOutboxFront(t, stub)
	front.guardSibling = "0000000000000000000000000000f00d"
	d.backendClient = backendapi.New(front.URL, "", nil)
	linkSibling(t, d, railSiblingID)
	setGuard(stub, []byte("guard-salt-abcde"))

	backfillRequestWithReport(t, d, "env-req-1", &syncrail.DestroySaltReport{Held: false})
	stub.WaitForSend(t, 1, 5*time.Second)
	noSendWithin(t, stub, 300*time.Millisecond, "a salt went to a device the guard does not name")
	if got := saltEnvelopes(t, d, stub); got != 0 {
		t.Fatalf("%d salt envelopes to a device the guard does not name, want 0", got)
	}
}

func TestSaltHeal_APushWaitingForTheBundleIsNotSentTwice(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	front := newOutboxFront(t, stub)
	d.backendClient = backendapi.New(front.URL, "", nil)
	linkSibling(t, d, railSiblingID)
	shortSaltOrder(t, 30*time.Second)

	const bundle = "env-bundle-0003"
	front.set(bundle, outboxStateEnqueued)
	noteIdentityBundleQueued(d, railSiblingID, bundle)
	if _, err := setLocalDestroyPIN(context.Background(), d, ipc.SetDestroyPINRequest{New: "1234"}); err != nil {
		t.Fatalf("setLocalDestroyPIN: %v", err)
	}
	if why := saltPushOnItsWay(context.Background(), d, railSiblingID, outboxStateOf(d)); why != "waiting_for_identity_bundle" {
		t.Fatalf("on its way = %q, want waiting_for_identity_bundle", why)
	}

	backfillRequestWithReport(t, d, "env-req-1", &syncrail.DestroySaltReport{Held: false})
	stub.WaitForSend(t, 1, 5*time.Second)
	noSendWithin(t, stub, 300*time.Millisecond, "the heal sent a salt while the first push waited for the bundle")

	front.set(bundle, deliveryStateSent)
	stub.WaitForSend(t, 1, 5*time.Second)
	noSendWithin(t, stub, 300*time.Millisecond, "a second salt went after the bundle left")
	if got := saltEnvelopes(t, d, stub); got != 1 {
		t.Fatalf("%d salt envelopes, want 1", got)
	}
}

func TestSaltOrder_AShutdownDuringTheWaitSendsNothing(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	linkSibling(t, d, railSiblingID)
	const bundle = "env-bundle-0004"
	noteIdentityBundleQueued(d, railSiblingID, bundle)
	withSaltPush(d, railSiblingID, func(p *saltPush) { p.waiting = true })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	queued := func(context.Context, string) (string, error) { return outboxStateEnqueued, nil }
	timing := saltOrderTiming{wait: 5 * time.Second, poll: 10 * time.Millisecond}
	salt := []byte("cancel-salt-abcd")
	if got := awaitEnvelopeLeft(ctx, queued, bundle, timing); got != bundleOutCancelled {
		t.Fatalf("outcome = %q, want %q", got, bundleOutCancelled)
	}
	pushSaltAfterBundle(ctx, d, railSiblingID, bundle, salt, 1, queued, timing)
	noSendWithin(t, stub, 300*time.Millisecond, "a salt went after the process stopped waiting")
	if why := saltPushOnItsWay(context.Background(), d, railSiblingID, queued); why != "" {
		t.Fatalf("on its way = %q after a cancelled wait, want none", why)
	}
	if got := identityBundleInFlight(d, railSiblingID); got != "" {
		t.Fatalf("bundle %q still recorded after the wait ended", got)
	}
}

func TestSaltOrder_AnUnlinkDuringTheWaitSendsNothing(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	front := newOutboxFront(t, stub)
	d.backendClient = backendapi.New(front.URL, "", nil)
	linkSibling(t, d, railSiblingID)
	shortSaltOrder(t, 30*time.Second)

	const bundle = "env-bundle-0005"
	front.set(bundle, outboxStateEnqueued)
	noteIdentityBundleQueued(d, railSiblingID, bundle)
	if !holdSaltBehindBundle(d, railSiblingID, []byte("unlink-salt-abcd"), 1) {
		t.Fatal("the push did not wait for the bundle")
	}

	if _, err := d.linkedDevices.Unlink(railSiblingID, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	forgetSaltPushes(d, railSiblingID)
	front.set(bundle, deliveryStateSent)
	noSendWithin(t, stub, 500*time.Millisecond, "a salt went to a device unlinked during the wait")
}
