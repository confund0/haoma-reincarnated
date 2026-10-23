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
    val chatFontScale: Float,
    
    val defaultRetentionSec: Long,
    val defaultSendReceipts: Boolean,
) {
    companion object {
        fun fromJson(o: JSONObject): WelcomePayload = WelcomePayload(
            daemonVersion = o.optStringOrEmpty("daemon_version"),
            protocolVersion = o.optInt("protocol_version", 0),
            selfNick = o.optStringOrEmpty("self_nick"),
            selfNickIsDefault = o.optBoolean("self_nick_is_default", false),
            
            
            chatFontScale = o.optDouble("chat_font_scale", 1.0).toFloat(),
            
            
            defaultRetentionSec = o.optLong("default_retention_sec", DEFAULT_RETENTION_SEC_FALLBACK),
            defaultSendReceipts = o.optBoolean("default_send_receipts", true),
        )
    }
}


const val DEFAULT_RETENTION_SEC_FALLBACK: Long = 28L * 24 * 3600

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
    val tor: SystemInfoComponent,
) {
    companion object {
        fun fromJson(o: JSONObject): SystemInfoResponse = SystemInfoResponse(
            haoma = o.optJSONObject("haoma")?.let { SystemInfoComponent.fromJson(it) } ?: SystemInfoComponent.EMPTY,
            haomad = o.optJSONObject("haomad")?.let { SystemInfoComponent.fromJson(it) } ?: SystemInfoComponent.EMPTY,
            tor = o.optJSONObject("tor")?.let { SystemInfoComponent.fromJson(it) } ?: SystemInfoComponent.EMPTY,
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
    
    val sending: Boolean = false,
    val chatty: String,
    val effective: String,
    

    val sibling: Boolean,
    

    val unreachable: Boolean,
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
            sending = o.optBoolean("sending", false),
            chatty = o.optStringOrEmpty("chatty"),
            effective = o.optStringOrEmpty("effective"),
            sibling = o.optBoolean("sibling", false),
            unreachable = o.optBoolean("unreachable", false),
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
    val nickOverride: String,
    val members: List<String>,
    val createdAt: Long,
    val lastActivityAt: Long,
    val unreadCount: Long,
    
    val accepting: Boolean,
    
    val sending: Boolean = false,
    val chatty: String,
    val effective: String,
    

    val waitingForSession: Boolean = false,
    val waitingForContact: Boolean = false,
    val chatStartedAt: Long = 0L,
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
            nickOverride = o.optStringOrEmpty("nick_override"),
            members = o.optStringList("members"),
            createdAt = o.optLong("created_at", 0L),
            lastActivityAt = o.optLong("last_activity_at", 0L),
            unreadCount = o.optLong("unread_count", 0L),
            accepting = o.optBoolean("accepting", false),
            sending = o.optBoolean("sending", false),
            chatty = o.optStringOrEmpty("chatty"),
            effective = o.optStringOrEmpty("effective"),
            waitingForSession = o.optBoolean("waiting_for_session", false),
            waitingForContact = o.optBoolean("waiting_for_contact", false),
            chatStartedAt = o.optLong("chat_started_at", 0L),
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
    val sending: Boolean,
    val chatty: String,
    val effective: String,
) {
    companion object {
        fun fromJson(o: JSONObject): PresenceChangedPayload = PresenceChangedPayload(
            peerId = o.optStringOrEmpty("peer_id"),
            accepting = o.optBoolean("accepting", false),
            sending = o.optBoolean("sending", false),
            chatty = o.optStringOrEmpty("chatty"),
            effective = o.optStringOrEmpty("effective"),
        )
    }
}


data class ChatPresenceChangedPayload(
    val chatId: String,
    val accepting: Boolean,
    val sending: Boolean,
    val chatty: String,
    val effective: String,
) {
    companion object {
        fun fromJson(o: JSONObject): ChatPresenceChangedPayload = ChatPresenceChangedPayload(
            chatId = o.optStringOrEmpty("chat_id"),
            accepting = o.optBoolean("accepting", false),
            sending = o.optBoolean("sending", false),
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


data class SetChatFontScaleRequest(val scale: Float) {
    fun toJson(): JSONObject = JSONObject().apply { put("scale", scale.toDouble()) }
}

data class ChatFontScalePayload(val scale: Float) {
    companion object {
        fun fromJson(o: JSONObject): ChatFontScalePayload = ChatFontScalePayload(
            scale = o.optDouble("scale", 1.0).toFloat(),
        )
    }
}


data class SetChatDefaultsRequest(val retentionSec: Long, val sendReceipts: Boolean) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("retention_sec", retentionSec)
        put("send_receipts", sendReceipts)
    }
}

data class ChatDefaultsPayload(val retentionSec: Long, val sendReceipts: Boolean) {
    companion object {
        fun fromJson(o: JSONObject): ChatDefaultsPayload = ChatDefaultsPayload(
            retentionSec = o.optLong("retention_sec", DEFAULT_RETENTION_SEC_FALLBACK),
            sendReceipts = o.optBoolean("send_receipts", true),
        )
    }
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


class DeviceLinkOfferRequest {
    fun toJson(): JSONObject = JSONObject()
}


data class DeviceLinkStartedResponse(
    val handleId: String,
    val words: List<String>,
    val expiresAt: Long,
) {
    companion object {
        fun fromJson(o: JSONObject): DeviceLinkStartedResponse = DeviceLinkStartedResponse(
            handleId = o.optStringOrEmpty("handle_id"),
            words = o.optStringList("words"),
            expiresAt = o.optLong("expires_at", 0L),
        )
    }
}


data class DeviceLinkProbePush(
    val handleId: String,
    val attempt: Int,
    val ready: Boolean,
    val error: String,
    val at: Long,
) {
    companion object {
        fun fromJson(o: JSONObject): DeviceLinkProbePush = DeviceLinkProbePush(
            handleId = o.optStringOrEmpty("handle_id"),
            attempt = o.optInt("attempt", 0),
            ready = o.optBoolean("ready", false),
            error = o.optStringOrEmpty("error"),
            at = o.optLong("at", 0L),
        )
    }
}


data class DeviceLinkAcceptRequest(val words: List<String>) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("words", stringListJson(words))
    }
}


data class DeviceLinkAcceptedResponse(
    val peerId: String,
    val adoptedFingerprint: String,
    val contactsCleared: Boolean,
) {
    companion object {
        fun fromJson(o: JSONObject): DeviceLinkAcceptedResponse = DeviceLinkAcceptedResponse(
            peerId = o.optStringOrEmpty("peer_id"),
            adoptedFingerprint = o.optStringOrEmpty("adopted_fingerprint"),
            contactsCleared = o.optBoolean("contacts_cleared", false),
        )
    }
}


data class DeviceLinkCompletedPush(
    val handleId: String,
    val peerId: String,
) {
    companion object {
        fun fromJson(o: JSONObject): DeviceLinkCompletedPush = DeviceLinkCompletedPush(
            handleId = o.optStringOrEmpty("handle_id"),
            peerId = o.optStringOrEmpty("peer_id"),
        )
    }
}


data class DeviceLinkFailedPush(
    val handleId: String,
    val reason: String,
    val detail: String,
) {
    companion object {
        fun fromJson(o: JSONObject): DeviceLinkFailedPush = DeviceLinkFailedPush(
            handleId = o.optStringOrEmpty("handle_id"),
            reason = o.optStringOrEmpty("reason"),
            detail = o.optStringOrEmpty("detail"),
        )
    }
}


data class DeviceLinkCancelRequest(val handleId: String) {
    fun toJson(): JSONObject = JSONObject().apply { put("handle_id", handleId) }
}


class DeviceLinksListRequest {
    fun toJson(): JSONObject = JSONObject()
}


data class DeviceLinkEntry(
    val peerId: String,
    val linkedAt: Long,
) {
    companion object {
        fun fromJson(o: JSONObject): DeviceLinkEntry = DeviceLinkEntry(
            peerId = o.optStringOrEmpty("peer_id"),
            linkedAt = o.optLong("linked_at", 0L),
        )
    }
}


data class DeviceLinksListedResponse(val devices: List<DeviceLinkEntry>) {
    companion object {
        fun fromJson(o: JSONObject): DeviceLinksListedResponse =
            DeviceLinksListedResponse(devices = o.deviceLinkArray("devices"))
    }
}

internal fun JSONObject.deviceLinkArray(key: String): List<DeviceLinkEntry> {
    val arr = optJSONArray(key) ?: return emptyList()
    return buildList {
        for (i in 0 until arr.length()) {
            arr.optJSONObject(i)?.let { add(DeviceLinkEntry.fromJson(it)) }
        }
    }
}


data class DeviceSyncRequest(val full: Boolean) {
    fun toJson(): JSONObject = JSONObject().apply { put("full", full) }
}


data class DeviceSyncSentResponse(val siblings: Int?, val full: Boolean, val alreadyRunning: Boolean) {
    companion object {
        fun fromJson(o: JSONObject): DeviceSyncSentResponse = DeviceSyncSentResponse(
            siblings = if (o.has("siblings") && !o.isNull("siblings")) o.optInt("siblings", 0) else null,
            full = o.optBoolean("full", false),
            alreadyRunning = o.optBoolean("already_running", false),
        )
    }
}


data class DeviceSyncFinishedPush(
    val full: Boolean,
    val siblings: Int,
    val answered: Int,
    val rowsApplied: Int,
    val timedOut: Boolean,
    val snapshotApplied: Int = 0,
    val held: Int = 0,
    val unreachable: Int = 0,
) {
    
    val changes: Int get() = rowsApplied + snapshotApplied

    companion object {
        fun fromJson(o: JSONObject): DeviceSyncFinishedPush = DeviceSyncFinishedPush(
            full = o.optBoolean("full", false),
            siblings = o.optInt("siblings", 0),
            answered = o.optInt("answered", 0),
            rowsApplied = o.optInt("rows_applied", 0),
            timedOut = o.optBoolean("timed_out", false),
            snapshotApplied = o.optInt("snapshot_applied", 0),
            held = o.optInt("held", 0),
            unreachable = o.optInt("unreachable", 0),
        )
    }
}


data class InheritedContactEntry(
    val fingerprint: String,
    val nick: String = "",
    val alias: String = "",
    val label: String = "",
    val devices: Int = 0,
    val paired: Int = 0,
    val offers: Int = 0,
    val waiting: Boolean = false,
    val invited: Boolean = false,
    val open: Int = 0,
) {
    companion object {
        fun fromJson(o: JSONObject): InheritedContactEntry = InheritedContactEntry(
            fingerprint = o.optStringOrEmpty("fingerprint"),
            nick = o.optStringOrEmpty("nick"),
            alias = o.optStringOrEmpty("alias"),
            label = o.optStringOrEmpty("label"),
            devices = o.optInt("devices", 0),
            paired = o.optInt("paired", 0),
            offers = o.optInt("offers", 0),
            waiting = o.optBoolean("waiting", false),
            invited = o.optBoolean("invited", false),
            open = o.optInt("open", 0),
        )
    }
}


data class InheritedContactsBody(
    val contacts: List<InheritedContactEntry>,
    val linked: Boolean,
) {
    companion object {
        fun fromJson(o: JSONObject): InheritedContactsBody {
            val arr = o.optJSONArray("contacts")
            val contacts = buildList {
                if (arr != null) {
                    for (i in 0 until arr.length()) {
                        val c = arr.optJSONObject(i)?.let(InheritedContactEntry::fromJson) ?: continue
                        
                        
                        if (c.fingerprint.isNotEmpty()) add(c)
                    }
                }
            }
            return InheritedContactsBody(contacts = contacts, linked = o.optBoolean("linked", false))
        }
    }
}


data class InvitationOfferRequest(
    val fingerprint: String,
    val force: Boolean = false,
    val openOnly: Boolean = false,
) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("fingerprint", fingerprint)
        if (force) put("force", true)
        if (openOnly) put("open_only", true)
    }
}


