package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/files"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/store"
	"haoma-frontend/internal/syncrail"
)

const railAppliedPrefix = "rail-applied:"

func processSiblingRailEnvelope(ctx context.Context, d *daemon, entry backendapi.InboxEntry) {
	logger := slog.With(
		slog.String("envelope_id", entry.Envelope.ID),
		slog.String("sibling_peer_id", entry.PeerID),
	)
	logger.Debug("rail: sibling envelope observed",
		slog.Int("payload_bytes", len(entry.Envelope.Payload)),
	)

	key, err := d.railKey()
	if err != nil {
		logger.Warn("rail: drop — no rail key",
			slog.String("reason", "no_rail_key"),
			slog.Any("err", err),
		)
		return
	}

	env, err := syncrail.OpenEnvelope(key, entry.Envelope.Payload)
	if err != nil {

		logger.Warn("rail: drop — envelope did not open",
			slog.String("reason", railOpenDropReason(err)),
			slog.Any("err", err),
		)
		return
	}
	logger.Debug("rail: envelope opened",
		slog.String("rail_kind", string(env.Kind)),
		slog.Int64("rail_ts", env.Ts),
	)

	noteSending(d, entry)

	switch env.Kind {
	case syncrail.KindGapRequest:
		req, pErr := syncrail.ParseGapRequest(env)
		if pErr != nil {
			logger.Debug("rail: drop — gap request unusable",
				slog.String("reason", "bad_gap_request"),
				slog.Any("err", pErr),
			)
			return
		}
		go handleGapRequest(ctx, d, entry.PeerID, req)
		return

	case syncrail.KindGapResponse:
		resp, pErr := syncrail.ParseGapResponse(env)
		if pErr != nil {
			logger.Debug("rail: drop — gap response unusable",
				slog.String("reason", "bad_gap_response"),
				slog.Any("err", pErr),
			)
			return
		}
		go handleGapResponse(ctx, d, entry.PeerID, resp)
		return

	case syncrail.KindBackfillRequest:
		req, pErr := syncrail.ParseBackfillRequest(env)
		if pErr != nil {
			logger.Debug("rail: drop — backfill request unusable",
				slog.String("reason", "bad_backfill_request"),
				slog.Any("err", pErr),
			)
			return
		}
		go handleBackfillRequest(ctx, d, entry.PeerID, req)
		return

	case syncrail.KindBackfillResponse:
		resp, pErr := syncrail.ParseBackfillResponse(env)
		if pErr != nil {
			logger.Debug("rail: drop — backfill response unusable",
				slog.String("reason", "bad_backfill_response"),
				slog.Any("err", pErr),
			)
			return
		}
		go handleBackfillResponse(ctx, d, entry.PeerID, resp)
		return

	case syncrail.KindChatSettings:
		body, pErr := syncrail.ParseChatSettings(env)
		if pErr != nil {
			logger.Debug("rail: drop — chat settings unusable",
				slog.String("reason", "bad_chat_settings"),
				slog.Any("err", pErr),
			)
			return
		}

		go handleChatSettingsPush(ctx, d, entry.PeerID, body)
		return

	case syncrail.KindContacts:
		body, pErr := syncrail.ParseContacts(env)
		if pErr != nil {
			logger.Debug("rail: drop — contacts unusable",
				slog.String("reason", "bad_contacts"),
				slog.Any("err", pErr),
			)
			return
		}

		go handleContactsPush(ctx, d, entry.PeerID, body)
		return

	case syncrail.KindSelfNick:
		body, pErr := syncrail.ParseSelfNick(env)
		if pErr != nil {
			logger.Debug("rail: drop — self-nick unusable",
				slog.String("reason", "bad_self_nick"),
				slog.Any("err", pErr),
			)
			return
		}

		go handleSelfNickPush(ctx, d, entry.PeerID, body)
		return

	case syncrail.KindDestroySalt:
		body, salt, pErr := syncrail.ParseDestroySalt(env)
		if pErr != nil {

			logger.Debug("rail: drop — destroy salt unusable",
				slog.String("reason", "bad_destroy_salt"),
				slog.Any("err", pErr),
			)
			return
		}

		go applyDestroySalt(ctx, d, entry.PeerID, body, salt)
		return

	case syncrail.KindInvitation:
		body, pErr := syncrail.ParseInvitationBody(env)
		if pErr != nil {

			logger.Debug("rail: drop — invitation unusable",
				slog.String("reason", "bad_invitation"),
				slog.Any("err", pErr),
			)
			return
		}

		go forwardInvitation(ctx, d, entry.PeerID, body)
		return

	case syncrail.KindFileRequest:
		body, pErr := syncrail.ParseFileRequest(env)
		if pErr != nil {
			logger.Debug("rail: drop — file request unusable",
				slog.String("reason", "bad_file_request"),
				slog.Any("err", pErr),
			)
			return
		}

		handleFileRequest(d, entry.PeerID, body)
		return

	case syncrail.KindGapSiblingOffer:
		body, pErr := syncrail.ParseGapSiblingOffer(env)
		if pErr != nil {

			logger.Debug("rail: drop — sibling offer on gap-fill unusable",
				slog.String("reason", "bad_gap_sibling_offer"),
				slog.Any("err", pErr),
			)
			return
		}

		go handleGapSiblingOffer(ctx, d, entry.PeerID, body)
		return

	case syncrail.KindContactDelete:
		body, pErr := syncrail.ParseContactDelete(env)
		if pErr != nil {
			logger.Debug("rail: drop — contact delete unusable",
				slog.String("reason", "bad_contact_delete"),
				slog.Any("err", pErr),
			)
			return
		}

		go handleContactDeletePush(ctx, d, entry.PeerID, body)
		return

	case syncrail.KindCallSummary:
		body, pErr := syncrail.ParseCallSummary(env)
		if pErr != nil {
			logger.Debug("rail: drop — call summary unusable",
				slog.String("reason", "bad_call_summary"),
				slog.Any("err", pErr),
			)
			return
		}

		applyCallSummaryPush(ctx, d, logger, body)
		return
	}

	body, err := syncrail.ParseMirrorBody(env)
	if err != nil {
		logger.Debug("rail: drop — mirror body unusable",
			slog.String("reason", "bad_mirror_body"),
			slog.Any("err", err),
		)
		return
	}

	wrapper, err := msg.Unmarshal(body.Wrapper)
	if err != nil {
		logger.Debug("rail: drop — mirrored wrapper will not parse",
			slog.String("reason", "wrapper_parse_failed"),
			slog.String("declared_wire_kind", body.WireKind),
			slog.Any("err", err),
		)
		return
	}

	if body.MsgID != wrapper.MsgID {
		logger.Warn("rail: mirror body msg_id disagrees with the sealed wrapper; trusting the wrapper",
			slog.String("body_msg_id", body.MsgID),
			slog.String("wrapper_msg_id", wrapper.MsgID),
		)
	}

	logger = logger.With(
		slog.String("wire_kind", string(wrapper.Kind)),
		slog.String("msg_id", wrapper.MsgID),
	)

	applied, err := railMirrorApplied(d.store, wrapper.MsgID)
	if err != nil {
		logger.Warn("rail: drop — applied-ledger read failed",
			slog.String("reason", "ledger_read_failed"),
			slog.Any("err", err),
		)
		return
	}
	if applied {
		logger.Debug("rail: mirror already applied — no state change (idempotent replay)",
			slog.String("outcome", "noop_duplicate"),
		)
		return
	}

	chatID, localPeerID, err := resolveChatByFingerprint(d, body.Fingerprint)
	if err != nil && gapRowRetryable(err) {

		logger.Debug(railMirrorUnplacedLine,
			slog.String("reason", railResolveDropReason(err)),
			slog.String("fingerprint", body.Fingerprint),
			slog.String("origin_chat_id", body.OriginChatID),
			slog.String("origin_peer_id", body.OriginPeerID),
		)
		d.unplacedSweep.ask(ctx, d)
		return
	}
	if err != nil {
		logger.Debug("rail: drop — no local conversation for this fingerprint",
			slog.String("reason", railResolveDropReason(err)),
			slog.String("fingerprint", body.Fingerprint),
			slog.String("origin_chat_id", body.OriginChatID),
			slog.String("origin_peer_id", body.OriginPeerID),
			slog.Any("err", err),
		)
		return
	}
	logger = logger.With(
		slog.String("chat_id", string(chatID)),
		slog.String("local_peer_id", localPeerID),
	)
	logger.Debug("rail: mirror resolved to a local chat by fingerprint",
		slog.Int64("origin_display_ts", body.DisplayTs),
	)

	var outcome mirrorOutcome
	if body.SiblingOffer != nil {
		outcome = applySiblingOffer(ctx, d, logger, chatID, entry.PeerID, wrapper, *body.SiblingOffer, body.DisplayTs)
	} else {
		outcome = applyMirroredMutation(ctx, d, logger, chatID, wrapper, body.DisplayTs)
	}
	switch outcome {
	case mirrorNoEffect:

		logger.Debug("rail: mirror observed but caused no state change",
			slog.String("outcome", "noop_no_effect"),
		)
		return

	case mirrorHeld:

		logger.Debug("rail: mirror already held — no second row",
			slog.String("outcome", "noop_duplicate"),
		)

	case mirrorApplied:
		logger.Debug("rail: mirror applied — local state changed",
			slog.String("outcome", "applied"),
		)
	}
	if err := markRailMirrorApplied(d.store, wrapper.MsgID); err != nil {

		logger.Warn("rail: applied-ledger write failed; a replay of this mirror will re-apply",
			slog.Any("err", err),
		)
	}
}

