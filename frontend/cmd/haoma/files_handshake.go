package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/files"
	"haoma-frontend/internal/mirror"
	"haoma-frontend/internal/msg"
)

func ingestFileReceipt(ctx context.Context, d *daemon, fromPeerID string, body *msg.FileReceiptBody, receiptMsgID string) {
	logger := slog.With(
		slog.String("from_peer_id", fromPeerID),
		slog.String("receipt_msg_id", receiptMsgID),
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
	logger = logger.With(slog.String("msg_id", meta.MsgID))
	if meta.Direction != files.DirOut {
		logger.Warn("file receipt: metadata is not outbound; dropping",
			slog.String("direction", string(meta.Direction)),
		)
		return
	}
	bound, ok := meta.RecipientTokens[fromPeerID]
	if !ok || bound != body.Token {
		logger.Warn("file receipt: peer is not bound recipient for token; dropping")
		return
	}
	if len(meta.KeyBytes) == 0 || len(meta.Nonce) == 0 {
		logger.Warn("file receipt: outbound metadata missing key/nonce; cannot ship key")
		return
	}
	logger.Debug("file receipt: received from a device the file was offered to")
	if earlyReceiptIgnored(d, logger, meta.MsgID, fromPeerID) {
		return
	}

	if earlier := meta.KeyEnvelopes[fromPeerID]; earlier != "" {
		if onItsWay, why := keyAlreadyOnItsWay(ctx, d, earlier); onItsWay {
			logger.Debug("file receipt: key already sent to this device; not sent again",
				slog.String("key_envelope_id", earlier),
				slog.String("reason", why),
				slog.String("outcome", "key_not_resent"),
			)
			return
		} else {
			logger.Debug("file receipt: the earlier key did not arrive; sending it again",
				slog.String("key_envelope_id", earlier),
				slog.String("reason", why),
			)
		}
	}

	envelopeID, shipped := d.shipFileKey(ctx, fromPeerID, meta.MsgID, receiptMsgID, body.Token, meta.KeyBytes, meta.Nonce)
	if !shipped {

		return
	}
	recordKeyEnvelope(d, logger, meta.MsgID, fromPeerID, envelopeID)

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
			logger.Warn("file receipt: haomad reported recipient mismatch (protocol drift)")
		case errors.Is(err, backendapi.ErrFileReceiptTokenUnknown):
			logger.Warn("file receipt: haomad reports token unknown (already swept?)")
		default:
			logger.Warn("file receipt: haomad decrement failed", slog.Any("err", err))
		}
		return
	}
	logger.Info("file receipt processed; key shipped + receipt verified")
}

