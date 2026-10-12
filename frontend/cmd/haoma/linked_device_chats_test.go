package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/coder/websocket"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/files"
	"haoma-frontend/internal/ipc"
)

func TestEnsureChat_RefusesALinkedDevice(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	d, addr, certPath, token := linkDaemon(t, newLinkStub(t, "our-onion"))
	if _, err := d.linkedDevices.Link("peer-my-desktop", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	conn := dialTest(t, ctx, addr, certPath, token)

	req, _ := ipc.NewFrame(ipc.FrameEnsureChat, "ens-mine", ipc.EnsureChatRequest{PeerID: "peer-my-desktop"})
	writeFrame(t, ctx, conn, req)
	f := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	if err := json.Unmarshal(f.Payload, &ep); err != nil {
		t.Fatal(err)
	}
	if ep.Code != ipc.ReasonBadRequest {
		t.Fatalf("ensure_chat for a linked device: error %q (%s), want %q", ep.Code, ep.Message, ipc.ReasonBadRequest)
	}
	if chatExistsForPeer(d, "peer-my-desktop") {
		t.Fatal("ensure_chat created a chat with one of my own devices")
	}

	req, _ = ipc.NewFrame(ipc.FrameEnsureChat, "ens-contact", ipc.EnsureChatRequest{PeerID: "peer-contact"})
	writeFrame(t, ctx, conn, req)
	readUntil(t, ctx, conn, ipc.FrameChatEnsured)
	if !chatExistsForPeer(d, "peer-contact") {
		t.Fatal("ensure_chat for a contact created no chat")
	}
}

func TestDropLinkedDeviceChats(t *testing.T) {
	d, _, _, _ := linkDaemon(t, newLinkStub(t, "our-onion"))
	mine, _, err := d.createDirectWithDefaults("peer-my-desktop")
	if err != nil {
		t.Fatal(err)
	}
	appendRowAt(t, d, mine.ID, "msg-to-my-desktop", 100)
	contact, _, err := d.createDirectWithDefaults("peer-contact")
	if err != nil {
		t.Fatal(err)
	}
	appendRowAt(t, d, contact.ID, "msg-from-contact", 100)

	mixed, _, err := d.createDirectWithDefaults("peer-other-contact")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.chats.AttachPeerID(mixed.ID, "peer-my-laptop"); err != nil {
		t.Fatal(err)
	}

	old, _, err := d.createDirectWithDefaults("peer-my-old-phone")
	if err != nil {
		t.Fatal(err)
	}
	appendRowAt(t, d, old.ID, "msg-to-my-old-phone", 100)
	for _, id := range []string{"peer-my-desktop", "peer-my-laptop", "peer-my-old-phone"} {
		if _, err := d.linkedDevices.Link(id, time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.linkedDevices.Unlink("peer-my-old-phone", time.Now().Unix()+1); err != nil {
		t.Fatal(err)
	}

	dropLinkedDeviceChats(d)

	if chatExistsForPeer(d, "peer-my-old-phone") {
		t.Fatal("the chat with an unlinked device of mine is still there")
	}
	if rows, err := d.events.List(old.ID, 0, 0); err != nil || len(rows) != 0 {
		t.Fatalf("the unlinked device's chat events: %d rows, err %v; want none", len(rows), err)
	}

	if chatExistsForPeer(d, "peer-my-desktop") {
		t.Fatal("the chat with a linked device is still there")
	}
	if rows, err := d.events.List(mine.ID, 0, 0); err != nil || len(rows) != 0 {
		t.Fatalf("the dropped chat's events: %d rows, err %v; want none", len(rows), err)
	}
	if !chatExistsForPeer(d, "peer-contact") {
		t.Fatal("the chat with a contact was dropped")
	}
	if rows, err := d.events.List(contact.ID, 0, 0); err != nil || len(rows) != 1 {
		t.Fatalf("the contact's chat events: %d rows, err %v; want 1", len(rows), err)
	}
	if !chatExistsForPeer(d, "peer-other-contact") {
		t.Fatal("the chat with a contact's device and a linked device was dropped")
	}

	before, err := d.chats.List()
	if err != nil {
		t.Fatal(err)
	}
	dropLinkedDeviceChats(d)
	after, err := d.chats.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 2 || len(after) != len(before) {
		t.Fatalf("chats: %d after the first run, %d after the second; want 2 and 2", len(before), len(after))
	}
}

func TestSends_RefuseALinkedDevice(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	d, addr, certPath, token := linkDaemon(t, newLinkStub(t, "our-onion"))
	if d.files == nil {
		mgr, err := files.NewManager(d.store, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		d.files = mgr
	}
	if _, err := d.linkedDevices.Link("peer-my-desktop", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	conn := dialTest(t, ctx, addr, certPath, token)

	cases := []struct {
		name    string
		typ     ipc.FrameType
		payload any
	}{
		{"send_text", ipc.FrameSendText, ipc.SendTextRequest{PeerID: "peer-my-desktop", Text: "hi"}},
		{"send_file", ipc.FrameSendFile, ipc.SendFileRequest{PeerID: "peer-my-desktop", Path: writeTempFile(t, []byte("x"))}},
	}
	for _, c := range cases {
		req, _ := ipc.NewFrame(c.typ, "req-"+c.name, c.payload)
		writeFrame(t, ctx, conn, req)
		f := readUntil(t, ctx, conn, ipc.FrameError)
		var ep ipc.ErrorPayload
		if err := json.Unmarshal(f.Payload, &ep); err != nil {
			t.Fatal(err)
		}
		if f.ID != "req-"+c.name || ep.Code != ipc.ReasonBadRequest || ep.Message != "this is a linked device, not a contact" {
			t.Fatalf("%s to a linked device: error %q (%s) for %q, want %q with the linked-device refusal",
				c.name, ep.Code, ep.Message, f.ID, ipc.ReasonBadRequest)
		}
		if chatExistsForPeer(d, "peer-my-desktop") {
			t.Fatalf("%s created a chat with one of my own devices", c.name)
		}
	}
}

func TestPeerRows_NotPushedWhileAnAcceptWaits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	d, addr, certPath, token := linkDaemon(t, newLinkStub(t, "our-onion"))
	conn := dialTest(t, ctx, addr, certPath, token)

	_, release := registerDeviceLinkWaiter(d, "peer-joining")
	pushPeerRowUpdated(ctx, d, "peer-joining")
	peerUnreachableHandler(ctx, d)(backendapi.UnreachableObservation{PeerID: "peer-joining"})

	pushPeerRowUpdated(ctx, d, "peer-contact")
	if got := nextPeerRow(t, ctx, conn); got != "peer-contact" {
		t.Fatalf("a row for %s went out while an accept waited on it", got)
	}

	release()
	pushPeerRowUpdated(ctx, d, "peer-joining")
	if got := nextPeerRow(t, ctx, conn); got != "peer-joining" {
		t.Fatalf("after the wait ended the next row was for %s, want peer-joining", got)
	}
}

func nextPeerRow(t *testing.T, ctx context.Context, conn *websocket.Conn) string {
	t.Helper()
	f := readUntil(t, ctx, conn, ipc.FramePeerUpdated)
	var p ipc.PeerUpdatedPayload
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		t.Fatalf("decode peer.updated: %v", err)
	}
	return p.Peer.ID
}
