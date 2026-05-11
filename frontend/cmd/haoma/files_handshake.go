package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/files"
	"haoma-frontend/internal/msg"
)

func shortToken(token string) string {
	if len(token) <= 8 {
		return token
	}
	return token[:8] + "…"
}

func ingestFileReceipt(ctx context.Context, d *daemon, fromPeerID string, body *msg.FileReceiptBody) {
	logger := slog.With(
		slog.String("from_peer_id", fromPeerID),
		slog.String("token", body.Token),
	)
	if d.files == nil {
		logger.Debug("file receipt: files manager not initialised")
		return
	}
	meta, err := d.files.GetMetaByToken(body.Token)
	if errors.Is(err, files.ErrMetaNotFound) {
		logger.Warn("file receipt: no metadata for token; dropping")
		return
	}
	if err != nil {
		logger.Warn("file receipt: metadata lookup failed", slog.Any("err", err))
		return
	}
	if meta.Direction != files.DirOut {
		logger.Warn("file receipt: metadata is not outbound; dropping",
			slog.String("direction", string(meta.Direction)),
			slog.String("msg_id", meta.MsgID),
		)
		return
	}
	bound, ok := meta.RecipientTokens[fromPeerID]
	if !ok || bound != body.Token {
		logger.Warn("file receipt: peer is not bound recipient for token; dropping",
			slog.String("msg_id", meta.MsgID),
		)
		return
	}
	if len(meta.KeyBytes) == 0 || len(meta.Nonce) == 0 {
		logger.Warn("file receipt: outbound metadata missing key/nonce; cannot ship key",
			slog.String("msg_id", meta.MsgID),
		)
		return
	}

	if !d.shipFileKey(ctx, fromPeerID, meta.MsgID, body.Token, meta.KeyBytes, meta.Nonce) {

		return
	}

	redeemCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if d.backendClient == nil {
		logger.Debug("file receipt: no backend client; skip haomad decrement")
		return
	}
	if _, err := d.backendClient.RedeemFileReceipt(redeemCtx, backendapi.RedeemFileReceiptRequest{
		Token:           body.Token,
		RecipientPeerID: fromPeerID,
	}); err != nil {
		switch {
		case errors.Is(err, backendapi.ErrFileReceiptRecipientMismatch):
			logger.Warn("file receipt: haomad reported recipient mismatch (protocol drift)",
				slog.String("msg_id", meta.MsgID),
			)
		case errors.Is(err, backendapi.ErrFileReceiptTokenUnknown):
			logger.Warn("file receipt: haomad reports token unknown (already swept?)",
				slog.String("msg_id", meta.MsgID),
			)
		default:
			logger.Warn("file receipt: haomad decrement failed",
				slog.String("msg_id", meta.MsgID),
				slog.Any("err", err),
			)
		}
		return
	}
	logger.Info("file receipt processed; key shipped + token decremented",
		slog.String("msg_id", meta.MsgID),
	)
}

