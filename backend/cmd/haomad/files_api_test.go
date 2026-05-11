package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"haoma/internal/files"
)

func removeBlobByMsg(d *daemon, msgID string) error {
	if err := os.Remove(filepath.Join(d.files.RootDir(), msgID)); err != nil {
		return err
	}
	return nil
}

func newFilesTestDaemon(t *testing.T) *daemon {
	t.Helper()
	d := newTestDaemon(t)
	mgr, err := files.NewManager(d.store, t.TempDir())
	if err != nil {
		t.Fatalf("files.NewManager: %v", err)
	}
	d.files = mgr
	return d
}

func postJSON(t *testing.T, srv *httptest.Server, path string, body any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(srv.URL+path, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

const (
	filesTestMsgID = "0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a"
	filesPeerA     = "peerA"
	filesPeerB     = "peerB"
)

func TestAPI_StageFile_Success(t *testing.T) {
	d := newFilesTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	body := stageFileRequest{
		MsgID:            filesTestMsgID,
		Ciphertext:       []byte("opaque-ciphertext"),
		RecipientPeerIDs: []string{filesPeerA, filesPeerB},
	}
	resp := postJSON(t, srv, "/files", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 201; body=%s", resp.StatusCode, raw)
	}
	var out stageFileResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.MsgID != filesTestMsgID {
		t.Errorf("msg_id = %q, want %q", out.MsgID, filesTestMsgID)
	}
	if len(out.Tokens) != 2 {
		t.Fatalf("tokens len = %d, want 2", len(out.Tokens))
	}
	row, err := d.files.LookupToken(out.Tokens[0])
	if err != nil {
		t.Fatalf("LookupToken: %v", err)
	}
	if row.RecipientPeerID != filesPeerA {
		t.Errorf("token[0].recipient = %q, want %q", row.RecipientPeerID, filesPeerA)
	}
}

func TestAPI_StageFile_MissingFields(t *testing.T) {
	d := newFilesTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	cases := []struct {
		name string
		body stageFileRequest
	}{
		{"missing msg_id", stageFileRequest{Ciphertext: []byte("x"), RecipientPeerIDs: []string{filesPeerA}}},
		{"missing ciphertext", stageFileRequest{MsgID: filesTestMsgID, RecipientPeerIDs: []string{filesPeerA}}},
		{"missing recipients", stageFileRequest{MsgID: filesTestMsgID, Ciphertext: []byte("x")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := postJSON(t, srv, "/files", tc.body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", resp.StatusCode)
			}
		})
	}
}

func TestAPI_StageFile_RejectsRestage(t *testing.T) {
	d := newFilesTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	body := stageFileRequest{
		MsgID:            filesTestMsgID,
		Ciphertext:       []byte("first"),
		RecipientPeerIDs: []string{filesPeerA},
	}
	resp := postJSON(t, srv, "/files", body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first stage status = %d", resp.StatusCode)
	}
	resp2 := postJSON(t, srv, "/files", body)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("re-stage status = %d, want 409", resp2.StatusCode)
	}
}

func TestAPI_StageFile_UnsupportedMediaType(t *testing.T) {
	d := newFilesTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/files", "text/plain", strings.NewReader("nope"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want 415", resp.StatusCode)
	}
}

func TestAPI_DropFile_Success(t *testing.T) {
	d := newFilesTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	tokens, err := d.files.StageBlob(filesTestMsgID, []byte("x"), []string{filesPeerA, filesPeerB}, 0)
	if err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/files/"+filesTestMsgID, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	for _, tok := range tokens {
		if _, err := d.files.LookupToken(tok); err == nil {
			t.Errorf("token %s survived drop", tok)
		}
	}
}

func TestAPI_DropFile_IdempotentOnUnknown(t *testing.T) {
	d := newFilesTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/files/never-existed", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
}

func newPeerOnionMux(d *daemon) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /files/{token}", d.handleFileFetch)
	return mux
}

func TestPeerOnion_FetchFile_OK(t *testing.T) {
	d := newFilesTestDaemon(t)
	srv := httptest.NewServer(newPeerOnionMux(d))
	defer srv.Close()

	ct := []byte("the-bytes-of-a-tiny-file")
	tokens, err := d.files.StageBlob(filesTestMsgID, ct, []string{filesPeerA}, 0)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/files/" + tokens[0])
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, ct) {
		t.Fatalf("body diverged: got %q, want %q", got, ct)
	}
	if ctt := resp.Header.Get("Content-Type"); ctt != "application/octet-stream" {
		t.Errorf("Content-Type = %q", ctt)
	}
}

