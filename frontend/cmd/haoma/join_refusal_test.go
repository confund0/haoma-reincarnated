package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/ipc"
)

var joinRefusalCases = []struct {
	name   string
	status int
	pair   string
	link   string
}{
	{"a word is wrong", http.StatusUnprocessableEntity, ipc.ReasonJoinWordsInvalid, ipc.ReasonJoinWordsInvalid},
	{"the invitation was already answered", http.StatusGone, ipc.ReasonJoinInvitationSpent, ipc.ReasonJoinInvitationSpent},
	{"the dial did not get through", http.StatusBadGateway, ipc.ReasonOnionAcceptFailed, ipc.ReasonBackendDown},
}

func refusalCode(t *testing.T, f ipc.Frame) string {
	t.Helper()
	var ep ipc.ErrorPayload
	if err := json.Unmarshal(f.Payload, &ep); err != nil {
		t.Fatal(err)
	}
	return ep.Code
}

func TestPairOnionAccept_RefusalSaysWhyAndReleasesTheOnion(t *testing.T) {
	for _, c := range joinRefusalCases {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			stub := newLinkStub(t, "pair-join-onion")
			stub.acceptStatus = c.status
			d, addr, certPath, token := newTestDaemon(t, startHaomadStub(t, []string{"unused-onion"}, http.StatusCreated))
			d.backendClient = backendapi.New(stub.URL, "", nil)

			conn := dialTest(t, ctx, addr, certPath, token)
			req, _ := ipc.NewFrame(ipc.FramePairOnionAccept, "req-1", ipc.PairOnionAcceptRequest{
				Words: []string{"acid", "acorn", "acre", "acts", "afar", "affix", "aged"},
			})
			writeFrame(t, ctx, conn, req)

			if got := refusalCode(t, readUntil(t, ctx, conn, ipc.FrameError)); got != c.pair {
				t.Fatalf("refusal code = %q, want %q", got, c.pair)
			}
			if got := stub.deletedOnions(); len(got) != 1 || got[0] != "pair-join-onion" {
				t.Fatalf("released %v, want the one address this join minted", got)
			}
		})
	}
}

func TestDeviceLinkAccept_RefusalSaysWhyAndReleasesTheOnion(t *testing.T) {
	for _, c := range joinRefusalCases {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			stub := newLinkStub(t, "link-join-onion")
			stub.acceptStatus = c.status
			_, addr, certPath, token := linkDaemon(t, stub)

			conn := dialTest(t, ctx, addr, certPath, token)
			req, _ := ipc.NewFrame(ipc.FrameDeviceLinkAccept, "req-1", ipc.DeviceLinkAcceptRequest{
				Words: []string{"acid", "acorn", "acre", "acts", "afar", "affix", "aged"},
			})
			writeFrame(t, ctx, conn, req)

			if got := refusalCode(t, readUntil(t, ctx, conn, ipc.FrameError)); got != c.link {
				t.Fatalf("refusal code = %q, want %q", got, c.link)
			}
			if got := stub.deletedOnions(); len(got) != 1 || got[0] != "link-join-onion" {
				t.Fatalf("released %v, want the one address this join minted", got)
			}
		})
	}
}
