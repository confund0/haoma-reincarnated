package main

import (
	"errors"
	"log/slog"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/presence"
)

func (d *daemon) chatPresence(dc *chat.DirectChat) (presence.Snapshot, bool) {
	if d == nil || d.presenceCache == nil || dc == nil {
		return presence.Snapshot{}, false
	}
	ids := dc.FanoutPeerIDs()
	if len(ids) == 0 {
		return presence.Snapshot{}, false
	}
	snaps := make([]presence.Snapshot, 0, len(ids))
	for _, id := range ids {
		snaps = append(snaps, d.presenceCache.Snapshot(id))
	}
	return presence.Best(snaps), true
}

func (d *daemon) chatOfPeer(peerID string) *chat.DirectChat {
	if d == nil || d.chats == nil || peerID == "" {
		return nil
	}
	dc, err := d.chats.GetByDirectPeer(peerID)
	if err != nil {
		if !errors.Is(err, chat.ErrNotFound) {
			slog.Warn("presence: the chat of a peer could not be read; its chat presence is not pushed",
				slog.String("peer_id", peerID),
				slog.Any("err", err),
			)
		}
		return nil
	}
	return dc
}

func presencePayload(peerID string, snap presence.Snapshot) ipc.PresenceChangedPayload {
	return ipc.PresenceChangedPayload{
		PeerID:    peerID,
		Accepting: snap.Accepting,
		Sending:   snap.Sending,
		Chatty:    snap.Chatty,
		Effective: snap.Effective,
	}
}

func chatPresencePayload(chatID chat.ChatID, snap presence.Snapshot) ipc.ChatPresenceChangedPayload {
	return ipc.ChatPresenceChangedPayload{
		ChatID:    string(chatID),
		Accepting: snap.Accepting,
		Sending:   snap.Sending,
		Chatty:    snap.Chatty,
		Effective: snap.Effective,
	}
}

func (d *daemon) pushChatPresence(id chat.ChatID, why string) {
	if d == nil || d.ipcSrv == nil || d.chats == nil || id == "" {
		return
	}
	d.chatPresenceMu.Lock()
	defer d.chatPresenceMu.Unlock()
	dc, err := directChat(d, id)
	if err != nil {
		if !errors.Is(err, chat.ErrNotFound) {
			slog.Warn("presence: a chat could not be read; its presence is not pushed",
				slog.String("chat_id", string(id)),
				slog.Any("err", err),
			)
		}
		return
	}
	snap, ok := d.chatPresence(dc)
	if !ok {
		return
	}
	if prev, pushed := d.chatPresencePushed[id]; pushed && prev == snap {
		return
	}
	if d.chatPresencePushed == nil {
		d.chatPresencePushed = map[chat.ChatID]presence.Snapshot{}
	}
	d.chatPresencePushed[id] = snap
	slog.Debug("presence: chat presence pushed",
		slog.String("chat_id", string(id)),
		slog.String("why", why),
		slog.String("effective", snap.Effective),
		slog.Bool("accepting", snap.Accepting),
		slog.Bool("sending", snap.Sending),
	)
	push(d.ipcSrv, ipc.FrameChatPresenceChanged, "", chatPresencePayload(id, snap))
}

func (d *daemon) pushChatPresenceOfPeer(peerID, why string) {
	if dc := d.chatOfPeer(peerID); dc != nil {
		d.pushChatPresence(dc.ID, why)
	}
}

func (d *daemon) dropRoutes(id chat.ChatID, primary string, drop []string) (*chat.DirectChat, bool, error) {
	after, changed, err := d.chats.DropRoutes(id, primary, drop)
	if err == nil && changed {
		d.pushChatPresence(id, "routes dropped")
	}
	return after, changed, err
}

func (d *daemon) dropPairing(id chat.ChatID, peerID string) (*chat.DirectChat, error) {
	after, err := d.chats.DropPairing(id, peerID)
	if err == nil {
		d.pushChatPresence(id, "pairing dropped")
	}
	return after, err
}
