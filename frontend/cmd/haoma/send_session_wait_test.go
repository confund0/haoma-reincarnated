package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/signal"
)

func setSendSessionTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := sendSessionTimeout
	sendSessionTimeout = d
	t.Cleanup(func() { sendSessionTimeout = prev })
}

func queueTail(d *daemon, peerID string) chan struct{} {
	d.sendQueue.mu.Lock()
	defer d.sendQueue.mu.Unlock()
	return d.sendQueue.tail[peerID]
}

func waitForQueueTail(t *testing.T, d *daemon, peerID string, prev chan struct{}) chan struct{} {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if tail := queueTail(d, peerID); tail != nil && tail != prev {
			return tail
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no send took a place in the chat's queue")
	return nil
}

func readReplies(t *testing.T, ctx context.Context, conn *websocket.Conn, ids ...string) map[string]ipc.Frame {
	t.Helper()
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	got := make(map[string]ipc.Frame, len(ids))
	readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for len(got) < len(ids) {
		_, raw, err := conn.Read(readCtx)
		if err != nil {
			t.Fatalf("read: %v (replies so far: %d of %d)", err, len(got), len(ids))
		}
		f, err := ipc.Decode(raw)
		if err != nil {
			continue
		}
		if want[f.ID] {
			got[f.ID] = f
		}
	}
	return got
}

func countSendsTo(sent []string, peerID string) int {
	n := 0
	for _, id := range sent {
		if id == peerID {
			n++
		}
	}
	return n
}

func textSentSeq(t *testing.T, f ipc.Frame) uint64 {
	t.Helper()
	if f.Type != ipc.FrameTextSent {
		var ep ipc.ErrorPayload
		_ = json.Unmarshal(f.Payload, &ep)
		t.Fatalf("reply %s is %s (code=%s message=%s), want %s", f.ID, f.Type, ep.Code, ep.Message, ipc.FrameTextSent)
	}
	var p ipc.SendTextResponse
	if err := json.Unmarshal(f.Payload, &p); err != nil {
		t.Fatal(err)
	}
	return p.SenderSeq
}

func TestSendText_WaitsForTheSessionThenSends(t *testing.T) {
	setSendSessionTimeout(t, 10*time.Second)
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000c0de"

	dc, _, err := d.createPairedChat(peerID, false)
	if err != nil {
		t.Fatal(err)
	}
	if !dc.WaitingForSession || dc.WaitingForContact || dc.ChatStartedAt != 0 {
		t.Fatalf("the joiner's new chat reads session=%t contact=%t started=%d, want waiting for a session",
			dc.WaitingForSession, dc.WaitingForContact, dc.ChatStartedAt)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendText, "w1", ipc.SendTextRequest{PeerID: peerID, Text: "typed too early"}))

	waitForQueueTail(t, d, peerID, nil)
	time.Sleep(100 * time.Millisecond)
	if stub.SendCalls != 0 {
		t.Fatalf("haomad saw %d /send calls before the session existed, want 0", stub.SendCalls)
	}

	firstMessageFrom(t, ctx, d, peerID, "env-first-from-inviter")

	f := readUntil(t, ctx, conn, ipc.FrameTextSent)
	if f.ID != "w1" {
		t.Fatalf("text_sent for %q, want w1", f.ID)
	}
	stub.WaitForSend(t, 2, 5*time.Second)
	if n := countSendsTo(stub.SentPeerIDs, peerID); n != 2 {
		t.Fatalf("haomad got %d sends to the contact, want 2 (the presence echo and the text)", n)
	}

	got, err := directChat(d, dc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.WaitingForSession || got.ChatStartedAt == 0 {
		t.Fatalf("after the first decrypt the chat reads waiting=%t started=%d, want started", got.WaitingForSession, got.ChatStartedAt)
	}
}

func TestSendText_WaitRunsOutWithTheReason(t *testing.T) {
	setSendSessionTimeout(t, 200*time.Millisecond)
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000c0de"
	if _, err := d.chats.CreateDirect(peerID); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	start := time.Now()
	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendText, "t1", ipc.SendTextRequest{PeerID: peerID, Text: "nobody there"}))
	f := readUntil(t, ctx, conn, ipc.FrameError)
	if waited := time.Since(start); waited < 200*time.Millisecond {
		t.Fatalf("the send failed after %v, before the wait could run out", waited)
	}
	var ep ipc.ErrorPayload
	if err := json.Unmarshal(f.Payload, &ep); err != nil {
		t.Fatal(err)
	}
	if ep.Code != "encrypt_failed" || ep.Message != noSessionYetDetail {
		t.Fatalf("error code=%q message=%q, want encrypt_failed and %q", ep.Code, ep.Message, noSessionYetDetail)
	}
	if stub.SendCalls != 0 {
		t.Fatalf("haomad saw %d /send calls, want 0", stub.SendCalls)
	}

	if tail := queueTail(d, peerID); tail != nil {
		t.Fatal("the failed send still holds a place in the chat's queue")
	}
}

