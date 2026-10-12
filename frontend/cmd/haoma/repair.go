package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/devicetag"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/invitation"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/mirror"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/pair"
	"haoma-frontend/internal/syncrail"
)

const (
	reasonUnknownContact = ipc.ReasonUnknownContact

	reasonAlreadyPaired = ipc.ReasonAlreadyPaired

	reasonNoSibling = ipc.ReasonNoSibling

	reasonTagFailed = ipc.ReasonTagFailed

	reasonNotInherited = ipc.ReasonNotInherited

	reasonInviteFailed = ipc.ReasonInviteFailed

	reasonRailSendFailed = ipc.ReasonRailSendFailed

	reasonPersistFailed = ipc.ReasonPersistFailed

	reasonKeyStashFailed = ipc.ReasonKeyStashFailed

	reasonOfferInProgress = ipc.ReasonOfferInProgress

	reasonSelfCheckFailed = ipc.ReasonSelfCheckFailed

	reasonContactOffers = ipc.ReasonContactOffers

	reasonInvitationFromContact = ipc.ReasonInvitationFromContact

	reasonNothingOpen = ipc.ReasonNothingOpen

	reasonDelOnionFailed = "del_onion_failed"
)

const invitationOfferTimeout = 5 * time.Minute

var invitationSelfCheckTimeout = 4 * time.Minute

const invitationAfterCheckReserve = time.Minute

