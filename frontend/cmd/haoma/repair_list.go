package main

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
)

type invitationRow struct {
	entry      ipc.InvitationEntry
	mintedAt   int64
	answerable bool
}

func (sd *sessionDispatcher) handleInvitationsList(_ context.Context, sess *ipc.Session, f ipc.Frame) {
	d := sd.d
	slog.Debug("invitation: list observed")

	out := ipc.InvitationsListedResponse{Invitations: []ipc.InvitationEntry{}}
	if d != nil && d.invitations != nil {
		now := time.Now().Unix()
		var rows []invitationRow

		records, err := d.invitations.List()
		if err != nil {
			slog.Error("invitation: the invitations offered to this device could not be read", slog.Any("err", err))
			sendError(sess, f.ID, reasonStoreReadFailed, "the invitations could not be read")
			return
		}
		for _, rec := range records {
			rows = append(rows, invitationRow{
				entry: ipc.InvitationEntry{
					MsgID:     rec.MsgID,
					Label:     invitationPersonLabel(d, rec.ChatID),
					Direction: ipc.InvitationOfferedToMe,
					ExpiresAt: rec.ExpiresAt,
					State:     rec.State,
				},
				mintedAt:   rec.MintedAt,
				answerable: events.InvitationAnswerable(rec.State, rec.ExpiresAt, now),
			})
		}

		notes, err := d.invitations.ListNotes()
		if err != nil {
			slog.Error("invitation: the invitations this device offered could not be read", slog.Any("err", err))
			sendError(sess, f.ID, reasonStoreReadFailed, "the invitations could not be read")
			return
		}
		for _, n := range notes {

			rows = append(rows, invitationRow{

				entry: ipc.InvitationEntry{
					MsgID:       n.MsgID,
					Label:       invitationPersonLabelByFingerprint(d, n.Fingerprint),
					Direction:   ipc.InvitationOfferedByMe,
					ExpiresAt:   n.ExpiresAt,
					State:       events.InvitationLive,
					Fingerprint: n.Fingerprint,
				},
				mintedAt:   n.MintedAt,
				answerable: false,
			})
		}

		sortInvitationRows(rows)
		for _, r := range rows {
			out.Invitations = append(out.Invitations, r.entry)
		}
	}

	slog.Debug("invitation: list answered", slog.Int("invitation_count", len(out.Invitations)))
	resp, err := ipc.NewFrame(ipc.FrameInvitationsListed, f.ID, out)
	if err != nil {
		sendError(sess, f.ID, reasonEncodeFrame, err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("invitation: send listed frame failed", slog.Any("err", err))
	}
}

func invitationPersonLabel(d *daemon, chatID string) string {
	if d == nil || d.chats == nil || chatID == "" {
		return ""
	}
	c, err := d.chats.Get(chat.ChatID(chatID))
	if err != nil {
		return ""
	}
	entry := chatToEntry(c)
	applyChatLabel(d, &entry)
	return entry.Label
}

func invitationPersonLabelByFingerprint(d *daemon, fingerprint string) string {
	dc, found := chatForPerson(d, fingerprint)
	if !found || dc == nil {
		return ""
	}
	return invitationPersonLabel(d, string(dc.ID))
}

func sortInvitationRows(rows []invitationRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].answerable != rows[j].answerable {
			return rows[i].answerable
		}
		if rows[i].mintedAt != rows[j].mintedAt {
			return rows[i].mintedAt > rows[j].mintedAt
		}
		return rows[i].entry.MsgID < rows[j].entry.MsgID
	})
}
