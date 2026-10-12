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

func TestPeerOnion_FetchFile_MultiUse(t *testing.T) {
	d := newFilesTestDaemon(t)
	onion := httptest.NewServer(newPeerOnionMux(d))
	defer onion.Close()
	api := httptest.NewServer(d.apiHandler())
	defer api.Close()

	ct := []byte("the-bytes-of-a-tiny-file")
	tokens, err := d.files.StageBlob(filesTestMsgID, ct, []string{filesPeerA}, 0)
	if err != nil {
		t.Fatal(err)
	}

	fetch := func(tag string) {
		resp, err := http.Get(onion.URL + "/files/" + tokens[0])
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", tag, resp.StatusCode)
		}
		got, _ := io.ReadAll(resp.Body)
		if !bytes.Equal(got, ct) {
			t.Fatalf("%s: body diverged", tag)
		}
	}

	fetch("first pull")
	fetch("retry pull")

	rec := postJSON(t, api, "/files/receipts", receiveReceiptRequest{
		Token: tokens[0], RecipientPeerID: filesPeerA,
	})
	rec.Body.Close()
	if rec.StatusCode != http.StatusOK {
		t.Fatalf("receipt ack status = %d, want 200", rec.StatusCode)
	}
	fetch("post-receipt failover pull")
}

