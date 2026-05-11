package main

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/files"
	"haoma-frontend/internal/ipc"
)

const testOpenChatID = chat.ChatID("aabbccddeeff00112233445566778899")
const testOpenMsgID = "11111111111111111111111111111111"

var pngHeader = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}

func seedReadyForOpen(t *testing.T, mgr *files.Manager, declaredMIME string, readyBytes []byte) {
	t.Helper()
	if _, err := mgr.SealAtRest(testOpenChatID, testOpenMsgID, readyBytes); err != nil {
		t.Fatalf("SealAtRest: %v", err)
	}
	if err := mgr.PutMeta(files.Metadata{
		MsgID:        testOpenMsgID,
		ChatID:       testOpenChatID,
		Direction:    files.DirIn,
		OriginalName: "thing.png",
		Mime:         declaredMIME,
		Size:         uint64(len(readyBytes)),
		State:        files.StateReady,
	}); err != nil {
		t.Fatalf("PutMeta: %v", err)
	}
}

func TestHandleOpenFile_HappyPath_MIMEMatches(t *testing.T) {
	stub := startHaomadStub(t, []string{"o1", "o2"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	mgr, err := files.NewManager(d.store, d.dataDir)
	if err != nil {
		t.Fatalf("files.NewManager: %v", err)
	}
	d.files = mgr

	seedReadyForOpen(t, mgr, "image/png", pngHeader)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)
	req, _ := ipc.NewFrame(ipc.FrameOpenFile, "of-1", ipc.OpenFileRequest{
		ChatID: string(testOpenChatID), MsgID: testOpenMsgID,
	})
	writeFrame(t, ctx, conn, req)

	resp := readUntil(t, ctx, conn, ipc.FrameFileOpenReady)
	if resp.ID != "of-1" {
		t.Errorf("correlation id = %q, want of-1", resp.ID)
	}
	var p ipc.OpenFileReadyResponse
	if err := json.Unmarshal(resp.Payload, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !p.MIMEMatches {
		t.Errorf("MIMEMatches = false, want true (declared=%q sniffed=%q)", "image/png", p.SniffedMIME)
	}
	wantPath := filepath.Join(d.dataDir, files.SubdirName, files.OpenSubdir, testOpenMsgID)
	if p.FullPath != wantPath {
		t.Errorf("FullPath = %q, want %q", p.FullPath, wantPath)
	}
}

func TestHandleOpenFile_MIMEMismatch(t *testing.T) {
	stub := startHaomadStub(t, []string{"o1", "o2"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	mgr, err := files.NewManager(d.store, d.dataDir)
	if err != nil {
		t.Fatalf("files.NewManager: %v", err)
	}
	d.files = mgr

	seedReadyForOpen(t, mgr, "text/plain", pngHeader)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)
	req, _ := ipc.NewFrame(ipc.FrameOpenFile, "of-mm", ipc.OpenFileRequest{
		ChatID: string(testOpenChatID), MsgID: testOpenMsgID,
	})
	writeFrame(t, ctx, conn, req)
	resp := readUntil(t, ctx, conn, ipc.FrameFileOpenReady)
	var p ipc.OpenFileReadyResponse
	if err := json.Unmarshal(resp.Payload, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.MIMEMatches {
		t.Errorf("MIMEMatches = true, want false (declared text/plain vs sniffed %q)", p.SniffedMIME)
	}
	if p.SniffedMIME == "" {
		t.Errorf("SniffedMIME empty, want a recognised MIME for PNG header")
	}
}

func TestHandleOpenFile_NotReady(t *testing.T) {
	stub := startHaomadStub(t, []string{"o1", "o2"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	mgr, err := files.NewManager(d.store, d.dataDir)
	if err != nil {
		t.Fatalf("files.NewManager: %v", err)
	}
	d.files = mgr
	if err := mgr.PutMeta(files.Metadata{
		MsgID:     testOpenMsgID,
		ChatID:    testOpenChatID,
		Direction: files.DirIn,
		Mime:      "image/png",
		State:     files.StateDownloading,
	}); err != nil {
		t.Fatalf("PutMeta: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)
	req, _ := ipc.NewFrame(ipc.FrameOpenFile, "of-nr", ipc.OpenFileRequest{
		ChatID: string(testOpenChatID), MsgID: testOpenMsgID,
	})
	writeFrame(t, ctx, conn, req)
	resp := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	_ = json.Unmarshal(resp.Payload, &ep)
	if ep.Code != "not_ready" {
		t.Errorf("error code = %q, want not_ready", ep.Code)
	}
}

func TestHandleOpenFile_UnknownMsgID(t *testing.T) {
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
	req, _ := ipc.NewFrame(ipc.FrameOpenFile, "of-u", ipc.OpenFileRequest{
		ChatID: string(testOpenChatID), MsgID: testOpenMsgID,
	})
	writeFrame(t, ctx, conn, req)
	resp := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	_ = json.Unmarshal(resp.Payload, &ep)
	if ep.Code != "unknown_msg" {
		t.Errorf("error code = %q, want unknown_msg", ep.Code)
	}
}

func TestHandleOpenFile_WrongChat(t *testing.T) {
	stub := startHaomadStub(t, []string{"o1", "o2"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	mgr, err := files.NewManager(d.store, d.dataDir)
	if err != nil {
		t.Fatalf("files.NewManager: %v", err)
	}
	d.files = mgr
	seedReadyForOpen(t, mgr, "image/png", pngHeader)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)
	req, _ := ipc.NewFrame(ipc.FrameOpenFile, "of-wc", ipc.OpenFileRequest{
		ChatID: "deadbeefdeadbeefdeadbeefdeadbeef", MsgID: testOpenMsgID,
	})
	writeFrame(t, ctx, conn, req)
	resp := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	_ = json.Unmarshal(resp.Payload, &ep)
	if ep.Code != "wrong_chat" {
		t.Errorf("error code = %q, want wrong_chat", ep.Code)
	}
}

func TestHandleOpenFile_BadRequest(t *testing.T) {
	stub := startHaomadStub(t, []string{"o1", "o2"}, http.StatusCreated)
	_, addr, certPath, token := newTestDaemon(t, stub)
	cases := []struct {
		name string
		req  ipc.OpenFileRequest
	}{
		{"empty chat", ipc.OpenFileRequest{MsgID: testOpenMsgID}},
		{"empty msg", ipc.OpenFileRequest{ChatID: string(testOpenChatID)}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn := dialTest(t, ctx, addr, certPath, token)
			req, _ := ipc.NewFrame(ipc.FrameOpenFile, "of-br", tc.req)
			writeFrame(t, ctx, conn, req)
			resp := readUntil(t, ctx, conn, ipc.FrameError)
			var ep ipc.ErrorPayload
			_ = json.Unmarshal(resp.Payload, &ep)
			if ep.Code != "bad_request" {
				t.Errorf("error code = %q, want bad_request", ep.Code)
			}
		})
	}
}
