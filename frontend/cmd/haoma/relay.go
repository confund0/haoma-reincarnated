package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.mau.fi/libsignal/protocol"
	"go.mau.fi/libsignal/signalerror"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/calls"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/msg"
	"haoma-frontend/internal/pair"
)

func relayBackend(ctx context.Context, d *daemon) error {
	doneEvents := make(chan struct{})
	doneStatus := make(chan struct{})
	donePair := make(chan struct{})
	doneRotation := make(chan struct{})

	go func() {
		defer close(doneEvents)
		eventsLoop(ctx, d)
	}()
	go func() {
		defer close(doneStatus)
		backendStatusLoop(ctx, d)
	}()
	go func() {
		defer close(donePair)
		dhtReturnLoop(ctx, d)
	}()
	go func() {
		defer close(doneRotation)
		if d.rotation != nil {
			d.rotation.Run(ctx)
		}
	}()

	<-doneEvents
	<-doneStatus
	<-donePair
	<-doneRotation
	return nil
}

func pushTimelineEvents(ctx context.Context, bus *events.Bus, ipcSrv *ipc.Server) {
	ch, cancel := bus.Subscribe(64)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			raw, err := json.Marshal(ev)
			if err != nil {
				slog.Warn("timeline push marshal failed", slog.Any("err", err))
				continue
			}
			slog.Debug("timeline event pushed",
				slog.Uint64("recv_seq", ev.RecvSeq),
				slog.String("chat_id", string(ev.ChatID)),
				slog.String("direction", string(ev.Direction)),
				slog.String("kind", string(ev.Kind)),
			)
			push(ipcSrv, ipc.FrameTimelineEvent, "", ipc.TimelineEventPayload{Event: raw})
		}
	}
}

func pushTimelineDeletions(ctx context.Context, bus *events.Bus, ipcSrv *ipc.Server) {
	ch, cancel := bus.SubscribeDeletions(64)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case d, ok := <-ch:
			if !ok {
				return
			}
			slog.Debug("timeline deletion pushed",
				slog.String("chat_id", string(d.ChatID)),
				slog.Uint64("recv_seq", d.RecvSeq),
			)
			push(ipcSrv, ipc.FrameTimelineEventDeleted, "", ipc.TimelineEventDeletedPayload{
				ChatID:  string(d.ChatID),
				RecvSeq: d.RecvSeq,
			})
		}
	}
}

func retentionSweeper(ctx context.Context, d *daemon) {
	if d.events == nil {
		return
	}
	t := time.NewTicker(retentionSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := d.events.SweepExpired(time.Now().Unix())
			if err != nil {
				slog.Warn("retention sweep failed", slog.Any("err", err))
				continue
			}
			if n > 0 {
				slog.Debug("retention sweep removed rows", slog.Int("count", n))
			}
		}
	}
}

const retentionSweepInterval = 60 * time.Second

