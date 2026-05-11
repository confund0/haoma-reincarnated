package main

import (
	"context"
	"errors"
	"log/slog"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/notify"
	"haoma-frontend/internal/peerstate"
)

func emitInboundNotification(ctx context.Context, d *daemon, chatID chat.ChatID, peerID, body string) {
	if d == nil || d.notifier == nil || chatID == "" {
		return
	}
	snap := d.settingsSnapshot.Load()
	if snap == nil {
		slog.Debug("notify suppressed: settings snapshot not yet populated",
			slog.String("chat_id", string(chatID)),
		)
		return
	}
	if !snap.NotifyShellEnabled {
		slog.Debug("notify suppressed: global NotifyShellEnabled=false",
			slog.String("chat_id", string(chatID)),
		)
		return
	}
	if chatIsFocused(d, chatID) {
		slog.Debug("notify suppressed: chat is focused",
			slog.String("chat_id", string(chatID)),
		)
		return
	}
	if d.clientSoftLocked.Load() && !snap.NotificationsOnLock {
		slog.Debug("notify suppressed: soft-locked + NotificationsOnLock=false",
			slog.String("chat_id", string(chatID)),
		)
		return
	}

	muted, err := chatNotificationsMuted(d, chatID)
	if err != nil {
		slog.Warn("notify: chat mute lookup failed; defaulting to NOT muted",
			slog.String("chat_id", string(chatID)),
			slog.Any("err", err),
		)
	} else if muted {
		slog.Debug("notify suppressed: per-chat NotificationsMuted=true",
			slog.String("chat_id", string(chatID)),
		)
		return
	}

	peerLabel := resolvePeerLabel(d, peerID)
	ev := notify.Event{
		ChatID:    string(chatID),
		PeerLabel: peerLabel,
		Body:      body,
	}
	priv := notify.Privacy{
		ShellEnabled: snap.NotifyShellEnabled,
		ShowSender:   snap.NotifyShowSender,
		ShowBody:     snap.NotifyShowBody,
	}
	dec := d.notifier.Notify(ctx, ev, priv)
	if !dec.Fired {
		return
	}
	slog.Info("notification fired",
		slog.String("chat_id", string(chatID)),
		slog.Bool("show_sender", snap.NotifyShowSender),
		slog.Bool("show_body", snap.NotifyShowBody),
	)
	if d.ipcSrv != nil {
		push(d.ipcSrv, ipc.FrameNotificationEmitted, "", ipc.NotificationEmittedPayload{
			ChatID:         string(chatID),
			PeerLabel:      peerLabel,
			Title:          dec.Title,
			Body:           dec.Body,
			RedactedSender: dec.RedactedSender,
			RedactedBody:   dec.RedactedBody,
		})
	}
}

func chatNotificationsMuted(d *daemon, chatID chat.ChatID) (bool, error) {
	if d.chats == nil {
		return false, nil
	}
	c, err := d.chats.Get(chatID)
	if err != nil {
		if errors.Is(err, chat.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return c.IsNotificationsMuted(), nil
}

func resolvePeerLabel(d *daemon, peerID string) string {
	if d == nil || peerID == "" {
		return ""
	}
	var rec peerstate.MetaRecord
	if d.peerMeta != nil {
		if got, err := d.peerMeta.Get(peerID); err == nil {
			rec = got
		}
	}
	return peerstate.Resolve(rec, peerID)
}
