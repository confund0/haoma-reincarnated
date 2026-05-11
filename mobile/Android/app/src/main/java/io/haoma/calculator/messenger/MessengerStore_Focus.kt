package io.haoma.calculator.messenger

import io.haoma.calculator.core.ipc.Frame
import io.haoma.calculator.core.ipc.FrameType
import io.haoma.calculator.log.Logger
import org.json.JSONObject


fun MessengerStore.markRead(chatId: String) {
    if (chatId.isEmpty()) return
    val c = ipc ?: return
    val payload = JSONObject().apply { put("chat_id", chatId) }
    c.send(Frame(type = FrameType.MarkRead, payload = payload))
    Logger.d("messenger", "mark_read emit chat=${shortChat(chatId)}")
}


fun MessengerStore.setClientFocus(chatId: String, scrollPosition: Int) {
    currentFocusChatId = chatId
    emitClientFocus(chatId, scrollPosition)
}


fun MessengerStore.pauseFocusOnBackground() {
    if (currentFocusChatId.isEmpty()) return
    emitClientFocus("", 0)
}

internal fun MessengerStore.emitClientFocus(chatId: String, scrollPosition: Int) {
    val c = ipc ?: return
    val payload = JSONObject().apply {
        put("chat_id", chatId)
        put("scroll_position", scrollPosition)
    }
    c.send(Frame(type = FrameType.ClientFocus, payload = payload))
    Logger.d(
        "messenger",
        "client_focus emit chat=${if (chatId.isEmpty()) "(none)" else shortChat(chatId)} pos=$scrollPosition",
    )
}


fun MessengerStore.syncLockState(softLocked: Boolean) {
    if (lastSoftLocked == softLocked) return
    lastSoftLocked = softLocked
    emitLockState(softLocked)
    
    
    clearImageCaches()
}

internal fun MessengerStore.emitLockState(softLocked: Boolean) {
    val c = ipc ?: return
    c.send(
        Frame(
            type = FrameType.ClientLockState,
            payload = ClientLockStateRequest(softLocked = softLocked).toJson(),
        ),
    )
    Logger.d("messenger", "client_lock_state emit soft_locked=$softLocked")
}


fun MessengerStore.refireFocusOnResume() {
    val chatId = currentFocusChatId
    if (chatId.isEmpty()) return
    markRead(chatId)
    emitClientFocus(chatId, 0)
}