func TestAPI_ReceiveFileReceipt_VerifiesAndTokenStaysValid(t *testing.T) {
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
	if out.Token != tokens[0] {
		t.Errorf("token = %q, want %q", out.Token, tokens[0])
	}

	if _, err := d.files.LookupToken(tokens[0]); err != nil {
		t.Errorf("token invalidated by receipt ack: %v", err)
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

func TestAPI_ReceiveFileReceipt_DuplicateIs200_Idempotent(t *testing.T) {
	d := newFilesTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	tokens, err := d.files.StageBlob(filesTestMsgID, []byte("opaque"), []string{filesPeerA}, 0)
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		resp := postJSON(t, srv, "/files/receipts", receiveReceiptRequest{
			Token: tokens[0], RecipientPeerID: filesPeerA,
		})
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("ack %d status = %d, want 200", i, resp.StatusCode)
		}
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

func putStagingFetch(t *testing.T, d *daemon, token string, state files.FetchState) {
	t.Helper()
	row := files.Fetch{
		Token:          token,
		MsgID:          filesTestMsgID,
		PeerID:         filesPeerA,
		UrlPath:        "/files/" + token,
		ExpectedSize:   5,
		ExpectedSha256: strings.Repeat("a", 64),
		State:          files.FetchStatePending,
	}
	if err := d.files.PutFetch(row); err != nil {
		t.Fatal(err)
	}
	row.State = state
	if err := d.files.UpdateFetch(row); err != nil {
		t.Fatal(err)
	}
}

func writeStagingFile(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func deleteStaging(t *testing.T, srv *httptest.Server, msgID, token string) int {
	t.Helper()
	code, _ := deleteStagingCT(t, srv, msgID, token)
	return code
}

func deleteStagingCT(t *testing.T, srv *httptest.Server, msgID, token string) (int, string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"token": token})
	req, err := http.NewRequest(http.MethodDelete, srv.URL+"/files/staging/"+msgID, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("Content-Type")
}

func TestAPI_StagingGet_404WhileDownloading(t *testing.T) {
	d := newFilesTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()

	putStagingFetch(t, d, "tok-get", files.FetchStateDownloading)
	writeStagingFile(t, d.files.StagingPartPath(filesTestMsgID), "par")

	for _, id := range []string{filesTestMsgID, filesTestMsgID + files.StagingPartSuffix} {
		resp, err := http.Get(srv.URL + "/files/staging/" + id)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s while downloading: status = %d, want 404", id, resp.StatusCode)
		}
	}

	if err := os.Rename(d.files.StagingPartPath(filesTestMsgID), d.files.StagingPath(filesTestMsgID)); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(srv.URL + "/files/staging/" + filesTestMsgID)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(got) != "par" {
		t.Fatalf("GET after the rename: status %d body %q, want 200 %q", resp.StatusCode, got, "par")
	}
}

func TestAPI_StagingDelete_409WhileInFlight(t *testing.T) {
	for _, state := range []files.FetchState{files.FetchStatePending, files.FetchStateDownloading} {
		t.Run(string(state), func(t *testing.T) {
			d := newFilesTestDaemon(t)
			srv := httptest.NewServer(d.apiHandler())
			defer srv.Close()

			putStagingFetch(t, d, "tok-del", state)
			writeStagingFile(t, d.files.StagingPartPath(filesTestMsgID), "par")

			for _, tok := range []string{"tok-del", ""} {
				got, ct := deleteStagingCT(t, srv, filesTestMsgID, tok)
				if got != http.StatusConflict {
					t.Fatalf("DELETE (token %q) status = %d, want 409", tok, got)
				}
				if ct != "application/json" {
					t.Fatalf("DELETE (token %q) Content-Type = %q, want the JSON error every haomad error uses", tok, ct)
				}
			}
			if _, err := os.Stat(d.files.StagingPartPath(filesTestMsgID)); err != nil {
				t.Fatalf(".part file gone after a refused DELETE: %v", err)
			}
			if row, err := d.files.GetFetch("tok-del"); err != nil || row.State != state {
				t.Fatalf("fetch row after a refused DELETE = %+v, %v; want state %s", row, err, state)
			}
		})
	}
}

func TestAPI_StagingDelete_RemovesWhenNotInFlight(t *testing.T) {
	for _, state := range []files.FetchState{files.FetchStateReady, files.FetchStateFailedTransient} {
		t.Run(string(state), func(t *testing.T) {
			d := newFilesTestDaemon(t)
			srv := httptest.NewServer(d.apiHandler())
			defer srv.Close()

			putStagingFetch(t, d, "tok-del", state)
			writeStagingFile(t, d.files.StagingPath(filesTestMsgID), "whole")
			writeStagingFile(t, d.files.StagingPartPath(filesTestMsgID), "par")

			if got := deleteStaging(t, srv, filesTestMsgID, "tok-del"); got != http.StatusNoContent {
				t.Fatalf("DELETE status = %d, want 204", got)
			}
			for _, p := range []string{d.files.StagingPath(filesTestMsgID), d.files.StagingPartPath(filesTestMsgID)} {
				if _, err := os.Stat(p); !os.IsNotExist(err) {
					t.Fatalf("%s left after DELETE: stat err = %v", filepath.Base(p), err)
				}
			}
			if _, err := d.files.GetFetch("tok-del"); err == nil {
				t.Fatalf("fetch row left after DELETE")
			}
		})
	}
}

func TestAPI_StagingDelete_RefusesPartName(t *testing.T) {
	d := newFilesTestDaemon(t)
	srv := httptest.NewServer(d.apiHandler())
	defer srv.Close()
	writeStagingFile(t, d.files.StagingPartPath(filesTestMsgID), "par")

	if got := deleteStaging(t, srv, filesTestMsgID+files.StagingPartSuffix, ""); got != http.StatusBadRequest {
		t.Fatalf("DELETE of a .part name status = %d, want 400", got)
	}
	if _, err := os.Stat(d.files.StagingPartPath(filesTestMsgID)); err != nil {
		t.Fatalf(".part file gone: %v", err)
	}
}

func TestAPI_FetchFile_ReadyRowWithBlobIsKept(t *testing.T) {
	cases := map[string]struct {
		state     files.FetchState
		blob      bool
		wantState files.FetchState
	}{
		"ready, blob staged":          {state: files.FetchStateReady, blob: true, wantState: files.FetchStateReady},
		"ready, blob gone":            {state: files.FetchStateReady, wantState: files.FetchStatePending},
		"failed permanent, blob gone": {state: files.FetchStateFailedPermanent, wantState: files.FetchStatePending},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			d := newFilesTestDaemon(t)
			srv := httptest.NewServer(d.apiHandler())
			defer srv.Close()

			req := fetchFileRequest{
				MsgID:          filesTestMsgID,
				PeerID:         filesPeerA,
				Token:          "tok-again",
				UrlPath:        "/files/tok-again",
				ExpectedSize:   5,
				ExpectedSha256: strings.Repeat("a", 64),
			}
			resp := postJSON(t, srv, "/files/fetch", req)
			resp.Body.Close()
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("first POST status = %d, want 202", resp.StatusCode)
			}
			row, err := d.files.GetFetch("tok-again")
			if err != nil {
				t.Fatal(err)
			}
			row.State = c.state
			row.BytesReceived = 5
			if err := d.files.UpdateFetch(row); err != nil {
				t.Fatal(err)
			}
			if c.blob {
				writeStagingFile(t, d.files.StagingPath(filesTestMsgID), "whole")
			}

			resp = postJSON(t, srv, "/files/fetch", req)
			var out fetchFileResponse
			_ = json.NewDecoder(resp.Body).Decode(&out)
			resp.Body.Close()
			if resp.StatusCode != http.StatusAccepted || out.State != c.wantState {
				t.Fatalf("second POST = %d state %q, want 202 state %q", resp.StatusCode, out.State, c.wantState)
			}
			got, err := d.files.GetFetch("tok-again")
			if err != nil || got.State != c.wantState {
				t.Fatalf("row after the second POST = %+v, %v; want state %q", got, err, c.wantState)
			}
		})
	}
}
