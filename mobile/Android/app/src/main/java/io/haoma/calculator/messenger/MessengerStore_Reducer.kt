package io.haoma.calculator.messenger

import io.haoma.calculator.core.ipc.Frame
import io.haoma.calculator.core.ipc.FrameType
import io.haoma.calculator.log.Logger
import kotlinx.coroutines.flow.update


internal fun MessengerStore.dispatch(frame: Frame) {
    val payload = frame.payload
    when (frame.type) {
        FrameType.Welcome -> if (payload != null) {
            val w = WelcomePayload.fromJson(payload)
            _health.update {
                it.copy(
                    selfNick = w.selfNick,
                    selfNickIsDefault = w.selfNickIsDefault,
                    daemonVersion = w.daemonVersion,
                    protocolVersion = w.protocolVersion,
                )
            }
            appendStatus("welcome — daemon ${w.daemonVersion} (protocol v${w.protocolVersion})")
        }

        FrameType.Error -> if (payload != null) {
            val err = ErrorPayload.fromJson(payload)
            appendStatus("error: ${err.code} — ${err.message}", level = StatusLevel.WARN)
        }

        FrameType.Nick -> if (payload != null) {
            val n = NickPayload.fromJson(payload)
            _health.update { it.copy(selfNick = n.nick, selfNickIsDefault = n.isDefault) }
            appendStatus("self-nick → ${n.nick}")
        }

        FrameType.BackendStatus -> if (payload != null) {
            val b = BackendStatusPayload.fromJson(payload)
            val now = System.currentTimeMillis() / 1000L
            _health.update {
                it.copy(
                    backendReachable = b.backendReachable,
                    tor = b.tor,
                    onionCount = b.onionCount,
                    backendStatusAt = now,
                )
            }
        }

        FrameType.PeerSelfReachChanged -> if (payload != null) {
            val p = PeerSelfReachPayload.fromJson(payload)
            if (p.peerId.isNotEmpty()) {
                _health.update { s ->
                    s.copy(selfReach = s.selfReach + (p.peerId to SelfReach(p.onion, p.ok, p.at)))
                }
            }
        }

        FrameType.ExternalReachChanged -> if (payload != null) {
            val e = ExternalReachPayload.fromJson(payload)
            _health.update { it.copy(externalReach = ExternalReach(e.ok, e.lastTarget, e.at)) }
        }

        FrameType.PeerUpdated -> if (payload != null) {
            val p = PeerUpdatedPayload.fromJson(payload).peer
            if (p.id.isNotEmpty()) upsertPeer(p)
        }

        FrameType.PeerDeleted -> if (payload != null) {
            val pid = PeerDeletedPayload.fromJson(payload).peerId
            if (pid.isNotEmpty()) {
                _peers.update { list -> list.filterNot { it.id == pid } }
                _presence.update { it - pid }
                appendStatus("peer deleted: ${pid.take(8)}")
            }
        }

        FrameType.PeerLastSeenChanged -> if (payload != null) {
            val u = PeerLastSeenChangedPayload.fromJson(payload)
            if (u.peerId.isNotEmpty()) {
                mapPeer(u.peerId) { it.copy(lastActiveAt = u.lastActiveAt, lastPassiveAt = u.lastPassiveAt) }
            }
        }

        FrameType.PresenceChanged -> if (payload != null) {
            val u = PresenceChangedPayload.fromJson(payload)
            if (u.peerId.isNotEmpty()) {
                _presence.update { it + (u.peerId to u.effective) }
                mapPeer(u.peerId) {
                    it.copy(accepting = u.accepting, chatty = u.chatty, effective = u.effective)
                }
            }
        }

        FrameType.ChatUpdated -> if (payload != null) {
            val c = ChatUpdatedPayload.fromJson(payload).chat
            if (c.chatId.isNotEmpty()) upsertChat(c)
        }

        FrameType.ChatCleared -> if (payload != null) {
            val u = ChatClearedPayload.fromJson(payload)
            if (u.chatId.isNotEmpty()) {
                _timelines.update { it - u.chatId }
                forgetEnvelopesFor(u.chatId)
            }
            appendStatus("chat cleared: ${shortChat(u.chatId)} (${u.deletedCount} events)")
        }

        FrameType.ChatDeleted -> if (payload != null) {
            val u = ChatDeletedPayload.fromJson(payload)
            if (u.chatId.isNotEmpty()) {
                _chats.update { list -> list.filterNot { it.chatId == u.chatId } }
                _timelines.update { it - u.chatId }
                forgetEnvelopesFor(u.chatId)
                appendStatus("chat deleted: ${shortChat(u.chatId)} (${u.deletedCount} events)")
            }
        }

        FrameType.ChatActivityChanged -> if (payload != null) {
            val u = ChatActivityChangedPayload.fromJson(payload)
            if (u.chatId.isNotEmpty()) {
                mapChat(u.chatId) { it.copy(lastActivityAt = u.lastActivityAt) }
            }
        }

        FrameType.ChatUnreadChanged -> if (payload != null) {
            val u = ChatUnreadChangedPayload.fromJson(payload)
            if (u.chatId.isNotEmpty()) {
                mapChat(u.chatId) { it.copy(unreadCount = u.unreadCount) }
            }
        }

        FrameType.PeerPaired -> if (payload != null) {
            val u = PeerPairedPush.fromJson(payload)
            appendStatus("paired — peer ${shortChat(u.peerId)} via ${u.source}")
        }

        FrameType.PairOnionProbe -> if (payload != null) {
            val p = PairOnionProbePush.fromJson(payload)
            if (p.ready) {
                _pendingInvites.update { list ->
                    list.map { pi ->
                        if (pi.handleId == p.handleId && !pi.ready) {
                            pi.copy(ready = true, probeNote = p.error)
                        } else pi
                    }
                }
                if (p.error.isNotEmpty()) {
                    appendStatus(
                        "onion ${shortChat(p.handleId)}: descriptor slow — sharing words anyway",
                        level = StatusLevel.WARN,
                    )
                } else {
                    appendStatus("onion ${shortChat(p.handleId)}: ready — share words OOB")
                }
            }
        }

        FrameType.PairOnionCompleted -> if (payload != null) {
            val c = PairOnionCompletedPush.fromJson(payload)
            movePendingToRecent(
                handleId = c.handleId,
                outcome = RecentOutcome.Success,
                peerId = c.peerId,
                nick = c.nick,
            )
        }

        FrameType.PairOnionFailed -> if (payload != null) {
            val f = PairOnionFailedPush.fromJson(payload)
            movePendingToRecent(
                handleId = f.handleId,
                outcome = RecentOutcome.Failed,
                reason = f.reason,
            )
            appendStatus(
                "onion ${shortChat(f.handleId)} failed: ${f.reason}",
                level = StatusLevel.WARN,
            )
        }

        FrameType.TimelineEvent -> if (payload != null) {
            val ev = TimelineEventPayload.fromJson(payload).event
            if (ev.chatId.isNotEmpty()) {
                if (ev.isOutbound && ev.envelopeId.isNotEmpty()) {
                    rememberEnvelope(ev.envelopeId, ev.chatId)
                }
                upsertTimeline(ev.chatId) { mergeTimelineEvent(it, ev) }
            }
        }

        FrameType.DeliveryStatus -> if (payload != null) {
            val u = DeliveryStatusPayload.fromJson(payload)
            if (u.envelopeId.isNotEmpty()) {
                val chatId = lookupEnvelope(u.envelopeId)
                if (chatId != null) {
                    upsertTimeline(chatId) { applyDeliveryStatus(it, u.envelopeId, u.state) }
                }
            }
        }

        FrameType.TimelineEventDeleted -> if (payload != null) {
            val u = TimelineEventDeletedPayload.fromJson(payload)
            if (u.chatId.isNotEmpty() && u.recvSeq > 0L) {
                upsertTimeline(u.chatId) { removeTimelineEvent(it, u.recvSeq) }
            }
        }

        FrameType.ChatSettings -> if (payload != null) {
            val s = ChatSettingsPayload.fromJson(payload)
            if (s.chatId.isNotEmpty()) {
                mapChat(s.chatId) {
                    it.copy(
                        retentionTtl = s.retentionTtl.toLong(),
                        disableReadReceipts = s.disableReadReceipts,
                        notificationsMuted = s.notificationsMuted,
                    )
                }
            }
        }

        FrameType.CallStateChanged -> if (payload != null) {
            val p = CallStateChangedPayload.fromJson(payload)
            if (p.call.callId.isNotEmpty()) applyCallStateChange(p.call)
        }

        
        FrameType.CallStreamEvent -> if (payload != null) {
            val ev = CallStreamEventPayload.fromJson(payload)
            when (ev.type) {
                "stats" -> if (ev.callId.isNotEmpty()) {
                    val now = System.currentTimeMillis()
                    _callStreamState.update { prev ->
                        val cur = prev[ev.callId] ?: CallStreamState()
                        val side = CallStreamSide(
                            lastSampleAtMs = now,
                            framesOut = ev.framesOut,
                            prevFramesOut = when (ev.side) {
                                "mic" -> cur.mic?.framesOut ?: ev.framesOut
                                "spk" -> cur.spk?.framesOut ?: ev.framesOut
                                else -> ev.framesOut
                            },
                            jitterMs = ev.jitterMs,
                        )
                        val next = when (ev.side) {
                            "mic" -> cur.copy(mic = side)
                            "spk" -> cur.copy(spk = side)
                            else -> cur
                        }.let { s ->
                            if (ev.framesDropped > s.dropped) s.copy(dropped = ev.framesDropped) else s
                        }
                        prev + (ev.callId to next)
                    }
                }
                "warn" -> appendStatus(
                    "call streamer warn (${shortCallId(ev.callId)}/${ev.side}): ${ev.reason}",
                    level = StatusLevel.WARN,
                )
                "error" -> appendStatus(
                    "call streamer error (${shortCallId(ev.callId)}/${ev.side}): ${ev.reason}",
                    level = StatusLevel.WARN,
                )
                
            }
        }

        FrameType.RotateLifecycle -> if (payload != null) {
            val r = RotateLifecyclePush.fromJson(payload)
            val tail = if (r.reason.isNotEmpty()) " — ${r.reason}" else ""
            appendStatus(
                "rotate ${r.role} ${r.state}: ${shortChat(r.peerId)}$tail",
                level = if (r.state == "failed") StatusLevel.WARN else StatusLevel.INFO,
            )
        }

        FrameType.FileProgress -> if (payload != null) {
            val p = FileProgressPayload.fromJson(payload)
            Logger.d(
                "messenger",
                "file-progress chat=${shortChat(p.chatId)} msg=${p.msgId.take(8)} " +
                    "${p.bytesReceived}/${p.totalBytes}",
            )
        }

        FrameType.RotateRequested -> if (payload != null) {
            appendStatus("rotate requested by peer (use TUI for accept modal)")
        }

        FrameType.NotificationEmitted -> if (payload != null) {
            val n = NotificationEmittedPayload.fromJson(payload)
            val who = n.peerLabel.ifEmpty { shortChat(n.chatId) }
            appendStatus("notify ($who): ${n.title}")
            
            
            notificationPoster?.post(n)
        }

        
        else -> Logger.d("messenger", "frame received: type=${frame.type} id=${frame.id ?: "-"}")
    }
}