func eventsLoop(ctx context.Context, d *daemon) {
	attempt := 0
	for {
		if ctx.Err() != nil {
			return
		}
		slog.Debug("events stream connecting", slog.Int("attempt", attempt+1))

		onReady := func() {
			if d.backendReachable.CompareAndSwap(false, true) {
				attempt = 0
				slog.Debug("backend reachable (events stream up)")
				pushBackendStatus(ctx, d)
			}

			sweepInbox(ctx, d)

			d.fileRetrySweepOnce.Do(func() {
				go func() {
					n, err := d.backendClient.RetryFailedFiles(ctx)
					if err != nil {
						slog.Warn("file retry sweep on startup failed", slog.Any("err", err))
						return
					}
					slog.Info("file retry sweep on startup", slog.Int("enqueued", n))
				}()
			})
		}
		err := d.backendClient.Events(ctx, backendapi.EventsOpts{
			OnReady:             onReady,
			OnFileFetchState:    fileFetchStateHandler(ctx, d),
			OnFileFetchProgress: fileFetchProgressHandler(d),
			OnEvent: func(n backendapi.Notification) {
				push(d.ipcSrv, ipc.FrameStatusEvent, "", notificationToStatusEvent(n))
			},
			OnPresence: func(o backendapi.PresenceObservation) {
				if d.presenceCache == nil || o.PeerID == "" {
					return
				}

				slog.Debug("peer.presence-changed via SSE",
					slog.String("peer_id", o.PeerID),
					slog.String("source", o.Source),
				)
				d.presenceCache.ObserveTechnical(o.PeerID)
			},
			OnLastSeen: func(o backendapi.LastSeenObservation) {
				if o.PeerID == "" {
					return
				}

				slog.Debug("peer.last-seen-changed via SSE",
					slog.String("peer_id", o.PeerID),
					slog.Int64("last_active_at", o.LastActiveAt),
					slog.Int64("last_passive_at", o.LastPassiveAt),
				)
				push(d.ipcSrv, ipc.FramePeerLastSeenChanged, "", ipc.PeerLastSeenChangedPayload{
					PeerID:        o.PeerID,
					LastActiveAt:  o.LastActiveAt,
					LastPassiveAt: o.LastPassiveAt,
				})
			},
			OnDelivery: func(ds backendapi.DeliveryStatus) {
				slog.Debug("delivery.state-changed via SSE",
					slog.String("envelope_id", ds.EnvelopeID),
					slog.String("state", ds.State),
				)

				if d.events != nil {
					if _, err := d.events.UpdateDeliveryState(ds.EnvelopeID, ds.State); err != nil {
						if errors.Is(err, events.ErrEventNotFound) {
							slog.Debug("delivery_status: no event row to update (pre-index envelope)",
								slog.String("envelope_id", ds.EnvelopeID),
							)
						} else {
							slog.Warn("persist delivery state failed",
								slog.String("envelope_id", ds.EnvelopeID),
								slog.String("state", ds.State),
								slog.Any("err", err),
							)
						}
					}
				}
				push(d.ipcSrv, ipc.FrameDeliveryStatus, "", ipc.DeliveryStatusPayload{
					EnvelopeID: ds.EnvelopeID,
					State:      ds.State,
					At:         ds.At,
					Attempts:   ds.Attempts,
					LastError:  ds.LastError,
				})
			},
			OnInbox: func(entry backendapi.InboxEntry) {
				slog.Debug("inbox.received via SSE",
					slog.String("envelope_id", entry.Envelope.ID),
					slog.String("peer_id", entry.PeerID),
				)
				processInboxEntry(ctx, d, entry)
				if err := d.backendClient.DeleteInboxEntry(ctx, entry.Envelope.ID); err != nil {
					slog.Warn("inbox ack failed",
						slog.String("envelope_id", entry.Envelope.ID),
						slog.Any("err", err),
					)
				}
			},
			OnPairOnionProbe: func(p backendapi.PairOnionProbe) {
				slog.Debug("pair.onion-probe via SSE",
					slog.String("handle_id", p.HandleID),
					slog.Int("attempt", p.Attempt),
					slog.Bool("ready", p.Ready),
				)
				push(d.ipcSrv, ipc.FramePairOnionProbe, "", ipc.PairOnionProbePush{
					HandleID: p.HandleID,
					Attempt:  p.Attempt,
					Ready:    p.Ready,
					Error:    p.Error,
					At:       p.At,
				})
			},
			OnPeerSelfReach: func(ev backendapi.PeerSelfReachEvent) {
				slog.Debug("peer.self-reach-changed via SSE",
					slog.String("peer_id", ev.PeerID),
					slog.Bool("ok", ev.Ok),
				)
				push(d.ipcSrv, ipc.FramePeerSelfReachChanged, "", ipc.PeerSelfReachPayload{
					PeerID: ev.PeerID,
					Onion:  ev.Onion,
					Ok:     ev.Ok,
					At:     ev.At,
				})
			},
			OnExternalReach: func(ev backendapi.ExternalReachEvent) {
				slog.Debug("health.external-reach-changed via SSE",
					slog.Bool("ok", ev.Ok),
					slog.String("last_target", ev.LastTargetName),
				)
				push(d.ipcSrv, ipc.FrameExternalReachChanged, "", ipc.ExternalReachPayload{
					Ok:         ev.Ok,
					LastTarget: ev.LastTargetName,
					At:         ev.At,
				})
			},
		})

		if d.backendReachable.CompareAndSwap(true, false) {
			slog.Debug("backend unreachable (events stream closed)")
			pushBackendStatus(ctx, d)
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			slog.Warn("backend events stream failed; will reconnect",
				slog.Any("err", err),
				slog.Int("attempt", attempt+1),
			)
		} else {
			slog.Debug("events stream closed cleanly")
		}

		attempt++
		select {
		case <-ctx.Done():
			return
		case <-time.After(backendapi.DefaultReconnectBackoff(attempt)):
		}
	}
}