func (sd *sessionDispatcher) handleInvitationOffer(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	d := sd.d
	var req ipc.InvitationOfferRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, reasonBadRequest, fmt.Sprintf("decode payload: %v", err))
		return
	}
	if req.Fingerprint == "" {
		sendError(sess, f.ID, reasonBadRequest, "fingerprint required")
		return
	}
	logger := slog.With(slog.String("fingerprint", req.Fingerprint))
	logger.Debug("invitation: offer observed")

	if d.signalState == nil || d.stores == nil || d.backendClient == nil ||
		d.chats == nil || d.peerMeta == nil || d.invitations == nil || d.events == nil {
		logger.Debug("invitation: offer refused", slog.String("reason", reasonNotReady))
		sendError(sess, f.ID, reasonNotReady, "frontend wiring incomplete")
		return
	}

	if !d.invitationOfferRunning.CompareAndSwap(false, true) {
		logger.Info("invitation: offer refused", slog.String("reason", reasonOfferInProgress))
		sendError(sess, f.ID, reasonOfferInProgress, "another invitation is being offered; offer again once it has ended")
		return
	}
	defer d.invitationOfferRunning.Store(false)

	chatID, plan, reason, detail, ok := pairingOfferPlan(ctx, d, req.Fingerprint, req.Force)
	if !ok {
		logger.Info("invitation: offer refused", slog.String("reason", reason))
		logger.Debug("invitation: offer refusal detail",
			slog.String("reason", reason),
			slog.String("detail", detail),
		)
		sendError(sess, f.ID, reason, detail)
		return
	}

	standing, err := d.invitations.NotesFor(req.Fingerprint)
	if err != nil {
		logger.Warn("invitation: the invitations standing for this person could not be read; the first missing device is offered",
			slog.Any("err", err),
		)
		standing = nil
	}
	target, open := chooseOfferTarget(plan, standing)
	if req.OpenOnly && !open {

		logger.Info("invitation: offer refused", slog.String("reason", reasonNothingOpen))
		sendError(sess, f.ID, reasonNothingOpen, fmt.Sprintf("an invitation stands for each of the %d missing device(s)", len(plan.missing)))
		return
	}
	logger = logger.With(
		slog.String("chat_id", string(chatID)),

		slog.String("device", devicetag.Tag(target)),
	)
	logger.Debug("invitation: the device this offer is for",
		slog.Int("devices", plan.devices),
		slog.Int("paired", plan.paired),
		slog.Int("missing", len(plan.missing)),
		slog.Int("standing", len(standing)),
		slog.Bool("legacy", plan.legacy),
		slog.Bool("by_rule", plan.offer),
		slog.Bool("force", req.Force),
	)

	siblings := d.siblings()
	if len(siblings) == 0 {
		logger.Info("invitation: offer refused", slog.String("reason", reasonNoSibling))
		sendError(sess, f.ID, reasonNoSibling, "this device has no linked device to carry an invitation")
		return
	}

	offeringDevice, err := pair.OfferingDeviceTag(d.store, req.Fingerprint)
	if err != nil {
		logger.Warn("invitation: offer refused", slog.String("reason", reasonTagFailed), slog.Any("err", err))
		sendError(sess, f.ID, reasonTagFailed, "this device could not name itself to that person")
		return
	}
	logger = logger.With(slog.String("offering_device", offeringDevice))

	invitationID, err := msg.NewID()
	if err != nil {
		sendError(sess, f.ID, reasonBuildFailed, err.Error())
		return
	}
	logger = logger.With(slog.String("invitation_id", invitationID))

	offerCtx, cancelOffer := context.WithTimeout(context.Background(), invitationOfferTimeout)
	defer cancelOffer()

	watch, err := openRendezvousWatch(offerCtx, d)
	if err != nil {
		logger.Warn("invitation: offer failed — haomad's event stream did not open",
			slog.String("reason", reasonBackendDown),
			slog.Any("err", err),
		)
		sendError(sess, f.ID, reasonBackendDown, "the daemon's event stream did not open")
		return
	}
	defer watch.close()

	minted, err := d.backendClient.MintOnion(offerCtx)
	if err != nil {
		logger.Warn("invitation: offer failed", slog.String("reason", reasonBackendDown), slog.Any("err", err))
		sendError(sess, f.ID, reasonBackendDown, fmt.Sprintf("mint persistent onion: %v", err))
		return
	}
	if minted.Address == "" || minted.PrivateKey == "" {
		abandonOffer(offerCtx, d, logger, "", minted.Address)
		sendError(sess, f.ID, reasonNotReady, "haomad returned empty mint")
		return
	}
	inv, mine, err := pair.Build(d.signalState, d.stores, []string{minted.Address}, d.selfNick(), nil)
	if err != nil {
		logger.Warn("invitation: offer failed", slog.String("reason", reasonBuildFailed), slog.Any("err", err))
		abandonOffer(offerCtx, d, logger, "", minted.Address)
		sendError(sess, f.ID, reasonBuildFailed, err.Error())
		return
	}
	raw, err := inv.Marshal()
	if err != nil {
		abandonOffer(offerCtx, d, logger, "", minted.Address)
		sendError(sess, f.ID, reasonBuildFailed, err.Error())
		return
	}
	mintedAt := time.Now().Unix()
	started, err := d.backendClient.PairOnionInvite(offerCtx, raw, 0, true)
	if err != nil {

		logger.Warn("invitation: offer failed", slog.String("reason", reasonInviteFailed))
		abandonOffer(offerCtx, d, logger, "", minted.Address)
		sendError(sess, f.ID, reasonInviteFailed, "the daemon did not publish a rendezvous")
		return
	}
	if started.Secret == "" || started.HandleID == "" {
		logger.Warn("invitation: offer failed", slog.String("reason", reasonInviteFailed))
		abandonOffer(offerCtx, d, logger, started.HandleID, minted.Address)
		sendError(sess, f.ID, reasonInviteFailed, "the daemon minted no addressable rendezvous")
		return
	}
	if started.ExpiresAt <= 0 {

		logger.Warn("invitation: offer failed",
			slog.String("reason", reasonInviteFailed),
			slog.Int64("expires_at", started.ExpiresAt),
		)
		abandonOffer(offerCtx, d, logger, started.HandleID, minted.Address)
		sendError(sess, f.ID, reasonInviteFailed, "the daemon published a rendezvous with no deadline")
		return
	}
	logger = logger.With(
		slog.String("handle_id", started.HandleID),
		slog.Int64("minted_at", mintedAt),
		slog.Int64("expires_at", started.ExpiresAt),
	)
	logger.Debug("invitation: rendezvous minted",
		slog.String("local_peer_id", mine.PeerID),
	)

	if err := pair.SaveMyKeys(d.store, started.HandleID, mine, minted); err != nil {
		logger.Error("invitation: offer failed", slog.String("reason", reasonKeyStashFailed), slog.Any("err", err))
		abandonOffer(offerCtx, d, logger, started.HandleID, minted.Address)
		sendError(sess, f.ID, reasonKeyStashFailed, "this device could not keep the keys the pairing needs")
		return
	}

	if reason, detail, ok := awaitInvitationRendezvous(offerCtx, watch, logger, started.HandleID); !ok {

		backCtx, cancelBack := context.WithTimeout(context.Background(), invitationRetireTimeout)
		abandonOffer(backCtx, d, logger, started.HandleID, minted.Address)
		cancelBack()
		sendError(sess, f.ID, reason, detail)
		return
	}
	watch.close()

	_, again, reason, detail, ok := pairingOfferPlan(offerCtx, d, req.Fingerprint, true)
	if ok && !stillMissing(again, target) {
		ok = false
		reason = reasonAlreadyPaired
		detail = "the device this offer was for is paired with this device now"
	}
	if !ok {
		logger.Info("invitation: offer refused after the self-check", slog.String("reason", reason))
		logger.Debug("invitation: offer refusal detail",
			slog.String("reason", reason),
			slog.String("detail", detail),
		)
		abandonOffer(offerCtx, d, logger, started.HandleID, minted.Address)
		sendError(sess, f.ID, reason, detail)
		return
	}

	previous, hasPrevious := previousAttempt(d, logger, req.Fingerprint, target)

	if hasPrevious {
		if err := d.backendClient.PairOnionCancel(offerCtx, previous.HandleID); err != nil {
			logger.Warn("invitation: the previous rendezvous was not cancelled; it stands until it lapses",
				slog.String("previous_handle_id", previous.HandleID),
				slog.Any("err", err),
			)
		} else {
			logger.Debug("invitation: the previous rendezvous for this person was cancelled",
				slog.String("previous_handle_id", previous.HandleID),
			)
		}
		if err := pair.DeleteMyKeys(d.store, previous.HandleID); err != nil {
			logger.Warn("invitation: the previous attempt's keys were not dropped",
				slog.String("previous_handle_id", previous.HandleID),
				slog.Any("err", err),
			)
		}
	}
	note := invitation.Note{
		MsgID:       invitationID,
		HandleID:    started.HandleID,
		Fingerprint: req.Fingerprint,
		MintedAt:    mintedAt,
		ExpiresAt:   started.ExpiresAt,
		Device:      target,
	}
	if err := d.putInvitationNote(note); err != nil {

		logger.Error("invitation: offer failed — attempt note not written",
			slog.String("reason", reasonPersistFailed),
			slog.Any("err", err),
		)
		abandonOffer(offerCtx, d, logger, started.HandleID, minted.Address)
		sendError(sess, f.ID, reasonPersistFailed, "this device could not record the invitation")
		return
	}
	logger.Debug("invitation: attempt note written", slog.Any("note", note))
	if target != 0 {
		retireDevicelessNote(offerCtx, d, logger, req.Fingerprint)
	}

	d.attachInvitationWaiter(context.Background(), started.HandleID, req.Fingerprint, invitationID)

	body := syncrail.InvitationBody{
		ID:             invitationID,
		Fingerprint:    req.Fingerprint,
		OfferingDevice: offeringDevice,
		Secret:         started.Secret,
		MintedAt:       mintedAt,
		ExpiresAt:      started.ExpiresAt,
		Device:         target,
	}

	handed := 0
	for _, sib := range siblings {
		if err := sealInvitationToSibling(offerCtx, d, sib, body); err != nil {
			logger.Warn("invitation: a sibling did not take the invitation",
				slog.String("sibling_peer_id", sib),
				slog.Any("err", err),
			)
			continue
		}
		handed++
		logger.Debug("invitation: handed to a sibling to pass on",
			slog.String("sibling_peer_id", sib),
		)
	}

	body.Secret = ""
	started.Secret = ""
	logger.Debug("invitation: secret released",
		slog.Int("siblings", len(siblings)),
		slog.Int("handed", handed),
	)

	if handed == 0 {
		logger.Warn("invitation: offer failed", slog.String("reason", reasonRailSendFailed))
		abandonOffer(offerCtx, d, logger, started.HandleID, minted.Address)

		dropInvitationAttempt(d, logger, started.HandleID, req.Fingerprint)
		sendError(sess, f.ID, reasonRailSendFailed, "no linked device accepted the invitation")
		return
	}

	markerBody, err := json.Marshal(events.InvitationBody{
		ExpiresAt: started.ExpiresAt,
		State:     events.InvitationLive,
	})
	if err == nil {
		_, err = d.events.AppendLocal(events.LocalParams{
			ChatID:        chatID,
			Kind:          events.KindInvitation,
			Direction:     events.DirOut,
			MsgID:         invitationID,
			ExpireSeconds: chatRetentionTTL(d, chatID),
			Body:          markerBody,
		})
	}
	if err != nil {

		logger.Error("invitation: marker row not written", slog.Any("err", err))
		abandonOffer(offerCtx, d, logger, started.HandleID, minted.Address)
		dropInvitationAttempt(d, logger, started.HandleID, req.Fingerprint)
		sendError(sess, f.ID, reasonPersistFailed, err.Error())
		return
	}
	bumpChatActivity(offerCtx, d, chatID, time.Now().Unix())
	slog.Info("invitation: offered — awaiting the person",
		slog.String("fingerprint", req.Fingerprint),
		slog.String("invitation_id", invitationID),
		slog.String("handle_id", started.HandleID),
		slog.Int64("expires_at", started.ExpiresAt),
		slog.Int("handed", handed),
	)

	resp, err := ipc.NewFrame(ipc.FrameInvitationOffered, f.ID, ipc.InvitationOfferedResponse{
		MsgID:     invitationID,
		ExpiresAt: started.ExpiresAt,
		Siblings:  handed,
	})
	if err != nil {
		sendError(sess, f.ID, reasonEncodeFrame, err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("invitation: send offered frame failed", slog.Any("err", err))
	}
}

