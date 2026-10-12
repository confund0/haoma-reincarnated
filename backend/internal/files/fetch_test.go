package files_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

	if got, err := mgr.StagingPartSize(testMsgA); err != nil || got != 0 {
		t.Fatalf("StagingPartSize with no .part file = (%d, %v), want (0, nil)", got, err)
	}
	part := []byte("partial")
	if err := os.WriteFile(mgr.StagingPartPath(testMsgA), part, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := mgr.StagingPartSize(testMsgA); err != nil || got != int64(len(part)) {
		t.Fatalf("StagingPartSize = (%d, %v), want (%d, nil)", got, err, len(part))
	}

	if err := mgr.DeleteStaging(testMsgA); err != nil {
		t.Fatalf("DeleteStaging: %v", err)
	}
	if got, err := mgr.StagingPartSize(testMsgA); err != nil || got != 0 {
		t.Fatalf("StagingPartSize after delete = (%d, %v), want (0, nil)", got, err)
	}
	if _, _, err := mgr.OpenStaging(testMsgA); !errors.Is(err, files.ErrBlobNotFound) {
		t.Fatalf("OpenStaging after delete err = %v, want ErrBlobNotFound", err)
	}
}

func TestStaging_OpenRefusesPart(t *testing.T) {
	mgr, _, _ := newManager(t)
	if err := mgr.EnsureStagingDir(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mgr.StagingPartPath(testMsgA), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := mgr.OpenStaging(testMsgA); !errors.Is(err, files.ErrBlobNotFound) {
		t.Fatalf("OpenStaging with only a .part file err = %v, want ErrBlobNotFound", err)
	}
	if _, _, err := mgr.OpenStaging(testMsgA + files.StagingPartSuffix); !errors.Is(err, files.ErrBlobNotFound) {
		t.Fatalf("OpenStaging of a .part name err = %v, want ErrBlobNotFound", err)
	}
	if err := mgr.DeleteStaging(testMsgA + files.StagingPartSuffix); !errors.Is(err, files.ErrStagingIDInvalid) {
		t.Fatalf("DeleteStaging of a .part name err = %v, want ErrStagingIDInvalid", err)
	}
	if _, err := os.Stat(mgr.StagingPartPath(testMsgA)); err != nil {
		t.Fatalf(".part file gone after refused calls: %v", err)
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
	assertFailReason(t, row, sink, files.FetchFailBlobGone)
}

func assertFailReason(t *testing.T, row files.Fetch, sink *fakeSink, want files.FetchFailReason) {
	t.Helper()
	if row.FailReason != want {
		t.Errorf("row fail reason = %q, want %q", row.FailReason, want)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		sink.mu.Lock()
		var last files.FetchEvent
		n := len(sink.events)
		if n > 0 {
			last = sink.events[n-1]
		}
		sink.mu.Unlock()
		if last.State == files.FetchStateFailedPermanent {
			if last.Reason != want {
				t.Errorf("event reason = %q, want %q", last.Reason, want)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no failed_permanent event emitted; last = %+v", last)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestFetchWorker_PermanentFailureNamesItsReason(t *testing.T) {
	body := []byte(strings.Repeat("z", 64))
	sum := sha256.Sum256(body)
	goodSha := hex.EncodeToString(sum[:])
	cases := map[string]struct {
		status   int
		size     int64
		sha      string
		want     files.FetchFailReason
		errMatch string
	}{
		"410":           {status: http.StatusGone, size: 64, sha: goodSha, want: files.FetchFailBlobGone, errMatch: "410"},
		"size mismatch": {status: http.StatusOK, size: 65, sha: goodSha, want: files.FetchFailIntegrity, errMatch: "size mismatch"},
		"oversized":     {status: http.StatusOK, size: 10, sha: goodSha, want: files.FetchFailIntegrity, errMatch: "oversized"},
		"sha mismatch":  {status: http.StatusOK, size: 64, sha: strings.Repeat("0", 64), want: files.FetchFailIntegrity, errMatch: "sha256 mismatch"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			mgr, _, _ := newManager(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if c.status != http.StatusOK {
					http.Error(w, "gone", c.status)
					return
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

			tok := "tok-reason"
			mustPutFetch(t, mgr, files.Fetch{
				Token:          tok,
				MsgID:          testMsgA,
				PeerID:         peerA,
				UrlPath:        "/files/" + tok,
				ExpectedSize:   c.size,
				ExpectedSha256: c.sha,
				State:          files.FetchStatePending,
			})
			w.Kick(tok)
			row := waitForState(t, mgr, tok, files.FetchStateFailedPermanent)
			if !strings.Contains(row.LastError, c.errMatch) {
				t.Errorf("LastError = %q, want it to name %q", row.LastError, c.errMatch)
			}
			assertFailReason(t, row, sink, c.want)
		})
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
	assertFailReason(t, row, sink, files.FetchFailIntegrity)

	if _, _, err := mgr.OpenStaging(testMsgA); !errors.Is(err, files.ErrBlobNotFound) {
		t.Fatalf("staging not cleaned: err=%v", err)
	}
}

func TestFetchWorker_RangeResume(t *testing.T) {
	mgr, _, _ := newManager(t)

	body := []byte(strings.Repeat("Z", 512))
	digest := sha256.Sum256(body)
	digestHex := hex.EncodeToString(digest[:])

	if err := mgr.EnsureStagingDir(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mgr.StagingPartPath(testMsgA), body[:100], 0o600); err != nil {
		t.Fatal(err)
	}

	var gotRange atomic.Value
	gotRange.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRange.Store(r.Header.Get("Range"))
		http.ServeContent(w, r, "", time.Time{}, &fakeSeeker{data: body})
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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
	kickTestWorker(ctx, mgr, srv.URL, tok)

	row := waitForState(t, mgr, tok, files.FetchStateReady)
	if row.BytesReceived != int64(len(body)) {
		t.Fatalf("BytesReceived = %d, want %d", row.BytesReceived, len(body))
	}
	if got := gotRange.Load().(string); got != "bytes=100-" {
		t.Fatalf("Range header = %q, want %q", got, "bytes=100-")
	}
	assertStagedBlob(t, mgr, testMsgA, body)
}

func TestFetchWorker_FinalPathAbsentWhileDownloading(t *testing.T) {
	mgr, _, _ := newManager(t)
	body := []byte(strings.Repeat("P", 8192))
	digest := sha256.Sum256(body)
	half := len(body) / 2

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body[:half])
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write(body[half:])
	}))
	defer srv.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tok := "tok-inflight"
	mustPutFetch(t, mgr, files.Fetch{
		Token:          tok,
		MsgID:          testMsgA,
		PeerID:         peerA,
		UrlPath:        "/files/" + tok,
		ExpectedSize:   int64(len(body)),
		ExpectedSha256: hex.EncodeToString(digest[:]),
		State:          files.FetchStatePending,
	})
	kickTestWorker(ctx, mgr, srv.URL, tok)

	waitForPartSize(t, mgr, testMsgA, int64(half))
	if row, err := mgr.GetFetch(tok); err != nil || row.State != files.FetchStateDownloading {
		t.Fatalf("row = %+v, err = %v; want downloading", row, err)
	}
	if f, sz, err := mgr.OpenStaging(testMsgA); !errors.Is(err, files.ErrBlobNotFound) {
		if f != nil {
			f.Close()
		}
		t.Fatalf("OpenStaging during the download = (size %d, err %v), want ErrBlobNotFound", sz, err)
	}

	close(release)
	waitForState(t, mgr, tok, files.FetchStateReady)
	assertStagedBlob(t, mgr, testMsgA, body)
}

func TestFetchWorker_PartSurvivesRestartAndResumes(t *testing.T) {
	mgr, _, _ := newManager(t)
	body := []byte(strings.Repeat("R", 6000))
	digest := sha256.Sum256(body)
	half := 2500

	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body[:half])
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv1.Close()

	tok := "tok-restart"
	mustPutFetch(t, mgr, files.Fetch{
		Token:          tok,
		MsgID:          testMsgA,
		PeerID:         peerA,
		UrlPath:        "/files/" + tok,
		ExpectedSize:   int64(len(body)),
		ExpectedSha256: hex.EncodeToString(digest[:]),
		State:          files.FetchStatePending,
	})
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	kickTestWorker(ctx1, mgr, srv1.URL, tok)
	waitForPartSize(t, mgr, testMsgA, int64(half))
	cancel1()

	waitForState(t, mgr, tok, files.FetchStateFailedTransient)
	row, err := mgr.GetFetch(tok)
	if err != nil {
		t.Fatal(err)
	}
	row.State = files.FetchStateDownloading
	if err := mgr.UpdateFetch(row); err != nil {
		t.Fatal(err)
	}

	if scan, err := mgr.ReconcileStaging(); err != nil || scan != (files.StagingScan{}) {
		t.Fatalf("ReconcileStaging = (%+v, %v), want nothing done", scan, err)
	}
	if sz, err := mgr.StagingPartSize(testMsgA); err != nil || sz != int64(half) {
		t.Fatalf(".part size after restart scan = (%d, %v), want (%d, nil)", sz, err, half)
	}
	var gotRange atomic.Value
	gotRange.Store("")
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRange.Store(r.Header.Get("Range"))
		http.ServeContent(w, r, "", time.Time{}, &fakeSeeker{data: body})
	}))
	defer srv2.Close()
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	startTestWorker(ctx2, mgr, srv2.URL)

	waitForState(t, mgr, tok, files.FetchStateReady)
	if got, want := gotRange.Load().(string), fmt.Sprintf("bytes=%d-", half); got != want {
		t.Fatalf("Range header after restart = %q, want %q", got, want)
	}
	assertStagedBlob(t, mgr, testMsgA, body)
}