func backendStatusLoop(ctx context.Context, d *daemon) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()

	pushBackendStatus(ctx, d)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pushBackendStatus(ctx, d)
		}
	}
}

func pushBackendStatus(ctx context.Context, d *daemon) {
	reachable := d.backendReachable.Load()
	payload := ipc.BackendStatusPayload{BackendReachable: reachable}
	if reachable && d.backendClient != nil {

		tctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		info, err := d.backendClient.TorInfo(tctx)
		if err != nil {

			payload.Tor = ipc.TorHealth{Unreachable: true}
		} else {
			payload.Tor = ipc.TorHealth{
				Bootstrap:   info.Health.Bootstrap,
				Ready:       info.Health.Ready,
				Unreachable: info.Health.Unreachable,
			}
			payload.OnionCount = len(info.Slots)
		}
	} else {
		payload.Tor = ipc.TorHealth{Unreachable: true}
	}
	d.latestStatus.Store(&payload)
	push(d.ipcSrv, ipc.FrameBackendStatus, "", payload)
}

func sweepInbox(ctx context.Context, d *daemon) {
	if d.backendClient == nil {
		return
	}
	resp, err := d.backendClient.Inbox(ctx, 0, 0)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("inbox sweep failed", slog.Any("err", err))
		}
		return
	}
	if len(resp.Entries) > 0 {
		slog.Debug("inbox sweep", slog.Int("entries", len(resp.Entries)))
	}
	for _, entry := range resp.Entries {
		processInboxEntry(ctx, d, entry)
		if err := d.backendClient.DeleteInboxEntry(ctx, entry.Envelope.ID); err != nil {
			slog.Warn("inbox ack failed",
				slog.String("envelope_id", entry.Envelope.ID),
				slog.Any("err", err),
			)
		}
	}
}