func awaitInvitationRendezvous(ctx context.Context, watch *rendezvousWatch, logger *slog.Logger, handleID string) (reason, detail string, ok bool) {
	bound := invitationSelfCheckTimeout
	if deadline, has := ctx.Deadline(); has {
		if left := time.Until(deadline) - invitationAfterCheckReserve; left < bound {
			bound = left
		}
	}
	checkCtx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	logger.Debug("invitation: waiting for the rendezvous to answer this device's own dial",
		slog.Duration("bound", bound),
	)
	last, err := watch.await(checkCtx, handleID, func(p backendapi.PairOnionProbe) {

		logger.Debug("invitation: self-check attempt",
			slog.Int("attempt", p.Attempt),
			slog.Bool("ready", p.Ready),
			slog.String("reach", reachToken(p.Error)),
		)
	})
	switch {
	case err != nil && checkCtx.Err() != nil:
		logger.Warn("invitation: offer failed — the rendezvous did not answer in time",
			slog.String("reason", reasonSelfCheckFailed),
			slog.Duration("bound", bound),
		)
		return reasonSelfCheckFailed, "the rendezvous did not answer this device's own dial in time", false
	case err != nil:
		logger.Warn("invitation: offer failed — haomad's event stream ended during the self-check",
			slog.String("reason", reasonBackendDown),
			slog.Any("err", err),
		)
		return reasonBackendDown, "the daemon's event stream ended before the rendezvous answered", false
	case last.Error != "":
		logger.Warn("invitation: offer failed — haomad gave up dialling the rendezvous",
			slog.String("reason", reasonSelfCheckFailed),
			slog.Int("attempt", last.Attempt),
		)
		return reasonSelfCheckFailed, "the daemon gave up dialling the rendezvous", false
	}
	logger.Info("invitation: the rendezvous answers — sending the invitation",
		slog.Int("attempt", last.Attempt),
	)
	return "", "", true
}

