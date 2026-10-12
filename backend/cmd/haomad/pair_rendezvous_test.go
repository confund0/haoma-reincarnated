package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dgraph-io/badger/v4"

	"haoma/internal/pair"
	"haoma/internal/tor/control"
)

type fakeOnionPublisher struct {
	mu      sync.Mutex
	adds    int
	key     string
	target  string
	deleted []string
}

func (f *fakeOnionPublisher) AddOnion(privateKey string, ports []control.OnionPort, _ ...string) (*control.Onion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(ports) == 0 || ports[0].Target == "" {
		return nil, errors.New("fakeOnionPublisher: missing target")
	}
	f.adds++
	f.key = privateKey
	f.target = ports[0].Target
	return &control.Onion{ServiceID: "fakeonionserviceidnotrealxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxad"}, nil
}

func (f *fakeOnionPublisher) DelOnion(serviceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, serviceID)
	return nil
}

func (f *fakeOnionPublisher) snapshot() (adds int, key, target string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.adds, f.key, f.target
}

type hostRewriteDialer struct{ target string }

func (d *hostRewriteDialer) Do(req *http.Request) (*http.Response, error) {
	cloned := *req
	u := *req.URL
	u.Host = d.target
	cloned.URL = &u
	return http.DefaultClient.Do(&cloned)
}

func newRendezvousDaemon(t *testing.T) (*daemon, *fakeOnionPublisher) {
	t.Helper()
	d := newTestDaemon(t)
	d.rendezvous = newRendezvousStore(d.store)
	d.onionInvites = newOnionInviteRegistry()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d.bgCtx = ctx

	pub := &fakeOnionPublisher{}

	driver := pair.NewOnionDriver(pub, nil)
	driver.SkipServiceIDCheckForTest()
	d.onionDriver = driver
	d.onionDriverOnce.Do(func() {})

	d.torHTTP = &http.Client{}
	return d, pub
}