func processInboxEntry(ctx context.Context, d *daemon, entry backendapi.InboxEntry) {

	envBytes, err := json.Marshal(entry.Envelope)
	if err == nil {
		push(d.ipcSrv, ipc.FrameInboxEntry, "", ipc.InboxEntryPayload{
			ArrivalAt: entry.ArrivalAt,
			PeerID:    entry.PeerID,
			Envelope:  envBytes,
		})
	}

	if entry.PeerID == "" {
		slog.Warn("inbox entry has empty peer_id; skipping timeline persist",
			slog.String("envelope_id", entry.Envelope.ID),
		)
		return
	}

	chatID, chatErr := resolveChatForPeer(ctx, d, entry.PeerID)
	if chatErr != nil {
		slog.Error("resolve chat for inbound failed; dropping timeline persist",
			slog.String("peer_id", entry.PeerID),
			slog.Any("err", chatErr),
		)
		return
	}

	plain, decErr := d.cipher.Decrypt(ctx, entry.PeerID, entry.Envelope.Payload)
	if decErr != nil {

		if errors.Is(decErr, signalerror.ErrOldCounter) {
			slog.Debug("inbox decrypt skipped: benign old-counter re-delivery",
				slog.String("envelope_id", entry.Envelope.ID),
				slog.String("peer_id", entry.PeerID),
			)
			return
		}
		_, persistErr := d.events.AppendInbound(events.InboundParams{
			ChatID:     chatID,
			Kind:       events.KindText,
			SenderTs:   entry.Envelope.Timestamp,
			EnvelopeID: entry.Envelope.ID,
			Status:     events.DecryptFailed,
			RawBlob:    entry.Envelope.Payload,
		})
		if persistErr != nil {
			slog.Error("persist decrypt-failed event failed",
				slog.String("envelope_id", entry.Envelope.ID),
				slog.Any("err", persistErr),
			)
		} else {

			bumpChatActivity(ctx, d, chatID, time.Now().Unix())
		}
		slog.Warn("inbox decrypt failed",
			slog.String("envelope_id", entry.Envelope.ID),
			slog.String("peer_id", entry.PeerID),
			slog.Any("err", decErr),
		)
		return
	}

	wrapper, parseErr := msg.Unmarshal(plain)
	if parseErr != nil {
		_, persistErr := d.events.AppendInbound(events.InboundParams{
			ChatID:     chatID,
			Kind:       events.KindText,
			SenderTs:   entry.Envelope.Timestamp,
			EnvelopeID: entry.Envelope.ID,
			Status:     events.DecryptFailed,
			RawBlob:    plain,
		})
		if persistErr != nil {
			slog.Error("persist unparseable-wrapper event failed",
				slog.String("envelope_id", entry.Envelope.ID),
				slog.Any("err", persistErr),
			)
		} else {
			bumpChatActivity(ctx, d, chatID, time.Now().Unix())
		}
		slog.Warn("inbox wrapper parse failed (decrypted but unintelligible)",
			slog.String("envelope_id", entry.Envelope.ID),
			slog.String("peer_id", entry.PeerID),
			slog.Any("err", parseErr),
		)
		return
	}

	slog.Debug("inbox decrypted",
		slog.String("envelope_id", entry.Envelope.ID),
		slog.String("peer_id", entry.PeerID),
		slog.String("chat_id", string(chatID)),
		slog.String("kind", string(wrapper.Kind)),
		slog.String("msg_id", wrapper.MsgID),
		slog.Uint64("sender_seq", wrapper.Seq),
	)

	switch wrapper.Kind {
	case msg.KindText:

		converged, oldTTL := maybeConvergeRetention(d, chatID, entry.PeerID, wrapper.ExpireSeconds, wrapper.Ts)
		if converged {
			writeTimerChangeBreadcrumb(d, chatID, entry.PeerID, oldTTL, wrapper.ExpireSeconds, wrapper.Ts)
		}

		if textBody, perr := wrapper.Text(); perr == nil {
			if textBody.PresenceState != "" {
				d.presenceCache.ObserveHuman(entry.PeerID, textBody.PresenceState)
			}
			if textBody.SenderNick != "" && d.peerMeta != nil {
				clampedTs := events.ClampSenderTs(wrapper.Ts, time.Now().Unix())
				changed, err := d.peerMeta.SetNick(entry.PeerID, textBody.SenderNick, clampedTs)
				if err != nil {
					slog.Warn("peerMeta.SetNick (text piggyback) failed",
						slog.String("peer_id", entry.PeerID),
						slog.Any("err", err),
					)
				} else {
					slog.Debug("peer-meta nick updated (text piggyback)",
						slog.String("peer_id", entry.PeerID),
						slog.String("nick", textBody.SenderNick),
						slog.Int64("clamped_ts", clampedTs),
						slog.Bool("changed", changed),
					)
					if changed {
						pushPeerMetaUpdated(ctx, d, entry.PeerID)
					}
				}
			}
		}
		if _, persistErr := d.events.AppendInbound(events.InboundParams{
			ChatID:        chatID,
			Kind:          events.KindText,
			SenderTs:      wrapper.Ts,
			SenderSeq:     wrapper.Seq,
			EnvelopeID:    entry.Envelope.ID,
			MsgID:         wrapper.MsgID,
			ExpireSeconds: wrapper.ExpireSeconds,
			Status:        events.DecryptOK,
			Body:          wrapper.Body,
		}); persistErr != nil {
			slog.Error("persist decrypted event failed",
				slog.String("envelope_id", entry.Envelope.ID),
				slog.Any("err", persistErr),
			)
		} else {

			maybeSuppressInboundReceipt(d, chatID, wrapper.MsgID)

			bumpChatActivity(context.Background(), d, chatID, time.Now().Unix())
			if !chatIsFocused(d, chatID) {
				incrementChatUnread(context.Background(), d, chatID)
			}

			d.autoMarkOnArrival(context.Background(), chatID)

			notifyBody := ""
			if textBody, perr := wrapper.Text(); perr == nil {
				notifyBody = textBody.Text
			}
			emitInboundNotification(context.Background(), d, chatID, entry.PeerID, notifyBody)
		}

	case msg.KindEdit:
		body, err := wrapper.Edit()
		if err != nil {
			slog.Warn("inbound edit: body parse failed",
				slog.String("envelope_id", entry.Envelope.ID),
				slog.String("peer_id", entry.PeerID),
				slog.Any("err", err),
			)
			return
		}

		if _, err := d.events.ApplyEdit(body.Target, body.Text, wrapper.Ts, entry.PeerID); err != nil {
			switch {
			case errors.Is(err, events.ErrEditorNotAuthor):
				slog.Warn("inbound edit: editor is not the author; rejecting",
					slog.String("target_msg_id", body.Target),
					slog.String("peer_id", entry.PeerID),
				)
			case errors.Is(err, events.ErrEventNotFound):
				slog.Info("inbound edit: target missing (swept or out-of-order)",
					slog.String("target_msg_id", body.Target),
					slog.String("peer_id", entry.PeerID),
				)
			case errors.Is(err, events.ErrEditUnsupportedKind):
				slog.Warn("inbound edit: target is not a text row; rejecting",
					slog.String("target_msg_id", body.Target),
					slog.String("peer_id", entry.PeerID),
				)
			default:
				slog.Error("inbound edit: apply failed",
					slog.String("target_msg_id", body.Target),
					slog.Any("err", err),
				)
			}
		}

	case msg.KindDelete:
		body, err := wrapper.Delete()
		if err != nil {
			slog.Warn("inbound delete: body parse failed",
				slog.String("envelope_id", entry.Envelope.ID),
				slog.String("peer_id", entry.PeerID),
				slog.Any("err", err),
			)
			return
		}

		if _, err := d.events.ApplyDelete(body.Target, wrapper.Ts, entry.PeerID); err != nil {
			switch {
			case errors.Is(err, events.ErrDeleterNotAuthor):
				slog.Warn("inbound delete: deleter is not the author; rejecting",
					slog.String("target_msg_id", body.Target),
					slog.String("peer_id", entry.PeerID),
				)
			case errors.Is(err, events.ErrEventNotFound):
				slog.Info("inbound delete: target missing (swept or out-of-order)",
					slog.String("target_msg_id", body.Target),
					slog.String("peer_id", entry.PeerID),
				)
			case errors.Is(err, events.ErrDeleteUnsupportedKind):
				slog.Warn("inbound delete: target is not a text row; rejecting",
					slog.String("target_msg_id", body.Target),
					slog.String("peer_id", entry.PeerID),
				)
			default:
				slog.Error("inbound delete: apply failed",
					slog.String("target_msg_id", body.Target),
					slog.Any("err", err),
				)
			}
		}

	case msg.KindReaction:
		body, err := wrapper.Reaction()
		if err != nil {
			slog.Warn("inbound reaction: body parse failed",
				slog.String("envelope_id", entry.Envelope.ID),
				slog.String("peer_id", entry.PeerID),
				slog.Any("err", err),
			)
			return
		}

		clampedTs := events.ClampSenderTs(wrapper.Ts, time.Now().Unix())
		if _, err := d.events.AppendReactionBreadcrumb(body.Target, body.Emoji, entry.PeerID, clampedTs); err != nil {
			switch {
			case errors.Is(err, events.ErrEventNotFound):
				slog.Info("inbound reaction: target missing (swept or out-of-order)",
					slog.String("target_msg_id", body.Target),
					slog.String("peer_id", entry.PeerID),
				)
			case errors.Is(err, events.ErrReactionUnsupportedKind):
				slog.Warn("inbound reaction: target is not reactable; dropping",
					slog.String("target_msg_id", body.Target),
					slog.String("peer_id", entry.PeerID),
				)
			default:
				slog.Error("inbound reaction: append failed",
					slog.String("target_msg_id", body.Target),
					slog.Any("err", err),
				)
			}
		}

	case msg.KindPresence:
		body, err := wrapper.Presence()
		if err != nil {
			slog.Warn("inbound presence: body parse failed",
				slog.String("envelope_id", entry.Envelope.ID),
				slog.String("peer_id", entry.PeerID),
				slog.Any("err", err),
			)
			return
		}

		d.presenceCache.ObserveHuman(entry.PeerID, body.State)
		slog.Debug("inbound presence observed",
			slog.String("peer_id", entry.PeerID),
			slog.String("state", body.State),
		)

	case msg.KindFileOffer:
		body, err := wrapper.FileOffer()
		if err != nil {
			slog.Warn("inbound file_offer: body parse failed",
				slog.String("envelope_id", entry.Envelope.ID),
				slog.String("peer_id", entry.PeerID),
				slog.Any("err", err),
			)
			return
		}
		ingestFileOffer(ctx, d, chatID, entry, wrapper, body)

	case msg.KindFileReceipt:
		body, err := wrapper.FileReceipt()
		if err != nil {
			slog.Warn("inbound file_receipt: body parse failed",
				slog.String("envelope_id", entry.Envelope.ID),
				slog.String("peer_id", entry.PeerID),
				slog.Any("err", err),
			)
			return
		}
		ingestFileReceipt(ctx, d, entry.PeerID, body)

	case msg.KindFileKey:
		body, key, nonce, err := wrapper.FileKey()
		if err != nil {
			slog.Warn("inbound file_key: body parse failed",
				slog.String("envelope_id", entry.Envelope.ID),
				slog.String("peer_id", entry.PeerID),
				slog.Any("err", err),
			)
			return
		}
		ingestFileKey(ctx, d, entry.PeerID, body.Token, key, nonce)

	case msg.KindCallOffer:
		body, err := wrapper.CallOffer()
		if err != nil {
			slog.Warn("inbound call_offer: body parse failed",
				slog.String("envelope_id", entry.Envelope.ID),
				slog.String("peer_id", entry.PeerID),
				slog.Any("err", err),
			)
			return
		}
		ingestCallOffer(ctx, d, chatID, entry.PeerID, body.CallID, body.Modalities, body.OutboundKey, body.Tokens)

	case msg.KindCallAccept:
		body, err := wrapper.CallAccept()
		if err != nil {
			slog.Warn("inbound call_accept: body parse failed",
				slog.String("envelope_id", entry.Envelope.ID),
				slog.String("peer_id", entry.PeerID),
				slog.Any("err", err),
			)
			return
		}
		applyCallAccept(ctx, d, body.CallID, body.OutboundKey, body.Tokens)

	case msg.KindCallReject:
		body, err := wrapper.CallReject()
		if err != nil {
			slog.Warn("inbound call_reject: body parse failed",
				slog.String("envelope_id", entry.Envelope.ID),
				slog.String("peer_id", entry.PeerID),
				slog.Any("err", err),
			)
			return
		}
		applyCallTransition(d, body.CallID, calls.StatusRejected, body.Reason, "call_reject")

	case msg.KindCallEnd:
		body, err := wrapper.CallEnd()
		if err != nil {
			slog.Warn("inbound call_end: body parse failed",
				slog.String("envelope_id", entry.Envelope.ID),
				slog.String("peer_id", entry.PeerID),
				slog.Any("err", err),
			)
			return
		}
		applyCallTransition(d, body.CallID, calls.StatusEnded, "", "call_end")

	case msg.KindRotateRequest:
		dispatchRotateRequest(ctx, d, entry.PeerID, wrapper)

	case msg.KindRotateAccept:
		dispatchRotateAccept(ctx, d, entry.PeerID, wrapper)

	case msg.KindRotateAddress:
		dispatchRotateAddress(ctx, d, entry.PeerID, wrapper)

	case msg.KindRotateConfirm:
		dispatchRotateConfirm(ctx, d, entry.PeerID, wrapper)

	case msg.KindRotateCancel:
		dispatchRotateCancel(ctx, d, entry.PeerID, wrapper)

	case msg.KindRead:
		body, err := wrapper.Read()
		if err != nil {
			slog.Warn("inbound read: body parse failed",
				slog.String("envelope_id", entry.Envelope.ID),
				slog.String("peer_id", entry.PeerID),
				slog.Any("err", err),
			)
			return
		}

		if body.PresenceState != "" {
			d.presenceCache.ObserveHuman(entry.PeerID, body.PresenceState)
		}

		clampedTs := events.ClampSenderTs(wrapper.Ts, time.Now().Unix())
		for _, target := range body.Targets {
			if _, err := d.events.ApplyReadReceipt(target, clampedTs, chatID); err != nil {
				switch {
				case errors.Is(err, events.ErrEventNotFound):
					slog.Info("inbound read: target missing (swept or out-of-order)",
						slog.String("target_msg_id", target),
						slog.String("peer_id", entry.PeerID),
					)
				case errors.Is(err, events.ErrReaderNotPeer):
					slog.Warn("inbound read: reader/chat mismatch; rejecting",
						slog.String("target_msg_id", target),
						slog.String("peer_id", entry.PeerID),
					)
				case errors.Is(err, events.ErrReadUnsupportedKind):
					slog.Warn("inbound read: target is not a text row; rejecting",
						slog.String("target_msg_id", target),
						slog.String("peer_id", entry.PeerID),
					)
				default:
					slog.Error("inbound read: apply failed",
						slog.String("target_msg_id", target),
						slog.Any("err", err),
					)
				}
			}
		}

	default:
		if _, persistErr := d.events.AppendInbound(events.InboundParams{
			ChatID:        chatID,
			Kind:          events.KindText,
			SenderTs:      wrapper.Ts,
			SenderSeq:     wrapper.Seq,
			EnvelopeID:    entry.Envelope.ID,
			MsgID:         wrapper.MsgID,
			ExpireSeconds: wrapper.ExpireSeconds,
			Status:        events.DecryptFailed,
			RawBlob:       plain,
		}); persistErr != nil {
			slog.Error("persist unknown-kind event failed",
				slog.String("envelope_id", entry.Envelope.ID),
				slog.Any("err", persistErr),
			)
		} else {
			bumpChatActivity(ctx, d, chatID, time.Now().Unix())
		}
		slog.Warn("inbox: unknown wrapper kind",
			slog.String("kind", string(wrapper.Kind)),
			slog.String("peer_id", entry.PeerID),
		)
	}
}

