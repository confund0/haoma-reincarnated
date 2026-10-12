package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"haoma/internal/peers"
	"haoma/internal/xport"
)

type deadlineRecorder struct {
	deadline time.Time
	held     bool
}

func (r *deadlineRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.deadline, r.held = req.Context().Deadline()
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("{}")),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func TestSendDestroyVerify_WaitsTheJoinsTwoMinutes(t *testing.T) {
	if destroyVerifyDialTimeout != 120*time.Second {
		t.Fatalf("the check waits %v; the join waits 120 s and the check must wait as long", destroyVerifyDialTimeout)
	}

	rec := &deadlineRecorder{}
	hc := &http.Client{Timeout: 60 * time.Second, Transport: rec}
	before := time.Now()
	if _, err := sendDestroyVerify(context.Background(), hc, "http://sibling.onion", xport.Envelope{ID: "verify-1"}); err != nil {
		t.Fatalf("sendDestroyVerify: %v", err)
	}
	if !rec.held {
		t.Fatal("the request reached the transport with no deadline at all; nothing bounds the check")
	}
	left := rec.deadline.Sub(before)
	if left < destroyVerifyDialTimeout-5*time.Second || left > destroyVerifyDialTimeout+5*time.Second {
		t.Fatalf("the request reached the transport with %v left, want about %v", left.Round(time.Second), destroyVerifyDialTimeout)
	}
	if hc.Timeout != 60*time.Second {
		t.Fatalf("the client handed in was changed: Timeout %v, want 60s", hc.Timeout)
	}
}

func TestSendDestroyVerify_ACallersShorterDeadlineStands(t *testing.T) {
	rec := &deadlineRecorder{}
	hc := &http.Client{Timeout: 60 * time.Second, Transport: rec}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	want, _ := ctx.Deadline()
	if _, err := sendDestroyVerify(ctx, hc, "http://sibling.onion", xport.Envelope{ID: "verify-1"}); err != nil {
		t.Fatalf("sendDestroyVerify: %v", err)
	}
	if !rec.held || !rec.deadline.Equal(want) {
		t.Fatalf("deadline at the transport %v, want the caller's %v", rec.deadline, want)
	}
}

func TestDestroyVerifyAnswer_SilenceABadReplyAndAnAnswerAreThreeThings(t *testing.T) {
	peer := &peers.Peer{ID: "sibling", InboundSecret: []byte("inbound-secret-0123456789abcdef")}
	ack := func(verifyID string, secret []byte) []byte {
		t.Helper()
		body, err := json.Marshal(destroyVerifyAckBody{VerifyID: verifyID, Verified: true})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(xport.Sign(xport.Envelope{ID: "ack-1", Kind: xport.KindDestroyVerifyAck, Payload: body}, secret))
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	for _, tc := range []struct {
		name string
		body []byte
		want destroyVerifySendResponse
	}{
		{"nothing", nil, destroyVerifySendResponse{}},
		{"whitespace", []byte(" \n"), destroyVerifySendResponse{}},
		{"not json", []byte("<html>"), destroyVerifySendResponse{Reason: destroyVerifyAnswerBad}},
		{"another key", ack("verify-1", []byte("somebody-else-entirely-000000000")), destroyVerifySendResponse{Reason: destroyVerifyAnswerBad}},
		{"another check", ack("verify-0", peer.InboundSecret), destroyVerifySendResponse{Reason: destroyVerifyAnswerBad}},
		{"the answer", ack("verify-1", peer.InboundSecret), destroyVerifySendResponse{Answered: true, Verified: true}},
	} {
		if got := destroyVerifyAnswer(peer, tc.body, "verify-1"); got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}