func previousAttempt(d *daemon, logger *slog.Logger, fingerprint string, device uint32) (invitation.Note, bool) {
	note, err := d.invitations.GetNote(fingerprint, device)
	if errors.Is(err, invitation.ErrNotFound) {
		return invitation.Note{}, false
	}
	if err != nil {
		logger.Warn("invitation: could not read the previous attempt for this person; it will not be cancelled",
			slog.Any("err", err),
		)
		return invitation.Note{}, false
	}
	return note, true
}

func abandonOffer(ctx context.Context, d *daemon, logger *slog.Logger, handleID, onionAddress string) {
	if d.backendClient == nil {
		return
	}
	if handleID != "" {
		if err := d.backendClient.PairOnionCancel(ctx, handleID); err != nil {
			logger.Warn("invitation: the abandoned rendezvous was not cancelled; it stands until it lapses",
				slog.String("handle_id", handleID),
				slog.Any("err", err),
			)
		}
		if err := pair.DeleteMyKeys(d.store, handleID); err != nil {
			logger.Warn("invitation: the abandoned attempt's keys were not dropped",
				slog.String("handle_id", handleID),
				slog.Any("err", err),
			)
		}
	}
	if onionAddress != "" {

		if err := d.backendClient.DelOnion(ctx, onionAddress); err != nil {
			logger.Warn("invitation: the abandoned address was not released",
				slog.String("reason", reasonDelOnionFailed),
			)
		}
	}
	logger.Debug("invitation: offer abandoned — nothing of it is left standing",
		slog.Bool("rendezvous", handleID != ""),
	)
}

