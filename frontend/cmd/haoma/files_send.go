package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/chacha20poly1305"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/files"
	"haoma-frontend/internal/imgstrip"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/logid"
	"haoma-frontend/internal/mirror"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/syncrail"
)

const (
	imageModeCompressed = "compressed"
	imageModeOriginal   = "original"
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
	var req ipc.SendFileRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	slog.Debug("handle send_file",
		slog.String("peer_id", req.PeerID),
		slog.String("path", req.Path),
		slog.String("image_mode", req.ImageMode),
	)
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

	if refuseLinkedDevice(sd.d, sess, f, req.PeerID) {
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

	dc, release, err := awaitSendSession(ctx, sd.d, req.PeerID, dc.ID)
	if err != nil {
		sendError(sess, f.ID, sendWaitErrorCode(err), err.Error())
		return
	}
	release()

	res, code, err := runSendFile(ctx, sd.d, dc, req.PeerID, req.Path, req.ImageMode, req.Caption)
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

func runSendFile(ctx context.Context, d *daemon, dc *chat.DirectChat, peerID, path, imageMode, caption string) (sendFileResult, string, error) {

	logger := slog.With(
		slog.String("peer_id", peerID),
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

	imgStripDecision := "skipped_original"
	imgStripInBytes := len(plaintext)
	imgStripOutBytes := len(plaintext)
	if imageMode != imageModeOriginal {
		maxEdge := imgstrip.DefaultMaxLongEdge
		stripped, ext, ierr := imgstrip.Process(plaintext, maxEdge)
		switch {
		case ierr == nil:
			plaintext = stripped
			name = renameWithExt(name, ext)
			imgStripDecision = "applied"
			imgStripOutBytes = len(stripped)
		case errors.Is(ierr, imgstrip.ErrUnsupportedMIME):
			imgStripDecision = "skipped_unsupported_mime"
		default:
			imgStripDecision = "failed_shipped_raw"
			logger.Warn("imgstrip failed; shipping raw bytes",
				slog.String("err_kind", "imgstrip"),
				slog.Any("err", ierr),
			)
		}
	}
	logger.Debug("imgstrip decision",
		slog.String("image_mode", imageMode),
		slog.String("decision", imgStripDecision),
		slog.Int("in_bytes", imgStripInBytes),
		slog.Int("out_bytes", imgStripOutBytes),
	)

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

	recipients := dc.FanoutPeerIDs()
	if len(recipients) == 0 {
		zeroBytes(plaintext)
		zeroBytes(key)
		return sendFileResult{}, "unknown_peer", fmt.Errorf("chat %s has no routing targets", dc.ID)
	}
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
		logger.Debug("file offer token minted for a device",
			slog.String("recipient_peer_id", rid),
			slog.String("msg_id", msgID),
		)
	}

	sibStagings := stageForSiblings(ctx, d, logger, msgID, plaintext, d.siblings())
	defer wipeSiblingStagings(sibStagings)

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

		SiblingStagingIDs: siblingStagingIDs(sibStagings),
		CreatedAt:         time.Now().Unix(),
		UpdatedAt:         time.Now().Unix(),
	}
	if err := d.files.PutMeta(meta); err != nil {

		dropStagedBlob(ctx, d, msgID)
		dropSiblingStagings(ctx, d, sibStagings)
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

	prepare := func(ctx context.Context, deviceID string) (backendapi.SendRequest, []byte, error) {
		token, ok := recipientTokens[deviceID]
		if !ok {
			return backendapi.SendRequest{}, nil, fmt.Errorf("no token minted for device %s", logid.Short(deviceID))
		}
		seq, err := d.peerSeq.NextSendSeq(deviceID)
		if err != nil {
			return backendapi.SendRequest{}, nil, fmt.Errorf("next seq: %w", err)
		}
		wrapper, err := msg.BuildFileOffer(seq, time.Now().Unix(), msgID, token, "/files/"+token, name, size, mime, sha256Hex, caption, dc.RetentionTTL)
		if err != nil {
			return backendapi.SendRequest{}, nil, fmt.Errorf("build file_offer: %w", err)
		}
		plaintextWrapper, err := msg.Marshal(wrapper)
		if err != nil {
			return backendapi.SendRequest{}, nil, fmt.Errorf("marshal file_offer: %w", err)
		}
		blob, err := d.cipher.Encrypt(ctx, deviceID, plaintextWrapper)
		if err != nil {
			return backendapi.SendRequest{}, nil, fmt.Errorf("encrypt: %w", err)
		}
		logger.Debug("file offer envelope prepared",
			slog.String("recipient_peer_id", deviceID),
			slog.String("msg_id", msgID),
			slog.Uint64("sender_seq", seq),
		)
		return backendapi.SendRequest{
			PeerID:         deviceID,
			Payload:        blob,
			PresenceSource: backendapi.PresenceSourceHaoma,
		}, plaintextWrapper, nil
	}

	siblingOffer := func(sib string, primary *msg.Wrapper) ([]byte, syncrail.SiblingOffer, error) {
		st, ok := sibStagings[sib]
		if !ok {
			return nil, syncrail.SiblingOffer{}, fmt.Errorf("no blob staged for sibling %s", logid.Short(sib))
		}
		wrapper, err := msg.BuildFileOffer(primary.Seq, primary.Ts, msgID, st.token, "/files/"+st.token, name, size, mime, st.sha256Hex, caption, dc.RetentionTTL)
		if err != nil {
			return nil, syncrail.SiblingOffer{}, fmt.Errorf("build sibling file_offer: %w", err)
		}
		plain, err := msg.Marshal(wrapper)
		if err != nil {
			return nil, syncrail.SiblingOffer{}, fmt.Errorf("marshal sibling file_offer: %w", err)
		}
		return plain, syncrail.SiblingOffer{
			StagingID:        st.stagingID,
			Token:            st.token,
			Key:              append([]byte(nil), st.key...),
			Nonce:            append([]byte(nil), st.nonce...),
			Sha256Ciphertext: st.sha256Hex,
			BlobSize:         st.size,
			PlaintextSize:    st.plainLen,
		}, nil
	}

	result, dErr := mirror.Dispatch(ctx, d.backendClient, mirror.NewRail(d.railKey, d.siblings, d.peerMeta),
		mirror.FanoutTargets(recipients), prepare, mirror.NewFileOfferOp(string(dc.ID), siblingOffer))
	zeroBytes(key)
	if dErr != nil {
		dropStagedBlob(ctx, d, msgID)
		dropSiblingStagings(ctx, d, sibStagings)
		_ = d.files.DeleteMeta(msgID)
		return sendFileResult{}, mirrorDispatchErrorCode(dErr), fmt.Errorf("file offer fan-out: %w", dErr)
	}

	dropUnofferedSiblingStagings(ctx, d, logger, msgID, sibStagings, result.SiblingOffersSent)
	firstEnvelope := result.Primary.EnvelopeID
	firstSeq := result.Primary.Seq
	logger.Debug("file offer fan-out complete",
		slog.String("msg_id", msgID),
		slog.Int("targets", len(recipients)),
		slog.Int("accepted", len(result.EnvelopeIDs)),
		slog.Int("skipped", len(result.Skipped)),
	)

	feBody := FileEventBody{
		Token:            primaryToken,
		UrlPath:          "/files/" + primaryToken,
		Name:             name,
		Size:             size,
		Mime:             mime,
		Sha256Ciphertext: sha256Hex,
		State:            string(files.StateReady),
		Caption:          caption,
	}
	bodyRaw, err := json.Marshal(feBody)
	if err != nil {

		logger.Error("marshal outbound file event body failed", slog.Any("err", err))
	} else if _, persistErr := d.events.AppendOutbound(events.OutboundParams{
		ChatID:    dc.ID,
		Kind:      events.KindFile,
		SenderSeq: firstSeq,

		EnvelopeIDs:   result.EnvelopeIDs,
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
		slog.Uint64("size", size),
		slog.String("mime", mime),
		slog.String("image_mode", imageMode),
		slog.String("imgstrip", imgStripDecision),
		slog.Int("devices", len(recipients)),
		slog.Int("devices_accepted", len(result.EnvelopeIDs)),
		slog.Int("devices_skipped", len(result.Skipped)),
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
	dropHostedBlob(ctx, d, "msg_id", msgID)
}

func dropSiblingBlob(ctx context.Context, d *daemon, stagingID string) {
	dropHostedBlob(ctx, d, "staging_id", stagingID)
}

func dropHostedBlob(ctx context.Context, d *daemon, idKey, id string) {
	if d == nil || d.backendClient == nil || id == "" {
		return
	}
	dropCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := d.backendClient.DropFile(dropCtx, id); err != nil {
		slog.Warn("drop staged blob after send-file failure",
			slog.String(idKey, id),
			slog.Any("err", err),
		)
	}
}

type siblingStaging struct {
	stagingID  string
	token      string
	key, nonce []byte
	sha256Hex  string
	size       uint64
	plainLen   uint64
}

const siblingPadMin = 64 << 10

func siblingPadMax(size int) int {
	if m := size / 4; m > siblingPadMin {
		return m
	}
	return siblingPadMin
}

func siblingPadLen(size int) (int, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(siblingPadMax(size))))
	if err != nil {
		return 0, err
	}
	return int(n.Int64()) + 1, nil
}

func stageForSiblings(ctx context.Context, d *daemon, logger *slog.Logger, msgID string, plaintext []byte, siblings []string) map[string]siblingStaging {
	out := make(map[string]siblingStaging, len(siblings))
	for _, sib := range siblings {
		st, err := stageForSibling(ctx, d, sib, plaintext)
		if err != nil {
			logger.Warn("file staging for a sibling failed; it gets no sibling offer",
				slog.String("sibling_peer_id", sib),
				slog.String("msg_id", msgID),
				slog.Any("err", err),
			)
			continue
		}
		out[sib] = st
		logger.Debug("file staged for a sibling on its own",
			slog.String("sibling_peer_id", sib),
			slog.String("msg_id", msgID),
			slog.String("staging_id", st.stagingID),
			slog.Uint64("size", st.size),
		)
	}
	return out
}

func stageForSibling(ctx context.Context, d *daemon, sib string, plaintext []byte) (siblingStaging, error) {
	st := siblingStaging{
		key:   make([]byte, chacha20poly1305.KeySize),
		nonce: make([]byte, chacha20poly1305.NonceSizeX),
	}
	if _, err := rand.Read(st.key); err != nil {
		return siblingStaging{}, fmt.Errorf("mint key: %w", err)
	}
	if _, err := rand.Read(st.nonce); err != nil {
		zeroBytes(st.key)
		return siblingStaging{}, fmt.Errorf("mint nonce: %w", err)
	}
	aead, err := chacha20poly1305.NewX(st.key)
	if err != nil {
		zeroBytes(st.key)
		return siblingStaging{}, fmt.Errorf("aead init: %w", err)
	}

	padLen, err := siblingPadLen(len(plaintext))
	if err != nil {
		zeroBytes(st.key)
		return siblingStaging{}, fmt.Errorf("draw padding length: %w", err)
	}
	padded := make([]byte, len(plaintext)+padLen)
	copy(padded, plaintext)
	if _, err := rand.Read(padded[len(plaintext):]); err != nil {
		zeroBytes(padded)
		zeroBytes(st.key)
		return siblingStaging{}, fmt.Errorf("draw padding: %w", err)
	}
	ciphertext := aead.Seal(nil, st.nonce, padded, nil)
	zeroBytes(padded)
	sum := sha256.Sum256(ciphertext)
	st.sha256Hex = hex.EncodeToString(sum[:])
	st.size = uint64(len(ciphertext))
	st.plainLen = uint64(len(plaintext))

	if st.stagingID, err = msg.NewID(); err != nil {
		zeroBytes(st.key)
		return siblingStaging{}, fmt.Errorf("mint staging id: %w", err)
	}
	resp, err := d.backendClient.StageFile(ctx, backendapi.StageFileRequest{
		MsgID:            st.stagingID,
		Ciphertext:       ciphertext,
		RecipientPeerIDs: []string{sib},
		ExpiresAt:        0,
	})
	if err != nil {
		zeroBytes(st.key)
		return siblingStaging{}, fmt.Errorf("stage: %w", err)
	}
	if len(resp.Tokens) != 1 {
		zeroBytes(st.key)
		dropSiblingBlob(ctx, d, st.stagingID)
		return siblingStaging{}, fmt.Errorf("haomad returned %d tokens, want 1", len(resp.Tokens))
	}
	st.token = resp.Tokens[0]
	return st, nil
}

func siblingStagingIDs(stagings map[string]siblingStaging) []string {
	if len(stagings) == 0 {
		return nil
	}
	ids := make([]string, 0, len(stagings))
	for _, st := range stagings {
		ids = append(ids, st.stagingID)
	}
	sort.Strings(ids)
	return ids
}

func dropSiblingStagings(ctx context.Context, d *daemon, stagings map[string]siblingStaging) {
	for _, id := range siblingStagingIDs(stagings) {
		dropSiblingBlob(ctx, d, id)
	}
}

func dropUnofferedSiblingStagings(ctx context.Context, d *daemon, logger *slog.Logger, msgID string, stagings map[string]siblingStaging, sent []string) {
	offered := make(map[string]bool, len(sent))
	for _, sib := range sent {
		offered[sib] = true
	}
	dropped := 0
	for sib, st := range stagings {
		if offered[sib] {
			continue
		}
		logger.Debug("sibling blob staged but its offer was not sent; dropping it",
			slog.String("sibling_peer_id", sib),
			slog.String("msg_id", msgID),
			slog.String("staging_id", st.stagingID),
		)
		dropSiblingBlob(ctx, d, st.stagingID)
		zeroBytes(st.key)
		zeroBytes(st.nonce)
		delete(stagings, sib)
		dropped++
	}
	if dropped == 0 {
		return
	}
	remaining := siblingStagingIDs(stagings)
	err := d.files.UpdateMeta(msgID, func(m *files.Metadata) error {
		metaCheckpoint("drop_unoffered", msgID)
		m.SiblingStagingIDs = remaining
		m.UpdatedAt = time.Now().Unix()
		return nil
	})
	if err != nil {
		logger.Warn("persist sibling staging ids after dropping unoffered blobs failed",
			slog.String("msg_id", msgID),
			slog.Any("err", err),
		)
	}
}

func wipeSiblingStagings(stagings map[string]siblingStaging) {
	for _, st := range stagings {
		zeroBytes(st.key)
		zeroBytes(st.nonce)
	}
}

func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

func renameWithExt(name, newExt string) string {
	if newExt == "" || name == "" {
		return name
	}
	stem := name
	if dot := strings.LastIndex(name, "."); dot > 0 {
		stem = name[:dot]
	}
	return stem + "." + newExt
}
