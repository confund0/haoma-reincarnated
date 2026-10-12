package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/dgraph-io/badger/v4"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/invitation"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/pair"
)

const (
	reasonUnknownInvitation = ipc.ReasonUnknownInvitation

	reasonInvitationNotLive = ipc.ReasonInvitationNotLive

	reasonInvitationLapsed = ipc.ReasonInvitationLapsed

	reasonRendezvousRefused = ipc.ReasonRendezvousRefused

	reasonRendezvousUnreachable = ipc.ReasonRendezvousUnreachable

	reasonFingerprintMismatch = ipc.ReasonFingerprintMismatch

	reasonImportFailed = ipc.ReasonImportFailed

	reasonStoreReadFailed = ipc.ReasonStoreReadFailed
)

var errFingerprintMismatch = errors.New("invitation: the far side is not the person this invitation was addressed to")

const invitationAcceptTimeout = 5 * time.Minute

func (sd *sessionDispatcher) handleInvitationAccept(ctx context.Context, sess *ipc.Session, f ipc.Frame) {
	d := sd.d
	var req ipc.InvitationAcceptRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		sendError(sess, f.ID, reasonBadRequest, fmt.Sprintf("decode payload: %v", err))
		return
	}
	if req.MsgID == "" {
		sendError(sess, f.ID, reasonBadRequest, "msg_id required")
		return
	}
	logger := slog.With(slog.String("invitation_id", req.MsgID))
	logger.Debug("invitation: accept observed")

	if d.signalState == nil || d.stores == nil || d.backendClient == nil ||
		d.chats == nil || d.peerMeta == nil || d.invitations == nil || d.events == nil {
		logger.Debug("invitation: accept refused", slog.String("reason", reasonNotReady))
		sendError(sess, f.ID, reasonNotReady, "frontend wiring incomplete")
		return
	}

	rec, err := d.invitations.Claim(req.MsgID)
	switch {
	case errors.Is(err, invitation.ErrNotFound):
		logger.Info("invitation: accept refused", slog.String("reason", reasonUnknownInvitation))
		sendError(sess, f.ID, reasonUnknownInvitation, "no invitation is held for that row")
		return
	case errors.Is(err, invitation.ErrNotLive):
		logger.Info("invitation: accept refused", slog.String("reason", reasonInvitationNotLive))
		sendError(sess, f.ID, reasonInvitationNotLive, "that invitation is no longer the live one")
		return
	case err != nil:
		logger.Warn("invitation: accept refused", slog.String("reason", reasonStoreReadFailed), slog.Any("err", err))
		sendError(sess, f.ID, reasonStoreReadFailed, "the invitation could not be read")
		return
	}
	logger = logger.With(
		slog.String("chat_id", rec.ChatID),
		slog.String("fingerprint", rec.Fingerprint),
	)
	logger.Debug("invitation: the accept claimed the invitation")

	if now := time.Now().Unix(); rec.ExpiresAt <= now {

		logger.Info("invitation: accept refused",
			slog.String("reason", reasonInvitationLapsed),
			slog.Int64("expires_at", rec.ExpiresAt),
		)
		stampInvitationOutcome(d, logger, req.MsgID, events.InvitationFailed, rec.ExpiresAt, true)
		sendError(sess, f.ID, reasonInvitationLapsed, "that invitation has lapsed")
		return
	}

	acceptCtx, cancelAccept := context.WithTimeout(context.Background(), invitationAcceptTimeout)
	defer cancelAccept()

	minted, err := d.backendClient.MintOnion(acceptCtx)
	if err != nil {
		logger.Warn("invitation: accept failed", slog.String("reason", reasonBackendDown), slog.Any("err", err))
		releaseInvitationClaim(d, logger, req.MsgID)
		sendError(sess, f.ID, reasonBackendDown, "the daemon could not mint an address")
		return
	}
	if minted.Address == "" || minted.PrivateKey == "" {

		logger.Warn("invitation: accept failed", slog.String("reason", reasonNotReady))
		releaseInvitationOnion(acceptCtx, d, logger, minted.Address)
		releaseInvitationClaim(d, logger, req.MsgID)
		sendError(sess, f.ID, reasonNotReady, "haomad returned empty mint")
		return
	}
	mineInv, mine, err := pair.Build(d.signalState, d.stores, []string{minted.Address}, d.selfNick(), nil)
	if err != nil {
		logger.Warn("invitation: accept failed", slog.String("reason", reasonBuildFailed), slog.Any("err", err))
		releaseInvitationOnion(acceptCtx, d, logger, minted.Address)
		releaseInvitationClaim(d, logger, req.MsgID)
		sendError(sess, f.ID, reasonBuildFailed, err.Error())
		return
	}
	raw, err := mineInv.Marshal()
	if err != nil {
		logger.Warn("invitation: accept failed", slog.String("reason", reasonBuildFailed), slog.Any("err", err))
		releaseInvitationOnion(acceptCtx, d, logger, minted.Address)
		releaseInvitationClaim(d, logger, req.MsgID)
		sendError(sess, f.ID, reasonBuildFailed, err.Error())
		return
	}
	logger.Debug("invitation: dialling the rendezvous the invitation names",
		slog.String("local_peer_id", mine.PeerID),
	)

	answered, err := d.backendClient.PairOnionAcceptSecret(acceptCtx, rec.Secret, raw)
	if err != nil {
		reason, detail, settled := invitationDialRefusal(err)

		if !settled && rec.ExpiresAt <= time.Now().Unix() {
			reason, detail, settled = reasonInvitationLapsed, "that invitation has lapsed", true
		}

		logger.Warn("invitation: accept failed",
			slog.String("reason", reason),
			slog.Bool("settled", settled),
		)
		releaseInvitationOnion(acceptCtx, d, logger, minted.Address)
		if settled {
			stampInvitationOutcome(d, logger, req.MsgID, events.InvitationFailed, rec.ExpiresAt, true)
		} else {
			releaseInvitationClaim(d, logger, req.MsgID)
		}
		sendError(sess, f.ID, reason, detail)
		return
	}
	logger.Debug("invitation: the rendezvous answered",
		slog.Int("inviter_payload_bytes", len(answered.InviterPayload)),
	)

	theirInv, err := pair.Parse(answered.InviterPayload)
	if err != nil {
		logger.Warn("invitation: accept failed", slog.String("reason", reasonBadPayload), slog.Any("err", err))
		releaseInvitationOnion(acceptCtx, d, logger, minted.Address)
		stampInvitationOutcome(d, logger, req.MsgID, events.InvitationFailed, rec.ExpiresAt, true)
		sendError(sess, f.ID, reasonBadPayload, "the rendezvous answered with something unusable")
		return
	}

	fingerprint, err := checkInvitationFingerprint(theirInv, rec.Fingerprint)
	if err != nil {
		logger.Warn("invitation: accept refused — nothing was registered",
			slog.String("reason", reasonFingerprintMismatch),
			slog.String("proved_fingerprint", fingerprint),
			slog.String("peer_id", theirInv.PeerID),
		)
		releaseInvitationOnion(acceptCtx, d, logger, minted.Address)
		stampInvitationOutcome(d, logger, req.MsgID, events.InvitationFailed, rec.ExpiresAt, true)
		sendError(sess, f.ID, reasonFingerprintMismatch, "the rendezvous belongs to somebody else")
		return
	}
	logger = logger.With(slog.String("peer_id", theirInv.PeerID))
	logger.Debug("invitation: the far side proved the identity this invitation names")

	seedVerifiedFingerprint(d, logger, theirInv.PeerID, fingerprint)

	if err := pair.ImportResponder(acceptCtx, d.stores, d.backendClient, theirInv, mine, minted); err != nil {

		logger.Warn("invitation: accept failed", slog.String("reason", reasonImportFailed), slog.Any("err", err))
		stampInvitationOutcome(d, logger, req.MsgID, events.InvitationFailed, rec.ExpiresAt, true)
		sendError(sess, f.ID, reasonImportFailed, "the pairing material would not install")
		return
	}
	seedPairNick(d, theirInv.PeerID, theirInv.Frontend.Nick)

	folded, fresh, err := d.createPairedChat(theirInv.PeerID, false)
	if err != nil {
		logger.Warn("invitation: the completed pairing was not filed into a conversation", slog.Any("err", err))
	} else {
		logger.Debug("invitation: the fresh pairing joined the person's conversation",
			slog.String("folded_chat_id", string(folded.ID)),
			slog.Bool("new_conversation", fresh),
		)
		if fresh {

			logger.Warn("invitation: the pairing started a second conversation for a person who already had one",
				slog.String("folded_chat_id", string(folded.ID)),
				slog.String("expected_chat_id", rec.ChatID),
			)
		}
	}

	stampInvitationOutcome(d, logger, req.MsgID, events.InvitationAccepted, rec.ExpiresAt, true)

	chatID := rec.ChatID
	if folded != nil {
		chatID = string(folded.ID)
	}
	slog.Info("invitation: accepted — a person this device could only read about is now reachable",
		slog.String("invitation_id", req.MsgID),
		slog.String("peer_id", theirInv.PeerID),
		slog.String("chat_id", chatID),
	)

	resp, err := ipc.NewFrame(ipc.FrameInvitationAccepted, f.ID, ipc.InvitationAcceptedResponse{
		MsgID:               req.MsgID,
		PeerID:              theirInv.PeerID,
		ChatID:              chatID,
		IdentityFingerprint: fingerprint,
	})
	if err != nil {
		sendError(sess, f.ID, reasonEncodeFrame, err.Error())
		return
	}
	if err := sess.Send(resp); err != nil {
		slog.Warn("invitation: send accepted frame failed", slog.Any("err", err))
	}
	pushPeerPaired(acceptCtx, d, theirInv.PeerID, "invitation-accept")
}

