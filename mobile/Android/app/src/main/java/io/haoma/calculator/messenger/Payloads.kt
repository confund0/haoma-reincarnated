package io.haoma.calculator.messenger

import org.json.JSONArray
import org.json.JSONObject


internal fun JSONObject.optStringOrEmpty(key: String): String = optString(key, "")

internal fun JSONObject.optStringList(key: String): List<String> {
    val arr = optJSONArray(key) ?: return emptyList()
    val out = ArrayList<String>(arr.length())
    for (i in 0 until arr.length()) out += arr.optString(i, "")
    return out
}

internal fun stringListJson(values: List<String>): JSONArray {
    val arr = JSONArray()
    values.forEach { arr.put(it) }
    return arr
}


data class WelcomePayload(
    val daemonVersion: String,
    val protocolVersion: Int,
    val selfNick: String,
    val selfNickIsDefault: Boolean,
) {
    companion object {
        fun fromJson(o: JSONObject): WelcomePayload = WelcomePayload(
            daemonVersion = o.optStringOrEmpty("daemon_version"),
            protocolVersion = o.optInt("protocol_version", 0),
            selfNick = o.optStringOrEmpty("self_nick"),
            selfNickIsDefault = o.optBoolean("self_nick_is_default", false),
        )
    }
}

data class ErrorPayload(
    val code: String,
    val message: String,
) {
    companion object {
        fun fromJson(o: JSONObject): ErrorPayload = ErrorPayload(
            code = o.optStringOrEmpty("code"),
            message = o.optStringOrEmpty("message"),
        )
    }
}

data class SubscribeRequest(
    val topics: List<String> = emptyList(),
) {
    fun toJson(): JSONObject {
        val o = JSONObject()
        if (topics.isNotEmpty()) o.put("topics", stringListJson(topics))
        return o
    }
}

data class SubscribedResponse(
    val topics: List<String>,
) {
    companion object {
        fun fromJson(o: JSONObject): SubscribedResponse = SubscribedResponse(
            topics = o.optStringList("topics"),
        )
    }
}


data class NickPayload(
    val nick: String,
    val isDefault: Boolean,
) {
    companion object {
        fun fromJson(o: JSONObject): NickPayload = NickPayload(
            nick = o.optStringOrEmpty("nick"),
            isDefault = o.optBoolean("is_default", false),
        )
    }
}


data class TorHealth(
    val bootstrap: Int,
    val ready: Boolean,
    val unreachable: Boolean,
) {
    companion object {
        val ZERO = TorHealth(bootstrap = 0, ready = false, unreachable = false)
        fun fromJson(o: JSONObject): TorHealth = TorHealth(
            bootstrap = o.optInt("bootstrap", 0),
            ready = o.optBoolean("ready", false),
            unreachable = o.optBoolean("unreachable", false),
        )
    }
}

data class TorSlot(
    val slot: Int,
    val serviceId: String,
    val url: String,
) {
    companion object {
        fun fromJson(o: JSONObject): TorSlot = TorSlot(
            slot = o.optInt("slot", 0),
            serviceId = o.optStringOrEmpty("service_id"),
            url = o.optStringOrEmpty("url"),
        )
    }
}

data class TorInfoResponse(
    val slots: List<TorSlot>,
    val health: TorHealth,
) {
    companion object {
        fun fromJson(o: JSONObject): TorInfoResponse {
            val slotArr = o.optJSONArray("slots")
            val slots = if (slotArr == null) emptyList() else buildList {
                for (i in 0 until slotArr.length()) {
                    slotArr.optJSONObject(i)?.let { add(TorSlot.fromJson(it)) }
                }
            }
            val health = o.optJSONObject("health")?.let { TorHealth.fromJson(it) } ?: TorHealth.ZERO
            return TorInfoResponse(slots = slots, health = health)
        }
    }
}

data class BackendStatusPayload(
    val backendReachable: Boolean,
    val tor: TorHealth,
    val onionCount: Int,
) {
    companion object {
        fun fromJson(o: JSONObject): BackendStatusPayload = BackendStatusPayload(
            backendReachable = o.optBoolean("backend_reachable", false),
            tor = o.optJSONObject("tor")?.let { TorHealth.fromJson(it) } ?: TorHealth.ZERO,
            onionCount = o.optInt("onion_count", 0),
        )
    }
}


data class SystemInfoComponent(
    val version: String,
    val startedAt: String,
) {
    companion object {
        val EMPTY = SystemInfoComponent(version = "", startedAt = "")
        fun fromJson(o: JSONObject): SystemInfoComponent = SystemInfoComponent(
            version = o.optStringOrEmpty("version"),
            startedAt = o.optStringOrEmpty("started_at"),
        )
    }
}


data class SystemInfoResponse(
    val haoma: SystemInfoComponent,
    val haomad: SystemInfoComponent,
) {
    companion object {
        fun fromJson(o: JSONObject): SystemInfoResponse = SystemInfoResponse(
            haoma = o.optJSONObject("haoma")?.let { SystemInfoComponent.fromJson(it) } ?: SystemInfoComponent.EMPTY,
            haomad = o.optJSONObject("haomad")?.let { SystemInfoComponent.fromJson(it) } ?: SystemInfoComponent.EMPTY,
        )
    }
}


