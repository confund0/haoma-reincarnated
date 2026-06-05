package io.haoma.calculator.messenger

import io.haoma.calculator.core.ipc.FrameType
import io.haoma.calculator.log.Logger
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch


data class ChatSearchState(
    val chatId: String,
    val query: String,
    val matches: List<ChatSearchMatch> = emptyList(),
    val cursorIdx: Int = 0,
    val truncated: Boolean = false,
    val loading: Boolean = false,
)


fun MessengerStore.openChatSearch(chatId: String) {
    if (chatId.isEmpty()) return
    _chatSearch.value = ChatSearchState(chatId = chatId, query = "")
}


fun MessengerStore.setChatSearchQuery(text: String) {
    _chatSearch.update { it?.copy(query = text) }
}


fun MessengerStore.runChatSearch(chatId: String, query: String) {
    val trimmed = query.trim()
    val active = _chatSearch.value
    if (active == null || active.chatId != chatId) return
    if (trimmed.isEmpty()) {
        
        
        _chatSearch.update { it?.copy(query = "", matches = emptyList(), cursorIdx = 0, truncated = false, loading = false) }
        return
    }
    _chatSearch.update { it?.copy(query = trimmed, loading = true) }

    scope.launch {
        val c = ipc ?: run {
            appendStatus("search: ipc not connected", level = StatusLevel.WARN)
            _chatSearch.update { it?.copy(loading = false) }
            return@launch
        }
        try {
            val reply = c.request(
                type = FrameType.ChatSearch,
                payload = ChatSearchRequest(chatId, trimmed).toJson(),
            )
            if (reply.type == FrameType.Error) {
                val err = reply.payload?.let(ErrorPayload::fromJson)
                appendStatus("search error: ${err?.message ?: "?"}", level = StatusLevel.WARN)
                _chatSearch.update { it?.copy(loading = false) }
                return@launch
            }
            val resp = reply.payload?.let(ChatSearchResponse::fromJson) ?: run {
                _chatSearch.update { it?.copy(loading = false) }
                return@launch
            }
            
            
            val current = _chatSearch.value
            if (current == null || current.chatId != chatId || current.query != trimmed) return@launch

            _chatSearch.value = current.copy(
                matches = resp.matches,
                cursorIdx = 0,
                truncated = resp.truncated,
                loading = false,
            )
            Logger.i(
                "search",
                "chat=${shortChat(chatId)} q=${trimmed.take(16)} hits=${resp.matches.size} truncated=${resp.truncated}",
            )
            if (resp.matches.isNotEmpty()) {
                
                
                scrollToMsgId(chatId, resp.matches.first().msgId)
            }
        } catch (t: Throwable) {
            appendStatus("search failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
            _chatSearch.update { it?.copy(loading = false) }
        }
    }
}


fun MessengerStore.stepChatSearch(delta: Int) {
    val cur = _chatSearch.value ?: return
    val n = cur.matches.size
    if (n == 0) return
    val nextIdx = ((cur.cursorIdx + delta) % n + n) % n
    if (nextIdx == cur.cursorIdx) return
    _chatSearch.value = cur.copy(cursorIdx = nextIdx)
    scrollToMsgId(cur.chatId, cur.matches[nextIdx].msgId)
}


fun MessengerStore.closeChatSearch() {
    val cur = _chatSearch.value ?: return
    _chatSearch.value = null
    
    
    _pendingAnchors.update { if (cur.chatId in it) it - cur.chatId else it }
}