func checkInvitationFingerprint(inv *pair.Invite, want string) (string, error) {
	if inv == nil {
		return "", errors.New("invitation: no invite to check")
	}
	if want == "" {

		return "", errors.New("invitation: no identity to check the far side against")
	}
	got, err := inv.IdentityFingerprint()
	if err != nil {
		return "", err
	}
	if got != want {
		return got, errFingerprintMismatch
	}
	return got, nil
}

func seedVerifiedFingerprint(d *daemon, logger *slog.Logger, peerID, fingerprint string) {
	if d == nil || d.peerMeta == nil || peerID == "" || fingerprint == "" {
		return
	}
	changed, prev, err := d.peerMeta.SetFingerprint(peerID, fingerprint)
	if err != nil {
		logger.Warn("invitation: the verified identity was not recorded for this pairing",
			slog.Any("err", err),
		)
		return
	}
	logger.Debug("invitation: verified identity recorded before the import",
		slog.String("peer_id", peerID),
		slog.Bool("changed", changed),
		slog.Bool("had_previous", prev != ""),
	)
}

func stampInvitationOutcome(d *daemon, logger *slog.Logger, msgID, state string, expiresAt int64, heldHere bool) {
	if d == nil || d.events == nil || msgID == "" {
		return
	}
	body, err := json.Marshal(events.InvitationBody{ExpiresAt: expiresAt, State: state})
	if err != nil {
		logger.Error("invitation: the outcome could not be encoded", slog.Any("err", err))
		return
	}
	var attach events.TxnWrite
	if heldHere && d.invitations != nil {
		attach = func(txn *badger.Txn) error {
			return d.invitations.SetStateTxn(txn, msgID, state)
		}
	}
	_, err = d.events.UpdateInvitationBody(msgID, body, attach)
	switch {
	case err == nil:
		logger.Debug("invitation: outcome stamped — the row and the record agree",
			slog.String("state", state),
			slog.Bool("secret_destroyed", heldHere),
		)
	case errors.Is(err, invitation.ErrSettled):
		logger.Debug("invitation: outcome already settled — no state change",
			slog.String("state", state),
		)
	case errors.Is(err, events.ErrEventNotFound):

		logger.Debug("invitation: no row left to stamp",
			slog.String("state", state),
		)
	default:

		logger.Error("invitation: the outcome was not stamped; a spent secret may remain at rest",
			slog.String("state", state),
			slog.Any("err", err),
		)
	}
}

