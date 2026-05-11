package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/peerstate"
)

func (sd *sessionDispatcher) handleListChats(_ context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle list_chats")
	if sd.d.chats == nil {
		sendError(sess, f.ID, "not_ready", "chat store not wired")
		return
	}
	chats, err := sd.d.chats.List()
	if err != nil {
		sendError(sess, f.ID, "internal", err.Error())
		return
	}
	entries := make([]ipc.ChatEntry, 0, len(chats))
	for _, c := range chats {
		entry := chatToEntry(c)
		applyChatLabel(sd.d, &entry)
		applyChatPresenceSnapshot(sd.d, &entry)
		entries = append(entries, entry)
	}
	resp, err := ipc.NewFrame(ipc.FrameChatsListed, f.ID, ipc.ChatsListedResponse{Chats: entries})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send chats_listed frame failed", slog.Any("err", err))
	}
}

func (sd *sessionDispatcher) handleChatAction(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle chat_action")
	var req ipc.ChatActionRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	if req.ChatID == "" {
		sendError(sess, f.ID, "bad_request", "chat_id required")
		return
	}
	if sd.d.chats == nil || sd.d.events == nil {
		sendError(sess, f.ID, "not_ready", "chat store or events log not wired")
		return
	}

	chatID := chat.ChatID(req.ChatID)
	c, err := sd.d.chats.Get(chatID)
	if err != nil {
		if errors.Is(err, chat.ErrNotFound) {
			sendError(sess, f.ID, "not_found", fmt.Sprintf("no chat %q", req.ChatID))
			return
		}
		sendError(sess, f.ID, "internal", err.Error())
		return
	}
	snapshot := chatToEntry(c)
	applyChatLabel(sd.d, &snapshot)

	var deleted int
	switch req.Action {
	case ipc.ChatActionClear:
		n, err := sd.d.events.DeleteByChat(chatID)
		if err != nil {
			sendError(sess, f.ID, "internal", fmt.Sprintf("clear events: %v", err))
			return
		}
		deleted = n

	case ipc.ChatActionDelete:

		n, err := sd.d.events.DeleteByChat(chatID)
		if err != nil {
			sendError(sess, f.ID, "internal", fmt.Sprintf("clear events: %v", err))
			return
		}
		deleted = n
		if err := sd.d.chats.Delete(chatID); err != nil {
			sendError(sess, f.ID, "internal", fmt.Sprintf("drop chat: %v", err))
			return
		}

	default:
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("unknown action %q", req.Action))
		return
	}

	slog.Info("chat action applied",
		slog.String("chat_id", req.ChatID),
		slog.String("action", string(req.Action)),
		slog.Int("deleted_count", deleted),
	)
	resp, err := ipc.NewFrame(ipc.FrameChatActionApplied, f.ID, ipc.ChatActionAppliedResponse{
		Chat:         snapshot,
		Action:       req.Action,
		DeletedCount: deleted,
	})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send chat_action_applied frame failed", slog.Any("err", err))
	}

	switch req.Action {
	case ipc.ChatActionClear:
		push(sd.d.ipcSrv, ipc.FrameChatCleared, "", ipc.ChatClearedPayload{
			ChatID:       req.ChatID,
			DeletedCount: deleted,
		})
		slog.Debug("chat.cleared broadcast",
			slog.String("chat_id", req.ChatID),
			slog.Int("deleted_count", deleted),
		)
	case ipc.ChatActionDelete:
		push(sd.d.ipcSrv, ipc.FrameChatDeleted, "", ipc.ChatDeletedPayload{
			ChatID:       req.ChatID,
			DeletedCount: deleted,
		})
		slog.Debug("chat.deleted broadcast",
			slog.String("chat_id", req.ChatID),
			slog.Int("deleted_count", deleted),
		)

		if snapshot.Kind == ipc.ChatKindDirect && snapshot.PeerID != "" {
			pushPeerMetaUpdated(ctx, sd.d, snapshot.PeerID)
		}
	}
}

