package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/chacha20poly1305"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/files"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/msg"
)

type sendFileResult struct {
	MsgID      string
	EnvelopeID string
	SenderSeq  uint64
	Name       string
	Size       uint64
	Mime       string
}

func (sd *sessionDispatcher) handleSendFile(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle send_file")
	var req ipc.SendFileRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	if req.PeerID == "" {
		sendError(sess, f.ID, "bad_request", "peer_id empty")
		return
	}
	if req.Path == "" {
		sendError(sess, f.ID, "bad_request", "path empty")
		return
	}
	if sd.d.cipher == nil || sd.d.peerSeq == nil || sd.d.backendClient == nil ||
		sd.d.chats == nil || sd.d.events == nil || sd.d.files == nil {
		sendError(sess, f.ID, "not_ready", "frontend wiring incomplete (cipher / peer-seq / chats / events / files / backend client missing)")
		return
	}

	if _, err := resolveChatForPeer(ctx, sd.d, req.PeerID); err != nil {
		sendError(sess, f.ID, "unknown_peer", fmt.Sprintf("resolve chat for peer %s: %v", req.PeerID, err))
		return
	}
	dc, err := sd.d.chats.GetByDirectPeer(req.PeerID)
	if err != nil {
		sendError(sess, f.ID, "internal", fmt.Sprintf("post-resolve chat lookup for peer %s: %v", req.PeerID, err))
		return
	}

	res, code, err := runSendFile(ctx, sd.d, dc, req.PeerID, req.Path)
	if err != nil {
		sendError(sess, f.ID, code, err.Error())
		return
	}

	resp, encErr := ipc.NewFrame(ipc.FrameFileSent, f.ID, ipc.SendFileResponse{
		EnvelopeID: res.EnvelopeID,
		MsgID:      res.MsgID,
		SenderSeq:  res.SenderSeq,
		Name:       res.Name,
		Size:       res.Size,
		Mime:       res.Mime,
	})
	if encErr != nil {
		sendError(sess, f.ID, "encode_frame", encErr.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send file_sent frame failed", slog.Any("err", err))
	}
}

func runSendFile(ctx context.Context, d *daemon, dc *chat.DirectChat, peerID, path string) (sendFileResult, string, error) {
	logger := slog.With(
		slog.String("peer_id", peerID),
		slog.String("path", path),
	)

	info, err := os.Stat(path)
	if err != nil {
		return sendFileResult{}, "file_open", fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() {
		return sendFileResult{}, "file_open", fmt.Errorf("%s is a directory", path)
	}
	if info.Size() > files.MaxPlaintextBytes {
		return sendFileResult{}, "too_large", fmt.Errorf("file %d bytes exceeds policy cap %d", info.Size(), files.MaxPlaintextBytes)
	}
	plaintext, err := os.ReadFile(path)
	if err != nil {
		return sendFileResult{}, "file_open", fmt.Errorf("read %s: %w", path, err)
	}

	if len(plaintext) > files.MaxPlaintextBytes {
		zeroBytes(plaintext)
		return sendFileResult{}, "too_large", fmt.Errorf("file grew during read: %d bytes exceeds policy cap %d", len(plaintext), files.MaxPlaintextBytes)
	}

	name := filepath.Base(path)
	mime := http.DetectContentType(plaintext)

	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := rand.Read(key); err != nil {
		zeroBytes(plaintext)
		return sendFileResult{}, "build_failed", fmt.Errorf("mint aead key: %w", err)
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		zeroBytes(plaintext)
		zeroBytes(key)
		return sendFileResult{}, "build_failed", fmt.Errorf("mint aead nonce: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		zeroBytes(plaintext)
		zeroBytes(key)
		return sendFileResult{}, "build_failed", fmt.Errorf("aead init: %w", err)
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, nil)

	size := uint64(len(ciphertext))

	sum := sha256.Sum256(ciphertext)
	sha256Hex := hex.EncodeToString(sum[:])

	msgID, err := msg.NewID()
	if err != nil {
		zeroBytes(plaintext)
		zeroBytes(key)
		return sendFileResult{}, "build_failed", fmt.Errorf("new msg id: %w", err)
	}

	recipients := []string{peerID}
	stageResp, err := d.backendClient.StageFile(ctx, backendapi.StageFileRequest{
		MsgID:            msgID,
		Ciphertext:       ciphertext,
		RecipientPeerIDs: recipients,
		ExpiresAt:        0,
	})
	if err != nil {
		zeroBytes(plaintext)
		zeroBytes(key)
		return sendFileResult{}, "stage_failed", fmt.Errorf("stage ciphertext on haomad: %w", err)
	}
	if len(stageResp.Tokens) != len(recipients) {
		zeroBytes(plaintext)
		zeroBytes(key)
		return sendFileResult{}, "stage_failed", fmt.Errorf("haomad returned %d tokens, want %d", len(stageResp.Tokens), len(recipients))
	}
	recipientTokens := make(map[string]string, len(recipients))
	for i, rid := range recipients {
		recipientTokens[rid] = stageResp.Tokens[i]
	}

	primaryToken := stageResp.Tokens[0]

	meta := files.Metadata{
		MsgID:            msgID,
		ChatID:           dc.ID,
		Direction:        files.DirOut,
		Token:            primaryToken,
		RecipientTokens:  recipientTokens,
		OriginalName:     name,
		Mime:             mime,
		Size:             size,
		Sha256Ciphertext: sha256Hex,
		KeyBytes:         append([]byte(nil), key...),
		Nonce:            append([]byte(nil), nonce...),
		State:            files.StateReady,
		CreatedAt:        time.Now().Unix(),
		UpdatedAt:        time.Now().Unix(),
	}
	if err := d.files.PutMeta(meta); err != nil {

		dropStagedBlob(ctx, d, msgID)
		zeroBytes(plaintext)
		zeroBytes(key)
		return sendFileResult{}, "internal", fmt.Errorf("persist outbound metadata: %w", err)
	}

	sealedPath, sealErr := d.files.SealAtRest(dc.ID, msgID, plaintext)
	if sealErr != nil {
		logger.Warn("seal-at-rest sender copy failed (continuing)",
			slog.String("msg_id", msgID),
			slog.Any("err", sealErr),
		)
	} else {

		meta.SealedPath = sealedPath
		if err := d.files.PutMeta(meta); err != nil {
			logger.Warn("persist sealed_path on outbound metadata failed",
				slog.String("msg_id", msgID),
				slog.Any("err", err),
			)
		}
	}

	zeroBytes(plaintext)

	var firstEnvelope string
	var firstSeq uint64
	for i, rid := range recipients {
		token := stageResp.Tokens[i]
		seq, err := d.peerSeq.NextSendSeq(rid)
		if err != nil {

			zeroBytes(key)
			if firstEnvelope == "" {
				dropStagedBlob(ctx, d, msgID)
				_ = d.files.DeleteMeta(msgID)
			}
			return sendFileResult{}, "internal", fmt.Errorf("next seq for peer %s: %w", rid, err)
		}
		wrapper, err := msg.BuildFileOffer(seq, time.Now().Unix(), msgID, token, "/files/"+token, name, size, mime, sha256Hex, dc.RetentionTTL)
		if err != nil {
			zeroBytes(key)
			if firstEnvelope == "" {
				dropStagedBlob(ctx, d, msgID)
				_ = d.files.DeleteMeta(msgID)
			}
			return sendFileResult{}, "build_failed", fmt.Errorf("build file_offer for peer %s: %w", rid, err)
		}
		plaintextWrapper, err := msg.Marshal(wrapper)
		if err != nil {
			zeroBytes(key)
			if firstEnvelope == "" {
				dropStagedBlob(ctx, d, msgID)
				_ = d.files.DeleteMeta(msgID)
			}
			return sendFileResult{}, "build_failed", fmt.Errorf("marshal file_offer for peer %s: %w", rid, err)
		}
		blob, err := d.cipher.Encrypt(ctx, rid, plaintextWrapper)
		if err != nil {
			zeroBytes(key)
			if firstEnvelope == "" {
				dropStagedBlob(ctx, d, msgID)
				_ = d.files.DeleteMeta(msgID)
			}
			return sendFileResult{}, "encrypt_failed", fmt.Errorf("encrypt for peer %s: %w", rid, err)
		}
		sendResp, err := d.backendClient.Send(ctx, backendapi.SendRequest{
			PeerID:         rid,
			Payload:        blob,
			PresenceSource: backendapi.PresenceSourceHaoma,
		})
		if err != nil {
			zeroBytes(key)
			if firstEnvelope == "" {
				dropStagedBlob(ctx, d, msgID)
				_ = d.files.DeleteMeta(msgID)
			}
			return sendFileResult{}, "backend_send", fmt.Errorf("haomad send for peer %s: %w", rid, err)
		}
		if firstEnvelope == "" {
			firstEnvelope = sendResp.EnvelopeID
			firstSeq = seq
		}
		logger.Debug("file offer envelope shipped",
			slog.String("recipient_peer_id", rid),
			slog.String("envelope_id", sendResp.EnvelopeID),
			slog.String("msg_id", msgID),
			slog.String("token", token),
			slog.Uint64("sender_seq", seq),
		)
	}
	zeroBytes(key)

	feBody := FileEventBody{
		Token:            primaryToken,
		UrlPath:          "/files/" + primaryToken,
		Name:             name,
		Size:             size,
		Mime:             mime,
		Sha256Ciphertext: sha256Hex,
		State:            string(files.StateReady),
	}
	bodyRaw, err := json.Marshal(feBody)
	if err != nil {

		logger.Error("marshal outbound file event body failed", slog.Any("err", err))
	} else if _, persistErr := d.events.AppendOutbound(events.OutboundParams{
		ChatID:        dc.ID,
		Kind:          events.KindFile,
		SenderSeq:     firstSeq,
		EnvelopeID:    firstEnvelope,
		MsgID:         msgID,
		ExpireSeconds: dc.RetentionTTL,
		Body:          bodyRaw,
	}); persistErr != nil {
		logger.Error("persist outbound file event failed",
			slog.String("envelope_id", firstEnvelope),
			slog.Any("err", persistErr),
		)
	} else {
		bumpChatActivity(ctx, d, dc.ID, time.Now().Unix())
	}

	logger.Info("file sent",
		slog.String("msg_id", msgID),
		slog.String("envelope_id", firstEnvelope),
		slog.Uint64("sender_seq", firstSeq),
		slog.String("name", name),
		slog.Uint64("size", size),
		slog.String("mime", mime),
		slog.Int("recipients", len(recipients)),
	)

	return sendFileResult{
		MsgID:      msgID,
		EnvelopeID: firstEnvelope,
		SenderSeq:  firstSeq,
		Name:       name,
		Size:       size,
		Mime:       mime,
	}, "", nil
}

func dropStagedBlob(ctx context.Context, d *daemon, msgID string) {
	if d == nil || d.backendClient == nil || msgID == "" {
		return
	}
	dropCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := d.backendClient.DropFile(dropCtx, msgID); err != nil {
		slog.Warn("drop staged blob after send-file failure",
			slog.String("msg_id", msgID),
			slog.Any("err", err),
		)
	}
}

func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