func TestSendText_TwoWaitingTextsKeepTheirOrder(t *testing.T) {
	setSendSessionTimeout(t, 10*time.Second)
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	stub.SendGate = make(chan struct{})

	openGate := sync.OnceFunc(func() { close(stub.SendGate) })
	t.Cleanup(openGate)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000c0de"
	if _, err := d.chats.CreateDirect(peerID); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendText, "first", ipc.SendTextRequest{PeerID: peerID, Text: "one"}))
	firstPlace := waitForQueueTail(t, d, peerID, nil)
	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendText, "second", ipc.SendTextRequest{PeerID: peerID, Text: "two"}))
	waitForQueueTail(t, d, peerID, firstPlace)

	remote, err := signal.Bootstrap(3)
	if err != nil {
		t.Fatal(err)
	}
	processRemoteBundle(t, d, peerID, remote)
	d.sessionsChanged.fire()

	stub.WaitForSendEntered(t, 1, 5*time.Second)
	select {
	case <-stub.SendEntered:
		t.Fatal("the second text reached haomad while the first was still held there; it did not keep its place")
	case <-time.After(300 * time.Millisecond):
	}
	openGate()

	replies := readReplies(t, ctx, conn, "first", "second")
	s1, s2 := textSentSeq(t, replies["first"]), textSentSeq(t, replies["second"])
	if s1 >= s2 {
		t.Fatalf("sender seqs first=%d second=%d, want the first lower", s1, s2)
	}
}

func TestSendText_ASiblingDoesNotHoldTheText(t *testing.T) {
	setSendSessionTimeout(t, 5*time.Second)
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000c0de"
	preEstablishSession(t, d, peerID)
	if _, _, err := d.peerMeta.SetFingerprint(peerID, railFingerprint); err != nil {
		t.Fatal(err)
	}
	linkSibling(t, d, railSiblingID)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	start := time.Now()
	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendText, "s1", ipc.SendTextRequest{PeerID: peerID, Text: "goes at once"}))
	readUntil(t, ctx, conn, ipc.FrameTextSent)
	if waited := time.Since(start); waited > 2*time.Second {
		t.Fatalf("the text took %v; the sibling held it", waited)
	}
	stub.WaitForSend(t, 2, 5*time.Second)
	sent := stub.SentPeerIDs
	if countSendsTo(sent, peerID) != 1 || countSendsTo(sent, railSiblingID) != 1 {
		t.Fatalf("haomad got sends to %v, want one to the contact and one mirror copy to the sibling", sent)
	}
}

func TestSendQueue_IsFirstInFirstOut(t *testing.T) {
	var q sendQueue
	const key = "peer-1"
	p1, p2, p3 := q.take(key), q.take(key), q.take(key)
	if p1.prev != nil {
		t.Fatal("the first send has a send ahead of it")
	}

	p2.release()
	select {
	case <-p3.prev:
		t.Fatal("the third send may go before the first left")
	case <-time.After(50 * time.Millisecond):
	}
	p1.release()
	select {
	case <-p3.prev:
	case <-time.After(5 * time.Second):
		t.Fatal("the third send never got its turn")
	}
	p3.release()
	p3.release()
	q.mu.Lock()
	n := len(q.tail)
	q.mu.Unlock()
	if n != 0 {
		t.Fatalf("an idle queue holds %d key(s), want none", n)
	}
}

