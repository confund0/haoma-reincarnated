package main

import (
	"context"
	"log/slog"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/ipc"
)

func bumpChatActivity(ctx context.Context, d *daemon, chatID chat.ChatID, ts int64) {
	if d == nil || d.chats == nil || chatID == "" {
		return
	}
	changed, err := d.chats.BumpActivity(chatID, ts)
	if err != nil {
		slog.Warn("BumpActivity failed",
			slog.String("chat_id", string(chatID)),
			slog.Any("err", err),
		)
		return
	}
	if !changed {
		return
	}
	push(d.ipcSrv, ipc.FrameChatActivityChanged, "", ipc.ChatActivityChangedPayload{
		ChatID:         string(chatID),
		LastActivityAt: ts,
	})
	_ = ctx
}

func incrementChatUnread(ctx context.Context, d *daemon, chatID chat.ChatID) {
	if d == nil || d.chats == nil || chatID == "" {
		return
	}
	count, err := d.chats.IncrementUnread(chatID)
	if err != nil {
		slog.Warn("IncrementUnread failed",
			slog.String("chat_id", string(chatID)),
			slog.Any("err", err),
		)
		return
	}
	push(d.ipcSrv, ipc.FrameChatUnreadChanged, "", ipc.ChatUnreadChangedPayload{
		ChatID:      string(chatID),
		UnreadCount: count,
	})
	_ = ctx
}

func chatIsFocused(d *daemon, chatID chat.ChatID) bool {
	if d == nil {
		return false
	}
	focus := d.clientFocus.Load()
	if focus == nil {
		return false
	}
	return focus.ChatID != "" && chat.ChatID(focus.ChatID) == chatID && focus.ScrollPosition == 0
}

func clearChatUnread(ctx context.Context, d *daemon, chatID chat.ChatID) {
	if d == nil || d.chats == nil || chatID == "" {
		return
	}
	changed, err := d.chats.ClearUnread(chatID)
	if err != nil {
		slog.Warn("ClearUnread failed",
			slog.String("chat_id", string(chatID)),
			slog.Any("err", err),
		)
		return
	}
	if !changed {
		return
	}
	push(d.ipcSrv, ipc.FrameChatUnreadChanged, "", ipc.ChatUnreadChangedPayload{
		ChatID:      string(chatID),
		UnreadCount: 0,
	})
	_ = ctx
}