func maybeConvergeRetention(d *daemon, chatID chat.ChatID, senderPeerID string, incomingTTL uint32, senderTs int64) (bool, uint32) {
	if d.chats == nil {
		return false, 0
	}
	c, err := d.chats.Get(chatID)
	if err != nil {
		slog.Debug("retention convergence: chat lookup failed",
			slog.String("chat_id", string(chatID)),
			slog.Any("err", err),
		)
		return false, 0
	}
	oldTTL := c.Retention()
	if incomingTTL == oldTTL {
		return false, oldTTL
	}
	clampedTs := events.ClampSenderTs(senderTs, time.Now().Unix())
	if clampedTs <= c.TimerChangeTs() {
		slog.Debug("retention convergence: stale ts ignored",
			slog.String("chat_id", string(chatID)),
			slog.Int64("sender_ts", senderTs),
			slog.Int64("clamped_ts", clampedTs),
			slog.Int64("last_timer_change_ts", c.TimerChangeTs()),
		)
		return false, oldTTL
	}
	if err := d.chats.SetRetentionAndTimerTs(chatID, incomingTTL, clampedTs); err != nil {
		slog.Warn("retention convergence: persist failed",
			slog.String("chat_id", string(chatID)),
			slog.Any("err", err),
		)
		return false, oldTTL
	}
	slog.Info("retention converged",
		slog.String("chat_id", string(chatID)),
		slog.String("from_peer", senderPeerID),
		slog.Int("old_ttl", int(oldTTL)),
		slog.Int("new_ttl", int(incomingTTL)),
	)
	push(d.ipcSrv, ipc.FrameChatSettings, "", ipc.ChatSettingsPayload{
		ChatID:       string(chatID),
		RetentionTTL: incomingTTL,
	})
	return true, oldTTL
}