data class PeerEntry(
    val id: String,
    val chatId: String,
    val nick: String,
    val alias: String,
    val label: String,
    val lastActiveAt: Long,
    val lastPassiveAt: Long,
    val retiredAt: Long,
    val accepting: Boolean,
    val chatty: String,
    val effective: String,
) {
    companion object {
        fun fromJson(o: JSONObject): PeerEntry = PeerEntry(
            id = o.optStringOrEmpty("id"),
            chatId = o.optStringOrEmpty("chat_id"),
            nick = o.optStringOrEmpty("nick"),
            alias = o.optStringOrEmpty("alias"),
            label = o.optStringOrEmpty("label"),
            lastActiveAt = o.optLong("last_active_at", 0L),
            lastPassiveAt = o.optLong("last_passive_at", 0L),
            retiredAt = o.optLong("retired_at", 0L),
            accepting = o.optBoolean("accepting", false),
            chatty = o.optStringOrEmpty("chatty"),
            effective = o.optStringOrEmpty("effective"),
        )
    }
}

enum class ChatKind(val wire: String) {
    Direct("direct"),
    Group("group"),
    Unknown("");

    companion object {
        fun fromWire(s: String): ChatKind = when (s) {
            Direct.wire -> Direct
            Group.wire -> Group
            else -> Unknown
        }
    }
}

data class ChatEntry(
    val chatId: String,
    val kind: ChatKind,
    val peerId: String,
    val groupName: String,
    val groupAlias: String,
    val label: String,
    val retentionTtl: Long,
    val disableReadReceipts: Boolean,
    val notificationsMuted: Boolean,
    val members: List<String>,
    val createdAt: Long,
    val lastActivityAt: Long,
    val unreadCount: Long,
    val accepting: Boolean,
    val chatty: String,
    val effective: String,
) {
    companion object {
        fun fromJson(o: JSONObject): ChatEntry = ChatEntry(
            chatId = o.optStringOrEmpty("chat_id"),
            kind = ChatKind.fromWire(o.optStringOrEmpty("kind")),
            peerId = o.optStringOrEmpty("peer_id"),
            groupName = o.optStringOrEmpty("group_name"),
            groupAlias = o.optStringOrEmpty("group_alias"),
            label = o.optStringOrEmpty("label"),
            retentionTtl = o.optLong("retention_ttl", 0L),
            disableReadReceipts = o.optBoolean("disable_read_receipts", false),
            notificationsMuted = o.optBoolean("notifications_muted", false),
            members = o.optStringList("members"),
            createdAt = o.optLong("created_at", 0L),
            lastActivityAt = o.optLong("last_activity_at", 0L),
            unreadCount = o.optLong("unread_count", 0L),
            accepting = o.optBoolean("accepting", false),
            chatty = o.optStringOrEmpty("chatty"),
            effective = o.optStringOrEmpty("effective"),
        )
    }
}

internal fun JSONObject.peerArray(key: String): List<PeerEntry> {
    val arr = optJSONArray(key) ?: return emptyList()
    return buildList {
        for (i in 0 until arr.length()) {
            arr.optJSONObject(i)?.let { add(PeerEntry.fromJson(it)) }
        }
    }
}

internal fun JSONObject.chatArray(key: String): List<ChatEntry> {
    val arr = optJSONArray(key) ?: return emptyList()
    return buildList {
        for (i in 0 until arr.length()) {
            arr.optJSONObject(i)?.let { add(ChatEntry.fromJson(it)) }
        }
    }
}


data class PeerUpdatedPayload(val peer: PeerEntry) {
    companion object {
        fun fromJson(o: JSONObject): PeerUpdatedPayload =
            PeerUpdatedPayload(o.optJSONObject("peer")?.let(PeerEntry::fromJson)
                ?: PeerEntry.fromJson(JSONObject()))
    }
}

data class ChatUpdatedPayload(val chat: ChatEntry) {
    companion object {
        fun fromJson(o: JSONObject): ChatUpdatedPayload =
            ChatUpdatedPayload(o.optJSONObject("chat")?.let(ChatEntry::fromJson)
                ?: ChatEntry.fromJson(JSONObject()))
    }
}

data class PeerDeletedPayload(val peerId: String) {
    companion object {
        fun fromJson(o: JSONObject): PeerDeletedPayload =
            PeerDeletedPayload(o.optStringOrEmpty("peer_id"))
    }
}

data class ChatClearedPayload(val chatId: String, val deletedCount: Int) {
    companion object {
        fun fromJson(o: JSONObject): ChatClearedPayload = ChatClearedPayload(
            chatId = o.optStringOrEmpty("chat_id"),
            deletedCount = o.optInt("deleted_count", 0),
        )
    }
}

data class ChatDeletedPayload(val chatId: String, val deletedCount: Int) {
    companion object {
        fun fromJson(o: JSONObject): ChatDeletedPayload = ChatDeletedPayload(
            chatId = o.optStringOrEmpty("chat_id"),
            deletedCount = o.optInt("deleted_count", 0),
        )
    }
}

data class ChatActivityChangedPayload(val chatId: String, val lastActivityAt: Long) {
    companion object {
        fun fromJson(o: JSONObject): ChatActivityChangedPayload = ChatActivityChangedPayload(
            chatId = o.optStringOrEmpty("chat_id"),
            lastActivityAt = o.optLong("last_activity_at", 0L),
        )
    }
}

data class ChatUnreadChangedPayload(val chatId: String, val unreadCount: Long) {
    companion object {
        fun fromJson(o: JSONObject): ChatUnreadChangedPayload = ChatUnreadChangedPayload(
            chatId = o.optStringOrEmpty("chat_id"),
            unreadCount = o.optLong("unread_count", 0L),
        )
    }
}

