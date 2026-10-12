package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"haoma/internal/files"
	"haoma/internal/outbox"
	"haoma/internal/store"
	"haoma/internal/tor/control"
)

func livePeerDest(t *testing.T, d *daemon, c string) string {
	t.Helper()
	addr := strings.Repeat(c, 56)
	if err := d.registry.Add(freshTestPeer(t, addr)); err != nil {
		t.Fatal(err)
	}
	return "http://" + addr + ".onion"
}

type countingDial struct{ n atomic.Int32 }

func (c *countingDial) RoundTrip(*http.Request) (*http.Response, error) {
	c.n.Add(1)
	return nil, errors.New("dial tcp: socks connect: host unreachable")
}

func attachFailedFetch(t *testing.T, d *daemon) (*countingDial, string) {
	t.Helper()
	dial := &countingDial{}
	resolver := files.PeerResolverFunc(func(string) ([]string, error) {
		return []string{strings.Repeat("p", 56)}, nil
	})
	client := files.HTTPClientForPeer(func(string) (*http.Client, error) {
		return &http.Client{Transport: dial}, nil
	})
	w := files.NewWorker(d.files, resolver, client, nil, nil)
	go w.Run(t.Context())
	d.fetchWorker.Store(w)
	const tok = "Nc4ng3-token-of-a-fetch-that-failed-1a2b"
	if err := d.files.PutFetch(files.Fetch{
		Token: tok, MsgID: "netchange-fetch-id", PeerID: "fetch-peer", UrlPath: "/files/" + tok,
		ExpectedSize: 10, ExpectedSha256: strings.Repeat("0", 64),
		State: files.FetchStateFailedTransient, RetryAttempts: 1, CreatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	return dial, tok
}

func waitRetried(t *testing.T, d *daemon, dial *countingDial, tok string, dials int32, attempts uint16) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		row, err := d.files.GetFetch(tok)
		if err == nil && dial.n.Load() == dials && row.State == files.FetchStateFailedTransient && !row.Kicked {
			if row.RetryAttempts != attempts {
				t.Fatalf("after %d dials the fetch counts %d attempts, want %d", dials, row.RetryAttempts, attempts)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	row, _ := d.files.GetFetch(tok)
	t.Fatalf("the failed fetch was not retried: dials=%d state=%s attempts=%d kicked=%v", dial.n.Load(), row.State, row.RetryAttempts, row.Kicked)
}

func TestNetworkChanged_KicksEveryRowAndRetriesTheFetches(t *testing.T) {
	d := newFilesTestDaemon(t)
	st := attachOutbox(t, d)
	pa := importPeerWithAddresses(t, d, []string{sid("peera")}, sid("minea"), "minea-key")
	pb := importPeerWithAddresses(t, d, []string{sid("peerb")}, sid("mineb"), "mineb-key")
	queueStalled(t, st, peerDests(pa)[0], "nc-a1")
	queueStalled(t, st, peerDests(pa)[0], "nc-a2")
	queueStalled(t, st, peerDests(pb)[0], "nc-b1")
	dial, tok := attachFailedFetch(t, d)

	res := d.networkChanged(t.Context(), networkChangeSourceTor)

	want := networkChangeResult{Rows: 3, Destinations: 2, Fetches: 1}
	if res != want {
		t.Errorf("result = %+v, want %+v", res, want)
	}
	for _, id := range []string{"nc-a1", "nc-a2", "nc-b1"} {
		assertDue(t, st, id)
		if !loadRow(t, st, id).Kicked {
			t.Errorf("%s was moved without the Kicked mark; its attempt would count toward the give-up limit", id)
		}
	}

	waitRetried(t, d, dial, tok, 1, 1)
	if _, err := d.fetchWorker.Load().RetryAllFailed(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	waitRetried(t, d, dial, tok, 2, 2)
}

func TestNetworkChanged_SkipsRetiredPeers(t *testing.T) {
	d := newTestDaemon(t)
	st := attachOutbox(t, d)
	gone := importPeerWithAddresses(t, d, []string{sid("retiredpeer")}, sid("mineretired"), "mineretired-key")
	goneDest := peerDests(gone)[0]
	if err := d.registry.Retire(gone.ID); err != nil {
		t.Fatal(err)
	}
	live := importPeerWithAddresses(t, d, []string{sid("livepeer")}, sid("minelive"), "minelive-key")
	queueStalled(t, st, goneDest, "nc-gone")
	queueStalled(t, st, peerDests(live)[0], "nc-live")

	res := d.networkChanged(t.Context(), networkChangeSourceOS)
	if res.Rows != 1 {
		t.Errorf("rows = %d, want 1 (the live peer's)", res.Rows)
	}
	assertDue(t, st, "nc-live")
	assertNotDue(t, st, "nc-gone")
}

func TestNetworkChanged_FailedRunGivesTheWindowBack(t *testing.T) {
	d := newTestDaemon(t)
	broken, err := store.Unlock(t.TempDir(), "pw")
	if err != nil {
		t.Fatal(err)
	}
	if err := broken.Lock(); err != nil {
		t.Fatal(err)
	}
	d.worker = outbox.NewWorker(outbox.NewStore(broken), nil, nil, &outbox.Bus{})
	t0 := time.Now()
	if res := d.networkChangedAt(t.Context(), networkChangeSourceOS, t0); !res.Failed {
		t.Fatalf("run on a closed store = %+v, want failed", res)
	}
	if res := d.networkChangedAt(t.Context(), networkChangeSourceOS, t0.Add(time.Second)); res.Dropped {
		t.Error("the call after a failed run was dropped; the failed run kept the window")
	}
}

func TestNetworkChanged_TorAfterOSPassesOnce(t *testing.T) {
	d := newTestDaemon(t)
	attachOutbox(t, d)
	t0 := time.Now()
	steps := []struct {
		after   time.Duration
		source  string
		dropped bool
	}{
		{0, networkChangeSourceOS, false},
		{5 * time.Second, networkChangeSourceOS, true},
		{10 * time.Second, networkChangeSourceTor, false},
		{20 * time.Second, networkChangeSourceTor, true},
		{30 * time.Second, networkChangeSourceOS, true},
	}
	for i, s := range steps {
		if res := d.networkChangedAt(t.Context(), s.source, t0.Add(s.after)); res.Dropped != s.dropped {
			t.Errorf("step %d (%s at +%s): dropped = %v, want %v", i, s.source, s.after, res.Dropped, s.dropped)
		}
	}

	d2 := newTestDaemon(t)
	attachOutbox(t, d2)
	d2.networkChangedAt(t.Context(), networkChangeSourceTor, t0)
	if res := d2.networkChangedAt(t.Context(), networkChangeSourceTor, t0.Add(10*time.Second)); !res.Dropped {
		t.Error("a second tor call inside the window ran")
	}
}

func TestNetworkChanged_WindowDropsASecondCall(t *testing.T) {
	d := newTestDaemon(t)
	st := attachOutbox(t, d)
	t0 := time.Now()

	if res := d.networkChangedAt(t.Context(), networkChangeSourceOS, t0); res.Dropped {
		t.Fatal("the first call was dropped")
	}
	queueStalled(t, st, livePeerDest(t, d, "c"), "nc-c1")

	res := d.networkChangedAt(t.Context(), networkChangeSourceOS, t0.Add(networkChangeWindow-time.Second))
	if !res.Dropped || res.Rows != 0 {
		t.Errorf("call inside the window = %+v, want dropped with no rows", res)
	}
	assertNotDue(t, st, "nc-c1")

	res = d.networkChangedAt(t.Context(), networkChangeSourceTor, t0.Add(networkChangeWindow))
	if res.Dropped || res.Rows != 1 {
		t.Errorf("call after the window = %+v, want one row moved", res)
	}
	assertDue(t, st, "nc-c1")
}

func TestLivenessStatus(t *testing.T) {
	for _, tc := range []struct {
		ev   control.Event
		want string
		ok   bool
	}{
		{control.Event{Type: "NETWORK_LIVENESS", Lines: []string{"NETWORK_LIVENESS UP"}}, "UP", true},
		{control.Event{Type: "NETWORK_LIVENESS", Lines: []string{"NETWORK_LIVENESS DOWN"}}, "DOWN", true},
		{control.Event{Type: "NETWORK_LIVENESS", Lines: []string{"NETWORK_LIVENESS"}}, "", false},
		{control.Event{Type: "NETWORK_LIVENESS", Lines: []string{"NETWORK_LIVENESS SIDEWAYS"}}, "", false},
		{control.Event{Type: "CIRC", Lines: []string{"CIRC 1 BUILT"}}, "", false},
	} {
		got, ok := livenessStatus(tc.ev)
		if got != tc.want || ok != tc.ok {
			t.Errorf("livenessStatus(%v) = %q %v, want %q %v", tc.ev.Lines, got, ok, tc.want, tc.ok)
		}
	}
}

func TestNetworkLiveness_UpAfterDownOnly(t *testing.T) {
	var l networkLiveness
	steps := []struct {
		status string
		fire   bool
	}{
		{"UP", false},
		{"DOWN", false},
		{"DOWN", false},
		{"UP", true},
		{"UP", false},
	}
	for i, s := range steps {
		if got := l.observe(s.status); got != s.fire {
			t.Errorf("step %d (%s): fire = %v, want %v", i, s.status, got, s.fire)
		}
	}
}

func livenessDown(d *daemon) bool {
	d.liveness.mu.Lock()
	defer d.liveness.mu.Unlock()
	return d.liveness.down
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func rowDue(st *outbox.Store, envID string) func() bool {
	return func() bool {
		row, err := st.Load(envID)
		return err == nil && row.NextAttemptAt <= time.Now().UnixNano()
	}
}

func setEventsOn(f *fakeTor, conn int) bool {
	for _, c := range f.sent() {
		if c.conn == conn && c.line == "SETEVENTS NETWORK_LIVENESS" {
			return true
		}
	}
	return false
}

func TestNetworkLiveness_EventsReachTheHandler(t *testing.T) {
	d, f := newTorDaemon(t)
	st := attachOutbox(t, d)
	queueStalled(t, st, livePeerDest(t, d, "d"), "nc-d1")

	d.watchNetworkLiveness(t.Context(), d.ctrlConn)
	if !setEventsOn(f, 1) {
		t.Fatal("no SETEVENTS NETWORK_LIVENESS on the control connection")
	}

	f.push(t, 1, "650 NETWORK_LIVENESS UP")
	f.push(t, 1, "650 NETWORK_LIVENESS DOWN")
	waitFor(t, "the DOWN", func() bool { return livenessDown(d) })
	assertNotDue(t, st, "nc-d1")

	f.push(t, 1, "650 NETWORK_LIVENESS UP")
	waitFor(t, "the row kicked by UP after DOWN", rowDue(st, "nc-d1"))
}

func TestNetworkLiveness_SubscribedAgainAfterReconnect(t *testing.T) {
	d, f := newTorDaemon(t)
	st := attachOutbox(t, d)
	queueStalled(t, st, livePeerDest(t, d, "e"), "nc-e1")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	d.watchNetworkLiveness(ctx, d.ctrlConn)
	f.push(t, 1, "650 NETWORK_LIVENESS DOWN")
	waitFor(t, "the DOWN on the first connection", func() bool { return livenessDown(d) })

	if !watchdogReconnect(ctx, d, "test", false) {
		t.Fatal("reconnect failed")
	}
	waitFor(t, "SETEVENTS on the new connection", func() bool { return setEventsOn(f, 2) })

	f.push(t, 2, "650 NETWORK_LIVENESS UP")
	waitFor(t, "the row kicked by UP on the new connection", rowDue(st, "nc-e1"))
}

func TestHandleNetworkChanged(t *testing.T) {
	d := newTestDaemon(t)
	st := attachOutbox(t, d)
	queueStalled(t, st, livePeerDest(t, d, "f"), "nc-f1")
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	post := func() networkChangeResult {
		t.Helper()
		resp, err := http.Post(srv.URL+"/network/changed", "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d", resp.StatusCode)
		}
		var res networkChangeResult
		if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := post(); res.Dropped || res.Rows != 1 || res.Destinations != 1 {
		t.Errorf("first call = %+v, want one row on one destination", res)
	}
	assertDue(t, st, "nc-f1")
	if res := post(); !res.Dropped {
		t.Errorf("second call = %+v, want dropped", res)
	}
}