type mirrorOutcome int

const (
	mirrorNoEffect mirrorOutcome = iota

	mirrorApplied

	mirrorHeld
)

func applyMirroredMutation(
	ctx context.Context,
	d *daemon,
	logger *slog.Logger,
	chatID chat.ChatID,
	wrapper *msg.Wrapper,
	originDisplayTs int64,
) mirrorOutcome {
	switch wrapper.Kind {
	case msg.KindText:
		return applyMirroredText(ctx, d, logger, chatID, wrapper, originDisplayTs)

	case msg.KindEdit:
		body, err := wrapper.Edit()
		if err != nil {
			logger.Debug("rail: mirrored edit body parse failed", slog.Any("err", err))
			return mirrorNoEffect
		}

		if _, err := d.events.ApplyEdit(body.Target, body.Text, wrapper.Ts, ""); err != nil {
			return logMirrorApplyDecline(logger, "edit", body.Target, err)
		}
		logger.Debug("rail: mirrored edit applied", slog.String("target_msg_id", body.Target))
		return mirrorApplied

	case msg.KindDelete:
		body, err := wrapper.Delete()
		if err != nil {
			logger.Debug("rail: mirrored delete body parse failed", slog.Any("err", err))
			return mirrorNoEffect
		}
		if _, err := d.events.ApplyDelete(body.Target, wrapper.Ts, ""); err != nil {
			return logMirrorApplyDecline(logger, "delete", body.Target, err)
		}
		logger.Debug("rail: mirrored delete applied", slog.String("target_msg_id", body.Target))
		return mirrorApplied

	case msg.KindReaction:
		body, err := wrapper.Reaction()
		if err != nil {
			logger.Debug("rail: mirrored reaction body parse failed", slog.Any("err", err))
			return mirrorNoEffect
		}

		clampedTs := events.ClampSenderTs(wrapper.Ts, time.Now().Unix())
		if _, err := d.events.AppendReactionBreadcrumb(body.Target, body.Emoji, "", wrapper.MsgID, clampedTs, wrapper.ExpireSeconds); err != nil {
			return logMirrorApplyDecline(logger, "reaction", body.Target, err)
		}
		logger.Debug("rail: mirrored reaction applied", slog.String("target_msg_id", body.Target))
		return mirrorApplied

	case msg.KindFileOffer:
		return applyMirroredFileOffer(ctx, d, logger, chatID, wrapper, originDisplayTs)

	case msg.KindFileReceipt, msg.KindFileKey, msg.KindFileDelivered:

		logger.Debug("rail: mirrored file handshake step has no sibling-local effect (per-device transfer)")
		return mirrorNoEffect

	default:
		logger.Debug("rail: drop — mirrored wire kind is not applicable here",
			slog.String("reason", "unknown_wire_kind"),
		)
		return mirrorNoEffect
	}
}