data class PeerLastSeenChangedPayload(
    val peerId: String,
    val lastActiveAt: Long,
    val lastPassiveAt: Long,
) {
    companion object {
        fun fromJson(o: JSONObject): PeerLastSeenChangedPayload = PeerLastSeenChangedPayload(
            peerId = o.optStringOrEmpty("peer_id"),
            lastActiveAt = o.optLong("last_active_at", 0L),
            lastPassiveAt = o.optLong("last_passive_at", 0L),
        )
    }
}

data class PresenceChangedPayload(
    val peerId: String,
    val accepting: Boolean,
    val chatty: String,
    val effective: String,
) {
    companion object {
        fun fromJson(o: JSONObject): PresenceChangedPayload = PresenceChangedPayload(
            peerId = o.optStringOrEmpty("peer_id"),
            accepting = o.optBoolean("accepting", false),
            chatty = o.optStringOrEmpty("chatty"),
            effective = o.optStringOrEmpty("effective"),
        )
    }
}

data class PeerPairedPush(val peerId: String, val source: String) {
    companion object {
        fun fromJson(o: JSONObject): PeerPairedPush = PeerPairedPush(
            peerId = o.optStringOrEmpty("peer_id"),
            source = o.optStringOrEmpty("source"),
        )
    }
}


data class EnsureChatRequest(val peerId: String) {
    fun toJson(): JSONObject = JSONObject().apply { put("peer_id", peerId) }
}

data class ChatEnsuredResponse(val peer: PeerEntry, val chat: ChatEntry) {
    companion object {
        fun fromJson(o: JSONObject): ChatEnsuredResponse = ChatEnsuredResponse(
            peer = o.optJSONObject("peer")?.let(PeerEntry::fromJson)
                ?: PeerEntry.fromJson(JSONObject()),
            chat = o.optJSONObject("chat")?.let(ChatEntry::fromJson)
                ?: ChatEntry.fromJson(JSONObject()),
        )
    }
}


data class SetNickRequest(val nick: String) {
    fun toJson(): JSONObject = JSONObject().apply { put("nick", nick) }
}

data class SetTorPasswordRequest(val password: String) {
    fun toJson(): JSONObject = JSONObject().apply { put("password", password) }
}


data class SetAliasRequest(val peerId: String, val alias: String) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("peer_id", peerId)
        put("alias", alias)
    }
}

data class AliasUpdatedResponse(val peer: PeerEntry) {
    companion object {
        fun fromJson(o: JSONObject): AliasUpdatedResponse = AliasUpdatedResponse(
            peer = o.optJSONObject("peer")?.let(PeerEntry::fromJson)
                ?: PeerEntry.fromJson(JSONObject()),
        )
    }
}


enum class PeerAction(val wire: String) {
    Retire("retire"),
    Delete("delete"),
}

data class PeerActionRequest(val peerId: String, val action: PeerAction) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("peer_id", peerId)
        put("action", action.wire)
    }
}

data class PeerActionAppliedResponse(
    val peer: PeerEntry,
    val action: String,
    val deletedCount: Int,
) {
    companion object {
        fun fromJson(o: JSONObject): PeerActionAppliedResponse = PeerActionAppliedResponse(
            peer = o.optJSONObject("peer")?.let(PeerEntry::fromJson)
                ?: PeerEntry.fromJson(JSONObject()),
            action = o.optStringOrEmpty("action"),
            deletedCount = o.optInt("deleted_count", 0),
        )
    }
}

data class GetPeerFingerprintRequest(val peerId: String) {
    fun toJson(): JSONObject = JSONObject().apply { put("peer_id", peerId) }
}

data class PeerFingerprintPayload(val peerId: String, val fingerprint: String) {
    companion object {
        fun fromJson(o: JSONObject): PeerFingerprintPayload = PeerFingerprintPayload(
            peerId = o.optStringOrEmpty("peer_id"),
            fingerprint = o.optStringOrEmpty("fingerprint"),
        )
    }
}


data class PairOnionInviteRequest(val nick: String = "", val alias: String = "") {
    fun toJson(): JSONObject = JSONObject().apply {
        if (nick.isNotEmpty()) put("nick", nick)
        if (alias.isNotEmpty()) put("alias", alias)
    }
}

data class PairOnionStartedResponse(
    val handleId: String,
    val words: List<String>,
    val expiresAt: Long,
) {
    companion object {
        fun fromJson(o: JSONObject): PairOnionStartedResponse = PairOnionStartedResponse(
            handleId = o.optStringOrEmpty("handle_id"),
            words = o.optStringList("words"),
            expiresAt = o.optLong("expires_at", 0L),
        )
    }
}

data class PairOnionProbePush(
    val handleId: String,
    val attempt: Int,
    val ready: Boolean,
    val error: String,
    val at: Long,
) {
    companion object {
        fun fromJson(o: JSONObject): PairOnionProbePush = PairOnionProbePush(
            handleId = o.optStringOrEmpty("handle_id"),
            attempt = o.optInt("attempt", 0),
            ready = o.optBoolean("ready", false),
            error = o.optStringOrEmpty("error"),
            at = o.optLong("at", 0L),
        )
    }
}

data class PairOnionCompletedPush(
    val handleId: String,
    val peerId: String,
    val nick: String,
    val identityFingerprint: String,
) {
    companion object {
        fun fromJson(o: JSONObject): PairOnionCompletedPush = PairOnionCompletedPush(
            handleId = o.optStringOrEmpty("handle_id"),
            peerId = o.optStringOrEmpty("peer_id"),
            nick = o.optStringOrEmpty("nick"),
            identityFingerprint = o.optStringOrEmpty("identity_fingerprint"),
        )
    }
}