func writeTimerChangeBreadcrumb(d *daemon, chatID chat.ChatID, senderPeerID string, oldTTL, newTTL uint32, senderTs int64) {
	body, err := json.Marshal(events.TimerChangeBody{
		From:      oldTTL,
		To:        newTTL,
		ChangedBy: senderPeerID,
	})
	if err != nil {
		slog.Warn("marshal timer_change body failed", slog.Any("err", err))
		return
	}
	clampedTs := events.ClampSenderTs(senderTs, time.Now().Unix())
	if _, err := d.events.AppendLocal(events.LocalParams{
		ChatID:       chatID,
		Kind:         events.KindTimerChange,
		Direction:    events.DirIn,
		DisplayTs:    clampedTs,
		SenderPeerID: senderPeerID,
		Body:         body,
	}); err != nil {
		slog.Warn("persist timer_change breadcrumb failed", slog.Any("err", err))
	} else {
		bumpChatActivity(context.Background(), d, chatID, clampedTs)
	}
}

func resolveChatForPeer(ctx context.Context, d *daemon, peerID string) (chat.ChatID, error) {
	dc, fresh, err := d.createDirectWithDefaults(peerID)
	if err != nil {
		return "", fmt.Errorf("resolve+create chat: %w", err)
	}
	if fresh {
		pushPeerMetaUpdated(ctx, d, peerID)
		slog.Debug("auto-materialised DirectChat on inbound (post-delete recovery)",
			slog.String("peer_id", peerID),
			slog.String("chat_id", string(dc.ID)),
		)
	}
	return dc.ID, nil
}

