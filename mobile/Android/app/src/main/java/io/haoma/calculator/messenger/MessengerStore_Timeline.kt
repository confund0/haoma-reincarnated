package io.haoma.calculator.messenger

import io.haoma.calculator.core.ipc.FrameType
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.launch


fun MessengerStore.timelineFor(chatId: String): StateFlow<TimelineCache> =
    _timelines
        .map { it[chatId] ?: TimelineCache(chatId = chatId) }
        .distinctUntilChanged()
        .stateIn(scope, SharingStarted.Eagerly, TimelineCache(chatId = chatId))


fun MessengerStore.loadTimeline(chatId: String) {
    if (chatId.isEmpty()) return
    val peerId = _chats.value.firstOrNull { it.chatId == chatId }?.peerId.orEmpty()
    if (peerId.isEmpty()) {
        appendStatus("load timeline: chat not snapshot-loaded yet ($chatId)", level = StatusLevel.WARN)
        return
    }
    val current = _timelines.value[chatId] ?: TimelineCache(chatId = chatId)
    if (current.loading) return
    if (!current.hasMore && current.events.isNotEmpty()) return

    upsertTimeline(chatId) { it.copy(loading = true) }

    scope.launch {
        val c = ipc ?: run {
            upsertTimeline(chatId) { it.copy(loading = false) }
            appendStatus("load timeline: ipc not connected", level = StatusLevel.WARN)
            return@launch
        }
        try {
            val reply = c.request(
                type = FrameType.ListTimeline,
                payload = ListTimelineRequest(
                    peerId = peerId,
                    beforeDisplayTs = current.oldestDisplayTs,
                ).toJson(),
            )
            if (reply.type == FrameType.Error) {
                val err = reply.payload?.let(ErrorPayload::fromJson)
                appendStatus("list_timeline error: ${err?.message ?: "?"}", level = StatusLevel.WARN)
                upsertTimeline(chatId) { it.copy(loading = false) }
                return@launch
            }
            val page = reply.payload?.let(TimelinePageResponse::fromJson) ?: run {
                upsertTimeline(chatId) { it.copy(loading = false) }
                return@launch
            }
            indexEnvelopes(chatId, page.events)
            upsertTimeline(chatId) { mergeTimelinePage(it, page) }
        } catch (t: Throwable) {
            appendStatus("list_timeline failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
            upsertTimeline(chatId) { it.copy(loading = false) }
        }
    }
}


fun MessengerStore.sendText(chatId: String, text: String) {
    val trimmed = text.trim()
    if (chatId.isEmpty() || trimmed.isEmpty()) return
    val peerId = peerIdOrWarn(chatId, "send") ?: return
    scope.launch {
        val c = ipc ?: run {
            appendStatus("send: ipc not connected", level = StatusLevel.WARN)
            return@launch
        }
        try {
            val reply = c.request(
                type = FrameType.SendText,
                payload = SendTextRequest(peerId, trimmed).toJson(),
            )
            if (reply.type == FrameType.Error) {
                val err = reply.payload?.let(ErrorPayload::fromJson)
                appendStatus("send error: ${err?.message ?: "?"}", level = StatusLevel.WARN)
                return@launch
            }
            val resp = reply.payload?.let(SendTextResponse::fromJson) ?: return@launch
            if (resp.envelopeId.isNotEmpty()) {
                rememberEnvelope(resp.envelopeId, chatId)
            }
        } catch (t: Throwable) {
            appendStatus("send failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
        }
    }
}


fun MessengerStore.sendReaction(chatId: String, targetMsgId: String, emoji: String) {
    if (chatId.isEmpty() || targetMsgId.isEmpty()) return
    val peerId = peerIdOrWarn(chatId, "react") ?: return
    scope.launch {
        val c = ipc ?: run {
            appendStatus("react: ipc not connected", level = StatusLevel.WARN)
            return@launch
        }
        try {
            val reply = c.request(
                type = FrameType.SendReaction,
                payload = SendReactionRequest(peerId, targetMsgId, emoji).toJson(),
            )
            if (reply.type == FrameType.Error) {
                val err = reply.payload?.let(ErrorPayload::fromJson)
                appendStatus("react error: ${err?.message ?: "?"}", level = StatusLevel.WARN)
            }
        } catch (t: Throwable) {
            appendStatus("react failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
        }
    }
}


fun MessengerStore.sendEdit(chatId: String, targetMsgId: String, text: String) {
    val trimmed = text.trim()
    if (chatId.isEmpty() || targetMsgId.isEmpty() || trimmed.isEmpty()) return
    val peerId = peerIdOrWarn(chatId, "edit") ?: return
    scope.launch {
        val c = ipc ?: run {
            appendStatus("edit: ipc not connected", level = StatusLevel.WARN)
            return@launch
        }
        try {
            val reply = c.request(
                type = FrameType.SendEdit,
                payload = SendEditRequest(peerId, targetMsgId, trimmed).toJson(),
            )
            if (reply.type == FrameType.Error) {
                val err = reply.payload?.let(ErrorPayload::fromJson)
                appendStatus("edit error: ${err?.message ?: "?"}", level = StatusLevel.WARN)
            }
        } catch (t: Throwable) {
            appendStatus("edit failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
        }
    }
}


fun MessengerStore.sendDelete(chatId: String, targetMsgId: String) {
    if (chatId.isEmpty() || targetMsgId.isEmpty()) return
    val peerId = peerIdOrWarn(chatId, "delete") ?: return
    scope.launch {
        val c = ipc ?: run {
            appendStatus("delete: ipc not connected", level = StatusLevel.WARN)
            return@launch
        }
        try {
            val reply = c.request(
                type = FrameType.SendDelete,
                payload = SendDeleteRequest(peerId, targetMsgId).toJson(),
            )
            if (reply.type == FrameType.Error) {
                val err = reply.payload?.let(ErrorPayload::fromJson)
                appendStatus("delete error: ${err?.message ?: "?"}", level = StatusLevel.WARN)
            }
        } catch (t: Throwable) {
            appendStatus("delete failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
        }
    }
}


fun MessengerStore.toggleReaction(chatId: String, targetMsgId: String, tappedEmoji: String) {
    if (tappedEmoji.isEmpty()) return
    val cache = _timelines.value[chatId]
    
    
    val mine = cache?.reactionsByTarget?.get(targetMsgId)?.get("")
    val outbound = if (mine?.emoji == tappedEmoji) "" else tappedEmoji
    sendReaction(chatId, targetMsgId, outbound)
}
