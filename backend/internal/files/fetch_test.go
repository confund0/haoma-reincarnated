package files_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"haoma/internal/files"
)

func mustPutFetch(t *testing.T, mgr *files.Manager, f files.Fetch) {
	t.Helper()
	if err := mgr.PutFetch(f); err != nil {
		t.Fatalf("PutFetch: %v", err)
	}
}

func TestPutFetch_RejectsDoubleEnqueueWhilePending(t *testing.T) {
	mgr, _, _ := newManager(t)
	row := files.Fetch{
		Token:          "tok-1",
		MsgID:          testMsgA,
		PeerID:         peerA,
		UrlPath:        "/files/tok-1",
		ExpectedSize:   16,
		ExpectedSha256: strings.Repeat("a", 64),
		State:          files.FetchStatePending,
		CreatedAt:      time.Now().Unix(),
	}
	mustPutFetch(t, mgr, row)

	if err := mgr.PutFetch(row); !errors.Is(err, files.ErrFetchTokenInUse) {
		t.Fatalf("second PutFetch err = %v, want ErrFetchTokenInUse", err)
	}
}

func TestPutFetch_OverwritesAfterTerminalState(t *testing.T) {
	mgr, _, _ := newManager(t)
	row := files.Fetch{
		Token:          "tok-2",
		MsgID:          testMsgB,
		PeerID:         peerA,
		UrlPath:        "/files/tok-2",
		ExpectedSize:   16,
		ExpectedSha256: strings.Repeat("b", 64),
		State:          files.FetchStatePending,
		CreatedAt:      time.Now().Unix(),
	}
	mustPutFetch(t, mgr, row)

	row.State = files.FetchStateFailedPermanent
	if err := mgr.UpdateFetch(row); err != nil {
		t.Fatalf("UpdateFetch terminal: %v", err)
	}

	row.State = files.FetchStatePending
	row.CreatedAt = time.Now().Unix() + 1
	if err := mgr.PutFetch(row); err != nil {
		t.Fatalf("re-PutFetch after terminal: %v", err)
	}
}

func TestListPendingFetches_RoundTrip(t *testing.T) {
	mgr, _, _ := newManager(t)
	pending := files.Fetch{Token: "p1", MsgID: testMsgA, PeerID: peerA, UrlPath: "/files/p1", ExpectedSize: 1, ExpectedSha256: "x", State: files.FetchStatePending}
	downloading := files.Fetch{Token: "p2", MsgID: testMsgB, PeerID: peerB, UrlPath: "/files/p2", ExpectedSize: 1, ExpectedSha256: "x", State: files.FetchStateDownloading}
	terminal := files.Fetch{Token: "p3", MsgID: "0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c", PeerID: peerC, UrlPath: "/files/p3", ExpectedSize: 1, ExpectedSha256: "x", State: files.FetchStateReady}
	mustPutFetch(t, mgr, pending)
	mustPutFetch(t, mgr, downloading)
	mustPutFetch(t, mgr, terminal)

	got, err := mgr.ListPendingFetches()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("pending count = %d, want 2", len(got))
	}
	tokens := map[string]bool{}
	for _, f := range got {
		tokens[f.Token] = true
	}
	if !tokens["p1"] || !tokens["p2"] || tokens["p3"] {
		t.Fatalf("unexpected tokens: %+v", tokens)
	}
}