data class InvitationOfferedResponse(
    val msgId: String,
    val expiresAt: Long,
    val siblings: Int,
) {
    companion object {
        fun fromJson(o: JSONObject): InvitationOfferedResponse = InvitationOfferedResponse(
            msgId = o.optStringOrEmpty("msg_id"),
            expiresAt = o.optLong("expires_at", 0L),
            siblings = o.optInt("siblings", 0),
        )
    }
}


data class InvitationAcceptRequest(val msgId: String) {
    fun toJson(): JSONObject = JSONObject().apply { put("msg_id", msgId) }
}


data class InvitationAcceptedResponse(
    val msgId: String,
    val peerId: String,
    val chatId: String,
    val identityFingerprint: String,
) {
    companion object {
        fun fromJson(o: JSONObject): InvitationAcceptedResponse = InvitationAcceptedResponse(
            msgId = o.optStringOrEmpty("msg_id"),
            peerId = o.optStringOrEmpty("peer_id"),
            chatId = o.optStringOrEmpty("chat_id"),
            identityFingerprint = o.optStringOrEmpty("identity_fingerprint"),
        )
    }
}


object InvitationDirection {
    
    const val IN = "in"

    
    const val OUT = "out"
}


object InvitationState {
    const val LIVE = "live"
    const val SUPERSEDED = "superseded"
    const val ACCEPTED = "accepted"
    const val FAILED = "failed"
}


