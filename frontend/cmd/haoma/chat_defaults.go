package main

import (
	"errors"
	"fmt"
	"log/slog"

	"haoma-frontend/internal/chat"
)

func (d *daemon) applyChatDefaults(chatID chat.ChatID) {
	retention := d.defaultRetentionSec()
	sendReceipts := d.defaultSendReceipts()
	if err := d.chats.SetRetentionTTL(chatID, uint32(retention)); err != nil {
		slog.Warn("apply chat default retention failed",
			slog.String("chat_id", string(chatID)),
			slog.Any("err", err),
		)
	}

	if err := d.chats.SetDisableReadReceipts(chatID, !sendReceipts); err != nil {
		slog.Warn("apply chat default receipts failed",
			slog.String("chat_id", string(chatID)),
			slog.Any("err", err),
		)
	}
	slog.Debug("chat defaults applied",
		slog.String("chat_id", string(chatID)),
		slog.Uint64("retention_sec", retention),
		slog.Bool("send_receipts", sendReceipts),
	)
}

func (d *daemon) createDirectWithDefaults(peerID string) (created *chat.DirectChat, fresh bool, err error) {
	if d.chats == nil {
		return nil, false, fmt.Errorf("createDirectWithDefaults: chat store not wired")
	}
	existing, getErr := d.chats.GetByDirectPeer(peerID)
	if getErr == nil {
		return existing, false, nil
	}
	if !errors.Is(getErr, chat.ErrNotFound) {
		return nil, false, fmt.Errorf("createDirectWithDefaults: probe: %w", getErr)
	}
	if folded, ok := d.foldByFingerprint(peerID); ok {

		return folded, false, nil
	}
	dc, cErr := d.chats.CreateDirect(peerID)
	if cErr != nil {
		return nil, false, cErr
	}
	d.applyChatDefaults(dc.ID)
	return dc, true, nil
}

func (d *daemon) foldByFingerprint(peerID string) (*chat.DirectChat, bool) {
	if d.peerMeta == nil || d.chats == nil {
		return nil, false
	}
	rec, err := d.peerMeta.Get(peerID)
	if err != nil || rec.Fingerprint == "" {

		slog.Debug("fold: no fingerprint for this peer yet — filing as a new chat",
			slog.String("peer_id", peerID),
		)
		return nil, false
	}

	if edgeless, err := d.chats.GetByFingerprint(rec.Fingerprint); err == nil {
		if err := d.chats.AttachPeerID(edgeless.ID, peerID); err != nil {
			slog.Warn("fold: attaching the new edge to a backfilled chat failed",
				slog.String("chat_id", string(edgeless.ID)),
				slog.String("peer_id", peerID),
				slog.Any("err", err),
			)
			return nil, false
		}
		slog.Info("fold: a contact known only from my other device now has an edge here",
			slog.String("chat_id", string(edgeless.ID)),
			slog.String("peer_id", peerID),
			slog.Bool("was_edgeless", len(edgeless.FanoutPeerIDs()) == 0),
		)
		folded, err := d.chats.GetByDirectPeer(peerID)
		if err != nil {
			return nil, false
		}
		d.pushChatPresence(folded.ID, "pairing joined")
		return folded, true
	} else if !errors.Is(err, chat.ErrNotFound) {
		slog.Warn("fold: chat-by-person lookup failed", slog.Any("err", err))
		return nil, false
	}

	siblings, err := d.peerMeta.PeerIDsByFingerprint(rec.Fingerprint)
	if err != nil {
		slog.Warn("fold: fingerprint lookup failed", slog.Any("err", err))
		return nil, false
	}
	for _, other := range siblings {
		if other == peerID {
			continue
		}
		dc, err := d.chats.GetByDirectPeer(other)
		if err != nil {
			continue
		}
		if err := d.chats.AttachPeerID(dc.ID, peerID); err != nil {
			slog.Warn("fold: attaching the new peer to its person's chat failed",
				slog.String("chat_id", string(dc.ID)),
				slog.String("peer_id", peerID),
				slog.Any("err", err),
			)
			return nil, false
		}
		slog.Info("fold: a second device for a known contact joined their existing chat",
			slog.String("chat_id", string(dc.ID)),
			slog.String("peer_id", peerID),
			slog.String("existing_peer_id", other),
		)
		folded, err := d.chats.GetByDirectPeer(peerID)
		if err != nil {
			return nil, false
		}
		d.pushChatPresence(folded.ID, "pairing joined")
		return folded, true
	}
	return nil, false
}
