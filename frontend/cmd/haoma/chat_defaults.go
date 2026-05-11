package main

import (
	"errors"
	"fmt"
	"log/slog"

	"haoma-frontend/internal/chat"
)

func (d *daemon) applyChatDefaults(chatID chat.ChatID) {
	snap := d.settingsSnapshot.Load()
	if snap == nil {
		return
	}
	if err := d.chats.SetRetentionTTL(chatID, uint32(snap.DefaultRetentionSec)); err != nil {
		slog.Warn("apply chat default retention failed",
			slog.String("chat_id", string(chatID)),
			slog.Any("err", err),
		)
	}

	if err := d.chats.SetDisableReadReceipts(chatID, !snap.DefaultSendReceipts); err != nil {
		slog.Warn("apply chat default receipts failed",
			slog.String("chat_id", string(chatID)),
			slog.Any("err", err),
		)
	}
	slog.Debug("chat defaults applied",
		slog.String("chat_id", string(chatID)),
		slog.Uint64("retention_sec", snap.DefaultRetentionSec),
		slog.Bool("send_receipts", snap.DefaultSendReceipts),
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
	dc, cErr := d.chats.CreateDirect(peerID)
	if cErr != nil {
		return nil, false, cErr
	}
	d.applyChatDefaults(dc.ID)
	return dc, true, nil
}