data class InvitationEntry(
    val msgId: String,
    val label: String,
    val direction: String,
    val expiresAt: Long,
    val state: String,
    val fingerprint: String = "",
) {
    

    fun standing(nowSeconds: Long): Boolean =
        state == InvitationState.LIVE && expiresAt > 0L && nowSeconds < expiresAt

    companion object {
        fun fromJson(o: JSONObject): InvitationEntry = InvitationEntry(
            msgId = o.optStringOrEmpty("msg_id"),
            label = o.optStringOrEmpty("label"),
            direction = o.optStringOrEmpty("direction"),
            expiresAt = o.optLong("expires_at", 0L),
            state = o.optStringOrEmpty("state"),
            fingerprint = o.optStringOrEmpty("fingerprint"),
        )
    }
}


data class InvitationsListedResponse(val invitations: List<InvitationEntry>) {
    companion object {
        fun fromJson(o: JSONObject): InvitationsListedResponse {
            val arr = o.optJSONArray("invitations")
            return InvitationsListedResponse(
                invitations = buildList {
                    if (arr != null) {
                        for (i in 0 until arr.length()) {
                            arr.optJSONObject(i)?.let { add(InvitationEntry.fromJson(it)) }
                        }
                    }
                },
            )
        }
    }
}


object DestroyPinSetReason {
    
