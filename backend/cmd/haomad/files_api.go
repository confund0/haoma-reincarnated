package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"haoma/internal/files"
)

const maxStageBlobBody = 16 << 20

func (d *daemon) handleFileFetch(w http.ResponseWriter, r *http.Request) {
	if d.files == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("files manager not initialised"))
		return
	}
	token := r.PathValue("token")
	row, err := d.files.LookupToken(token)
	switch {
	case errors.Is(err, files.ErrTokenNotFound):
		http.Error(w, "unknown token", http.StatusNotFound)
		slog.Debug("/files miss",
			slog.String("status", "404"),
			slog.String("remote", r.RemoteAddr),
		)
		return
	case errors.Is(err, files.ErrTokenInvalidated):
		http.Error(w, "token invalidated", http.StatusGone)
		slog.Debug("/files invalidated",
			slog.String("status", "410"),
			slog.String("msg_id", row.MsgID),
			slog.String("recipient_peer_id", row.RecipientPeerID),
		)
		return
	case err != nil:
		slog.Warn("/files lookup failed", slog.Any("err", err))
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	f, size, err := d.files.OpenBlob(row.MsgID)
	if errors.Is(err, files.ErrBlobNotFound) {

		slog.Warn("/files token outlived blob; serving 404",
			slog.String("token", token),
			slog.String("msg_id", row.MsgID),
		)
		http.Error(w, "blob missing", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Warn("/files open blob failed",
			slog.String("msg_id", row.MsgID),
			slog.Any("err", err),
		)
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer f.Close()

	slog.Debug("/files serving",
		slog.String("msg_id", row.MsgID),
		slog.String("recipient_peer_id", row.RecipientPeerID),
		slog.Int64("size", size),
		slog.String("range", r.Header.Get("Range")),
	)

	w.Header().Set("Content-Type", "application/octet-stream")

	http.ServeContent(w, r, "", time.Time{}, f)
}

type stageFileRequest struct {
	MsgID            string   `json:"msg_id"`
	Ciphertext       []byte   `json:"ciphertext"`
	RecipientPeerIDs []string `json:"recipient_peer_ids"`
	ExpiresAt        int64    `json:"expires_at,omitempty"`
}

type stageFileResponse struct {
	MsgID  string   `json:"msg_id"`
	Tokens []string `json:"tokens"`
}

func (d *daemon) handleStageFile(w http.ResponseWriter, r *http.Request) {
	if d.files == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("files manager not initialised"))
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, errors.New("content-type must be application/json"))
		return
	}
	var req stageFileRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxStageBlobBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.MsgID == "" {
		writeErr(w, http.StatusBadRequest, errors.New("msg_id required"))
		return
	}
	if len(req.Ciphertext) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("ciphertext required"))
		return
	}
	if len(req.RecipientPeerIDs) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("recipient_peer_ids required"))
		return
	}

	tokens, err := d.files.StageBlob(req.MsgID, req.Ciphertext, req.RecipientPeerIDs, req.ExpiresAt)
	switch {
	case errors.Is(err, files.ErrCiphertextTooLong):
		writeErr(w, http.StatusRequestEntityTooLarge, err)
		return
	case errors.Is(err, files.ErrMsgIDInUse):
		writeErr(w, http.StatusConflict, err)
		return
	case err != nil:
		slog.Warn("/files stage failed",
			slog.String("msg_id", req.MsgID),
			slog.Any("err", err),
		)
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	slog.Info("file staged",
		slog.String("msg_id", req.MsgID),
		slog.Int("recipients", len(req.RecipientPeerIDs)),
		slog.Int("ciphertext_bytes", len(req.Ciphertext)),
		slog.Int64("expires_at", req.ExpiresAt),
	)
	writeJSON(w, http.StatusCreated, stageFileResponse{
		MsgID:  req.MsgID,
		Tokens: tokens,
	})
}