data class PairOnionFailedPush(
    val handleId: String,
    val reason: String,
    val detail: String,
) {
    companion object {
        fun fromJson(o: JSONObject): PairOnionFailedPush = PairOnionFailedPush(
            handleId = o.optStringOrEmpty("handle_id"),
            reason = o.optStringOrEmpty("reason"),
            detail = o.optStringOrEmpty("detail"),
        )
    }
}

data class PairOnionAcceptRequest(
    val words: List<String>,
    val nick: String = "",
    val alias: String = "",
) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("words", stringListJson(words))
        if (nick.isNotEmpty()) put("nick", nick)
        if (alias.isNotEmpty()) put("alias", alias)
    }
}

data class PairOnionAcceptedResponse(
    val peerId: String,
    val nick: String,
    val identityFingerprint: String,
) {
    companion object {
        fun fromJson(o: JSONObject): PairOnionAcceptedResponse = PairOnionAcceptedResponse(
            peerId = o.optStringOrEmpty("peer_id"),
            nick = o.optStringOrEmpty("nick"),
            identityFingerprint = o.optStringOrEmpty("identity_fingerprint"),
        )
    }
}

data class PairOnionCancelRequest(val handleId: String) {
    fun toJson(): JSONObject = JSONObject().apply { put("handle_id", handleId) }
}


object EventDirection {
    const val IN = "in"
    const val OUT = "out"
}


object EventKind {
    const val TEXT = "text"
    const val TIMER_CHANGE = "timer_change"
    const val REACTION = "reaction"
    const val FILE = "file"
}


object DeliveryState {
    const val ENQUEUED = "enqueued"
    const val SENT = "sent"
    const val DELIVERED = "delivered"
    const val READ = "read"
    const val FAILED = "failed"

    
    fun rank(state: String): Int = when (state) {
        ENQUEUED -> 0
        FAILED -> 0    
        SENT -> 1
        DELIVERED -> 2
        READ -> 3
        else -> 0
    }
}


data class TimelineEvent(
    val recvSeq: Long,
    val chatId: String,
    val direction: String,
    val kind: String,
    val displayTs: Long,
    val senderTs: Long,
    val recvTs: Long,
    val senderSeq: Long,
    val senderPeerId: String,
    val envelopeId: String,
    val msgId: String,
    val decryptStatus: String,
    val body: JSONObject?,
    val deliveryState: String,
    val expireSeconds: Int,
    val readAt: Long,
    val editedAt: Long,
    val deletedAt: Long,
) {
    
    fun bodyTextOrEmpty(): String =
        body?.optStringOrEmpty("text").orEmpty()

    val isOutbound: Boolean get() = direction == EventDirection.OUT
    val isInbound: Boolean get() = direction == EventDirection.IN
    val isTombstoned: Boolean get() = deletedAt > 0L
    val isEdited: Boolean get() = editedAt > 0L

    
    fun isReadyImage(): Boolean {
        if (kind != EventKind.FILE || isTombstoned) return false
        val b = FileEventBody.fromJson(body)
        return b.isImage && b.state == FileState.READY
    }

    companion object {
        fun fromJson(o: JSONObject): TimelineEvent = TimelineEvent(
            recvSeq = o.optLong("recv_seq", 0L),
            chatId = o.optStringOrEmpty("chat_id"),
            direction = o.optStringOrEmpty("direction"),
            kind = o.optStringOrEmpty("kind"),
            displayTs = o.optLong("display_ts", 0L),
            senderTs = o.optLong("sender_ts", 0L),
            recvTs = o.optLong("recv_ts", 0L),
            senderSeq = o.optLong("sender_seq", 0L),
            senderPeerId = o.optStringOrEmpty("sender_peer_id"),
            envelopeId = o.optStringOrEmpty("envelope_id"),
            msgId = o.optStringOrEmpty("msg_id"),
            decryptStatus = o.optStringOrEmpty("decrypt_status"),
            body = o.optJSONObject("body"),
            deliveryState = o.optStringOrEmpty("delivery_state"),
            expireSeconds = o.optInt("expire_seconds", 0),
            readAt = o.optLong("read_at", 0L),
            editedAt = o.optLong("edited_at", 0L),
            deletedAt = o.optLong("deleted_at", 0L),
        )
    }
}

data class ListTimelineRequest(
    val peerId: String,
    val limit: Int = DEFAULT_LIMIT,
    val beforeDisplayTs: Long = 0L,
) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("peer_id", peerId)
        if (limit > 0) put("limit", limit)
        if (beforeDisplayTs > 0L) put("before_display_ts", beforeDisplayTs)
    }

    companion object {
        const val DEFAULT_LIMIT = 50
    }
}

data class TimelinePageResponse(
    val peerId: String,
    val events: List<TimelineEvent>,
    val hasMore: Boolean,
) {
    companion object {
        fun fromJson(o: JSONObject): TimelinePageResponse {
            val arr = o.optJSONArray("events")
            val events = if (arr == null) emptyList() else buildList {
                for (i in 0 until arr.length()) {
                    arr.optJSONObject(i)?.let { add(TimelineEvent.fromJson(it)) }
                }
            }
            return TimelinePageResponse(
                peerId = o.optStringOrEmpty("peer_id"),
                events = events,
                hasMore = o.optBoolean("has_more", false),
            )
        }
    }
}