    const val ALREADY_SET = "already_set"

    
    const val BAD_PIN_SHAPE = "bad_pin_shape"

    
    const val WRONG_DESTROY_PIN = "wrong_destroy_pin"

    
    const val NO_LINKED_DEVICE = "no_linked_device"
}


object DestroyPinVerifiedReason {
    
    const val NO_SALT_FOR_SIBLING = "no_salt_for_sibling"

    
    const val NOT_MY_DEVICE = "not_my_device"

    
    const val ANSWER_DID_NOT_CHECK_OUT = "answer_did_not_check_out"
}


class GetDestroyPinRequest {
    fun toJson(): JSONObject = JSONObject()
}


data class DestroyPinSiblingEntry(
    val peerId: String,
    val saltHeld: Boolean,
    val receivedAt: Long,
) {
    companion object {
        fun fromJson(o: JSONObject): DestroyPinSiblingEntry = DestroyPinSiblingEntry(
            peerId = o.optStringOrEmpty("peer_id"),
            saltHeld = o.optBoolean("salt_held", false),
            receivedAt = o.optLong("received_at", 0L),
        )
    }
}


data class DestroyPinListedResponse(
    val set: Boolean,
    val setAt: Long,
    val siblings: List<DestroyPinSiblingEntry>,
    

    val waiting: DestroyWaiting? = null,
) {
    companion object {
        fun fromJson(o: JSONObject): DestroyPinListedResponse = DestroyPinListedResponse(
            set = o.optBoolean("set", false),
            setAt = o.optLong("set_at", 0L),
            siblings = o.destroyPinSiblingArray("siblings"),
            waiting = o.optJSONObject("waiting")?.let(DestroyWaiting::fromJson),
        )
    }
}


