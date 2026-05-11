package io.haoma.calculator.messenger

import android.content.Context
import io.haoma.calculator.core.DisguiseStore
import io.haoma.calculator.core.VaultSession
import io.haoma.calculator.core.ipc.IpcClient
import io.haoma.calculator.log.Logger
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancelChildren
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.drop
import kotlinx.coroutines.flow.launchIn
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.onEach
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking


class MessengerStore(
    private val clientName: String = "haoma-android",
    private val clientVersion: String = "",
    internal val vaultSessionProvider: () -> VaultSession? = { null },
    internal val disguise: DisguiseStore? = null,
    internal val notificationPoster: io.haoma.calculator.notifications.NotificationPoster? = null,
    internal val appContext: Context? = null,
) {
    internal val scope = CoroutineScope(
        SupervisorJob() + Dispatchers.Default + Logger.coroutineExceptionHandler,
    )

    
    internal val _peers = MutableStateFlow<List<PeerEntry>>(emptyList())
    val peers: StateFlow<List<PeerEntry>> = _peers.asStateFlow()

    internal val _chats = MutableStateFlow<List<ChatEntry>>(emptyList())
    val chats: StateFlow<List<ChatEntry>> = _chats.asStateFlow()

    internal val _presence = MutableStateFlow<Map<String, String>>(emptyMap())
    val presence: StateFlow<Map<String, String>> = _presence.asStateFlow()

    internal val _health = MutableStateFlow(SystemHealth.INITIAL)
    val health: StateFlow<SystemHealth> = _health.asStateFlow()

    internal val _statusLog = MutableStateFlow<List<StatusLine>>(emptyList())
    val statusLog: StateFlow<List<StatusLine>> = _statusLog.asStateFlow()

    internal val _backStack = MutableStateFlow<List<Screen>>(listOf(Screen.Tabbed(Tab.Chats)))
    val backStack: StateFlow<List<Screen>> = _backStack.asStateFlow()

    internal val _connection = MutableStateFlow(false)
    val connection: StateFlow<Boolean> = _connection.asStateFlow()

    internal val _pendingInvites = MutableStateFlow<List<PendingInvite>>(emptyList())
    val pendingInvites: StateFlow<List<PendingInvite>> = _pendingInvites.asStateFlow()

    internal val _recentInvites = MutableStateFlow<List<RecentInvite>>(emptyList())
    val recentInvites: StateFlow<List<RecentInvite>> = _recentInvites.asStateFlow()

    
    internal val _imagePathByMsgId = MutableStateFlow<Map<String, String>>(emptyMap())
    val imagePathByMsgId: StateFlow<Map<String, String>> = _imagePathByMsgId.asStateFlow()

    
    internal val _imageDimsByMsgId = MutableStateFlow<Map<String, Pair<Int, Int>>>(emptyMap())
    val imageDimsByMsgId: StateFlow<Map<String, Pair<Int, Int>>> = _imageDimsByMsgId.asStateFlow()

    
    internal val _timelines = MutableStateFlow<Map<String, TimelineCache>>(emptyMap())

    
    private val envelopeIndex = HashMap<String, String>()

    
    val current: StateFlow<Screen> = _backStack
        .map { it.lastOrNull() ?: Screen.Tabbed(Tab.Chats) }
        .stateIn(scope, SharingStarted.Eagerly, _backStack.value.last())

    
    @Volatile internal var ipc: IpcClient? = null

    
    @Volatile internal var currentFocusChatId: String = ""

    @Volatile
    internal var lastSoftLocked: Boolean = false

    
    fun onDaemonsReady(haomaAddr: String, frontendDir: File) {
        if (ipc != null) {
            Logger.w("messenger", "onDaemonsReady called while ipc already attached; ignoring")
            return
        }
        appendStatus("daemons up; dialing IPC at wss://$haomaAddr/ws")
        val client = IpcClient(
            frontendDir = frontendDir,
            addr = haomaAddr,
            clientName = clientName,
            clientVersion = clientVersion,
            scope = scope,
        )
        ipc = client

        client.incoming
            .onEach { dispatch(it) }
            .launchIn(scope)

        client.connection
            .drop(1) 
            .distinctUntilChanged()
            .onEach { up ->
                _connection.value = up
                if (up) {
                    appendStatus("ipc connected — subscribing")
                    bootstrapAfterConnect()
                } else {
                    appendStatus("ipc disconnected; reconnecting")
                }
            }
            .launchIn(scope)

        client.connect()
    }

    fun onDaemonsStopped() {
        val client = ipc ?: return
        ipc = null
        appendStatus("daemons stopping; closing IPC")
        
        
        try {
            runBlocking { client.close() }
        } catch (t: Throwable) {
            Logger.e("messenger", "ipc close on teardown", t)
        }
        scope.coroutineContext.cancelChildren()
        _connection.value = false
        
        
        notificationPoster?.cancelAll()
        
        
        lastSoftLocked = false
        
        
    }

    
    private fun bootstrapAfterConnect() {
        scope.launch {
            val c = ipc ?: return@launch
            try {
                val sub = c.request(
                    type = io.haoma.calculator.core.ipc.FrameType.Subscribe,
                    payload = SubscribeRequest().toJson(),
                )
                val topics = sub.payload?.let { SubscribedResponse.fromJson(it).topics } ?: emptyList()
                appendStatus(
                    "subscribed — topics=" + if (topics.isEmpty()) "(all)" else topics.joinToString(","),
                )
                
                
                emitLockState(lastSoftLocked)
                
                
                pushSettingsSync()
                fetchPeers(c)
                fetchChats(c)
                fetchTorInfo(c)
            } catch (t: Throwable) {
                Logger.w("messenger", "bootstrap failed: ${t.message}")
                appendStatus("bootstrap failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
            }
        }
    }

    private suspend fun fetchPeers(c: IpcClient) {
        try {
            val reply = c.request(type = io.haoma.calculator.core.ipc.FrameType.ListPeers)
            val peers = reply.payload?.peerArray("peers").orEmpty()
            _peers.value = peers
            appendStatus("peers snapshot: ${peers.size} row(s)")
        } catch (t: Throwable) {
            appendStatus("list_peers failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
        }
    }

    private suspend fun fetchChats(c: IpcClient) {
        try {
            val reply = c.request(type = io.haoma.calculator.core.ipc.FrameType.ListChats)
            val chats = reply.payload?.chatArray("chats").orEmpty()
            _chats.value = chats
            appendStatus("chats snapshot: ${chats.size} row(s)")
        } catch (t: Throwable) {
            appendStatus("list_chats failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
        }
    }

    private suspend fun fetchTorInfo(c: IpcClient) {
        try {
            val reply = c.request(type = io.haoma.calculator.core.ipc.FrameType.TorInfo)
            val info = reply.payload?.let(TorInfoResponse::fromJson) ?: return
            _health.update {
                it.copy(
                    tor = info.health,
                    onionCount = info.slots.size,
                )
            }
        } catch (t: Throwable) {
            appendStatus("tor_info failed: ${t.message ?: "?"}", level = StatusLevel.WARN)
        }
    }

    
    internal fun upsertPeer(p: PeerEntry) {
        _peers.update { list ->
            val idx = list.indexOfFirst { it.id == p.id }
            if (idx < 0) list + p else list.toMutableList().apply { this[idx] = p }
        }
        if (p.effective.isNotEmpty()) {
            _presence.update { it + (p.id to p.effective) }
        }
    }

    internal fun upsertChat(c: ChatEntry) {
        _chats.update { list ->
            val idx = list.indexOfFirst { it.chatId == c.chatId }
            if (idx < 0) list + c else list.toMutableList().apply { this[idx] = c }
        }
    }

    internal fun upsertTimeline(chatId: String, transform: (TimelineCache) -> TimelineCache) {
        _timelines.update { map ->
            val current = map[chatId] ?: TimelineCache(chatId = chatId)
            val next = transform(current)
            if (next === current) map else map + (chatId to next)
        }
    }

    internal fun mapPeer(peerId: String, transform: (PeerEntry) -> PeerEntry) {
        _peers.update { list -> list.map { if (it.id == peerId) transform(it) else it } }
    }

    internal fun mapChat(chatId: String, transform: (ChatEntry) -> ChatEntry) {
        _chats.update { list -> list.map { if (it.chatId == chatId) transform(it) else it } }
    }

    
    internal fun peerIdOrWarn(chatId: String, verb: String): String? {
        val pid = _chats.value.firstOrNull { it.chatId == chatId }?.peerId.orEmpty()
        if (pid.isEmpty()) {
            appendStatus("$verb: chat not found ($chatId)", level = StatusLevel.WARN)
            return null
        }
        return pid
    }

    internal fun rememberEnvelope(envelopeId: String, chatId: String) {
        synchronized(envelopeIndex) { envelopeIndex[envelopeId] = chatId }
    }

    internal fun lookupEnvelope(envelopeId: String): String? =
        synchronized(envelopeIndex) { envelopeIndex[envelopeId] }

    internal fun forgetEnvelopesFor(chatId: String) {
        synchronized(envelopeIndex) {
            val drop = envelopeIndex.entries.filter { it.value == chatId }.map { it.key }
            for (k in drop) envelopeIndex.remove(k)
        }
    }

    internal fun indexEnvelopes(chatId: String, events: List<TimelineEvent>) {
        synchronized(envelopeIndex) {
            for (ev in events) {
                if (ev.isOutbound && ev.envelopeId.isNotEmpty()) {
                    envelopeIndex[ev.envelopeId] = chatId
                }
            }
        }
    }

    
    internal fun movePendingToRecent(
        handleId: String,
        outcome: RecentOutcome,
        peerId: String = "",
        nick: String = "",
        reason: String = "",
    ) {
        val pending = _pendingInvites.value.firstOrNull { it.handleId == handleId } ?: return
        _pendingInvites.update { list -> list.filterNot { it.handleId == handleId } }
        val entry = RecentInvite(
            handleId = handleId,
            alias = pending.alias,
            outcome = outcome,
            peerId = peerId,
            nick = nick,
            reason = reason,
            at = System.currentTimeMillis(),
        )
        _recentInvites.update { list ->
            val next = listOf(entry) + list
            if (next.size > RECENT_INVITES_CAP) next.take(RECENT_INVITES_CAP) else next
        }
    }

    
    internal fun appendStatus(text: String, level: StatusLevel = StatusLevel.INFO) {
        val line = StatusLine(at = System.currentTimeMillis(), text = text, level = level)
        _statusLog.update { list ->
            val next = list + line
            
            
            if (next.size > STATUS_LOG_CAP) next.takeLast(STATUS_LOG_CAP) else next
        }
    }

    internal fun shortChat(id: String): String =
        if (id.length > 8) id.substring(0, 8) else id

    companion object {
        private const val STATUS_LOG_CAP = 500
        private const val RECENT_INVITES_CAP = 5
    }
}


data class SystemHealth(
    val backendReachable: Boolean,
    val tor: TorHealth,
    val onionCount: Int,
    val selfNick: String,
    val selfNickIsDefault: Boolean,
    val daemonVersion: String,
    val protocolVersion: Int,
) {
    companion object {
        val INITIAL = SystemHealth(
            backendReachable = false,
            tor = TorHealth.ZERO,
            onionCount = 0,
            selfNick = "",
            selfNickIsDefault = true,
            daemonVersion = "",
            protocolVersion = 0,
        )
    }
}

enum class StatusLevel { INFO, WARN }

data class StatusLine(
    val at: Long,
    val text: String,
    val level: StatusLevel,
) {
    fun stamp(): String = TS_FMT.format(Date(at))

    companion object {
        private val TS_FMT = SimpleDateFormat("HH:mm:ss", Locale.US)
    }
}