data class TimelineEventPayload(val event: TimelineEvent) {
    companion object {
        fun fromJson(o: JSONObject): TimelineEventPayload {
            val ev = o.optJSONObject("event")
                ?: throw IllegalArgumentException("timeline-event push missing 'event'")
            return TimelineEventPayload(TimelineEvent.fromJson(ev))
        }
    }
}

data class DeliveryStatusPayload(
    val envelopeId: String,
    val state: String,
    val at: Long,
    val attempts: Int,
    val lastError: String,
) {
    companion object {
        fun fromJson(o: JSONObject): DeliveryStatusPayload = DeliveryStatusPayload(
            envelopeId = o.optStringOrEmpty("envelope_id"),
            state = o.optStringOrEmpty("state"),
            at = o.optLong("at", 0L),
            attempts = o.optInt("attempts", 0),
            lastError = o.optStringOrEmpty("last_error"),
        )
    }
}

data class TimelineEventDeletedPayload(
    val chatId: String,
    val recvSeq: Long,
) {
    companion object {
        fun fromJson(o: JSONObject): TimelineEventDeletedPayload = TimelineEventDeletedPayload(
            chatId = o.optStringOrEmpty("chat_id"),
            recvSeq = o.optLong("recv_seq", 0L),
        )
    }
}

data class SendTextRequest(val peerId: String, val text: String) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("peer_id", peerId)
        put("text", text)
    }
}

data class SendTextResponse(
    val envelopeId: String,
    val msgId: String,
    val senderSeq: Long,
) {
    companion object {
        fun fromJson(o: JSONObject): SendTextResponse = SendTextResponse(
            envelopeId = o.optStringOrEmpty("envelope_id"),
            msgId = o.optStringOrEmpty("msg_id"),
            senderSeq = o.optLong("sender_seq", 0L),
        )
    }
}


data class SendEditRequest(val peerId: String, val targetMsgId: String, val text: String) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("peer_id", peerId)
        put("target_msg_id", targetMsgId)
        put("text", text)
    }
}


data class SendEditResponse(
    val envelopeId: String,
    val msgId: String,
    val senderSeq: Long,
    val targetMsgId: String,
) {
    companion object {
        fun fromJson(o: JSONObject): SendEditResponse = SendEditResponse(
            envelopeId = o.optStringOrEmpty("envelope_id"),
            msgId = o.optStringOrEmpty("msg_id"),
            senderSeq = o.optLong("sender_seq", 0L),
            targetMsgId = o.optStringOrEmpty("target_msg_id"),
        )
    }
}


data class SendDeleteRequest(val peerId: String, val targetMsgId: String) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("peer_id", peerId)
        put("target_msg_id", targetMsgId)
    }
}


data class SendDeleteResponse(
    val envelopeId: String,
    val msgId: String,
    val senderSeq: Long,
    val targetMsgId: String,
) {
    companion object {
        fun fromJson(o: JSONObject): SendDeleteResponse = SendDeleteResponse(
            envelopeId = o.optStringOrEmpty("envelope_id"),
            msgId = o.optStringOrEmpty("msg_id"),
            senderSeq = o.optLong("sender_seq", 0L),
            targetMsgId = o.optStringOrEmpty("target_msg_id"),
        )
    }
}


data class SendReactionRequest(val peerId: String, val targetMsgId: String, val emoji: String) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("peer_id", peerId)
        put("target_msg_id", targetMsgId)
        put("emoji", emoji)
    }
}


data class SendReactionResponse(
    val envelopeId: String,
    val msgId: String,
    val senderSeq: Long,
    val targetMsgId: String,
) {
    companion object {
        fun fromJson(o: JSONObject): SendReactionResponse = SendReactionResponse(
            envelopeId = o.optStringOrEmpty("envelope_id"),
            msgId = o.optStringOrEmpty("msg_id"),
            senderSeq = o.optLong("sender_seq", 0L),
            targetMsgId = o.optStringOrEmpty("target_msg_id"),
        )
    }
}

data class MarkReadRequest(val chatId: String) {
    fun toJson(): JSONObject = JSONObject().apply { put("chat_id", chatId) }
}

data class MarkedReadResponse(val chatId: String, val markedCount: Int) {
    companion object {
        fun fromJson(o: JSONObject): MarkedReadResponse = MarkedReadResponse(
            chatId = o.optStringOrEmpty("chat_id"),
            markedCount = o.optInt("marked_count", 0),
        )
    }
}

data class ClientFocusRequest(val chatId: String, val scrollPosition: Int = 0) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("chat_id", chatId)
        put("scroll_position", scrollPosition)
    }
}


data class GetChatSettingsRequest(val chatId: String) {
    fun toJson(): JSONObject = JSONObject().apply { put("chat_id", chatId) }
}


data class ChatSettingsPayload(
    val chatId: String,
    val retentionTtl: Int,
    val disableReadReceipts: Boolean,
    val notificationsMuted: Boolean,
) {
    companion object {
        fun fromJson(o: JSONObject): ChatSettingsPayload = ChatSettingsPayload(
            chatId = o.optStringOrEmpty("chat_id"),
            retentionTtl = o.optInt("retention_ttl", 0),
            disableReadReceipts = o.optBoolean("disable_read_receipts", false),
            notificationsMuted = o.optBoolean("notifications_muted", false),
        )
    }
}


data class SetChatSettingsRequest(
    val chatId: String,
    val retentionTtl: Int,
    val disableReadReceipts: Boolean,
    val notificationsMuted: Boolean,
) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("chat_id", chatId)
        put("retention_ttl", retentionTtl)
        put("disable_read_receipts", disableReadReceipts)
        put("notifications_muted", notificationsMuted)
    }
}