data class DestroyWaiting(
    val peerId: String,
    val deadline: Long,
    val startedAt: Long,
) {
    companion object {
        fun fromJson(o: JSONObject): DestroyWaiting = DestroyWaiting(
            peerId = o.optStringOrEmpty("peer_id"),
            deadline = o.optLong("deadline", 0L),
            startedAt = o.optLong("started_at", 0L),
        )
    }
}

internal fun JSONObject.destroyPinSiblingArray(key: String): List<DestroyPinSiblingEntry> {
    val arr = optJSONArray(key) ?: return emptyList()
    return buildList {
        for (i in 0 until arr.length()) {
            arr.optJSONObject(i)?.let { add(DestroyPinSiblingEntry.fromJson(it)) }
        }
    }
}


data class SetDestroyPinRequest(val newPin: String) {
    fun toJson(): JSONObject = JSONObject().apply { put("new", newPin) }
}


data class DestroyPinSetResponse(
    val accepted: Boolean,
    val reason: String,
    val saltPushed: Int,
    val siblings: Int,
) {
    companion object {
        fun fromJson(o: JSONObject): DestroyPinSetResponse = DestroyPinSetResponse(
            accepted = o.optBoolean("accepted", false),
            reason = o.optStringOrEmpty("reason"),
            saltPushed = o.optInt("salt_pushed", 0),
            siblings = o.optInt("siblings", 0),
        )
    }
}


data class VerifyDestroyPinRequest(
    val peerId: String,
    val pin: String,
) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("peer_id", peerId)
        put("pin", pin)
    }
}


data class DestroyPinVerifiedResponse(
    val answered: Boolean,
    val verified: Boolean,
    val reason: String,
) {
    companion object {
        fun fromJson(o: JSONObject): DestroyPinVerifiedResponse = DestroyPinVerifiedResponse(
            answered = o.optBoolean("answered", false),
            verified = o.optBoolean("verified", false),
            reason = o.optStringOrEmpty("reason"),
        )
    }
}


data class UnlinkDeviceRequest(
    val peerId: String,
    val pin: String = "",
) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("peer_id", peerId)
        if (pin.isNotEmpty()) put("pin", pin)
    }
}


data class DeviceUnlinkedResponse(
    val unlinked: Boolean,
    val sent: Boolean,
    val receipted: Boolean,
    val deadline: Long,
    val reason: String,
) {
    companion object {
        fun fromJson(o: JSONObject): DeviceUnlinkedResponse = DeviceUnlinkedResponse(
            unlinked = o.optBoolean("unlinked", false),
            sent = o.optBoolean("sent", false),
            receipted = o.optBoolean("receipted", false),
            deadline = o.optLong("deadline", 0L),
            reason = o.optStringOrEmpty("reason"),
        )
    }
}


data class DeviceWipingResponse(val started: Boolean) {
    companion object {
        fun fromJson(o: JSONObject): DeviceWipingResponse = DeviceWipingResponse(
            started = o.optBoolean("started", false),
        )
    }
}