func applyMirroredText(
	ctx context.Context,
	d *daemon,
	logger *slog.Logger,
	chatID chat.ChatID,
	wrapper *msg.Wrapper,
	originDisplayTs int64,
) mirrorOutcome {
	textBody, err := wrapper.Text()
	if err != nil {
		logger.Debug("rail: mirrored text body parse failed", slog.Any("err", err))
		return mirrorNoEffect
	}
	var replyTo *events.ReplyToSnapshot
	if textBody.ReplyTo != nil {
		replyTo = &events.ReplyToSnapshot{MsgID: textBody.ReplyTo.MsgID, Text: textBody.ReplyTo.Text}
	}
	raw, err := json.Marshal(events.TextBody{Text: textBody.Text, ReplyTo: replyTo})
	if err != nil {
		logger.Warn("rail: marshal mirrored text body failed", slog.Any("err", err))
		return mirrorNoEffect
	}
	out, err := d.events.AppendOutbound(events.OutboundParams{
		ChatID:    chatID,
		Kind:      events.KindText,
		SenderSeq: wrapper.Seq,
		MsgID:     wrapper.MsgID,

		DisplayTs:           originDisplayTs,
		ExpireSeconds:       wrapper.ExpireSeconds,
		Body:                raw,
		MirroredFromSibling: true,
	})
	if errors.Is(err, events.ErrDuplicateMsgID) {

		logger.Debug("rail: mirrored text already held — no second row",
			slog.String("outcome", "noop_duplicate"),
		)
		return mirrorHeld
	}
	if err != nil {
		logger.Error("rail: persist mirrored text failed", slog.Any("err", err))
		return mirrorNoEffect
	}

	bumpChatActivity(ctx, d, chatID, time.Now().Unix())
	logger.Debug("rail: mirrored text appended as an outbound row",
		slog.Int("text_bytes", len(textBody.Text)),
		slog.String("delivery_state", out.DeliveryState),
	)
	return mirrorApplied
}