func TestStaging_WriteReadDelete(t *testing.T) {
	mgr, _, dir := newManager(t)
	if err := mgr.EnsureStagingDir(); err != nil {
		t.Fatalf("EnsureStagingDir: %v", err)
	}

	stagingFile := filepath.Join(dir, "files", "staging", testMsgA)
	if err := os.MkdirAll(filepath.Dir(stagingFile), 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte("hello-staging")
	if err := os.WriteFile(stagingFile, body, 0o600); err != nil {
		t.Fatal(err)
	}

	f, sz, err := mgr.OpenStaging(testMsgA)
	if err != nil {
		t.Fatalf("OpenStaging: %v", err)
	}
	if sz != int64(len(body)) {
		t.Fatalf("size: got %d want %d", sz, len(body))
	}
	got, _ := io.ReadAll(f)
	f.Close()
	if string(got) != string(body) {
		t.Fatalf("staging bytes diverged: %q", got)
	}

	if got, err := mgr.StagingSize(testMsgA); err != nil || got != int64(len(body)) {
		t.Fatalf("StagingSize = (%d, %v), want (%d, nil)", got, err, len(body))
	}

	if err := mgr.DeleteStaging(testMsgA); err != nil {
		t.Fatalf("DeleteStaging: %v", err)
	}
	if got, err := mgr.StagingSize(testMsgA); err != nil || got != 0 {
		t.Fatalf("StagingSize after delete = (%d, %v), want (0, nil)", got, err)
	}
	if _, _, err := mgr.OpenStaging(testMsgA); !errors.Is(err, files.ErrBlobNotFound) {
		t.Fatalf("OpenStaging after delete err = %v, want ErrBlobNotFound", err)
	}
}

type fakeSink struct {
	mu     sync.Mutex
	events []files.FetchEvent
}

func (s *fakeSink) OnFetchEvent(ev files.FetchEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

func (s *fakeSink) lastState() files.FetchState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) == 0 {
		return ""
	}
	return s.events[len(s.events)-1].State
}

func waitForState(t *testing.T, mgr *files.Manager, token string, want files.FetchState) files.Fetch {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		row, err := mgr.GetFetch(token)
		if err == nil && row.State == want {
			return row
		}
		time.Sleep(20 * time.Millisecond)
	}
	row, err := mgr.GetFetch(token)
	t.Fatalf("fetch did not reach %q within 3s; row=%+v err=%v", want, row, err)
	return files.Fetch{}
}

func TestFetchWorker_HappyPath_Streams_AndVerifies(t *testing.T) {
	mgr, _, _ := newManager(t)

	body := []byte(strings.Repeat("X", 4096))
	digest := sha256.Sum256(body)
	digestHex := hex.EncodeToString(digest[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "", time.Time{}, &fakeSeeker{data: body})
	}))
	defer srv.Close()

	resolver := files.PeerResolverFunc(func(string) ([]string, error) {
		return []string{"unused"}, nil
	})
	clientForPeer := files.HTTPClientForPeer(func(string) (*http.Client, error) {
		return &http.Client{Transport: &rewriteTransport{base: srv.URL}}, nil
	})
	sink := &fakeSink{}

	w := files.NewWorker(mgr, resolver, clientForPeer, sink, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	defer cancel()

	tok := "fetch-tok-1"
	row := files.Fetch{
		Token:          tok,
		MsgID:          testMsgA,
		PeerID:         peerA,
		UrlPath:        "/files/" + tok,
		ExpectedSize:   int64(len(body)),
		ExpectedSha256: digestHex,
		State:          files.FetchStatePending,
		CreatedAt:      time.Now().Unix(),
	}
	mustPutFetch(t, mgr, row)
	w.Kick(tok)

	final := waitForState(t, mgr, tok, files.FetchStateReady)
	if final.BytesReceived != int64(len(body)) {
		t.Fatalf("BytesReceived = %d, want %d", final.BytesReceived, len(body))
	}

	f, sz, err := mgr.OpenStaging(testMsgA)
	if err != nil {
		t.Fatalf("OpenStaging: %v", err)
	}
	if sz != int64(len(body)) {
		t.Fatalf("staging size = %d, want %d", sz, len(body))
	}
	got, _ := io.ReadAll(f)
	f.Close()
	if string(got) != string(body) {
		t.Fatalf("staging bytes diverged from body")
	}
}

