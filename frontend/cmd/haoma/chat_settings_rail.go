package main

import (
	"context"
	"log/slog"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/syncrail"
)

func pushChatSettings(ctx context.Context, d *daemon, chatID chat.ChatID) {
	if d == nil || d.chats == nil || chatID == "" {
		return
	}
	sibs := d.siblings()
	if len(sibs) == 0 {

		return
	}
	snap, ok := chatSettingsSnapshot(d, chatID)
	if !ok {
		return
	}
	go func() {
		for _, sib := range sibs {
			if ctx.Err() != nil {
				return
			}

			if err := sendGapEnvelope(ctx, d, sib, syncrail.KindChatSettings, syncrail.ChatSettingsBody{Chat: snap}); err != nil {

				slog.Debug("chat settings: push failed; the sweep will carry it",
					slog.String("sibling_peer_id", sib),
					slog.String("chat_id", string(chatID)),
					slog.Any("err", err),
				)
				continue
			}
			slog.Debug("chat settings: pushed to sibling",
				slog.String("sibling_peer_id", sib),
				slog.String("chat_id", string(chatID)),
			)
		}
	}()
}

func chatSettingsSnapshot(d *daemon, chatID chat.ChatID) (syncrail.ChatSnapshot, bool) {
	c, err := d.chats.Get(chatID)
	if err != nil {
		slog.Debug("chat settings: conversation not readable for the push",
			slog.String("chat_id", string(chatID)),
			slog.Any("err", err),
		)
		return syncrail.ChatSnapshot{}, false
	}
	dc, isDirect := c.(*chat.DirectChat)
	if !isDirect {

		return syncrail.ChatSnapshot{}, false
	}
	fp := fingerprintForChat(d, dc.ID)
	if fp == "" {
		slog.Debug("chat settings: conversation names no person; not pushed",
			slog.String("chat_id", string(chatID)),
		)
		return syncrail.ChatSnapshot{}, false
	}
	stamps := dc.Stamps()
	return syncrail.ChatSnapshot{
		ChatID:           string(dc.ID),
		Fingerprint:      fp,
		RetentionSeconds: dc.RetentionTTL,
		RetentionAt:      dc.LastTimerChangeTs,
		Muted:            dc.NotificationsMuted,
		ReceiptsDisabled: dc.DisableReadReceipts,
		NickOverride:     dc.NickOverride,
		SettingsAt: syncrail.SettingsStamps{
			Muted:        stamps.Muted,
			Receipts:     stamps.Receipts,
			NickOverride: stamps.NickOverride,
		},
	}, true
}

func handleChatSettingsPush(ctx context.Context, d *daemon, siblingPeerID string, body syncrail.ChatSettingsBody) {
	logger := slog.With(
		slog.String("sibling_peer_id", siblingPeerID),
		slog.String("fingerprint", body.Chat.Fingerprint),
	)
	chatID, _, err := resolveChatByFingerprint(d, body.Chat.Fingerprint)
	if err != nil {
		logger.Debug("chat settings: no local conversation for this person; the sweep will adopt it",
			slog.String("reason", railResolveDropReason(err)),
		)
		return
	}

	mergeChatSettings(d, logger, chatID, body.Chat)

	broadcastChatSettings(d, chatID)
}

func broadcastChatSettings(d *daemon, chatID chat.ChatID) {
	if d == nil || d.ipcSrv == nil || d.chats == nil {
		return
	}
	c, err := d.chats.Get(chatID)
	if err != nil {
		return
	}
	push(d.ipcSrv, ipc.FrameChatSettings, "", ipc.ChatSettingsPayload{
		ChatID:              string(chatID),
		RetentionTTL:        c.Retention(),
		DisableReadReceipts: c.ReadReceiptsDisabled(),
		NotificationsMuted:  c.IsNotificationsMuted(),
		NickOverride:        c.NickOverrideValue(),
	})
}
