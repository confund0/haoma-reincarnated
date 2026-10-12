package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"haoma-frontend/internal/ipc"
)

func TestSetNick_RepliesAndBroadcastsOnceToEverySession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	if err := d.loadSelfNickInto(); err != nil {
		t.Fatal(err)
	}

	asker := dialTest(t, ctx, addr, certPath, token)
	bystander := dialTest(t, ctx, addr, certPath, token)

	req, err := ipc.NewFrame(ipc.FrameSetNick, "c-nick-1", ipc.SetNickRequest{Nick: "  mara  "})
	if err != nil {
		t.Fatal(err)
	}
	writeFrame(t, ctx, asker, req)

	reply := readNext(t, ctx, asker)
	if reply.Type != ipc.FrameNick || reply.ID != "c-nick-1" {
		t.Fatalf("the client that asked got %q with id %q first, want %q with id %q",
			reply.Type, reply.ID, ipc.FrameNick, "c-nick-1")
	}
	var rp ipc.NickPayload
	if err := json.Unmarshal(reply.Payload, &rp); err != nil {
		t.Fatalf("decode the reply: %v", err)
	}
	if rp.Nick != "mara" || rp.IsDefault {
		t.Errorf("the reply says %+v, want the cleaned nick %q and not the placeholder", rp, "mara")
	}

	askerFrame := readNext(t, ctx, asker)
	if askerFrame.Type != ipc.FrameNick {
		t.Fatalf("the client that asked got %q second, want %q", askerFrame.Type, ipc.FrameNick)
	}
	assertNickPush(t, askerFrame, "mara", false, "the client that asked")

	bystanderFrame := readUntil(t, ctx, bystander, ipc.FrameNick)
	assertNickPush(t, bystanderFrame, "mara", false, "a client that did not ask")

	list, err := ipc.NewFrame(ipc.FrameListPeers, "c-nick-2", struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	writeFrame(t, ctx, asker, list)
	next := readNext(t, ctx, asker)
	if next.Type != ipc.FramePeersListed {
		t.Fatalf("the asker's next frame is %q, want %q — the set produced more than one push", next.Type, ipc.FramePeersListed)
	}

	if got := d.selfNick(); got != "mara" {
		t.Errorf("the daemon's cached nick is %q, want %q", got, "mara")
	}
}

func TestSetNick_BroadcastReportsThePlaceholder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	if _, err := d.setSelfNick("mara"); err != nil {
		t.Fatal(err)
	}
	client := dialTest(t, ctx, addr, certPath, token)

	req, err := ipc.NewFrame(ipc.FrameSetNick, "c-nick-3", ipc.SetNickRequest{Nick: defaultSelfNick})
	if err != nil {
		t.Fatal(err)
	}
	writeFrame(t, ctx, client, req)

	if reply := readUntil(t, ctx, client, ipc.FrameNick); reply.ID != "c-nick-3" {
		t.Fatalf("the first nick frame has id %q, want the reply on %q", reply.ID, "c-nick-3")
	}
	assertNickPush(t, readUntil(t, ctx, client, ipc.FrameNick), defaultSelfNick, true, "the client that asked")
}

func assertNickPush(t *testing.T, f ipc.Frame, wantNick string, wantIsDefault bool, who string) {
	t.Helper()
	if f.ID != "" {
		t.Errorf("%s got a nick push with correlation id %q; a broadcast carries none, and a client keying on the id would drop it", who, f.ID)
	}
	var p ipc.NickPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		t.Fatalf("%s: decode the nick push: %v", who, err)
	}
	if p.Nick != wantNick {
		t.Errorf("%s got nick %q, want %q", who, p.Nick, wantNick)
	}
	if p.IsDefault != wantIsDefault {
		t.Errorf("%s got is-default %t, want %t", who, p.IsDefault, wantIsDefault)
	}
}

func TestSetVerbs_AnswerTheClientThatAskedOnItsID(t *testing.T) {
	cases := []struct {
		name    string
		req     ipc.FrameType
		payload any
		want    ipc.FrameType
	}{
		{"set_nick", ipc.FrameSetNick, ipc.SetNickRequest{Nick: "mara"}, ipc.FrameNick},
		{"set_chat_font_scale", ipc.FrameSetChatFontScale, ipc.SetChatFontScaleRequest{Scale: 1.2}, ipc.FrameChatFontScale},
		{"set_chat_defaults", ipc.FrameSetChatDefaults, ipc.SetChatDefaultsRequest{RetentionSec: 3600, SendReceipts: true}, ipc.FrameChatDefaultsChanged},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
			_, addr, certPath, token := newTestDaemon(t, stub)
			client := dialTest(t, ctx, addr, certPath, token)

			id := "c-" + c.name
			req, err := ipc.NewFrame(c.req, id, c.payload)
			if err != nil {
				t.Fatal(err)
			}
			writeFrame(t, ctx, client, req)

			got := readNext(t, ctx, client)
			if got.Type != c.want || got.ID != id {
				t.Fatalf("first frame is %q with id %q, want %q with id %q", got.Type, got.ID, c.want, id)
			}
		})
	}
}
