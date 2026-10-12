package main

import (
	"log/slog"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/crdt"
	"haoma-frontend/internal/ipc"
)

func refuseLinkedDevice(d *daemon, sess *ipc.Session, f ipc.Frame, peerID string) bool {
	mine, err := d.readIsSibling(peerID)
	if err != nil {
		sendError(sess, f.ID, reasonStoreReadFailed, "the linked-device set could not be read")
		return true
	}
	if !mine {
		return false
	}
	slog.Warn("request refused: a linked device has no chat",
		slog.String("frame", string(f.Type)),
		slog.String("peer_id", peerID),
	)
	sendError(sess, f.ID, ipc.ReasonBadRequest, "this is a linked device, not a contact")
	return true
}

func skipRowWhileLinking(d *daemon, peerID string) bool {
	if !deviceLinkPending(d, peerID) {
		return false
	}
	slog.Debug("peer.updated not pushed — an accept waits on this device",
		slog.String("peer_id", peerID),
	)
	return true
}

func dropLinkedDeviceChats(d *daemon) {
	if d == nil || d.chats == nil || d.linkedDevices == nil {
		return
	}
	set, err := d.linkedDevices.Get()
	if err != nil {
		slog.Warn("linked-device chats: not dropped, the linked-device set could not be read",
			slog.Any("err", err))
		return
	}
	all, err := d.chats.List()
	if err != nil {
		slog.Warn("linked-device chats: not dropped, the chats could not be read",
			slog.Any("err", err))
		return
	}
	dropped, eventsDeleted, mixed, failed := 0, 0, 0, 0
	for _, c := range all {
		dc, ok := c.(*chat.DirectChat)
		if !ok {
			continue
		}
		ids := dc.FanoutPeerIDs()
		linked := 0
		for _, id := range ids {
			if everLinked(set, id) {
				linked++
			}
		}
		if linked == 0 {
			continue
		}
		if linked < len(ids) {
			mixed++
			slog.Warn("linked-device chats: a chat holds a linked device and a device that is not linked; left alone",
				slog.String("chat_id", string(dc.ID)),
				slog.Int("devices", len(ids)),
				slog.Int("linked_devices", linked),
			)
			continue
		}
		n := 0
		if d.events != nil {
			n, _, err = d.events.DeleteByChat(dc.ID)
			if err != nil {
				failed++
				slog.Warn("linked-device chats: the events of a chat could not be deleted; chat kept",
					slog.String("chat_id", string(dc.ID)),
					slog.Any("err", err),
				)
				continue
			}
		}
		if err := d.chats.Delete(dc.ID); err != nil {
			failed++
			slog.Warn("linked-device chats: a chat could not be deleted",
				slog.String("chat_id", string(dc.ID)),
				slog.Any("err", err),
			)
			continue
		}
		dropped++
		eventsDeleted += n
		slog.Info("linked-device chats: dropped a chat with a linked device",
			slog.String("chat_id", string(dc.ID)),
			slog.Int("events_deleted", n),
		)
	}
	slog.Info("linked-device chats: start-up check done",
		slog.Int("chats", len(all)),
		slog.Int("dropped", dropped),
		slog.Int("events_deleted", eventsDeleted),
		slog.Int("left_mixed", mixed),
		slog.Int("failed", failed),
	)
}

func everLinked(set crdt.TwoPSet, id string) bool {
	if _, ok := set.Added[id]; ok {
		return true
	}
	_, ok := set.Removed[id]
	return ok
}
