package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"haoma-frontend/internal/calls"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/msg"
)

func ingestCallOffer(ctx context.Context, d *daemon, chatID chat.ChatID, peerID, callID string, modalities []string, peerOutboundKey []byte, peerTokens map[string]string, offerTs int64) {
	if d.calls == nil {
		slog.Warn("call_offer dropped: calls manager not wired",
			slog.String("call_id", callID),
			slog.String("peer_id", peerID),
		)
		return
	}
	if existing, err := d.calls.GetState(callID); err == nil {
		slog.Debug("call_offer ignored: state already exists",
			slog.String("call_id", callID),
			slog.String("peer_id", peerID),
			slog.String("existing_status", string(existing.Status)),
		)
		return
	}
	if callOfferTooOld(offerTs, time.Now()) {
		slog.Info("call_offer dropped: older than the offer age limit; nothing sent",
			slog.String("call_id", callID),
			slog.String("peer_id", peerID),
			slog.Int64("age_s", time.Now().Unix()-offerTs),
			slog.Duration("max_age", callOfferMaxAge),
		)
		return
	}
	state := calls.State{
		CallID:            callID,
		ChatID:            chatID,
		PeerID:            peerID,
		Direction:         calls.DirIn,
		Status:            calls.StatusRinging,
		Modalities:        modalities,
		StartedAt:         time.Now().Unix(),
		RemoteOutboundKey: append([]byte(nil), peerOutboundKey...),
		RemoteTokens:      peerTokens,
	}
	if err := d.calls.PutState(state); err != nil {
		slog.Error("call_offer persist failed",
			slog.String("call_id", callID),
			slog.String("peer_id", peerID),
			slog.Any("err", err),
		)
		return
	}
	slog.Info("call_offer ringing",
		slog.String("call_id", callID),
		slog.String("peer_id", peerID),
		slog.String("chat_id", string(chatID)),
		slog.Any("modalities", modalities),
		slog.Bool("has_remote_key", len(peerOutboundKey) > 0),
		slog.Int("remote_token_count", len(peerTokens)),
	)
	broadcastCallStateChanged(d, state)
	scheduleCalleeRingTimeout(d, callID)
	_ = ctx
}