func mustSecret(t *testing.T) string {
	t.Helper()
	s, err := pair.GenerateOnionSecret()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mustHandle(t *testing.T) string {
	t.Helper()
	h, err := newPairHandleID()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func liveRows(t *testing.T, d *daemon) []storedRendezvous {
	t.Helper()
	rows, err := d.rendezvous.LoadLive(time.Now())
	if err != nil {
		t.Fatalf("reading stored rendezvous: %v", err)
	}
	return rows
}

func storedRowCount(t *testing.T, d *daemon) int {
	t.Helper()
	n := 0
	err := d.store.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = []byte(rendezvousPrefix)
		opts.PrefetchValues = false
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("counting stored rendezvous: %v", err)
	}
	return n
}

func inviteRequest(t *testing.T, longLived bool) *http.Request {
	t.Helper()
	body, err := json.Marshal(pairOnionInviteRequest{
		Payload:   []byte(`{"side":"inviter"}`),
		LongLived: longLived,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/pair/onion/invite", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestPairOnionInvite_OnlyALongLivedRendezvousIsStored(t *testing.T) {
	d, _ := newRendezvousDaemon(t)

	ordinary := httptest.NewRecorder()
	d.handlePairOnionInvite(ordinary, inviteRequest(t, false))
	if ordinary.Code != http.StatusCreated {
		t.Fatalf("ordinary invite: status %d, body %s", ordinary.Code, ordinary.Body.String())
	}
	if rows := liveRows(t, d); len(rows) != 0 {
		t.Fatalf("an hour-capped rendezvous was persisted (%d rows) — a restart would revive an invitation nobody is watching", len(rows))
	}

	long := httptest.NewRecorder()
	d.handlePairOnionInvite(long, inviteRequest(t, true))
	if long.Code != http.StatusCreated {
		t.Fatalf("long-lived invite: status %d, body %s", long.Code, long.Body.String())
	}
	var resp pairOnionInviteResponse
	if err := json.Unmarshal(long.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	rows := liveRows(t, d)
	if len(rows) != 1 {
		t.Fatalf("%d rendezvous stored, want exactly the long-lived one", len(rows))
	}
	if rows[0].HandleID != resp.HandleID {
		t.Errorf("stored handle %q, want %q — a restart could not re-attach the frontend's wait", rows[0].HandleID, resp.HandleID)
	}
	if rows[0].Secret != resp.Secret {
		t.Error("the stored secret is not the one handed to the caller — the republished onion would answer at a different address")
	}
	if rows[0].ExpiresAt != resp.ExpiresAt {
		t.Errorf("stored deadline %d, want %d", rows[0].ExpiresAt, resp.ExpiresAt)
	}
	if len(rows[0].Payload) == 0 {
		t.Error("stored without the inviter payload — the republished rendezvous would have nothing to hand the joiner")
	}
}

func TestStoredRendezvous_RepublishesAtTheSameAddress(t *testing.T) {
	d, pub := newRendezvousDaemon(t)

	secret := mustSecret(t)
	handle := mustHandle(t)
	deadline := time.Now().Add(3 * 24 * time.Hour).Unix()
	if err := d.persistRendezvous(handle, secret, []byte(`{"side":"inviter"}`), deadline); err != nil {
		t.Fatal(err)
	}

	if err := d.republishStoredRendezvous(); err != nil {
		t.Fatalf("republish: %v", err)
	}

	want, err := pair.OnionDeriveSecret(secret)
	if err != nil {
		t.Fatal(err)
	}
	adds, key, _ := pub.snapshot()
	if adds != 1 {
		t.Fatalf("published %d onions, want 1", adds)
	}
	if key != want.TorExpandedKeyB64 {
		t.Error("republished under a different key than the stored secret derives — the advertised address still answers nothing")
	}

	entry, ok := d.onionInvites.get(handle)
	if !ok {
		t.Fatal("republished rendezvous is not in the invite registry — /wait and /cancel cannot reach it")
	}
	if entry.Secret != secret {
		t.Error("registry entry lost the secret — the self-probe would derive the wrong address")
	}

	if drift := entry.ExpiresAt - deadline; drift < -5 || drift > 5 {
		t.Errorf("deadline moved by %ds on republish, want the original to stand", drift)
	}
}

func TestStoredRendezvous_ExpiredIsDroppedNotRepublished(t *testing.T) {
	d, pub := newRendezvousDaemon(t)

	handle := mustHandle(t)
	if err := d.persistRendezvous(handle, mustSecret(t), []byte(`{}`), time.Now().Add(-time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}

	if err := d.republishStoredRendezvous(); err != nil {
		t.Fatalf("republish: %v", err)
	}

	if adds, _, _ := pub.snapshot(); adds != 0 {
		t.Errorf("published %d onions for a rendezvous past its deadline", adds)
	}
	if _, ok := d.onionInvites.get(handle); ok {
		t.Error("an expired rendezvous was put back in the invite registry")
	}
	if rows := liveRows(t, d); len(rows) != 0 {
		t.Errorf("%d expired rows survived startup, want them removed", len(rows))
	}
}

func TestStoredRendezvous_DroppedOnceItConcludes(t *testing.T) {
	t.Run("answered", func(t *testing.T) {
		d, pub := newRendezvousDaemon(t)
		handle, pi := storedAndLive(t, d, time.Hour)

		_, _, target := pub.snapshot()
		joiner := pair.NewOnionDriver(nil, &hostRewriteDialer{target: target})
		if _, err := joiner.AcceptInvite(context.Background(), pair.AcceptRequest{
			Blob:    pair.InviteBlob{Secret: rowSecret(t, d, handle)},
			Payload: []byte(`{"side":"joiner"}`),
		}); err != nil {
			t.Fatalf("handshake: %v", err)
		}

		d.dropRendezvousWhenDone(d.bgCtx, handle, pi)
		if rows := liveRows(t, d); len(rows) != 0 {
			t.Errorf("%d rows left after the handshake landed — the next start would republish a spent invitation", len(rows))
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		d, _ := newRendezvousDaemon(t)
		handle, pi := storedAndLive(t, d, time.Hour)

		pi.Cancel()
		d.dropRendezvousWhenDone(d.bgCtx, handle, pi)
		if rows := liveRows(t, d); len(rows) != 0 {
			t.Errorf("%d rows left after cancel", len(rows))
		}
	})

	t.Run("timed out", func(t *testing.T) {
		d, _ := newRendezvousDaemon(t)
		handle, pi := storedAndLive(t, d, 50*time.Millisecond)

		d.dropRendezvousWhenDone(d.bgCtx, handle, pi)
		if rows := liveRows(t, d); len(rows) != 0 {
			t.Errorf("%d rows left after the rendezvous timed out", len(rows))
		}
	})
}

func TestStoredRendezvous_SurvivesShutdown(t *testing.T) {
	d, _ := newRendezvousDaemon(t)
	handle, pi := storedAndLive(t, d, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d.dropRendezvousWhenDone(ctx, handle, pi)

	if rows := liveRows(t, d); len(rows) != 1 {
		t.Fatalf("%d rows after shutdown, want the rendezvous kept for the next start", len(rows))
	}
}

func TestRendezvousStore_UndecodableRowIsDroppedNotFatal(t *testing.T) {
	d, _ := newRendezvousDaemon(t)

	good := mustHandle(t)
	if err := d.persistRendezvous(good, mustSecret(t), []byte(`{}`), time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if err := d.store.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(rendezvousPrefix+"aaaaaaaaaaaaaaaa"), []byte("{not json"))
	}); err != nil {
		t.Fatal(err)
	}

	rows := liveRows(t, d)
	if len(rows) != 1 || rows[0].HandleID != good {
		t.Fatalf("got %d rows, want only the decodable one", len(rows))
	}

	if n := storedRowCount(t, d); n != 1 {
		t.Errorf("%d rows still in the store, want the undecodable one gone", n)
	}
}

func TestRendezvousSecretNeverReachesTheLog(t *testing.T) {
	var buf syncBuffer
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(restore) })

	d, _ := newRendezvousDaemon(t)

	rec := httptest.NewRecorder()
	d.handlePairOnionInvite(rec, inviteRequest(t, true))
	if rec.Code != http.StatusCreated {
		t.Fatalf("invite: status %d", rec.Code)
	}
	var resp pairOnionInviteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	entry, ok := d.onionInvites.get(resp.HandleID)
	if !ok {
		t.Fatal("invite missing from the registry")
	}

	entry.Pending.Cancel()
	d.dropRendezvousWhenDone(d.bgCtx, resp.HandleID, entry.Pending)
	d.onionInvites.drop(resp.HandleID)
	if err := d.persistRendezvous(resp.HandleID, resp.Secret, []byte(`{}`), time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if err := d.republishStoredRendezvous(); err != nil {
		t.Fatal(err)
	}

	if resp.Secret == "" {
		t.Fatal("no secret to look for")
	}
	if strings.Contains(buf.String(), resp.Secret) {
		t.Error("the rendezvous secret was written to the log — anyone holding the log can complete the handshake")
	}

	if !strings.Contains(buf.String(), resp.HandleID) {
		t.Error("no log line names the rendezvous, so the search above proves nothing")
	}
}

func storedAndLive(t *testing.T, d *daemon, ttl time.Duration) (string, pair.PendingInvite) {
	t.Helper()
	handle := mustHandle(t)
	secret := mustSecret(t)
	if err := d.persistRendezvous(handle, secret, []byte(`{"side":"inviter"}`), time.Now().Add(ttl).Unix()); err != nil {
		t.Fatal(err)
	}
	driver, err := d.ensureOnionDriver()
	if err != nil {
		t.Fatal(err)
	}
	pi, err := driver.CreateInvite(d.bgCtx, pair.CreateRequest{
		Payload: []byte(`{"side":"inviter"}`),
		Secret:  secret,
		Timeout: ttl,
	})
	if err != nil {
		t.Fatal(err)
	}
	return handle, pi
}

func rowSecret(t *testing.T, d *daemon, handle string) string {
	t.Helper()
	for _, r := range liveRows(t, d) {
		if r.HandleID == handle {
			return r.Secret
		}
	}
	t.Fatalf("no stored rendezvous for handle %q", handle)
	return ""
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestPairOnionAccept_TakesEitherAddressButNotBoth(t *testing.T) {
	d, _ := newRendezvousDaemon(t)
	words := []string{"acorn", "badge", "cabin", "dance", "eagle", "fable", "gauge"}

	for _, tc := range []struct {
		name    string
		words   []string
		secret  string
		refused string
	}{
		{
			name:   "a secret on its own addresses a long-lived rendezvous",
			secret: mustSecret(t),
		},
		{
			name:  "a phrase on its own addresses an ordinary one",
			words: words,
		},
		{
			name:    "neither addresses anything",
			refused: "words or secret required",
		},
		{
			name:    "both would leave the address ambiguous",
			words:   words,
			secret:  mustSecret(t),
			refused: "alternatives",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(pairOnionAcceptRequest{
				Words:   tc.words,
				Secret:  tc.secret,
				Payload: []byte(`{"side":"joiner"}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/pair/onion/accept", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			d.handlePairOnionAccept(rec, req)

			if tc.refused != "" {
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status %d, want 400 — %s", rec.Code, tc.name)
				}
				if !strings.Contains(rec.Body.String(), tc.refused) {
					t.Errorf("refusal %q does not mention %q", rec.Body.String(), tc.refused)
				}
				return
			}

			if rec.Code == http.StatusBadRequest && strings.Contains(rec.Body.String(), "required") {
				t.Fatalf("refused before dialling: %s", rec.Body.String())
			}
		})
	}
}

func TestPairOnionCancel_DropsTheRowOfARendezvousNotYetRepublished(t *testing.T) {
	d, pub := newRendezvousDaemon(t)
	handle := mustHandle(t)
	if err := d.persistRendezvous(handle, mustSecret(t), []byte(`{}`), time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/pair/onion/invite/"+handle, nil)
	req.SetPathValue("handle_id", handle)
	rec := httptest.NewRecorder()
	d.handlePairOnionCancel(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("cancel answered %d, want 204", rec.Code)
	}
	if n := storedRowCount(t, d); n != 0 {
		t.Fatalf("%d stored row(s) left after the cancel", n)
	}

	if err := d.republishStoredRendezvous(); err != nil {
		t.Fatalf("republish: %v", err)
	}
	if adds, _, _ := pub.snapshot(); adds != 0 {
		t.Fatalf("published %d onion(s) for a cancelled rendezvous", adds)
	}
}