func invitationDialRefusal(err error) (reason, detail string, settled bool) {
	switch {
	case errors.Is(err, backendapi.ErrRendezvousNotReady):
		return reasonNotReady, "the daemon is not ready to dial an invitation", false
	case errors.Is(err, backendapi.ErrRendezvousRequestRefused):

		return reasonBadRequest, "the daemon would not dial this invitation", true
	case errors.Is(err, backendapi.ErrRendezvousRefused):
		return reasonRendezvousRefused, "the rendezvous refused this invitation", true
	case errors.Is(err, backendapi.ErrRendezvousUnreachable):
		return reasonRendezvousUnreachable, "the rendezvous did not answer; it can be tried again", false
	default:
		return reasonRendezvousUnreachable, "the rendezvous did not answer; it can be tried again", false
	}
}

func releaseInvitationOnion(ctx context.Context, d *daemon, logger *slog.Logger, address string) {
	if d == nil || d.backendClient == nil || address == "" {
		return
	}
	if err := d.backendClient.DelOnion(ctx, address); err != nil {
		logger.Warn("invitation: the address minted for this accept was not released",
			slog.String("reason", reasonDelOnionFailed),
		)
		return
	}
	logger.Debug("invitation: the address minted for this accept was released")
}

func releaseInvitationClaim(d *daemon, logger *slog.Logger, msgID string) {
	if d == nil || d.invitations == nil || msgID == "" {
		return
	}
	if err := d.invitations.Release(msgID); err != nil {
		logger.Warn("invitation: the accept claim was not released; this invitation can no longer be answered",
			slog.Any("err", err),
		)
		return
	}
	logger.Debug("invitation: the accept claim was released — the invitation can be answered again")
}
