package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"haoma/internal/files"
	"haoma/internal/logging"
	"haoma/internal/store"
)

type toServer struct{ srv *url.URL }

func (t toServer) RoundTrip(r *http.Request) (*http.Response, error) {
	out := r.Clone(r.Context())
	out.Host = r.URL.Host
	out.URL.Scheme = t.srv.Scheme
	out.URL.Host = t.srv.Host
	return http.DefaultTransport.RoundTrip(out)
}

type failDial struct{}

func (failDial) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("dial tcp: socks connect: host unreachable")
}

func TestFileLogs_NoTokenNoAddressAtDebug(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "haomad.log")
	logger, closeLog, err := logging.New(logging.Config{Level: "debug", Format: "text", File: logPath})
	if err != nil {
		t.Fatal(err)
	}
	restore := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(restore); _ = closeLog() })

	const dialled = "servedpeeraaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const publishedAddr = "publishedbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	server := newFilesTestDaemon(t)
	peer := freshTestPeer(t, dialled)
	peer.MyOnionAddr = publishedAddr
	if err := server.registry.Add(peer); err != nil {
		t.Fatal(err)
	}
	blob := []byte("ciphertext of a small file")
	tokens, err := server.files.StageBlob(filesTestMsgID, blob, []string{peer.ID}, 0)
	if err != nil {
		t.Fatal(err)
	}
	tok := tokens[0]
	mux := http.NewServeMux()
	mux.HandleFunc("GET /files/{token}", server.handleFileFetch)
	srv := httptest.NewServer(server.unlinkGate(mux))
	defer srv.Close()
	srvURL, _ := url.Parse(srv.URL)

	fetcher := newFilesTestDaemon(t)
	published := peer.MyOnionAddr
	resolver := files.PeerResolverFunc(func(string) ([]string, error) { return []string{published}, nil })
	client := files.HTTPClientForPeer(func(string) (*http.Client, error) {
		return &http.Client{Transport: toServer{srv: srvURL}}, nil
	})
	w := files.NewWorker(fetcher.files, resolver, client, nil, logger)
	ctx := t.Context()
	go w.Run(ctx)

	digest := sha256.Sum256(blob)
	sum := hex.EncodeToString(digest[:])
	putFetch(t, fetcher.files, tok, "fetch-ok-id", int64(len(blob)), sum)
	w.Kick(tok)
	waitFetchState(t, fetcher.files, tok, files.FetchStateReady)

	const badTok = "Zt0k3n-secret-capability-value-4d9f1a7c"
	failing := files.NewWorker(fetcher.files, resolver, files.HTTPClientForPeer(func(string) (*http.Client, error) {
		return &http.Client{Transport: failDial{}}, nil
	}), nil, logger)
	go failing.Run(ctx)
	putFetch(t, fetcher.files, badTok, "fetch-bad-id", 10, strings.Repeat("0", 64))
	failing.Kick(badTok)
	waitFetchState(t, fetcher.files, badTok, files.FetchStateFailedTransient)

	const cappedTok = "Cap0ed-token-at-the-attempt-limit-77e1b2"
	if err := fetcher.files.PutFetch(files.Fetch{
		Token: cappedTok, MsgID: "fetch-cap-id", PeerID: "fetch-peer", UrlPath: "/files/" + cappedTok,
		ExpectedSize: 10, ExpectedSha256: strings.Repeat("0", 64), State: files.FetchStateFailedTransient,
		RetryAttempts: files.RetryAttemptCap, CreatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := failing.RetryAllFailed(ctx, nil); err != nil {
		t.Fatal(err)
	}

	api := httptest.NewServer(server.apiHandler())
	defer api.Close()
	const unknownTok = "Unkn0wn-token-never-minted-3c2b1a0f9e8d7c6b"
	for _, rr := range []receiveReceiptRequest{
		{Token: tok, RecipientPeerID: peer.ID},
		{Token: tok, RecipientPeerID: "someone-else"},
		{Token: unknownTok, RecipientPeerID: peer.ID},
	} {
		resp := postJSON(t, api, "/files/receipts", rr)
		resp.Body.Close()
	}

	goneTokens, err := server.files.StageBlob("0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b", blob, []string{peer.ID}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := removeBlobByMsg(server, "0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b"); err != nil {
		t.Fatal(err)
	}
	if resp, err := http.Get(srv.URL + "/files/" + goneTokens[0]); err == nil {
		resp.Body.Close()
	}

	if err := server.store.PutUnlinkPending(store.UnlinkPending{
		PeerID: peer.ID, Deadline: time.Now().Add(time.Hour).Unix(), StartedAt: time.Now().Unix(), Proof: "ab",
	}); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/files/"+tok, nil)
	req.Host = published + ".onion"
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}

	_ = closeLog()
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	for _, want := range []string{"fetch attempt", "fetch ready", "fetch failed transient", "token outlived blob", "unlink: dropped a request", "attempt cap reached", "file receipt acked", "/files/receipts recipient mismatch", "/files/receipts unknown token"} {
		if !strings.Contains(out, want) {
			t.Fatalf("precondition: the run never logged %q, so the check below reads less than it claims:\n%s", want, out)
		}
	}
	secrets := map[string]string{
		"token":             tok,
		"token prefix":      tok[:8],
		"failed token":      badTok[:8],
		"capped token":      cappedTok[:8],
		"unknown token":     unknownTok[:8],
		"gone blob's token": goneTokens[0][:8],
		"dialled address":   dialled[:8],
		"published address": published[:8],
	}
	for name, s := range secrets {
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, s) {
				t.Errorf("the %s reached a log line: %s", name, line)
			}
		}
	}
}

func putFetch(t *testing.T, mgr *files.Manager, tok, id string, size int64, sha string) {
	t.Helper()
	if err := mgr.PutFetch(files.Fetch{
		Token: tok, MsgID: id, PeerID: "fetch-peer", UrlPath: "/files/" + tok,
		ExpectedSize: size, ExpectedSha256: sha, State: files.FetchStatePending, CreatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
}

func waitFetchState(t *testing.T, mgr *files.Manager, tok string, want files.FetchState) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if row, err := mgr.GetFetch(tok); err == nil && row.State == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	row, _ := mgr.GetFetch(tok)
	t.Fatalf("fetch never reached %s (at %s, %s)", want, row.State, row.LastError)
}