func (d *daemon) handleDropFile(w http.ResponseWriter, r *http.Request) {
	if d.files == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("files manager not initialised"))
		return
	}
	msgID := r.PathValue("msg_id")
	if msgID == "" {
		writeErr(w, http.StatusBadRequest, errors.New("msg_id required"))
		return
	}
	if err := d.files.DropByMsgID(msgID); err != nil {
		slog.Warn("/files drop failed",
			slog.String("msg_id", msgID),
			slog.Any("err", err),
		)
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("drop %s: %w", msgID, err))
		return
	}
	slog.Info("file dropped", slog.String("msg_id", msgID))
	w.WriteHeader(http.StatusNoContent)
}

type fetchFileRequest struct {
	MsgID          string `json:"msg_id"`
	PeerID         string `json:"peer_id"`
	Token          string `json:"token"`
	UrlPath        string `json:"url_path"`
	ExpectedSize   int64  `json:"expected_size"`
	ExpectedSha256 string `json:"expected_sha256"`
}

type fetchFileResponse struct {
	MsgID         string           `json:"msg_id"`
	Token         string           `json:"token"`
	State         files.FetchState `json:"state"`
	BytesReceived int64            `json:"bytes_received,omitempty"`
}

func (d *daemon) handleFetchFile(w http.ResponseWriter, r *http.Request) {
	if d.files == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("files manager not initialised"))
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, errors.New("content-type must be application/json"))
		return
	}
	var req fetchFileRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.MsgID == "" || req.PeerID == "" || req.Token == "" || req.UrlPath == "" {
		writeErr(w, http.StatusBadRequest, errors.New("msg_id, peer_id, token, url_path required"))
		return
	}
	if req.ExpectedSize <= 0 {
		writeErr(w, http.StatusBadRequest, errors.New("expected_size must be > 0"))
		return
	}
	if req.ExpectedSha256 == "" {
		writeErr(w, http.StatusBadRequest, errors.New("expected_sha256 required"))
		return
	}

	row := files.Fetch{
		Token:          req.Token,
		MsgID:          req.MsgID,
		PeerID:         req.PeerID,
		UrlPath:        req.UrlPath,
		ExpectedSize:   req.ExpectedSize,
		ExpectedSha256: req.ExpectedSha256,
		State:          files.FetchStatePending,
		CreatedAt:      time.Now().Unix(),
	}
	switch err := d.files.PutFetch(row); {
	case errors.Is(err, files.ErrFetchTokenInUse):

		existing, gerr := d.files.GetFetch(req.Token)
		if gerr != nil {
			writeErr(w, http.StatusInternalServerError, gerr)
			return
		}
		writeJSON(w, http.StatusAccepted, fetchFileResponse{
			MsgID:         existing.MsgID,
			Token:         existing.Token,
			State:         existing.State,
			BytesReceived: existing.BytesReceived,
		})
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	if w := d.fetchWorker.Load(); w != nil {
		w.Kick(req.Token)
	}
	slog.Info("file fetch enqueued",
		slog.String("msg_id", req.MsgID),
		slog.String("peer_id", req.PeerID),
		slog.Int64("expected_size", req.ExpectedSize),
	)
	writeJSON(w, http.StatusAccepted, fetchFileResponse{
		MsgID: req.MsgID,
		Token: req.Token,
		State: row.State,
	})
}

func (d *daemon) handleStagingGet(w http.ResponseWriter, r *http.Request) {
	if d.files == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("files manager not initialised"))
		return
	}
	msgID := r.PathValue("msg_id")
	if msgID == "" {
		writeErr(w, http.StatusBadRequest, errors.New("msg_id required"))
		return
	}
	f, _, err := d.files.OpenStaging(msgID)
	if errors.Is(err, files.ErrBlobNotFound) {
		http.Error(w, "staging blob not found", http.StatusNotFound)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, "", time.Time{}, f)
}

type receiveReceiptRequest struct {
	Token           string `json:"token"`
	RecipientPeerID string `json:"recipient_peer_id"`
}

