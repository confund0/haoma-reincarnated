package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/files"
	"haoma-frontend/internal/ipc"
)

const (
	testSaveChatID = chat.ChatID("aabbccddeeff00112233445566778899")
	testSaveMsgID  = "11111111111111111111111111111111"
)

func seedReadyFile(t *testing.T, mgr *files.Manager, originalName string, body []byte) {
	t.Helper()
	if _, err := mgr.SealAtRest(testSaveChatID, testSaveMsgID, body); err != nil {
		t.Fatalf("SealAtRest: %v", err)
	}
	if err := mgr.PutMeta(files.Metadata{
		MsgID:        testSaveMsgID,
		ChatID:       testSaveChatID,
		Direction:    files.DirIn,
		OriginalName: originalName,
		Size:         uint64(len(body)),
		State:        files.StateReady,
	}); err != nil {
		t.Fatalf("PutMeta: %v", err)
	}
}

func TestHandleSaveFile_HappyPath(t *testing.T) {
	stub := startHaomadStub(t, []string{"o1", "o2"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)

	mgr, err := files.NewManager(d.store, d.dataDir)
	if err != nil {
		t.Fatalf("files.NewManager: %v", err)
	}
	d.files = mgr

	plaintext := []byte("hello, attachments")
	seedReadyFile(t, mgr, "photo.jpg", plaintext)

	dest := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)

	req, _ := ipc.NewFrame(ipc.FrameSaveFile, "sf-1", ipc.SaveFileRequest{
		ChatID:  string(testSaveChatID),
		MsgID:   testSaveMsgID,
		DestDir: dest,
	})
	writeFrame(t, ctx, conn, req)

	resp := readUntil(t, ctx, conn, ipc.FrameFileSaved)
	if resp.ID != "sf-1" {
		t.Errorf("correlation id = %q, want sf-1", resp.ID)
	}
	var p ipc.SaveFileResponse
	if err := json.Unmarshal(resp.Payload, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.HasPrefix(p.FullPath, dest) {
		t.Errorf("FullPath = %q, want prefix %q", p.FullPath, dest)
	}

	base := filepath.Base(p.FullPath)
	matched, _ := regexp.MatchString(`^haoma-[0-9a-f]{16}\.jpg$`, base)
	if !matched {
		t.Errorf("filename = %q, want haoma-{16-hex}.jpg", base)
	}

	got, err := os.ReadFile(p.FullPath)
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Errorf("plaintext = %q, want %q", got, plaintext)
	}

	info, err := os.Stat(p.FullPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("perms = %v, want 0600", info.Mode().Perm())
	}
}