enum class ChatAction(val wire: String) {
    Clear("clear"),
    Delete("delete"),
}

data class ChatActionRequest(val chatId: String, val action: ChatAction) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("chat_id", chatId)
        put("action", action.wire)
    }
}

data class ChatActionAppliedResponse(
    val chat: ChatEntry,
    val action: String,
    val deletedCount: Int,
) {
    companion object {
        fun fromJson(o: JSONObject): ChatActionAppliedResponse = ChatActionAppliedResponse(
            chat = o.optJSONObject("chat")?.let(ChatEntry::fromJson)
                ?: ChatEntry.fromJson(JSONObject()),
            action = o.optStringOrEmpty("action"),
            deletedCount = o.optInt("deleted_count", 0),
        )
    }
}


data class NewCircuitForPeerRequest(val peerId: String) {
    fun toJson(): JSONObject = JSONObject().apply { put("peer_id", peerId) }
}

data class NewCircuitClosedResponse(val peerId: String, val closed: Int) {
    companion object {
        fun fromJson(o: JSONObject): NewCircuitClosedResponse = NewCircuitClosedResponse(
            peerId = o.optStringOrEmpty("peer_id"),
            closed = o.optInt("closed", 0),
        )
    }
}


data class PeerSelfProbeRequest(val peerId: String) {
    fun toJson(): JSONObject = JSONObject().apply { put("peer_id", peerId) }
}


data class PeerSelfReachPayload(
    val peerId: String,
    val onion: String,
    val ok: Boolean,
    val at: Long,
) {
    companion object {
        fun fromJson(o: JSONObject): PeerSelfReachPayload = PeerSelfReachPayload(
            peerId = o.optStringOrEmpty("peer_id"),
            onion = o.optStringOrEmpty("onion"),
            ok = o.optBoolean("ok", false),
            at = o.optLong("at", 0L),
        )
    }
}


data class ExternalProbeBurstRequest(val placeholder: Unit = Unit) {
    fun toJson(): JSONObject = JSONObject()
}


data class ExternalReachPayload(
    val ok: Boolean,
    val lastTarget: String,
    val at: Long,
) {
    companion object {
        fun fromJson(o: JSONObject): ExternalReachPayload = ExternalReachPayload(
            ok = o.optBoolean("ok", false),
            lastTarget = o.optStringOrEmpty("last_target"),
            at = o.optLong("at", 0L),
        )
    }
}


data class RotateBeginRequest(val peerId: String) {
    fun toJson(): JSONObject = JSONObject().apply { put("peer_id", peerId) }
}

data class RotateBegunResponse(
    val rotationId: String,
    val peerId: String,
    val deadlineAt: Long,
) {
    companion object {
        fun fromJson(o: JSONObject): RotateBegunResponse = RotateBegunResponse(
            rotationId = o.optStringOrEmpty("rotation_id"),
            peerId = o.optStringOrEmpty("peer_id"),
            deadlineAt = o.optLong("deadline_at", 0L),
        )
    }
}


data class RotateLifecyclePush(
    val rotationId: String,
    val peerId: String,
    val role: String,
    val state: String,
    val reason: String,
) {
    companion object {
        fun fromJson(o: JSONObject): RotateLifecyclePush = RotateLifecyclePush(
            rotationId = o.optStringOrEmpty("rotation_id"),
            peerId = o.optStringOrEmpty("peer_id"),
            role = o.optStringOrEmpty("role"),
            state = o.optStringOrEmpty("state"),
            reason = o.optStringOrEmpty("reason"),
        )
    }
}


object CallAction {
    const val Accept = "accept"
    const val Reject = "reject"
    const val End = "end"
}


object CallControlAction {
    const val Mute = "mute"
    const val Unmute = "unmute"
}

data class CallControlRequest(val callId: String, val action: String) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("call_id", callId)
        put("action", action)
    }
}

data class CallControlledResponse(val callId: String, val action: String) {
    companion object {
        fun fromJson(o: JSONObject): CallControlledResponse = CallControlledResponse(
            callId = o.optStringOrEmpty("call_id"),
            action = o.optStringOrEmpty("action"),
        )
    }
}


data class CallStreamEventPayload(
    val callId: String,
    val side: String,       
    val type: String,       
    val jitterMs: Double,
    val framesIn: Long,
    val framesOut: Long,
    val framesDropped: Long,
    val reason: String,
) {
    companion object {
        fun fromJson(o: JSONObject): CallStreamEventPayload = CallStreamEventPayload(
            callId = o.optStringOrEmpty("call_id"),
            side = o.optStringOrEmpty("side"),
            type = o.optStringOrEmpty("type"),
            jitterMs = o.optDouble("jitter_ms", 0.0),
            framesIn = o.optLong("frames_in", 0L),
            framesOut = o.optLong("frames_out", 0L),
            framesDropped = o.optLong("frames_dropped", 0L),
            reason = o.optStringOrEmpty("reason"),
        )
    }
}


data class CallStreamSide(
    val lastSampleAtMs: Long,
    val framesOut: Long,
    val prevFramesOut: Long,
    val jitterMs: Double,
)

data class CallStreamState(
    val mic: CallStreamSide? = null,
    val spk: CallStreamSide? = null,
    val dropped: Long = 0L,
)


object CallDirection {
    const val In = "in"
    const val Out = "out"
}


object CallStatus {
    const val Offered = "offered"
    const val Ringing = "ringing"
    const val Accepted = "accepted"
    const val Rejected = "rejected"
    const val Ended = "ended"
    const val Failed = "failed"
}

