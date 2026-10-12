package io.haoma.calculator.messenger

import io.haoma.calculator.core.ipc.FrameType
import io.haoma.calculator.log.Logger
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
    if (ipc == null && requestSender == null && timedRequestSender == null) {
        return AcceptResult.Error("daemon not connected")
    }
    return try {
        
        
        val reply = askUnlessDropped(
            FrameType.PairOnionAccept,
            PairOnionAcceptRequest(words = words, alias = alias.trim()).toJson(),
            PAIR_ONION_ACCEPT_TIMEOUT,
        )
        if (reply.type == FrameType.Error) {
            val err = reply.payload?.let(ErrorPayload::fromJson)
            val code = err?.code.orEmpty()
            if (code == REASON_NO_ANSWER) return AcceptResult.Error(PAIR_JOIN_NO_ANSWER, code = code)
            return AcceptResult.Error(err?.message ?: "unknown error", code = code)
        }
        if (reply.type != FrameType.PairOnionAccepted) {
            return AcceptResult.Error("unexpected response: ${reply.type}")
        }
        val accepted = reply.payload?.let(PairOnionAcceptedResponse::fromJson)
            ?: return AcceptResult.Error("daemon returned empty payload")
        appendStatus("paired (onion) — ${shortChat(accepted.peerId)}")
        markFreshPeer(accepted.peerId)
        AcceptResult.Ok(accepted)
    } catch (t: Throwable) {
        AcceptResult.Error(t.message ?: "unknown failure")
    }
}


data class PairJoinState(
    val wordsInput: String = "",
    val waitingSinceMs: Long = 0L,
    val error: String = "",
)


fun MessengerStore.setPairJoinWords(text: String) {
    _pairJoin.update { it.copy(wordsInput = text, error = "") }
}


fun MessengerStore.startPairJoin(wordsLookValid: Boolean = true) {
    scope.launch { runPairJoin(wordsLookValid) }
}

internal const val PAIR_JOIN_WORDS_NEEDED = "Need the 7 words from the invite."


internal suspend fun MessengerStore.runPairJoin(
    wordsLookValid: Boolean = true,
    nowMs: () -> Long = { System.currentTimeMillis() },
) {
    
    
    var tokens: List<String>? = null
    val startedAt = nowMs()
    _pairJoin.update { cur ->
        tokens = null
        val typed = splitWords(cur.wordsInput)
        when {
            cur.waitingSinceMs != 0L -> cur
            typed.size < 3 -> cur.copy(error = PAIR_JOIN_WORDS_NEEDED)
            !wordsLookValid -> cur.copy(error = JOIN_WORDS_WRONG)
            else -> {
                tokens = typed
                cur.copy(waitingSinceMs = startedAt, error = "")
            }
        }
    }
    val words = tokens ?: return
    when (val r = acceptOnion(words, "")) {
        is AcceptResult.Ok -> _pairJoin.value = PairJoinState()
        is AcceptResult.Error -> {
            val line = joinFailureLine(r.code)
            if (line != null) Logger.i("pairing", "join refused reason=${r.code}")
            _pairJoin.update {
                it.copy(
                    waitingSinceMs = 0L,
                    
                    wordsInput = if (r.code == REASON_JOIN_INVITATION_SPENT) "" else it.wordsInput,
                    error = line ?: r.message,
                )
            }
        }
    }
}


fun MessengerStore.renamePendingInvite(handleId: String, alias: String) {
    _pendingInvites.update { list ->
        list.map {
            if (it.handleId == handleId) it.copy(alias = alias.trim()) else it
        }
    }
}


fun MessengerStore.removeRecentInvite(handleId: String) {
    _recentInvites.update { list -> list.filterNot { it.handleId == handleId } }
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

    
    data class Error(
        val message: String,
        val code: String = "",
    ) : AcceptResult
}