func (sd *sessionDispatcher) handleEnsureChat(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("handle ensure_chat")
	var req ipc.EnsureChatRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, "bad_request", fmt.Sprintf("decode payload: %v", err))
		return
	}
	if req.PeerID == "" {
		sendError(sess, f.ID, "bad_request", "peer_id required")
		return
	}
	if sd.d.chats == nil || sd.d.backendClient == nil {
		sendError(sess, f.ID, "not_ready", "chat store or backend client not wired")
		return
	}
	if _, err := resolveChatForPeer(ctx, sd.d, req.PeerID); err != nil {
		sendError(sess, f.ID, "internal", fmt.Sprintf("resolve chat: %v", err))
		return
	}
	dc, err := sd.d.chats.GetByDirectPeer(req.PeerID)
	if err != nil {
		sendError(sess, f.ID, "internal", fmt.Sprintf("post-resolve chat lookup: %v", err))
		return
	}
	pp, err := sd.d.backendClient.Peer(ctx, req.PeerID)
	if err != nil {
		sendError(sess, f.ID, "backend_unavailable", fmt.Sprintf("fetch peer: %v", err))
		return
	}
	peerEntry := peerEntryFor(sd.d, pp)
	chatEntry := chatToEntry(dc)
	applyChatLabel(sd.d, &chatEntry)
	applyChatPresenceSnapshot(sd.d, &chatEntry)
	resp, err := ipc.NewFrame(ipc.FrameChatEnsured, f.ID, ipc.ChatEnsuredResponse{
		Peer: peerEntry,
		Chat: chatEntry,
	})
	if err != nil {
		sendError(sess, f.ID, "encode_frame", err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("send chat_ensured frame failed", slog.Any("err", err))
	}
}

func applyChatPresenceSnapshot(d *daemon, entry *ipc.ChatEntry) {
	if d == nil || d.presenceCache == nil || entry == nil {
		return
	}
	if entry.Kind != ipc.ChatKindDirect || entry.PeerID == "" {
		return
	}
	snap := d.presenceCache.Snapshot(entry.PeerID)
	entry.Accepting = snap.Accepting
	entry.Chatty = snap.Chatty
	entry.Effective = snap.Effective
}

func applyChatLabel(d *daemon, entry *ipc.ChatEntry) {
	if entry == nil {
		return
	}
	switch entry.Kind {
	case ipc.ChatKindDirect:
		if entry.PeerID == "" {
			return
		}
		var rec peerstate.MetaRecord
		if d != nil && d.peerMeta != nil {
			if got, err := d.peerMeta.Get(entry.PeerID); err == nil {
				rec = got
			}
		}
		entry.Label = peerstate.Resolve(rec, entry.PeerID)
	case ipc.ChatKindGroup:
		if entry.GroupAlias != "" {
			entry.Label = entry.GroupAlias
		} else {
			entry.Label = entry.GroupName
		}
	}
}

func chatToEntry(c chat.Chat) ipc.ChatEntry {
	switch v := c.(type) {
	case *chat.DirectChat:
		return ipc.ChatEntry{
			ChatID:              string(v.ID),
			Kind:                ipc.ChatKindDirect,
			PeerID:              v.PeerID,
			RetentionTTL:        v.RetentionTTL,
			DisableReadReceipts: v.DisableReadReceipts,
			NotificationsMuted:  v.NotificationsMuted,
			Members:             append([]string(nil), v.Members...),
			CreatedAt:           v.CreatedAt,
			LastActivityAt:      v.LastActivityAt,
			UnreadCount:         v.UnreadCount,
			LastTimerChangeTs:   v.LastTimerChangeTs,
			GroupName:           v.GroupName,
			GroupAlias:          v.GroupAlias,
		}
	case *chat.GroupChat:
		return ipc.ChatEntry{
			ChatID:              string(v.ID),
			Kind:                ipc.ChatKindGroup,
			GroupName:           v.GroupName,
			GroupAlias:          v.GroupAlias,
			RetentionTTL:        v.RetentionTTL,
			DisableReadReceipts: v.DisableReadReceipts,
			NotificationsMuted:  v.NotificationsMuted,
			Members:             append([]string(nil), v.Members...),
			CreatedAt:           v.CreatedAt,
			LastActivityAt:      v.LastActivityAt,
			UnreadCount:         v.UnreadCount,
			LastTimerChangeTs:   v.LastTimerChangeTs,
		}
	default:
		return ipc.ChatEntry{}
	}
}