func applyCallTransition(ctx context.Context, d *daemon, fromPeerID, callID string, next calls.Status, reason, fromKind string) {
	if d.calls == nil {
		slog.Warn("call transition dropped: calls manager not wired",
			slog.String("call_id", callID),
			slog.String("kind", fromKind),
		)
		return
	}
	cur, err := d.calls.GetState(callID)
	if err != nil {
		if errors.Is(err, calls.ErrCallNotFound) {
			slog.Info("inbound call op: state missing (already cleaned or never seen)",
				slog.String("call_id", callID),
				slog.String("kind", fromKind),
			)
			return
		}
		slog.Error("inbound call op: state lookup failed",
			slog.String("call_id", callID),
			slog.String("kind", fromKind),
			slog.Any("err", err),
		)
		return
	}
	if !cur.IsTarget(fromPeerID) {
		slog.Warn("inbound call op from a device the call does not belong to; dropped",
			slog.String("call_id", callID),
			slog.String("kind", fromKind),
			slog.String("from_peer_id", fromPeerID),
			slog.String("direction", string(cur.Direction)),
		)
		return
	}

	var endOthers []string
	if cur.Direction == calls.DirOut {
		switch {
		case cur.AnsweredBy != "" && fromPeerID != cur.AnsweredBy:
			slog.Info("inbound call op from a device that did not answer; dropped",
				slog.String("call_id", callID),
				slog.String("kind", fromKind),
				slog.String("from_peer_id", fromPeerID),
			)
			return
		case cur.AnsweredBy == "" && cur.Status == calls.StatusOffered && next == calls.StatusEnded:
			stopped, merr := d.calls.MarkStopped(callID, fromPeerID)
			if merr != nil {
				slog.Warn("inbound call_end: recording the device that stopped ringing failed",
					slog.String("call_id", callID),
					slog.String("from_peer_id", fromPeerID),
					slog.Any("err", merr),
				)
				return
			}
			if ringing := stopped.Ringing(); len(ringing) > 0 {
				slog.Info("inbound call_end: one device stopped ringing; the others still ring",
					slog.String("call_id", callID),
					slog.String("from_peer_id", fromPeerID),
					slog.Int("still_ringing", len(ringing)),
				)
				return
			}
			slog.Info("inbound call_end: no device rings any more; the call ends",
				slog.String("call_id", callID),
				slog.String("from_peer_id", fromPeerID),
			)
		case cur.AnsweredBy == "" && next == calls.StatusRejected:
			endOthers = cur.RingingExcept(fromPeerID)
		}
	}

	state, err := d.calls.Transition(callID, next, reason, time.Now().Unix())
	if err != nil {
		switch {
		case errors.Is(err, calls.ErrCallNotFound):
			slog.Info("inbound call op: state missing (already cleaned or never seen)",
				slog.String("call_id", callID),
				slog.String("kind", fromKind),
			)
		case errors.Is(err, calls.ErrIllegalTransition):
			slog.Warn("inbound call op: illegal transition; dropping",
				slog.String("call_id", callID),
				slog.String("kind", fromKind),
				slog.String("requested", string(next)),
				slog.Any("err", err),
			)
		default:
			slog.Error("inbound call transition failed",
				slog.String("call_id", callID),
				slog.String("kind", fromKind),
				slog.Any("err", err),
			)
		}
		return
	}
	slog.Info("call state changed (inbound)",
		slog.String("call_id", callID),
		slog.String("status", string(state.Status)),
		slog.String("reason", reason),
	)
	broadcastCallStateChanged(d, state)
	if len(endOthers) > 0 {

		endCallOnDevices(ctx, d, callID, endOthers, "", "declined on another device")
	}
	if state.IsTerminal() {
		callTeardown(d, state)
	}
}

func applyCallAccept(ctx context.Context, d *daemon, fromPeerID, callID string, peerOutboundKey []byte, peerTokens map[string]string) {
	if d.calls == nil {
		slog.Warn("call_accept dropped: calls manager not wired",
			slog.String("call_id", callID),
		)
		return
	}
	state, err := d.calls.GetState(callID)
	if err != nil {
		if errors.Is(err, calls.ErrCallNotFound) {
			slog.Info("call_accept: state missing", slog.String("call_id", callID))
			return
		}
		slog.Error("call_accept: state lookup failed", slog.String("call_id", callID), slog.Any("err", err))
		return
	}
	if state.Direction != calls.DirOut {
		slog.Warn("call_accept arrived on non-outbound row; ignoring",
			slog.String("call_id", callID),
			slog.String("direction", string(state.Direction)),
		)
		return
	}
	if !state.IsTarget(fromPeerID) {
		slog.Warn("call_accept from a device the call was not offered to; dropped",
			slog.String("call_id", callID),
			slog.String("from_peer_id", fromPeerID),
		)
		return
	}
	claimed, won, err := claimCallAnswer(d.calls, callID, fromPeerID)
	if err != nil {

		slog.Error("call_accept: claim failed; sending that device an end",
			slog.String("call_id", callID),
			slog.String("from_peer_id", fromPeerID),
			slog.Any("err", err),
		)
		endCallOnDevices(ctx, d, callID, []string{fromPeerID}, "", "answer could not be recorded")
		return
	}
	if !won {
		answerLost(ctx, d, claimed, fromPeerID)
		return
	}
	slog.Info("call_accept: first answer; this device takes the call",
		slog.String("call_id", callID),
		slog.String("from_peer_id", fromPeerID),
	)

	endCallOnDevices(ctx, d, callID, claimed.RingingExcept(fromPeerID), msg.CallEndAnsweredElsewhere, "answered on another device")

	if len(peerOutboundKey) == 0 || len(peerTokens) == 0 {
		failAnsweredCall(ctx, d, callID, fromPeerID, calls.FailReasonInvalidOffer+": peer accept missing tokens / outbound_key (pre-1f peer)")
		return
	}
	if err := d.calls.SetRemoteMaterial(callID, peerOutboundKey, peerTokens); err != nil {
		slog.Warn("call_accept: SetRemoteMaterial failed",
			slog.String("call_id", callID),
			slog.Any("err", err),
		)
	}

	state, err = d.calls.GetState(callID)
	if err != nil {
		slog.Error("call_accept: state re-read failed", slog.Any("err", err))
		failAnsweredCall(ctx, d, callID, fromPeerID, calls.FailReasonStreamerSpawn+": state re-read failed")
		return
	}

	for _, modality := range state.Modalities {
		peerToken := peerTokens[modality]

		if _, serr := spawnCallReceiver(ctx, d, callID, fromPeerID, modality, peerToken, peerOutboundKey); serr != nil {
			slog.Warn("call_accept: receiving streamer failed to start",
				slog.String("call_id", callID),
				slog.String("modality", modality),
				slog.Any("err", serr),
			)

			failAnsweredCall(ctx, d, callID, fromPeerID, calls.FailReasonStreamerSpawn+": receiver")
			return
		}
	}

	updated, err := transitionCall(d.calls, callID, calls.StatusAccepted, "", time.Now().Unix())
	if err != nil {
		if errors.Is(err, calls.ErrIllegalTransition) {

			slog.Warn("call_accept: row moved on while the media came up; tearing down",
				slog.String("call_id", callID),
				slog.Any("err", err),
			)
			teardownCall(d, state)
			return
		}
		slog.Error("call_accept: accepted state could not be written; failing the call",
			slog.String("call_id", callID),
			slog.Any("err", err),
		)
		failAnsweredCall(ctx, d, callID, fromPeerID, calls.FailReasonPersist)
		return
	}
	broadcastCallStateChanged(d, updated)
	slog.Info("call_accept: accepted, audio flowing",
		slog.String("call_id", callID),
		slog.String("peer_id", fromPeerID),
	)
}