data class DestroyDevicePush(val reason: String, val eraseId: String = "") {
    companion object {
        fun fromJson(o: JSONObject): DestroyDevicePush = DestroyDevicePush(
            reason = o.optStringOrEmpty("reason"),
            eraseId = o.optStringOrEmpty("erase_id"),
        )
    }
}


data class DestroyAttemptsSeenPush(
    val count: Int,
    val lastAt: Long,
) {
    companion object {
        fun fromJson(o: JSONObject): DestroyAttemptsSeenPush = DestroyAttemptsSeenPush(
            count = o.optInt("count", 0),
            lastAt = o.optLong("last_at", 0L),
        )
    }
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
    

    const val CALL_SUMMARY = "call_summary"
    

    const val ONION_ROTATION = "onion_rotation"
    

    const val INVITATION = "invitation"
}


object DeliveryState {
    const val PENDING = "pending"
    const val ENQUEUED = "enqueued"
    const val SENT = "sent"
    const val DELIVERED = "delivered"
    const val READ = "read"
    const val FAILED = "failed"

    
    fun rank(state: String): Int? = when (state) {
        "" -> 0
        PENDING, ENQUEUED -> 1
        SENT -> 2
        DELIVERED -> 3
        READ -> 4
        else -> null
    }

    
    fun outranks(cur: String, next: String): Boolean {
        val curRank = rank(cur) ?: return false
        val nextRank = rank(next) ?: return false
        return nextRank > curRank
    }

    
    fun advances(cur: String, next: String): Boolean {
        if (next.isEmpty() || cur == next) return false
        if (cur.isEmpty()) return true
        if (next == FAILED) return cur != DELIVERED && cur != READ
        return outranks(cur, next)
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
    val failReason: String,
    val body: JSONObject?,
    val deliveryState: String,
    val expireSeconds: Int,
    val readAt: Long,
    val editedAt: Long,
    val deletedAt: Long,
    

    val mirroredFromSibling: Boolean = false,
) {
    
    fun bodyTextOrEmpty(): String =
        body?.optStringOrEmpty("text").orEmpty()

    
    fun replyToOrNull(): ReplyToSnapshot? =
        if (kind == EventKind.TEXT && !isTombstoned)
            ReplyToSnapshot.fromJson(body?.optJSONObject("reply_to"))
        else null

    val isOutbound: Boolean get() = direction == EventDirection.OUT
    val isInbound: Boolean get() = direction == EventDirection.IN
    val isTombstoned: Boolean get() = deletedAt > 0L
    val isEdited: Boolean get() = editedAt > 0L

    
    fun isRetryableFile(): Boolean {
        if (kind != EventKind.FILE || isTombstoned) return false
        return FileEventBody.fromJson(body).state == FileState.FAILED_TRANSIENT
    }

    
    fun isReadyImage(): Boolean {
        if (kind != EventKind.FILE || isTombstoned) return false
        val b = FileEventBody.fromJson(body)
        return b.isImage && b.state == FileState.READY
    }

    
    fun isReadyVideo(): Boolean {
        if (kind != EventKind.FILE || isTombstoned) return false
        val b = FileEventBody.fromJson(body)
        return b.isVideo && b.state == FileState.READY
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
            failReason = o.optStringOrEmpty("fail_reason"),
            body = o.optJSONObject("body"),
            deliveryState = o.optStringOrEmpty("delivery_state"),
            expireSeconds = o.optInt("expire_seconds", 0),
            readAt = o.optLong("read_at", 0L),
            editedAt = o.optLong("edited_at", 0L),
            deletedAt = o.optLong("deleted_at", 0L),
            mirroredFromSibling = o.optBoolean("mirrored_from_sibling", false),
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
        
        
        const val DEFAULT_LIMIT = 500
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


data class ChatSearchRequest(
    val chatId: String,
    val query: String,
) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("chat_id", chatId)
        put("query", query)
    }
}


data class ChatSearchMatch(
    val msgId: String,
    val displayTs: Long,
    val bodyOffset: Int,
) {
    companion object {
        fun fromJson(o: JSONObject): ChatSearchMatch = ChatSearchMatch(
            msgId = o.optStringOrEmpty("msg_id"),
            displayTs = o.optLong("display_ts", 0L),
            bodyOffset = o.optInt("body_offset", 0),
        )
    }
}


data class ChatSearchResponse(
    val chatId: String,
    val query: String,
    val matches: List<ChatSearchMatch>,
    val truncated: Boolean,
) {
    companion object {
        fun fromJson(o: JSONObject): ChatSearchResponse {
            val arr = o.optJSONArray("matches")
            val matches = if (arr == null) emptyList() else buildList {
                for (i in 0 until arr.length()) {
                    arr.optJSONObject(i)?.let { add(ChatSearchMatch.fromJson(it)) }
                }
            }
            return ChatSearchResponse(
                chatId = o.optStringOrEmpty("chat_id"),
                query = o.optStringOrEmpty("query"),
                matches = matches,
                truncated = o.optBoolean("truncated", false),
            )
        }
    }
}

data class SendTextRequest(
    val peerId: String,
    val text: String,
    val replyToMsgId: String = "",
) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("peer_id", peerId)
        put("text", text)
        if (replyToMsgId.isNotEmpty()) put("reply_to_msg_id", replyToMsgId)
    }
}