func sealInvitationToSibling(ctx context.Context, d *daemon, siblingPeerID string, body syncrail.InvitationBody) error {
	if d.backendClient == nil {
		return errors.New("invitation: no backend client")
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	defer zeroBytes(raw)
	key, err := d.railKey()
	if err != nil {
		return err
	}
	_, err = mirror.SealAndSend(ctx, d.backendClient, key, siblingPeerID, syncrail.Envelope{
		V:    syncrail.Version,
		Kind: syncrail.KindInvitation,
		Ts:   time.Now().Unix(),
		Body: raw,
	})
	return err
}

func (d *daemon) awaitInvitationJoiner(ctx context.Context, handleID, fingerprint, invitationID string) {
	logger := slog.With(
		slog.String("handle_id", handleID),
		slog.String("fingerprint", fingerprint),
		slog.String("invitation_id", invitationID),
	)
	logger.Debug("invitation: waiting for the person to dial")
	resp, err := d.waitInvitationJoiner(ctx, logger, handleID, fingerprint)
	if err != nil && ctx.Err() != nil {

		logger.Debug("invitation: the wait stopped with this process", slog.Any("err", err))
		return
	}
	if err != nil {
		logger.Info("invitation: the wait ended without anyone arriving",
			slog.String("reason", reasonWaitFailed),
		)
		logger.Debug("invitation: wait failure detail", slog.Any("err", err))
		return
	}
	logger.Debug("invitation: somebody dialled the rendezvous",
		slog.Int("joiner_payload_bytes", len(resp.JoinerPayload)),
	)

	completeCtx, cancel := context.WithTimeout(context.Background(), invitationCompleteTimeout)
	defer cancel()

	joiner, err := pair.Parse(resp.JoinerPayload)
	if err != nil {
		logger.Warn("invitation: the pairing did not complete", slog.String("reason", reasonBadPayload), slog.Any("err", err))
		abandonInvitationAttempt(completeCtx, d, logger, handleID, fingerprint, invitationID, false)
		return
	}

	proved, err := checkInvitationFingerprint(joiner, fingerprint)
	if err != nil {
		logger.Warn("invitation: the pairing was refused — nothing was registered",
			slog.String("reason", reasonFingerprintMismatch),
			slog.String("proved_fingerprint", proved),
			slog.String("peer_id", joiner.PeerID),
		)
		abandonInvitationAttempt(completeCtx, d, logger, handleID, fingerprint, invitationID, false)
		return
	}
	logger = logger.With(slog.String("peer_id", joiner.PeerID))
	logger.Debug("invitation: the person proved the identity this invitation was offered to")

	mine, minted, err := pair.LoadMyKeys(d.store, handleID)
	if err != nil {
		logger.Error("invitation: the pairing did not complete", slog.String("reason", reasonMyKeysMissing), slog.Any("err", err))
		abandonInvitationAttempt(completeCtx, d, logger, handleID, fingerprint, invitationID, false)
		return
	}

	seedVerifiedFingerprint(d, logger, joiner.PeerID, proved)

	if err := pair.Import(completeCtx, d.stores, d.backendClient, joiner, mine, minted); err != nil {

		logger.Error("invitation: the pairing did not complete", slog.String("reason", reasonImportFailed), slog.Any("err", err))
		abandonInvitationAttempt(completeCtx, d, logger, handleID, fingerprint, invitationID, true)
		return
	}
	seedPairNick(d, joiner.PeerID, joiner.Frontend.Nick)

	folded, fresh, err := d.createPairedChat(joiner.PeerID, true)
	if err != nil {
		logger.Warn("invitation: the completed pairing was not filed into a conversation", slog.Any("err", err))
	} else {
		logger.Debug("invitation: the fresh pairing joined the person's conversation",
			slog.String("folded_chat_id", string(folded.ID)),
			slog.Bool("new_conversation", fresh),
		)
		if fresh {
			logger.Warn("invitation: the pairing started a new conversation rather than joining the inherited one",
				slog.String("folded_chat_id", string(folded.ID)),
			)
		}
	}

	if err := d.sendFirstPresence(completeCtx, joiner.PeerID, "invitation-offer"); err != nil {
		logger.Warn("invitation: the first presence was not shipped; the person stays mute until one is",
			slog.Any("err", err),
		)
	}

	stampInvitationOutcome(d, logger, invitationID, events.InvitationAccepted, invitationNoteDeadline(d, handleID, fingerprint), false)

	dropInvitationAttempt(d, logger, handleID, fingerprint)

	chatID := ""
	if folded != nil {
		chatID = string(folded.ID)
	}
	slog.Info("invitation: the person answered — a device that could only read about them can now reach them",
		slog.String("invitation_id", invitationID),
		slog.String("peer_id", joiner.PeerID),
		slog.String("chat_id", chatID),
	)
	pushPeerPaired(completeCtx, d, joiner.PeerID, "invitation-offer")
}

const invitationCompleteTimeout = 60 * time.Second

func abandonInvitationAttempt(ctx context.Context, d *daemon, logger *slog.Logger, handleID, fingerprint, invitationID string, registered bool) {
	if d.backendClient != nil && handleID != "" {
		if err := d.backendClient.PairOnionCancel(ctx, handleID); err != nil {
			logger.Warn("invitation: the spent rendezvous was not cancelled; it stands until it lapses",
				slog.Any("err", err),
			)
		}

		if _, minted, err := pair.LoadMyKeys(d.store, handleID); err == nil && minted.Address != "" && !registered {
			if err := d.backendClient.DelOnion(ctx, minted.Address); err != nil {
				logger.Warn("invitation: the address minted for this invitation was not released",
					slog.String("reason", reasonDelOnionFailed),
				)
			}
		}
	}
	stampInvitationOutcome(d, logger, invitationID, events.InvitationFailed, invitationNoteDeadline(d, handleID, fingerprint), false)
	dropInvitationAttempt(d, logger, handleID, fingerprint)
	logger.Debug("invitation: the attempt was abandoned — nothing of it is left standing",
		slog.Bool("address_kept_for_a_registration", registered),
	)
}

func dropInvitationAttempt(d *daemon, logger *slog.Logger, handleID, fingerprint string) {
	if d.invitations != nil && fingerprint != "" {
		switch note, found, err := noteForHandle(d, fingerprint, handleID); {
		case err != nil:
			logger.Warn("invitation: the attempt note was not read, so it was not dropped", slog.Any("err", err))
		case !found:
			logger.Debug("invitation: no note names this rendezvous; a newer one for that device of the person stays")
		default:
			if err := d.invitations.DeleteNote(fingerprint, note.Device); err != nil {
				logger.Warn("invitation: the attempt note was not dropped", slog.Any("err", err))
			}
		}
	}
	if handleID != "" {
		if err := pair.DeleteMyKeys(d.store, handleID); err != nil {
			logger.Warn("invitation: the attempt's keys were not dropped", slog.Any("err", err))
		}
	}
	logger.Debug("invitation: the attempt's local handles were dropped")
}

func invitationNoteDeadline(d *daemon, handleID, fingerprint string) int64 {
	if d == nil || d.invitations == nil || fingerprint == "" {
		return 0
	}
	note, found, err := noteForHandle(d, fingerprint, handleID)
	if err != nil || !found {
		return 0
	}
	return note.ExpiresAt
}

func noteForHandle(d *daemon, fingerprint, handleID string) (invitation.Note, bool, error) {
	if d == nil || d.invitations == nil || fingerprint == "" || handleID == "" {
		return invitation.Note{}, false, nil
	}
	notes, err := d.invitations.NotesFor(fingerprint)
	if err != nil {
		return invitation.Note{}, false, err
	}
	for _, n := range notes {
		if n.HandleID == handleID {
			return n, true, nil
		}
	}
	return invitation.Note{}, false, nil
}

func (d *daemon) putInvitationNote(n invitation.Note) error {
	if d.notePut != nil {
		return d.notePut(n)
	}
	return d.invitations.PutNote(n)
}

func retireDevicelessNote(ctx context.Context, d *daemon, logger *slog.Logger, fingerprint string) {
	old, err := d.invitations.GetNote(fingerprint, 0)
	if errors.Is(err, invitation.ErrNotFound) {
		return
	}
	if err != nil {
		logger.Warn("invitation: the offer that names no device could not be read; it stands until it lapses", slog.Any("err", err))
		return
	}
	if err := d.backendClient.PairOnionCancel(ctx, old.HandleID); err != nil {
		logger.Warn("invitation: the offer that names no device was not cancelled; it stands until it lapses",
			slog.String("previous_handle_id", old.HandleID),
			slog.Any("err", err),
		)
		return
	}
	stampInvitationOutcome(d, logger, old.MsgID, events.InvitationSuperseded, old.ExpiresAt, false)
	if err := d.invitations.DeleteNote(fingerprint, 0); err != nil {
		logger.Warn("invitation: the note of the offer that names no device was not dropped", slog.Any("err", err))
	}
	if err := pair.DeleteMyKeys(d.store, old.HandleID); err != nil {
		logger.Warn("invitation: the keys of the offer that names no device were not dropped", slog.Any("err", err))
	}
	logger.Info("invitation: the offer that named no device was taken back; offers now name the device",
		slog.String("previous_handle_id", old.HandleID),
	)
}
