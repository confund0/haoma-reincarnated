package io.haoma.calculator.messenger

import io.haoma.calculator.core.ipc.FrameType
import io.haoma.calculator.log.Logger
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch


fun MessengerStore.updateRecordAudioGranted(granted: Boolean) {
    _recordAudioGranted.value = granted
}


fun MessengerStore.updateBluetoothConnectGranted(granted: Boolean) {
    _bluetoothConnectGranted.value = granted
}


fun MessengerStore.acceptedCallForPeer(peerId: String): CallEntry? {
    if (peerId.isEmpty()) return null
    return _activeCalls.value.values
        .firstOrNull { it.peerId == peerId && it.status == CallStatus.Accepted }
}


fun MessengerStore.acceptedCallForChat(chatId: String): CallEntry? {
    if (chatId.isEmpty()) return null
    return _activeCalls.value.values
        .firstOrNull { it.chatId == chatId && it.status == CallStatus.Accepted }
}


fun MessengerStore.activeCallForChat(chatId: String): CallEntry? {
    if (chatId.isEmpty()) return null
    return _activeCalls.value.values
        .filter { it.chatId == chatId && !it.isTerminal }
        .maxByOrNull { it.startedAt }
}


fun MessengerStore.hangupCallInChat(chatId: String) {
    val target = activeCallForChat(chatId) ?: return
    respondCall(target.callId, CallAction.End)
}


fun MessengerStore.hangupLatest() {
    val target = _activeCalls.value.values
        .filter { it.status == CallStatus.Accepted }
        .maxByOrNull { it.startedAt }
        ?: return
    respondCall(target.callId, CallAction.End)
}