func TestMarkChatStarted_OnlyOnceAndOnlyForANewChat(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	dc, _, err := d.createPairedChat("0000000000000000000000000000c0de", true)
	if err != nil {
		t.Fatal(err)
	}
	if !dc.WaitingForContact || dc.WaitingForSession {
		t.Fatalf("the inviter's new chat reads session=%t contact=%t, want waiting for the contact", dc.WaitingForSession, dc.WaitingForContact)
	}
	if e := chatToEntry(dc); !e.WaitingForContact || e.WaitingForSession {
		t.Fatalf("the inviter's chat entry reads session=%t contact=%t, want waiting for the contact", e.WaitingForSession, e.WaitingForContact)
	}
	if changed, err := d.chats.MarkChatStarted(dc.ID, 1000); err != nil || !changed {
		t.Fatalf("first mark: changed=%t err=%v, want changed", changed, err)
	}
	if changed, err := d.chats.MarkChatStarted(dc.ID, 2000); err != nil || changed {
		t.Fatalf("second mark: changed=%t err=%v, want unchanged", changed, err)
	}
	got, err := directChat(d, dc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ChatStartedAt != 1000 {
		t.Fatalf("chat_started_at = %d, want the first mark's 1000", got.ChatStartedAt)
	}
	entry := chatToEntry(got)
	if entry.WaitingForSession || entry.WaitingForContact || entry.ChatStartedAt != 1000 {
		t.Fatalf("chat entry waiting=%t started=%d, want started at 1000", entry.WaitingForSession, entry.ChatStartedAt)
	}

	old, err := d.chats.CreateDirectEdgeless("00000000000000000000000000000e1d", railFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if old.WaitingToStart() {
		t.Fatal("a chat that came by backfill waits for a session; it should show no line")
	}
	if changed, err := d.chats.MarkChatStarted(old.ID, 3000); err != nil || changed {
		t.Fatalf("mark on a chat that never waited: changed=%t err=%v, want unchanged", changed, err)
	}
}

func assertNoLine(t *testing.T, d *daemon, peerID, path string) {
	t.Helper()
	got, err := d.chats.GetByDirectPeer(peerID)
	if err != nil {
		t.Fatalf("%s: no chat for the contact: %v", path, err)
	}
	if got.WaitingToStart() || got.ChatStartedAt != 0 {
		t.Fatalf("%s: a chat made again for an old contact reads session=%t contact=%t started=%d, want no line",
			path, got.WaitingForSession, got.WaitingForContact, got.ChatStartedAt)
	}
}

func TestRecreatedChat_ThroughASend_ShowsNoLine(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000c0de"
	preEstablishSession(t, d, peerID)
	if err := d.chats.Delete(chatIDForPeer(t, d, peerID)); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)
	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendText, "r1", ipc.SendTextRequest{PeerID: peerID, Text: "back again"}))
	readUntil(t, ctx, conn, ipc.FrameTextSent)
	assertNoLine(t, d, peerID, "send path")
}

func TestRecreatedChat_ThroughAnInboundMessage_ShowsNoLine(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, _, _, _ := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000c0de"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	firstMessageFrom(t, ctx, d, peerID, "env-old-contact")
	assertNoLine(t, d, peerID, "inbound path")
}

func TestSendEdit_ValidatesBeforeTheWait(t *testing.T) {
	setSendSessionTimeout(t, 5*time.Second)
	stub := startHaomadStub(t, []string{"our-onion"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	const peerID = "0000000000000000000000000000c0de"
	if _, _, err := d.createPairedChat(peerID, false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	start := time.Now()
	writeFrame(t, ctx, conn, mustFrame(ipc.FrameSendEdit, "e1", ipc.SendEditRequest{
		PeerID: peerID, TargetMsgID: "no-such-message", Text: "edited",
	}))
	f := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	_ = json.Unmarshal(f.Payload, &ep)
	if ep.Code != "unknown_target" {
		t.Fatalf("code = %q, want unknown_target", ep.Code)
	}
	if waited := time.Since(start); waited > 2*time.Second {
		t.Fatalf("the refusal took %v; the edit sat out the session wait first", waited)
	}
}