data class StartCallRequest(val chatId: String) {
    fun toJson(): JSONObject = JSONObject().apply { put("chat_id", chatId) }
}

data class CallStartedResponse(val callId: String, val call: CallEntry) {
    companion object {
        fun fromJson(o: JSONObject): CallStartedResponse = CallStartedResponse(
            callId = o.optStringOrEmpty("call_id"),
            call = o.optJSONObject("call")?.let(CallEntry::fromJson) ?: CallEntry.EMPTY,
        )
    }
}

data class RespondCallRequest(
    val callId: String,
    val action: String,
    val reason: String = "",
) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("call_id", callId)
        put("action", action)
        if (reason.isNotEmpty()) put("reason", reason)
    }
}

data class CallRespondedResponse(val callId: String, val status: String) {
    companion object {
        fun fromJson(o: JSONObject): CallRespondedResponse = CallRespondedResponse(
            callId = o.optStringOrEmpty("call_id"),
            status = o.optStringOrEmpty("status"),
        )
    }
}

data class CallStateChangedPayload(val call: CallEntry) {
    companion object {
        fun fromJson(o: JSONObject): CallStateChangedPayload = CallStateChangedPayload(
            call = o.optJSONObject("call")?.let(CallEntry::fromJson) ?: CallEntry.EMPTY,
        )
    }
}


data class CallEntry(
    val callId: String,
    val chatId: String,
    val peerId: String,
    val direction: String,
    val status: String,
    val modalities: List<String>,
    val startedAt: Long,
    val updatedAt: Long,
    val endedAt: Long,
    val failReason: String,
) {
    val isTerminal: Boolean
        get() = status == CallStatus.Rejected ||
            status == CallStatus.Ended ||
            status == CallStatus.Failed

    companion object {
        val EMPTY = CallEntry(
            callId = "",
            chatId = "",
            peerId = "",
            direction = "",
            status = "",
            modalities = emptyList(),
            startedAt = 0L,
            updatedAt = 0L,
            endedAt = 0L,
            failReason = "",
        )

        fun fromJson(o: JSONObject): CallEntry = CallEntry(
            callId = o.optStringOrEmpty("call_id"),
            chatId = o.optStringOrEmpty("chat_id"),
            peerId = o.optStringOrEmpty("peer_id"),
            direction = o.optStringOrEmpty("direction"),
            status = o.optStringOrEmpty("status"),
            modalities = o.optStringList("modalities"),
            startedAt = o.optLong("started_at", 0L),
            updatedAt = o.optLong("updated_at", 0L),
            endedAt = o.optLong("ended_at", 0L),
            failReason = o.optStringOrEmpty("fail_reason"),
        )
    }
}


data class NotificationEmittedPayload(
    val chatId: String,
    val peerLabel: String,
    val title: String,
    val body: String,
    val redactedSender: Boolean,
    val redactedBody: Boolean,
) {
    companion object {
        fun fromJson(o: JSONObject): NotificationEmittedPayload = NotificationEmittedPayload(
            chatId = o.optStringOrEmpty("chat_id"),
            peerLabel = o.optStringOrEmpty("peer_label"),
            title = o.optStringOrEmpty("title"),
            body = o.optStringOrEmpty("body"),
            redactedSender = o.optBoolean("redacted_sender", false),
            redactedBody = o.optBoolean("redacted_body", false),
        )
    }
}


data class ClientLockStateRequest(
    val softLocked: Boolean,
) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("soft_locked", softLocked)
    }
}


data class SettingsSnapshot(
    val defaultRetentionSec: Long,
    val defaultSendReceipts: Boolean,
    val idleAction: String,
    val idleTimeoutSeconds: Int,
    val pinValiditySec: Int,
    val notifyShellEnabled: Boolean,
    val notifyShowSender: Boolean,
    val notifyShowBody: Boolean,
    val notificationsOnLock: Boolean,
    val threatProfile: String,
    val panicAction: String,
    val hasTorPassword: Boolean,
    val defaultSaveDir: String,
    val defaultAttachStartDir: String,
) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("default_retention_sec", defaultRetentionSec)
        put("default_send_receipts", defaultSendReceipts)
        if (idleAction.isNotEmpty()) put("idle_action", idleAction)
        if (idleTimeoutSeconds > 0) put("idle_timeout_seconds", idleTimeoutSeconds)
        put("pin_validity_sec", pinValiditySec)
        put("notify_shell_enabled", notifyShellEnabled)
        put("notify_show_sender", notifyShowSender)
        put("notify_show_body", notifyShowBody)
        put("notifications_on_lock", notificationsOnLock)
        if (threatProfile.isNotEmpty()) put("threat_profile", threatProfile)
        if (panicAction.isNotEmpty()) put("panic_action", panicAction)
        put("has_tor_password", hasTorPassword)
        if (defaultSaveDir.isNotEmpty()) put("default_save_dir", defaultSaveDir)
        if (defaultAttachStartDir.isNotEmpty()) put("default_attach_start_dir", defaultAttachStartDir)
    }
}

data class SyncSettingsRequest(
    val settings: SettingsSnapshot,
) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("settings", settings.toJson())
    }
}


data class SendFileRequest(val peerId: String, val path: String) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("peer_id", peerId)
        put("path", path)
    }
}

