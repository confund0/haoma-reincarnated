package main

import (
	"errors"
	"log/slog"
	"time"

	"haoma-frontend/internal/chat"
)

func markChatStarted(d *daemon, chatID chat.ChatID) {
	if d == nil || d.chats == nil || chatID == "" {
		return
	}
	c, err := d.chats.Get(chatID)
	if err != nil {
		return
	}
	dc, ok := c.(*chat.DirectChat)
	if !ok || !dc.WaitingToStart() {
		return
	}
	changed, err := d.chats.MarkChatStarted(chatID, time.Now().Unix())
	if errors.Is(err, chat.ErrNotFound) {

		return
	}
	if err != nil {
		slog.Warn("chat: could not record that the chat started",
			slog.String("chat_id", string(chatID)),
			slog.Any("err", err),
		)
		return
	}
	if !changed {
		return
	}
	after, err := directChat(d, chatID)
	if err != nil {
		return
	}
	slog.Info("chat: a message from the contact decrypted; the chat started",
		slog.String("chat_id", string(chatID)),
		slog.Int64("chat_started_at", after.ChatStartedAt),
	)
	pushChatUpdated(d, after)
}

func (d *daemon) createPairedChat(peerID string, holdsSession bool) (*chat.DirectChat, bool, error) {
	dc, fresh, err := d.createDirectWithDefaults(peerID)
	if err != nil || !fresh {
		return dc, fresh, err
	}
	if err := d.chats.SetWaitingForStart(dc.ID, holdsSession); err != nil {
		slog.Warn("chat: could not mark the new chat as waiting to start",
			slog.String("chat_id", string(dc.ID)),
			slog.Any("err", err),
		)
		return dc, fresh, nil
	}
	if after, err := directChat(d, dc.ID); err == nil {
		dc = after
	}
	return dc, fresh, nil
}