fun MessengerStore.toggleMute(callId: String) {
    if (callId.isEmpty()) return
    val nowMuted = _mutedCalls.value[callId] ?: false
    val action = if (nowMuted) CallControlAction.Unmute else CallControlAction.Mute
    Logger.i("call", "toggleMute dispatch call=${shortCallId(callId)} -> $action")
    scope.launch {
        val c = ipc ?: run {
            appendStatus("mute: ipc not connected", level = StatusLevel.WARN)
            return@launch
        }
        try {
            val reply = c.request(
                type = FrameType.CallControl,
                payload = CallControlRequest(callId, action).toJson(),
            )
            if (reply.type == FrameType.Error) {
                val err = reply.payload?.let(ErrorPayload::fromJson)
                appendStatus("mute error: ${err?.message ?: "?"}", level = StatusLevel.WARN)
                return@launch
            }
            val ok = reply.payload?.let(CallControlledResponse::fromJson)
            val applied = ok?.action == action
            if (applied) {
                _mutedCalls.update { it + (callId to (action == CallControlAction.Mute)) }
            }
        } catch (t: Throwable) {
            appendStatus("mute failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
        }
    }
}


fun MessengerStore.startCall(chatId: String) {
    Logger.i("call", "startCall dispatch chat=${shortChat(chatId)} rec_audio=${recordAudioGranted.value}")
    if (!recordAudioGranted.value) {
        appendStatus(
            "call: microphone permission not granted — grant RECORD_AUDIO in system Settings first",
            level = StatusLevel.WARN,
        )
        return
    }
    val existing = findActiveCallForChat(chatId)
    if (existing != null) {
        appendStatus(
            "call: already ${existing.status} in this chat (call_id=${shortCallId(existing.callId)})",
            level = StatusLevel.WARN,
        )
        return
    }
    scope.launch {
        val c = ipc ?: run {
            appendStatus("call: ipc not connected", level = StatusLevel.WARN)
            return@launch
        }
        try {
            val reply = c.request(
                type = FrameType.StartCall,
                payload = StartCallRequest(chatId).toJson(),
            )
            if (reply.type == FrameType.Error) {
                val err = reply.payload?.let(ErrorPayload::fromJson)
                appendStatus("call error: ${err?.message ?: "?"}", level = StatusLevel.WARN)
                return@launch
            }
            if (reply.type != FrameType.CallStarted) {
                appendStatus("call unexpected response: ${reply.type}", level = StatusLevel.WARN)
                return@launch
            }
            val started = reply.payload?.let(CallStartedResponse::fromJson) ?: run {
                appendStatus("call: daemon returned empty payload", level = StatusLevel.WARN)
                return@launch
            }
            
            
            upsertActiveCall(started.call)
            appendStatus("calling ${peerLabelFor(started.call.peerId)} (call_id=${shortCallId(started.callId)})")
        } catch (t: Throwable) {
            appendStatus("call failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
        }
    }
}


fun MessengerStore.respondCall(callId: String, action: String, reason: String = "") {
    Logger.i("call", "respondCall dispatch call=${shortCallId(callId)} action=$action")
    scope.launch {
        val c = ipc ?: run {
            appendStatus("respond: ipc not connected", level = StatusLevel.WARN)
            return@launch
        }
        try {
            val reply = c.request(
                type = FrameType.RespondCall,
                payload = RespondCallRequest(callId, action, reason).toJson(),
            )
            if (reply.type == FrameType.Error) {
                val err = reply.payload?.let(ErrorPayload::fromJson)
                appendStatus("respond error: ${err?.message ?: "?"}", level = StatusLevel.WARN)
                return@launch
            }
            
            
        } catch (t: Throwable) {
            appendStatus("respond failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
        }
    }
}


internal fun MessengerStore.applyCallStateChange(call: CallEntry) {
    val prior = _activeCalls.value[call.callId]
    upsertActiveCall(call)
    val label = peerLabelFor(call.peerId)
    Logger.i(
        "call",
        "state-changed dir=${call.direction} status=${call.status} " +
            "peer=${shortChat(call.peerId)} call=${shortCallId(call.callId)} " +
            "prior=${prior?.status ?: "none"}",
    )

    
    val poster = notificationPoster
    if (poster != null) {
        if (call.direction == CallDirection.In && call.status == CallStatus.Ringing) {
            poster.postCall(
                callId = call.callId,
                chatId = call.chatId,
                peerLabel = label,
                softLocked = lastSoftLocked,
            )
        } else if (call.isTerminal) {
            poster.cancelCall(call.callId)
        }
    }

    when (call.direction) {
        CallDirection.In -> when (call.status) {
            CallStatus.Ringing -> appendStatus("ringing — incoming call from $label")
            CallStatus.Accepted -> appendStatus("connected — call with $label")
            CallStatus.Rejected -> appendStatus("call from $label ended (declined)")
            CallStatus.Ended -> {
                if (prior?.status == CallStatus.Ringing) {
                    appendStatus("missed call from $label", level = StatusLevel.WARN)
                } else {
                    appendStatus("call from $label ended")
                }
            }
            CallStatus.Failed -> appendStatus(
                "call from $label failed: ${call.failReason}",
                level = StatusLevel.WARN,
            )
        }
        CallDirection.Out -> when (call.status) {
            CallStatus.Offered -> Unit 
            CallStatus.Accepted -> appendStatus("connected — $label answered")
            CallStatus.Rejected -> {
                val reason = call.failReason.ifEmpty { "declined" }
                appendStatus("$label declined: $reason")
            }
            CallStatus.Ended -> appendStatus("call with $label ended")
            CallStatus.Failed -> appendStatus(
                "outgoing call to $label failed: ${call.failReason}",
                level = StatusLevel.WARN,
            )
        }
    }
}


internal fun MessengerStore.upsertActiveCall(call: CallEntry) {
    if (call.callId.isEmpty()) return
    if (call.isTerminal) {
        _activeCalls.update { it - call.callId }
        
        
        _mutedCalls.update { it - call.callId }
        _callJitter.update { it - call.callId }
        return
    }
    _activeCalls.update { it + (call.callId to call) }
}


internal fun MessengerStore.findActiveCallForChat(chatId: String): CallEntry? =
    _activeCalls.value.values
        .filter { it.chatId == chatId }
        .maxByOrNull { it.startedAt }


internal fun MessengerStore.findRingingIncoming(): List<CallEntry> =
    _activeCalls.value.values
        .filter { it.direction == CallDirection.In && it.status == CallStatus.Ringing }
        .toList()


internal fun MessengerStore.findActiveCalls(): List<CallEntry> =
    _activeCalls.value.values.toList()


internal fun MessengerStore.resolveChatByAlias(query: String): String? {
    val q = query.trim()
    if (q.isEmpty()) return null
    val ql = q.lowercase()
    val candidates = _chats.value.filter { row ->
        
        if (row.peerId.isEmpty()) return@filter false
        if (row.peerId.startsWith(q)) return@filter true
        peerNickFor(row.peerId).lowercase().contains(ql)
    }
    return when (candidates.size) {
        0 -> null
        1 -> candidates.first().chatId
        else -> null
    }
}


internal fun MessengerStore.peerLabelFor(peerId: String): String {
    if (peerId.isEmpty()) return "<unknown>"
    val label = _peers.value.firstOrNull { it.id == peerId }?.label.orEmpty()
    return label.ifEmpty { shortChat(peerId) }
}


private fun MessengerStore.peerNickFor(peerId: String): String =
    _peers.value.firstOrNull { it.id == peerId }?.let { it.alias.ifEmpty { it.nick } }.orEmpty()


internal fun shortCallId(callId: String): String =
    if (callId.length <= 8) callId else callId.substring(0, 8) + "…"