func (d *daemon) shipFileKey(ctx context.Context, peerID, offerMsgID, token string, keyBytes, nonce []byte) bool {
	if d.cipher == nil || d.peerSeq == nil || d.backendClient == nil {
		slog.Warn("ship file key: wiring incomplete; skipping",
			slog.String("peer_id", peerID),
		)
		return false
	}
	seq, err := d.peerSeq.NextSendSeq(peerID)
	if err != nil {
		slog.Warn("ship file key: next seq failed",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return false
	}
	keyMsgID, err := msg.NewID()
	if err != nil {
		slog.Warn("ship file key: new id failed", slog.Any("err", err))
		return false
	}
	wrapper, err := msg.BuildFileKey(seq, time.Now().Unix(), keyMsgID, token, keyBytes, nonce, 0)
	if err != nil {
		slog.Warn("ship file key: build wrapper failed", slog.Any("err", err))
		return false
	}
	plaintext, err := msg.Marshal(wrapper)
	if err != nil {
		slog.Warn("ship file key: marshal failed", slog.Any("err", err))
		return false
	}
	blob, err := d.cipher.Encrypt(ctx, peerID, plaintext)
	if err != nil {
		slog.Warn("ship file key: encrypt failed",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return false
	}
	resp, err := d.backendClient.Send(ctx, backendapi.SendRequest{
		PeerID:         peerID,
		Payload:        blob,
		PresenceSource: backendapi.PresenceSourceHaoma,
	})
	if err != nil {
		slog.Warn("ship file key: backend send failed",
			slog.String("peer_id", peerID),
			slog.String("token", token),
			slog.Any("err", err),
		)
		return false
	}
	slog.Debug("file key shipped",
		slog.String("peer_id", peerID),
		slog.String("token", token),
		slog.String("offer_msg_id", offerMsgID),
		slog.String("envelope_id", resp.EnvelopeID),
		slog.String("key_msg_id", keyMsgID),
		slog.Uint64("sender_seq", seq),
	)
	return true
}

func ingestFileKey(ctx context.Context, d *daemon, fromPeerID, token string, key, nonce []byte) {
	logger := slog.With(
		slog.String("from_peer_id", fromPeerID),
		slog.String("token", shortToken(token)),
	)
	if d.files == nil {
		logger.Debug("file key: files manager not initialised")
		return
	}
	meta, err := d.files.GetMetaByToken(token)
	if errors.Is(err, files.ErrMetaNotFound) {
		logger.Warn("file key: no metadata for token; dropping (rare reorder OR stranger spam)")
		return
	}
	if err != nil {
		logger.Warn("file key: metadata lookup failed", slog.Any("err", err))
		return
	}
	if meta.Direction != files.DirIn {
		logger.Warn("file key: metadata direction is not inbound; dropping (likely sender's own row)",
			slog.String("msg_id", meta.MsgID),
		)
		return
	}
	if meta.State == files.StateReady || meta.State == files.StateFailedPermanent || meta.State == files.StateExpired {
		logger.Debug("file key: row already in terminal state; dropping",
			slog.String("msg_id", meta.MsgID),
			slog.String("state", string(meta.State)),
		)
		return
	}

	if d.chats != nil {
		c, gErr := d.chats.Get(meta.ChatID)
		if gErr == nil {
			if dc, ok := c.(*chat.DirectChat); ok && dc.PeerID != "" && dc.PeerID != fromPeerID {
				logger.Warn("file key: sender is not the chat peer; dropping",
					slog.String("msg_id", meta.MsgID),
					slog.String("expected_peer_id", dc.PeerID),
				)
				return
			}
		}
	}

	meta.KeyBytes = append(meta.KeyBytes[:0], key...)
	meta.Nonce = append(meta.Nonce[:0], nonce...)
	meta.UpdatedAt = time.Now().Unix()
	if err := d.files.PutMeta(meta); err != nil {
		logger.Warn("file key: persist metadata failed", slog.Any("err", err))
		return
	}
	logger.Debug("file key persisted; converging",
		slog.String("msg_id", meta.MsgID),
		slog.String("state", string(meta.State)),
	)
	convergeFileReady(ctx, d, meta.MsgID)
}

func convergeFileReady(ctx context.Context, d *daemon, msgID string) {
	if d.files == nil || msgID == "" {
		return
	}
	meta, err := d.files.GetMeta(msgID)
	if err != nil {
		if !errors.Is(err, files.ErrMetaNotFound) {
			slog.Warn("file converge: metadata lookup failed",
				slog.String("msg_id", msgID),
				slog.Any("err", err),
			)
		}
		return
	}
	switch meta.State {
	case files.StateReady, files.StateFailedPermanent, files.StateExpired:
		return
	}
	haveStaging := stagingExists(d, msgID)
	haveKey := len(meta.KeyBytes) > 0 && len(meta.Nonce) > 0
	logger := slog.With(
		slog.String("msg_id", msgID),
		slog.String("chat_id", string(meta.ChatID)),
		slog.Bool("staging_present", haveStaging),
		slog.Bool("key_present", haveKey),
		slog.String("state", string(meta.State)),
	)

	switch {
	case haveStaging && haveKey:
		logger.Debug("file converge: both halves in hand; sealing")
		sealedPath, err := d.files.DecryptSealMove(meta.ChatID, msgID, meta.KeyBytes, meta.Nonce)
		if err != nil {
			if errors.Is(err, files.ErrAEADOpen) {
				logger.Warn("file converge: AEAD open failed (failed_permanent)", slog.Any("err", err))
				stampFileEventState(d, meta.ChatID, msgID, files.StateFailedPermanent, meta.Size, "decrypt failed")

				meta.KeyBytes = nil
				meta.Nonce = nil
				meta.State = files.StateFailedPermanent
				meta.UpdatedAt = time.Now().Unix()
				_ = d.files.PutMeta(meta)
				return
			}
			logger.Warn("file converge: seal-move failed (transient)", slog.Any("err", err))
			stampFileEventState(d, meta.ChatID, msgID, files.StateFailedTransient, meta.Size, err.Error())
			return
		}

		meta.SealedPath = sealedPath
		meta.State = files.StateReady
		meta.KeyBytes = nil
		meta.Nonce = nil
		meta.UpdatedAt = time.Now().Unix()
		if err := d.files.PutMeta(meta); err != nil {
			logger.Warn("file converge: persist post-seal metadata failed", slog.Any("err", err))
			return
		}
		stampFileEventState(d, meta.ChatID, msgID, files.StateReady, meta.Size, "")
		logger.Info("file ready (sealed at rest)")
	case haveStaging && !haveKey:

		if meta.State != files.StateAwaitingKey {
			meta.State = files.StateAwaitingKey
			meta.UpdatedAt = time.Now().Unix()
			if err := d.files.PutMeta(meta); err != nil {
				logger.Warn("file converge: persist awaiting_key failed", slog.Any("err", err))
				return
			}
			stampFileEventState(d, meta.ChatID, msgID, files.StateAwaitingKey, meta.Size, "")
		}
		logger.Debug("file converge: staging in hand; awaiting key")
	case !haveStaging && haveKey:

		logger.Debug("file converge: key in hand; awaiting staging blob")
	default:
		logger.Debug("file converge: nothing in hand; nothing to do")
	}
}

func stagingExists(d *daemon, msgID string) bool {
	if d.files == nil {
		return false
	}
	path, err := d.files.StagingPath(msgID)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}
