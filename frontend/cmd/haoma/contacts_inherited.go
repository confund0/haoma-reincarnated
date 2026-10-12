package main

import (
	"context"
	"log/slog"
	"time"

	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/peerstate"
)

func (sd *sessionDispatcher) handleListInheritedContacts(_ context.Context, sess *ipc.Session, f ipc.Frame) {
	slog.Debug("inherited contacts: list observed")

	contacts, err := inheritedContacts(sd.d)
	if err != nil {
		slog.Error("inherited contacts: the contacts held here could not be read", slog.Any("err", err))
		sendError(sess, f.ID, reasonStoreReadFailed, "the contacts could not be read")
		return
	}

	linked, err := sd.d.readHasSibling()
	if err != nil {
		slog.Error("inherited contacts: the linked-device set could not be read", slog.Any("err", err))
		sendError(sess, f.ID, reasonStoreReadFailed, "the contacts could not be read")
		return
	}

	slog.Debug("inherited contacts: list answered",
		slog.Int("contact_count", len(contacts)),
		slog.Bool("linked", linked),
	)
	resp, err := ipc.NewFrame(ipc.FrameInheritedContactsListed, f.ID, ipc.InheritedContactsListedResponse{
		Contacts: contacts,
		Linked:   linked,
	})
	if err != nil {
		sendError(sess, f.ID, reasonEncodeFrame, err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("inherited contacts: send listed frame failed", slog.Any("err", err))
	}
}

func pushInheritedContactsChanged(d *daemon, logger *slog.Logger, reason string) {
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With(slog.String("reason", reason))
	logger.Debug("inherited contacts: a change to the set was observed")
	if d == nil || d.ipcSrv == nil {
		logger.Debug("inherited contacts: change not pushed — no client server")
		return
	}
	contacts, err := inheritedContacts(d)
	if err != nil {
		logger.Warn("inherited contacts: change not pushed — the contacts held here could not be read",
			slog.Any("err", err),
		)
		return
	}
	linked, err := d.readHasSibling()
	if err != nil {
		logger.Warn("inherited contacts: change not pushed — the linked-device set could not be read",
			slog.Any("err", err),
		)
		return
	}
	push(d.ipcSrv, ipc.FrameInheritedContactsChanged, "", ipc.InheritedContactsChangedPayload{
		Contacts: contacts,
		Linked:   linked,
	})
	logger.Debug("inherited contacts: change pushed",
		slog.Int("contact_count", len(contacts)),
	)
}

func inheritedContacts(d *daemon) ([]ipc.InheritedContactEntry, error) {
	out := []ipc.InheritedContactEntry{}
	if d == nil || d.peerMeta == nil || d.chats == nil {
		return out, nil
	}
	persons, err := d.peerMeta.Persons()
	if err != nil {
		return nil, err
	}
	for _, p := range persons {

		if leaveOutDeleted(d, slog.Default(), p.Fingerprint) {
			continue
		}
		chatID, plan, reason, _, ok := pairingOfferPlan(context.Background(), d, p.Fingerprint, false)
		waiting := !ok && reason == reasonContactOffers
		invited := !ok && reason == reasonInvitationFromContact
		if !ok && !waiting && !invited {
			slog.Debug("inherited contacts: person left out of the list",
				slog.String("fingerprint", p.Fingerprint),
				slog.String("reason", reason),
			)
			continue
		}
		offers := len(plan.missing)
		open := openMissing(d, p.Fingerprint, plan)
		slog.Debug("inherited contacts: person listed",
			slog.String("fingerprint", p.Fingerprint),
			slog.String("chat_id", string(chatID)),
			slog.Int("devices", plan.devices),
			slog.Int("paired", plan.paired),
			slog.Int("offers", offers),
			slog.Bool("waiting", waiting),
			slog.Bool("invited", invited),
			slog.Int("open", open),
		)
		out = append(out, ipc.InheritedContactEntry{
			Fingerprint: p.Fingerprint,
			Nick:        p.Meta.Nick,
			Alias:       p.Meta.Alias,

			Label:   peerstate.Resolve(p.Meta, p.Fingerprint),
			Devices: plan.devices,
			Paired:  plan.paired,
			Offers:  offers,
			Waiting: waiting,
			Invited: invited,
			Open:    open,
		})
	}
	return out, nil
}

func openMissing(d *daemon, fingerprint string, plan pairingPlan) int {
	if d.invitations == nil {
		return len(plan.missing)
	}
	notes, err := d.invitations.NotesFor(fingerprint)
	if err != nil {
		slog.Warn("inherited contacts: the invitations this device offered could not be read; nothing counts as open",
			slog.String("fingerprint", fingerprint),
			slog.Any("err", err),
		)
		return 0
	}
	now := time.Now().Unix()
	devicelessLive := false
	for _, n := range notes {
		if n.Device == 0 && n.ExpiresAt > now {
			devicelessLive = true
		}
	}
	open := 0
	for _, m := range plan.missing {
		standing := false
		for _, n := range notes {
			if n.Device == m && n.ExpiresAt > now {
				standing = true
				break
			}
		}
		if !standing {
			open++
		}
	}
	if devicelessLive && open > 0 {
		open--
	}
	return open
}
