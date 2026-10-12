package files_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"haoma/internal/files"
	"haoma/internal/xport"
)

type cutTransport struct{}

func (cutTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("%w: cut at 30 s", xport.ErrCircuitRenewed)
}

func TestFetchWorker_CircuitRenewedAsksAgainAtOnce(t *testing.T) {
	mgr, _, _ := newManager(t)
	body := []byte(strings.Repeat("Y", 2048))
	digest := sha256.Sum256(body)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "", time.Time{}, &fakeSeeker{data: body})
	}))
	defer srv.Close()

	var clients atomic.Int32
	resolver := files.PeerResolverFunc(func(string) ([]string, error) { return []string{"any"}, nil })
	clientForPeer := files.HTTPClientForPeer(func(string) (*http.Client, error) {
		if clients.Add(1) == 1 {
			return &http.Client{Transport: cutTransport{}}, nil
		}
		return &http.Client{Transport: &rewriteTransport{base: srv.URL}}, nil
	})
	w := files.NewWorker(mgr, resolver, clientForPeer, &fakeSink{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	tok := "tok-renewed"
	mustPutFetch(t, mgr, files.Fetch{
		Token:          tok,
		MsgID:          testMsgA,
		PeerID:         peerA,
		UrlPath:        "/files/" + tok,
		ExpectedSize:   int64(len(body)),
		ExpectedSha256: hex.EncodeToString(digest[:]),
		State:          files.FetchStatePending,
		CreatedAt:      time.Now().Unix(),
	})
	w.Kick(tok)
	waitForState(t, mgr, tok, files.FetchStateReady)
	if n := clients.Load(); n != 2 {
		t.Fatalf("clients built = %d, want 2: the cut one and the one on the new circuit", n)
	}
}
