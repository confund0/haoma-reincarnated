package io.haoma.calculator.messenger

import io.haoma.calculator.core.ipc.FrameType
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch


fun MessengerStore.inviteOnion(alias: String) {
    scope.launch {
        val c = ipc ?: run {
            appendStatus("invite: ipc not connected", level = StatusLevel.WARN)
            return@launch
        }
        val trimmed = alias.trim()
        try {
            val reply = c.request(
                type = FrameType.PairOnionInvite,
                payload = PairOnionInviteRequest(alias = trimmed).toJson(),
            )
            if (reply.type == FrameType.Error) {
                val err = reply.payload?.let(ErrorPayload::fromJson)
                appendStatus("invite error: ${err?.message ?: "?"}", level = StatusLevel.WARN)
                return@launch
            }
            if (reply.type != FrameType.PairOnionStarted) {
                appendStatus(
                    "invite unexpected response: ${reply.type}",
                    level = StatusLevel.WARN,
                )
                return@launch
            }
            val started = reply.payload?.let(PairOnionStartedResponse::fromJson) ?: run {
                appendStatus("invite: daemon returned empty payload", level = StatusLevel.WARN)
                return@launch
            }
            _pendingInvites.update { list ->
                list + PendingInvite(
                    handleId = started.handleId,
                    alias = trimmed,
                    words = started.words,
                    ready = false,
                    expiresAt = started.expiresAt,
                    createdAt = System.currentTimeMillis(),
                    probeNote = "",
                )
            }
            appendStatus(
                "invite ${shortChat(started.handleId)}: publishing onion (~30–60s)…",
            )
        } catch (t: Throwable) {
            appendStatus("invite failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
        }
    }
}


suspend fun MessengerStore.acceptOnion(words: List<String>, alias: String): AcceptResult {
    val c = ipc ?: return AcceptResult.Error("daemon not connected")
    return try {
        val reply = c.request(
            type = FrameType.PairOnionAccept,
            payload = PairOnionAcceptRequest(words = words, alias = alias.trim()).toJson(),
        )
        if (reply.type == FrameType.Error) {
            val err = reply.payload?.let(ErrorPayload::fromJson)
            return AcceptResult.Error(err?.message ?: "unknown error")
        }
        if (reply.type != FrameType.PairOnionAccepted) {
            return AcceptResult.Error("unexpected response: ${reply.type}")
        }
        val accepted = reply.payload?.let(PairOnionAcceptedResponse::fromJson)
            ?: return AcceptResult.Error("daemon returned empty payload")
        val label = accepted.nick.ifEmpty { "(no nick)" }
        appendStatus("paired (onion) — $label / ${shortChat(accepted.peerId)}")
        AcceptResult.Ok(accepted)
    } catch (t: Throwable) {
        AcceptResult.Error(t.message ?: "unknown failure")
    }
}


fun MessengerStore.cancelInvite(handleId: String) {
    scope.launch {
        val c = ipc ?: run {
            appendStatus("cancel: ipc not connected", level = StatusLevel.WARN)
            return@launch
        }
        try {
            val reply = c.request(
                type = FrameType.PairOnionCancel,
                payload = PairOnionCancelRequest(handleId).toJson(),
            )
            if (reply.type == FrameType.Error) {
                val err = reply.payload?.let(ErrorPayload::fromJson)
                appendStatus("cancel error: ${err?.message ?: "?"}", level = StatusLevel.WARN)
                return@launch
            }
            movePendingToRecent(handleId, RecentOutcome.Cancelled)
            appendStatus("cancelled invite ${shortChat(handleId)}")
        } catch (t: Throwable) {
            appendStatus("cancel failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
        }
    }
}


data class PendingInvite(
    val handleId: String,
    val alias: String,
    val words: List<String>,
    val ready: Boolean,
    val expiresAt: Long,
    val createdAt: Long,
    val probeNote: String,
)

enum class RecentOutcome { Success, Failed, Cancelled }


data class RecentInvite(
    val handleId: String,
    val alias: String,
    val outcome: RecentOutcome,
    val peerId: String,
    val nick: String,
    val reason: String,
    val at: Long,
)


sealed interface AcceptResult {
    data class Ok(val accepted: PairOnionAcceptedResponse) : AcceptResult
    data class Error(val message: String) : AcceptResult
}