func notificationToStatusEvent(n backendapi.Notification) ipc.StatusEventPayload {
	evBytes, err := json.Marshal(n.Event)
	if err != nil {
		evBytes = json.RawMessage(`null`)
	}
	actionsBytes, err := json.Marshal(n.Actions)
	if err != nil {
		actionsBytes = json.RawMessage(`null`)
	}
	return ipc.StatusEventPayload{Event: evBytes, Actions: actionsBytes}
}

func dhtReturnLoop(ctx context.Context, d *daemon) {
	if d.backendClient == nil {
		return
	}
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			processDHTReturns(ctx, d)
		}
	}
}

func processDHTReturns(ctx context.Context, d *daemon) {
	pending, err := d.backendClient.DHTPending(ctx)
	if err != nil {

		return
	}
	if len(pending) > 0 {
		slog.Debug("pair: consumer found pending returns", slog.Int("count", len(pending)))
	}
	for _, p := range pending {
		inv, err := pair.Parse(p.ReturnInvite)
		if err != nil {
			slog.Warn("pair: return invite parse failed; revoking anyway",
				slog.String("guid", p.GUID),
				slog.Any("err", err),
			)
			_ = d.backendClient.DHTCancel(ctx, p.GUID)
			continue
		}
		mine, minted, err := pair.LoadMyKeys(d.store, p.GUID)
		if err != nil {
			slog.Error("pair: return invite mykeys load failed",
				slog.String("guid", p.GUID),
				slog.Any("err", err),
			)
			continue
		}
		if err := pair.Import(ctx, d.stores, d.backendClient, inv, mine, minted); err != nil {
			slog.Error("pair: return invite import failed",
				slog.String("guid", p.GUID),
				slog.String("peer_id", inv.PeerID),
				slog.Any("err", err),
			)

			continue
		}
		seedPairNick(d, inv.PeerID, inv.Frontend.Nick)
		if err := pair.DeleteMyKeys(d.store, p.GUID); err != nil {
			slog.Warn("pair: delete return-invite mykeys failed",
				slog.String("guid", p.GUID),
				slog.Any("err", err),
			)
		}
		if d.chats != nil {
			if _, _, err := d.createDirectWithDefaults(inv.PeerID); err != nil {
				slog.Warn("create direct chat after return-invite import failed",
					slog.String("peer_id", inv.PeerID),
					slog.Any("err", err),
				)
			}
		}

		addr := protocol.NewSignalAddress(inv.PeerID, pair.DeviceID)
		fingerprint := ""
		if remoteKey, err := d.stores.GetRemoteIdentity(addr); err == nil {
			fingerprint = remoteKey.Fingerprint()
		}
		slog.Info("pair: return invite imported",
			slog.String("guid", p.GUID),
			slog.String("peer_id", inv.PeerID),
			slog.String("nick", inv.Frontend.Nick),
		)

		push(d.ipcSrv, ipc.FrameAcceptedDHT, "", ipc.AcceptedDHTResponse{
			PeerID:              inv.PeerID,
			Nick:                inv.Frontend.Nick,
			IdentityFingerprint: fingerprint,
		})
		push(d.ipcSrv, ipc.FramePeerPaired, "", ipc.PeerPairedPush{
			PeerID: inv.PeerID,
			Source: "dht-return",
		})
		pushPeerMetaUpdated(ctx, d, inv.PeerID)

		if err := d.backendClient.DHTCancel(ctx, p.GUID); err != nil {
			slog.Warn("pair: revoke failed after successful return-import",
				slog.String("guid", p.GUID),
				slog.Any("err", err),
			)
		}
	}
}