func TestFetchWorker_404_TerminatesPermanent(t *testing.T) {
	mgr, _, _ := newManager(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such token", http.StatusNotFound)
	}))
	defer srv.Close()

	resolver := files.PeerResolverFunc(func(string) ([]string, error) { return []string{"any"}, nil })
	clientForPeer := files.HTTPClientForPeer(func(string) (*http.Client, error) {
		return &http.Client{Transport: &rewriteTransport{base: srv.URL}}, nil
	})
	sink := &fakeSink{}

	w := files.NewWorker(mgr, resolver, clientForPeer, sink, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	tok := "tok-404"
	mustPutFetch(t, mgr, files.Fetch{
		Token:          tok,
		MsgID:          testMsgA,
		PeerID:         peerA,
		UrlPath:        "/files/" + tok,
		ExpectedSize:   1024,
		ExpectedSha256: strings.Repeat("0", 64),
		State:          files.FetchStatePending,
	})
	w.Kick(tok)

	row := waitForState(t, mgr, tok, files.FetchStateFailedPermanent)
	if row.LastError == "" {
		t.Fatalf("LastError is empty; want a 404 reason")
	}
}

func TestFetchWorker_ShaMismatch_Permanent(t *testing.T) {
	mgr, _, _ := newManager(t)
	body := []byte(strings.Repeat("y", 64))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "", time.Time{}, &fakeSeeker{data: body})
	}))
	defer srv.Close()

	resolver := files.PeerResolverFunc(func(string) ([]string, error) { return []string{"any"}, nil })
	clientForPeer := files.HTTPClientForPeer(func(string) (*http.Client, error) {
		return &http.Client{Transport: &rewriteTransport{base: srv.URL}}, nil
	})
	sink := &fakeSink{}
	w := files.NewWorker(mgr, resolver, clientForPeer, sink, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	tok := "tok-sha"
	mustPutFetch(t, mgr, files.Fetch{
		Token:          tok,
		MsgID:          testMsgA,
		PeerID:         peerA,
		UrlPath:        "/files/" + tok,
		ExpectedSize:   int64(len(body)),
		ExpectedSha256: strings.Repeat("0", 64),
		State:          files.FetchStatePending,
	})
	w.Kick(tok)

	row := waitForState(t, mgr, tok, files.FetchStateFailedPermanent)
	if !strings.Contains(row.LastError, "sha256 mismatch") {
		t.Fatalf("LastError = %q, want sha256 mismatch", row.LastError)
	}

	if _, _, err := mgr.OpenStaging(testMsgA); !errors.Is(err, files.ErrBlobNotFound) {
		t.Fatalf("staging not cleaned: err=%v", err)
	}
}

func TestFetchWorker_RangeResume(t *testing.T) {
	mgr, _, dir := newManager(t)

	body := []byte(strings.Repeat("Z", 512))
	digest := sha256.Sum256(body)
	digestHex := hex.EncodeToString(digest[:])

	if err := mgr.EnsureStagingDir(); err != nil {
		t.Fatal(err)
	}
	stagingPath := filepath.Join(dir, "files", "staging", testMsgA)
	if err := os.WriteFile(stagingPath, body[:100], 0o600); err != nil {
		t.Fatal(err)
	}

	var sawRange atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			sawRange.Store(true)
		}
		http.ServeContent(w, r, "", time.Time{}, &fakeSeeker{data: body})
	}))
	defer srv.Close()

	resolver := files.PeerResolverFunc(func(string) ([]string, error) { return []string{"any"}, nil })
	clientForPeer := files.HTTPClientForPeer(func(string) (*http.Client, error) {
		return &http.Client{Transport: &rewriteTransport{base: srv.URL}}, nil
	})
	sink := &fakeSink{}
	w := files.NewWorker(mgr, resolver, clientForPeer, sink, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	tok := "tok-resume"
	mustPutFetch(t, mgr, files.Fetch{
		Token:          tok,
		MsgID:          testMsgA,
		PeerID:         peerA,
		UrlPath:        "/files/" + tok,
		ExpectedSize:   int64(len(body)),
		ExpectedSha256: digestHex,
		State:          files.FetchStateDownloading,
		BytesReceived:  100,
	})
	w.Kick(tok)

	row := waitForState(t, mgr, tok, files.FetchStateReady)
	if row.BytesReceived != int64(len(body)) {
		t.Fatalf("BytesReceived = %d, want %d", row.BytesReceived, len(body))
	}
	if !sawRange.Load() {
		t.Fatalf("server never received a Range header")
	}
	final, _ := os.ReadFile(stagingPath)
	if string(final) != string(body) {
		t.Fatalf("final body diverged after resume")
	}
}