func answerLost(ctx context.Context, d *daemon, s calls.State, fromPeerID string) {
	if fromPeerID == s.AnsweredBy || (s.AnsweredBy == "" && s.Status == calls.StatusAccepted && fromPeerID == s.PeerID) {
		slog.Debug("call_accept: repeat from the device that answered; ignored",
			slog.String("call_id", s.CallID),
			slog.String("from_peer_id", fromPeerID),
		)
		return
	}
	reason, why := "", "accept after the call ended"
	if s.AnsweredBy != "" {
		reason, why = msg.CallEndAnsweredElsewhere, "late accept, answered on another device"
	}
	slog.Info("call_accept: another device answered first, or the call is over; sending end",
		slog.String("call_id", s.CallID),
		slog.String("from_peer_id", fromPeerID),
		slog.String("status", string(s.Status)),
		slog.Bool("answered", s.AnsweredBy != ""),
	)
	endCallOnDevices(ctx, d, s.CallID, []string{fromPeerID}, reason, why)
}

func failAnsweredCall(ctx context.Context, d *daemon, callID, winner, reason string) {
	failed, err := transitionCall(d.calls, callID, calls.StatusFailed, reason, time.Now().Unix())
	if err != nil {
		if errors.Is(err, calls.ErrIllegalTransition) {
			slog.Info("call_accept: row already moved on; no fail needed",
				slog.String("call_id", callID),
			)
			return
		}
		slog.Error("call_accept: Failed state could not be written; tearing down and ending anyway",
			slog.String("call_id", callID),
			slog.Any("err", err),
		)
		cur, gerr := d.calls.GetState(callID)
		if gerr != nil {
			cur = calls.State{CallID: callID}
		}
		endCallOnDevices(ctx, d, callID, []string{winner}, "", "call failed after the answer")
		callTeardown(d, cur)
		return
	}
	broadcastCallStateChanged(d, failed)

	endCallOnDevices(ctx, d, callID, callRecipients(failed), "", "call failed after the answer")
	callTeardown(d, failed)
}

