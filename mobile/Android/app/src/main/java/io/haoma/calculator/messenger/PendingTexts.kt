package io.haoma.calculator.messenger

import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale


const val SYSTEM_LINE_NO_SESSION = "Waiting for a secure session. Your messages go out when it is up."
const val SYSTEM_LINE_NO_ANSWER = "Waiting for the contact to answer."
private const val SYSTEM_LINE_STARTED = "Chat started "


fun chatSystemLine(chat: ChatEntry?): String? = when {
    chat == null -> null
    chat.waitingForSession -> SYSTEM_LINE_NO_SESSION
    chat.waitingForContact -> SYSTEM_LINE_NO_ANSWER
    chat.chatStartedAt > 0L ->
        SYSTEM_LINE_STARTED + SimpleDateFormat("yyyy-MM-dd HH:mm:ss", Locale.US)
            .format(Date(chat.chatStartedAt * 1000L))
    else -> null
}


data class PendingText(
    val localId: Long,
    val chatId: String,
    val text: String,
    val at: Long,
    val msgId: String = "",
    val failedReason: String = "",
) {
    val isFailed: Boolean get() = failedReason.isNotEmpty()
}


internal fun addPendingText(map: Map<String, List<PendingText>>, p: PendingText): Map<String, List<PendingText>> =
    map + (p.chatId to (map[p.chatId].orEmpty() + p))


internal fun markPendingFailed(
    map: Map<String, List<PendingText>>,
    chatId: String,
    localId: Long,
    reason: String,
): Map<String, List<PendingText>> {
    val list = map[chatId] ?: return map
    if (list.none { it.localId == localId }) return map
    val why = reason.ifEmpty { "not sent" }
    return map + (chatId to list.map { if (it.localId == localId) it.copy(failedReason = why) else it })
}


internal fun markPendingSent(
    map: Map<String, List<PendingText>>,
    chatId: String,
    localId: Long,
    msgId: String,
    rowPresent: Boolean,
): Map<String, List<PendingText>> {
    val list = map[chatId] ?: return map
    if (list.none { it.localId == localId }) return map
    val next = if (rowPresent || msgId.isEmpty()) {
        list.filter { it.localId != localId }
    } else {
        list.map { if (it.localId == localId) it.copy(msgId = msgId) else it }
    }
    return if (next.isEmpty()) map - chatId else map + (chatId to next)
}


internal fun dropPendingForRow(
    map: Map<String, List<PendingText>>,
    chatId: String,
    msgId: String,
): Map<String, List<PendingText>> {
    if (msgId.isEmpty()) return map
    val list = map[chatId] ?: return map
    if (list.none { it.msgId == msgId }) return map
    val next = list.filter { it.msgId != msgId }
    return if (next.isEmpty()) map - chatId else map + (chatId to next)
}


internal fun dropSentPendingTexts(map: Map<String, List<PendingText>>, chatId: String): Map<String, List<PendingText>> {
    val list = map[chatId] ?: return map
    val next = list.filter { it.msgId.isEmpty() }
    return when {
        next.size == list.size -> map
        next.isEmpty() -> map - chatId
        else -> map + (chatId to next)
    }
}


internal fun PendingText.asTimelineEvent(): TimelineEvent = TimelineEvent(
    recvSeq = 0L,
    chatId = chatId,
    direction = EventDirection.OUT,
    kind = EventKind.TEXT,
    displayTs = at,
    senderTs = at,
    recvTs = at,
    senderSeq = 0L,
    senderPeerId = "",
    envelopeId = "",
    msgId = "",
    decryptStatus = "",
    failReason = "",
    body = org.json.JSONObject().put("text", text),
    deliveryState = if (isFailed) DeliveryState.FAILED else DeliveryState.PENDING,
    expireSeconds = 0,
    readAt = 0L,
    editedAt = 0L,
    deletedAt = 0L,
)