func TestHandleSaveFile_NotReady(t *testing.T) {
	stub := startHaomadStub(t, []string{"o1", "o2"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	mgr, err := files.NewManager(d.store, d.dataDir)
	if err != nil {
		t.Fatalf("files.NewManager: %v", err)
	}
	d.files = mgr

	if err := mgr.PutMeta(files.Metadata{
		MsgID:        testSaveMsgID,
		ChatID:       testSaveChatID,
		Direction:    files.DirIn,
		OriginalName: "photo.jpg",
		Size:         100,
		State:        files.StateDownloading,
	}); err != nil {
		t.Fatalf("PutMeta: %v", err)
	}

	dest := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)
	req, _ := ipc.NewFrame(ipc.FrameSaveFile, "sf-nr", ipc.SaveFileRequest{
		ChatID: string(testSaveChatID), MsgID: testSaveMsgID, DestDir: dest,
	})
	writeFrame(t, ctx, conn, req)

	resp := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	_ = json.Unmarshal(resp.Payload, &ep)
	if ep.Code != "not_ready" {
		t.Errorf("error code = %q, want not_ready", ep.Code)
	}
}

func TestHandleSaveFile_UnknownMsgID(t *testing.T) {
	stub := startHaomadStub(t, []string{"o1", "o2"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	mgr, err := files.NewManager(d.store, d.dataDir)
	if err != nil {
		t.Fatalf("files.NewManager: %v", err)
	}
	d.files = mgr

	dest := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)
	req, _ := ipc.NewFrame(ipc.FrameSaveFile, "sf-u", ipc.SaveFileRequest{
		ChatID: string(testSaveChatID), MsgID: testSaveMsgID, DestDir: dest,
	})
	writeFrame(t, ctx, conn, req)

	resp := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	_ = json.Unmarshal(resp.Payload, &ep)
	if ep.Code != "unknown_msg" {
		t.Errorf("error code = %q, want unknown_msg", ep.Code)
	}
}

func TestHandleSaveFile_DestMissing(t *testing.T) {
	stub := startHaomadStub(t, []string{"o1", "o2"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	mgr, err := files.NewManager(d.store, d.dataDir)
	if err != nil {
		t.Fatalf("files.NewManager: %v", err)
	}
	d.files = mgr
	seedReadyFile(t, mgr, "photo.jpg", []byte("x"))

	dest := filepath.Join(t.TempDir(), "does-not-exist")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)
	req, _ := ipc.NewFrame(ipc.FrameSaveFile, "sf-dm", ipc.SaveFileRequest{
		ChatID: string(testSaveChatID), MsgID: testSaveMsgID, DestDir: dest,
	})
	writeFrame(t, ctx, conn, req)

	resp := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	_ = json.Unmarshal(resp.Payload, &ep)
	if ep.Code != "dest_missing" {
		t.Errorf("error code = %q, want dest_missing", ep.Code)
	}
}

func TestHandleSaveFile_DestNotDir(t *testing.T) {
	stub := startHaomadStub(t, []string{"o1", "o2"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	mgr, err := files.NewManager(d.store, d.dataDir)
	if err != nil {
		t.Fatalf("files.NewManager: %v", err)
	}
	d.files = mgr
	seedReadyFile(t, mgr, "photo.jpg", []byte("x"))

	dest := filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(dest, []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("seed regular file: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)
	req, _ := ipc.NewFrame(ipc.FrameSaveFile, "sf-nd", ipc.SaveFileRequest{
		ChatID: string(testSaveChatID), MsgID: testSaveMsgID, DestDir: dest,
	})
	writeFrame(t, ctx, conn, req)

	resp := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	_ = json.Unmarshal(resp.Payload, &ep)
	if ep.Code != "dest_not_dir" {
		t.Errorf("error code = %q, want dest_not_dir", ep.Code)
	}
}

func TestHandleSaveFile_WrongChat(t *testing.T) {
	stub := startHaomadStub(t, []string{"o1", "o2"}, http.StatusCreated)
	d, addr, certPath, token := newTestDaemon(t, stub)
	mgr, err := files.NewManager(d.store, d.dataDir)
	if err != nil {
		t.Fatalf("files.NewManager: %v", err)
	}
	d.files = mgr
	seedReadyFile(t, mgr, "photo.jpg", []byte("x"))

	dest := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn := dialTest(t, ctx, addr, certPath, token)
	req, _ := ipc.NewFrame(ipc.FrameSaveFile, "sf-wc", ipc.SaveFileRequest{
		ChatID: "deadbeefdeadbeefdeadbeefdeadbeef", MsgID: testSaveMsgID, DestDir: dest,
	})
	writeFrame(t, ctx, conn, req)

	resp := readUntil(t, ctx, conn, ipc.FrameError)
	var ep ipc.ErrorPayload
	_ = json.Unmarshal(resp.Payload, &ep)
	if ep.Code != "wrong_chat" {
		t.Errorf("error code = %q, want wrong_chat", ep.Code)
	}
}

func TestHandleSaveFile_BadRequest(t *testing.T) {
	stub := startHaomadStub(t, []string{"o1", "o2"}, http.StatusCreated)
	_, addr, certPath, token := newTestDaemon(t, stub)

	cases := []struct {
		name string
		req  ipc.SaveFileRequest
	}{
		{"empty chat", ipc.SaveFileRequest{MsgID: testSaveMsgID, DestDir: "/tmp"}},
		{"empty msg", ipc.SaveFileRequest{ChatID: string(testSaveChatID), DestDir: "/tmp"}},
		{"empty dest", ipc.SaveFileRequest{ChatID: string(testSaveChatID), MsgID: testSaveMsgID}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn := dialTest(t, ctx, addr, certPath, token)
			req, _ := ipc.NewFrame(ipc.FrameSaveFile, "sf-br", tc.req)
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

func TestGenerateSavedFilename_PreservesExtension(t *testing.T) {
	name, err := generateSavedFilename("photo.jpg")
	if err != nil {
		t.Fatalf("generateSavedFilename: %v", err)
	}
	if !strings.HasSuffix(name, ".jpg") {
		t.Errorf("filename = %q, want suffix .jpg", name)
	}
	if !strings.HasPrefix(name, "haoma-") {
		t.Errorf("filename = %q, want prefix haoma-", name)
	}
}

func TestGenerateSavedFilename_NoExtension(t *testing.T) {
	name, err := generateSavedFilename("README")
	if err != nil {
		t.Fatalf("generateSavedFilename: %v", err)
	}
	if strings.Contains(name, ".") {
		t.Errorf("filename = %q, expected no dot when OriginalName has no extension", name)
	}
	matched, _ := regexp.MatchString(`^haoma-[0-9a-f]{16}$`, name)
	if !matched {
		t.Errorf("filename = %q, want haoma-{16-hex}", name)
	}
}

func TestGenerateSavedFilename_SanitizesExt(t *testing.T) {
	name, err := generateSavedFilename("weird.exe$$")
	if err != nil {
		t.Fatalf("generateSavedFilename: %v", err)
	}
	if !strings.HasSuffix(name, ".exe") {
		t.Errorf("filename = %q, want clean .exe suffix", name)
	}
	if strings.Contains(name, "$") {
		t.Errorf("filename = %q contains stripped metachar", name)
	}
}

func TestGenerateSavedFilename_LengthCap(t *testing.T) {
	name, err := generateSavedFilename("file.thisisaridiculouslylongextensionsuffix")
	if err != nil {
		t.Fatalf("generateSavedFilename: %v", err)
	}
	parts := strings.SplitN(name, ".", 2)
	if len(parts) != 2 {
		t.Fatalf("filename = %q has no dot", name)
	}
	if len(parts[1]) > 16 {
		t.Errorf("ext length = %d, want ≤ 16 (got %q)", len(parts[1]), parts[1])
	}
}

func TestGenerateSavedFilename_DotOnlyExt(t *testing.T) {
	name, err := generateSavedFilename("trailing.")
	if err != nil {
		t.Fatalf("generateSavedFilename: %v", err)
	}
	if strings.HasSuffix(name, ".") {
		t.Errorf("filename = %q, expected no trailing dot", name)
	}
}