type receiveReceiptResponse struct {
	Token             string `json:"token"`
	ReceiptsRemaining int    `json:"receipts_remaining"`
}

func (d *daemon) handleReceiveFileReceipt(w http.ResponseWriter, r *http.Request) {
	if d.files == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("files manager not initialised"))
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, errors.New("content-type must be application/json"))
		return
	}
	var req receiveReceiptRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Token == "" || req.RecipientPeerID == "" {
		writeErr(w, http.StatusBadRequest, errors.New("token and recipient_peer_id required"))
		return
	}

	row, err := d.files.LookupToken(req.Token)
	switch {
	case errors.Is(err, files.ErrTokenNotFound):
		http.Error(w, "unknown token", http.StatusNotFound)
		slog.Debug("/files/receipts unknown token",
			slog.String("token", req.Token),
			slog.String("recipient_peer_id", req.RecipientPeerID),
		)
		return
	case errors.Is(err, files.ErrTokenInvalidated):

		if row.RecipientPeerID != req.RecipientPeerID {

			http.Error(w, "recipient mismatch", http.StatusForbidden)
			slog.Warn("/files/receipts recipient mismatch on invalidated token",
				slog.String("token", req.Token),
				slog.String("claimed_recipient", req.RecipientPeerID),
				slog.String("bound_recipient", row.RecipientPeerID),
			)
			return
		}
		writeJSON(w, http.StatusOK, receiveReceiptResponse{
			Token:             req.Token,
			ReceiptsRemaining: 0,
		})
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	if row.RecipientPeerID != req.RecipientPeerID {
		http.Error(w, "recipient mismatch", http.StatusForbidden)
		slog.Warn("/files/receipts recipient mismatch",
			slog.String("token", req.Token),
			slog.String("claimed_recipient", req.RecipientPeerID),
			slog.String("bound_recipient", row.RecipientPeerID),
			slog.String("msg_id", row.MsgID),
		)
		return
	}

	if err := d.files.DecrementReceipts(req.Token); err != nil {

		if errors.Is(err, files.ErrTokenNotFound) {
			http.Error(w, "unknown token", http.StatusNotFound)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	slog.Info("file receipt acked",
		slog.String("token", req.Token),
		slog.String("recipient_peer_id", req.RecipientPeerID),
		slog.String("msg_id", row.MsgID),
	)
	writeJSON(w, http.StatusOK, receiveReceiptResponse{
		Token:             req.Token,
		ReceiptsRemaining: row.ReceiptsRemaining - 1,
	})
}

func (d *daemon) handleStagingDelete(w http.ResponseWriter, r *http.Request) {
	if d.files == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("files manager not initialised"))
		return
	}
	msgID := r.PathValue("msg_id")
	if msgID == "" {
		writeErr(w, http.StatusBadRequest, errors.New("msg_id required"))
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if r.ContentLength > 0 {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}
	if err := d.files.DeleteStaging(msgID); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if body.Token != "" {
		if err := d.files.DeleteFetch(body.Token); err != nil {
			slog.Warn("/files/staging delete: drop fetch row failed",
				slog.String("token", body.Token),
				slog.Any("err", err),
			)
		}
	}
	slog.Debug("file staging dropped", slog.String("msg_id", msgID))
	w.WriteHeader(http.StatusNoContent)
}

func (d *daemon) handleRetryFailed(w http.ResponseWriter, r *http.Request) {
	if d.files == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("files manager not initialised"))
		return
	}
	worker := d.fetchWorker.Load()
	if worker == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("fetch worker not running"))
		return
	}

	_, _ = io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, 4<<10))

	isRetired := func(peerID string) bool {
		p, err := d.registry.Get(peerID)
		if err != nil {
			return false
		}
		return p.RetiredAt != 0
	}
	enqueued, err := worker.RetryAllFailed(r.Context(), isRetired)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	slog.Info("file retry sweep done", slog.Int("enqueued", enqueued))
	writeJSON(w, http.StatusOK, map[string]int{"enqueued": enqueued})
}