func TestReconcileStaging_UpgradeMoveResumes(t *testing.T) {
	mgr, _, _ := newManager(t)
	body := []byte(strings.Repeat("U", 3000))
	digest := sha256.Sum256(body)
	if err := mgr.EnsureStagingDir(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mgr.StagingPath(testMsgA), body[:1000], 0o600); err != nil {
		t.Fatal(err)
	}
	tok := "tok-upgrade"
	mustPutFetch(t, mgr, files.Fetch{
		Token:          tok,
		MsgID:          testMsgA,
		PeerID:         peerA,
		UrlPath:        "/files/" + tok,
		ExpectedSize:   int64(len(body)),
		ExpectedSha256: hex.EncodeToString(digest[:]),
		State:          files.FetchStateDownloading,
		BytesReceived:  1000,
	})

	scan, err := mgr.ReconcileStaging()
	if err != nil || scan != (files.StagingScan{MovedToPart: 1}) {
		t.Fatalf("ReconcileStaging = (%+v, %v), want one moved to .part", scan, err)
	}
	if _, _, err := mgr.OpenStaging(testMsgA); !errors.Is(err, files.ErrBlobNotFound) {
		t.Fatalf("OpenStaging after the move err = %v, want ErrBlobNotFound", err)
	}
	if sz, err := mgr.StagingPartSize(testMsgA); err != nil || sz != 1000 {
		t.Fatalf(".part size after the move = (%d, %v), want (1000, nil)", sz, err)
	}

	var gotRange atomic.Value
	gotRange.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRange.Store(r.Header.Get("Range"))
		http.ServeContent(w, r, "", time.Time{}, &fakeSeeker{data: body})
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startTestWorker(ctx, mgr, srv.URL)

	waitForState(t, mgr, tok, files.FetchStateReady)
	if got := gotRange.Load().(string); got != "bytes=1000-" {
		t.Fatalf("Range header = %q, want %q", got, "bytes=1000-")
	}
	assertStagedBlob(t, mgr, testMsgA, body)
}

