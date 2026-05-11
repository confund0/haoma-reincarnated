package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/files"
	"haoma-frontend/internal/ipc"
)

func TestHandleListFiles_ReturnsTombstoneFilteredAndSorted(t *testing.T) {
	stub := startHaomadStub(t, []string{"our-onion-1", "our-onion-2"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)

	mgr, err := files.NewManager(d.store, d.dataDir)
	if err != nil {
		t.Fatalf("files.NewManager: %v", err)
	}
	d.files = mgr

	const chatID = chat.ChatID("aabbccddeeff00112233445566778899")
	const (
		msgReady   = "11111111111111111111111111111111"
		msgPending = "22222222222222222222222222222222"
		msgGone    = "33333333333333333333333333333333"
	)

	body := func(name string) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"original_name":%q}`, name))
	}
	mustAppend := func(msgID string, kind events.Kind, b json.RawMessage, senderSeq uint64) {
		t.Helper()
		if _, err := d.events.AppendOutbound(events.OutboundParams{
			ChatID: chatID, Kind: kind, MsgID: msgID, Body: b,
			SenderSeq: senderSeq,
		}); err != nil {
			t.Fatalf("AppendOutbound %s: %v", msgID, err)
		}
	}

	mustAppend(msgGone, events.KindFile, body("gone.bin"), 1)
	time.Sleep(1100 * time.Millisecond)
	mustAppend(msgPending, events.KindFile, body("pending.bin"), 2)
	time.Sleep(1100 * time.Millisecond)
	mustAppend(msgReady, events.KindFile, body("ready.bin"), 3)

	if _, err := d.events.ApplyDelete(msgGone, time.Now().Unix(), ""); err != nil {
		t.Fatalf("ApplyDelete: %v", err)
	}

	for _, m := range []files.Metadata{
		{MsgID: msgReady, ChatID: chatID, Direction: files.DirOut, OriginalName: "ready.bin", Mime: "application/octet-stream", Size: 100, State: files.StateReady},
		{MsgID: msgPending, ChatID: chatID, Direction: files.DirIn, OriginalName: "pending.bin", Size: 200, State: files.StateDownloading},
		{MsgID: msgGone, ChatID: chatID, Direction: files.DirOut, OriginalName: "gone.bin", Size: 300, State: files.StateReady},
	} {
		if err := mgr.PutMeta(m); err != nil {
			t.Fatalf("PutMeta %s: %v", m.MsgID, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	req, _ := ipc.NewFrame(ipc.FrameListFiles, "lf-1", ipc.ListFilesRequest{ChatID: string(chatID)})
	writeFrame(t, ctx, conn, req)

	resp := readUntil(t, ctx, conn, ipc.FrameFilesList)
	if resp.ID != "lf-1" {
		t.Errorf("correlation id = %q, want lf-1", resp.ID)
	}
	var p ipc.FilesListResponse
	if err := json.Unmarshal(resp.Payload, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.ChatID != string(chatID) {
		t.Errorf("response chat_id = %q, want %q", p.ChatID, chatID)
	}
	if len(p.Files) != 2 {
		t.Fatalf("got %d files, want 2 (gone.bin should be filtered as tombstone)", len(p.Files))
	}

	if p.Files[0].MsgID != msgReady {
		t.Errorf("Files[0].MsgID = %q, want %q (newest first)", p.Files[0].MsgID, msgReady)
	}
	if p.Files[1].MsgID != msgPending {
		t.Errorf("Files[1].MsgID = %q, want %q", p.Files[1].MsgID, msgPending)
	}
	if p.Files[0].OriginalName != "ready.bin" || p.Files[0].Direction != "out" || p.Files[0].State != "ready" {
		t.Errorf("ready entry shape mismatch: %+v", p.Files[0])
	}
	if p.Files[1].Direction != "in" || p.Files[1].State != "downloading" {
		t.Errorf("pending entry shape mismatch: %+v", p.Files[1])
	}
}

func TestHandleListFiles_EmptyChatID(t *testing.T) {
	stub := startHaomadStub(t, []string{"o1", "o2"}, http.StatusCreated)
	_, addr, certPath, token := newTestDaemon(t, stub)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)
	req, _ := ipc.NewFrame(ipc.FrameListFiles, "lf-empty", ipc.ListFilesRequest{ChatID: ""})
	writeFrame(t, ctx, conn, req)
	resp := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	_ = json.Unmarshal(resp.Payload, &ep)
	if ep.Code != "bad_request" {
		t.Errorf("error code = %q, want bad_request", ep.Code)
	}
}

func TestHandleListFiles_PopulatesDeletable(t *testing.T) {
	stub := startHaomadStub(t, []string{"o1", "o2"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	mgr, err := files.NewManager(d.store, d.dataDir)
	if err != nil {
		t.Fatalf("files.NewManager: %v", err)
	}
	d.files = mgr

	const chatID = chat.ChatID("aabbccddeeff00112233445566778899")
	const (
		msgOutFresh = "11111111111111111111111111111111"
		msgOutOld   = "22222222222222222222222222222222"
		msgInFresh  = "33333333333333333333333333333333"
	)

	body := func(name string) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"original_name":%q}`, name))
	}

	if _, err := d.events.AppendOutbound(events.OutboundParams{
		ChatID: chatID, Kind: events.KindFile, MsgID: msgOutFresh, Body: body("fresh.bin"), SenderSeq: 1,
	}); err != nil {
		t.Fatalf("AppendOutbound fresh: %v", err)
	}

	oldNow := time.Now().Add(-25 * time.Hour)
	d.events = events.New(d.store, d.eventBus, func() time.Time { return oldNow })
	if _, err := d.events.AppendOutbound(events.OutboundParams{
		ChatID: chatID, Kind: events.KindFile, MsgID: msgOutOld, Body: body("old.bin"), SenderSeq: 2,
	}); err != nil {
		t.Fatalf("AppendOutbound old: %v", err)
	}
	d.events = events.New(d.store, d.eventBus, time.Now)

	if _, err := d.events.AppendInbound(events.InboundParams{
		ChatID: chatID, Kind: events.KindFile, MsgID: msgInFresh, EnvelopeID: "env-in",
		SenderTs: time.Now().Unix(), SenderSeq: 1, Status: events.DecryptOK, Body: body("inbound.bin"),
	}); err != nil {
		t.Fatalf("AppendInbound: %v", err)
	}

	for _, m := range []files.Metadata{
		{MsgID: msgOutFresh, ChatID: chatID, Direction: files.DirOut, OriginalName: "fresh.bin", Size: 100, State: files.StateReady},
		{MsgID: msgOutOld, ChatID: chatID, Direction: files.DirOut, OriginalName: "old.bin", Size: 100, State: files.StateReady},
		{MsgID: msgInFresh, ChatID: chatID, Direction: files.DirIn, OriginalName: "inbound.bin", Size: 100, State: files.StateReady},
	} {
		if err := mgr.PutMeta(m); err != nil {
			t.Fatalf("PutMeta %s: %v", m.MsgID, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)
	req, _ := ipc.NewFrame(ipc.FrameListFiles, "lf-d", ipc.ListFilesRequest{ChatID: string(chatID)})
	writeFrame(t, ctx, conn, req)
	resp := readUntil(t, ctx, conn, ipc.FrameFilesList)
	var p ipc.FilesListResponse
	if err := json.Unmarshal(resp.Payload, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got := map[string]bool{}
	for _, fe := range p.Files {
		got[fe.MsgID] = fe.Deletable
	}
	if !got[msgOutFresh] {
		t.Errorf("OUT-fresh Deletable = false, want true")
	}
	if got[msgOutOld] {
		t.Errorf("OUT-old Deletable = true, want false (outside window)")
	}
	if got[msgInFresh] {
		t.Errorf("IN-fresh Deletable = true, want false (inbound — can't revoke)")
	}
}

func TestHandleListFiles_UnknownChatReturnsEmpty(t *testing.T) {
	stub := startHaomadStub(t, []string{"o1", "o2"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	mgr, err := files.NewManager(d.store, d.dataDir)
	if err != nil {
		t.Fatalf("files.NewManager: %v", err)
	}
	d.files = mgr

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)
	req, _ := ipc.NewFrame(ipc.FrameListFiles, "lf-unknown", ipc.ListFilesRequest{ChatID: "deadbeefdeadbeefdeadbeefdeadbeef"})
	writeFrame(t, ctx, conn, req)
	resp := readUntil(t, ctx, conn, ipc.FrameFilesList)
	var p ipc.FilesListResponse
	_ = json.Unmarshal(resp.Payload, &p)
	if len(p.Files) != 0 {
		t.Errorf("got %d files, want 0 for unknown chat", len(p.Files))
	}
}
