package io.haoma.calculator.messenger

import io.haoma.calculator.core.ipc.FrameType
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch


fun MessengerStore.selectTab(tab: Tab) {
    _backStack.update { _ ->
        
        
        listOf(Screen.Tabbed(tab))
    }
}

fun MessengerStore.openChatDetail(chatId: String) {
    _backStack.update { it + Screen.ChatDetail(chatId) }
}

fun MessengerStore.openContactDetail(peerId: String) {
    _backStack.update { it + Screen.ContactDetail(peerId) }
}

fun MessengerStore.openChatSettings(chatId: String) {
    _backStack.update { it + Screen.ChatSettings(chatId) }
}

fun MessengerStore.openSettingsSection(domain: String) {
    _backStack.update { it + Screen.SettingsSection(domain) }
}

fun MessengerStore.openAccept(type: PairType) {
    _backStack.update { it + Screen.Accept(type) }
}


fun MessengerStore.openChatForPeer(peerId: String) {
    val cached = _chats.value.firstOrNull { it.peerId == peerId }
    if (cached != null) {
        openChatDetail(cached.chatId)
        return
    }
    scope.launch {
        val c = ipc ?: run {
            appendStatus("open chat: ipc not connected", level = StatusLevel.WARN)
            return@launch
        }
        try {
            val reply = c.request(
                type = FrameType.EnsureChat,
                payload = EnsureChatRequest(peerId).toJson(),
            )
            if (reply.type == FrameType.Error) {
                val err = reply.payload?.let(ErrorPayload::fromJson)
                appendStatus(
                    "ensure_chat error: ${err?.message ?: "?"}",
                    level = StatusLevel.WARN,
                )
                return@launch
            }
            val payload = reply.payload ?: run {
                appendStatus("ensure_chat: empty reply", level = StatusLevel.WARN)
                return@launch
            }
            val resp = ChatEnsuredResponse.fromJson(payload)
            if (resp.chat.chatId.isEmpty()) {
                appendStatus("ensure_chat: blank chat_id", level = StatusLevel.WARN)
                return@launch
            }
            
            
            upsertChat(resp.chat)
            openChatDetail(resp.chat.chatId)
        } catch (t: Throwable) {
            appendStatus("ensure_chat failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
        }
    }
}


fun MessengerStore.popBack(): Boolean {
    val current = _backStack.value
    if (current.size <= 1) return false
    _backStack.value = current.dropLast(1)
    return true
}