data class ReplyToSnapshot(val msgId: String, val text: String) {
    companion object {
        fun fromJson(o: JSONObject?): ReplyToSnapshot? {
            if (o == null) return null
            val mid = o.optStringOrEmpty("msg_id")
            if (mid.isEmpty()) return null
            return ReplyToSnapshot(
                msgId = mid,
                text = o.optStringOrEmpty("text"),
            )
        }
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
    val nickOverride: String,
) {
    companion object {
        fun fromJson(o: JSONObject): ChatSettingsPayload = ChatSettingsPayload(
            chatId = o.optStringOrEmpty("chat_id"),
            retentionTtl = o.optInt("retention_ttl", 0),
            disableReadReceipts = o.optBoolean("disable_read_receipts", false),
            notificationsMuted = o.optBoolean("notifications_muted", false),
            nickOverride = o.optStringOrEmpty("nick_override"),
        )
    }
}


data class SetChatSettingsRequest(
    val chatId: String,
    val retentionTtl: Int,
    val disableReadReceipts: Boolean,
    val notificationsMuted: Boolean,
    val nickOverride: String,
) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("chat_id", chatId)
        put("retention_ttl", retentionTtl)
        put("disable_read_receipts", disableReadReceipts)
        put("notifications_muted", notificationsMuted)
        put("nick_override", nickOverride)
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
    val ok: Boolean,
    val at: Long,
) {
    companion object {
        fun fromJson(o: JSONObject): PeerSelfReachPayload = PeerSelfReachPayload(
            peerId = o.optStringOrEmpty("peer_id"),
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


const val CallEndReasonAutoHangup = "auto-hangup"


const val CallAnsweredElsewhere = "answered_elsewhere"


object CallControlAction {
    const val Mute = "mute"
    const val Unmute = "unmute"
    const val VideoMute = "video_mute"
    const val VideoUnmute = "video_unmute"
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
    val framesIn: Long,
    val prevFramesIn: Long,
    val jitterMs: Double,
)

data class CallStreamState(
    val mic: CallStreamSide? = null,
    val spk: CallStreamSide? = null,
    val cam: CallStreamSide? = null,
    val vid: CallStreamSide? = null,
)


data class CallStreamRawTransportPayload(
    val callId: String,
    val side: String,    
    val rawUnix: String, 
) {
    companion object {
        fun fromJson(o: JSONObject): CallStreamRawTransportPayload = CallStreamRawTransportPayload(
            callId = o.optStringOrEmpty("call_id"),
            side = o.optStringOrEmpty("side"),
            rawUnix = o.optStringOrEmpty("raw_unix"),
        )
    }
}


data class CallStreamClockSamplePayload(
    val callId: String,
    val localNs: Long,
    val senderPtsNs: Long,
) {
    companion object {
        fun fromJson(o: JSONObject): CallStreamClockSamplePayload = CallStreamClockSamplePayload(
            callId = o.optStringOrEmpty("call_id"),
            localNs = o.optLong("local_ns", 0L),
            senderPtsNs = o.optLong("sender_pts_ns", 0L),
        )
    }
}


data class ClockSample(
    val localNs: Long,
    val senderPtsNs: Long,
    val receivedAtElapsedNs: Long,
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

data class StartCallRequest(
    val chatId: String,
    val modalities: List<String> = emptyList(),
) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("chat_id", chatId)
        if (modalities.isNotEmpty()) {
            put("modalities", org.json.JSONArray().apply { modalities.forEach { put(it) } })
        }
    }
}


object CallModality {
    const val Audio = "audio"
    const val Video = "video"
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
    
    
    val idleAction: String,
    val idleTimeoutSeconds: Int,
    val pinValiditySec: Int,
    val notifyShellEnabled: Boolean,
    val notifyShowSender: Boolean,
    val notifyShowBody: Boolean,
    val notificationsOnLock: Boolean,
    
    val notifyPersistUntilOpen: Boolean,
    val notifyDeepLink: Boolean,
    
    
    val showFailedDestroyAttempts: Boolean,
    val threatProfile: String,
    val panicAction: String,
    val hasTorPassword: Boolean,
    val defaultSaveDir: String,
    val defaultAttachStartDir: String,
) {
    fun toJson(): JSONObject = JSONObject().apply {
        if (idleAction.isNotEmpty()) put("idle_action", idleAction)
        if (idleTimeoutSeconds > 0) put("idle_timeout_seconds", idleTimeoutSeconds)
        put("pin_validity_sec", pinValiditySec)
        put("notify_shell_enabled", notifyShellEnabled)
        put("notify_show_sender", notifyShowSender)
        put("notify_show_body", notifyShowBody)
        put("notifications_on_lock", notificationsOnLock)
        if (notifyPersistUntilOpen) put("notify_persist_until_open", true)
        if (notifyDeepLink) put("notify_deep_link", true)
        if (showFailedDestroyAttempts) put("show_failed_destroy_attempts", true)
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


data class SendFileRequest(
    val peerId: String,
    val path: String,
    val imageMode: String = "",
    val caption: String = "",
) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("peer_id", peerId)
        put("path", path)
        if (imageMode.isNotEmpty()) put("image_mode", imageMode)
        if (caption.isNotEmpty()) put("caption", caption)
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
    val isVideo: Boolean get() = mime.startsWith("video/", ignoreCase = true)
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


data class ImageStreamRequest(val chatId: String, val msgId: String) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("chat_id", chatId)
        put("msg_id", msgId)
    }
}


data class ImageStreamReadyResponse(
    val bytesB64: String,
    val sniffedMime: String,
    val mimeMatches: Boolean,
) {
    companion object {
        fun fromJson(o: JSONObject): ImageStreamReadyResponse = ImageStreamReadyResponse(
            bytesB64 = o.optStringOrEmpty("bytes_b64"),
            sniffedMime = o.optStringOrEmpty("sniffed_mime"),
            mimeMatches = o.optBoolean("mime_matches", false),
        )
    }
}


data class WipeOpenTransientRequest(val msgId: String) {
    fun toJson(): JSONObject = JSONObject().apply {
        put("msg_id", msgId)
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
    val caption: String,
) {
    val isImage: Boolean get() = mime.startsWith("image/", ignoreCase = true)
    val isVideo: Boolean get() = mime.startsWith("video/", ignoreCase = true)

    companion object {
        val EMPTY = FileEventBody(
            name = "",
            size = 0L,
            mime = "",
            state = "",
            bytesReceived = 0L,
            lastError = "",
            caption = "",
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
                caption = o.optStringOrEmpty("caption"),
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
