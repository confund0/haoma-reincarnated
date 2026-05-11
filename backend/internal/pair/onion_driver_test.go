package pair

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"haoma/internal/tor/control"
)

type fakePublisher struct {
	mu      sync.Mutex
	target  string
	port    int
	deleted []string
	addErr  error
}

func (f *fakePublisher) AddOnion(_ string, ports []control.OnionPort, _ ...string) (*control.Onion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.addErr != nil {
		return nil, f.addErr
	}
	if len(ports) == 0 || ports[0].Target == "" {
		return nil, errors.New("fakePublisher: missing target")
	}
	f.target = ports[0].Target
	f.port = ports[0].VirtPort

	return &control.Onion{ServiceID: "fakeonionserviceidnotrealxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxad"}, nil
}

func (f *fakePublisher) DelOnion(serviceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, serviceID)
	return nil
}

func (f *fakePublisher) delCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.deleted)
}

func (f *fakePublisher) localTarget() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.target
}

type directDialer struct{ target string }

func (d *directDialer) Do(req *http.Request) (*http.Response, error) {
	cloned := *req
	u := *req.URL
	u.Host = d.target
	cloned.URL = &u
	return http.DefaultClient.Do(&cloned)
}

func TestOnionDriver_RoundTrip(t *testing.T) {
	t.Parallel()
	pub := &fakePublisher{}
	driver := NewOnionDriver(pub, nil)
	driver.SkipServiceIDCheckForTest()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	inviterPayload := []byte(`{"side":"inviter","peer_id":"abc","nick":"Bob"}`)
	pi, err := driver.CreateInvite(ctx, CreateRequest{
		Payload: inviterPayload,
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	oob := pi.OOB()
	if len(oob.Words) != OnionWordCount {
		t.Fatalf("OOB.Words len %d, want %d", len(oob.Words), OnionWordCount)
	}
	if oob.Tag != "onion" {
		t.Errorf("OOB.Tag = %q, want \"onion\"", oob.Tag)
	}
	if pi.ExpiresAt() == 0 {
		t.Error("ExpiresAt = 0")
	}

	target := pub.localTarget()
	if target == "" {
		t.Fatal("publisher captured no local target")
	}

	dialerDriver := NewOnionDriver(nil, &directDialer{target: target})
	joinerPayload := []byte(`{"side":"joiner","peer_id":"abc","nick":"Alice"}`)
	res, err := dialerDriver.AcceptInvite(ctx, AcceptRequest{
		Blob:    InviteBlob{Words: oob.Words},
		Payload: joinerPayload,
	})
	if err != nil {
		t.Fatalf("AcceptInvite: %v", err)
	}
	if !bytes.Equal(res.InviterPayload, inviterPayload) {
		t.Errorf("joiner got %q, want %q", res.InviterPayload, inviterPayload)
	}

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer waitCancel()
	wr, err := pi.Wait(waitCtx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !bytes.Equal(wr.JoinerPayload, joinerPayload) {
		t.Errorf("inviter got %q, want %q", wr.JoinerPayload, joinerPayload)
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for pub.delCount() < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := pub.delCount(); got != 1 {
		t.Errorf("DelOnion called %d times, want 1", got)
	}
}

func TestOnionDriver_WaitReturnsTimeout(t *testing.T) {
	t.Parallel()
	pub := &fakePublisher{}
	driver := NewOnionDriver(pub, nil)
	driver.SkipServiceIDCheckForTest()
	pi, err := driver.CreateInvite(context.Background(), CreateRequest{
		Payload: []byte("x"),
		Timeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	wr, err := pi.Wait(context.Background())
	if !errors.Is(err, ErrTimedOut) {
		t.Errorf("Wait err = %v, want ErrTimedOut", err)
	}
	if wr.JoinerPayload != nil {
		t.Errorf("expected nil JoinerPayload on timeout")
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for pub.delCount() < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if pub.delCount() != 1 {
		t.Errorf("DelOnion not called on timeout: %d", pub.delCount())
	}
}

func TestOnionDriver_CancelTearsDown(t *testing.T) {
	t.Parallel()
	pub := &fakePublisher{}
	driver := NewOnionDriver(pub, nil)
	driver.SkipServiceIDCheckForTest()
	pi, err := driver.CreateInvite(context.Background(), CreateRequest{
		Payload: []byte("x"),
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	pi.Cancel()
	_, err = pi.Wait(context.Background())
	if !errors.Is(err, ErrCancelled) {
		t.Errorf("Wait err = %v, want ErrCancelled", err)
	}
	pi.Cancel()
	if pub.delCount() != 1 {
		t.Errorf("DelOnion called %d times, want 1 (idempotent)", pub.delCount())
	}
}

func TestOnionDriver_AcceptInvite_RejectsBadMACFromInviter(t *testing.T) {
	t.Parallel()

	bogusKey := [32]byte{1, 2, 3}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = body
		out := []byte("evil response")
		w.Header().Set(PairMACHeader, InviterHandshakeMAC(bogusKey, out))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(out)
	}))
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	driver := NewOnionDriver(nil, &directDialer{target: host})

	words := []string{"acid", "acorn", "acre", "acts", "afar", "affix", "aged"}
	_, err := driver.AcceptInvite(context.Background(), AcceptRequest{
		Blob:    InviteBlob{Words: words},
		Payload: []byte("joiner pkt"),
	})
	if !errors.Is(err, ErrMACMismatch) {
		t.Errorf("AcceptInvite err = %v, want ErrMACMismatch", err)
	}
}

func TestOnionDriver_Server_RejectsBadMACFromJoiner(t *testing.T) {
	t.Parallel()
	pub := &fakePublisher{}
	driver := NewOnionDriver(pub, nil)
	driver.SkipServiceIDCheckForTest()
	pi, err := driver.CreateInvite(context.Background(), CreateRequest{
		Payload: []byte("inviter pkt"),
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pi.Cancel()

	url := fmt.Sprintf("http://%s%s", pub.localTarget(), PairHandoffPath)
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader("body"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(PairMACHeader, "deadbeef")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestOnionDriver_CreateInvite_RejectsEmptyPayload(t *testing.T) {
	t.Parallel()
	d := NewOnionDriver(&fakePublisher{}, nil)
	d.SkipServiceIDCheckForTest()
	_, err := d.CreateInvite(context.Background(), CreateRequest{Payload: nil})
	if err == nil {
		t.Error("expected err on empty payload")
	}
}

func TestOnionDriver_AcceptInvite_RejectsEmptyPayload(t *testing.T) {
	t.Parallel()
	d := NewOnionDriver(nil, &directDialer{target: "127.0.0.1:0"})
	_, err := d.AcceptInvite(context.Background(), AcceptRequest{
		Blob: InviteBlob{Words: []string{"acid", "acorn", "acre", "acts", "afar", "affix", "aged"}},
	})
	if err == nil {
		t.Error("expected err on empty joiner payload")
	}
}

func TestOnionDriver_AcceptInvite_RejectsBadWords(t *testing.T) {
	t.Parallel()
	d := NewOnionDriver(nil, &directDialer{target: "127.0.0.1:0"})
	_, err := d.AcceptInvite(context.Background(), AcceptRequest{
		Blob:    InviteBlob{Words: []string{"acid", "acorn", "definitelynotaword", "acts", "afar", "affix", "aged"}},
		Payload: []byte("x"),
	})
	if err == nil {
		t.Error("expected err on invalid word")
	}
}

func TestOnionDriver_Wait_MultipleConcurrentWaiters(t *testing.T) {
	t.Parallel()
	pub := &fakePublisher{}
	driver := NewOnionDriver(pub, nil)
	driver.SkipServiceIDCheckForTest()

	pi, err := driver.CreateInvite(context.Background(), CreateRequest{
		Payload: []byte("inviter"),
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	const N = 8
	type waitResult struct {
		wr  WaitResult
		err error
	}
	results := make(chan waitResult, N)
	for i := 0; i < N; i++ {
		go func() {
			wr, err := pi.Wait(context.Background())
			results <- waitResult{wr, err}
		}()
	}

	time.Sleep(50 * time.Millisecond)

	dialerDriver := NewOnionDriver(nil, &directDialer{target: pub.localTarget()})
	joinerPayload := []byte("joiner")
	if _, err := dialerDriver.AcceptInvite(context.Background(), AcceptRequest{
		Blob:    InviteBlob{Words: pi.OOB().Words},
		Payload: joinerPayload,
	}); err != nil {
		t.Fatalf("AcceptInvite: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for i := 0; i < N; i++ {
		select {
		case r := <-results:
			if r.err != nil {
				t.Errorf("waiter %d err = %v, want nil", i, r.err)
			}
			if !bytes.Equal(r.wr.JoinerPayload, joinerPayload) {
				t.Errorf("waiter %d got payload %q, want %q", i, r.wr.JoinerPayload, joinerPayload)
			}
		case <-time.After(time.Until(deadline)):
			t.Fatalf("only %d/%d waiters returned within deadline — broadcast broken?", i, N)
		}
	}
}

func TestOnionDriver_CancelClosesListener(t *testing.T) {
	t.Parallel()
	pub := &fakePublisher{}
	driver := NewOnionDriver(pub, nil)
	driver.SkipServiceIDCheckForTest()
	pi, err := driver.CreateInvite(context.Background(), CreateRequest{
		Payload: []byte("x"),
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	target := pub.localTarget()
	pi.Cancel()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		c, derr := net.DialTimeout("tcp", target, 50*time.Millisecond)
		if derr != nil {
			return
		}
		c.Close()
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("listener at %s still accepting after Cancel", target)
}

func TestOnionDriver_Server_FirstSuccessOnly(t *testing.T) {
	t.Parallel()
	pub := &fakePublisher{}
	driver := NewOnionDriver(pub, nil)
	driver.SkipServiceIDCheckForTest()
	inviterPayload := []byte("inviter-bundle")
	pi, err := driver.CreateInvite(context.Background(), CreateRequest{
		Payload: inviterPayload,
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	defer pi.Cancel()

	mat, err := OnionDerive(pi.OOB().Words)
	if err != nil {
		t.Fatalf("OnionDerive: %v", err)
	}

	body := []byte(`{"side":"joiner"}`)
	mac := JoinerHandshakeMAC(mat.HandoffHMAC, body)
	url := fmt.Sprintf("http://%s%s", pub.localTarget(), PairHandoffPath)

	post := func() (int, error) {
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return 0, err
		}
		req.Header.Set(PairMACHeader, mac)
		req.Header.Set("Content-Type", PairContentType)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode, nil
	}

	type result struct {
		status int
		err    error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			s, err := post()
			results <- result{s, err}
		}()
	}
	wg.Wait()
	close(results)

	gotOK, gotRejected, gotOther := 0, 0, 0
	for r := range results {
		if r.err != nil {

			gotRejected++
			continue
		}
		switch r.status {
		case http.StatusOK:
			gotOK++
		case http.StatusGone:
			gotRejected++
		default:
			gotOther++
			t.Errorf("unexpected status %d", r.status)
		}
	}
	if gotOK != 1 || gotRejected != 1 || gotOther != 0 {
		t.Errorf("counts: OK=%d rejected=%d other=%d, want 1/1/0", gotOK, gotRejected, gotOther)
	}
}
