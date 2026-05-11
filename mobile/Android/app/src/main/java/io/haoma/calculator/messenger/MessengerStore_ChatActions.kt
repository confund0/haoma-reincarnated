package io.haoma.calculator.messenger

import io.haoma.calculator.core.ipc.FrameType
import kotlinx.coroutines.launch


fun MessengerStore.setChatMute(chatId: String, muted: Boolean) {
    if (chatId.isEmpty()) return
    val chat = _chats.value.firstOrNull { it.chatId == chatId } ?: run {
        appendStatus("mute: chat not found ($chatId)", level = StatusLevel.WARN)
        return
    }
    if (chat.notificationsMuted == muted) return
    scope.launch {
        val c = ipc ?: run {
            appendStatus("mute: ipc not connected", level = StatusLevel.WARN)
            return@launch
        }
        try {
            val reply = c.request(
                type = FrameType.SetChatSettings,
                payload = SetChatSettingsRequest(
                    chatId = chatId,
                    retentionTtl = chat.retentionTtl.toInt(),
                    disableReadReceipts = chat.disableReadReceipts,
                    notificationsMuted = muted,
                ).toJson(),
            )
            if (reply.type == FrameType.Error) {
                val err = reply.payload?.let(ErrorPayload::fromJson)
                appendStatus("mute error: ${err?.message ?: "?"}", level = StatusLevel.WARN)
                return@launch
            }
            appendStatus(
                "${if (muted) "muted" else "unmuted"} ${shortChat(chatId)}",
            )
        } catch (t: Throwable) {
            appendStatus("mute failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
        }
    }
}


fun MessengerStore.setChatSettings(
    chatId: String,
    retentionTtl: Int,
    disableReadReceipts: Boolean,
    notificationsMuted: Boolean,
) {
    if (chatId.isEmpty()) return
    scope.launch {
        val c = ipc ?: run {
            appendStatus("chat settings: ipc not connected", level = StatusLevel.WARN)
            return@launch
        }
        try {
            val reply = c.request(
                type = FrameType.SetChatSettings,
                payload = SetChatSettingsRequest(
                    chatId = chatId,
                    retentionTtl = retentionTtl,
                    disableReadReceipts = disableReadReceipts,
                    notificationsMuted = notificationsMuted,
                ).toJson(),
            )
            if (reply.type == FrameType.Error) {
                val err = reply.payload?.let(ErrorPayload::fromJson)
                appendStatus("chat settings error: ${err?.message ?: "?"}", level = StatusLevel.WARN)
                return@launch
            }
            appendStatus("settings saved ${shortChat(chatId)}")
        } catch (t: Throwable) {
            appendStatus("chat settings failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
        }
    }
}


fun MessengerStore.clearChat(chatId: String) {
    if (chatId.isEmpty()) return
    scope.launch {
        val c = ipc ?: run {
            appendStatus("cleared: ipc not connected", level = StatusLevel.WARN)
            return@launch
        }
        try {
            val reply = c.request(
                type = FrameType.ChatAction,
                payload = ChatActionRequest(chatId, ChatAction.Clear).toJson(),
            )
            if (reply.type == FrameType.Error) {
                val err = reply.payload?.let(ErrorPayload::fromJson)
                appendStatus("cleared error: ${err?.message ?: "?"}", level = StatusLevel.WARN)
            }
            
            
        } catch (t: Throwable) {
            appendStatus("cleared failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
        }
    }
}


fun MessengerStore.deleteChat(chatId: String) {
    if (chatId.isEmpty()) return
    scope.launch {
        val c = ipc ?: run {
            appendStatus("deleted: ipc not connected", level = StatusLevel.WARN)
            return@launch
        }
        try {
            val reply = c.request(
                type = FrameType.ChatAction,
                payload = ChatActionRequest(chatId, ChatAction.Delete).toJson(),
            )
            if (reply.type == FrameType.Error) {
                val err = reply.payload?.let(ErrorPayload::fromJson)
                appendStatus("deleted error: ${err?.message ?: "?"}", level = StatusLevel.WARN)
            }
            
            
        } catch (t: Throwable) {
            appendStatus("deleted failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
        }
    }
}


fun MessengerStore.rotateTorForChat(chatId: String) {
    if (chatId.isEmpty()) return
    val peerId = peerIdOrWarn(chatId, "rotate") ?: return
    scope.launch {
        val c = ipc ?: run {
            appendStatus("rotate: ipc not connected", level = StatusLevel.WARN)
            return@launch
        }
        try {
            val reply = c.request(
                type = FrameType.RotateBegin,
                payload = RotateBeginRequest(peerId).toJson(),
            )
            if (reply.type == FrameType.Error) {
                val err = reply.payload?.let(ErrorPayload::fromJson)
                appendStatus("rotate error: ${err?.message ?: "?"}", level = StatusLevel.WARN)
                return@launch
            }
            val resp = reply.payload?.let(RotateBegunResponse::fromJson) ?: return@launch
            appendStatus("rotation begun ${shortChat(resp.rotationId)} (peer ${shortChat(peerId)})")
        } catch (t: Throwable) {
            appendStatus("rotate failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
        }
    }
}