func maybeSuppressInboundReceipt(d *daemon, chatID chat.ChatID, msgID string) {
	if d.chats == nil || d.events == nil || msgID == "" {
		return
	}
	c, err := d.chats.Get(chatID)
	if err != nil {
		slog.Debug("inbound receipt suppression: chat lookup failed",
			slog.String("chat_id", string(chatID)),
			slog.Any("err", err),
		)
		return
	}
	if !c.ReadReceiptsDisabled() {
		return
	}
	if err := d.events.MarkReadReceiptSent([]string{msgID}, events.ReadReceiptSuppressedSentinel); err != nil {
		slog.Warn("inbound receipt suppression: stamp sentinel failed",
			slog.String("chat_id", string(chatID)),
			slog.String("msg_id", msgID),
			slog.Any("err", err),
		)
		return
	}
	slog.Debug("inbound receipt suppressed (chat toggle off)",
		slog.String("chat_id", string(chatID)),
		slog.String("msg_id", msgID),
	)
}

func push(ipcSrv *ipc.Server, t ipc.FrameType, id string, payload any) {
	f, err := ipc.NewFrame(t, id, payload)
	if err != nil {
		slog.Warn("build push frame failed",
			slog.String("type", string(t)),
			slog.Any("err", err),
		)
		return
	}
	ipcSrv.Broadcast(f)
}