func applyMirroredFileOffer(
	ctx context.Context,
	d *daemon,
	logger *slog.Logger,
	chatID chat.ChatID,
	wrapper *msg.Wrapper,
	originDisplayTs int64,
) mirrorOutcome {
	return appendMirroredFileRow(ctx, d, logger, chatID, wrapper, originDisplayTs, true)
}

func appendMirroredFileRow(
	ctx context.Context,
	d *daemon,
	logger *slog.Logger,
	chatID chat.ChatID,
	wrapper *msg.Wrapper,
	originDisplayTs int64,
	withCapability bool,
) mirrorOutcome {
	offer, err := wrapper.FileOffer()
	if err != nil {
		logger.Debug("rail: mirrored file_offer body parse failed", slog.Any("err", err))
		return mirrorNoEffect
	}
	token, urlPath := offer.Token, offer.UrlPath
	if !withCapability {
		token, urlPath = "", ""
	}
	raw, err := json.Marshal(FileEventBody{
		Token:            token,
		UrlPath:          urlPath,
		Name:             offer.Name,
		Size:             offer.Size,
		Mime:             offer.Mime,
		Sha256Ciphertext: offer.Sha256Ciphertext,
		State:            string(files.StatePending),
		Caption:          offer.Caption,
	})
	if err != nil {
		logger.Warn("rail: marshal mirrored file body failed", slog.Any("err", err))
		return mirrorNoEffect
	}
	out, err := d.events.AppendOutbound(events.OutboundParams{
		ChatID:    chatID,
		Kind:      events.KindFile,
		SenderSeq: wrapper.Seq,
		MsgID:     wrapper.MsgID,

		DisplayTs:           originDisplayTs,
		ExpireSeconds:       wrapper.ExpireSeconds,
		Body:                raw,
		MirroredFromSibling: true,
	})
	if errors.Is(err, events.ErrDuplicateMsgID) {
		logger.Debug("rail: mirrored file offer already held — no second row",
			slog.String("outcome", "noop_duplicate"),
		)
		return mirrorHeld
	}
	if err != nil {
		logger.Error("rail: persist mirrored file row failed", slog.Any("err", err))
		return mirrorNoEffect
	}
	bumpChatActivity(ctx, d, chatID, time.Now().Unix())

	noteWantedFile(d, logger, chatID, wrapper.MsgID, out.DisplayTs, "mirror")

	logger.Debug("rail: mirrored file offer appended as an outbound row",
		slog.Uint64("size", offer.Size),
		slog.String("mime", offer.Mime),
		slog.String("delivery_state", out.DeliveryState),
		slog.String("transfer_state", string(files.StatePending)),
	)
	return mirrorApplied
}

var (
	errRailNoPeerForFingerprint = errors.New("rail: no local peer carries this fingerprint")
	errRailNoChatForPeer        = errors.New("rail: local peer has no direct chat")
)

func resolveChatByFingerprint(d *daemon, fingerprint string) (chat.ChatID, string, error) {
	if d == nil || d.peerMeta == nil || d.chats == nil {
		return "", "", errors.New("rail: peer-meta or chat store not wired")
	}
	peerIDs, err := d.peerMeta.PeerIDsByFingerprint(fingerprint)
	if err != nil {
		return "", "", err
	}
	for _, pid := range peerIDs {
		dc, err := d.chats.GetByDirectPeer(pid)
		if err == nil {
			return dc.ID, pid, nil
		}
		if !errors.Is(err, chat.ErrNotFound) {
			return "", "", fmt.Errorf("rail: chat lookup for %s: %w", pid, err)
		}
	}

	if dc, err := d.chats.GetByFingerprint(fingerprint); err == nil {
		return dc.ID, "", nil
	} else if !errors.Is(err, chat.ErrNotFound) {
		return "", "", fmt.Errorf("rail: chat-by-person lookup: %w", err)
	}
	if len(peerIDs) == 0 {
		return "", "", errRailNoPeerForFingerprint
	}
	return "", "", errRailNoChatForPeer
}