type fakeSeeker struct {
	data []byte
	pos  int64
}

func (s *fakeSeeker) Read(p []byte) (int, error) {
	if s.pos >= int64(len(s.data)) {
		return 0, io.EOF
	}
	n := copy(p, s.data[s.pos:])
	s.pos += int64(n)
	return n, nil
}

func (s *fakeSeeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		s.pos = offset
	case io.SeekCurrent:
		s.pos += offset
	case io.SeekEnd:
		s.pos = int64(len(s.data)) + offset
	}
	return s.pos, nil
}

type rewriteTransport struct{ base string }

func (rt *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {

	newURL := rt.base + req.URL.Path
	if req.URL.RawQuery != "" {
		newURL += "?" + req.URL.RawQuery
	}
	rewritten, err := http.NewRequestWithContext(req.Context(), req.Method, newURL, req.Body)
	if err != nil {
		return nil, err
	}
	for k, v := range req.Header {
		rewritten.Header[k] = v
	}
	if cl := req.ContentLength; cl > 0 {
		rewritten.ContentLength = cl
	}
	return http.DefaultTransport.RoundTrip(rewritten)
}

var _ = strconv.Itoa

func failedTransientRow(token, msgID, peerID string, attempts uint16, createdAt int64) files.Fetch {
	return files.Fetch{
		Token:          token,
		MsgID:          msgID,
		PeerID:         peerID,
		UrlPath:        "/files/" + token,
		ExpectedSize:   16,
		ExpectedSha256: strings.Repeat("a", 64),
		State:          files.FetchStatePending,
		CreatedAt:      createdAt,
		RetryAttempts:  attempts,
	}
}

func putTransient(t *testing.T, mgr *files.Manager, row files.Fetch) {
	t.Helper()
	mustPutFetch(t, mgr, row)
	row.State = files.FetchStateFailedTransient
	if err := mgr.UpdateFetch(row); err != nil {
		t.Fatalf("UpdateFetch %s: %v", row.Token, err)
	}
}

func TestRetryAllFailed_KicksSurvivors(t *testing.T) {
	mgr, _, _ := newManager(t)
	now := time.Now().Unix()
	for i, tok := range []string{"r1", "r2", "r3"} {
		putTransient(t, mgr, failedTransientRow(tok, "m"+strings.Repeat("0", 31)+strconv.Itoa(i), peerA, 1, now-int64(i)))
	}

	w := files.NewWorker(mgr, files.PeerResolverFunc(func(string) ([]string, error) { return nil, nil }), func(string) (*http.Client, error) { return nil, nil }, &fakeSink{}, nil)
	enq, err := w.RetryAllFailed(context.Background(), nil)
	if err != nil {
		t.Fatalf("RetryAllFailed: %v", err)
	}
	if enq != 3 {
		t.Fatalf("enqueued = %d, want 3", enq)
	}

	for _, tok := range []string{"r1", "r2", "r3"} {
		row, err := mgr.GetFetch(tok)
		if err != nil {
			t.Fatalf("GetFetch %s: %v", tok, err)
		}
		if row.State != files.FetchStateFailedTransient {
			t.Errorf("%s state = %s, want failed_transient", tok, row.State)
		}
	}
}

func TestRetryAllFailed_AttemptCap(t *testing.T) {
	mgr, _, _ := newManager(t)
	now := time.Now().Unix()
	putTransient(t, mgr, failedTransientRow("at-cap", testMsgA, peerA, files.RetryAttemptCap, now))
	putTransient(t, mgr, failedTransientRow("under-cap", testMsgB, peerA, files.RetryAttemptCap-1, now))

	w := files.NewWorker(mgr, files.PeerResolverFunc(func(string) ([]string, error) { return nil, nil }), func(string) (*http.Client, error) { return nil, nil }, &fakeSink{}, nil)
	enq, err := w.RetryAllFailed(context.Background(), nil)
	if err != nil {
		t.Fatalf("RetryAllFailed: %v", err)
	}
	if enq != 1 {
		t.Fatalf("enqueued = %d, want 1 (only under-cap kicks)", enq)
	}
	atCap, _ := mgr.GetFetch("at-cap")
	if atCap.State != files.FetchStateFailedPermanent {
		t.Errorf("at-cap state = %s, want failed_permanent", atCap.State)
	}
	if !strings.Contains(atCap.LastError, "attempt cap") {
		t.Errorf("at-cap LastError = %q", atCap.LastError)
	}
	under, _ := mgr.GetFetch("under-cap")
	if under.State != files.FetchStateFailedTransient {
		t.Errorf("under-cap state = %s, want failed_transient", under.State)
	}
}

func TestRetryAllFailed_AgeCap(t *testing.T) {
	mgr, _, _ := newManager(t)
	old := time.Now().Add(-files.RetryAgeCap - time.Second).Unix()
	fresh := time.Now().Unix()
	putTransient(t, mgr, failedTransientRow("stale", testMsgA, peerA, 1, old))
	putTransient(t, mgr, failedTransientRow("fresh", testMsgB, peerA, 1, fresh))

	w := files.NewWorker(mgr, files.PeerResolverFunc(func(string) ([]string, error) { return nil, nil }), func(string) (*http.Client, error) { return nil, nil }, &fakeSink{}, nil)
	enq, err := w.RetryAllFailed(context.Background(), nil)
	if err != nil {
		t.Fatalf("RetryAllFailed: %v", err)
	}
	if enq != 1 {
		t.Fatalf("enqueued = %d, want 1 (only fresh kicks)", enq)
	}
	stale, _ := mgr.GetFetch("stale")
	if stale.State != files.FetchStateFailedPermanent {
		t.Errorf("stale state = %s, want failed_permanent", stale.State)
	}
	if !strings.Contains(stale.LastError, "row age cap") {
		t.Errorf("stale LastError = %q", stale.LastError)
	}
}

func TestRetryAllFailed_BacklogCap(t *testing.T) {
	mgr, _, _ := newManager(t)

	base := time.Now().Unix()
	total := files.RetryBacklogCap + 1
	for i := 0; i < total; i++ {
		tok := fmt.Sprintf("bl-%02d", i)

		msg := fmt.Sprintf("%032x", i+1)
		putTransient(t, mgr, failedTransientRow(tok, msg, peerA, 1, base+int64(i)))
	}

	w := files.NewWorker(mgr, files.PeerResolverFunc(func(string) ([]string, error) { return nil, nil }), func(string) (*http.Client, error) { return nil, nil }, &fakeSink{}, nil)
	enq, err := w.RetryAllFailed(context.Background(), nil)
	if err != nil {
		t.Fatalf("RetryAllFailed: %v", err)
	}
	if enq != files.RetryBacklogCap {
		t.Fatalf("enqueued = %d, want %d", enq, files.RetryBacklogCap)
	}

	first, _ := mgr.GetFetch("bl-00")
	if first.State != files.FetchStateFailedPermanent {
		t.Errorf("bl-00 state = %s, want failed_permanent", first.State)
	}
	if !strings.Contains(first.LastError, "backlog cap") {
		t.Errorf("bl-00 LastError = %q", first.LastError)
	}
	last, _ := mgr.GetFetch(fmt.Sprintf("bl-%02d", total-1))
	if last.State != files.FetchStateFailedTransient {
		t.Errorf("bl-newest state = %s, want failed_transient", last.State)
	}
}

func TestRetryAllFailed_RetiredPeerBakesPermanent(t *testing.T) {
	mgr, _, _ := newManager(t)
	now := time.Now().Unix()
	putTransient(t, mgr, failedTransientRow("retired-row", testMsgA, peerA, 1, now))
	putTransient(t, mgr, failedTransientRow("live-row", testMsgB, peerB, 1, now))

	isRetired := func(peerID string) bool { return peerID == peerA }
	w := files.NewWorker(mgr, files.PeerResolverFunc(func(string) ([]string, error) { return nil, nil }), func(string) (*http.Client, error) { return nil, nil }, &fakeSink{}, nil)
	enq, err := w.RetryAllFailed(context.Background(), isRetired)
	if err != nil {
		t.Fatalf("RetryAllFailed: %v", err)
	}
	if enq != 1 {
		t.Fatalf("enqueued = %d, want 1", enq)
	}
	retired, _ := mgr.GetFetch("retired-row")
	if retired.State != files.FetchStateFailedPermanent {
		t.Errorf("retired-row state = %s, want failed_permanent", retired.State)
	}
	if !strings.Contains(retired.LastError, "peer retired") {
		t.Errorf("retired-row LastError = %q", retired.LastError)
	}
}

func TestFetchWorker_RetryAttemptsBumpsOnTransient(t *testing.T) {
	mgr, _, _ := newManager(t)

	resolver := files.PeerResolverFunc(func(string) ([]string, error) { return nil, nil })
	clientForPeer := files.HTTPClientForPeer(func(string) (*http.Client, error) {
		return &http.Client{}, nil
	})
	sink := &fakeSink{}
	w := files.NewWorker(mgr, resolver, clientForPeer, sink, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	tok := "bump-tok"
	mustPutFetch(t, mgr, files.Fetch{
		Token:          tok,
		MsgID:          testMsgA,
		PeerID:         peerA,
		UrlPath:        "/files/" + tok,
		ExpectedSize:   1,
		ExpectedSha256: strings.Repeat("0", 64),
		State:          files.FetchStatePending,
		CreatedAt:      time.Now().Unix(),
	})
	w.Kick(tok)

	row := waitForState(t, mgr, tok, files.FetchStateFailedTransient)
	if row.RetryAttempts != 1 {
		t.Fatalf("RetryAttempts = %d, want 1 after first transient", row.RetryAttempts)
	}
}

func TestFetchWorker_SemaphoreCapsParallelism(t *testing.T) {
	mgr, _, _ := newManager(t)

	release := make(chan struct{})
	var concurrent atomic.Int32
	var peakConcurrent atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := concurrent.Add(1)
		for {
			peak := peakConcurrent.Load()
			if c <= peak || peakConcurrent.CompareAndSwap(peak, c) {
				break
			}
		}
		defer concurrent.Add(-1)
		w.Header().Set("Content-Length", "1024")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		if flusher != nil {
			flusher.Flush()
		}
		<-release
	}))
	defer srv.Close()
	defer close(release)

	resolver := files.PeerResolverFunc(func(string) ([]string, error) { return []string{"any"}, nil })
	clientForPeer := files.HTTPClientForPeer(func(string) (*http.Client, error) {
		return &http.Client{Transport: &rewriteTransport{base: srv.URL}}, nil
	})
	sink := &fakeSink{}
	w := files.NewWorker(mgr, resolver, clientForPeer, sink, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	const total = 25
	for i := 0; i < total; i++ {
		tok := fmt.Sprintf("sem-%02d", i)
		msg := fmt.Sprintf("%032x", i+1)
		mustPutFetch(t, mgr, files.Fetch{
			Token:          tok,
			MsgID:          msg,
			PeerID:         peerA,
			UrlPath:        "/files/" + tok,
			ExpectedSize:   1024,
			ExpectedSha256: strings.Repeat("0", 64),
			State:          files.FetchStatePending,
			CreatedAt:      time.Now().Unix(),
		})
		w.Kick(tok)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if concurrent.Load() == int32(files.FetchParallelism) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	time.Sleep(150 * time.Millisecond)

	if got := peakConcurrent.Load(); got > int32(files.FetchParallelism) {
		t.Fatalf("peak concurrent = %d, exceeds cap %d", got, files.FetchParallelism)
	}
	if got := peakConcurrent.Load(); got < int32(files.FetchParallelism) {
		t.Fatalf("peak concurrent = %d, did not reach cap %d (sem may be unwired)", got, files.FetchParallelism)
	}
}