data class SendFileResponse(
    val envelopeId: String,
    val msgId: String,
    val senderSeq: Long,
    val name: String,
    val size: Long,
    val mime: String,
) {
    companion object {
        fun fromJson(o: JSONObject): SendFileResponse = SendFileResponse(
            envelopeId = o.optStringOrEmpty("envelope_id"),
            msgId = o.optStringOrEmpty("msg_id"),
            senderSeq = o.optLong("sender_seq", 0L),
            name = o.optStringOrEmpty("name"),
            size = o.optLong("size", 0L),
            mime = o.optStringOrEmpty("mime"),
        )
    }
}

data class ListFilesRequest(val chatId: String) {
    fun toJson(): JSONObject = JSONObject().apply { put("chat_id", chatId) }
}

data class FileEntry(
    val msgId: String,
    val direction: String,
    val originalName: String,
    val mime: String,
    val size: Long,
    val displayTs: Long,
    val state: String,
    val deliveryState: String,
    val deletable: Boolean,
) {
    val isImage: Boolean get() = mime.startsWith("image/", ignoreCase = true)
    val isReady: Boolean get() = state == FileState.READY
    val isInbound: Boolean get() = direction == EventDirection.IN
    val isOutbound: Boolean get() = direction == EventDirection.OUT

    companion object {
        fun fromJson(o: JSONObject): FileEntry = FileEntry(
            msgId = o.optStringOrEmpty("msg_id"),
            direction = o.optStringOrEmpty("direction"),
            originalName = o.optStringOrEmpty("original_name"),
            mime = o.optStringOrEmpty("mime"),
            size = o.optLong("size", 0L),
            displayTs = o.optLong("display_ts", 0L),
            state = o.optStringOrEmpty("state"),
            deliveryState = o.optStringOrEmpty("delivery_state"),
            deletable = o.optBoolean("deletable", false),
        )
    }
}

object FileState {
    const val PENDING = "pending"
    const val DOWNLOADING = "downloading"
    const val AWAITING_KEY = "awaiting_key"
    const val READY = "ready"
    const val FAILED_TRANSIENT = "failed_transient"
    const val FAILED_PERMANENT = "failed_permanent"
    const val EXPIRED = "expired"
}

data class FilesListResponse(val chatId: String, val files: List<FileEntry>) {
    companion object {
        fun fromJson(o: JSONObject): FilesListResponse {
            val arr = o.optJSONArray("files")
            val files = if (arr == null) emptyList() else buildList {
                for (i in 0 until arr.length()) {
                    arr.optJSONObject(i)?.let { add(FileEntry.fromJson(it)) }
                }
            }
            return FilesListResponse(
                chatId = o.optStringOrEmpty("chat_id"),
                files = files,
            )
        }
    }
}

data class SaveFileRequest(val chatId: String, val msgId: String, val destDir: String) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("chat_id", chatId)
        put("msg_id", msgId)
        put("dest_dir", destDir)
    }
}

data class SaveFileResponse(val fullPath: String) {
    companion object {
        fun fromJson(o: JSONObject): SaveFileResponse =
            SaveFileResponse(fullPath = o.optStringOrEmpty("full_path"))
    }
}

data class OpenFileRequest(val chatId: String, val msgId: String) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("chat_id", chatId)
        put("msg_id", msgId)
    }
}

data class OpenFileReadyResponse(
    val fullPath: String,
    val sniffedMime: String,
    val mimeMatches: Boolean,
) {
    companion object {
        fun fromJson(o: JSONObject): OpenFileReadyResponse = OpenFileReadyResponse(
            fullPath = o.optStringOrEmpty("full_path"),
            sniffedMime = o.optStringOrEmpty("sniffed_mime"),
            mimeMatches = o.optBoolean("mime_matches", false),
        )
    }
}

data class FileProgressPayload(
    val chatId: String,
    val msgId: String,
    val bytesReceived: Long,
    val totalBytes: Long,
) {
    companion object {
        fun fromJson(o: JSONObject): FileProgressPayload = FileProgressPayload(
            chatId = o.optStringOrEmpty("chat_id"),
            msgId = o.optStringOrEmpty("msg_id"),
            bytesReceived = o.optLong("bytes_received", 0L),
            totalBytes = o.optLong("total_bytes", 0L),
        )
    }
}


data class OpenResult(
    val path: String,
    val sniffedMime: String,
    val matches: Boolean,
)


data class FileEventBody(
    val name: String,
    val size: Long,
    val mime: String,
    val state: String,
    val bytesReceived: Long,
    val lastError: String,
) {
    val isImage: Boolean get() = mime.startsWith("image/", ignoreCase = true)

    companion object {
        val EMPTY = FileEventBody(
            name = "",
            size = 0L,
            mime = "",
            state = "",
            bytesReceived = 0L,
            lastError = "",
        )

        fun fromJson(o: JSONObject?): FileEventBody {
            if (o == null) return EMPTY
            return FileEventBody(
                name = o.optStringOrEmpty("name"),
                size = o.optLong("size", 0L),
                mime = o.optStringOrEmpty("mime"),
                state = o.optStringOrEmpty("state"),
                bytesReceived = o.optLong("bytes_received", 0L),
                lastError = o.optStringOrEmpty("last_error"),
            )
        }
    }
}


fun humanBytes(bytes: Long): String {
    if (bytes < 1024) return "$bytes B"
    val units = arrayOf("KB", "MB", "GB")
    var v = bytes.toDouble() / 1024.0
    var i = 0
    while (v >= 1024.0 && i < units.lastIndex) {
        v /= 1024.0
        i++
    }
    return if (v >= 10.0) "${v.toInt()} ${units[i]}" else "%.1f ${units[i]}".format(v)
}