func callStateToEntry(s calls.State) ipc.CallEntry {
	return ipc.CallEntry{
		CallID:     s.CallID,
		ChatID:     string(s.ChatID),
		PeerID:     s.PeerID,
		Direction:  string(s.Direction),
		Status:     string(s.Status),
		Modalities: s.Modalities,
		StartedAt:  s.StartedAt,
		UpdatedAt:  s.UpdatedAt,
		EndedAt:    s.EndedAt,
		FailReason: s.FailReason,
	}
}

func broadcastCallStateChanged(d *daemon, state calls.State) {

	if state.Direction == calls.DirOut && state.Status != calls.StatusOffered {
		stopCallOfferTimer(state.CallID)
	}
	if d == nil || d.ipcSrv == nil {
		return
	}
	push(d.ipcSrv, ipc.FrameCallStateChanged, "", ipc.CallStateChangedPayload{
		Call: callStateToEntry(state),
	})
	if state.IsTerminal() {
		writeCallSummaryBreadcrumb(d, state)
	}
}

func writeCallSummaryBreadcrumb(d *daemon, state calls.State) {
	outcome := deriveCallOutcome(state)
	var duration int64
	if outcome == events.CallOutcomeCompleted && state.AcceptedAt > 0 && state.EndedAt > 0 {
		duration = state.EndedAt - state.AcceptedAt
		if duration < 0 {
			duration = 0
		}
	}
	body, err := json.Marshal(events.CallSummaryBody{
		CallID:          state.CallID,
		Direction:       string(state.Direction),
		Outcome:         outcome,
		DurationSeconds: duration,
		Modalities:      state.Modalities,
		FailReason:      state.FailReason,
	})
	if err != nil {
		slog.Warn("marshal call_summary body failed",
			slog.String("call_id", state.CallID),
			slog.Any("err", err),
		)
		return
	}
	displayTs := state.EndedAt
	if displayTs == 0 {
		displayTs = time.Now().Unix()
	}
	dir := events.DirOut
	var senderPeerID string
	if state.Direction == calls.DirIn {
		dir = events.DirIn
		senderPeerID = state.PeerID
	}

	var msgID string
	if dir == events.DirOut {
		id, idErr := msg.NewID()
		if idErr != nil {

			slog.Warn("call_summary msg id could not be made; the call summary stays on this device",
				slog.String("call_id", state.CallID),
				slog.Any("err", idErr),
			)
		}
		msgID = id
	}
	ev, err := d.events.AppendLocal(events.LocalParams{
		ChatID:        state.ChatID,
		Kind:          events.KindCallSummary,
		Direction:     dir,
		DisplayTs:     displayTs,
		MsgID:         msgID,
		SenderPeerID:  senderPeerID,
		ExpireSeconds: chatRetentionTTL(d, state.ChatID),
		Body:          body,
	})
	if err != nil {
		slog.Warn("persist call_summary breadcrumb failed",
			slog.String("call_id", state.CallID),
			slog.Any("err", err),
		)
		return
	}

	bumpChatActivity(context.Background(), d, state.ChatID, displayTs)
	if dir == events.DirOut && ev.MsgID != "" {
		pushCallSummary(d, ev)
	}
}

func deriveCallOutcome(state calls.State) events.CallOutcome {

	if state.Direction == calls.DirIn && state.Status == calls.StatusEnded && state.FailReason == msg.CallEndAnsweredElsewhere {
		return events.CallOutcomeAnsweredElsewhere
	}
	switch state.Status {
	case calls.StatusRejected:
		return events.CallOutcomeRejected
	case calls.StatusFailed:
		return events.CallOutcomeFailed
	case calls.StatusEnded:
		if state.AcceptedAt > 0 {
			if state.FailReason == calls.CallEndReasonAutoHangup {
				return events.CallOutcomeDisrupted
			}
			return events.CallOutcomeCompleted
		}
		if state.Direction == calls.DirIn {
			return events.CallOutcomeMissed
		}
		return events.CallOutcomeCancelled
	default:
		return events.CallOutcomeFailed
	}
}
