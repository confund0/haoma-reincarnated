package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/dgraph-io/badger/v4"
	"go.mau.fi/libsignal/protocol"
	"go.mau.fi/libsignal/signalerror"

	"haoma-frontend/internal/backendapi"
	"haoma-frontend/internal/calls"
	"haoma-frontend/internal/chat"
	"haoma-frontend/internal/events"
	"haoma-frontend/internal/invitation"
	"haoma-frontend/internal/ipc"
	"haoma-frontend/internal/logid"
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
			n, cascade, err := d.events.SweepExpired(time.Now().Unix())
			if err != nil {
				slog.Warn("retention sweep failed", slog.Any("err", err))
				continue
			}
			if n > 0 || cascade > 0 {
				slog.Debug("retention sweep removed rows",
					slog.Int("count", n),
					slog.Int("cascade_keys", cascade),
				)
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

			go sweepStagedFetches(ctx, d)

			go sweepSiblingFetches(ctx, d)

			checkKeptGapOffers(d)

			go func() {
				sweepGapFill(ctx, d)
				pruneJournalForSiblings(d)
			}()

			go reportDestroyAttempts(ctx, d)

			go sweepDeliveryReconcile(ctx, d)

			go reattachInvitationWaiters(ctx, d)

			go sweepRetiredRoutes(ctx, d)

			go sweepDeadPeerRows(ctx, d)

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
			OnDestroyDevice: func(ev backendapi.DestroyDeviceInstruction) {
				stopForDestroy(d, ev.Reason, ev.EraseID)
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
			OnUnreachable: peerUnreachableHandler(ctx, d),
			OnDelivery: func(ds backendapi.DeliveryStatus) {
				slog.Debug("delivery.state-changed via SSE",
					slog.String("envelope_id", ds.EnvelopeID),
					slog.String("state", ds.State),
				)

				if handled, _ := applyReceiptOutcome(d, ds.EnvelopeID, ds.State); handled {
					push(d.ipcSrv, ipc.FrameDeliveryStatus, "", ipc.DeliveryStatusPayload{
						EnvelopeID: ds.EnvelopeID,
						State:      ds.State,
						At:         ds.At,
						Attempts:   ds.Attempts,
						LastError:  clientDetail("delivery_"+ds.State, ds.LastError),
					})
					return
				}
				if p, ok := deliveryPush(d, ds); ok {
					push(d.ipcSrv, ipc.FrameDeliveryStatus, "", p)
				}
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
					Error:    reachToken(p.Error),
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
			payload.ClockSkew = clockSkewFromTor(info.Health)
		}
	} else {
		payload.Tor = ipc.TorHealth{Unreachable: true}
	}

	logClockSkew(d, payload.ClockSkew)
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

	if d.isSibling(entry.PeerID) {
		processSiblingRailEnvelope(ctx, d, entry)
		return
	}

	if deviceLinkPending(d, entry.PeerID) {
		processDeviceLinkInbound(ctx, d, entry)
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

	bootAddr := protocol.NewSignalAddress(entry.PeerID, pair.DeviceID)
	hadSession, sessErr := d.stores.ContainsSession(ctx, bootAddr)
	if sessErr != nil {

		slog.Debug("A0: contains-session precheck failed; suppressing echo",
			slog.String("peer_id", entry.PeerID),
			slog.Any("err", sessErr),
		)
		hadSession = true
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
			ChatID:        chatID,
			Kind:          events.KindText,
			SenderTs:      entry.Envelope.Timestamp,
			EnvelopeID:    entry.Envelope.ID,
			ExpireSeconds: chatRetentionTTL(d, chatID),
			Status:        events.DecryptFailed,
			RawBlob:       entry.Envelope.Payload,
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

	noteSending(d, entry)

	if !hadSession {
		d.sessionsChanged.fire()

		noteContactChangedForPeer(d, entry.PeerID)
		pushInheritedContactsChanged(d, nil, "session_started")
	}

	markChatStarted(d, chatID)
	if !hadSession && d.peerSeq != nil && d.cipher != nil {
		state := d.effectivePresenceState()
		if err := d.shipPresenceToDevice(ctx, entry.PeerID, state); err != nil {
			slog.Warn("A0: responder presence echo failed",
				slog.String("peer_id", entry.PeerID),
				slog.Any("err", err),
			)
		} else {
			slog.Debug("A0: responder presence echo sent (pairing round-trip closed)",
				slog.String("peer_id", entry.PeerID),
			)
		}
	}

	wrapper, parseErr := msg.Unmarshal(plain)
	if parseErr != nil {
		var reason events.DecryptFailReason
		if errors.Is(parseErr, msg.ErrUnsupportedVersion) {
			reason = events.DecryptReasonVersionMismatch
		}
		_, persistErr := d.events.AppendInbound(events.InboundParams{
			ChatID:        chatID,
			Kind:          events.KindText,
			SenderTs:      entry.Envelope.Timestamp,
			EnvelopeID:    entry.Envelope.ID,
			ExpireSeconds: chatRetentionTTL(d, chatID),
			Status:        events.DecryptFailed,
			FailReason:    reason,
			RawBlob:       plain,
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

	if d.peerMeta != nil {
		if rec, err := d.peerMeta.Get(entry.PeerID); err == nil && rec.Fingerprint == "" {
			seedPeerFingerprint(d, entry.PeerID)
			notePairingCompleted(d, entry.PeerID, "joiner-first-decrypt")
		}
	}

	switch wrapper.Kind {
	case msg.KindText:

		converged, oldTTL := maybeConvergeRetention(d, chatID, entry.PeerID, wrapper.ExpireSeconds, wrapper.Ts)
		if converged {
			writeTimerChangeBreadcrumb(d, chatID, entry.PeerID, oldTTL, wrapper.ExpireSeconds, wrapper.Ts)
		}

		if textBody, perr := wrapper.Text(); perr == nil {
			noteHumanState(d, entry, textBody.PresenceState)

			applyUnlinkFields(ctx, d, entry.PeerID, "text", textBody.SendsUnlinkNotice, textBody.DeviceCount, 0, wrapper.Ts)
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
						slog.Int64("clamped_ts", clampedTs),
						slog.Bool("changed", changed),
					)
					if changed {
						personChangedLocally(ctx, d, entry.PeerID)
					}
				}
			}
		}

		_, persistErr := d.events.AppendInbound(events.InboundParams{
			ChatID:            chatID,
			Kind:              events.KindText,
			SenderTs:          wrapper.Ts,
			SenderSeq:         wrapper.Seq,
			ArrivedFromPeerID: entry.PeerID,
			EnvelopeID:        entry.Envelope.ID,
			MsgID:             wrapper.MsgID,
			ExpireSeconds:     wrapper.ExpireSeconds,
			Status:            events.DecryptOK,
			Body:              wrapper.Body,
		})
		switch {
		case persistErr == nil:
			noteInboundTextArrival(d, chatID, entry.PeerID, wrapper)

		case errors.Is(persistErr, events.ErrDuplicateMsgID):

			slog.Debug("inbox: message already held — the row stands, the arrival is counted here",
				slog.String("envelope_id", entry.Envelope.ID),
				slog.String("msg_id", wrapper.MsgID),
				slog.String("peer_id", entry.PeerID),
				slog.String("outcome", "duplicate_row_arrival_counted"),
			)

			if _, err := d.events.RecordArrivedFrom(wrapper.MsgID, entry.PeerID); err != nil {
				slog.Warn("inbox: recording the sending device on a held row failed (its read receipt will address the chat's stored device)",
					slog.String("msg_id", wrapper.MsgID),
					slog.String("peer_id", entry.PeerID),
					slog.Any("err", err),
				)
			}
			noteInboundTextArrival(d, chatID, entry.PeerID, wrapper)

		default:
			slog.Error("persist decrypted event failed",
				slog.String("envelope_id", entry.Envelope.ID),
				slog.Any("err", persistErr),
			)
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
		if _, err := d.events.AppendReactionBreadcrumb(body.Target, body.Emoji, entry.PeerID, wrapper.MsgID, clampedTs, wrapper.ExpireSeconds); err != nil {
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
			case errors.Is(err, events.ErrDuplicateMsgID):

				slog.Debug("inbound reaction: already held — no second row",
					slog.String("target_msg_id", body.Target),
					slog.String("peer_id", entry.PeerID),
					slog.String("outcome", "noop_duplicate"),
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

		noteHumanState(d, entry, body.State)
		slog.Debug("inbound presence observed",
			slog.String("peer_id", entry.PeerID),
			slog.String("state", body.State),
		)

		applyUnlinkFields(ctx, d, entry.PeerID, "presence", body.SendsUnlinkNotice, body.DeviceCount, body.UnlinkedAt, wrapper.Ts)

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
		ingestFileReceipt(ctx, d, entry.PeerID, body, wrapper.MsgID)

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
		ingestFileKey(ctx, d, entry.PeerID, body.Token, key, nonce, wrapper.MsgID)

	case msg.KindFileDelivered:
		body, err := wrapper.FileDelivered()
		if err != nil {
			slog.Warn("inbound file_delivered: body parse failed",
				slog.String("envelope_id", entry.Envelope.ID),
				slog.String("peer_id", entry.PeerID),
				slog.Any("err", err),
			)
			return
		}

		if _, err := d.events.ApplyDeliveredReceipt(body.Target, chatID); err != nil {
			switch {
			case errors.Is(err, events.ErrEventNotFound):
				slog.Info("inbound file_delivered: target missing (swept or out-of-order)",
					slog.String("target_msg_id", body.Target),
					slog.String("peer_id", entry.PeerID),
				)
			case errors.Is(err, events.ErrReaderNotPeer):
				slog.Warn("inbound file_delivered: reader/chat mismatch; rejecting",
					slog.String("target_msg_id", body.Target),
					slog.String("peer_id", entry.PeerID),
				)
			case errors.Is(err, events.ErrReadUnsupportedKind):
				slog.Warn("inbound file_delivered: target is not a file row; rejecting",
					slog.String("target_msg_id", body.Target),
					slog.String("peer_id", entry.PeerID),
				)
			default:
				slog.Error("inbound file_delivered: apply failed",
					slog.String("target_msg_id", body.Target),
					slog.Any("err", err),
				)
			}
		}

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

		applyCallSenderNick(ctx, d, entry.PeerID, body.SenderNick, wrapper.Ts, "call_offer")
		ingestCallOffer(ctx, d, chatID, entry.PeerID, body.CallID, body.Modalities, body.OutboundKey, body.Tokens, wrapper.Ts)

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
		applyCallSenderNick(ctx, d, entry.PeerID, body.SenderNick, wrapper.Ts, "call_accept")
		applyCallAccept(ctx, d, entry.PeerID, body.CallID, body.OutboundKey, body.Tokens)

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
		applyCallTransition(ctx, d, entry.PeerID, body.CallID, calls.StatusRejected, body.Reason, "call_reject")

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

		endReason := ""
		if body.Reason == msg.CallEndAnsweredElsewhere {
			endReason = msg.CallEndAnsweredElsewhere
		}
		applyCallTransition(ctx, d, entry.PeerID, body.CallID, calls.StatusEnded, endReason, "call_end")

	case msg.KindCallControl:

		body, err := wrapper.CallControl()
		if err != nil {
			slog.Warn("inbound call_control: body parse failed",
				slog.String("envelope_id", entry.Envelope.ID),
				slog.String("peer_id", entry.PeerID),
				slog.Any("err", err),
			)
			return
		}
		callState, lookupErr := d.calls.GetState(body.CallID)
		if lookupErr != nil {
			slog.Debug("inbound call_control: no call-state — dropping (post-hangup or unknown call)",
				slog.String("call_id", body.CallID),
				slog.String("peer_id", entry.PeerID),
				slog.Any("err", lookupErr),
			)
			return
		}

		if callState.MediaPeer() != entry.PeerID {
			slog.Warn("inbound call_control: peer-id mismatch — dropping (possible spoof attempt)",
				slog.String("call_id", body.CallID),
				slog.String("call_peer_id", callState.MediaPeer()),
				slog.String("sender_peer_id", entry.PeerID),
			)
			return
		}
		slog.Debug("inbound call_control: broadcasting peer cam-off signal",
			slog.String("call_id", body.CallID),
			slog.String("peer_id", entry.PeerID),
			slog.String("action", body.Action),
		)
		push(d.ipcSrv, ipc.FrameCallStreamEvent, "", ipc.CallStreamEventPayload{
			CallID: body.CallID,
			Side:   "cam",
			Type:   body.Action,
		})

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

		noteHumanState(d, entry, body.PresenceState)

		clampedTs := events.ClampSenderTs(wrapper.Ts, time.Now().Unix())
		for _, target := range body.Targets {
			if _, _, err := d.events.ApplyReadReceipt(target, clampedTs, chatID); err != nil {
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

	case msg.KindDeviceLinkIdentity:

		zeroBytes(plain)
		slog.Warn("inbound identity bundle dropped",
			slog.String("peer_id", entry.PeerID),
			slog.String("envelope_id", entry.Envelope.ID),
			slog.String("reason", reasonNoPendingLink),
		)

	case msg.KindPairingInvitation:
		applyInboundInvitation(ctx, d, chatID, entry.PeerID, wrapper)
		zeroBytes(plain)

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
			if errors.Is(persistErr, events.ErrDuplicateMsgID) {
				slog.Debug("inbox: unknown-kind message already held — no second row",
					slog.String("envelope_id", entry.Envelope.ID),
					slog.String("msg_id", wrapper.MsgID),
					slog.String("outcome", "noop_duplicate"),
				)
			} else {
				slog.Error("persist unknown-kind event failed",
					slog.String("envelope_id", entry.Envelope.ID),
					slog.Any("err", persistErr),
				)
			}
		} else {
			bumpChatActivity(ctx, d, chatID, time.Now().Unix())
		}
		slog.Warn("inbox: unknown wrapper kind",
			slog.String("kind", string(wrapper.Kind)),
			slog.String("peer_id", entry.PeerID),
		)
	}
}

func applyInboundInvitation(ctx context.Context, d *daemon, chatID chat.ChatID, peerID string, wrapper *msg.Wrapper) {
	logger := slog.With(
		slog.String("peer_id", peerID),
		slog.String("chat_id", string(chatID)),
		slog.String("invitation_id", wrapper.MsgID),
	)
	logger.Debug("invitation: inbound observed")

	if d.invitations == nil || d.events == nil {
		logger.Warn("invitation: drop — frontend wiring incomplete",
			slog.String("reason", reasonNotReady),
		)
		return
	}

	if _, err := d.invitations.Get(wrapper.MsgID); err == nil {
		logger.Debug("invitation: already held — no state change (idempotent redelivery)",
			slog.String("outcome", "noop_duplicate"),
		)
		return
	} else if !errors.Is(err, invitation.ErrNotFound) {
		logger.Warn("invitation: drop — invitation store read failed",
			slog.String("reason", "store_read_failed"),
			slog.Any("err", err),
		)
		return
	}

	body, err := wrapper.PairingInvitation()
	if err != nil {
		logger.Debug("invitation: drop — body unusable",
			slog.String("reason", "bad_invitation"),
			slog.Any("err", err),
		)
		return
	}

	now := time.Now().Unix()
	if reason, ok := unusableInvitationDeadline(body.ExpiresAt, now); !ok {
		logger.Info("invitation: drop — the deadline is not one this device can honour",
			slog.String("reason", reason),
			slog.Int64("expires_at", body.ExpiresAt),
		)
		return
	}

	logger = logger.With(slog.String("offering_device", body.OfferingDevice))

	if liveID, err := invitationAlreadyOutranked(d, chatID, body.OfferingDevice, body.MintedAt); err != nil {
		logger.Warn("invitation: drop — could not read the conversation's earlier invitations",
			slog.String("reason", "store_read_failed"),
			slog.Any("err", err),
		)
		return
	} else if liveID != "" {
		logger.Info("invitation: drop — a newer invitation from that device is already held",
			slog.String("reason", "outranked"),
			slog.String("live_id", liveID),
			slog.Int64("minted_at", body.MintedAt),
		)
		return
	}

	rec := invitation.Record{
		MsgID:          wrapper.MsgID,
		ChatID:         string(chatID),
		Fingerprint:    fingerprintForChat(d, chatID),
		OfferingDevice: body.OfferingDevice,
		Secret:         body.Secret,
		MintedAt:       body.MintedAt,
		ExpiresAt:      body.ExpiresAt,
		State:          events.InvitationLive,
	}
	markerBody, err := json.Marshal(events.InvitationBody{
		ExpiresAt: body.ExpiresAt,
		State:     events.InvitationLive,
	})
	if err == nil {

		_, err = d.events.AppendInbound(events.InboundParams{
			ChatID:        chatID,
			Kind:          events.KindInvitation,
			SenderTs:      wrapper.Ts,
			SenderSeq:     wrapper.Seq,
			MsgID:         wrapper.MsgID,
			ExpireSeconds: wrapper.ExpireSeconds,
			Status:        events.DecryptOK,
			Body:          markerBody,
			Attach: func(txn *badger.Txn) error {
				return d.invitations.PutTxn(txn, rec)
			},
		})
	}
	if errors.Is(err, events.ErrDuplicateMsgID) {

		logger.Debug("invitation: already marked in this conversation — no second row",
			slog.String("outcome", "noop_duplicate"),
		)
		return
	}
	if err != nil {
		logger.Error("invitation: drop — the invitation and its row were not written",
			slog.String("reason", "persist_failed"),
			slog.Any("err", err),
		)
		return
	}
	logger.Debug("invitation: held and marked — local state changed",
		slog.String("outcome", "applied"),
		slog.Any("invitation", rec),
	)

	supersedeOlderInvitations(d, logger, chatID, rec.MsgID, rec.OfferingDevice, rec.MintedAt)

	bumpChatActivity(ctx, d, chatID, time.Now().Unix())
	slog.Info("invitation: received in an existing conversation",
		slog.String("chat_id", string(chatID)),
		slog.String("invitation_id", rec.MsgID),
		slog.Int64("expires_at", rec.ExpiresAt),
	)
}

func invitationAlreadyOutranked(d *daemon, chatID chat.ChatID, offeringDevice string, mintedAt int64) (string, error) {
	held, err := d.invitations.ListByChat(chatID)
	if err != nil {
		return "", err
	}
	for _, rec := range held {
		if rec.OfferingDevice != offeringDevice {
			continue
		}
		if rec.State == events.InvitationLive && rec.MintedAt > mintedAt {
			return rec.MsgID, nil
		}
	}
	return "", nil
}

func supersedeOlderInvitations(d *daemon, logger *slog.Logger, chatID chat.ChatID, keepMsgID, keepOfferingDevice string, keepMintedAt int64) {
	held, err := d.invitations.ListByChat(chatID)
	if err != nil {
		logger.Warn("invitation: could not read the conversation's earlier invitations",
			slog.Any("err", err),
		)
		return
	}
	for _, old := range held {
		if old.OfferingDevice != keepOfferingDevice {
			continue
		}
		if old.MsgID == keepMsgID || old.State != events.InvitationLive || old.MintedAt >= keepMintedAt {
			continue
		}
		markerBody, mErr := json.Marshal(events.InvitationBody{
			ExpiresAt: old.ExpiresAt,
			State:     events.InvitationSuperseded,
		})
		if mErr == nil {
			_, mErr = d.events.UpdateInvitationBody(old.MsgID, markerBody, func(txn *badger.Txn) error {
				return d.invitations.SetStateTxn(txn, old.MsgID, events.InvitationSuperseded)
			})
		}
		if mErr != nil && !errors.Is(mErr, events.ErrEventNotFound) {
			logger.Warn("invitation: an earlier invitation was not retired",
				slog.String("superseded_id", old.MsgID),
				slog.Any("err", mErr),
			)
			continue
		}
		if errors.Is(mErr, events.ErrEventNotFound) {

			logger.Debug("invitation: an earlier invitation has no row left to retire",
				slog.String("superseded_id", old.MsgID),
			)
			continue
		}
		logger.Debug("invitation: an earlier invitation was retired by this one",
			slog.String("superseded_id", old.MsgID),
			slog.String("outcome", "superseded"),
		)
	}
}

const invitationMaxLifetime = 8 * 24 * time.Hour

func unusableInvitationDeadline(expiresAt, now int64) (string, bool) {
	switch {
	case expiresAt <= now:
		return "deadline_passed", false
	case expiresAt > now+int64(invitationMaxLifetime.Seconds()):
		return "deadline_absurd", false
	}
	return "", true
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
		slog.String("from_peer", logid.Short(senderPeerID)),
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
		ChatID:        chatID,
		Kind:          events.KindTimerChange,
		Direction:     events.DirIn,
		DisplayTs:     clampedTs,
		SenderPeerID:  senderPeerID,
		ExpireSeconds: newTTL,
		Body:          body,
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

func chatRetentionTTL(d *daemon, chatID chat.ChatID) uint32 {
	if d == nil || d.chats == nil || chatID == "" {
		return 0
	}
	c, err := d.chats.Get(chatID)
	if err != nil {
		return 0
	}
	return c.Retention()
}

func applyCallSenderNick(ctx context.Context, d *daemon, peerID, senderNick string, senderTs int64, source string) {
	if senderNick == "" || d.peerMeta == nil || peerID == "" {
		return
	}
	clampedTs := events.ClampSenderTs(senderTs, time.Now().Unix())
	changed, err := d.peerMeta.SetNick(peerID, senderNick, clampedTs)
	if err != nil {
		slog.Warn("peerMeta.SetNick (call piggyback) failed",
			slog.String("peer_id", peerID),
			slog.String("source", source),
			slog.Any("err", err),
		)
		return
	}
	slog.Debug("peer-meta nick updated (call piggyback)",
		slog.String("peer_id", peerID),
		slog.String("source", source),
		slog.Int64("clamped_ts", clampedTs),
		slog.Bool("changed", changed),
	)
	if changed {
		personChangedLocally(ctx, d, peerID)
	}
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
		seedPeerFingerprint(d, inv.PeerID)
		if err := pair.DeleteMyKeys(d.store, p.GUID); err != nil {
			slog.Warn("pair: delete return-invite mykeys failed",
				slog.String("guid", p.GUID),
				slog.Any("err", err),
			)
		}
		if d.chats != nil {
			if _, _, err := d.createPairedChat(inv.PeerID, true); err != nil {
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
		)

		push(d.ipcSrv, ipc.FrameAcceptedDHT, "", ipc.AcceptedDHTResponse{
			PeerID:              inv.PeerID,
			Nick:                inv.Frontend.Nick,
			IdentityFingerprint: fingerprint,
		})
		pushPeerPaired(ctx, d, inv.PeerID, "dht-return")

		if err := d.backendClient.DHTCancel(ctx, p.GUID); err != nil {
			slog.Warn("pair: revoke failed after successful return-import",
				slog.String("guid", p.GUID),
				slog.Any("err", err),
			)
		}
	}
}

func noteInboundTextArrival(d *daemon, chatID chat.ChatID, peerID string, wrapper *msg.Wrapper) {

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
	emitInboundNotification(context.Background(), d, chatID, peerID, notifyBody)
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

func peerUnreachableHandler(ctx context.Context, d *daemon) func(backendapi.UnreachableObservation) {
	return func(o backendapi.UnreachableObservation) {
		if o.PeerID == "" || d == nil || d.ipcSrv == nil || d.backendClient == nil {
			return
		}
		slog.Debug("peer.unreachable-changed via SSE",
			slog.String("peer_id", o.PeerID),
			slog.Bool("unreachable", o.Unreachable),
		)
		if skipRowWhileLinking(d, o.PeerID) {
			return
		}
		p, err := d.backendClient.Peer(ctx, o.PeerID)
		if err != nil || p.ID == "" {
			slog.Warn("peer.updated not pushed for the unreachable mark — haomad did not answer for the row",
				slog.String("peer_id", o.PeerID),
				slog.Any("err", err),
			)
			return
		}
		entry, err := peerEntryFor(d, p)
		if err != nil {
			slog.Warn("peer.updated not pushed for the unreachable mark — the row could not be built",
				slog.String("peer_id", o.PeerID),
				slog.Any("err", err),
			)
			return
		}
		push(d.ipcSrv, ipc.FramePeerUpdated, "", ipc.PeerUpdatedPayload{Peer: entry})
		slog.Debug("peer.updated pushed",
			slog.String("peer_id", o.PeerID),
			slog.Bool("unreachable", entry.Unreachable),
		)
	}
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
