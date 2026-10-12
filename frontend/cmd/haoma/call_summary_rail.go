package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/syncrail"
)

func pushCallSummary(d *daemon, ev events.Event) {
	if d == nil || ev.MsgID == "" || ev.Direction != events.DirOut || ev.Kind != events.KindCallSummary {
		return
	}
	sibs := d.siblings()
	if len(sibs) == 0 {

		return
	}
	fp := fingerprintForChat(d, ev.ChatID)
	if fp == "" {

		slog.Debug("call summary: conversation names no person; not sent to my other devices",
			slog.String("chat_id", string(ev.ChatID)),
			slog.String("msg_id", ev.MsgID),
		)
		return
	}
	body := syncrail.CallSummaryBody{
		Fingerprint:   fp,
		MsgID:         ev.MsgID,
		DisplayTs:     ev.DisplayTs,
		ExpireSeconds: ev.ExpireSeconds,
		Summary:       ev.Body,
	}
	ctx := d.relayContext()
	go func() {
		for _, sib := range sibs {
			if ctx.Err() != nil {
				return
			}
			envelopeID, err := sendGapEnvelopeID(ctx, d, sib, syncrail.KindCallSummary, body)
			if err != nil {

				slog.Debug("call summary: push failed; the sweep will carry it",
					slog.String("sibling_peer_id", sib),
					slog.String("msg_id", ev.MsgID),
					slog.Any("err", err),
				)
				continue
			}
			slog.Debug("call summary: sent to my other device",
				slog.String("sibling_peer_id", sib),
				slog.String("msg_id", ev.MsgID),
				slog.String("envelope_id", envelopeID),
			)
		}
	}()
}

func applyCallSummaryPush(ctx context.Context, d *daemon, logger *slog.Logger, body syncrail.CallSummaryBody) {
	logger = logger.With(slog.String("msg_id", body.MsgID))
	chatID, _, err := resolveChatByFingerprint(d, body.Fingerprint)
	if err != nil && gapRowRetryable(err) {
		logger.Debug("rail: a call summary names a person with no conversation here yet; a sweep is asked for",
			slog.String("reason", railResolveDropReason(err)),
			slog.String("fingerprint", body.Fingerprint),
		)
		d.unplacedSweep.ask(ctx, d)
		return
	}
	if err != nil {
		logger.Debug("rail: drop — no local conversation for this call summary",
			slog.String("reason", railResolveDropReason(err)),
			slog.String("fingerprint", body.Fingerprint),
			slog.Any("err", err),
		)
		return
	}
	logger = logger.With(slog.String("chat_id", string(chatID)))

	displayTs := body.DisplayTs
	if displayTs == 0 {
		displayTs = time.Now().Unix()
	}
	inserted, err := appendSyncedCallSummary(d, chatID, body.MsgID, displayTs, body.ExpireSeconds, body.Summary)
	if errors.Is(err, errSyncedCallSummaryExpired) {

		logger.Debug("rail: call summary already expired — not written",
			slog.String("outcome", "noop_expired"),
		)
		return
	}
	if err != nil {
		logger.Warn("rail: call summary not written",
			slog.String("reason", "call_summary_refused"),
			slog.Any("err", err),
		)
		return
	}
	if !inserted {

		logger.Debug("rail: call summary already held — no second row",
			slog.String("outcome", "noop_duplicate"),
		)
		return
	}
	bumpChatActivity(ctx, d, chatID, displayTs)
	logger.Debug("rail: call summary applied — local state changed",
		slog.String("outcome", "applied"),
		slog.Int64("display_ts", displayTs),
	)
}

var errSyncedCallSummaryNotOutgoing = errors.New("call summary from my other device is not of an outgoing call")

var errSyncedCallSummaryExpired = errors.New("call summary from my other device has already expired")

func appendSyncedCallSummary(d *daemon, chatID chat.ChatID, msgID string, displayTs int64, expireSeconds uint32, summary json.RawMessage) (bool, error) {
	if msgID == "" {
		return false, errors.New("call summary has no msg id")
	}
	var cs events.CallSummaryBody
	if err := json.Unmarshal(summary, &cs); err != nil {
		return false, fmt.Errorf("call summary body: %w", err)
	}
	if cs.CallID == "" {
		return false, errors.New("call summary names no call")
	}
	if cs.Direction != string(events.DirOut) {
		return false, fmt.Errorf("%w (direction %q)", errSyncedCallSummaryNotOutgoing, cs.Direction)
	}
	if expireSeconds > 0 && displayTs > 0 && displayTs+int64(expireSeconds) <= time.Now().Unix() {
		return false, errSyncedCallSummaryExpired
	}
	_, err := d.events.AppendLocal(events.LocalParams{
		ChatID:        chatID,
		Kind:          events.KindCallSummary,
		Direction:     events.DirOut,
		DisplayTs:     displayTs,
		MsgID:         msgID,
		ExpireSeconds: expireSeconds,
		Body:          summary,
	})
	if errors.Is(err, events.ErrDuplicateMsgID) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