func TestReconcileStaging_Rules(t *testing.T) {
	mgr, _, _ := newManager(t)
	if err := mgr.EnsureStagingDir(); err != nil {
		t.Fatal(err)
	}
	type fileCase struct {
		state     files.FetchState
		final     bool
		part      bool
		partBody  string
		wantFinal bool
		wantPart  bool
	}
	cases := map[string]fileCase{
		"final, downloading":        {state: files.FetchStateDownloading, final: true, wantPart: true},
		"final, pending":            {state: files.FetchStatePending, final: true, wantPart: true},
		"final, failed transient":   {state: files.FetchStateFailedTransient, final: true, wantPart: true},
		"final, ready":              {state: files.FetchStateReady, final: true, wantFinal: true},
		"final, failed permanent":   {state: files.FetchStateFailedPermanent, final: true},
		"final, no row":             {final: true, wantFinal: true},
		"final and part, pending":   {state: files.FetchStatePending, final: true, part: true, wantFinal: true, wantPart: true},
		"part, downloading":         {state: files.FetchStateDownloading, part: true, wantPart: true},
		"part, failed transient":    {state: files.FetchStateFailedTransient, part: true, wantPart: true},
		"part, no row":              {part: true},
		"part, failed permanent":    {state: files.FetchStateFailedPermanent, part: true},
		"part, ready":               {state: files.FetchStateReady, part: true},
		"final and part, permanent": {state: files.FetchStateFailedPermanent, final: true, part: true},

		"part, ready, whole":     {state: files.FetchStateReady, part: true, partBody: "whole", wantFinal: true},
		"part, ready, wrong sha": {state: files.FetchStateReady, part: true, partBody: "wholX"},
		"final and part, ready":  {state: files.FetchStateReady, final: true, part: true, partBody: "whole", wantFinal: true},
	}
	wholeSum := sha256.Sum256([]byte("whole"))
	wholeSha := hex.EncodeToString(wholeSum[:])
	ids := map[string]string{}
	n := 0
	for name, c := range cases {
		n++
		id := fmt.Sprintf("%032x", n)
		ids[name] = id
		if c.final {
			if err := os.WriteFile(mgr.StagingPath(id), []byte("final"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if c.part {
			body := c.partBody
			if body == "" {
				body = "part"
			}
			if err := os.WriteFile(mgr.StagingPartPath(id), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if c.state != "" {
			tok := "tok-" + id
			mustPutFetch(t, mgr, files.Fetch{Token: tok, MsgID: id, PeerID: peerA, UrlPath: "/files/" + tok, ExpectedSize: 5, ExpectedSha256: wholeSha, State: files.FetchStatePending})
			if err := mgr.UpdateFetch(files.Fetch{Token: tok, MsgID: id, PeerID: peerA, UrlPath: "/files/" + tok, ExpectedSize: 5, ExpectedSha256: wholeSha, State: c.state}); err != nil {
				t.Fatal(err)
			}
		}
	}

	scan, err := mgr.ReconcileStaging()
	if err != nil {
		t.Fatalf("ReconcileStaging: %v", err)
	}
	if want := (files.StagingScan{MovedToPart: 3, RestoredFinal: 1, Removed: 8}); scan != want {
		t.Errorf("ReconcileStaging = %+v, want %+v", scan, want)
	}
	exists := func(path string) bool {
		_, err := os.Stat(path)
		return err == nil
	}
	for name, c := range cases {
		id := ids[name]
		if got := exists(mgr.StagingPath(id)); got != c.wantFinal {
			t.Errorf("%s: final file present = %v, want %v", name, got, c.wantFinal)
		}
		if got := exists(mgr.StagingPartPath(id)); got != c.wantPart {
			t.Errorf("%s: .part file present = %v, want %v", name, got, c.wantPart)
		}
	}
}

func TestFetchWorker_WholePartFinishesWithoutRequest(t *testing.T) {
	body := []byte(strings.Repeat("W", 2048))
	sum := sha256.Sum256(body)
	cases := map[string]struct {
		onDisk      []byte
		atFinal     bool
		wantRequest bool
	}{
		"whole .part":             {onDisk: body},
		"final path, then a scan": {onDisk: body, atFinal: true},
		"full size, wrong sha":    {onDisk: []byte(strings.Repeat("V", len(body))), wantRequest: true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			mgr, _, _ := newManager(t)
			if err := mgr.EnsureStagingDir(); err != nil {
				t.Fatal(err)
			}
			path := mgr.StagingPartPath(testMsgA)
			if c.atFinal {
				path = mgr.StagingPath(testMsgA)
			}
			if err := os.WriteFile(path, c.onDisk, 0o600); err != nil {
				t.Fatal(err)
			}
			tok := "tok-whole"
			mustPutFetch(t, mgr, files.Fetch{
				Token:          tok,
				MsgID:          testMsgA,
				PeerID:         peerA,
				UrlPath:        "/files/" + tok,
				ExpectedSize:   int64(len(body)),
				ExpectedSha256: hex.EncodeToString(sum[:]),
				State:          files.FetchStateDownloading,
				BytesReceived:  int64(len(body)),
			})
			if c.atFinal {
				if scan, err := mgr.ReconcileStaging(); err != nil || scan.MovedToPart != 1 {
					t.Fatalf("ReconcileStaging = (%+v, %v), want one moved to .part", scan, err)
				}
			}

			var requests atomic.Int32
			var gotRange atomic.Value
			gotRange.Store("")
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				gotRange.Store(r.Header.Get("Range"))
				http.ServeContent(w, r, "", time.Time{}, &fakeSeeker{data: body})
			}))
			defer srv.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			startTestWorker(ctx, mgr, srv.URL)

			waitForState(t, mgr, tok, files.FetchStateReady)
			assertStagedBlob(t, mgr, testMsgA, body)
			if n := requests.Load(); c.wantRequest != (n > 0) {
				t.Fatalf("requests = %d, want a request = %v", n, c.wantRequest)
			}
			if c.wantRequest && gotRange.Load().(string) != "" {
				t.Fatalf("Range header = %q, want none (download from byte 0)", gotRange.Load())
			}
		})
	}
}

func startTestWorker(ctx context.Context, mgr *files.Manager, base string) *files.Worker {
	resolver := files.PeerResolverFunc(func(string) ([]string, error) { return []string{"any"}, nil })
	clientForPeer := files.HTTPClientForPeer(func(string) (*http.Client, error) {
		return &http.Client{Transport: &rewriteTransport{base: base}}, nil
	})
	w := files.NewWorker(mgr, resolver, clientForPeer, &fakeSink{}, nil)
	go w.Run(ctx)
	return w
}

func kickTestWorker(ctx context.Context, mgr *files.Manager, base, tok string) {
	startTestWorker(ctx, mgr, base).Kick(tok)
}

func waitForPartSize(t *testing.T, mgr *files.Manager, msgID string, want int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if sz, err := mgr.StagingPartSize(msgID); err == nil && sz == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	sz, err := mgr.StagingPartSize(msgID)
	t.Fatalf(".part size = (%d, %v), want %d within 3s", sz, err, want)
}

func assertStagedBlob(t *testing.T, mgr *files.Manager, msgID string, want []byte) {
	t.Helper()
	f, sz, err := mgr.OpenStaging(msgID)
	if err != nil {
		t.Fatalf("OpenStaging: %v", err)
	}
	got, _ := io.ReadAll(f)
	f.Close()
	if sz != int64(len(want)) || string(got) != string(want) {
		t.Fatalf("staged blob: size %d, want %d; bytes equal = %v", sz, len(want), string(got) == string(want))
	}
	gotSum, wantSum := sha256.Sum256(got), sha256.Sum256(want)
	if gotSum != wantSum {
		t.Fatalf("staged blob sha differs")
	}
	if _, err := os.Stat(mgr.StagingPartPath(msgID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf(".part file left after ready: stat err = %v", err)
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
	if atCap.FailReason != files.FetchFailLimit {
		t.Errorf("at-cap fail reason = %q, want limit", atCap.FailReason)
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
	if stale.FailReason != files.FetchFailLimit {
		t.Errorf("stale fail reason = %q, want limit", stale.FailReason)
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
	if first.FailReason != files.FetchFailLimit {
		t.Errorf("bl-00 fail reason = %q, want limit", first.FailReason)
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
	if retired.FailReason != files.FetchFailLimit {
		t.Errorf("retired-row fail reason = %q, want limit", retired.FailReason)
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

type dialFailTransport struct{}

func (dialFailTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("dial tcp: socks connect: host unreachable")
}

func TestFetchWorker_DialErrorKeepsNoTokenOrAddress(t *testing.T) {
	mgr, _, _ := newManager(t)
	const onion = "abcdefghijklmnopqrstuvwxyz234567abcdefghijklmnopqrstuvwx"
	resolver := files.PeerResolverFunc(func(string) ([]string, error) { return []string{onion}, nil })
	clientForPeer := files.HTTPClientForPeer(func(string) (*http.Client, error) {
		return &http.Client{Transport: dialFailTransport{}}, nil
	})
	sink := &fakeSink{}
	w := files.NewWorker(mgr, resolver, clientForPeer, sink, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	tok := "Zt0k3n-secret-capability-value-4d9f1a7c"
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
	if !strings.Contains(row.LastError, "host unreachable") {
		t.Fatalf("precondition: LastError = %q, want the dial error it came from", row.LastError)
	}
	texts := []string{row.LastError}
	sink.mu.Lock()
	for _, ev := range sink.events {
		texts = append(texts, ev.LastError)
	}
	sink.mu.Unlock()
	for _, text := range texts {
		if strings.Contains(text, tok) {
			t.Errorf("the token survived into a fetch error: %q", text)
		}

		if strings.Contains(text, onion[:8]) {
			t.Errorf("the address, or its prefix, survived into a fetch error: %q", text)
		}
	}
}

func TestFetchEvent_ReasonWireName(t *testing.T) {
	raw, err := json.Marshal(files.FetchEvent{State: files.FetchStateFailedPermanent, Reason: files.FetchFailBlobGone})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"reason":"blob_gone"`) {
		t.Fatalf("event JSON = %s, want the reason under \"reason\"", raw)
	}
	for _, r := range []files.FetchFailReason{files.FetchFailBlobGone, files.FetchFailIntegrity, files.FetchFailLimit} {
		switch string(r) {
		case "blob_gone", "integrity", "limit":
		default:
			t.Errorf("reason %q is not one haoma knows", r)
		}
	}
}