func keyAlreadyOnItsWay(ctx context.Context, d *daemon, envelopeID string) (bool, string) {
	if d.backendClient == nil {
		return false, "no_backend"
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	row, err := d.backendClient.OutboxEntry(lookupCtx, envelopeID)
	switch {
	case errors.Is(err, backendapi.ErrOutboxEntryNotFound):
		return false, "key_envelope_gone"
	case err != nil:
		return false, "outbox_unreadable"
	case row.State == "enqueued":
		return true, "key_envelope_enqueued"
	default:
		return false, "key_envelope_" + row.State
	}
}

func recordKeyEnvelope(d *daemon, logger *slog.Logger, msgID, deviceID, envelopeID string) {

	err := d.files.UpdateMeta(msgID, func(m *files.Metadata) error {
		metaCheckpoint("record_key", msgID)
		if m.KeyEnvelopes == nil {
			m.KeyEnvelopes = map[string]string{}
		}
		m.KeyEnvelopes[deviceID] = envelopeID
		m.UpdatedAt = time.Now().Unix()
		return nil
	})
	if errors.Is(err, files.ErrMetaNotFound) {
		logger.Debug("file receipt: the file was deleted after its key was sent; nothing recorded")
		return
	}
	if err != nil {
		logger.Warn("file receipt: recording the key envelope failed; a second receipt will send the key again", slog.Any("err", err))
		return
	}
	logger.Debug("file receipt: key envelope recorded for the device",
		slog.String("key_envelope_id", envelopeID),
	)
}

func (d *daemon) shipFileKey(ctx context.Context, peerID, offerMsgID, receiptMsgID, token string, keyBytes, nonce []byte) (string, bool) {
	if d.cipher == nil || d.peerSeq == nil || d.backendClient == nil {
		slog.Warn("ship file key: wiring incomplete; skipping",
			slog.String("peer_id", peerID),
		)
		return "", false
	}
	seq, err := d.peerSeq.NextSendSeq(peerID)
	if err != nil {
		slog.Warn("ship file key: next seq failed",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return "", false
	}
	keyMsgID, err := msg.NewID()
	if err != nil {
		slog.Warn("ship file key: new id failed", slog.Any("err", err))
		return "", false
	}
	wrapper, err := msg.BuildFileKey(seq, time.Now().Unix(), keyMsgID, token, keyBytes, nonce, 0)
	if err != nil {
		slog.Warn("ship file key: build wrapper failed", slog.Any("err", err))
		return "", false
	}
	plaintext, err := msg.Marshal(wrapper)
	if err != nil {
		slog.Warn("ship file key: marshal failed", slog.Any("err", err))
		return "", false
	}
	blob, err := d.cipher.Encrypt(ctx, peerID, plaintext)
	if err != nil {
		slog.Warn("ship file key: encrypt failed",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return "", false
	}
	result, err := mirror.Dispatch(ctx, d.backendClient, mirror.NewRail(d.railKey, d.siblings, d.peerMeta),
		mirror.SingleTarget(peerID),
		func(ctx context.Context, deviceID string) (backendapi.SendRequest, []byte, error) {
			return backendapi.SendRequest{
				PeerID:         deviceID,
				Payload:        blob,
				PresenceSource: backendapi.PresenceSourceHaoma,
			}, plaintext, nil
		},
		mirror.NewOp(wrapper.Kind, ""))
	if err != nil {
		slog.Warn("ship file key: backend send failed",
			slog.String("peer_id", peerID),
			slog.String("msg_id", offerMsgID),
			slog.Any("err", err),
		)
		return "", false
	}

	slog.Debug("file key shipped",
		slog.String("peer_id", peerID),
		slog.String("msg_id", offerMsgID),
		slog.String("receipt_msg_id", receiptMsgID),
		slog.String("envelope_id", result.Primary.EnvelopeID),
		slog.String("key_msg_id", keyMsgID),
		slog.Uint64("sender_seq", seq),
	)
	return result.Primary.EnvelopeID, true
}

func ingestFileKey(ctx context.Context, d *daemon, fromPeerID, token string, key, nonce []byte, keyMsgID string) {
	logger := slog.With(
		slog.String("from_peer_id", fromPeerID),
		slog.String("key_msg_id", keyMsgID),
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
			if dc, ok := c.(*chat.DirectChat); ok {
				member := false
				for _, pid := range dc.FanoutPeerIDs() {
					if pid == fromPeerID {
						member = true
						break
					}
				}
				if !member {
					logger.Warn("file key: sender does not route to this chat; dropping",
						slog.String("msg_id", meta.MsgID),
						slog.Int("chat_routes", len(dc.FanoutPeerIDs())),
					)
					return
				}
			}
		}
	}

	err = d.files.UpdateMeta(meta.MsgID, func(m *files.Metadata) error {
		m.KeyBytes = append([]byte(nil), key...)
		m.Nonce = append([]byte(nil), nonce...)
		m.UpdatedAt = time.Now().Unix()
		return nil
	})
	if err != nil {
		logger.Warn("file key: persist metadata failed", slog.String("msg_id", meta.MsgID), slog.Any("err", err))
		return
	}

	arrival := "file key: arrived before the bytes"
	if stagingExists(d, meta.MsgID) {
		arrival = "file key: arrived after the bytes"
	}
	logger.Debug(arrival,
		slog.String("msg_id", meta.MsgID),
		slog.String("state", string(meta.State)),
	)
	logger.Debug("file key persisted; converging",
		slog.String("msg_id", meta.MsgID),
		slog.String("state", string(meta.State)),
	)
	convergeFileReady(ctx, d, meta.MsgID)
}

func convergeFileReady(ctx context.Context, d *daemon, msgID string) string {
	if d.files == nil || msgID == "" {
		return "no_files_manager"
	}

	unlock := d.fileSealLocks.lock(msgID)
	defer unlock()

	meta, err := d.files.GetMeta(msgID)
	if err != nil {
		if !errors.Is(err, files.ErrMetaNotFound) {
			slog.Warn("file converge: metadata lookup failed",
				slog.String("msg_id", msgID),
				slog.Any("err", err),
			)
			return "meta_unreadable"
		}
		return "no_row"
	}
	switch meta.State {
	case files.StateReady, files.StateFailedPermanent, files.StateExpired:
		return "settled"
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
	metaCheckpoint("converge_read", msgID)

	switch {
	case haveStaging && haveKey:
		logger.Debug("file converge: both halves in hand; sealing")
		sealedPath, err := d.files.DecryptSealMove(meta.ChatID, msgID, meta.KeyBytes, meta.Nonce)
		if err != nil {
			if errors.Is(err, files.ErrAEADOpen) {
				logger.Warn("file converge: AEAD open failed (failed_permanent)", slog.Any("err", err))
				stampFileEventState(d, meta.ChatID, msgID, files.StateFailedPermanent, meta.Size, "decrypt failed")

				_ = d.files.UpdateMeta(msgID, func(m *files.Metadata) error {
					m.KeyBytes = nil
					m.Nonce = nil
					m.State = files.StateFailedPermanent
					m.UpdatedAt = time.Now().Unix()
					return nil
				})
				return convergeDecryptFailed
			}
			logger.Warn("file converge: seal-move failed (transient)", slog.Any("err", err))
			stampFileEventState(d, meta.ChatID, msgID, files.StateFailedTransient, meta.Size, err.Error())
			return "seal_failed"
		}
		metaCheckpoint("converge_sealed", msgID)

		err = d.files.UpdateMeta(msgID, func(m *files.Metadata) error {
			m.SealedPath = sealedPath
			m.State = files.StateReady
			m.KeyBytes = nil
			m.Nonce = nil
			m.UpdatedAt = time.Now().Unix()
			return nil
		})
		if err != nil {
			logger.Warn("file converge: persist post-seal metadata failed", slog.Any("err", err))
			return "seal_not_recorded"
		}
		stampFileEventState(d, meta.ChatID, msgID, files.StateReady, meta.Size, "")
		logger.Info("file ready (sealed at rest)")

		if peerID := resolveOfferPeer(d, msgID); peerID != "" {
			d.shipFileDelivered(ctx, peerID, msgID)
		} else {
			logger.Warn("file delivered answer skipped: no device to address")
		}

		d.autoMarkOnArrival(ctx, meta.ChatID)
		return "sealed"
	case haveStaging && !haveKey:

		if meta.State != files.StateAwaitingKey {
			err := d.files.UpdateMeta(msgID, func(m *files.Metadata) error {
				switch m.State {
				case files.StateReady, files.StateFailedPermanent, files.StateExpired:
					return errSettledMeanwhile
				}
				m.State = files.StateAwaitingKey
				m.UpdatedAt = time.Now().Unix()
				return nil
			})
			if err != nil {
				logger.Debug("file converge: awaiting_key not recorded", slog.Any("err", err))
				return "awaiting_key_not_recorded"
			}
			stampFileEventState(d, meta.ChatID, msgID, files.StateAwaitingKey, meta.Size, "")
		}
		logger.Debug("file converge: staging in hand; awaiting key")
		return "awaiting_key"
	case !haveStaging && haveKey:

		logger.Debug("file converge: key in hand; awaiting staging blob")
		return "awaiting_bytes"
	default:
		logger.Debug("file converge: nothing in hand; nothing to do")
		return "nothing_in_hand"
	}
}

var errSettledMeanwhile = errors.New("file row settled meanwhile")

type keyedLocks struct {
	mu    sync.Mutex
	locks map[string]*keyedLock
}

type keyedLock struct {
	mu   sync.Mutex
	refs int
}

func (k *keyedLocks) lock(key string) func() {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = map[string]*keyedLock{}
	}
	l := k.locks[key]
	if l == nil {
		l = &keyedLock{}
		k.locks[key] = l
	}
	l.refs++
	k.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		k.mu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}

func (d *daemon) shipFileDelivered(ctx context.Context, peerID, offerMsgID string) {
	if d.cipher == nil || d.peerSeq == nil || d.backendClient == nil {
		slog.Warn("ship file delivered: wiring incomplete; skipping",
			slog.String("peer_id", peerID),
		)
		return
	}
	seq, err := d.peerSeq.NextSendSeq(peerID)
	if err != nil {
		slog.Warn("ship file delivered: next seq failed",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return
	}
	rcptMsgID, err := msg.NewID()
	if err != nil {
		slog.Warn("ship file delivered: new id failed", slog.Any("err", err))
		return
	}
	wrapper, err := msg.BuildFileDelivered(seq, time.Now().Unix(), rcptMsgID, offerMsgID, 0)
	if err != nil {
		slog.Warn("ship file delivered: build wrapper failed", slog.Any("err", err))
		return
	}
	plaintext, err := msg.Marshal(wrapper)
	if err != nil {
		slog.Warn("ship file delivered: marshal failed", slog.Any("err", err))
		return
	}
	blob, err := d.cipher.Encrypt(ctx, peerID, plaintext)
	if err != nil {
		slog.Warn("ship file delivered: encrypt failed",
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return
	}
	result, err := mirror.Dispatch(ctx, d.backendClient, mirror.NewRail(d.railKey, d.siblings, d.peerMeta),
		mirror.SingleTarget(peerID),
		func(ctx context.Context, deviceID string) (backendapi.SendRequest, []byte, error) {
			return backendapi.SendRequest{
				PeerID:         deviceID,
				Payload:        blob,
				PresenceSource: backendapi.PresenceSourceHaoma,
			}, plaintext, nil
		},
		mirror.NewOp(wrapper.Kind, ""))
	if err != nil {
		slog.Warn("ship file delivered: backend send failed",
			slog.String("peer_id", peerID),
			slog.String("offer_msg_id", offerMsgID),
			slog.Any("err", err),
		)
		return
	}
	slog.Debug("file delivered receipt shipped",
		slog.String("peer_id", peerID),
		slog.String("offer_msg_id", offerMsgID),
		slog.String("envelope_id", result.Primary.EnvelopeID),
		slog.String("receipt_msg_id", rcptMsgID),
		slog.Uint64("sender_seq", seq),
	)
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