func TestPeerOnion_FetchFile_RangeSupported(t *testing.T) {
	d := newFilesTestDaemon(t)
	srv := httptest.NewServer(newPeerOnionMux(d))
	defer srv.Close()

	ct := bytes.Repeat([]byte("ABCDEFGH"), 512)
	tokens, err := d.files.StageBlob(filesTestMsgID, ct, []string{filesPeerA}, 0)
	if err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/files/"+tokens[0], nil)
	req.Header.Set("Range", "bytes=100-199")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, ct[100:200]) {
		t.Errorf("Range body diverged: got %q ...", got[:32])
	}
}

func TestPeerOnion_FetchFile_NotFound(t *testing.T) {
	d := newFilesTestDaemon(t)
	srv := httptest.NewServer(newPeerOnionMux(d))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/files/never-minted")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestPeerOnion_FetchFile_Gone(t *testing.T) {
	d := newFilesTestDaemon(t)
	srv := httptest.NewServer(newPeerOnionMux(d))
	defer srv.Close()

	tokens, err := d.files.StageBlob(filesTestMsgID, []byte("x"), []string{filesPeerA}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.files.DecrementReceipts(tokens[0]); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(srv.URL + "/files/" + tokens[0])
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Errorf("status = %d, want 410", resp.StatusCode)
	}
}

func TestAPI_ReceiveFileReceipt_DecrementsAndReturnsCount(t *testing.T) {
	d := newFilesTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	tokens, err := d.files.StageBlob(filesTestMsgID, []byte("opaque"), []string{filesPeerA}, 0)
	if err != nil {
		t.Fatal(err)
	}
	resp := postJSON(t, srv, "/files/receipts", receiveReceiptRequest{
		Token: tokens[0], RecipientPeerID: filesPeerA,
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, raw)
	}
	var out receiveReceiptResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.ReceiptsRemaining != 0 {
		t.Errorf("receipts_remaining = %d, want 0", out.ReceiptsRemaining)
	}

	if _, err := d.files.LookupToken(tokens[0]); err == nil {
		t.Errorf("token still resolvable after redeem")
	}
}

func TestAPI_ReceiveFileReceipt_RecipientMismatchIs403(t *testing.T) {
	d := newFilesTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	tokens, err := d.files.StageBlob(filesTestMsgID, []byte("opaque"), []string{filesPeerA}, 0)
	if err != nil {
		t.Fatal(err)
	}
	resp := postJSON(t, srv, "/files/receipts", receiveReceiptRequest{
		Token: tokens[0], RecipientPeerID: "an-imposter",
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}

	if _, err := d.files.LookupToken(tokens[0]); err != nil {
		t.Errorf("token invalidated by mismatch attempt: %v", err)
	}
}

func TestAPI_ReceiveFileReceipt_UnknownTokenIs404(t *testing.T) {
	d := newFilesTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()
	resp := postJSON(t, srv, "/files/receipts", receiveReceiptRequest{
		Token: "no-such-token", RecipientPeerID: filesPeerA,
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestAPI_ReceiveFileReceipt_AlreadyRedeemedIs200_Idempotent(t *testing.T) {
	d := newFilesTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	tokens, err := d.files.StageBlob(filesTestMsgID, []byte("opaque"), []string{filesPeerA}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.files.DecrementReceipts(tokens[0]); err != nil {
		t.Fatal(err)
	}

	resp := postJSON(t, srv, "/files/receipts", receiveReceiptRequest{
		Token: tokens[0], RecipientPeerID: filesPeerA,
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("idempotent re-call status = %d, want 200", resp.StatusCode)
	}
}

func TestAPI_ReceiveFileReceipt_BadRequest(t *testing.T) {
	d := newFilesTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()
	cases := []receiveReceiptRequest{
		{Token: "", RecipientPeerID: filesPeerA},
		{Token: "tok", RecipientPeerID: ""},
	}
	for _, c := range cases {
		resp := postJSON(t, srv, "/files/receipts", c)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("case %+v: status = %d, want 400", c, resp.StatusCode)
		}
	}
}

func TestPeerOnion_FetchFile_BlobMissing(t *testing.T) {

	d := newFilesTestDaemon(t)
	srv := httptest.NewServer(newPeerOnionMux(d))
	defer srv.Close()

	tokens, err := d.files.StageBlob(filesTestMsgID, []byte("x"), []string{filesPeerA}, 0)
	if err != nil {
		t.Fatal(err)
	}

	if err := removeBlobByMsg(d, filesTestMsgID); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(srv.URL + "/files/" + tokens[0])
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}