const railMirrorUnplacedLine = "rail: a mirror names a person with no conversation here yet; a sweep is asked for"

var (
	unplacedSweepSettle = 2 * time.Second
	unplacedSweepGap    = 30 * time.Second
	unplacedSweepMaxGap = 10 * time.Minute
	unplacedSweepRetry  = 10 * time.Second
)

type unplacedMirrorSweep struct {
	mu    sync.Mutex
	armed bool
	last  time.Time
	gap   time.Duration
}

func (u *unplacedMirrorSweep) nextWait(now time.Time) time.Duration {
	if u.gap == 0 || u.last.IsZero() || now.Sub(u.last) >= 2*u.gap {
		u.gap = unplacedSweepGap
	} else {
		u.gap *= 2
		if u.gap > unplacedSweepMaxGap {
			u.gap = unplacedSweepMaxGap
		}
	}
	wait := unplacedSweepSettle
	if u.last.IsZero() {
		return wait
	}
	if next := u.last.Add(u.gap).Sub(now); next > wait {
		wait = next
	}
	return wait
}

func (u *unplacedMirrorSweep) ask(ctx context.Context, d *daemon) {
	u.mu.Lock()
	if u.armed {
		u.mu.Unlock()
		return
	}
	u.armed = true
	wait := u.nextWait(time.Now())
	u.mu.Unlock()
	go func() {
		defer func() {
			u.mu.Lock()
			u.armed = false
			u.mu.Unlock()
		}()
		for attempt := 0; attempt < 3; attempt++ {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			u.mu.Lock()
			u.last = time.Now()
			u.mu.Unlock()
			outcome := sweepGapFill(ctx, d)
			slog.Debug("rail: the sweep a mirror for an unknown person asked for",
				slog.String("outcome", string(outcome)),
				slog.Int("attempt", attempt+1),
			)
			if outcome != sweepAlreadyRunning {
				return
			}
			wait = unplacedSweepRetry
		}
	}()
}

func railMirrorApplied(st *store.Store, msgID string) (bool, error) {
	if st == nil || msgID == "" {
		return false, errors.New("rail: applied-ledger needs a store and a msg id")
	}
	_, err := st.Get([]byte(railAppliedPrefix + msgID))
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func markRailMirrorApplied(st *store.Store, msgID string) error {
	if st == nil || msgID == "" {
		return errors.New("rail: applied-ledger needs a store and a msg id")
	}
	return st.Put([]byte(railAppliedPrefix+msgID), []byte{1})
}

func railOpenDropReason(err error) string {
	switch {
	case errors.Is(err, syncrail.ErrShortCiphertext):
		return "short_ciphertext"
	case errors.Is(err, syncrail.ErrVersionMismatch):
		return "rail_version_mismatch"
	case errors.Is(err, syncrail.ErrUnknownKind):
		return "unknown_rail_kind"
	default:
		return "aead_open_failed"
	}
}

func railResolveDropReason(err error) string {
	switch {
	case errors.Is(err, errRailNoPeerForFingerprint):
		return "unknown_fingerprint"
	case errors.Is(err, errRailNoChatForPeer):
		return "no_chat_for_peer"
	default:
		return "resolve_failed"
	}
}

func logMirrorApplyDecline(logger *slog.Logger, op, target string, err error) mirrorOutcome {
	switch {
	case errors.Is(err, events.ErrEventNotFound):
		logger.Debug("rail: mirrored mutation target missing (swept or out-of-order)",
			slog.String("op", op),
			slog.String("target_msg_id", target),
		)
	case errors.Is(err, events.ErrDuplicateMsgID):

		logger.Debug("rail: mirrored breadcrumb already held — no second row",
			slog.String("op", op),
			slog.String("target_msg_id", target),
			slog.String("outcome", "noop_duplicate"),
		)
		return mirrorHeld
	default:
		logger.Warn("rail: mirrored mutation refused by the event log",
			slog.String("op", op),
			slog.String("target_msg_id", target),
			slog.Any("err", err),
		)
	}
	return mirrorNoEffect
}
